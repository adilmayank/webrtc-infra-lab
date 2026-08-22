package room

import (
	"log"
	"math"
	"sync"
	"time"

	"github.com/adilmayanktiwari/sfu-refactor/config"
	"github.com/adilmayanktiwari/sfu-refactor/media"
	"github.com/pion/webrtc/v4"
)

// Room holds all peers in a session and manages the forwarding topology.
type Room struct {
	mu sync.RWMutex
	ID string

	peers map[string]*Peer

	// outputTracks maps "localTrackID" → the output track + metadata.
	// Used for subscribing late-joiners to existing streams.
	outputTracks map[string]*OutputTrack

	// metricsOnce ensures the metrics goroutine starts exactly once.
	metricsOnce sync.Once
	stopMetrics chan any

	//	called when last peer leaves
	onEmpty func()
}

// OutputTrack represents a single outbound track in the room —
// either a simple forwarded track or the output of a SimulcastTrack.
type OutputTrack struct {
	Local       *webrtc.TrackLocalStaticRTP
	SenderPeer  *Peer
	RemoteTrack *webrtc.TrackRemote // original source (for PLI targeting)
	Kind        string              // "audio" or "video"
	Stats       *media.TrackStats   // non-nil for simple forwarded tracks; nil for simulcast
}

// NewRoom creates an empty room.
func NewRoom(id string) *Room {
	return &Room{
		ID:           id,
		peers:        make(map[string]*Peer),
		outputTracks: make(map[string]*OutputTrack),
		stopMetrics:  make(chan any),
	}
}

// AddPeer creates a PeerConnection, registers the peer in the room, and
// sets up OnTrack / OnICECandidate / OnConnectionStateChange handlers.
func (r *Room) AddPeer(id string, send func(msg []byte)) (*Peer, error) {
	pc, err := r.createPeerConnection()
	if err != nil {
		return nil, err
	}

	peer := NewPeer(id, pc, send)

	r.mu.Lock()
	r.peers[id] = peer
	r.mu.Unlock()

	r.setupTrackHandler(peer)
	r.setupICEHandler(peer)
	r.setupConnectionStateHandler(peer)

	r.metricsOnce.Do(func() {
		go r.metricsLoop()
	})

	return peer, nil
}

// RemovePeer disconnects the peer, cleans up resources, and notifies others.
func (r *Room) RemovePeer(peerID string) {
	r.mu.Lock()
	peer, exists := r.peers[peerID]
	if !exists {
		r.mu.Unlock()
		return
	}
	delete(r.peers, peerID)

	empty := len(r.peers) == 0

	//	Collect stream IDs of tracks this peer was publishing
	//	(the browser uses these to remove video tiles)
	var streamIDs []string
	for trackID, ot := range r.outputTracks {
		if ot.SenderPeer.ID == peerID {
			streamIDs = append(streamIDs, ot.Local.StreamID())
			delete(r.outputTracks, trackID)
		}
	}

	r.mu.Unlock()

	//	Stop all media goroutines and close the PeerConnection
	peer.Cleanup()

	//	Notify remaining peers
	event := MustJSON(PeerLeftEvent{
		Type:      EventPeerLeft,
		StreamIDs: streamIDs,
	})

	r.mu.RLock()
	for _, p := range r.peers {
		p.Send(event)
	}
	r.mu.RUnlock()

	if empty && r.onEmpty != nil {
		close(r.stopMetrics)
		r.onEmpty()
	}

	log.Printf("[room] peer=%s removed from room=%s (remaining: %d)", peerID, r.ID, len(r.peers))

}

// GetPeer returns a peer by ID, or nil.
func (r *Room) GetPeer(peerID string) *Peer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.peers[peerID]
}

// PeerCount returns the number of connected peers.
func (r *Room) PeerCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.peers)
}

// SwitchLayer changes the simulcast layer for a target peer's tracks.
// If targetPeerID is "*", it switches all simulcast tracks in the room.
func (r *Room) SwitchLayer(targetPeerID string, layer media.LayerID) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, peer := range r.peers {
		if targetPeerID != "*" && peer.ID != targetPeerID {
			continue
		}

		for _, st := range peer.SimulcastTracks() {
			if track := st.SwitchLayer(layer); track != nil {
				// Send PLI burst directly using the returned track
				// (no lookup needed — SwitchLayer gives us exactly the right TrackRemote)
				burst := media.NewPLISender(peer.PC, track)
				burst.SendBurst()
				// Note: we don't Stop() burst here — SendBurst is fire-and-forget
				// and the goroutine exits on its own. The periodic PLI sender
				// (created in handleSimulcastTrack) keeps running independently.
			}
		}
	}
}

// Negotiate sends a new offer to the peer (server-side renegotiation).
// Called after adding tracks to the peer's PeerConnection.
// If the PeerConnection is already mid-negotiation, it defers until stable.
func (r *Room) Negotiate(peer *Peer) {
	if peer.PC.SignalingState() != webrtc.SignalingStateStable {
		// Already mid-negotiation — wait until stable, then retry
		peer.PC.OnSignalingStateChange(func(state webrtc.SignalingState) {
			if state == webrtc.SignalingStateStable {
				peer.PC.OnSignalingStateChange(nil) // remove one-shot handler
				r.Negotiate(peer)
			}
		})
		return
	}

	offer, err := peer.PC.CreateOffer(nil)
	if err != nil {
		log.Printf("[room] negotiate: create offer error: %v", err)
		return
	}

	if err := peer.PC.SetLocalDescription(offer); err != nil {
		log.Printf("[room] negotiate: set local desc error: %v", err)
		return
	}

	peer.Send(MustJSON(OfferEvent{
		Type: EventOffer,
		SDP:  offer.SDP,
	}))
}

// HandleAnswer processes an SDP answer from a peer (in response to our offer).
func (r *Room) HandleAnswer(peerID string, sdp string) {
	peer := r.GetPeer(peerID)
	if peer == nil {
		return
	}

	err := peer.PC.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  sdp,
	})

	if err != nil {
		log.Printf("[room] peer =%s set answer error: %v", peerID, err)
	}
}

// HandleOffer processes an SDP offer from a peer and sends back an answer.
func (r *Room) HandleOffer(peerID string, sdp string) {
	peer := r.GetPeer(peerID)
	if peer == nil {
		return
	}

	err := peer.PC.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  sdp,
	})

	if err != nil {
		log.Printf("[room] peer=%s set offer error: %v", peerID, err)
		return
	}

	answer, err := peer.PC.CreateAnswer(nil)
	if err != nil {
		log.Printf("[room] peer=%s create answer error: %v", peerID, err)
		return
	}

	if err := peer.PC.SetLocalDescription(answer); err != nil {
		log.Printf("[room] peer=%s set local desc error: %v", peerID, err)
		return
	}

	peer.Send(MustJSON(AnswerEvent{
		Type: EventAnswer,
		SDP:  answer.SDP,
	}))
}

// HandleCandidate adds a trickle ICE candidate from the peer.
func (r *Room) HandleCandidate(peerID string, candidate webrtc.ICECandidateInit) {
	peer := r.GetPeer(peerID)
	if peer == nil {
		return
	}

	if err := peer.PC.AddICECandidate(candidate); err != nil {
		log.Printf("[room] peer=%s add candidate error: %v", peerID, err)
	}
}

// SubscribeToExisting adds all currently-published tracks to a newly-joined peer.
// Does NOT trigger renegotiation — the client's own initial offer will include these.
func (r *Room) SubscribeToExisting(peer *Peer) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, ot := range r.outputTracks {
		if ot.SenderPeer.ID == peer.ID {
			continue
		}
		r.addTrackToPeer(peer, ot, false)
	}
}

// ─────── Internal: PeerConnection setup ────────────────

// createPeerConnection builds a PC with the MediaEngine configured for
// simulcast (header extensions registered).
func (r *Room) createPeerConnection() (*webrtc.PeerConnection, error) {
	m := &webrtc.MediaEngine{}
	if err := m.RegisterDefaultCodecs(); err != nil {
		return nil, err
	}

	//	Register header extensions required for simulcast RID detection
	for _, ext := range []string{
		"urn:ietf:params:rtp-hdrext:sdes:mid",
		"urn:ietf:params:rtp-hdrext:sdes:rtp-stream-id",
		"urn:ietf:params:rtp-hdrext:sdes:repaired-rtp-stream-id",
	} {
		if err := m.RegisterHeaderExtension(
			webrtc.RTPHeaderExtensionCapability{URI: ext},
			webrtc.RTPCodecTypeVideo,
		); err != nil {
			return nil, err
		}
	}

	api := webrtc.NewAPI(webrtc.WithMediaEngine(m))

	return api.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{config.STUNServer}},
		},
	})
}

// setupTrackHandler registers the OnTrack callback. Decides simulcast vs
// simple forward, then delegates media work to the media package.
func (r *Room) setupTrackHandler(peer *Peer) {
	peer.PC.OnTrack(func(remoteTrack *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		log.Printf("[room] peer=%s track arrived: codec=%s kind=%s RID=%s",
			peer.ID, remoteTrack.Codec().MimeType, remoteTrack.Kind(), remoteTrack.RID())

		if remoteTrack.RID() != "" && remoteTrack.Kind() == webrtc.RTPCodecTypeVideo {
			r.handleSimulcastTrack(peer, remoteTrack)
		} else {
			r.handleSimpleTrack(peer, remoteTrack)
		}
	})
}

// setupICEHandler relays ICE candidates from the PeerConnection to the peer's client.
func (r *Room) setupICEHandler(peer *Peer) {
	peer.PC.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		peer.Send(MustJSON(CandidateEvent{
			Type:      EventCandidate,
			Candidate: c.ToJSON(),
		}))
	})
}

// setupConnectionStateHandler detects disconnects and triggers peer removal.
func (r *Room) setupConnectionStateHandler(peer *Peer) {
	peer.PC.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("[room] peer=%s connection state: %s", peer.ID, state.String())
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateDisconnected {
			r.RemovePeer(peer.ID)
		}
	})
}

// ─── Internal: Track handling ───────────────────────────────────────────────

// handleSimpleTrack handles a non-simulcast track (audio, or video without RID).
func (r *Room) handleSimpleTrack(peer *Peer, remoteTrack *webrtc.TrackRemote) {
	// Create a local track to forward to subscribers
	localTrack, err := webrtc.NewTrackLocalStaticRTP(
		remoteTrack.Codec().RTPCodecCapability,
		remoteTrack.ID(),
		remoteTrack.StreamID(),
	)
	if err != nil {
		log.Printf("[room] peer=%s create local track error: %v", peer.ID, err)
		return
	}

	stats := &media.TrackStats{}

	ot := &OutputTrack{
		Local:       localTrack,
		SenderPeer:  peer,
		RemoteTrack: remoteTrack,
		Kind:        remoteTrack.Kind().String(),
		Stats:       stats,
	}

	r.mu.Lock()
	r.outputTracks[localTrack.ID()] = ot
	r.mu.Unlock()

	// Add to all other peers
	r.mu.RLock()
	for _, otherPeer := range r.peers {
		if otherPeer.ID == peer.ID {
			continue
		}
		r.addTrackToPeer(otherPeer, ot, true)
	}
	r.mu.RUnlock()

	// Start PLI sender for video tracks
	if remoteTrack.Kind() == webrtc.RTPCodecTypeVideo {
		pli := media.NewPLISender(peer.PC, remoteTrack)
		peer.AddPLISender(remoteTrack.ID(), pli)
	}

	// Start forwarding (with stats accumulation)
	go media.ForwardRTP(remoteTrack, localTrack, stats)
}

// handleSimulcastTrack handles a single simulcast layer arrival.
// On first layer: creates the SimulcastTrack and subscribes all peers.
// On subsequent layers: just registers the new layer.
func (r *Room) handleSimulcastTrack(peer *Peer, remoteTrack *webrtc.TrackRemote) {
	layer := media.LayerID(remoteTrack.RID())
	trackID := remoteTrack.StreamID() //	group layers by stream

	//	check if we already have a SimulcastTrack for this peer+stream
	st := peer.GetSimulcastTrack(trackID)

	if st == nil {
		//	First layer - create the SimulcastTrack
		var err error
		st, err = media.NewSimulcastTrack(peer.ID, trackID, remoteTrack.Codec().MimeType)
		if err != nil {
			log.Printf("[room] peer=%s create simulcast track error: %v", peer.ID, err)
			return
		}

		peer.AddSimulcastTrack(trackID, st)

		//	Register the output track so late-joiners get subscribed
		ot := &OutputTrack{
			Local:       st.Output,
			SenderPeer:  peer,
			RemoteTrack: remoteTrack,
			Kind:        "video",
		}

		r.mu.Lock()
		r.outputTracks[st.Output.ID()] = ot
		r.mu.Unlock()

		//	Subscribe all other peers to the output track
		r.mu.RLock()
		for _, otherPeer := range r.peers {
			if otherPeer.ID == peer.ID {
				continue
			}
			r.addTrackToPeer(otherPeer, ot, true)
		}
		r.mu.RUnlock()
	}

	//	Register this layer (start the read goroutine inside SimulcastTrack)
	st.AddLayer(layer, remoteTrack)

	//	Start a PLI sender for this specific layer
	pli := media.NewPLISender(peer.PC, remoteTrack)
	peer.AddPLISender(remoteTrack.ID()+":"+string(layer), pli)

}

// addTrackToPeer adds an output track to a peer's PeerConnection.
// If negotiate is true, triggers renegotiation so the peer's browser
// knows about the new track. Pass false during SubscribeToExisting
// (the client's own offer will cover these tracks).
func (r *Room) addTrackToPeer(peer *Peer, ot *OutputTrack, negotiate bool) {
	sender, err := peer.PC.AddTrack(ot.Local)
	if err != nil {
		log.Printf("[room] peer=%s add track error: %v", peer.ID, err)
		return
	}

	//	Drain RTCP from the sender - required or the sender buffer fills up
	go func() {
		buf := make([]byte, config.RTPReadBufferSize)
		for {
			if _, _, err := sender.Read(buf); err != nil {
				return
			}
		}
	}()

	if negotiate {
		r.Negotiate(peer)
	}
}

// ─── Internal: Metrics ──────────────────────────────────────────────────────

// metricsLoop periodically collects track stats and broadcasts to all peers.
func (r *Room) metricsLoop() {
	ticker := time.NewTicker(config.MetricsBroadcastInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.stopMetrics:
			return
		case <-ticker.C:
		}

		r.mu.RLock()
		if len(r.peers) == 0 {
			r.mu.RUnlock()
			continue
		}

		//	Collect simulcast metrics from all peers
		var trackMetrics []media.TrackMetrics
		for _, peer := range r.peers {
			for _, st := range peer.SimulcastTracks() {
				trackMetrics = append(trackMetrics, st.SnapshotMetrics())
			}
		}

		// Collect simple forwarded track metrics
		elapsed := config.MetricsBroadcastInterval.Seconds()
		for _, ot := range r.outputTracks {
			if ot.Stats == nil {
				continue // simulcast output tracks have no Stats (metrics come from SimulcastTrack)
			}

			pkts := ot.Stats.Packets.Swap(0)
			b := ot.Stats.Bytes.Swap(0)
			f := ot.Stats.Frames.Swap(0)

			trackMetrics = append(trackMetrics, media.TrackMetrics{
				PeerID:          ot.SenderPeer.ID,
				TrackID:         ot.Local.ID(),
				ActiveLayer:     ot.Kind, // "audio" or "video"
				AvailableLayers: []string{},
				PacketsPerSec:   float64(pkts) / elapsed,
				BitrateKbps:     float64(b) * 8 / 1000 / elapsed,
				FramesPerSec:    float64(f) / elapsed,
				SnapshotTime:    time.Now(),
			})
		}

		//	Build event payload matching what the browser expects
		type trackPayload struct {
			PeerID          string   `json:"peerId"`
			ActiveLayer     string   `json:"activeLayer"`
			AvailableLayers []string `json:"availableLayers"`
			PacketsPerSec   float64  `json:"packetsPerSec"`
			BitrateKbps     float64  `json:"bitrateKbps"`
			FramesPerSec    float64  `json:"framesPerSec"`
		}

		tracks := make([]trackPayload, 0, len(trackMetrics))
		for _, m := range trackMetrics {
			tracks = append(tracks, trackPayload{
				PeerID:          m.PeerID,
				ActiveLayer:     m.ActiveLayer,
				AvailableLayers: m.AvailableLayers,
				PacketsPerSec:   math.Round(m.PacketsPerSec*100) / 100,
				BitrateKbps:     math.Round(m.BitrateKbps*100) / 100,
				FramesPerSec:    math.Round(m.FramesPerSec*100) / 100,
			})
		}

		event := MustJSON(MetricsEvent{
			Type: EventMetrics,
			Metrics: map[string]any{
				"tracks": tracks,
			},
		})

		//	Broadcast
		for _, p := range r.peers {
			p.Send(event)
		}
		r.mu.RUnlock()
	}
}

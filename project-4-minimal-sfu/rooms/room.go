package rooms

import (
	"log"
	"sync"
	"time"

	"minimal-sfu/media"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v3"
)

type Peer struct {
	ID             string
	PeerConnection *webrtc.PeerConnection
	LocalTracks    map[string]*webrtc.TrackLocalStaticRTP
	// RemoteTracks stores incoming tracks from this peer's browser.
	// Key = track ID. Used to send PLI requests back to this peer
	// when a new subscriber needs a keyframe.
	RemoteTrack map[string]*webrtc.TrackRemote
	mu          sync.Mutex

	// Send delivers a signaling event to this peer's client.
	// Injected by the signaling layer at join time.
	// The room layer calls this without knowing about WebSockets or any transport.
	Send func(event RoomEvent)
}

// TrackInfo links a local forwarding track back to its source
type TrackInfo struct {
	LocalTrack  *webrtc.TrackLocalStaticRTP
	SenderPeer  *Peer               // 	who's sending this track
	RemoteTrack *webrtc.TrackRemote //	the actual incoming track
	Stats       *media.TrackStats   //	nil for simulcast
	Kind        string              // "audio" or "video"
}

type Room struct {
	ID    string
	Peers map[string]*Peer
	mu    sync.RWMutex
	//	Tracks maps local track ID -> info about its source
	//	Used to find the sender when a new peer subscribes and needs a PLI.
	Tracks map[string]*TrackInfo
	// SimulcastTracks maps "peerID:streamID" → SimulcastTrack
	// Only for video tracks that arrive with an RID (simulcast enabled)
	SimulcastTracks map[string]*media.SimulcastTrack

	// metricsOnce ensures we start the metrics broadcaster goroutine only once.
	metricsOnce sync.Once
	stopMetrics chan struct{}
}

func NewRoom(id string) *Room {
	return &Room{
		ID:              id,
		Peers:           make(map[string]*Peer),
		Tracks:          make(map[string]*TrackInfo),
		SimulcastTracks: make(map[string]*media.SimulcastTrack),
		stopMetrics:     make(chan struct{}),
	}
}

func (r *Room) AddPeer(peerID string) (*Peer, error) {

	//	Create a MediaEngine and register default codecs
	//	This ensures VP8 simulcast is properly negotiated
	m := &webrtc.MediaEngine{}
	if err := m.RegisterDefaultCodecs(); err != nil {
		return nil, err
	}

	// Register RTP header extensions required for simulcast.
	// Without these, pion can't parse the RID from incoming RTP packets
	// and OnTrack will fire with an empty RID — breaking simulcast detection.
	//
	// - mid: identifies which media section (m= line) a packet belongs to
	// - rtp-stream-id: carries the RID ("m", "l") on each RTP packet
	// - repaired-rtp-stream-id: carries the RID for RTX (retransmission) packets
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

	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		},
	}

	pc, err := api.NewPeerConnection(config)
	if err != nil {
		return nil, err
	}

	peer := &Peer{
		ID:             peerID,
		PeerConnection: pc,
		LocalTracks:    make(map[string]*webrtc.TrackLocalStaticRTP),
		RemoteTrack:    make(map[string]*webrtc.TrackRemote),
	}

	r.mu.Lock()
	r.Peers[peerID] = peer
	r.mu.Unlock()

	// Start the metrics broadcaster once (first peer triggers it)
	r.metricsOnce.Do(func() {
		go r.broadcastMetrics()
	})

	return peer, nil
}

func (r *Room) SetupTrackHandler(peer *Peer) {
	peer.PeerConnection.OnTrack(func(remoteTrack *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		log.Printf("Peer %s sent track: codec=%s, kind=%s RID=%s SSRC=%d",
			peer.ID,
			remoteTrack.Codec().MimeType,
			remoteTrack.Kind(),
			remoteTrack.RID(),
			remoteTrack.SSRC())

		peer.mu.Lock()
		peer.RemoteTrack[remoteTrack.ID()] = remoteTrack
		peer.mu.Unlock()

		// --- SIMULCAST PATH ---
		// If the track has an RID, it's a simulcast layer
		if remoteTrack.RID() != "" && remoteTrack.Kind() == webrtc.RTPCodecTypeVideo {
			log.Printf("SIMULCAST TRACK DETECTED for peer: %s", peer.ID)
			r.handleSimulcastLayer(peer, remoteTrack)
			return
		}

		log.Printf("NON-SIMULCAST TRACK DETECTED for peer: %s", peer.ID)
		// --- NON-SIMULCAST PATH (audio, or video without simulcast) ---
		// This is the existing code you already have
		localTrack, err := webrtc.NewTrackLocalStaticRTP(
			remoteTrack.Codec().RTPCodecCapability,
			remoteTrack.ID(),
			remoteTrack.StreamID(),
		)

		if err != nil {
			log.Println("Error creating local track:", err)
			return
		}

		r.mu.Lock()
		r.Tracks[localTrack.ID()] = &TrackInfo{
			LocalTrack:  localTrack,
			SenderPeer:  peer,
			RemoteTrack: remoteTrack,
		}
		r.mu.Unlock()

		// Step 2: Add this local track to every OTHER peer's PeerConnection
		r.mu.RLock()
		for id, otherPeer := range r.Peers {
			if id == peer.ID {
				continue // don't send my own track back to me
			}
			r.addTrackToPeer(otherPeer, localTrack, peer, remoteTrack)
		}
		r.mu.RUnlock()

		// This is redundant — addTrackToPeer already sends PLI for video tracks
		// if remoteTrack.Kind() == webrtc.RTPCodecTypeVideo {
		//     r.sendPLI(peer, remoteTrack)
		// }

		stats := &media.TrackStats{}
		r.Tracks[localTrack.ID()] = &TrackInfo{
			LocalTrack:  localTrack,
			SenderPeer:  peer,
			RemoteTrack: remoteTrack,
			Stats:       stats,
			Kind:        remoteTrack.Kind().String(),
		}

		// Step 3: Start the forwarding goroutine
		go media.ForwardRTP(remoteTrack, localTrack, stats)

		// For video tracks, periodically request keyframes as a safety net.
		// In production you'd be smarter about this (only on packet loss),
		// but for Milestone B this ensures things recover.
		if remoteTrack.Kind() == webrtc.RTPCodecTypeVideo {
			go func() {
				ticker := time.NewTicker(5 * time.Second)
				defer ticker.Stop()
				for range ticker.C {
					if r.GetPeer(peer.ID) == nil {
						return
					}
					r.sendPLI(peer, remoteTrack)
				}
			}()
		}
	})
}

// sendPLI sends a Picture Loss Indication to the peer who owns remoteTrack.
// This tells their encoder to produce a keyframe so new subscribers can
// decode the video stream immediately.
func (r *Room) sendPLI(senderPeer *Peer, remoteTrack *webrtc.TrackRemote) {
	// log.Printf("Requesting PLI sender original sender PeerID: %s for TrackID: %s\n", senderPeer.ID, remoteTrack.ID())
	err := senderPeer.PeerConnection.WriteRTCP([]rtcp.Packet{
		&rtcp.PictureLossIndication{
			MediaSSRC: uint32(remoteTrack.SSRC()),
		},
	})

	if err != nil {
		log.Printf("Error sending PLI to peer %s: %v", senderPeer.ID, err)
	} else {
		// log.Printf("Sent PLI to peer %s for track %s (SSRC: %d)",
		// 	senderPeer.ID, remoteTrack.ID(), remoteTrack.SSRC())
	}

}

func (r *Room) addTrackToPeer(otherPeer *Peer, track *webrtc.TrackLocalStaticRTP, senderPeer *Peer, remoteTrack *webrtc.TrackRemote) {
	otherPeer.mu.Lock()

	log.Printf("Adding track to existing peer [PeerId: %s | OUT Track ID: %s | SENDER PEERID: %s | SENDER Track ID: %s\n", otherPeer.ID, track.ID(), senderPeer.ID, remoteTrack.ID())

	sender, err := otherPeer.PeerConnection.AddTrack(track)
	if err != nil {
		otherPeer.mu.Unlock()
		log.Printf("Error adding track to peer %s: %v", otherPeer.ID, err)
		return
	}

	// Read incoming RTCP packets (like receiver reports)
	// This goroutine must exist or the sender will block
	go func() {
		buf := make([]byte, 1500)
		for {
			if _, _, err := sender.Read(buf); err != nil {
				return
			}
		}
	}()

	otherPeer.LocalTracks[track.ID()] = track
	otherPeer.mu.Unlock()

	if remoteTrack.Kind() == webrtc.RTPCodecTypeVideo {
		//	Commented intentionally, to see that go routine sendPLI will anyways fix the frozen video delay
		r.sendPLI(senderPeer, remoteTrack)
	}

}

// RemovePeer closes the peer connection and removes it from the room.
// Called when a participant disconnects or leaves.
func (r *Room) RemovePeer(peerID string) {
	r.mu.Lock()
	peer, exists := r.Peers[peerID]
	if !exists {
		r.mu.Unlock()
		return
	}
	delete(r.Peers, peerID)

	// Collect stream IDs before removing — the frontend uses these
	// to identify which video tiles to remove.
	streamIDs := make([]string, 0)
	for trackId, info := range r.Tracks {
		if info.SenderPeer.ID == peerID {
			streamIDs = append(streamIDs, info.LocalTrack.StreamID())
			delete(r.Tracks, trackId)
		}
	}

	for key, st := range r.SimulcastTracks {
		if st.PeerID == peerID {
			st.Stop()
			delete(r.SimulcastTracks, key)
		}
	}

	//	Remove peer from the room
	r.mu.Unlock()

	// Close the PeerConnection — this stops all tracks and ICE
	if err := peer.PeerConnection.Close(); err != nil {
		log.Printf("Error closing peer connection for %s: %v", peerID, err)
	}

	// Notify remaining peers that this peer has left
	r.mu.RLock()
	for _, p := range r.Peers {
		if p.Send != nil {
			p.Send(RoomEvent{
				Type:   EventPeerLeft,
				PeerID: peerID,
				Payload: map[string]any{
					"streamIDs": streamIDs,
				},
			})
		}
	}
	r.mu.RUnlock()

	log.Printf("Peer %s removed from room %s (remaining: %d)", peerID, r.ID, len(r.Peers))
}

// GetPeer returns the peer with the given ID, or nil if not found.
func (r *Room) GetPeer(peerID string) *Peer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.Peers[peerID]
}

// SubscribeToExistingTracks adds all tracks currently in the room to the
// newly joined peer. This handles the case where peer B joins after peer A
// has already started sending — without this, B would never receive A's tracks
// because A's OnTrack already fired before B existed.
func (r *Room) SubscribeToExistingTracks(peer *Peer) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, trackInfo := range r.Tracks {
		// Don't subscribe a peer to their own tracks
		if trackInfo.SenderPeer.ID == peer.ID {
			continue
		}
		r.addTrackToPeer(peer, trackInfo.LocalTrack, trackInfo.SenderPeer, trackInfo.RemoteTrack)
	}
}

func (r *Room) handleSimulcastLayer(peer *Peer, remoteTrack *webrtc.TrackRemote) {
	rid := media.Layer(remoteTrack.RID())
	//	Key for this peer's simulcast video: "peerID:streamID"
	key := peer.ID + ":" + remoteTrack.StreamID()

	r.mu.Lock()
	st, exists := r.SimulcastTracks[key]

	if !exists {
		//	First layer we've received for this peer's video
		//	Create the output track and simulcast grouping
		outTrack, err := webrtc.NewTrackLocalStaticRTP(
			remoteTrack.Codec().RTPCodecCapability,
			remoteTrack.ID(), //	track ID (same across all layers)
			remoteTrack.StreamID(),
		)

		if err != nil {
			r.mu.Unlock()
			log.Printf("Error creating simulcast output track: %v", err)
			return
		}

		st = media.NewSimulcastTrack(peer.ID, outTrack)
		r.SimulcastTracks[key] = st

		//	Register in tracks map so SubscribeToExistingTracks works
		r.Tracks[outTrack.ID()] = &TrackInfo{
			LocalTrack:  outTrack,
			SenderPeer:  peer,
			RemoteTrack: remoteTrack,
		}
		r.mu.Unlock()

		//	Add the output track to all other peers (just like non-simulcast)
		r.mu.RLock()

		for id, otherPeer := range r.Peers {
			if id == peer.ID {
				continue
			}
			r.addTrackToPeer(otherPeer, outTrack, peer, remoteTrack)
		}
		r.mu.RUnlock()

		// Periodic PLI for ALL layers — keeps Chrome from pausing
		// layers that aren't currently active. Without this, switching
		// back to a previously-inactive layer fails because Chrome
		// stopped sending it.
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				if r.GetPeer(peer.ID) == nil {
					return
				}
				// Send PLI for every layer we know about
				for _, layerTrack := range st.Layers {
					r.sendPLI(peer, layerTrack)
				}
			}
		}()
	} else {
		r.mu.Unlock()
	}

	st.AddLayer(rid, remoteTrack)
}

// SwitchSimulcastLayer changes the forwarded quality layer for a given sender's video
func (r *Room) SwitchSimulcastLayer(senderPeerID string, layer media.Layer) bool {
	r.mu.RLock()
	senderPeer := r.Peers[senderPeerID]

	for _, st := range r.SimulcastTracks {
		if st.PeerID == senderPeerID {
			track := st.SwitchLayer(layer)
			r.mu.RUnlock()
			// Send PLI to request a keyframe from the new layer
			if track != nil && senderPeer != nil {
				r.sendPLI(senderPeer, track)
			}
			return track != nil
		}
	}
	r.mu.RUnlock()
	log.Printf("No simulcast track found for peer %s", senderPeerID)
	return false
}

func (r *Room) SwitchAllSimulcastLayers(layer media.Layer) {
	r.mu.RLock()
	// Collect switch results under lock
	type pliTarget struct {
		peer  *Peer
		track *webrtc.TrackRemote
	}
	var targets []pliTarget

	for _, st := range r.SimulcastTracks {
		track := st.SwitchLayer(layer)
		if track != nil {
			if p, ok := r.Peers[st.PeerID]; ok {
				targets = append(targets, pliTarget{peer: p, track: track})
			}
		}
	}
	r.mu.RUnlock()

	// Send a burst of PLIs — a single PLI is often not enough to
	// reactivate a paused layer in Chrome's simulcast encoder.
	// We send immediately + 3 retries over 500ms to ensure Chrome responds.
	go func() {
		for i := 0; i < 4; i++ {
			for _, t := range targets {
				r.sendPLI(t.peer, t.track)
			}
			if i < 3 {
				time.Sleep(150 * time.Millisecond)
			}
		}
	}()
}

// broadcastMetrics runs in a goroutine and periodically sends track metrics
// to all peers in the room. Exits when stopMetrics is closed (room empty).
func (r *Room) broadcastMetrics() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.stopMetrics:
			return
		case <-ticker.C:
		}

		r.mu.RLock()
		if len(r.Peers) == 0 {
			r.mu.RUnlock()
			continue // room is empty, skip this tick but keep goroutine alive
		}

		// Collect metrics from all simulcast tracks
		trackMetrics := make([]TrackMetricsPayload, 0, len(r.SimulcastTracks))
		for _, st := range r.SimulcastTracks {
			snap := st.SnapshotAndReset()
			// Convert to per-second (we snapshot every 2 seconds)
			pps := snap.Packets / 2
			bps := snap.Bytes / 2
			fps := snap.Frames / 2
			bitrateKbps := float64(bps*8) / 1000.0

			layers := make([]string, len(snap.AvailableLayers))
			for i, l := range snap.AvailableLayers {
				layers[i] = string(l)
			}

			trackMetrics = append(trackMetrics, TrackMetricsPayload{
				PeerID:          snap.PeerID,
				ActiveLayer:     string(snap.ActiveLayer),
				AvailableLayers: layers,
				PacketsPerSec:   pps,
				BytesPerSec:     bps,
				BitrateKbps:     bitrateKbps,
				FramesPerSec:    fps,
			})
		}

		for _, info := range r.Tracks {
			if info.Stats == nil {
				continue //	simulcast tracks handled above
			}

			packets := info.Stats.Packets.Swap(0)
			bytes := info.Stats.Bytes.Swap(0)
			frames := info.Stats.Frames.Swap(0)

			trackMetrics = append(trackMetrics, TrackMetricsPayload{
				PeerID:          info.SenderPeer.ID,
				ActiveLayer:     info.Kind, // "audio" or "video" instead of layer
				AvailableLayers: []string{},
				PacketsPerSec:   packets / 2,
				BytesPerSec:     bytes / 2,
				BitrateKbps:     float64(bytes/2*8) / 1000.0,
				FramesPerSec:    frames / 2,
			})
		}

		// Broadcast to all peers
		peers := make([]*Peer, 0, len(r.Peers))
		for _, p := range r.Peers {
			peers = append(peers, p)
		}
		r.mu.RUnlock()

		event := RoomEvent{
			Type: EventMetrics,
			Payload: map[string]any{
				"tracks": trackMetrics,
			},
		}

		for _, p := range peers {
			if p.Send != nil {
				p.Send(event)
			}
		}
	}
}

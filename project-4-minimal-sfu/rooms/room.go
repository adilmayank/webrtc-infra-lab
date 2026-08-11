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
}

// TrackInfo links a local forwarding track back to its source
type TrackInfo struct {
	LocalTrack  *webrtc.TrackLocalStaticRTP
	SenderPeer  *Peer               // 	who's sending this track
	RemoteTrack *webrtc.TrackRemote //	the actual incoming track
}

type Room struct {
	ID    string
	Peers map[string]*Peer
	mu    sync.RWMutex
	//	Tracks maps local track ID -> info about its source
	//	Used to find the sender when a new peer subscribes and needs a PLI.
	Tracks map[string]*TrackInfo
}

func NewRoom(id string) *Room {
	return &Room{
		ID:     id,
		Peers:  make(map[string]*Peer),
		Tracks: make(map[string]*TrackInfo),
	}
}

func (r *Room) AddPeer(peerID string) (*Peer, error) {
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		},
	}

	pc, err := webrtc.NewPeerConnection(config)
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

	return peer, nil
}

func (r *Room) SetupTrackHandler(peer *Peer) {
	peer.PeerConnection.OnTrack(func(remoteTrack *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		// remoteTrack = the audio coming IN from this peer's browser
		//
		// remoteTrack.Codec() tells you: is this opus audio? VP8 video?
		// remoteTrack.Kind() tells you: audio or video
		// remoteTrack.ID() is a unique identifier for this track

		log.Printf("Peer %s sent track: codec=%s, kind=%s", peer.ID, remoteTrack.Codec().MimeType, remoteTrack.Kind())

		peer.mu.Lock()
		peer.RemoteTrack[remoteTrack.ID()] = remoteTrack
		peer.mu.Unlock()

		// Now we need to forward this track to every OTHER peer in the room.
		// Step 1: Create a local track that mirrors the remote track's codec
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

		// Step 3: Start the forwarding goroutine
		go media.ForwardRTP(remoteTrack, localTrack)

		// For video tracks, periodically request keyframes as a safety net.
		// In production you'd be smarter about this (only on packet loss),
		// but for Milestone B this ensures things recover.
		if remoteTrack.Kind() == webrtc.RTPCodecTypeVideo {
			go func() {
				ticker := time.NewTicker(3 * time.Second)
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
	err := senderPeer.PeerConnection.WriteRTCP([]rtcp.Packet{
		&rtcp.PictureLossIndication{
			MediaSSRC: uint32(remoteTrack.SSRC()),
		},
	})

	if err != nil {
		log.Printf("Error sending PLI to peer %s: %v", senderPeer.ID, err)
	} else {
		log.Printf("Sent PLI to peer %s for track %s (SSRC: %d)",
			senderPeer.ID, remoteTrack.ID(), remoteTrack.SSRC())
	}

}

func (r *Room) addTrackToPeer(otherPeer *Peer, track *webrtc.TrackLocalStaticRTP, senderPeer *Peer, remoteTrack *webrtc.TrackRemote) {
	otherPeer.mu.Lock()

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
		// r.sendPLI(senderPeer, remoteTrack)
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
	r.mu.Unlock()

	// Close the PeerConnection — this stops all tracks and ICE
	if err := peer.PeerConnection.Close(); err != nil {
		log.Printf("Error closing peer connection for %s: %v", peerID, err)
	}

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

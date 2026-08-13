package signaling

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"

	"minimal-sfu/media"
	"minimal-sfu/rooms"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v3"
)

// RoomManager holds all active rooms. A simple in-memory map for now.
type RoomManager struct {
	rooms map[string]*rooms.Room
	mu    sync.RWMutex
}

func NewRoomManager() *RoomManager {
	return &RoomManager{
		rooms: make(map[string]*rooms.Room),
	}
}

func (rm *RoomManager) GetOrCreateRoom(id string) *rooms.Room {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	if room, exists := rm.rooms[id]; exists {
		return room
	}

	room := rooms.NewRoom(id)
	rm.rooms[id] = room
	log.Printf("Created new room: %s\n", id)
	return room
}

// HandleWebSocket is the main entry point for a new WebSocket connection.
// Each browser tab connects here — one goroutine per connection.
func (rm *RoomManager) HandleWebSocket(conn *websocket.Conn) {
	var (
		room             *rooms.Room
		peer             *rooms.Peer
		peerID           string
		initialOfferDone bool // tracks whether the first offer/answer is complete
	)

	// wsMu protects writes to this single WebSocket connection.
	// Multiple goroutines (ICE candidate callbacks, renegotiation) may
	// try to send messages concurrently — this prevents interleaved writes.
	var wsMu sync.Mutex

	sendJSON := func(msg SignalMessage) {
		wsMu.Lock()
		defer wsMu.Unlock()
		if err := conn.WriteJSON(msg); err != nil {
			log.Printf("WebSocket write error for peer %s: %v\n", peerID, err)
		}
	}

	defer func() {
		if room != nil && peerID != "" {
			room.RemovePeer(peerID)
		}
		conn.Close()
	}()

	// Read loop — process messages from the browser
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			log.Printf("WebSocket read error: %v\n", err)
			return
		}

		var msg SignalMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			log.Printf("Invalid JSON from client: %v\n", err)
			continue
		}

		switch msg.Type {
		case TypeJoin:
			// --- JOIN ---
			// Browser says "I want to join room X with peer ID Y"
			peerID = msg.PeerID
			room = rm.GetOrCreateRoom(msg.RoomID)

			var addErr error
			peer, addErr = room.AddPeer(peerID)
			if addErr != nil {
				log.Printf("Failed to add peer %s: %v\n", peerID, addErr)
				return
			}

			// Set up the OnTrack handler BEFORE any SDP exchange.
			// This ensures we're ready to receive tracks as soon as
			// the connection is established.
			fmt.Printf("Setting up track handler for newly joined peer: %s", peer.ID)
			room.SetupTrackHandler(peer)

			// NOTE: We do NOT call SubscribeToExistingTracks here.
			// If we did, AddTrack would fire OnNegotiationNeeded → server creates
			// an offer → PC moves to have-local-offer. Then when the browser's own
			// offer arrives moments later, SetRemoteDescription fails because you
			// can't go from have-local-offer to have-remote-offer (offer collision/glare).
			// Instead, we subscribe after the first offer/answer completes (see TypeOffer).

			// ICE candidate callback — when pion discovers a candidate,
			// send it to the browser so it can reach us.
			peer.PeerConnection.OnICECandidate(func(c *webrtc.ICECandidate) {
				if c == nil {
					return // ICE gathering complete, nothing to send
				}
				init := c.ToJSON()
				sendJSON(SignalMessage{
					Type: TypeCandidate,
					Candidate: &Candidate{
						Candidate:     init.Candidate,
						SDPMid:        init.SDPMid,
						SDPMLineIndex: init.SDPMLineIndex,
					},
				})
			})

			// Connection state monitoring — detect disconnects
			peer.PeerConnection.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
				log.Printf("Peer %s connection state: %s\n", peerID, state.String())
				if state == webrtc.PeerConnectionStateFailed ||
					state == webrtc.PeerConnectionStateClosed ||
					state == webrtc.PeerConnectionStateDisconnected {
					room.RemovePeer(peerID)
				}
			})

			// Renegotiation callback — fires when we AddTrack to this peer
			// (i.e., another participant joined and we need to send them
			// that participant's audio). We create a new offer and send it.
			peer.PeerConnection.OnNegotiationNeeded(func() {
				offer, err := peer.PeerConnection.CreateOffer(nil)
				if err != nil {
					log.Printf("Error creating renegotiation offer for %s: %v\n", peerID, err)
					return
				}
				if err := peer.PeerConnection.SetLocalDescription(offer); err != nil {
					log.Printf("Error setting local desc for %s: %v\n", peerID, err)
					return
				}
				sendJSON(SignalMessage{
					Type: TypeOffer,
					SDP:  offer.SDP,
				})
			})

			log.Printf("Peer %s joined room %s\n", peerID, msg.RoomID)

		case TypeOffer:
			// --- OFFER ---
			// Browser sends an SDP offer (initial or renegotiation answer to our offer)
			if peer == nil {
				log.Println("Received offer before join")
				continue
			}

			offer := webrtc.SessionDescription{
				Type: webrtc.SDPTypeOffer,
				SDP:  msg.SDP,
			}

			if err := peer.PeerConnection.SetRemoteDescription(offer); err != nil {
				log.Printf("Error setting remote description for %s: %v\n", peerID, err)
				continue
			}

			answer, err := peer.PeerConnection.CreateAnswer(nil)
			if err != nil {
				log.Printf("Error creating answer for %s: %v\n", peerID, err)
				continue
			}

			if err := peer.PeerConnection.SetLocalDescription(answer); err != nil {
				log.Printf("Error setting local description for %s: %v\n", peerID, err)
				continue
			}

			// log.Printf("SDP from peer %s:\n%s", peerID, offer.SDP)

			sendJSON(SignalMessage{
				Type: TypeAnswer,
				SDP:  answer.SDP,
			})

			// After the first offer/answer completes, the PC is in "stable" state.
			// Now it's safe to AddTrack (which triggers OnNegotiationNeeded → server
			// sends a new offer). This avoids the glare/collision problem.
			if !initialOfferDone {
				initialOfferDone = true
				room.SubscribeToExistingTracks(peer)
			}

		case TypeAnswer:
			// --- ANSWER ---
			// Browser responds to a renegotiation offer we sent
			if peer == nil {
				log.Println("Received answer before join")
				continue
			}

			answer := webrtc.SessionDescription{
				Type: webrtc.SDPTypeAnswer,
				SDP:  msg.SDP,
			}

			if err := peer.PeerConnection.SetRemoteDescription(answer); err != nil {
				log.Printf("Error setting remote description (answer) for %s: %v\n", peerID, err)
			}

		case TypeCandidate:
			// --- ICE CANDIDATE ---
			// Browser sends an ICE candidate it discovered
			if peer == nil || msg.Candidate == nil {
				continue
			}

			candidate := webrtc.ICECandidateInit{
				Candidate:     msg.Candidate.Candidate,
				SDPMid:        msg.Candidate.SDPMid,
				SDPMLineIndex: msg.Candidate.SDPMLineIndex,
			}

			if err := peer.PeerConnection.AddICECandidate(candidate); err != nil {
				log.Printf("Error adding ICE candidate for %s: %v\n", peerID, err)
			}

		case TypeSwitchLayer:
			if room == nil {
				continue
			}
			if msg.TargetPeerID == "*" {
				// Switch all simulcast tracks in the room (demo mode)
				room.SwitchAllSimulcastLayers(media.Layer(msg.Layer))
			} else {
				room.SwitchSimulcastLayer(msg.TargetPeerID, media.Layer(msg.Layer))
			}

		}
	}
}

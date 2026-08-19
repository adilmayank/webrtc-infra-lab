package signaling

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/adilmayanktiwari/sfu-refactor/media"
	"github.com/adilmayanktiwari/sfu-refactor/room"
	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
)

// Handler handles WebSocket connections for signaling.
type Handler struct {
	upgrader websocket.Upgrader
	rooms    *room.Manager
}

// NewHandler creates a signaling handler wired to the given room manager.
func NewHandler(rooms *room.Manager) *Handler {
	return &Handler{
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		rooms: rooms,
	}
}

// ServeHTTP upgrades the connection to WebSocket and starts the read loop.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[signaling] upgrade error: %v", err)
		return
	}

	h.readLoop(conn)
}

// readLoop reads JSON messages from the WebSocket and dispatches them to the room.
// Blocks until the connection closes.
func (h *Handler) readLoop(conn *websocket.Conn) {
	var (
		currentRoom *room.Room
		peerID      string
		mu          sync.Mutex // protects writes to conn
	)

	// send is the callback injected into the Peer — writes JSON to this WebSocket
	send := func(msg []byte) {
		mu.Lock()
		defer mu.Unlock()
		if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			log.Printf("[signaling] peer=%s write error: %v", peerID, err)
		}
	}

	defer func() {
		if currentRoom != nil && peerID != "" {
			currentRoom.RemovePeer(peerID)
		}
		conn.Close()
	}()

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("[signaling] peer=%s read error: %v", peerID, err)
			}
			return
		}

		var msg InboundMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			log.Printf("[signaling] peer=%s invalid JSON: %v", peerID, err)
			continue
		}

		switch msg.Type {
		case "join":
			peerID = msg.PeerID
			currentRoom = h.rooms.GetOrCreate(msg.RoomID)

			peer, err := currentRoom.AddPeer(peerID, send)
			if err != nil {
				log.Printf("[signaling] peer=%s join error: %v", peerID, err)
				return
			}

			// Subscribe to tracks already in the room
			currentRoom.SubscribeToExisting(peer)
			log.Printf("[signaling] peer=%s joined room=%s", peerID, msg.RoomID)

		case "offer":
			if currentRoom == nil {
				continue
			}
			currentRoom.HandleOffer(peerID, msg.SDP)

		case "answer":
			if currentRoom == nil {
				continue
			}
			currentRoom.HandleAnswer(peerID, msg.SDP)

		case "candidate":
			if currentRoom == nil {
				continue
			}
			var candidate webrtc.ICECandidateInit
			if err := json.Unmarshal(msg.Candidate, &candidate); err != nil {
				log.Printf("[signaling] peer=%s invalid candidate: %v", peerID, err)
				continue
			}
			currentRoom.HandleCandidate(peerID, candidate)

		case "switchLayer":
			if currentRoom == nil {
				continue
			}
			currentRoom.SwitchLayer(msg.TargetPeerID, media.LayerID(msg.Layer))

		default:
			log.Printf("[signaling] peer=%s unknown message type: %s", peerID, msg.Type)
		}
	}
}

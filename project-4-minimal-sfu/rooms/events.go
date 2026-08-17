package rooms

// RoomEvent is a transport-agnostic message the room layer emits.
// The signaling layer (WebSocket, gRPC, etc.) is responsible for
// serializing this into whatever wire format it uses.
type RoomEvent struct {
	Type      string
	PeerID    string
	SDP       string
	Candidate *CandidateInfo
	Payload   map[string]any // extensible for future events
}

// CandidateInfo holds ICE candidate data without importing any WebSocket
// or webrtc packages — keeps the rooms package decoupled.
type CandidateInfo struct {
	Candidate     string
	SDPMid        *string
	SDPMLineIndex *uint16
}

// Event type constants — room-level events the signaling layer translates to wire messages.
const (
	EventOffer      = "offer"
	EventAnswer     = "answer"
	EventCandidate  = "candidate"
	EventPeerLeft   = "peer-left"
	EventPeerJoined = "peer-joined"
	EventMetrics    = "metrics"
)

// TrackMetricsPayload is a per-track snapshot sent in the metrics event.
type TrackMetricsPayload struct {
	PeerID          string   `json:"peerId"`
	ActiveLayer     string   `json:"activeLayer"`
	AvailableLayers []string `json:"availableLayers"`
	PacketsPerSec   uint64   `json:"packetsPerSec"`
	BytesPerSec     uint64   `json:"bytesPerSec"`
	BitrateKbps     float64  `json:"bitrateKbps"`
	FramesPerSec    uint64   `json:"framesPerSec"`
}

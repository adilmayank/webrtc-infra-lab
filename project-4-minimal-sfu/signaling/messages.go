package signaling

type MessageType string

const (
	TypeJoin        MessageType = "join"
	TypeOffer       MessageType = "offer"
	TypeAnswer      MessageType = "answer"
	TypeCandidate   MessageType = "candidate"
	TypeSwitchLayer MessageType = "switchLayer"
	TypePeerLeft    MessageType = "peer-left"
	TypeMetrics     MessageType = "metrics"
)

type SignalMessage struct {
	Type      MessageType `json:"type"`
	RoomID    string      `json:"roomId,omitempty"`
	PeerID    string      `json:"peerId,omitempty"`
	SDP       string      `json:"sdp,omitempty"`
	Candidate *Candidate  `json:"candidate,omitempty"`
	//	For simulcast layer switching
	TargetPeerID string `json:"targetPeerId,omitempty"` //	whose video to switch
	Layer        string `json:"layer,omitempty"`        //	"m" or "l"
	//	For peer-left event — stream IDs the frontend uses to remove tiles
	StreamIDs []string `json:"streamIDs,omitempty"`
	//	For metrics event
	Metrics *MetricsPayload `json:"metrics,omitempty"`
}

// MetricsPayload is the wire format for the metrics event.
type MetricsPayload struct {
	Tracks []TrackMetricsWire `json:"tracks"`
}

// TrackMetricsWire is a single track's metrics in wire format.
type TrackMetricsWire struct {
	PeerID          string   `json:"peerId"`
	ActiveLayer     string   `json:"activeLayer"`
	AvailableLayers []string `json:"availableLayers"`
	PacketsPerSec   uint64   `json:"packetsPerSec"`
	BytesPerSec     uint64   `json:"bytesPerSec"`
	BitrateKbps     float64  `json:"bitrateKbps"`
	FramesPerSec    uint64   `json:"framesPerSec"`
}

type Candidate struct {
	Candidate     string  `json:"candidate"`
	SDPMid        *string `json:"sdpMid,omitempty"`
	SDPMLineIndex *uint16 `json:"sdpMLineIndex,omitempty"`
}

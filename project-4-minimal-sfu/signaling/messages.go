package signaling

type MessageType string

const (
	TypeJoin      MessageType = "join"
	TypeOffer     MessageType = "offer"
	TypeAnswer    MessageType = "answer"
	TypeCandidate MessageType = "candidate"
)

type SignalMessage struct {
	Type      MessageType `json:"type"`
	RoomID    string      `json:"roomId,omitempty"`
	PeerID    string      `json:"peerId,omitempty"`
	SDP       string      `json:"sdp,omitempty"`
	Candidate *Candidate  `json:"candidate,omitempty"`
}

type Candidate struct {
	Candidate     string  `json:"candidate"`
	SDPMid        *string `json:"sdpMid,omitempty"`
	SDPMLineIndex *uint16 `json:"sdpMLineIndex,omitempty"`
}

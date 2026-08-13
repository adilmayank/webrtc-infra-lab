package signaling

type MessageType string

const (
	TypeJoin        MessageType = "join"
	TypeOffer       MessageType = "offer"
	TypeAnswer      MessageType = "answer"
	TypeCandidate   MessageType = "candidate"
	TypeSwitchLayer MessageType = "switchLayer"
)

type SignalMessage struct {
	Type      MessageType `json:"type"`
	RoomID    string      `json:"roomId,omitempty"`
	PeerID    string      `json:"peerId,omitempty"`
	SDP       string      `json:"sdp,omitempty"`
	Candidate *Candidate  `json:"candidate,omitempty"`
	//	For simulcast layer switching
	TargetPeerID string `json:"targetPeerId,omitempty"` //	whose video to switch
	Layer        string `json:"layer,omitempty"`        //	"h", "m" or "l"
}

type Candidate struct {
	Candidate     string  `json:"candidate"`
	SDPMid        *string `json:"sdpMid,omitempty"`
	SDPMLineIndex *uint16 `json:"sdpMLineIndex,omitempty"`
}

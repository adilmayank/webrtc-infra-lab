package signaling

import "encoding/json"

// InboundMessage is the envelope for all messages arriving from a client.
// Only the fields relevant to a given Type will be populated.
type InboundMessage struct {
	Type         string          `json:"type"`
	RoomID       string          `json:"roomId,omitempty"`
	PeerID       string          `json:"peerId,omitempty"`
	SDP          string          `json:"sdp,omitempty"`
	Candidate    json.RawMessage `json:"candidate,omitempty"`
	TargetPeerID string          `json:"targetPeerId,omitempty"`
	Layer        string          `json:"layer,omitempty"`
}

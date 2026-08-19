package room

import "encoding/json"

// Outbound event types - sent to peers over their Send Callback.
const (
	EventOffer     = "offer"
	EventAnswer    = "answer"
	EventCandidate = "candidate"
	EventPeerLeft  = "peer-left"
	EventMetrics   = "metrics"
)

// OfferEvent is sent when the SFU needs to renegotiate (new tracks added).
type OfferEvent struct {
	Type string `json:"type"`
	SDP  string `json:"sdp"`
}

// AnswerEvent is sent in response to a peer's offer.
type AnswerEvent struct {
	Type string `json:"type"`
	SDP  string `json:"sdp"`
}

// CandidateEvent relays an ICE candidate.
type CandidateEvent struct {
	Type      string `json:"type"`
	Candidate any    `json:"candidate"`
}

// PeerLeftEvent notifies remaining peers that someone left.
type PeerLeftEvent struct {
	Type      string   `json:"type"`
	StreamIDs []string `json:"streamIDs"`
}

// MetricsEvent pushes track stats to peers (for the debug panel).
type MetricsEvent struct {
	Type    string `json:"type"`
	Metrics any    `json:"metrics"`
}

// MustJSON marshals v to JSON bytes, panics on error (should never fail
// for our well-typed structs).
func MustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic("room: json marshal failed: " + err.Error())
	}
	return b
}

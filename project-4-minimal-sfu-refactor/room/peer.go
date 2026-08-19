package room

import (
	"sync"

	"github.com/adilmayanktiwari/sfu-refactor/media"
	"github.com/pion/webrtc/v4"
)

type Peer struct {
	ID string
	PC *webrtc.PeerConnection

	//	Send delivers a raw JSON message back to this peer's transport
	//	Injected by the signaling layer - the room doesn't know it's a WebSocket
	Send func(msg []byte)

	//	mu protects the fields below
	mu sync.Mutex

	//	simulcastTracks holds simulcast tracks published by this peer.
	//	Key: trackID
	simulcastTracks map[string]*media.SimulcastTrack

	//	pliSenders holds active PLI senders for this peer's remote tracks.
	//	Key: trackID
	pliSenders map[string]*media.PLISender
}

// NewPeer creates a peer with initialized maps
func NewPeer(id string, pc *webrtc.PeerConnection, send func(msg []byte)) *Peer {
	return &Peer{
		ID:              id,
		PC:              pc,
		Send:            send,
		simulcastTracks: make(map[string]*media.SimulcastTrack),
		pliSenders:      make(map[string]*media.PLISender),
	}
}

// AddSimulcastTrack registers a simulcast track published by this peer.
func (p *Peer) AddSimulcastTrack(trackID string, st *media.SimulcastTrack) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.simulcastTracks[trackID] = st
}

// GetSimulcastTrack returns a simulcast track by ID, or nil.
func (p *Peer) GetSimulcastTrack(trackID string) *media.SimulcastTrack {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.simulcastTracks[trackID]
}

// SimulcastTracks returns all simulcast tracks published by this peer.
func (p *Peer) SimulcastTracks() []*media.SimulcastTrack {
	p.mu.Lock()
	defer p.mu.Unlock()

	tracks := make([]*media.SimulcastTrack, 0, len(p.simulcastTracks))
	for _, st := range p.simulcastTracks {
		tracks = append(tracks, st)
	}
	return tracks
}

// AddPLISender registers a PLI sender for a remote track.
func (p *Peer) AddPLISender(trackID string, pli *media.PLISender) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pliSenders[trackID] = pli
}

func (p *Peer) Cleanup() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, pli := range p.pliSenders {
		pli.Stop()
	}
	for _, st := range p.simulcastTracks {
		st.Stop()
	}

	p.PC.Close()
}

package media

import (
	"log"
	"sync"
	"sync/atomic"

	"github.com/pion/webrtc/v3"
)

// Layer represents a simulcast quality tier
type Layer string

const (
	LayerHigh   Layer = "h"
	LayerMedium Layer = "m"
	LayerLow    Layer = "l"
)

// SmuilcastTrack groups multiple quality layers of the same video track
// and forwards only the currently selected layers to subscribers.
type SimulcastTrack struct {
	//	PeerID identifies who is sending this video
	PeerID string

	//	Layer holds the remote track for each quality tier,
	//	Populated as OnTrack fires for each RID (Restriction ID)
	Layers map[Layer]*webrtc.TrackRemote
	mu     sync.RWMutex

	//	ActiveLayer is the layer currently being forwarded
	ActiveLayer Layer

	//	OutTrack is the single local track that all subscribers receive.
	//	We write RTP packets from whichever layer is active into this track.
	OutTrack *webrtc.TrackLocalStaticRTP

	//	stopCh signals the active forwarding goroutines to stop
	//	(used when switching layers)
	stopCh chan struct{}

	//	running tracks whether a forwarding goroutine is active
	running atomic.Bool
}

// NewSimulcastTrack creates a new simulcast grouping.
// The outTrack should already be created with the correct codec.
func NewSimulcastTrack(peerID string, outTrack *webrtc.TrackLocalStaticRTP) *SimulcastTrack {
	return &SimulcastTrack{
		PeerID:      peerID,
		Layers:      make(map[Layer]*webrtc.TrackRemote),
		ActiveLayer: LayerHigh, //	default: forward the highest quality
		OutTrack:    outTrack,
		stopCh:      make(chan struct{}),
	}
}

// AddLayer registers a new quality layer (called when OnTrack fires for each RID)
func (st *SimulcastTrack) AddLayer(rid Layer, track *webrtc.TrackRemote) {
	st.mu.Lock()
	st.Layers[rid] = track
	st.mu.Unlock()

	log.Printf("[Simulcast] Peer %s: added layer %s (SSRC: %d)", st.PeerID, rid, track.SSRC())

	//	If this is the active layer (or the first layer we've received), start forwarding
	if rid == st.ActiveLayer || !st.running.Load() {
		st.startForwarding(rid)
	} else {
		//	Still need to drain RTP packets from non-active layers
		//	or they'll buffer up and waste memory
		go st.drainLayer(track)
	}
}

// SwitchLayer changes which qyality layer is being forwarded to subscribers.
// Returns true if the switch succeeded, false if the requested layer isn't available.
func (st *SimulcastTrack) SwitchLayer(target Layer) bool {
	st.mu.RLock()
	_, exists := st.Layers[target]
	st.mu.RUnlock()

	if !exists {
		log.Printf("[SIMULCAST] Peer %s: layer %s not available", st.PeerID, target)
		return false
	}

	if target == st.ActiveLayer && st.running.Load() {
		return true //	already forwarding this layer
	}

	log.Printf("[SIMULCAST] Peer %s: switching from %s -> %s", st.PeerID, st.ActiveLayer, target)

	//	Stop current forwarding goroutine
	close(st.stopCh)

	//	Reset stop channel for new goroutine
	st.stopCh = make(chan struct{})
	st.ActiveLayer = target

	// Start fowarding from the new layer
	st.startForwarding(target)
	return true
}

// startForwarding begings by reading RTP from the specified layer and writing to OutTrack
func (st *SimulcastTrack) startForwarding(layer Layer) {
	st.mu.RLock()
	track, exists := st.Layers[layer]
	st.mu.RUnlock()

	if !exists {
		return
	}

	//	stop any existing forwarding
	if st.running.Swap(true) {
		//	There was a previous goroutine - it will exit via stopCh
	}

	stopCh := st.stopCh

	go func() {
		buf := make([]byte, 1500)
		for {
			select {
			case <-stopCh:
				//	Layer switch requested, stop this goroutine
				return
			default:
			}

			n, _, err := track.Read(buf)
			if err != nil {
				log.Printf("[SIMULCAST]: Peer %s layer %s read error: %v", st.PeerID, layer, err)
				st.running.Store(false)
				return
			}

			if _, err := st.OutTrack.Write(buf[:n]); err != nil {
				log.Printf("[SIMULCAST] Peer %s write error: %v", st.PeerID, err)
				st.running.Store(false)
				return
			}
		}
	}()
}

// drainLayer reads and discard RTP packets froma a non-active layer.
// This prevents pion's internal buffers from growing unbounded.
func (st *SimulcastTrack) drainLayer(track *webrtc.TrackRemote) {
	buf := make([]byte, 1500)

	for {
		if _, _, err := track.Read(buf); err != nil {
			return
		}

		//	Packet discarded - this layer isn't active
	}
}

func (st *SimulcastTrack) GetAvailableLayers() []Layer {
	st.mu.RLock()
	defer st.mu.RUnlock()

	layers := make([]Layer, 0, len(st.Layers))
	for l := range st.Layers {
		layers = append(layers, l)
	}

	return layers
}

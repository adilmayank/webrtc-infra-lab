package media

import (
	"encoding/binary"
	"log"
	"sync"
	"sync/atomic"

	"github.com/pion/webrtc/v3"
)

// Layer represents a simulcast quality tier
type Layer string

const (
	LayerMedium Layer = "m"
	LayerLow    Layer = "l"
)

// SimulcastTrack groups multiple quality layers of the same video track
// and forwards only the currently selected layer to subscribers.
//
// Architecture: Each layer gets exactly ONE goroutine that is always reading.
// The goroutine checks st.ActiveLayer to decide whether to write to OutTrack
// (forward) or discard. This avoids the race condition of two goroutines
// reading from the same TrackRemote.
//
// RTP Rewriting: When switching layers, the source RTP packets have different
// sequence number and timestamp spaces. We rewrite them so the subscriber sees
// a continuous stream regardless of which layer is active.
type SimulcastTrack struct {
	PeerID string

	Layers map[Layer]*webrtc.TrackRemote
	mu     sync.RWMutex

	// ActiveLayer is the layer currently being forwarded.
	ActiveLayer Layer

	// OutTrack is the single local track that all subscribers receive.
	OutTrack *webrtc.TrackLocalStaticRTP

	// stopCh is closed when the SimulcastTrack is stopped entirely
	// (peer disconnect). All layer goroutines exit.
	stopCh chan struct{}

	// --- RTP sequence/timestamp rewriting ---
	// These fields ensure the subscriber sees continuous sequence numbers
	// and timestamps even when we switch between layers.
	seqMu     sync.Mutex
	outSeqNum uint16 // next sequence number to write to OutTrack
	outTS     uint32 // last timestamp written to OutTrack
	lastInSeq uint16 // last incoming sequence number from the active layer
	lastInTS  uint32 // last incoming timestamp from the active layer
	seqInited bool   // whether we've seen the first packet
	snOffset  uint16 // offset to add to incoming seq to get continuous out seq
	tsOffset  uint32 // offset to add to incoming ts to get continuous out ts
	switching bool   // true during a layer switch (need to recalculate offsets)

	// --- Metrics counters (atomically incremented in readLoop) ---
	// These track forwarded packets/bytes since the last metrics snapshot.
	FwdPackets atomic.Uint64
	FwdBytes   atomic.Uint64
	FwdFrames  atomic.Uint64 // counts RTP marker bits = complete frames
}

// NewSimulcastTrack creates a new simulcast grouping.
func NewSimulcastTrack(peerID string, outTrack *webrtc.TrackLocalStaticRTP) *SimulcastTrack {
	return &SimulcastTrack{
		PeerID:      peerID,
		Layers:      make(map[Layer]*webrtc.TrackRemote),
		ActiveLayer: "", // will be set when the first layer arrives
		OutTrack:    outTrack,
		stopCh:      make(chan struct{}),
	}
}

// Stop shuts down all layer goroutines. Called when the peer disconnects.
func (st *SimulcastTrack) Stop() {
	close(st.stopCh)
}

// AddLayer registers a new quality layer and starts its read goroutine.
// Called when OnTrack fires for each RID.
func (st *SimulcastTrack) AddLayer(rid Layer, track *webrtc.TrackRemote) {
	st.mu.Lock()
	st.Layers[rid] = track

	// First layer to arrive becomes the active layer
	if st.ActiveLayer == "" {
		st.ActiveLayer = rid
	}
	st.mu.Unlock()

	log.Printf("[Simulcast] Peer %s: added layer %s (SSRC: %d)", st.PeerID, rid, track.SSRC())

	// Start the one-and-only goroutine for this layer.
	go st.readLoop(rid, track)
}

// readLoop is the single goroutine per layer. It reads RTP packets and either
// writes them to OutTrack (if this is the active layer) or discards them.
// After a layer switch, it waits for a keyframe before forwarding to avoid
// sending undecodable delta frames to the subscriber.
func (st *SimulcastTrack) readLoop(layer Layer, track *webrtc.TrackRemote) {
	buf := make([]byte, 1500)
	needsKeyframe := true

	for {
		n, _, err := track.Read(buf)
		if err != nil {
			// Check if we were stopped intentionally
			select {
			case <-st.stopCh:
			default:
				log.Printf("[SIMULCAST] Peer %s layer %s read error: %v", st.PeerID, layer, err)
			}
			return
		}

		st.mu.RLock()
		active := st.ActiveLayer
		st.mu.RUnlock()

		if layer != active {
			needsKeyframe = true
			continue
		}

		// This layer is active — check if we need to wait for a keyframe
		if needsKeyframe {
			if !isVP8Keyframe(buf[:n]) {
				continue
			}
			needsKeyframe = false
			log.Printf("[SIMULCAST] Peer %s layer %s: keyframe received, forwarding resumed", st.PeerID, layer)
		}

		// Rewrite sequence number and timestamp for continuity, then write
		st.rewriteAndSend(buf[:n])
	}
}

// rewriteAndSend rewrites the RTP header's sequence number and timestamp
// so the subscriber sees a continuous, gap-free RTP stream when switching layers.
func (st *SimulcastTrack) rewriteAndSend(pkt []byte) {
	if len(pkt) < 12 {
		return // too short to be a valid RTP packet
	}

	// Parse incoming seq and timestamp from the RTP header
	inSeq := binary.BigEndian.Uint16(pkt[2:4])
	inTS := binary.BigEndian.Uint32(pkt[4:8])

	st.seqMu.Lock()

	if !st.seqInited {
		// First packet ever — no offset needed
		st.seqInited = true
		st.snOffset = 0
		st.tsOffset = 0
		st.switching = false
	} else if st.switching {
		// Layer switch just happened — recalculate offsets so that
		// the new layer's packets continue from where the old left off.
		// outSeqNum is the next expected; inSeq is what the new layer is sending.
		st.snOffset = st.outSeqNum - inSeq
		st.tsOffset = st.outTS - inTS + 1 // +1 to avoid duplicate timestamp
		st.switching = false
	}

	// Apply offsets
	outSeq := inSeq + st.snOffset
	outTS := inTS + st.tsOffset

	// Track what we wrote so the next switch can calculate correctly
	st.outSeqNum = outSeq + 1
	st.outTS = outTS
	st.lastInSeq = inSeq
	st.lastInTS = inTS

	st.seqMu.Unlock()

	// Rewrite the RTP header in-place
	binary.BigEndian.PutUint16(pkt[2:4], outSeq)
	binary.BigEndian.PutUint32(pkt[4:8], outTS)

	if _, err := st.OutTrack.Write(pkt); err != nil {
		log.Printf("[SIMULCAST] Peer %s write error: %v", st.PeerID, err)
	} else {
		st.FwdPackets.Add(1)
		st.FwdBytes.Add(uint64(len(pkt)))
		// Marker bit (M) is bit 7 of byte 1 in the RTP header.
		// Set on the last packet of each video frame.
		if pkt[1]&0x80 != 0 {
			st.FwdFrames.Add(1)
		}
	}
}

// isVP8Keyframe checks if an RTP packet contains a VP8 keyframe.
// See RFC 7741 (VP8 RTP) and VP8 bitstream spec.
func isVP8Keyframe(rtpPacket []byte) bool {
	if len(rtpPacket) < 12 {
		return false
	}

	// Parse RTP header to find where the payload starts.
	// Fixed header is 12 bytes, plus 4 bytes per CSRC.
	// If extension bit is set, skip the extension too.
	cc := int(rtpPacket[0] & 0x0F) // CSRC count
	headerLen := 12 + cc*4

	// Extension bit (bit 4 of first byte)
	if rtpPacket[0]&0x10 != 0 {
		if len(rtpPacket) < headerLen+4 {
			return false
		}
		// Extension length is in 32-bit words, at offset headerLen+2
		extLen := int(binary.BigEndian.Uint16(rtpPacket[headerLen+2:])) * 4
		headerLen += 4 + extLen
	}

	if headerLen >= len(rtpPacket) {
		return false
	}

	payload := rtpPacket[headerLen:]
	if len(payload) < 1 {
		return false
	}

	// VP8 RTP payload descriptor (RFC 7741 §4.2):
	// First byte: |X|R|N|S|R|PID(5bit)|
	firstByte := payload[0]
	// S bit (bit 4) must be set — start of VP8 partition
	if firstByte&0x10 == 0 {
		return false
	}
	idx := 1

	// X bit (bit 7) — extension present
	if firstByte&0x80 != 0 {
		if idx >= len(payload) {
			return false
		}
		extByte := payload[idx]
		idx++

		// I bit — PictureID present
		if extByte&0x80 != 0 {
			if idx >= len(payload) {
				return false
			}
			if payload[idx]&0x80 != 0 {
				idx += 2 // 15-bit PictureID
			} else {
				idx++ // 7-bit PictureID
			}
		}
		// L bit — TL0PICIDX
		if extByte&0x40 != 0 {
			idx++
		}
		// T or K bit — TID/KEYIDX byte
		if extByte&0x20 != 0 || extByte&0x10 != 0 {
			idx++
		}
	}

	if idx >= len(payload) {
		return false
	}

	// VP8 payload header: bit 0 = frame type (0=key, 1=inter)
	return payload[idx]&0x01 == 0
}

// SwitchLayer changes which quality layer is being forwarded to subscribers.
// Returns the TrackRemote of the new active layer (so the caller can send PLI),
// or nil if the switch failed.
func (st *SimulcastTrack) SwitchLayer(target Layer) *webrtc.TrackRemote {
	st.mu.RLock()
	track, exists := st.Layers[target]
	current := st.ActiveLayer
	st.mu.RUnlock()

	if !exists {
		log.Printf("[SIMULCAST] Peer %s: layer %s not available", st.PeerID, target)
		return nil
	}

	if target == current {
		return nil
	}

	log.Printf("[SIMULCAST] Peer %s: switching from %s -> %s", st.PeerID, current, target)

	// Mark that we need to recalculate offsets on the next packet
	st.seqMu.Lock()
	st.switching = true
	st.seqMu.Unlock()

	st.mu.Lock()
	st.ActiveLayer = target
	st.mu.Unlock()

	return track
}

// GetAvailableLayers returns which layers have been received so far.
func (st *SimulcastTrack) GetAvailableLayers() []Layer {
	st.mu.RLock()
	defer st.mu.RUnlock()

	layers := make([]Layer, 0, len(st.Layers))
	for l := range st.Layers {
		layers = append(layers, l)
	}

	return layers
}

// TrackMetrics holds a snapshot of forwarding stats for one simulcast track.
type TrackMetrics struct {
	PeerID          string
	ActiveLayer     Layer
	AvailableLayers []Layer
	Packets         uint64
	Bytes           uint64
	Frames          uint64
}

// SnapshotAndReset returns the current metrics and resets counters to zero.
// Called periodically by the room's metrics broadcaster.
func (st *SimulcastTrack) SnapshotAndReset() TrackMetrics {
	packets := st.FwdPackets.Swap(0)
	bytes := st.FwdBytes.Swap(0)
	frames := st.FwdFrames.Swap(0)

	st.mu.RLock()
	active := st.ActiveLayer
	layers := make([]Layer, 0, len(st.Layers))
	for l := range st.Layers {
		layers = append(layers, l)
	}
	st.mu.RUnlock()

	return TrackMetrics{
		PeerID:          st.PeerID,
		ActiveLayer:     active,
		AvailableLayers: layers,
		Packets:         packets,
		Bytes:           bytes,
		Frames:          frames,
	}
}

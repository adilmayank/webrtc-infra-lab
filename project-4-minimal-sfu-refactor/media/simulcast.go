package media

import (
	"encoding/binary"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/adilmayanktiwari/sfu-refactor/config"
	"github.com/pion/webrtc/v4"
)

// LayerID identifies a simulcast quality layer.
type LayerID string

const (
	LayerHigh   LayerID = "h"
	LayerMedium LayerID = "m"
	LayerLow    LayerID = "l"
)

type SimulcastTrack struct {
	mu sync.RWMutex

	//	Identity
	PeerID  string
	TrackID string

	//	The single outbound track all subscriber receive
	Output *webrtc.TrackLocalStaticRTP

	//	Layer State
	activeLayer LayerID
	layers      map[LayerID]*webrtc.TrackRemote
	stopCh      chan struct{}

	//	RTP rewriting state
	seqMu     sync.Mutex
	outSeqNum uint16 //	next sequence number to emit
	outTS     uint32 //	next timestamp emitted
	snOffset  uint16 //	added to incoming seq to produce outgoing seq
	tsOffset  uint32 //	added to incoming ts to produce outgoing ts
	seqInited bool   //	have we seen the first packet yet?
	switching bool   //	recalculate offsets on next packet?

	//	Metrics accumulators (atomically incremented in the read loop)
	packets          atomic.Uint64
	bytes            atomic.Uint64
	frames           atomic.Uint64
	lastSnapshotTime time.Time
}

// NewSimulcastTrack create SimulcastTrack with a fresh local output track.
// The codec string should be the MIME type, e.g. "video/VP8".
func NewSimulcastTrack(peerID, trackID, codec string) (*SimulcastTrack, error) {
	outTrack, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: codec},
		trackID+"-sfu",
		peerID+"-stream",
	)

	if err != nil {
		return nil, err
	}

	return &SimulcastTrack{
		PeerID:           peerID,
		TrackID:          trackID,
		Output:           outTrack,
		activeLayer:      "",
		layers:           make(map[LayerID]*webrtc.TrackRemote),
		stopCh:           make(chan struct{}),
		lastSnapshotTime: time.Now(),
	}, nil
}

// AddLayer registers that a new layer is available and starts its goroutine
// Called each time OnTrack fires for a new RID from this peer.
func (st *SimulcastTrack) AddLayer(layer LayerID, track *webrtc.TrackRemote) {
	st.mu.Lock()
	st.layers[layer] = track
	if st.activeLayer == "" {
		st.activeLayer = layer
	}
	st.mu.Unlock()

	log.Printf("[simulcast] peer=%s layer=%s added (SSRC=%d)", st.PeerID, layer, track.SSRC())
	go st.readLoop(layer, track)
}

// ActiveLayer returns the currently forwarded layer.
func (st *SimulcastTrack) ActiveLayer() LayerID {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.activeLayer
}

// AvailableLayers returns which layers have been receieved so far.
func (st *SimulcastTrack) AvailableLayers() []LayerID {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make([]LayerID, 0, len(st.layers))
	for l := range st.layers {
		out = append(out, l)
	}

	return out
}

// SwitchLayer changes which layer is forwarded. Returns the TrackRemote for
// the new layer (so the caller can send PLI for a keyframe), of nil if the switch
// is a no-op or the target layer doesn't exist.
func (st *SimulcastTrack) SwitchLayer(target LayerID) *webrtc.TrackRemote {
	st.mu.RLock()
	track, exists := st.layers[target]
	current := st.activeLayer
	st.mu.RUnlock()

	if !exists || target == current {
		return nil
	}

	log.Printf("[simulcast] peer=%s switching %s -> %s", st.PeerID, current, target)

	//	Mark that offsets must be recalculated on the next forwarded packet
	st.seqMu.Lock()
	st.switching = true
	st.seqMu.Unlock()

	st.mu.Lock()
	st.activeLayer = target
	st.mu.Unlock()

	return track
}

// Stop shuts down all layer read goroutines. Called on peer disconnect.
func (st *SimulcastTrack) Stop() {
	close(st.stopCh)
}

func (st *SimulcastTrack) SnapshotMetrics() TrackMetrics {
	now := time.Now()
	elapsed := now.Sub(st.lastSnapshotTime).Seconds()
	if elapsed <= 0 {
		elapsed = 1
	}

	st.lastSnapshotTime = now

	pkts := st.packets.Swap(0)
	b := st.bytes.Swap(0)
	f := st.frames.Swap(0)

	available := st.AvailableLayers()
	layerStrs := make([]string, len(available))
	for i, l := range available {
		layerStrs[i] = string(l)
	}

	return TrackMetrics{
		PeerID:          st.PeerID,
		TrackID:         st.TrackID,
		ActiveLayer:     string(st.ActiveLayer()),
		PacketsPerSec:   float64(pkts) / elapsed,
		AvailableLayers: layerStrs,
		BitrateKbps:     float64(b) * 8 / 1000 / elapsed,
		FramesPerSec:    float64(f) / elapsed,
		SnapshotTime:    now,
	}
}

// readLoop is the per-layer goroutine. Reads RTP packets continuously.
// Forwards only when this layer is active AND we have a keyframe.
func (st *SimulcastTrack) readLoop(layer LayerID, track *webrtc.TrackRemote) {
	buf := make([]byte, config.RTPReadBufferSize)
	waitingForKeyFrame := true

	for {
		n, _, err := track.Read(buf)
		if err != nil {
			select {
			case <-st.stopCh:
			default:
				log.Printf("[simulcast] peer=%s layer=%s read error: %v", st.PeerID, layer, err)
			}

			return
		}

		//	Check if this is the active layer
		st.mu.RLock()
		active := st.activeLayer
		st.mu.RUnlock()

		if layer != active {
			waitingForKeyFrame = true
			continue
		}

		//	Active layer - wait for keyframe before forwarding
		if waitingForKeyFrame {
			if !isVP8KeyFrame(buf[:n]) {
				continue
			}

			waitingForKeyFrame = false
			log.Printf("[simulcast] peer=%s layer=%s keyframe receieved, forwarding", st.PeerID, layer)
		}

		st.rewriteAndSend(buf[:n])
	}
}

// rewriteAndSend applies sequence number and timestamps offsets so the
// subscriber sees a continuous stream across layer switches, then writes
// the packet to the output track.
func (st *SimulcastTrack) rewriteAndSend(pkt []byte) {
	if len(pkt) < 12 {
		return
	}

	inSeq := binary.BigEndian.Uint16(pkt[2:4])
	inTS := binary.BigEndian.Uint32(pkt[4:8])

	st.seqMu.Lock()

	if !st.seqInited {
		//	First packet ever - start with zero offsets
		st.seqInited = true
		st.snOffset = 0
		st.tsOffset = 0
		st.switching = false
	} else if st.switching {
		//	Recalculate offsets so new layer continues from where old left off
		st.snOffset = st.outSeqNum - inSeq
		st.tsOffset = st.outTS - inTS + 1
		st.switching = false
	}

	outSeq := inSeq + st.snOffset
	outTS := inTS + st.tsOffset

	st.outSeqNum = outSeq + 1
	st.outTS = outTS

	st.seqMu.Unlock()

	//	Rewrite RTP header in place
	binary.BigEndian.PutUint16(pkt[2:4], outSeq)
	binary.BigEndian.PutUint32(pkt[4:8], outTS)

	if _, err := st.Output.Write(pkt); err != nil {
		log.Printf("[simulcast] peer=%s write error: %v", st.PeerID, err)
		return
	}

	st.packets.Add(1)
	st.bytes.Add(uint64(len(pkt)))
	if pkt[1]&0x80 != 0 { //	RTP marker bit = end of frame
		st.frames.Add(1)
	}
}

// isVP8KeyFrame checks if an RTP packet carries a VP8 keyframe.
// Parses the RTP header (including extensions) and VP8 payload descriptor
func isVP8KeyFrame(rtpPacket []byte) bool {
	if len(rtpPacket) < 12 {
		return false
	}

	//	Skip fixed RTP header (rewriteAndSend bytes) + CSRCs (4 byte each)
	cc := int(rtpPacket[0] & 0x0F)
	headerLen := 12 + cc*4

	//	Skip RTP header extension if present
	if rtpPacket[0]&0x10 != 0 {
		if len(rtpPacket) < headerLen+4 {
			return false
		}
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

	// VP8 payload descriptor (RFC 7741 §4.2)
	firstByte := payload[0]
	if firstByte&0x10 == 0 { //	S bit must be set (start of partition)
		return false
	}
	idx := 1

	//	X bit - extended fields present
	if firstByte&0x80 != 0 {
		if idx >= len(payload) {
			return false
		}
		extByte := payload[idx]
		idx++

		if extByte&0x80 != 0 { //	I bit - PictureID
			if idx >= len(payload) {
				return false
			}
			if payload[idx]&0x80 != 0 {
				idx += 2 // 15 bit PictureID (M bit set)
			} else {
				idx++ //	7-bit PictureID
			}
		}
		if extByte&0x40 != 0 { //	L bit - TL0PICIDX
			idx++
		}
		if extByte&0x20 != 0 || extByte&0x10 != 0 { ///	T or K bit
			idx++
		}
	}

	if idx >= len(payload) {
		return false
	}

	//	VP8 payload header: bit 0 of first byte = frame type (0 = key)
	return payload[idx]&0x01 == 0
}

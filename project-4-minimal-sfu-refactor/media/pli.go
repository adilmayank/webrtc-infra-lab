package media

import (
	"context"
	"log"
	"time"

	"github.com/adilmayanktiwari/sfu-refactor/config"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

// PLISender manages periodic and burst PLI (Picture Loss Indicator) requests
// for a single remote track. PLI tells the sender "I need a keyframe."
type PLISender struct {
	pc     *webrtc.PeerConnection
	remote *webrtc.TrackRemote
	cancel context.CancelFunc
}

// NewPLISender starts a background goroutine that sends a PLI every
// config.PLIInterval. Returns a PLISender whose Stop() method cancels it.
func NewPLISender(pc *webrtc.PeerConnection, remote *webrtc.TrackRemote) *PLISender {
	ctx, cancel := context.WithCancel(context.Background())

	p := &PLISender{
		pc:     pc,
		remote: remote,
		cancel: cancel,
	}

	go p.periodicLoop(ctx)
	return p
}

// SendBurst fires config.PLIBurstCount PLI requests with config.PLIBurstSpacing
// between them. Used after a layer switch to get a keyframe quickly.
// Non-blocking - runs in its own goroutine.
func (p *PLISender) SendBurst() {
	go func() {
		for i := 0; i < config.PLIBurstCount; i++ {
			if err := p.pc.WriteRTCP([]rtcp.Packet{
				&rtcp.PictureLossIndication{MediaSSRC: uint32(p.remote.SSRC())},
			}); err != nil {
				log.Printf("[pli] burst write error: %v", err)
				return
			}
			if i < config.PLIBurstCount-1 {
				time.Sleep(config.PLIBurstSpacing)
			}
		}
	}()
}

// Stop cancels the periodic PLI goroutine
func (p *PLISender) Stop() {
	p.cancel()
}

// periodicLoop sends a PLI at regular intervals until ctx is cancelled.
func (p *PLISender) periodicLoop(ctx context.Context) {
	ticker := time.NewTicker(config.PLIInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.pc.WriteRTCP([]rtcp.Packet{
				&rtcp.PictureLossIndication{MediaSSRC: uint32(p.remote.SSRC())},
			}); err != nil {
				log.Printf("[pli] periodic write error: %v", err)
				return
			}
		}
	}
}

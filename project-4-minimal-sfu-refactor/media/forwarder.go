package media

import (
	"github.com/adilmayanktiwari/sfu-refactor/config"
	"github.com/pion/webrtc/v4"
)

// ForwardRTP reads RTP packets from src and writes them to dst, incrementing
// stats on each successful write. It blocks until the source track is closed
// (returns io.EOF) or a write error occurs. The caller runs this in a goroutine.
func ForwardRTP(src *webrtc.TrackRemote, dst *webrtc.TrackLocalStaticRTP, stats *TrackStats) error {
	buf := make([]byte, config.RTPReadBufferSize)

	for {
		n, _, err := src.Read(buf)
		if err != nil {
			return err
		}

		if _, writeErr := dst.Write(buf[:n]); writeErr != nil {
			return writeErr
		}

		stats.Packets.Add(1)
		stats.Bytes.Add(uint64(n))
		if n >= 2 && buf[1]&0x80 != 0 { // RTP marker bit = end of frame
			stats.Frames.Add(1)
		}
	}
}

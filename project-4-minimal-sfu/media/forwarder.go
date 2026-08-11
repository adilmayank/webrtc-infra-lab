package media

import (
	"log"

	"github.com/pion/webrtc/v3"
)

// ForwardRTP reads RTP packets from a remote track and writes them
// to a local track. This is the core forwarding loop of the SFU.
//
// It runs in its own goroutine per remote track and exits when the
// remote track closes (peer disconnects or track is removed).

func ForwardRTP(remoteTrack *webrtc.TrackRemote, localTrack *webrtc.TrackLocalStaticRTP) {
	buf := make([]byte, 1500) //	MTU-sized buffer for one RTP packet

	for {
		// ReadRTP blocks until a packet arrives or the track closes
		n, _, readErr := remoteTrack.Read(buf)
		if readErr != nil {
			log.Printf("Track %s read error (peer likely disconnected): %v", remoteTrack.ID(), readErr)
			return
		}

		// Write the exact same bytes to the local track
		// Every peer subscribed to this local track will receive this packet
		if _, writeErr := localTrack.Write(buf[:n]); writeErr != nil {
			log.Printf("Track %s write error: %v", localTrack.ID(), writeErr)
			return
		}

	}
}

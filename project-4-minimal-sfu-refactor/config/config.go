package config

import "time"

const (
	//	ICE / STUN
	STUNServer = "stun:stun.l.google.com:19302"

	//	RTP Forwarding
	RTPReadBufferSize = 1500 //	bytes - one MTU-sized packet

	//	PLI (Picture Loss Indication) - keyframe requests
	PLIInterval     = 5 * time.Second        //	often we ask for a keyframe routinely
	PLIBurstCount   = 3                      //	rapid-fire PLIs after a layer switch
	PLIBurstSpacing = 150 * time.Millisecond //	gap between burst PLIs

	//	Metrics
	MetricsBroadcastInterval = 2 * time.Second
)

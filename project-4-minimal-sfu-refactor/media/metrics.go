package media

import (
	"sync/atomic"
	"time"
)

// TrackStats accumulates packet/byte/frame counters for simple forwarded tracks.
// The room's metrics loop reads and resets these atomically.
type TrackStats struct {
	Packets atomic.Uint64
	Bytes   atomic.Uint64
	Frames  atomic.Uint64
}

// TrackMetrics is a point-in-time snapshot of a forwarded track's stats.
// Produced by SimulcastTrack.SnapshotMetrics() or computed from TrackStats,
// consumed by the room layer for broadcasting to peers (and later by Project 5's
// telemetry pipeline).
type TrackMetrics struct {
	PeerID          string
	TrackID         string
	ActiveLayer     string
	AvailableLayers []string
	PacketsPerSec   float64
	BitrateKbps     float64
	FramesPerSec    float64
	SnapshotTime    time.Time
}

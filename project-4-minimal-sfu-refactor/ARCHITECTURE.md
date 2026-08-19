# SFU Refactor — Architecture Plan

This document defines the layer-by-layer architecture for the refactored SFU.
Each section covers a package's responsibility, its structs, interfaces, and
function signatures (without implementation). Build order is top-to-bottom.

---

## Dependency Direction

```
main.go (wiring)
   │
   ├──► signaling   (WebSocket transport, wire format)
   │        │
   │        ▼
   ├──► room         (peer membership, subscription graph)
   │        │
   │        ▼
   └──► media        (RTP forwarding, simulcast, PLI)
            │
            ▼
        config        (constants, timeouts — imported by all)
```

Rules:
- Arrows mean "depends on / imports"
- `signaling` imports `room` (to call join/leave methods)
- `room` imports `media` (to set up track routing)
- `media` imports nothing project-internal except `config`
- No circular imports. Ever.

---

## Package: `config`

**Responsibility:** Named constants and tunable values. Zero logic.

**File:** `config/config.go`

```go
package config

import "time"

const (
    // ICE / STUN
    STUNServer = "stun:stun.l.google.com:19302"

    // RTP forwarding
    RTPReadBufferSize = 1500 // bytes, one MTU

    // PLI (Picture Loss Indication)
    PLIInterval       = 5 * time.Second   // periodic keyframe request
    PLIBurstCount     = 3                 // rapid PLIs on layer switch
    PLIBurstSpacing   = 150 * time.Millisecond

    // Metrics
    MetricsBroadcastInterval = 2 * time.Second
)
```

No structs, no interfaces. Just constants. When you need to tweak behavior, you
come here — not grep the entire codebase.

---

## Package: `media`

**Responsibility:** Everything about RTP packet handling — reading from source
tracks, forwarding to local tracks, simulcast layer management, PLI scheduling.
Knows nothing about rooms, peers, or signaling.

### File: `media/forwarder.go`

The simplest unit: read RTP packets from a remote track, write them to a local track.

```go
package media

import (
    "github.com/pion/webrtc/v4"
)

// ForwardRTP reads RTP packets from src and writes them to dst.
// Blocks until src is closed or context is done.
// Returns the error that stopped forwarding.
func ForwardRTP(src *webrtc.TrackRemote, dst *webrtc.TrackLocalStaticRTP) error
```

This is essentially the same function you already have. It's correct — don't change
the logic, just move it here.

---

### File: `media/simulcast.go`

Manages the multi-layer track for a single simulcast source. Handles RTP timestamp/
sequence number rewriting so the subscriber sees a seamless stream when layers switch.

```go
package media

import (
    "sync"
    "time"

    "github.com/pion/webrtc/v4"
)

// LayerID identifies a simulcast layer.
type LayerID string

const (
    LayerHigh   LayerID = "h"
    LayerMedium LayerID = "m"
    LayerLow    LayerID = "l"
)

// SimulcastTrack manages multiple incoming layers for one video source and
// forwards the active layer to a single outbound track with RTP rewriting.
type SimulcastTrack struct {
    mu sync.Mutex

    // Identity
    PeerID  string
    TrackID string

    // The single outbound track subscribers receive
    Output *webrtc.TrackLocalStaticRTP

    // Layer state
    activeLayer     LayerID
    availableLayers map[LayerID]bool

    // RTP rewriting state (sequence number + timestamp offsets)
    seqOffset uint16
    tsOffset  uint32
    lastSeq   uint16
    lastTS    uint32

    // Metrics accumulators
    packets uint32
    bytes   uint64
    frames  uint32
    lastSnapshotTime time.Time
}

// NewSimulcastTrack creates a SimulcastTrack with a new local output track.
func NewSimulcastTrack(peerID, trackID, codec string) (*SimulcastTrack, error)

// AddLayer registers that a layer is now available (called when SFU receives
// a new RID stream from the client).
func (st *SimulcastTrack) AddLayer(layer LayerID)

// ActiveLayer returns the currently forwarded layer.
func (st *SimulcastTrack) ActiveLayer() LayerID

// SwitchLayer changes which layer is forwarded. Returns true if the switch
// actually changed the layer (false if already on that layer or layer unavailable).
func (st *SimulcastTrack) SwitchLayer(target LayerID) bool

// WriteRTP receives an RTP packet from a specific layer, applies rewriting
// if it's the active layer, and writes it to Output. Packets from non-active
// layers are silently dropped (but still counted for metrics).
func (st *SimulcastTrack) WriteRTP(layer LayerID, packet []byte) error

// SnapshotMetrics returns a point-in-time metrics snapshot and resets the
// accumulators. Called periodically by the metrics collector.
func (st *SimulcastTrack) SnapshotMetrics() TrackMetrics
```

---

### File: `media/pli.go`

Isolates PLI (keyframe request) logic. The media package owns when and how
PLIs are sent — the room doesn't need to know.

```go
package media

import (
    "context"

    "github.com/pion/webrtc/v4"
)

// PLISender manages periodic and burst PLI requests for a given track.
type PLISender struct {
    pc     *webrtc.PeerConnection
    remote *webrtc.TrackRemote
    cancel context.CancelFunc
}

// NewPLISender starts a background goroutine that sends PLI at config.PLIInterval.
// Call Stop() to cancel it.
func NewPLISender(pc *webrtc.PeerConnection, remote *webrtc.TrackRemote) *PLISender

// SendBurst sends config.PLIBurstCount PLI requests with config.PLIBurstSpacing
// between them. Used after a layer switch to get a quick keyframe.
// Non-blocking (runs in a goroutine).
func (p *PLISender) SendBurst()

// Stop cancels the periodic PLI goroutine.
func (p *PLISender) Stop()
```

---

### File: `media/metrics.go`

Types for metrics snapshots. No broadcast logic here — that lives in the room or
a dedicated metrics collector. This just defines the data shape.

```go
package media

import "time"

// TrackMetrics is a point-in-time snapshot of a forwarded track's stats.
type TrackMetrics struct {
    PeerID          string
    TrackID         string
    ActiveLayer     string   // "h", "m", "l", or "audio"/"video" for non-simulcast
    AvailableLayers []string
    PacketsPerSec   float64
    BitrateKbps     float64
    FramesPerSec    float64
    SnapshotTime    time.Time
}
```

---

## Package: `room`

**Responsibility:** Who is in the room, who subscribes to whose tracks, lifecycle
events (join, leave, track published). Delegates all media work to the `media` package.

### File: `room/peer.go`

A lean struct representing one connected participant.

```go
package room

import (
    "github.com/pion/webrtc/v4"
)

// Peer represents a single participant in a room.
type Peer struct {
    ID             string
    PC             *webrtc.PeerConnection

    // Send delivers a signaling/event message back to this peer.
    // Injected by the signaling layer.
    Send           func(msg []byte)
}
```

The `Send` callback is the key boundary: the room knows it can push bytes to a peer,
but it doesn't know or care that it's a WebSocket underneath. This is what makes the
signaling layer swappable.

---

### File: `room/events.go`

Events that flow from the room outward (to signaling, to clients).

```go
package room

// EventType constants for messages pushed to peers.
const (
    EventOffer     = "offer"
    EventAnswer    = "answer"
    EventCandidate = "candidate"
    EventPeerLeft  = "peer-left"
    EventMetrics   = "metrics"
)

// RoomEvent is a generic event envelope sent to peers.
type RoomEvent struct {
    Type    string      `json:"type"`
    Payload interface{} `json:"payload,omitempty"`
}
```

---

### File: `room/room.go`

The core: membership + subscription graph + track arrival handling.

```go
package room

import (
    "sync"

    "your-module/media"
    "github.com/pion/webrtc/v4"
)

// Room holds all peers and manages the forwarding topology.
type Room struct {
    mu    sync.RWMutex
    ID    string
    peers map[string]*Peer

    // Track routing: maps "peerID:trackID" → SimulcastTrack (or simple forward handle)
    simulcastTracks map[string]*media.SimulcastTrack
}

// NewRoom creates an empty room.
func NewRoom(id string) *Room

// AddPeer registers a peer and sets up the PeerConnection event handlers
// (OnTrack, OnICECandidate, OnConnectionStateChange).
func (r *Room) AddPeer(peer *Peer)

// RemovePeer disconnects a peer, cleans up their tracks, and notifies others.
func (r *Room) RemovePeer(peerID string)

// GetPeer returns a peer by ID, or nil if not found.
func (r *Room) GetPeer(peerID string) *Peer

// PeerCount returns the number of connected peers.
func (r *Room) PeerCount() int

// HandleTrack is called when a remote track arrives from a peer's PeerConnection.
// It determines whether the track is simulcast (has RID) or simple, sets up the
// appropriate forwarding, and subscribes all other peers.
func (r *Room) HandleTrack(peer *Peer, remote *webrtc.TrackRemote, receiver *webrtc.RTPReceiver)

// SwitchLayer tells the room to change the simulcast layer being forwarded for
// a given peer's track. targetPeerID="*" means all simulcast tracks.
func (r *Room) SwitchLayer(targetPeerID string, layer media.LayerID)

// Negotiate sends a renegotiation offer to a specific peer (used when adding
// new tracks to their connection).
func (r *Room) Negotiate(peer *Peer)

// BroadcastMetrics collects metrics from all active tracks and pushes a metrics
// event to all peers. Called periodically.
func (r *Room) BroadcastMetrics()
```

---

### File: `room/manager.go`

Room lifecycle — create, get, delete rooms.

```go
package room

import "sync"

// Manager tracks all active rooms.
type Manager struct {
    mu    sync.RWMutex
    rooms map[string]*Room
}

// NewManager creates an empty room manager.
func NewManager() *Manager

// GetOrCreate returns an existing room or creates a new one.
func (m *Manager) GetOrCreate(roomID string) *Room

// Remove deletes a room (called when last peer leaves).
func (m *Manager) Remove(roomID string)

// Get returns a room by ID, or nil.
func (m *Manager) Get(roomID string) *Room
```

---

## Package: `signaling`

**Responsibility:** WebSocket transport. Upgrades HTTP to WS, reads JSON messages,
dispatches them to the room layer, and sends responses back. Zero media logic.

### File: `signaling/messages.go`

Wire format — what the browser sends and receives over WebSocket.

```go
package signaling

import "encoding/json"

// InboundMessage is the envelope for all messages from a client.
type InboundMessage struct {
    Type         string          `json:"type"`
    RoomID       string          `json:"roomId,omitempty"`
    PeerID       string          `json:"peerId,omitempty"`
    SDP          string          `json:"sdp,omitempty"`
    Candidate    json.RawMessage `json:"candidate,omitempty"`
    TargetPeerID string          `json:"targetPeerId,omitempty"`
    Layer        string          `json:"layer,omitempty"`
}

// OutboundMessage is the envelope for all messages to a client.
type OutboundMessage struct {
    Type      string      `json:"type"`
    SDP       string      `json:"sdp,omitempty"`
    Candidate interface{} `json:"candidate,omitempty"`
    StreamIDs []string    `json:"streamIDs,omitempty"`
    Metrics   interface{} `json:"metrics,omitempty"`
}
```

---

### File: `signaling/handler.go`

HTTP handler + WebSocket read loop.

```go
package signaling

import (
    "net/http"

    "your-module/room"
    "github.com/gorilla/websocket"
)

// Handler handles WebSocket connections for signaling.
type Handler struct {
    upgrader websocket.Upgrader
    rooms    *room.Manager
}

// NewHandler creates a signaling handler wired to the given room manager.
func NewHandler(rooms *room.Manager) *Handler

// ServeHTTP upgrades the connection and starts the read loop.
// This is the http.Handler implementation mounted at /ws.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request)

// readLoop reads JSON messages from the WebSocket and dispatches them.
// Runs in a goroutine per connection. Blocks until the connection closes.
func (h *Handler) readLoop(conn *websocket.Conn)
```

---

## File: `main.go`

Wires everything together. Thin as possible.

```go
package main

import (
    "net/http"

    "your-module/room"
    "your-module/signaling"
)

func main() {
    // 1. Create room manager
    rooms := room.NewManager()

    // 2. Create signaling handler, pass it the room manager
    sigHandler := signaling.NewHandler(rooms)

    // 3. Mount routes
    http.Handle("/ws", sigHandler)
    http.Handle("/", http.FileServer(...))  // serve index.html

    // 4. Start HTTP server
    http.ListenAndServe(":8080", nil)
}
```

---

## Build Order (layer by layer, outside-in)

This is the order we'll implement, one package at a time:

| Step | Package | Why this order |
|------|---------|---------------|
| 1 | `config` | Zero deps, takes 2 minutes, gives us named constants from the start |
| 2 | `media/forwarder.go` | Standalone RTP copy loop — no room/signaling deps |
| 3 | `media/metrics.go` | Just type definitions |
| 4 | `media/simulcast.go` | Depends only on config + webrtc |
| 5 | `media/pli.go` | Depends only on config + webrtc |
| 6 | `room/peer.go` + `room/events.go` | Type definitions for the room layer |
| 7 | `room/room.go` | Core logic — uses media package to route tracks |
| 8 | `room/manager.go` | Thin wrapper over a map of rooms |
| 9 | `signaling/messages.go` | Wire format types |
| 10 | `signaling/handler.go` | WebSocket → room bridge |
| 11 | `main.go` | Glue |
| 12 | `signaling/index.html` | Copy + verify compatibility (no changes needed) |

---

## Key Design Decisions

### 1. Callback injection for transport (`Peer.Send`)
The room pushes bytes to the peer via `Peer.Send`. The signaling layer sets this
to write to the WebSocket. If you later add gRPC or QUIC, you change the callback —
not the room.

### 2. Media owns its goroutines
Every `ForwardRTP` loop, every PLI periodic sender — started and stopped by the media
package. The room calls `HandleTrack` and gets back a track handle. It doesn't manage
goroutines.

### 3. Room owns the subscription graph
"Peer A's track should go to peers B, C, D" — that decision is in the room. The media
package doesn't know who else is in the room.

### 4. Metrics flow upward via snapshots
`SimulcastTrack.SnapshotMetrics()` returns data. The room calls it periodically and
broadcasts to peers. When Project 5 arrives, you add a `metrics/` package that
receives these snapshots and pushes to Prometheus — without touching media internals.

### 5. No interfaces yet
We're not building a framework. Concrete types are fine until you have 2+ real
implementations. If Project 6 (horizontal scaling) needs to swap the room manager
for a distributed one, *that's* when you extract an interface — not now.

---

## What This Structure Enables for Future Projects

| Future milestone | How this structure helps |
|------------------|------------------------|
| Project 5 (Telemetry) | Add `metrics/collector.go` that reads from `SnapshotMetrics()`. Room already exposes this. No refactoring needed. |
| Adaptive bitrate | Add logic inside `room.SwitchLayer` that auto-selects based on receiver stats. Media package already supports switching. |
| Project 6 (Horizontal scaling) | Room Manager becomes the abstraction point. Replace in-memory map with Redis-backed routing. `signaling` and `media` untouched. |
| TURN integration | Just change the ICE config in `config/`. Nothing else knows or cares. |
| Recording/Egress | Add a "recorder" as a special Peer that only receives tracks. Room treats it like any subscriber. |

---

## File Tree (final state)

```
project-4-minimal-sfu-refactor/
├── main.go
├── go.mod
├── go.sum
├── config/
│   └── config.go
├── media/
│   ├── forwarder.go
│   ├── simulcast.go
│   ├── pli.go
│   └── metrics.go
├── room/
│   ├── peer.go
│   ├── events.go
│   ├── room.go
│   └── manager.go
├── signaling/
│   ├── handler.go
│   ├── messages.go
│   └── index.html
└── ARCHITECTURE.md    ← this file
```

---

## Next Step

Once you've read and internalized this, we start at Step 1 (`config/config.go`)
and work our way down. Each step, I'll give you the implementation to hand-write,
explain why each line exists, and connect it back to this overall picture.

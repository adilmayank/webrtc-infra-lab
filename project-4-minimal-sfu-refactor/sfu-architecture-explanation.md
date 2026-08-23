## 1. Project Overview

This project is called a Minimal SFU which implements very stripped down yet core functionality of a production grade SFU. My agenda was to write my own SFU to understand how big projects like Livekit is working internally. Although this project only scratches the surface of what's being used in the industry, its my first step towards understanding and entering the field of real time communication, with Golang's ecosystem.

This SFU supports simulcast, but do not support any ABR algorithm to selectively forward most appropriate layer to the peer. Quality layer switching capability is manual and is applied globally on a room level.

---

## 2. Goals & Non-Goals

**Goals**
- [x] Forward audio between N participants in a room
- [x] Forward video between N participants in a room
- [x] Handle keyframe requests on participant join
- [x] Support simulcast (receive multiple layers, forward one)
- [x] Basic telemetry hooks (ties to Project 5 — leave as a stub here)

**Non-Goals**

- **Transcoding:** At this point, I am only interested in forwarding whatever I am getting from the peers. Without having to decode/re-encode media packets, saving CPU usage. Moreover the transcoding the incoming media stream would make the solution more like an MCU (Multipoint Control Unit) which is architecturally a different thing.

- **TURN relay implementation:** So far the development and testing of the SFU is being done only on local environment and for non production use. Since all the peers are behind the same NAT (type `host`), the problem which TURN servers are there for, does not arise. In production I would use something like coturn or some other managed TURN services like Cloudflare TURN servers.

- **Recording/Egress:** This was intentionally kept out. Main agenda of this project was to build a minimal SFU that performs the core sfu responsibility. While I know Recordings and Egress are a part of such SaaS products, this is just another layer on top of the SFU. In an earlier project I had already implemented the hls livestreaming part which was done by piping the media stream to a ffmpeg process, which would generate and store hls live stream, so the concept is already touched by me in an earlier project.

- **Authentication/authorization:** Authentication/Authorisation is just a forward gating layer which manages whether a certain peer is authorized to join the asked room. SFU's core logic is independent of this gating, its only concern is to manage the peers and forward the media. Authentication can be implemented using JWTs for each candidate. I have deliberately left it out of this project this does not add to project value of building things core to an SFU.

---

## 3. High-Level Architecture

### Join Flow: participant joins a room → sees other participants' video

When candidate joins the room (the abstraction of grouping together peers belonging to a certain group), below is the sequential list of actions:

1. Frontend client generates `peer_id` and `room_id` and sends a join request over websocket to the server.
2. Signaling delegates the join request to the room package. On SFU side:
   - `getOrCreate` a room with that room name
   - Adds peer to that room, which includes:
     - Creating the local peer connection
     - Setup ontrack callback handler
     - Setup ICE candidate callback
     - Setup connection state handler
     - Start a metrics loop once for this room (managed by `metricsOnce` of type `sync.Once`)
   - Subscribe to existing peers:
     - Loop over all output tracks and add them to this peer's peer connection
     - Start reading from the sender's buffer to drain RTCP packets
     - Do not renegotiate — client is about to send an offer (prevents offer glare)
3. As the other peer's local track is added to this peer's peer connection, the `ontrack` handler in frontend fires with the received track and this peer starts seeing the other peer.

### Process/Service Boundaries

On the media server side, we have:

- **Signaling** — uses websocket to setup bidirectional communication between frontend client and media server.
- **SFU core** — separate from signaling implementation. Further divided into:
  - **Room** — manages peer memberships
  - **Media** — responsible for forwarding single track or handling simulcast tracks
  - **Metrics** — technically bound to media layer but will be a separate service later

---

## 4. Core Components

### 4.1 Room / Session Manager

**Room representation & lifecycle:**

A room is an abstraction of peers (participants) membership in a group, which determines who receives each other's media.

| State | Trigger |
|-------|---------|
| Created | First peer tries to enter a room with no prior roomId registered in the SFU |
| Active | At least one peer has joined successfully; assuming every joining peer is publishing media, the SFU is getting media tracks (audio or audio/video) |
| Empty → Destroyed | Last candidate leaves; after resource cleanup, entry is removed from room map and the room itself is cleaned up |

**Participant disconnect vs explicit leave:**

Two pathways trigger peer removal:

1. **WebSocket close** — `conn.ReadMessage()` produces an error in the read loop → implicit leave
2. **PeerConnection state change** — fires `Failed` or `Disconnected` → implicit leave

No explicit leave UI exists currently. In both cases, `currentRoom.RemovePeer(peerID)` is called which internally:

- Removes peer from the room's peer map
- Finds all output tracks of this peer being forwarded to others, collects their streamIDs, and deletes them from the room-level map
- Cleans up all peer resources: stops PLI request bursts, stops forwarding simulcast out track (if any), closes peer connection
- Sends remaining peers the streamIDs so they can remove the corresponding video elements

---

### 4.2 Peer Connection Handling

**Internal representation:**

Each participant's connection is represented with a `Peer` struct containing:

| Field | Purpose |
|-------|---------|
| `ID` | Unique identifier for tracking/logging |
| `PC` | `webrtc.PeerConnection` — manages all peer connection logic (callbacks, negotiation) |
| `Send` | Transport-agnostic function to deliver messages back to this peer; injected by signaling layer |
| `mu` | Mutex protecting shared state accessed by multiple goroutines |
| `simulcastTracks` | Map keyed by trackID → `SimulcastTrack` for each video stream |
| `pliSenders` | Map keyed by `trackID:layer` → `PLISender` managing periodic keyframe requests |

The peer manages its own cleanup, not the room.

**Concurrency model per peer:**

- **Shared state protection:** `mu` (sync.Mutex) protects `simulcastTracks` and `pliSenders` maps from concurrent goroutine access.
- **Goroutines spun up per peer** (all stopped on leave — PLISenders have `Stop()`, metricsLoop has `stopMetrics` channel, Read returns error after `PC.Close()`):
  - One-time metrics loop goroutine per room (triggered on first peer join, runs till room empties)
  - One read loop per peer to listen from their websocket connection
  - Per incoming track:
    - A forwarding loop from remote source to local destination track that other peers receive
    - A PLI sender goroutine so each source track keeps generating keyframes
  - Per subscription to another peer's track:
    - An RTCP drain goroutine for the RTPSender that this peer gets when a track is added to its peer connection

---

### 4.3 Track Management

**Incoming → outgoing routing:**

The room struct has a map named `outputTracks` (keyed by trackID) storing `OutputTrack` variables — the subscribable tracks currently in this room. Any new peer who joins registers their corresponding output tracks and subscribes to all existing ones.

Mechanism:

1. When a new peer joins, their `ontrack` handler is registered.
2. When a new track is received (simulcast or normal video), a corresponding `TrackLocalStaticRTP` is created from the remote track's mime type.
3. This local track + remote track + peer are bundled into an `OutputTrack` and stored in the room's `outputTracks` map keyed by `localTrack.ID()`.
4. A loop over all other peers adds this local track to their peer connections via `.AddTrack()`, triggering renegotiation.
5. The joining peer also subscribes to existing output tracks via the same mechanism, but without renegotiation (their own offer will cover these).

**Subscription model:**

Everyone gets everything — no selective forwarding logic.

`outputTracks` is the single source of truth for what's subscribable in a room. Each entry contains publishing peer, corresponding remote track, and the local track that every peer actually subscribes to. Irrespective of whether the publisher is sending simulcast layers or a single track, `outputTracks` only registers what's actively being published. The subscribing peer doesn't know or care about the source type.

---

### 4.4 Keyframe / PLI Handling

**When is a PLI sent, and to whom?**

PLI is sent to the producer for a specific media track (identified by SSRC). Two occasions:

1. **On track arrival** — when a new video track is received (simple or simulcast layer), a PLI sender is created. It starts a goroutine (cancellable via context) that periodically sends a PLI request every `config.PLIInterval` (5s) to the publisher via `WriteRTCP` on their peer connection.

2. **On layer switch** — `SwitchLayer` triggers a burst of PLI requests for the newly active layer. This is reactive vs the periodic pre-emptive approach — without it, video might stay stuck for up to 5 seconds waiting for the next natural keyframe.

**Verification:**

Even when I disabled periodic PLI, the browser's encoder was producing keyframes at its natural rate — so the second peer could see the first peer's video, but with noticeable delay. With periodic PLI, it was instantaneous. This especially helped when the third peer joined late and didn't have to wait for natural keyframe generation. Removing PLI bursts verified that upon layer switching there was a certain delay in getting the actual video of the newly switched layer. Network congestion (which could cause RTCP packet loss) wasn't encountered during local testing.

---

### 4.5 Simulcast

**How quality layers are received and selected:**

Unlike normal forward-only video tracks, simulcast tracks need special tracking per peer. For subscribing peers, they have no knowledge whether other peers are producing single or multiple quality layers — they only receive whatever tracks are in `outputTracks`.

*Receiving:*

- Quality layer tracks arrive in the `ontrack` handler like normal tracks, but with a RID (`"h"`, `"m"`, or `"l"`).
- Handled by `handleSimulcastTrack`:
  - First layer → creates a `SimulcastTrack` struct (holds sender identity, output track, active layer info), registered in peer's `simulcastTracks` map keyed by streamID.
  - Creates an output track that other peers subscribe to.
  - Adds the layer to the `SimulcastTrack`'s layer map and registers a PLI sender for it.

*Selection (switching):*

- Triggered by a `switchLayer` websocket message (currently a per-room operation — any peer can trigger it; kept simple for PoC).
- Sets `activeLayer` to the target layer and `switching` to true on the `SimulcastTrack`.
- On the next packet from the new layer, offsets are recalculated to keep timestamp and sequence number continuous for subscribers (from their POV, only quality changes, not these numbers).
- The underlying read loop runs for all registered layers simultaneously, but only forwards for the active layer and only after a valid VP8 keyframe has arrived. This is critical on switch — until the keyframe arrives, packets are drained without forwarding.

**Layer selection policy:**

No automated policy. As a proof of concept, layer switching is a per-room operation with UI controls. ABR-based per-peer selection is a future exploration.

---

## 5. Concurrency Model

*(This section is where your Go background should genuinely differentiate the writeup)*

**Goroutine-per-peer? Goroutine-per-track? Worker pool?**

Goroutines are spawned at different level of abstractions. Below is the list
- One goroutine per peer for reading from the websocket connection
- One goroutine per room for sending metrics over to all the peers connected to that room over transport method (websocket here)
- One goroutine per incoming track for rtp forwarding
- One goroutine per incoming track/layer for requesting keyframe from publisher
- One goroutine per outbound subscription to drain rtcp frames

In a 3-peer room where each publishes audio + video (simulcast layer), following are the goroutines - 
- 1 metrics per room
- 1 websocket connection per peer, i.e. 3
- 1 forwarding loop per incoming audio track per peer, i.e. 3 
- 1 simulcast read loop per incoming quality track per peer, i.e. 3 quality tracks for 3 peers i.e. 9 
- 1 PLI sender per layer per peer, i.e. 9,
- 1 drain goroutine per subscribing peer's sender. For a peer who subscribes to two other peer's audio and video its 4 drain goroutine per peer, so 12 goroutines for 3 peers.

Total Goroutines = 1 + 3 + 3 + 9 + 9 + 12 = ~37

**How is shared state protected?**

Shared state is protected by using mutexes and RWMutexes. Below is the list - 
- mu sync.RWMutex per room
- metricsOnce sync.Once to ensure metrics loop start only once
- mu sync.Mutex per websocket connection
- mu sync.RWMutex to protect the rooms map in Manager struct
- mu sync.Mutex to protect a SimulcastTrack and PLISenders
- seqMu sync.Mutex to protect read/writes to the sequence field of simulcast track

**Design choices from earlier projects:**

Same lifecycle pattern as my job orchestrator - goroutines are spawned with a clear termination signal, parent doesn't need to track them, they exit when their input closes or context cancels

---

## 6. Known Limitations

**Scale ceiling:**

- Single node, single process. No horizontal scaling,
- I'e so far only tested with 4 peer locally,
- Goroutine count grows roughly at O(peer<sup>2</sup> x tracks)

**What breaks under packet loss / poor network conditions?**

- No NACK handling, i.e. if a packet is lost between publisher and sfu, publisher has no way of getting that lost packet
- No bandwidth estimation / congestion control, sfu sends to susbcribers whatever publisher sends. There is no mechanism through which a subscriber can select a particular layer (in case of simulcast),
- No ACK mechanism for RTCP
- If PeerConnection state goes to failed/disconnected, peer is removed and there is no reconnect.
- No jitter buffer on the SFU side.

**What's stubbed vs real:**

- Auth not implemented
- TURN not implemented
- Telemetry partially implemented
- Simulcast layer policy, no ABR
- Reconnection not implemented
- Graceful shutdown not implemented

---

## 7. What I'd Do Differently / Next Steps

**If I had more time:**

If I had more time, I would have implemented Adaptive Bitrate algorithms to consider few factors like bandwidth congestion and viewport size of the video and a custom leave button to trigger graceful leaving of a peer.

**Link forward to Projects 5 & 6:**

Project 5 and 6 are adding telemetry and horizontal scaling to the application.

---

## 8. How to Run It

```
cd project-4-minimal-sfu-refactor
go run .
```

---

## Appendix: Things to Capture While Building

Keep a running scratch log (even just a `NOTES.md` in the repo) of:
- Bugs that took a long time to track down and *why* — these make the best interview stories
- Any moment you had to re-read the WebRTC spec or RFC to understand something — note what and why, it's a good signal of rigor
- Design decisions you reversed mid-build — these are more interesting than the final state alone
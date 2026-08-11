# webrtc-infra-lab

A weekend-driven, project-based path into WebRTC infrastructure engineering —
specifically targeting SFU / media-core engineering roles at developer infra
companies (LiveKit, Daily, 100ms, and similar).

Background: Go developer (backend infra, Kubernetes, FastAPI by day), mechanical
engineering background, self-taught. Learning style: build first, theory on demand —
not RFC-first.

Target specialization: **Media/SFU core** — routing, codecs, bandwidth estimation,
participant lifecycle, telemetry.

---

## How to use this README

Each project below has a **Goal**, an **Out of Scope** list (so you don't quietly
scope-creep into a 6-month project), and a **Definition of Done** — a checklist that
should be objectively true/false, not a feeling. If you can't check every box, it's
not done yet, even if it "mostly works."

Work top to bottom. Don't skip ahead to the SFU before Projects 0-3 are done — the
debugging instincts from the early projects are what make Project 4 tractable instead
of overwhelming.

---

## Project 0 — Networking Warmup

**Goal:** Get comfortable with raw sockets in Go before anything WebRTC-specific.

**Build:** A UDP echo server + client, and a basic TCP chat server (multiple clients,
broadcast messages) — both in Go.

**Out of scope:** Anything resembling WebRTC concepts. No SDP, no ICE. Pure sockets only.

**Definition of Done:**
- [x] UDP echo server runs, client sends a message, gets it echoed back
- [x] TCP chat server accepts 3+ simultaneous client connections
- [x] A message from one TCP client is broadcast to all other connected clients
- [x] You can explain, out loud, without notes, why UDP doesn't guarantee delivery and TCP does
- [x] You can explain what a "port" actually is at the OS level, in your own words

*Skip this project only if all 5 boxes above are already trivially true for you.*

---

## Project 1 — STUN Client from Scratch

**Goal:** Understand NAT traversal concretely, not abstractly.

**Build:** A minimal STUN client in Go. Sends a binding request to a public STUN
server (e.g. `stun.l.google.com:19302`), parses the response, extracts your public
IP/port.

**Out of scope:** TURN. ICE candidate pairing logic. Just the STUN binding request/response.

**Definition of Done:**
- [ ] Your program prints your actual public IP + port, confirmed against an external
      "what's my IP" check
- [ ] You manually parse the STUN response bytes yourself (not using a STUN library) —
      this is the point of the project
- [ ] You can explain why the IP your program sees locally is different from the IP
      the STUN server sees
- [ ] You can explain, in your own words, why this is a problem WebRTC has to solve
      at all

---

## Project 2 — Signaling Server

**Goal:** Understand the offer/answer + ICE candidate exchange by building the layer
WebRTC deliberately leaves undefined.

**Build:** A WebSocket-based signaling server in Go. Supports room creation,
participant join/leave, and relays SDP offers/answers and ICE candidates between
exactly two browser peers.

**Out of scope:** More than 2 participants. Authentication. Persistence (in-memory
room state is fine).

**Definition of Done:**
- [x] Two separate browser tabs can both connect to your signaling server and join
      the same room
- [x] An SDP offer sent from tab A is relayed by your server and received by tab B
      (visible in console logs)
- [x] ICE candidates are relayed bidirectionally between the two tabs
- [x] If tab B closes, your server detects the disconnect and could notify tab A
      (doesn't need a UI — a log line is enough)
- [x] You can explain why signaling isn't part of the WebRTC spec itself

---

## Project 3 — Two-Peer Video Call (P2P, no SFU)

**Goal:** See a working end-to-end call using your own signaling server, validating
Project 2 and giving you a working baseline before introducing SFU complexity.

**Build:** Connect two browser tabs via native browser WebRTC APIs, using your
Project 2 signaling server for the handshake. Pure peer-to-peer — no media server
involved.

**Out of scope:** Any server-side media handling. This is P2P only.

**Definition of Done:**
- [x] Two browser tabs (ideally two different devices/networks) establish a live
      video + audio call
- [x] You've recorded a short screen capture/demo of it working — this becomes the
      demo asset for Projects 2 and 3 combined
- [x] You've tested it across two different networks (e.g. laptop on wifi + phone on
      mobile data) at least once, and observed what happens when P2P struggles
      (this sets up *why* TURN/SFU matter later)

---

## Project 4 — Minimal SFU (centerpiece project)

**Goal:** Build the core artifact of your portfolio — a Go SFU using `pion/webrtc`
that forwards media between 3+ participants in a room.

**Build:** See `04-minimal-sfu/sfu-architecture-template.md` for the architecture doc
to fill in as you go. Don't fill it in upfront — let the document follow the build.

**Out of scope (explicitly, for v1):**
- Transcoding (forwarding only)
- TURN server implementation (use existing `coturn`, don't build your own)
- Recording/Egress
- Real authentication (a stub is fine)

**Definition of Done — broken into milestones, each independently checkable:**

*Milestone A — Audio-only, multi-peer:*
- [x] 3+ participants in a room, audio forwarded between all of them
- [x] A 4th participant joining mid-call hears existing participants immediately

*Milestone B — Video forwarding:*
- [ ] Video tracks forwarded the same way audio is
- [ ] A newly-joined participant receives a fresh keyframe (not a frozen/green
      screen on join) — this means your PLI handling works

*Milestone C — Simulcast (stretch, but worth attempting):*
- [ ] Client sends 2+ quality layers
- [ ] SFU selects and forwards one layer
- [ ] You can demonstrate switching which layer is forwarded (even manually
      triggered, doesn't need to be automatic/adaptive yet)

*Milestone D — Documentation:*
- [ ] `sfu-architecture-template.md` is fully filled in, including the Known
      Limitations and What I'd Do Differently sections — written honestly
- [ ] A `NOTES.md` exists with at least 3 real debugging stories from the build
- [ ] A short demo video/GIF exists showing 3+ participants in a call

**This project is not done until all of Milestones A, B, and D are checked.**
Milestone C is a stretch goal — attempt it, but don't let it block calling the
project complete if A/B/D are solid.

---

## Project 5 — Telemetry Pipeline

**Goal:** Instrument your SFU with the observability layer that's your stated area
of interest — this is what differentiates your portfolio from "yet another SFU clone."

**Build:** Client-side `getStats()` polling, sent to your server (reuse the
signaling WebSocket), logged per-participant (jitter, packet loss %, bitrate,
round-trip time), visualized in a simple dashboard (Grafana/Prometheus or a custom
minimal HTML dashboard — either is fine).

**Out of scope:** Long-term storage/historical analytics. This is live/session-scoped
telemetry only, not a full data warehouse.

**Definition of Done:**
- [ ] Each connected participant's `getStats()` data is polled client-side at a
      regular interval (e.g. every 2s)
- [ ] Stats are transmitted to your server and logged per-participant, per-room
- [ ] A dashboard (however simple) shows live jitter/packet-loss/bitrate for an
      active call
- [ ] You can intentionally degrade network conditions (e.g. using browser dev
      tools network throttling) and watch the dashboard numbers visibly change
- [ ] You've written a short note on what metrics you'd alert on in a real
      production system, and why

---

## Project 6 — Horizontal Scaling Demo

**Goal:** Bridge your existing Kubernetes/infra background directly into this
domain — this is where your day-job skills become a genuine differentiator.

**Build:** Run 2+ SFU pods in Kubernetes, with a simple routing layer assigning
rooms to specific nodes, plus graceful handling of a pod restart (active sessions
reconnect cleanly rather than dying silently).

**Out of scope:** Auto-scaling policies, multi-region deployment, production-grade
load balancing algorithms. This is a scaling *demo*, not a scaling *product*.

**Definition of Done:**
- [ ] 2+ SFU pods running in a local/cloud Kubernetes cluster
- [ ] A routing mechanism assigns a new room to a specific pod (even a simple
      hash-based or round-robin assignment is fine)
- [ ] You can kill one pod mid-call and observe what happens to affected
      participants (ideally: reconnect; acceptable for v1: documented failure mode)
- [ ] You've written a short doc on what "graceful" would actually require in
      production (session draining, connection migration) even if you didn't fully
      implement it — understanding the gap is itself a valid deliverable

---

## After Project 6

At this point you have a sequenced, documented body of work that demonstrates:
fundamentals → protocol understanding → core SFU engineering → observability →
distributed systems scaling. That arc, told well (top-level README narrative +
individual project READMEs + a couple of blog posts along the way), is a genuine
portfolio — not a pile of disconnected toy repos.

Revisit this roadmap if priorities shift, but resist the urge to add new projects
before finishing this list. Breadth without depth doesn't read as strong in this
specific niche — depth on the SFU + telemetry projects (4 and 5) matters more than
having 10 shallow repos.

---

## Progress Log

*(Update this as you go — even a one-line entry per weekend keeps momentum visible
to yourself)*

- [ ] Project 0 — started: ____ / done: ____
- [ ] Project 1 — started: ____ / done: ____
- [ ] Project 2 — started: ____ / done: ____
- [ ] Project 3 — started: ____ / done: ____
- [ ] Project 4 — started: ____ / done: ____
- [ ] Project 5 — started: ____ / done: ____
- [ ] Project 6 — started: ____ / done: ____

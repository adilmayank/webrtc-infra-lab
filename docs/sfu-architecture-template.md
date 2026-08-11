# Minimal SFU in Go — Architecture & README Template

> This is a structural scaffold, not a finished document. Sections are intentionally
> left as prompts/questions rather than filled-in answers — fill these in as you build,
> since the actual decisions should come from your own implementation experience.

---

## 1. Project Overview

*(Write this last, once you know what you actually built)*

- One-paragraph description: what does this SFU do, what does it deliberately NOT do
- Why you built it (1-2 lines — ties back to your career direction, keep it short)
- Demo link / GIF / video (this matters more than text for this kind of project)

---

## 2. Goals & Non-Goals

**Goals** *(fill in as you scope each milestone)*
- [ ] Forward audio between N participants in a room
- [ ] Forward video between N participants in a room
- [ ] Handle keyframe requests on participant join
- [ ] Support simulcast (receive multiple layers, forward one)
- [ ] Basic telemetry hooks (ties to Project 5 — leave as a stub here)

**Non-Goals** *(be explicit — this shows engineering maturity to a reviewer)*
- Transcoding? (probably not — note why)
- TURN relay implementation? (probably using coturn, not building your own — note why)
- Recording/Egress? (likely out of scope — note why)
- Authentication/authorization model? (note what's stubbed vs real)

---

## 3. High-Level Architecture

*(This is the section to fill in with a diagram once the shape is real)*

- [ ] Diagram: signaling server, SFU core, participants, TURN server (boxes and arrows is enough)
- [ ] One paragraph: how does a participant go from "joins a room" to "sees other participants' video"?
- [ ] What process/service boundaries exist? (signaling vs SFU — same process or separate?)

---

## 4. Core Components

*(Leave as headers with guiding questions — answer once you've actually built each piece)*

### 4.1 Room / Session Manager
- How are rooms represented? What's the lifecycle (create → active → empty → destroyed)?
- What happens on participant disconnect vs explicit leave?

### 4.2 Peer Connection Handling
- How is each participant's connection represented internally?
- What's the concurrency model per peer? (this is where your orchestrator experience should show up — note the parallel explicitly when you write this)

### 4.3 Track Management
- How do incoming tracks get routed to outgoing tracks for other participants?
- How is subscription handled — does everyone get everything, or is there selective forwarding logic?

### 4.4 Keyframe / PLI Handling
- When is a PLI sent, and to whom?
- How did you verify this actually works (this is a good place for a debugging story)

### 4.5 Simulcast (if reached)
- How are quality layers received and selected?
- What's the policy for choosing which layer to forward? (static, or based on something — bandwidth, viewport, etc.)

---

## 5. Concurrency Model

*(This section is where your Go background should genuinely differentiate the writeup)*

- Goroutine-per-peer? Goroutine-per-track? Worker pool?
- How is shared state (room participant list, track subscriptions) protected? (mutexes, channels, actor-pattern?)
- Any deliberate design choices carried over from your earlier concurrent job orchestrator project — worth a short callout if true, reviewers like seeing connected thinking across your portfolio

---

## 6. Known Limitations

*(Write honestly — this is more credible to a hiring engineer than pretending it's production-grade)*

- Scale ceiling (single node? tested with how many concurrent peers?)
- What breaks under packet loss / poor network conditions?
- What's stubbed vs real (auth, TURN, telemetry)

---

## 7. What I'd Do Differently / Next Steps

*(This section signals seniority — shows you can self-critique)*

- If you had more time, what's next — horizontal scaling? Better bandwidth estimation? Real telemetry?
- Explicit link forward to Project 5 (telemetry) and Project 6 (scaling) if applicable — shows this is part of a deliberate sequence, not a one-off

---

## 8. How to Run It

*(Fill in last — standard setup/run instructions, keep it short)*

```
# placeholder — actual commands once the project has a shape
```

---

## Appendix: Things to Capture While Building (don't lose these)

Keep a running scratch log (even just a `NOTES.md` in the repo) of:
- Bugs that took a long time to track down and *why* — these make the best interview stories
- Any moment you had to re-read the WebRTC spec or RFC to understand something — note what and why, it's a good signal of rigor
- Design decisions you reversed mid-build — these are more interesting than the final state alone


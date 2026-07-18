# Raven Drive Log

Field-test results from real drives. Newest first. The point is to capture what
the pipeline actually did on the road — not what it does on a bench — plus the
subjective read that logs can't show.

---

## Drive 1 — 2026-07-18 (first real-world drive)

**The open question this answered:** does Raven work off the home Wi-Fi — over
cellular, through Tailscale, screen-off — for a full drive? **Yes.**

### Setup

- iPhone 17 (iOS 26.5) on **cellular**, not home Wi-Fi. Tailscale showed the
  phone at a carrier IP (`172.58.x`, T-Mobile) with a **direct** connection to
  the Mac (`100.64.0.1`) — not relayed through Tailscale's DERP servers.
- Test payload: 10 custom messages injected from the Mac, one every ~2 minutes
  over ~18 minutes (a mix of status confirmations and prose, so it was pleasant
  to listen to). Injected straight into the queue, so the whole
  synthd → writer → HLS → phone path was exercised.

### Results (from `logs/events.jsonl`)

| Metric | Result |
|---|---|
| Clips synthesized | 12 (10 drive messages + 2 diagnostics) |
| Synthesis backend | **Kokoro on every clip — zero `say` fallbacks** |
| Synth latency | min **387 ms**, median **636 ms**, max **3312 ms** |
| Short messages (~150–220 chars) | **400–800 ms** to synthesize |
| Long paragraphs (~1200–1400 chars) | ~3 s (scales with length, as expected) |
| Delivery cadence | even — clips emitted on their ~120 s schedule |
| Dropped / expired | **none** — queue empty after, nothing held past its TTL |
| Connection | held for all 12 emits (the writer only emits to a live listener) |

The strongest single signal: the writer emits a clip **only** when the phone is
actively polling the stream. All 12 emitted — so the phone was connected over
cellular for every one, across the whole drive. Any dead zones reconnected fast
enough that nothing was lost.

### What we learned

1. **Cellular + Tailscale works, direct.** The core premise is proven on the
   actual road. The direct (non-relayed) connection means latency feels local
   even on the highway.
2. **Synthesis is not the bottleneck.** Sub-second for normal replies. The
   latency the driver *feels* is not the speaking — it's **when we tap**: the
   `Stop` hook fires only when the whole turn completes, which is too late for a
   turn with tool calls. This is the #1 thing to fix. The fix is already scoped:
   [`SCOPE_LIVE_NARRATION.md`](SCOPE_LIVE_NARRATION.md) — speak completed text
   blocks *during* the turn, before Stop. (Asif independently flagged this on the
   drive: "start speech before the hook stops.")
3. **Long replies are fragile on a live stream.** A ~3-minute reply is exposed:
   it's a live broadcast with no rewind, so a dead zone mid-reply drops the rest
   (the app reconnects at the live edge by design, to avoid replaying stale
   audio). Mitigations: **summarization** (keep replies short →
   [`SCOPE_SUMMARIZATION.md`](SCOPE_SUMMARIZATION.md)) shrinks the exposure
   window, and the **transcript** is the always-available fallback — read what
   you missed by ear (now formatted).
4. **Two "I didn't hear it" scares, both non-bugs.** (a) A rapid back-and-forth
   stretch went unheard because those turns were *interrupted* — `Stop` never
   fired (the documented interrupted-turn limit; live narration closes it).
   (b) One reply "wasn't heard" because the phone **volume was low** — the logs
   showed it queued, synthesized, and emitted correctly. Both validate the
   read-the-state-back discipline: the logs told the truth each time.

### Subjective read (Asif)

"It's not bad." First real drive. The one felt gap was start-latency (tap on
Stop = too late), not audio quality or reliability.

### Next builds this drive motivates (in priority order)

1. **[Streaming synthesis](SCOPE_STREAMING_SYNTHESIS.md)** — first word in ~0.3s
   instead of waiting for the whole reply to render. Self-contained, low risk.
2. **[Live narration](SCOPE_LIVE_NARRATION.md)** — the "start before Stop" fix;
   also closes the interrupted-turn gap. Higher risk (rewires who owns speaking).
3. **[Summarization](SCOPE_SUMMARIZATION.md)** — shorten long replies so they're
   less exposed to dead zones and less tedious.

### Not yet measured

- Whether the stream survived an *actual* multi-minute dead zone (no long tunnel
  on this route; reconnects were fast enough to lose nothing).
- Battery draw over a drive (deliberately not instrumented — CarPlay/USB charges
  the phone, so it doesn't change any decision).
- Perceived start-latency and voice pacing *at speed* — the tuning inputs a log
  can't capture; gather on future drives.

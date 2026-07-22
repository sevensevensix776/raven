# Raven — Future Work

Raven's core loop is **done**: you talk to Claude Code via its Remote Control app
(iOS dictation), Claude works on your Mac, and Raven speaks the replies back to
your phone — live, during the turn, over Tailscale. For the primary use case
(hands-free Claude Code while driving) that loop closes and is usable daily.

This document is the deferred list: what a next round *could* add, ranked by
impact, and — just as importantly — what it should **not** add. It reflects a
product review (self + an independent second opinion) that both landed on
"near-complete; don't broaden it."

The verdict worth remembering: **the output half is finished. The remaining gaps
are about eyes-free *operation* — state, control, and safe approvals — not about
making the voice better.**

---

## Essential (the two real gaps for eyes-free use)

### 1. Audible state protocol
Narrated text alone leaves you unsure whether Claude is *thinking*, *waiting on
you*, *finished*, or *the stream died*. Silence is ambiguous. Add a small set of
distinct, restrained, non-speech cues:

- **Working** — a quiet, occasional tick so silence ≠ "dead."
- **Your turn** — a clear earcon when Claude is actually blocked on your input.
- **Done** — turn complete, nothing more coming.
- **Disconnected / paused** — the stream dropped or playback stalled.

This is the single highest-value, lowest-cost addition. It closes the "is it my
turn / is it still alive?" loop that live narration alone can't.

### 2. Driving-safe approval policy
You cannot safely approve what you cannot see. A spoken "about to deploy — say
yes" is **not** enough while driving, because you can't inspect the actual command
or diff. The right design is to *defer*, not to blind-approve:

- **Auto-continue** only actions you've pre-declared low-risk.
- **Defer** anything destructive, external, credential-touching, deploy, or write:
  announce "approval required — queued for review" and hold it until you can look.
- Never prompt for a blind yes/no on a consequential action.

Safe *refusal* is the feature here.

---

## Polish (nice, clearly below the two above)

### 3. Brevity mode + barge-in

**Brevity:** a narration mode that speaks the *outcome*, the key *caveat*, and
the *next question* — not the full prose reply. Long replies are a lot to listen
to at speed. The `SUMMARIZE` flag already exists and is off by default; see
[`SUMMARIZATION.md`](SUMMARIZATION.md) for what ships today and what has to be
built before it can be turned on.

**Barge-in:** let a new prompt or a spoken "stop" cancel queued narration at a
sentence boundary. Today a channel switch drops *queued* audio immediately but
lets the playing clip finish, so the cut lands after the current sentence
([ADR 0010](adr/0010-latest-wins-interrupt.md)). True barge-in wants a bounded
semantic unit — finish the current sentence-sized chunk, then switch — with
manual Skip staying immediate. It depends on the parts protocol in §4: the
writer checks for a newer utterance *between* chunks rather than killing a
decoder mid-phoneme. The FIFO and the persistent HLS encoder must never stop
during either transition.

Both of these matter **more** than shaving first-word latency.

### 4. Streaming synthesis

First spoken word in well under a second instead of waiting for the whole block.
Genuinely lower priority: the perceived delay is dominated by HLS buffering
(already tuned to 1 s segments) and by reply *length*, not by synthesis start
time. It matters most for long blocks — a warm Kokoro render of ~2,500
characters takes about 15 seconds, and the driver hears comfort noise for all of
it.

**Approach.** Change the unit `synthd` publishes from one whole-block WAV to an
ordered stream of WAV parts, and let the writer start part `001` as soon as it
is atomically visible while later parts render behind playback.

Constraints worth keeping from the detailed draft:

- **Reuse Kokoro/misaki's existing chunking.** Its generator already segments and
  yields ordered audio. Do not add a second sentence regex or tokenizer.
- **Contiguous, zero-padded, one-based part names**, published atomically
  (temp file → fsync → rename). Never publish `003` before `002`, never revise a
  published part.
- **A terminal `complete.json` written last** is the *only* proof no more parts
  are coming, and its `part_count` is authoritative. Without it, a temporarily
  missing part looks identical to end-of-reply, and a later fully-synthesized
  reply can interleave into a still-rendering one.
- **Decode only the exact next index.** Never skip a gap because a higher part
  happens to exist; emit comfort noise while waiting.
- **Never fall back to a whole-reply `say` clip after part `001` has played.**
  That recreates the double-speak bug this project already fixed once. Publish a
  `status=partial` marker and close the reply instead.
- **A per-reply consumed cursor** so a writer restart cannot replay part `001`.
- **Ship behind a flag** (`STREAM_SYNTH=0` default) with the whole-block path
  intact as rollback.

**Acceptance:** queue commit to first speech PCM under one second for a warm
model, versus the measured ~15 s; parts play exactly once in order; the FIFO
never reaches EOF and the encoder PID never changes; a following reply cannot
start until the active reply's terminal marker exists and all its parts are
consumed. Prove it on the locked phone and the car route — gapless Mac PCM does
not prove the head unit heard every boundary.

**Honest limits:** this reduces time-to-first-word, not Kokoro's total compute or
the reply's spoken duration (summarization is the lever for duration), and it
does not touch the downstream HLS/player latency. Separate generator yields may
expose small prosody or loudness seams at boundaries.

<sub>The full protocol drafts (`SCOPE_STREAMING_SYNTHESIS.md`, `SCOPE_SENTENCE_CUT.md`) were
folded into this section and removed; `git log --diff-filter=D -- docs/` recovers them.</sub>

---

## Non-goals (don't build these — they're drift)

- **Absorbing voice-in.** Raven owns voice-*out* only. Remote Control + iOS
  dictation own voice-in. Rebuilding that is scope creep.
- **More voices, channels, settings screens, dashboards.** The product is one
  loop for one user. Breadth here is drift, not value.
- **A metered API / Agent-SDK path.** The whole design is subscription-safe and
  ~$0/reply on local Kokoro. Keep it that way.

## Honest boundary

Raven is truly hands-free only **after a session is active**. Starting dictation,
submitting, reconnecting, and selecting the channel still need a tap and a glance
in the Remote Control app. Don't hide this — optimize the default path so those
controls are rarely needed, and be honest that the "eyes-free" claim has that edge.

---

## North star: a dedicated Raven device (beyond the current product)

Everything above keeps the *phone* product focused. This is the deliberately
out-of-scope "someday": a purpose-built hardware device that replaces the phone
entirely. Captured so it isn't lost — not because it's next.

**Why a device beats the phone.** It solves the two eyes-free gaps structurally
instead of with workarounds:
- A physical **push-to-talk button** — no iOS background-mic restriction to fight.
- An **LED ring as the state protocol**: green = your turn, pulse = working, red =
  disconnected. Glanceable, no screen, no sound needed for state.
- Single-purpose: no notifications, no app-switching, no phone-call interruptions.

**Architecture decision — run STT *and* TTS on the device; exchange only TEXT with
the Mac.** This is the non-obvious win. Today Raven streams *audio* over the
network, so a dead zone mid-reply loses the rest (no rewind — see the drive log).
If the device transcribes your speech locally and synthesizes Claude's reply
locally, the Mac↔device link carries only text — tiny, and it survives dead zones.
The device holds the full reply and can speak or repeat it regardless of
connectivity. **The text protocol is what makes it robust for a car.**

Trade-offs: the device needs real compute (Raspberry Pi 4-class), and TTS moves off
**Kokoro** (Apple-Silicon only) to an ARM engine like **Piper** (good, runs on a
Pi — but A/B the voice first). The Mac still runs Claude Code and injects the prompt
— the hard part, unchanged, and subscription-safe via the CLI (not the metered API).
The `katib` repo (local macOS STT) is prior art for the recognition side.

**Build tiers:**
1. **Prototype (thin):** Pi + mic + speaker + button, tethered to your phone's
   hotspot, streaming audio to the Mac (Mac does STT + TTS). Proves the loop in a
   weekend, ~$40. Not robust — inherits the audio-over-cellular fragility.
2. **Real device (smart):** Pi 4 + far-field mic + its own LTE modem/SIM (no phone
   needed) + amp into car audio + LED ring + enclosure, running STT + TTS on-device
   with a text protocol to the Mac. The robust, product-shaped version.
3. **Bespoke:** custom PCB + molded case + on-device wake word. A real gadget — a
   separate discipline (hardware design, manufacturing, certification).

**Reconciling with the non-goals above:** "no voice-in" bounds the *phone* product,
where absorbing voice-in is scope creep. On a purpose-built device, voice-in is the
whole point, so that non-goal doesn't apply. Keep the tracks separate: ship and
open-source the phone product; treat the device as its own future project.

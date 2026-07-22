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
- **Brevity:** a narration mode that speaks the *outcome*, the key *caveat*, and
  the *next question* — not the full prose reply. Long replies are a lot to listen
  to at speed. (See [`SCOPE_SUMMARIZATION.md`](SCOPE_SUMMARIZATION.md); the
  `SUMMARIZE` config flag already exists, off by default.)
- **Barge-in:** let a new prompt or a spoken "stop" immediately cancel queued
  narration at a sentence boundary. (See [`SCOPE_SENTENCE_CUT.md`](SCOPE_SENTENCE_CUT.md).)

Both of these matter **more** than shaving first-word latency.

### 4. Streaming synthesis
First spoken word in ~0.3s instead of ~1s by synthesizing sentence-by-sentence.
(See [`SCOPE_STREAMING_SYNTHESIS.md`](SCOPE_STREAMING_SYNTHESIS.md).) Genuinely
lower priority — the perceived delay today is dominated by HLS buffering (already
tuned to 1s segments) and by reply *length*, not by synthesis start time.

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

# ADR 0001: Raven Owns Voice-Out Only

## Status

Accepted.

## Context

Asif already dictates prompts into Claude Code Remote Control from his iPhone. The missing capability during roughly two hours of daily driving is hearing Claude's replies safely.

The deleted v1, `claude-voice`, tried to own both directions. Its microphone path introduced speaker verification, background capture, voice processing, automatic gain control, and echo-cancellation problems. Desk-trained speaker verification did not transfer to the phone microphone and locked Asif out. Disabling iOS voice processing removed useful AGC and echo cancellation. V1 spent effort on those problems while failing to prove locked-screen background survival.

iOS also treats the two directions differently: web content cannot declare the background-audio capability required for durable microphone capture, while background media playback is supported.

## Decision

Raven owns only voice-out. Claude Code Remote Control plus iOS dictation remains the voice-in path. Raven listens for Claude Code lifecycle hooks, speaks selected completed replies, and never listens, transcribes, or submits prompts.

## Consequences

- The entire v1 class of microphone, speaker-verification, AGC, echo-cancellation, and background-capture failures is outside Raven's scope.
- Remote Control remains the input UX and subscription-safe transport.
- Raven is not a general full-duplex voice assistant and cannot independently accept a correction or follow-up.
- The broader hands-free workflow may still require safety boundaries for unattended agent actions; that does not justify adding an open microphone to Raven.
- The product boundary is simple enough to test: a completed Claude reply must become durable background playback.

## Alternatives considered

- **Rebuild v1 as a full-duplex native assistant.** Rejected because it restores the unnecessary microphone problems that killed v1.
- **Build a Claude Code wrapper that owns both directions.** Rejected. Remote Control already solves input, and the archived Omnara project warned that keeping a wrapper aligned with Claude Code's release cadence became unfeasible.
- **Move voice-in into a PWA.** Rejected because background microphone capture is not available to web content under the required iOS lifecycle.

See [`../HISTORY.md`](../HISTORY.md) phases 2–3 and [`../TRADEOFFS.md`](../TRADEOFFS.md).

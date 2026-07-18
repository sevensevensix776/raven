# Raven streaming (per-sentence) synthesis — scope and design

## Status

**Necessary, not yet built.** Priority raised on 2026-07-18 when the spoken-reply
cap was removed (`MAX_SPOKEN_CHARS=0`): uncapped replies are now the norm, and
long ones expose a real latency wall.

## Problem

Replies are synthesized whole. Kokoro is ~0.1s warm for a short reply but scales
with length — a measured 2,455-character reply took **~15 seconds** to synthesize.
The writer (correctly, since the double-speak fix) waits for synthd rather than
racing it with `say`, so **time-to-first-word for a long reply is now up to ~15s
of comfort-noise hiss** before anything is spoken. Driving, that reads as "did it
break?" — the exact ambiguity Raven is supposed to remove.

The tradeoff is currently: double-speak (bad) vs. long silence (also bad). The
real fix removes both by not synthesizing the whole reply before speaking any of
it.

## The fix: synthesize and emit per sentence

`synthd` already chunks internally (misaki splits into sentences; Kokoro yields
per-segment audio). Today those segments are concatenated into one `.wav` before
anything plays. Instead:

1. On a `queue/<stamp>.txt` job, split into sentences (or ≤N-second chunks) and
   synthesize each in order.
2. Emit **ordered part files** — `queue/<stamp>.001.wav`, `.002.wav`, … — writing
   each as soon as it's ready (atomic rename per part).
3. The writer plays a session's parts **in order**, starting `.001` the instant it
   lands (~0.3s) while `.002…` synthesize behind it. Comfort noise bridges any gap
   between parts so the timeline never breaks.

Result: **time-to-first-word ~0.3s regardless of reply length.**

## Design notes / decisions to make

- **The "In <project>." prefix** goes on the first part only (it's spoken once).
- **Transcript / caption**: record the utterance (role=claude) when the FIRST part
  starts emitting — the transcript shows the whole reply text (already in the
  caption), not per-part fragments.
- **Ordering + completeness**: the writer must not start a later part before an
  earlier one, and must know when a reply is complete (e.g. a `.done` marker or a
  known part count) so it doesn't interleave the next reply. A per-reply
  subdirectory (`queue/<stamp>/001.wav`) may be cleaner than flat part files.
- **Gap bridging**: between parts, emit the idle floor (existing behavior) so the
  encoder never underruns; parts should be ready fast enough that gaps are short.
- **say fallback** still applies per reply if synthd is down (unchanged).

## Why this is the same lever as sentence-boundary interruption

Per-part emission is the prerequisite for [`SCOPE_SENTENCE_CUT.md`](SCOPE_SENTENCE_CUT.md):
once a reply plays as ordered parts, the writer can check `queue/.latest` **between
parts** and drop the rest of a superseded reply at a clean sentence boundary,
instead of hard-cutting mid-word. Build streaming synthesis first (it stands alone
on latency), and sentence-boundary preemption falls out of it.

## Rollout

1. Add chunked emission to `synthd` behind a config flag (e.g. `STREAM_SYNTH=1`),
   default off, so the current whole-reply path stays the known-good fallback.
2. Teach the writer to consume ordered parts (and a completeness marker).
3. Verify time-to-first-word on a long reply drops from ~15s to <1s, and that a
   multi-part reply plays gap-free in order.
4. Flip the flag on; keep whole-reply synthesis as the fallback for one release.
5. Then layer sentence-boundary preemption on top per SCOPE_SENTENCE_CUT.

## Limits to be honest about

- Kokoro per-sentence still costs ~0.1–0.3s each; a very long reply is still
  minutes of audio — streaming fixes *time-to-first-word*, not total duration.
  Summarization (`SCOPE_SUMMARIZATION.md`) is the lever for total duration.
- More moving parts in the load-bearing writer path; each needs the same
  continuity care as the base timeline.

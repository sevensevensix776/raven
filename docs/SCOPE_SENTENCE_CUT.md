# Raven sentence-boundary cut — stage-2 scope and design

This document defines a stage-2 refinement to Raven's committed latest-wins interrupt design: let a newer selected-channel reply preempt at the next sentence-sized audio boundary instead of cutting the current voice mid-word. Manual Skip remains immediate. The result trades a small amount of freshness for substantially better listening quality while preserving Raven's continuous HLS timeline.

## Status

**Stage 2; not implemented.** This policy has two prerequisites:

1. [`SCOPE_STREAMING_SYNTHESIS.md`](SCOPE_STREAMING_SYNTHESIS.md) supplies the
   ordered per-reply part protocol and must be implemented first; and
2. [ADR 0010](adr/0010-latest-wins-interrupt.md) supplies the latest-wins and
   manual-Skip control plane and must be device-proven before automatic
   boundary cuts are enabled.

The current production writer plays a ready clip to completion and does not yet
implement streaming parts, hard latest-wins, or manual Skip.

This scope refines the *automatic* preemption point. It does not replace the latest-wins selection model, queue coalescing, `.latest`/`.interrupt` control files, or the persistent-encoder rules in the baseline design.

## Problem

Hard latest-wins optimizes freshness: as soon as a newer reply arrives, Raven terminates the decoder for the old reply. That is correct for multi-minute stale audio, but an arbitrary process kill can land inside a phoneme, word, or clause. The driver hears an unnatural cut with no graceful indication that the thought ended.

Waiting for the whole reply is not acceptable either. With uncapped replies, “finish current, then newest” can preserve several minutes of stale speech.

The useful middle ground is a bounded semantic unit: finish the current sentence-sized chunk, then switch.

## Desired behavior

```text
old reply: chunk 1 → chunk 2 → chunk 3 …
                              ↑ newer selected reply arrives
automatic latest-wins:        finish chunk 3 → idle bridge → newest reply
manual Skip:                  terminate chunk 3 now → idle bridge → idle/newest
```

- Kokoro publishes the ordered, sentence-aware parts defined by
  `SCOPE_STREAMING_SYNTHESIS.md`; their measured duration becomes the initial
  automatic-preemption window.
- The writer checks `queue/.latest` between chunks.
- If `.latest` points to a newer utterance, the writer stops the old utterance before opening its next chunk.
- A manual `/skip` changes the interrupt token and still terminates the active per-clip decoder immediately.
- The writer, FIFO, and persistent HLS encoder never stop during either transition.

Stage 2 should first use the parts Kokoro/misaki already yields. If device tests
show those boundaries are too short, too long, or not consistently semantic,
group adjacent yielded parts in `synthd` without changing the queue protocol.
Sentence integrity wins over an exact duration target unless a separate
maximum-safety policy is added later.

## Synthesis changes

Do not introduce a second part format, manifest, tokenizer, or transcript model.
Reuse the exact `queue/<stamp>/001.wav...complete.json` protocol in
[`SCOPE_STREAMING_SYNTHESIS.md`](SCOPE_STREAMING_SYNTHESIS.md). That scope owns
atomic publication, contiguous numbering, completeness, partial failure,
restart cursors, prefix placement, transcript timing, and per-reply `say`
fallback.

Sentence cut adds only latest-wins awareness around that protocol:

- before spending compute on the next unpublished part, `synthd` may stop work
  on a superseded reply according to [ADR 0010](adr/0010-latest-wins-interrupt.md);
- already published parts remain immutable; and
- if synthesis is terminated early, publish the protocol's terminal partial
  marker so the writer cannot wait forever or interleave replies accidentally.

The initial boundary is one Kokoro/misaki generator yield. Any later grouping or
splitting must be implemented and tested in the streaming-synthesis layer while
preserving the same external queue contract.

## Writer changes

After streaming synthesis is implemented, `raven write` already consumes one
active reply as ordered part files. Stage 2 adds an interrupt-policy check at the
safe point between two parts.

For each utterance:

1. Add the transcript entry when the first part begins, as defined by the
   streaming-synthesis protocol.
2. Decode the current chunk into the existing PCM output.
3. When that decoder exits, emit the normal comfort-noise bridge.
4. Re-read `queue/.latest` before opening the next chunk.
5. If a newer ID is selected, mark the old transcript `preempted` and move to the newest ready utterance.
6. Otherwise, decode the next chunk.
7. After the final declared part, mark the transcript `completed`.

The inter-chunk check is for automatic latest-wins. In parallel, the writer continues polling `.interrupt` during chunk playback exactly as designed for hard interruption. A `skip:` token kills the current chunk decoder immediately; a `new:` token is deferred to the next boundary under this stage-2 policy.

Only the per-chunk decoder is disposable. Never kill `.ffmpeg.pid`, close `pcm.fifo`, replace the HLS playlist, or introduce a discontinuity.

## Composition with latest-wins

[ADR 0010](adr/0010-latest-wins-interrupt.md) defines the control plane and safety model:

- `.latest` names the only reply worth delivering;
- `.interrupt` communicates new-reply and manual-skip events;
- synthesis avoids publishing superseded work;
- the writer coalesces backlog around newest;
- a cached PCM bridge keeps the encoder fed; and
- transcript state distinguishes completed, preempted, and skipped delivery.

Sentence-boundary cut changes one policy decision inside that design:

| Event | Hard latest-wins baseline | Stage-2 sentence cut |
|---|---|---|
| New selected reply | Kill the active decoder immediately. | Finish the active streamed part, then preempt before the next part. |
| Manual Skip | Kill immediately. | Kill immediately; unchanged. |
| Superseded queued reply not yet started | Drop it. | Drop it; unchanged. |
| HLS encoder and FIFO | Preserve continuously. | Preserve continuously; unchanged. |

The latest reply still wins. Stage 2 only defines *where* the automatic handoff is allowed to land.

## Tradeoff

Boundary-aware preemption is less fresh by at most the remaining duration of the
active part. HLS buffering then adds the existing playback delay before the
driver hears the switch. In exchange, most automatic transitions end after a
complete thought, Kokoro prosody remains intact, and long answers are still
bounded rather than allowed to finish.

Chunk length is the tuning lever:

- shorter chunks switch sooner but create more synthesis artifacts, handoffs, metadata, and possible prosody seams;
- longer chunks sound more continuous but make a newer answer wait longer; and
- sentence-only grouping is more natural but produces variable timing.

Start with the existing Kokoro/misaki yielded parts. Tune from device listening,
not waveform aesthetics, and add duration grouping only if the measured
boundaries require it.

## Rollout and verification

1. Implement and prove streaming synthesis, including atomic part publication,
   terminal completeness, restart behavior, and whole-audio parity.
2. Play a fixed story containing abbreviations, decimals, short sentences, and
   very long sentences. Confirm yielded boundaries never drop or duplicate
   audio and are acceptable preemption points.
3. Implement and soak-test hard latest-wins, including 20–50 mid-clip
   interruptions with the phone locked and on the car audio route.
4. Enable boundary checks for automatic `new:` events while leaving `skip:` immediate.
5. Measure actual yielded-part durations on a real drive. If grouping is needed,
   compare candidate windows and record arrival-to-cut time and whether the
   transition sounded intentional.
6. Verify `.ffmpeg.pid` remains identical through repeated boundary cuts, HLS media sequence remains monotonic, and the phone records uninterrupted playback progress.
7. Confirm transcript outcomes: begun old replies become `preempted`, manual cuts become `skipped`, completed replies remain `completed`, and never-started superseded replies do not appear.

## Limits

- This design depends on the not-yet-implemented latest-wins foundation.
- Sentence segmentation is linguistic, not merely punctuation splitting; edge cases require a corpus and tests.
- Independently synthesized chunks may have small prosody or loudness seams even when the text boundary is clean.
- A single long sentence can exceed the target window unless Raven later permits clause-level cuts.
- Boundary cuts improve the Mac-side transition. They do not remove the iPhone's existing HLS buffer, so the audible switch remains delayed.
- Manual Skip is intentionally jarring when necessary. Its contract is immediacy, not prosodic grace.

## Composition with live narration

[`LIVE_NARRATION.md`](LIVE_NARRATION.md) can enqueue multiple
completed assistant blocks during one Claude turn. Each block is an ordinary
reply job using the shared streaming-part protocol. If a newer block arrives
while an older block is speaking, this policy may hand off at the next part
boundary. Live narration does not add a second cut mechanism.

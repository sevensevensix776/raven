# Raven sentence-boundary cut — stage-2 scope and design

This document defines a stage-2 refinement to Raven's committed latest-wins interrupt design: let a newer selected-channel reply preempt at the next sentence-sized audio boundary instead of cutting the current voice mid-word. Manual Skip remains immediate. The result trades a small amount of freshness for substantially better listening quality while preserving Raven's continuous HLS timeline.

## Status

**Stage 2; not implemented.** The baseline hard-interrupt design is recorded in [`INTERRUPT_DESIGN.md`](INTERRUPT_DESIGN.md) and should be built and device-proven first. The current production writer plays a ready clip to completion and does not yet implement either hard latest-wins or manual Skip.

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

- Kokoro publishes ordered chunks that each contain complete sentence boundaries and target roughly 8–15 seconds of speech.
- The writer checks `queue/.latest` between chunks.
- If `.latest` points to a newer utterance, the writer stops the old utterance before opening its next chunk.
- A manual `/skip` changes the interrupt token and still terminates the active per-clip decoder immediately.
- The writer, FIFO, and persistent HLS encoder never stop during either transition.

The 8–15-second window is a target, not a promise. A single long sentence may exceed it; several short sentences may be grouped. Sentence integrity wins over hitting an exact duration unless a separate maximum-safety policy is added later.

## Synthesis changes

Today `synthd.py` asks Kokoro to generate the full reply, concatenates every returned audio segment, and atomically publishes one `<stamp>.wav`. Stage 2 requires an ordered chunk set.

### 1. Segment text at sentence boundaries

Split the final spoken text—verbatim or summarized—into sentences, preserving punctuation. Group adjacent sentences into chunks whose estimated spoken duration is near 8–15 seconds. Avoid a regex that treats every period as a sentence; abbreviations, decimals, paths, and initials need either the tokenizer already available in the Kokoro/misaki stack or a tested sentence segmenter.

### 2. Synthesize chunks independently

Render each group with the same model, voice, speed, and 24 kHz output format. Independent synthesis is intentional: each chunk needs to be a disposable decoder input. Preserve ordering with stable names such as:

```text
<stamp>.000.wav
<stamp>.001.wav
<stamp>.002.wav
```

Each chunk must still use a temporary file and atomic rename. A small manifest should be published last so the writer never starts a partially synthesized sequence. The manifest should contain the utterance ID, ordered chunk names, and spoken-text offsets or sentence indexes for transcript accounting.

### 3. Respect latest-wins while synthesizing

Before starting each chunk and before publishing the manifest, compare the utterance ID with `queue/.latest`. If the job is no longer latest, stop spending synthesis time and remove its unpublished artifacts. The rules from `INTERRUPT_DESIGN.md` remain authoritative for queue locking and commit order.

### 4. Preserve fallback behavior

If chunked Kokoro synthesis fails, Raven must still speak rather than silently drop the reply. The simplest stage-2 fallback may publish one full `say` clip, accepting hard-cut behavior for that exceptional path. Chunking the `say` fallback is optional until Kokoro chunking is stable.

## Writer changes

Today `writer.sh` selects one ready `.wav`/`.aiff` and runs one decoder to EOF. Stage 2 changes the unit of work from “utterance file” to “utterance manifest plus ordered chunk files.”

For each utterance:

1. Add the transcript entry when the first chunk begins, as today.
2. Decode the current chunk into the existing PCM output.
3. When that decoder exits, emit the normal cached idle bridge.
4. Re-read `queue/.latest` before opening the next chunk.
5. If a newer ID is selected, mark the old transcript `preempted` and move to the newest ready utterance.
6. Otherwise, decode the next chunk.
7. After the final chunk, mark the transcript `completed`.

The inter-chunk check is for automatic latest-wins. In parallel, the writer continues polling `.interrupt` during chunk playback exactly as designed for hard interruption. A `skip:` token kills the current chunk decoder immediately; a `new:` token is deferred to the next boundary under this stage-2 policy.

Only the per-chunk decoder is disposable. Never kill `.ffmpeg.pid`, close `pcm.fifo`, replace the HLS playlist, or introduce a discontinuity.

## Composition with latest-wins

[`INTERRUPT_DESIGN.md`](INTERRUPT_DESIGN.md) defines the control plane and safety model:

- `.latest` names the only reply worth delivering;
- `.interrupt` communicates new-reply and manual-skip events;
- synthesis avoids publishing superseded work;
- the writer coalesces backlog around newest;
- a cached PCM bridge keeps the encoder fed; and
- transcript state distinguishes completed, preempted, and skipped delivery.

Sentence-boundary cut changes one policy decision inside that design:

| Event | Hard latest-wins baseline | Stage-2 sentence cut |
|---|---|---|
| New selected reply | Kill the active decoder immediately. | Finish the active 8–15-second chunk, then preempt before the next chunk. |
| Manual Skip | Kill immediately. | Kill immediately; unchanged. |
| Superseded queued reply not yet started | Drop it. | Drop it; unchanged. |
| HLS encoder and FIFO | Preserve continuously. | Preserve continuously; unchanged. |

The latest reply still wins. Stage 2 only defines *where* the automatic handoff is allowed to land.

## Tradeoff

Boundary-aware preemption is less fresh by at most the remaining duration of the active chunk, normally several seconds. HLS buffering then adds the existing playback delay before the driver hears the switch. In exchange, most automatic transitions end after a complete thought, Kokoro prosody remains intact, and long answers are still bounded rather than allowed to finish.

Chunk length is the tuning lever:

- shorter chunks switch sooner but create more synthesis artifacts, handoffs, manifests, and possible prosody seams;
- longer chunks sound more continuous but make a newer answer wait longer; and
- sentence-only grouping is more natural but produces variable timing.

Start with sentence-preserving 8–15-second groups. Tune from device listening, not waveform aesthetics.

## Rollout and verification

1. Implement and soak-test hard latest-wins first, including 20–50 mid-clip interruptions with the phone locked and on the car audio route.
2. Add chunked synthesis behind a separate experimental switch; verify manifests are atomic and superseded partial chunks are cleaned.
3. Play a fixed story containing abbreviations, decimals, short sentences, and very long sentences. Confirm the segmenter never drops or duplicates text.
4. Enable boundary checks for automatic `new:` events while leaving `skip:` immediate.
5. Compare 5–8, 8–15, and 15–20-second targets on a real drive. Measure arrival-to-cut time and record whether the transition sounded intentional.
6. Verify `.ffmpeg.pid` remains identical through repeated boundary cuts, HLS media sequence remains monotonic, and the phone records uninterrupted playback progress.
7. Confirm transcript outcomes: begun old replies become `preempted`, manual cuts become `skipped`, completed replies remain `completed`, and never-started superseded replies do not appear.

## Limits

- This design depends on the not-yet-implemented latest-wins foundation.
- Sentence segmentation is linguistic, not merely punctuation splitting; edge cases require a corpus and tests.
- Independently synthesized chunks may have small prosody or loudness seams even when the text boundary is clean.
- A single long sentence can exceed the target window unless Raven later permits clause-level cuts.
- Boundary cuts improve the Mac-side transition. They do not remove the iPhone's existing HLS buffer, so the audible switch remains delayed.
- Manual Skip is intentionally jarring when necessary. Its contract is immediacy, not prosodic grace.


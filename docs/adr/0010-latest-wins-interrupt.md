# ADR 0010: Use Latest-Wins Interruption

## Status

Accepted; implementation deferred.

## Context

Raven speaks uncapped replies. A ready clip can last several minutes, so a FIFO policy that always finishes the current reply can trap the driver in stale content after a newer answer becomes available. Freshness matters during an active hands-free coding session.

Interruption must not break the background stream. Raven has one persistent FIFO and HLS encoder; terminating either would force `AVPlayer` to recover a new timeline. The only disposable process is the decoder for the active clip.

## Decision

When a newer reply arrives for the selected channel, the newer reply wins:

- coalesce queued work around the newest selected reply;
- interrupt the active old reply;
- kill only the per-clip decoder;
- immediately bridge the handoff with cached idle PCM; and
- preserve the writer, FIFO, encoder, and monotonically advancing HLS timeline.

Manual Skip follows the same server-side mechanism and is immediate. Automatic latest-wins should first ship as a hard cut. A later refinement may defer automatic preemption to the next Kokoro/misaki sentence-sized part; manual Skip remains immediate.

This policy is not current production behavior. The current writer plays an active clip to completion.

## Consequences

- Drivers reach current results instead of waiting through multi-minute stale replies.
- A hard cut can land mid-word or mid-sentence and sound jarring.
- Queue, synthesis, transcript, and writer state need explicit completed, preempted, skipped, and superseded outcomes.
- The persistent encoder and FIFO become inviolable during interrupt work.
- Sentence-boundary preemption depends on the not-yet-built streaming-part protocol in [`../FUTURE_WORK.md`](../FUTURE_WORK.md).
- The iPhone may still have 4–8 seconds of old HLS buffer after the Mac cuts; a client live-edge seek can discard that buffer only after server-side production stops.

## Alternatives considered

- **Finish every active reply.** Rejected because an uncapped stale answer can continue for minutes.
- **Finish current, then play only newest.** Better queue freshness, but still leaves the driver trapped in the current long clip.
- **Kill and restart the HLS encoder.** Rejected because it breaks the continuous background timeline.
- **Sentence-boundary cuts from the first implementation.** Deferred until hard interruption and sentence-streamed parts are independently proven.
- **Client-only seek.** Insufficient because the live edge remains old content while the Mac writer continues producing it.

See [`../FUTURE_WORK.md`](../FUTURE_WORK.md) and the [freshness tradeoff](../TRADEOFFS.md).

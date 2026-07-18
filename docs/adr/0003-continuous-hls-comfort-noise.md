# ADR 0003: Use Continuous HLS with a Comfort-Noise Floor

## Status

Accepted.

## Context

An iOS app remains active for background audio only while it is actually playing audio. A per-reply stream ends between turns. Digital silence can allow the backgrounded path to become inert, and car audio hardware can sleep during the gap and clip the beginning of the next reply.

Raven must preserve one live timeline across long pauses, synthesis delays, retries, and future interruptions. Tests established that a quiet pink-noise floor preserves the route; the equivalent digital-silence mode has not passed the same locked-phone drive test.

## Decision

Maintain one endless PCM timeline and one persistent HLS encoder:

- `raven write` emits either speech or a low pink-noise floor as 24 kHz mono signed 16-bit PCM.
- A persistent `ffmpeg -re` process reads the FIFO and creates two-second AAC HLS segments, a five-segment sliding playlist, and no end marker.
- The iPhone plays the live edge continuously.
- Future skip or interruption may terminate only the disposable per-clip decoder. It must never kill the persistent encoder, close the FIFO, or restart the HLS timeline.

## Consequences

- Background playback survives idle gaps and long synthesis waits.
- The car audio path stays awake, reducing first-word clipping.
- The user hears intentional low-level static between replies.
- `ffmpeg -re` is load-bearing; without real-time pacing, the encoder drains the FIFO faster than wall time and breaks the live timeline.
- HLS segmentation and `AVPlayer` buffering add roughly 4–8 seconds of downstream latency.
- Encoder continuity becomes a hard invariant for interruption and streaming work.

## Alternatives considered

- **Digital silence between replies.** Rejected as the default because it did not preserve equivalent background and car-route behavior.
- **A separate audio file or stream per reply.** Rejected because gaps and reconnects would make background survival and first-word delivery unreliable.
- **Restart HLS on every reply or interruption.** Rejected because it discards the live edge and forces the phone to recover a new timeline.
- **No idle output.** Rejected because closing or starving the FIFO can end or stall the stream.

See the [README invariants](../../README.md#load-bearing-invariants), [`../diagram-decisions.mmd`](../diagram-decisions.mmd), and [`../INTERRUPT_DESIGN.md`](../INTERRUPT_DESIGN.md).

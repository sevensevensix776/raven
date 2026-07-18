# ADR 0012: Speak Complete, Uncapped Replies

## Status

Accepted.

## Context

Early Raven configurations limited the number of spoken characters. A positive `MAX_SPOKEN_CHARS` is a byte-oriented cut and can end a useful reply in the middle of a sentence. A 700-character cap clipped real answers; larger fixed caps still encode the same arbitrary failure.

Removing the cap exposed a different issue: whole-reply Kokoro synthesis can take about 15 seconds for roughly 2,500 characters, and long audio can occupy the channel for minutes. The first attempted latency workaround—a five-second `say` fallback—raced a slow but healthy daemon and spoke the reply twice.

## Decision

Set `MAX_SPOKEN_CHARS=0`, meaning Raven speaks the complete cleaned reply. Do not solve synthesis latency or listening duration by silently truncating authored text.

Address the two resulting concerns separately:

- sentence-streamed synthesis for time-to-first-word; and
- explicitly designed, guarded summarization or latest-wins interruption for total listening burden and freshness.

## Consequences

- Raven does not discard the tail of a reply or stop mid-sentence because of an arbitrary byte budget.
- Long replies take longer to synthesize and hear.
- The writer waits while a live `synthd` works, even when that means roughly 15 seconds of comfort noise.
- Latest-wins interruption becomes important because a complete reply can be several minutes long.
- Summarization remains optional, disabled, and visibly lossy rather than an implicit cap.

## Alternatives considered

- **Restore the 700-character cap.** Rejected because it clipped real replies.
- **Use a larger fixed cap such as 2,500 characters.** Rejected because it still cuts by bytes and can stop mid-sentence.
- **Always summarize long replies.** Not yet accepted as production behavior; the Qwen3 path is built but disabled and untuned, and summaries can omit qualifications.
- **Race `say` after a fixed timeout.** Rejected after it produced double-speak.
- **Sentence-streamed synthesis.** Accepted as the recommended latency fix; it starts earlier without deleting content.

See [`../SCOPE_STREAMING_SYNTHESIS.md`](../SCOPE_STREAMING_SYNTHESIS.md), [`../SCOPE_SUMMARIZATION.md`](../SCOPE_SUMMARIZATION.md), and [ADR 0010](0010-latest-wins-interrupt.md).

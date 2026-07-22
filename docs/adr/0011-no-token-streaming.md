# ADR 0011: Do Not Pursue Token-Level Streaming

## Status

Accepted under the current Claude.ai subscription constraint.

## Context

The desired experience is to start speaking before a long reply has finished rendering. True Claude token deltas would permit the earliest narration, but Claude Code does not expose them through the accepted hook and transcript surfaces.

This was verified rather than assumed: the Claude Code transcript JSONL contains complete assistant text blocks and no `stream_event`, `content_block_delta`, or equivalent token-delta records. The `includePartialMessages` path belongs to the Agent SDK and requires an `ANTHROPIC_API_KEY` with separately billed API credits. Subscription authentication is not permitted for that SDK path.

## Decision

Do not build token-level streaming while Raven remains on the Claude.ai subscription. Keep the Stop-hook source of truth and pursue sentence-streamed synthesis as the first latency improvement.

Treat transcript-tailing live narration as a separate, higher-risk feature that can speak completed assistant blocks earlier in tool-using turns but is not token streaming.

## Consequences

- Raven preserves subscription billing and Remote Control compatibility.
- It cannot speak tokens as Claude generates them.
- The current system waits for `Stop` before synthesis begins.
- Sentence-streamed synthesis can still reduce Mac-side time-to-first-word to under a second after the complete reply is queued.
- Live narration may reduce unexplained silence when complete intermediate assistant blocks appear, but adds ownership, deduplication, and recovery complexity.
- This ADR should be revisited only if the authentication or billing constraint changes.

## Alternatives considered

- **Agent SDK partial messages.** Rejected because it requires an API key and separate credit billing.
- **Extract token deltas from Claude Code transcript JSONL.** Ruled out by inspection; those records are not present.
- **Sentence-streamed synthesis after `Stop`.** Accepted substitute and the recommended next build; see [`../SCOPE_STREAMING_SYNTHESIS.md`](../SCOPE_STREAMING_SYNTHESIS.md).
- **Tail complete transcript blocks before `Stop`.** Scoped as live narration, not token streaming; see [`../LIVE_NARRATION.md`](../LIVE_NARRATION.md).

See [ADR 0005](0005-claude-code-hooks.md).

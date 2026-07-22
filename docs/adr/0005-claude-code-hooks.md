# ADR 0005: Integrate Through Claude Code Hooks on the Subscription

## Status

Accepted.

## Context

Raven must receive replies whether the turn starts in a terminal or in Claude Code Remote Control. Claude Code invokes lifecycle hooks inside the session runtime, independent of which UI drove the turn. The `Stop` payload includes `last_assistant_message`; `UserPromptSubmit` and `SessionEnd` support channel state and cleanup.

The alternative Agent SDK path requires an `ANTHROPIC_API_KEY` and separately billed API credits. Anthropic's documented SDK path does not permit Claude.ai subscription authentication. Staying on the subscription is a product requirement.

## Decision

Use a two-second Claude Code command hook for `UserPromptSubmit`, `Stop`, and `SessionEnd`:

- `UserPromptSubmit` updates follow-mode selection and records the real user prompt on screen only.
- `Stop` queues the selected session's cleaned `last_assistant_message`.
- `SessionEnd` removes dead sessions and releases a pin to an ended session.

The hook is fail-silent so narration cannot block a Claude Code turn. Operational failures are diagnosed from queue, state, process, and event evidence.

## Consequences

- Remote Control turns use the same hook; Raven needs no second reply transport.
- The workflow stays on the Claude.ai subscription rather than consuming API credits.
- Raven receives completed hook messages, not token deltas.
- The two-second budget rewards a small, dependency-free hook path; the Go implementation starts in roughly 1 ms.
- Fail-silent behavior protects Claude Code but makes logs and `raven diagnose` essential for detecting narration failures.
- Harness-injected user-role prompts must be filtered before they appear as user-authored transcript lines.

## Alternatives considered

- **Agent SDK.** Rejected because it requires API-key authentication and separate billing under the accepted constraint.
- **Poll or tail the transcript as the sole owner of speech.** Deferred as the higher-risk live-narration design; exactly-once coordination and Stop recovery are more complex. (Later shipped — see the note below.) See [`../LIVE_NARRATION.md`](../LIVE_NARRATION.md).
- **Add a Remote Control-specific transport.** Rejected because session hooks already fire for those turns.
- **Fork or wrap Claude Code.** Rejected because it adds release-cadence coupling without solving a missing transport.

See [ADR 0011](0011-no-token-streaming.md) and the [Go hook documentation](../../cli/README.md) when viewed in the local workspace.

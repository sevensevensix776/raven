# ADR 0015: One Producer for the Speech Queue

## Status

Accepted. Implemented and running.

## Context

Live narration required speaking each completed assistant text block *during* a
turn, rather than waiting for the `Stop` hook at the end. That introduced a
second component — `raven tail` — capable of enqueuing speech.

Two producers for one queue creates an obvious hazard: the tailer speaks the
final text block when it lands, and moments later the `Stop` hook speaks the same
reply again. The driver hears the end of every turn twice. Raven had already
been burned by a double-speak bug once (the writer racing `say` against
`synthd`), so this was a known failure mode, not a hypothetical.

A scoped design proposed reconciling the two producers with a durable
**stop-intent ledger**: the hook would record an intent keyed by a hash of the
reply text, the tailer would consult it, and a grace interval would decide which
component owned delivery. That design was written before implementation.

## Decision

Do not reconcile two producers. Make the tailer the **only** producer while it
is running, and have the `Stop` hook yield to it.

On `Stop`, the hook checks whether `.tail.pid` names a live process using a
signal-0 probe. If it does, the hook logs `stop_yield_to_tailer` and enqueues
nothing. The tailer picks up the final text block from the transcript exactly as
it picks up every other block.

If the tailer is not alive, the hook enqueues the reply exactly as it always did.

## Consequences

- The double-speak race is removed by construction rather than mediated. There is
  no window in which both components believe they own delivery.
- The final block is not special. It is one more completed transcript block, so
  it inherits the tailer's existing dedup, cleaning, and selection checks.
- Turning live narration off is instant and safe: the Stop-hook path was demoted,
  never removed, so `LIVE_NARRATION=0` restores the original behaviour with no
  rebuild.
- The liveness check is a process probe, not a heartbeat. A tailer that is alive
  but wedged would cause the hook to yield to something that never speaks. This
  is an accepted risk: the failure is silence, which is recoverable and visible
  in `raven diagnose`, whereas the alternative failure is duplicated audio in the
  driver's ear.
- No new state file, no hashing of reply text, and no timing interval to tune.

## Alternatives considered

- **A durable stop-intent ledger with text hashes and grace intervals.** Rejected
  as unnecessary once the final block was recognized as an ordinary transcript
  block. It added a persistent file format, a hashing contract, and a timing
  parameter to solve a race that single-ownership eliminates.
- **Letting the hook always enqueue and having the tailer skip the last block.**
  Rejected because the tailer cannot reliably know which block will turn out to
  be last — a turn can continue through further tool calls after any given block.
- **Disabling the Stop hook entirely when live narration is on.** Rejected
  because it removes the fallback. If the tailer dies, Raven should keep speaking
  final replies rather than going silent.

See [`../LIVE_NARRATION.md`](../LIVE_NARRATION.md) for the shipped behaviour and
[ADR 0011](0011-no-token-streaming.md) for why a completed text block is the
smallest unit Raven can speak.

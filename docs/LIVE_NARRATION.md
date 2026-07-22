# Live narration (shipped)

Raven speaks each **completed assistant text block during a turn**, before the
`Stop` hook fires. Multi-step, tool-using turns are no longer silent, and turns
you interrupt with a new message still get narrated.

This documents what runs today. Unbuilt ideas live in
[`FUTURE_WORK.md`](FUTURE_WORK.md); the decision record for interruption is
[ADR 0010](adr/0010-latest-wins-interrupt.md).

## The constraint that shapes everything

Claude Code's session transcript contains **complete content blocks only** — no
token deltas, no streaming events ([ADR 0011](adr/0011-no-token-streaming.md)).
So the earliest unit Raven can speak is one finished text block. It cannot start
mid-sentence while Claude is still composing.

Conveniently, Claude Code writes roughly one content block per JSONL line, so a
newline-terminated `type: "assistant"` line whose block is `type: "text"` is a
complete, speakable unit.

## How `raven tail` works

A fifth long-lived process (`.tail.pid`, started by `start.sh`, gated by
`LIVE_NARRATION`):

1. Resolves the selected session and its `transcript_path` from `channels.json`,
   read under `.state.lock` so selection and channel metadata agree.
2. Reads from a durable byte cursor (`tail-state/<session>.json`), consuming only
   **newline-terminated** records — a half-written line is never parsed.
3. Accepts assistant entries whose content block is `type: "text"` and non-blank.
   `thinking` and `tool_use` blocks are never eligible.
4. Cleans the text through `internal/clean` — the same spoken cleaning the hook
   uses, so pronunciation fixes apply everywhere.
5. Deduplicates on a stable key: `sha256(session ‖ uuid ‖ block index ‖ sha256(raw))`,
   held in a bounded (2048) seen-set persisted beside the cursor. This collapses a
   resumed or rewritten entry while still letting two genuinely identical replies
   both speak.
6. Enqueues via the hook's protocol — caption first, then the `.txt` rename as the
   commit marker — **re-checking the selection under the lock immediately before
   committing**, so a block is never spoken into a channel you just left.

A session new to the tailer baselines at **EOF**: history is never narrated. If
the transcript's device/inode changes or the file shrinks, that's treated as
rotation — re-baseline at EOF rather than replay.

## Stop-hook coordination

Only one component may enqueue, or the driver hears the final block twice.

When `LIVE_NARRATION=1` **and** `.tail.pid` names a live process (a signal-0
probe), the `Stop` hook **yields**: it logs `stop_yield_to_tailer` and enqueues
nothing. The tailer picks the final block off the transcript like any other block.

If the tailer is not alive, `Stop` enqueues the final reply exactly as it always
did. That is the safety net, and it is why turning the feature off is instant.

> An earlier design proposed a durable stop-intent ledger with text hashes and
> grace intervals to reconcile the two producers. That was not built and is not
> needed: the final block is simply another completed transcript block, so making
> the tailer the single producer removes the race outright.

## Session-aware queue

Every poll, `pruneQueue` does two things:

- **Drops queued blocks belonging to a session you switched away from**, so
  changing channels cuts the old channel's audio instead of draining its backlog.
- **Caps the selected session's own backlog** to the newest few blocks, dropping
  the stale middle so narration stays near real time on long, chatty turns.

The writer plays oldest-first, so removing older stamps makes it jump forward. A
clip already open keeps playing to its end (macOS holds the descriptor), so a
switch cuts **after the current sentence**, not mid-word. True mid-sentence
barge-in is unbuilt — see [ADR 0010](adr/0010-latest-wins-interrupt.md).

## Failure model

| Situation | Behaviour |
|---|---|
| Tailer dies | `Stop` resumes speaking final replies; nothing goes silent. |
| Selection changes mid-commit | Block is skipped, never spoken into the wrong channel. |
| Transcript rotated or truncated | Re-baseline at EOF; no replay of old content. |
| Queue write fails | Block is skipped — a missed line beats double-speak. |
| Blocks arrive faster than playback | Stale middle is dropped; newest survive. |

## Rollback

Set `LIVE_NARRATION=0` and stop the tailer. The Stop-hook path was never removed,
only demoted, so speak-on-Stop returns immediately with no rebuild. See
[`ROLLBACK.md`](ROLLBACK.md).

## Tests

`cli/internal/tail` covers block eligibility (thinking/tool_use never speak),
ordering, retention of an unterminated final line, dedup across restarts,
two identical replies at distinct positions both speaking, and the queue prune —
switch-drop, backlog cap, and the no-selection safety case.

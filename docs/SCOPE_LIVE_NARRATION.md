# Raven live narration — stage-2 transcript-tailing design

## Status and recommendation

**Stage 2; feasible, higher risk, not implemented.** Build and device-prove
[`SCOPE_STREAMING_SYNTHESIS.md`](SCOPE_STREAMING_SYNTHESIS.md) first. Streaming
synthesis is a self-contained latency improvement. Live narration changes which
component owns speaking and must solve selection, deduplication, turn identity,
and Stop-hook races before it is safe to enable.

The useful capability is earlier narration from complete assistant text blocks:
Raven can speak a finished block such as “I’ll check the writer state” while the
same Claude turn continues through tool calls, then speak later completed blocks
as they are appended. This addresses long unexplained silences during multi-step
turns. It does not expose or speak tokens while Claude is still composing a
block.

## Established feasibility boundary

Token-level streaming is unavailable on the accepted subscription-only path.
The Agent SDK's `includePartialMessages` mechanism requires an
`ANTHROPIC_API_KEY` and separately billed API usage. The user explicitly rejected
that option. Claude Code's session transcript JSONL contains no token deltas or
stream events; it appends complete assistant text blocks as complete entries.

Therefore the earliest safe unit is one newly completed assistant text block.
Live narration can start before the Stop hook for multi-block/tool-using turns,
but it cannot start in the middle of the first prose block.

## Proposed ownership model

Add a long-lived `raven tail` process. Keep it in Go rather than putting file and
channel watching into Python `synthd`:

- channel selection, hook payloads, queue commits, cleaning, and transcript
  state already live in the Go codebase;
- `synthd` should remain a narrow text-to-audio service; and
- a separate process is independently restartable and diagnosable.

When `LIVE_NARRATION=1`, the tailer owns speech enqueueing for selected sessions.
The Stop hook no longer blindly enqueues `last_assistant_message`; it registers a
safety-net intent that the tailer deduplicates against blocks it already
enqueued. If the tailer is definitively down, the Stop hook uses the current
whole-final-block queue path.

This ownership switch is the core risk. Running both paths as independent
producers is explicitly forbidden because the final block will be spoken twice.

## Configuration and process lifecycle

Introduce a separate default-off flag:

```bash
LIVE_NARRATION=0
```

`raven tail` should have its own PID file and detached process entry, for example
`.tail.pid`, and should be included in start, stop, diagnose, and stale-PID
handling. `LIVE_NARRATION=0` preserves the current Stop-hook behavior exactly.

Do not couple this rollout to `STREAM_SYNTH`. Live narration can enqueue blocks
through either synthesis mode, although streamed synthesis is the recommended
and lower-latency composition.

## Getting and retaining `transcript_path`

Claude Code includes `transcript_path` in the hook payload. Extend the Go hook
payload and channel model:

```go
type payload struct {
    // existing fields...
    TranscriptPath string `json:"transcript_path"`
}

type Channel struct {
    // existing fields...
    TranscriptPath string `json:"transcript_path,omitempty"`
}
```

On every `UserPromptSubmit`, `Stop`, and `SessionEnd` registry update, store the
latest non-empty path for that session in `channels.json` under the existing
`.state.lock`. Do not erase a previously known path when an event omits the
field. `SessionEnd` removes the channel and its tail state as it does today.

Treat the path as local runtime input, not user-facing text. Require an absolute
path to a regular file, resolve symlinks before opening, and log/reject malformed
or missing paths. The tailer must never discover transcripts by scanning every
Claude project directory; `channels.json` is the authoritative session-to-path
mapping.

## Turn boundary and cursor

A byte cursor alone prevents rereading a file, but it does not define which
entries belong to the current prompt. On each selected `UserPromptSubmit`, the
hook should atomically create/update a per-session turn record:

```json
{
  "version": 1,
  "session_id": "...",
  "turn_id": "<hook-generated-id>",
  "transcript_path": "/absolute/path/session.jsonl",
  "device": 16777220,
  "inode": 12345,
  "start_offset": 987654
}
```

Capture the file size, device, and inode at prompt submission. This prevents a
polling race in which the tailer wakes after the first assistant block was
already appended and incorrectly seeks to the new end. The tailer begins at
`start_offset`, ignores user/system/tool-result records, and processes complete
assistant text entries appended for that turn.

If a session becomes selected in the middle of a turn and has no selected-turn
baseline, start at the current EOF. Do not narrate historical transcript data in
an attempt to catch up. Missing one in-progress block is safer than reading old
conversation while driving.

Persist the live cursor after each complete JSONL line. The cursor record should
contain path, device, inode, next byte offset, current turn ID, and the bounded
dedup state. If device/inode changes or the file shrinks below the stored offset,
treat it as rotation: stop the current turn, seek to EOF, and wait for the next
`UserPromptSubmit` boundary rather than replaying the file.

## Reading completed assistant blocks

The tail loop is incremental and line-oriented:

1. Read `selection.json` and `channels.json` under `.state.lock`.
2. Resolve the selected session's channel and current turn record.
3. Open its exact `transcript_path` and seek to the persisted offset.
4. Read only newline-terminated JSONL records. Retain an unterminated final line
   in memory and do not parse or advance past it.
5. Accept assistant-message entries only. From their content array, accept
   completed `type=text` blocks with non-blank text; ignore tool-use, thinking,
   system, user, and tool-result content.
6. Apply the existing `internal/clean` speech cleaning and cap policy. If the
   cleaned block is empty, mark it seen but do not enqueue it.
7. Atomically enqueue unseen blocks in transcript order through the same caption
   then `.txt` commit protocol used by the hook.
8. Persist the new cursor and seen key only after the queue commit succeeds. On
   failure, leave the cursor before that record so the block is retried.

The tailer must finish all eligible text blocks in one JSONL entry before moving
the durable offset past that line.

## Block identity and deduplication

Maintain a persistent, bounded seen-set per session and turn. Prefer a stable
transcript identity when present:

```text
block_key = sha256(session_id || message_id || content_block_index)
```

Also store a normalized text hash for Stop coordination:

```text
text_hash = sha256(cleaned_spoken_text)
```

If the transcript entry has no stable message ID, use a fallback key that still
distinguishes two legitimate identical replies:

```text
block_key = sha256(
  session_id || turn_id || device || inode || line_start_offset ||
  content_block_index || sha256(raw_text)
)
```

Do not use text hash alone as the block identity. Claude can legitimately emit
the same short sentence twice in one session; deduplicating only by text would
drop the second occurrence.

Store, for example, the newest 2,048 block keys and current-turn text hashes in
`tail-state/<session>.json`, written atomically. The file cursor handles normal
append-only progress; the seen-set handles restarts, partial queue commits, and
reprocessing around a line boundary.

Each queued caption should carry optional provenance without changing existing
transcript fields:

```json
{
  "id": "...",
  "session_id": "...",
  "project": "...",
  "text": "...",
  "source": "transcript_tail",
  "turn_id": "...",
  "block_key": "..."
}
```

## Stop-hook coordination and safety net

### Required invariant

For a selected turn, a completed assistant block is committed to the speech
queue at most once, regardless of whether it is observed first by the transcript
tailer or by the Stop hook.

### Normal path: tailer is alive

The Stop hook should not enqueue the final block directly. Instead it atomically
writes a stop intent containing the current session, turn ID, cleaned final text,
caption fields, and `text_hash`:

```json
{
  "version": 1,
  "session_id": "...",
  "turn_id": "...",
  "text_hash": "...",
  "text": "final cleaned block",
  "created_at_epoch": 0
}
```

The tailer owns resolution:

1. Drain that transcript through the current EOF, committing all unseen blocks.
2. If the stop intent's `text_hash` is already in the current turn's successfully
   enqueued text hashes, mark the intent satisfied and do not enqueue it.
3. If it is absent, wait one short grace interval (start with 500 ms), drain once
   more to close the append-vs-Stop race, then enqueue the Stop text exactly once
   using a key derived from `(session_id, turn_id, "stop_fallback", text_hash)`.
4. Persist the key/hash before atomically marking the stop intent resolved.

The tailer may see the final transcript line before or after the Stop event; the
result is the same. The grace interval affects only the missing-block safety path
and must not delay blocks already observed in the transcript.

### Safety path: tailer is down

Before writing only an intent, the Stop hook checks `.tail.pid` with the same
live-process semantics used for `synthd`. If the tailer is not alive, it enqueues
the final block immediately through the existing Stop path and records the same
turn-scoped dedup key. This preserves today's final-block behavior when stage 2
is enabled but its worker is unavailable.

A PID check cannot cover a crash one instruction later. Therefore unresolved
stop intents are durable and `raven tail` must process them first on restart.
`raven diagnose` should report their count and age. This is a safety net, not a
claim of exactly-once delivery across every possible filesystem crash; queue
commit and dedup-state commit cannot be one transaction. The design minimizes
the window and favors an occasional skipped duplicate over systematic
double-speak.

### Why Stop cannot remain an independent producer

The Stop hook's `last_assistant_message` is the final completed block that the
tailer will normally have observed. If both enqueue it without a shared ledger,
the driver hears the same conclusion twice. A timeout-only rule is also unsafe:
transcript append timing varies, and a fixed sleep merely moves the race. One
tailer-owned dedup decision is required.

## Selection behavior

The tailer speaks only the session currently named by Raven's selection state.
It must re-check selection before every queue commit, not only when it opens a
file.

- In follow mode, `UserPromptSubmit` selects the session and establishes its
  turn baseline before any assistant output.
- In pinned mode, blocks and Stop intents from non-selected sessions update
  channel metadata but are not spoken.
- If selection changes, stop tailing the old session immediately. Do not delete
  its durable cursor or seen-set; a future prompt starts a new turn.
- Do not backfill blocks completed while a session was unselected. The current
  product contract is selected-channel live narration, not an audio inbox.

There is an unavoidable race between selection changing and a queue commit.
Resolve it with the existing `.state.lock`: read selection, prepare the queue
files, re-check selection under the lock, and perform the final `.txt` rename
only if the session is still selected.

## Composition with streaming synthesis and sentence cuts

Each completed transcript block becomes an ordinary Raven reply job. With
`STREAM_SYNTH=1`, a long block is itself emitted as ordered sentence-sized audio
parts, so live narration gains both earlier block availability and sub-second
Kokoro startup. The queue protocol remains owned by
[`SCOPE_STREAMING_SYNTHESIS.md`](SCOPE_STREAMING_SYNTHESIS.md).

[`SCOPE_SENTENCE_CUT.md`](SCOPE_SENTENCE_CUT.md) can later preempt an active
block between those audio parts when a newer selected block or reply arrives.
Live narration does not need a second interruption format: block-level enqueue
order plus the shared per-part boundary is sufficient.

## Rollout and test plan

### Phase A: shadow tailer

Run `raven tail` with queue commits disabled. Log eligible block keys, text
hashes, timestamps, selection decisions, and matching Stop hashes. Compare its
predicted enqueue set against actual Stop-hook output for several days.

Acceptance: every selected Stop final block is either matched once or explicitly
classified as a safety fallback; no non-selected, tool, thinking, user, or
historical block is eligible.

### Phase B: enqueue with Stop safety intents

Enable live enqueueing behind `LIVE_NARRATION=1` for controlled sessions. Test:

- one prose-only turn with one assistant block;
- a tool-using turn with narration before and after multiple tool calls;
- identical assistant text occurring twice at different transcript positions;
- Stop before and after the tailer observes the final line;
- tailer death before Stop, after intent creation, and after queue commit;
- process restart with an unterminated JSONL line;
- follow/pinned selection changes during a turn; and
- transcript truncation/inode replacement.

Acceptance: block order matches transcript order, the final block is spoken once,
the Stop safety path speaks when the tailer is unavailable, and non-selected or
historical blocks never enter the queue.

### Phase C: drive test

With the phone locked and on the real car audio route, confirm intermediate
narration reduces unexplained silence without becoming noisy or repetitive.
Keep an immediate rollback to `LIVE_NARRATION=0`, which restores Stop-only
delivery.

## Feasibility and risks

| Area | Assessment |
| --- | --- |
| Reading completed blocks | Feasible. JSONL entries are append-only complete blocks and can be tailed incrementally. |
| Token-level speech | Not feasible under the accepted subscription-only constraint. |
| Earlier multi-step feedback | Feasible when Claude emits a prose block before tool work; no gain for a turn whose first/only prose block appears at the end. |
| Exactly-once coordination | Feasible in normal operation with one tailer-owned ledger, but higher risk than Stop-only delivery because queue and dedup files are not transactional together. |
| Session selection | Feasible using the existing locked channel/selection state; mid-turn switching deliberately does not backfill. |
| Narration quality | Product risk. Intermediate blocks may be useful progress or noisy implementation commentary; real drive testing must decide. |
| Failure recovery | Manageable with durable cursors, seen-sets, and stop intents, but adds a fifth long-lived process and new stale-state modes. |

The decisive go/no-go question after shadow mode is not whether the tailer can
read the file. It can. The question is whether the ownership protocol produces
one coherent audio narrative under Stop races, restarts, and selection changes.

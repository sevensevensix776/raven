# Raven streaming synthesis — stage-1 implementation design

## Status and decision

**Stage 1; recommended next build; not implemented.** Implement sentence-streamed
synthesis before live narration. It removes the largest avoidable delay without
changing when Claude Code decides that a reply is complete or which component
owns speech delivery.

The measured problem is time-to-first-word. A warm Kokoro render of a roughly
2,500-character reply takes about 15 seconds. Since the writer correctly waits
for `synthd` instead of racing it with `say`, the driver hears about 15 seconds
of comfort noise before the reply starts.

This design keeps the existing subscription-safe Stop-hook input. It changes the
unit published by `synthd` from one whole-reply WAV to an ordered stream of WAV
parts. The Go writer starts part `001` as soon as it is atomically visible while
`synthd` produces later parts behind playback.

Expected result: queue commit to Mac-side PCM emission starts in less than one
second for a warm model, rather than after the entire reply renders. Raven's
existing HLS and `AVPlayer` buffering still adds its normal downstream delay.

## Established constraint: this is not token streaming

Raven cannot speak Claude token-by-token on the user's Claude.ai subscription.
Claude Code's `includePartialMessages` path requires the Agent SDK, an
`ANTHROPIC_API_KEY`, and separately billed API credits. The user rejected that
path. The Claude Code `transcript_path` JSONL is not a fallback token stream: it
contains complete assistant text blocks and no `stream_event`,
`content_block_delta`, or other token-delta records.

This scope begins only after the Stop hook receives a completed reply. Earlier
speech from completed transcript blocks is a separate stage-2 design in
[`LIVE_NARRATION.md`](LIVE_NARRATION.md).

## Goals and non-goals

| Goal | Contract |
| --- | --- |
| Fast first word | Begin decoding `001.wav` as soon as it lands; do not wait for the rest of the reply. |
| Ordered delivery | Play every available part exactly once in ascending numeric order. |
| No interleaving | Once a reply begins, no later reply may play until the active reply reaches a terminal marker. |
| Continuous timeline | Emit the existing comfort-noise PCM whenever the next part is not ready. Never close the FIFO or restart the persistent HLS encoder. |
| Safe rollout | `STREAM_SYNTH=0` remains the default and preserves the current whole-reply path unchanged. |
| Existing emergency voice | If `synthd` is down before it claims a reply, the writer still uses one `say` fallback for the entire reply. |

This scope does not shorten the reply, provide token-level narration, or change
latest-wins policy. Streaming fixes startup latency, not total spoken duration.
Summarization is the lever for duration.

## Configuration and compatibility

Add one setting to `config.sh` and to `internal/config`:

```bash
# 1 = publish/play ordered Kokoro parts; 0 = current whole-reply WAV path
STREAM_SYNTH=0
```

`STREAM_SYNTH` uses the same precedence as the other Go settings: a non-empty
environment variable overrides `config.sh`. Only the exact value `1` enables
the new protocol.

When the flag is `0`, behavior and filenames remain unchanged:

```text
queue/<stamp>.caption.json
queue/<stamp>.txt
queue/<stamp>.wav            # atomically published whole reply
```

Both `synthd.py` and `raven write` must read the flag. A mixed configuration is
not supported: restart the Raven pipeline after changing it so both processes
agree. The old whole-reply code must remain intact as the rollback path.

## Streaming queue protocol

### 1. Hook commit remains unchanged

The Stop hook atomically publishes metadata first and text last:

```text
queue/<stamp>.caption.json
queue/<stamp>.txt            # ready/commit marker
```

The `.txt` contains the final spoken text, including `In <project>. ` when a
project is known. The caption contains the clean reply without that spoken
prefix. The hook must not split text or create part files.

### 2. `synthd` claims the job as one reply directory

In streaming mode, `synthd` processes root `.txt` jobs oldest-first. It creates
a hidden staging directory on the same filesystem, copies the committed request
and caption into it, fsyncs the files, and atomically renames the directory:

```text
queue/.<stamp>.claim/         # invisible staging name
  reply.txt
  caption.json

rename ->

queue/<stamp>/               # atomic claim/publication
  reply.txt
  caption.json
  request.json
```

`request.json` freezes the effective backend, model, voice, speed, and final
spoken-text hash for that reply. If summarization is enabled, run it once before
publishing the directory and store the summarized final spoken text in
`reply.txt`; recovery must never summarize the same request a second time.

Only after `queue/<stamp>/` is visible may `synthd` remove the original root
`<stamp>.txt` and `<stamp>.caption.json`. This ordering has two useful failure
properties:

- if `synthd` dies before the directory rename, the original `.txt` remains
  available to the existing synthd-down `say` fallback; and
- if it dies after the rename but before root cleanup, the reply directory is
  authoritative and the duplicate root files are ignored and cleaned on the
  next pass.

The writer treats a reply directory and root files with the same stamp as one
logical job; the directory wins. Hidden `.claim` directories are never played
and may be removed when their matching root `.txt` still exists.

### 3. Reuse Kokoro/misaki's existing chunking

Do not add a second sentence regex or tokenizer in stage 1. Kokoro's generator
already uses misaki to segment the input and yields ordered audio chunks. The
exact change to `Synth.kokoro` is to stop materializing all yields and
concatenating them:

```python
# Current whole-reply shape
segs = [result.audio for result in model.generate(...)]
sf.write(wav_part, np.concatenate(segs), 24000, format="WAV")

# Streaming shape
for index, result in enumerate(model.generate(...), start=1):
    write_part_atomically(reply_dir, index, result.audio)
publish_complete_atomically(reply_dir, part_count=index, status="complete")
```

Pass the full `reply.txt` to `model.generate` once. Because the project prefix is
already at the beginning of that text, it appears in the first generated part
only. Do not prepend it inside the loop and do not synthesize every sentence via
a separate model call.

Each generator yield is one ordered speech part. Tests should characterize its
sentence-boundary behavior with abbreviations, decimals, lists, and long
sentences, but the implementation should rely on the tokenizer already shipped
with the active Kokoro stack rather than creating competing rules.

### 4. Publish every part atomically

For each yield, write a same-directory temporary file with an explicit WAV
format, fsync/close it, then rename it to a zero-padded final name:

```text
queue/<stamp>/.001.wav.part   # never consumed
queue/<stamp>/001.wav         # atomic ready marker for part 1
queue/<stamp>/002.wav
queue/<stamp>/003.wav
```

Names are one-based, fixed-width, and contiguous. `synthd` must never publish
`003.wav` before `002.wav`, reuse an index, or revise a published part. Three
digits are sufficient for the current reply sizes; reject rather than wrap if a
reply would exceed `999` parts.

### 5. Publish completeness last

After the generator is exhausted, atomically publish
`queue/<stamp>/complete.json`:

```json
{"version":1,"id":"<stamp>","part_count":3,"status":"complete","backend":"kokoro"}
```

Write `complete.json.part`, fsync/close, then rename. Its presence is the only
proof that no more parts will arrive, and `part_count` is authoritative. The
writer may start before this marker exists, but it may not finish the reply or
advance to a later reply without it.

If generation returns no audio, fail before publishing `001.wav`; remove the
reply directory and restore or retain the root job for the existing whole-reply
fallback. If Kokoro fails before any part is published, `synthd` may keep its
current one-clip `say` fallback. If it fails after one or more parts are visible,
it must not publish a full-reply `say` clip, because that would repeat speech.
Instead log a terminal partial failure and atomically publish:

```json
{"version":1,"id":"<stamp>","part_count":2,"status":"partial","backend":"kokoro"}
```

The writer plays the published prefix, then closes that reply after part 2. This
is degraded delivery, but it is deterministic and cannot double-speak.

## Go writer state machine

`raven write` / `internal/write` becomes reply-aware in streaming mode. Its unit
of scheduling is a logical reply, not an arbitrary ready audio file.

```text
IDLE
  -> choose oldest logical reply
  -> if root .txt and synthd is down for >=5s: SAY_FALLBACK
  -> if reply directory: WAIT_FOR_001

ACTIVE(<stamp>, next=001)
  -> part exists: pre-roll once if first part; record transcript once; decode part
  -> next part missing and complete absent: emit comfort noise; keep same ACTIVE reply
  -> complete present and next <= part_count: wait for that exact part
  -> complete present and all 1..part_count consumed: remove reply; return IDLE
```

The load-bearing rules are:

1. Sort logical replies lexically by `<stamp>` and select the oldest.
2. Once a directory becomes active, pin it in the writer state. Do not inspect a
   later reply for playable audio while an expected part is missing.
3. Decode only the exact next integer. Never skip a gap because a higher part
   happens to exist.
4. Emit the existing 250 ms comfort-noise blocks while waiting. This is a bridge,
   not a new clip; it must not create transcript or emission events.
5. Emit the existing 350 ms pink pre-roll once, immediately before part `001`.
   Do not add pre-roll between parts.
6. Call `transcript.AddClaude` and log the reply-level `writer/emit` event once,
   when part `001` begins. Use `<stamp>/caption.json`; the transcript records the
   full clean reply once and does not contain `In <project>.`.
7. After a part's decoder exits, mark it consumed and remove its WAV. An atomic
   per-reply cursor such as `consumed.json` should store the highest contiguous
   consumed index so a writer restart cannot replay part `001` merely because
   later synthesis was still running.
8. Delete the reply directory only after the terminal marker exists and the
   cursor equals `part_count`. Then select the next reply.

`complete.json` solves two distinct races: a temporarily missing next part is
not mistaken for end-of-reply, and a fully synthesized later reply cannot be
interleaved into a still-synthesizing active reply.

### `say` fallback remains per reply

The writer's emergency fallback remains deliberately coarse. When the oldest
logical job is still a root `<stamp>.txt`, it is at least five seconds old, and
the `.synthd.pid` process is not alive, `raven write` synthesizes that entire
`.txt` once with macOS `say`, records the caption once, and removes the root job.

It does not synthesize individual missing parts. If `synthd` dies after a reply
directory has started playing, the writer holds that reply with comfort noise
until `synthd` recovers it or the normal stale-job policy expires it. Restart
recovery must use `consumed.json` to avoid republishing or replaying consumed
indexes. Falling back to the whole reply after part `001` has played is forbidden
because it recreates the double-speak bug.

On startup, `synthd` processes incomplete reply directories before new root
jobs, oldest stamp first. It reloads the frozen settings from `request.json`,
iterates the Kokoro generator from the beginning, discards yields whose indexes
are already covered by `consumed.json` or an existing final WAV, and resumes
atomic publication at the first missing index. It then publishes the terminal
marker normally. Re-iterating earlier yields costs compute after a crash but
does not replay them and avoids inventing a second checkpoint format inside
Kokoro.

## Cleanup and observability

Extend stale cleanup to reply directories. Age is based on the newest of the
directory's request, part, cursor, and terminal-marker mtimes. A stale active
reply is abandoned as one unit; never delete only `complete.json` or a middle
part and leave an unfinishable directory.

Add reply-level fields to existing structured events rather than logging every
comfort-noise loop:

| Event | Required fields |
| --- | --- |
| `synthd/part` | `id`, `part`, `ms`, `samples`, `chars_total` |
| `synthd/synth` | `id`, `backend`, `ok`, `status`, `parts`, `first_part_ms`, `total_ms`, `chars` |
| `writer/emit` | `id`, `mode=stream`, `first_part` |
| `writer/part` | `id`, `part`, `decode_ok` |
| `writer/reply_done` | `id`, `parts`, `status` |

`first_part_ms` is the key rollout metric. Total synthesis time remains useful
but is not the success criterion for this change.

## Test plan and acceptance criteria

### Unit and protocol tests

- Feed a fake Kokoro generator three deterministic arrays. Assert `001.wav`
  becomes visible before the generator is allowed to yield part 2, no `.part`
  file is consumable, names are contiguous, and `complete.json` appears last
  with `part_count=3`.
- Exercise failure before part 1 and after part 2. Assert the first uses the
  existing reply fallback and the second publishes `status=partial` without a
  full-reply fallback clip.
- Assert the Go writer waits for a missing `002.wav` even when a later reply is
  complete, emits comfort-noise PCM during the wait, and resumes `002` when it
  lands.
- Assert pre-roll, transcript insertion, project prefix, and reply-level emit
  logging happen once. The prefix must occur only in part 1; the caption text
  must never include it.
- Restart the writer after part 1 and verify `consumed.json` prevents replay.
- With `STREAM_SYNTH=0`, run the existing writer integration/parity suite and
  require no behavior or fixture changes.

### Audio parity

Refactor the Kokoro yield loop so both paths share it. For a fixed long fixture:

1. render the current whole-reply WAV by concatenating generator yields;
2. render the same yields as streaming parts;
3. concatenate the decoded part PCM in numeric order; and
4. assert equal 24 kHz mono format, equal sample count, and sample equality (or
   a documented near-zero tolerance if the WAV round trips require one).

Also verify the spoken text fixture is neither dropped nor duplicated across
boundaries. Total audio must match the whole-reply path; only publication timing
changes.

### End-to-end acceptance

| Scenario | Acceptance criterion |
| --- | --- |
| Warm Kokoro, roughly 2,500 characters | Stop-hook queue commit to writer beginning speech PCM is **<1 second**, versus the measured roughly 15 seconds. Record both first-part and total synthesis latency. |
| Multi-part reply | Parts play exactly once in numeric order. The FIFO never reaches EOF, the HLS encoder PID does not change, and any synthesis gap contains comfort noise rather than an underrun. |
| Following reply ready early | It does not play until the active reply's terminal marker exists and all declared parts are consumed. |
| `synthd` down before claim | One whole-reply `say` fallback is emitted; no part files and no duplicate Kokoro playback follow. |
| Flag rollback | `STREAM_SYNTH=0` restores the known-good single-WAV behavior after restart. |

Run the final locked-phone/car-route smoke test because gapless Mac PCM is
necessary but does not prove `AVPlayer` or the car head unit heard every
boundary.

## Composition with interruption and live narration

Per-part emission is the prerequisite for sentence-boundary interruption in
[`SCOPE_SENTENCE_CUT.md`](SCOPE_SENTENCE_CUT.md). That later scope should reuse
this directory, ordering, cursor, and terminal-marker protocol, then add a
latest-wins check between parts. It must not invent a second chunk format.

[`LIVE_NARRATION.md`](LIVE_NARRATION.md) changes when complete text
blocks enter the queue. Each narrated block can still use this streaming
synthesis path. Build and prove this scope first; live narration has a separate
deduplication and Stop-hook ownership problem.

## Honest limits

- Streaming reduces time-to-first-word, not Kokoro's total compute time or the
  reply's total spoken duration.
- A long reply can still take minutes to hear. Summarization, not streaming, is
  the control for that duration.
- Parts synthesized from separate generator yields may expose small prosody or
  loudness seams. Measure them on the real phone/car route.
- This design does not remove Raven's existing roughly 4–8-second HLS/player
  latency after PCM begins.
- Token-level speech remains impossible on the accepted subscription-only path.

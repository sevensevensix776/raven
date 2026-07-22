# `raven` — the Go binary

One dependency-free binary provides Raven's five commands. The ML boundary stays
Python: `synthd` uses Kokoro and `mlx-audio`, for which there is no useful Go
binding.

| Command | Role |
| --- | --- |
| `raven hook` | Claude Code hook. Maintains the channel registry; queues the final reply when the tailer is not running. |
| `raven tail` | Live narration. Follows the selected session's transcript and queues each completed text block mid-turn. |
| `raven write` | Emits the endless PCM timeline that feeds ffmpeg — idle floor, pre-roll, and queued clips oldest-first. |
| `raven serve` | HLS files plus the phone JSON API (channels, selection, transcript, health, log upload). |
| `raven diagnose` | Read-only health report across processes, stream, queue, and event log. |

This directory is the binary only. A working deployment also needs `synthd.py`,
the ffmpeg HLS encoder, and the iOS client — see the [repository
README](../README.md).

## Packages

No third-party Go dependencies; `go.mod` declares only the module and Go 1.25.

| Path | Responsibility |
| --- | --- |
| [`main.go`](./main.go) | CLI dispatch across the five subcommands. |
| [`internal/hook`](./internal/hook) | Parses Claude Code hook JSON from stdin, updates channel state, applies selection gating, and commits queue files atomically. Yields to a live tailer on `Stop`. |
| [`internal/tail`](./internal/tail) | Extracts completed assistant text blocks from a session transcript with a durable byte cursor and a bounded dedup set. `thinking` and `tool_use` blocks never qualify. |
| [`internal/state`](./internal/state) | `channels.json` and `selection.json` under `.state.lock`: follow/pin semantics, `SessionEnd` removal and unstick, TTL pruning, a 50-channel ceiling, and the last three replies per channel. Atomic renames. |
| [`internal/clean`](./internal/clean) | Reply text to speakable text: removes fenced and inline code, Markdown punctuation, and long paths; collapses whitespace; applies a byte cap; rewrites mispronounced symbols. |
| [`internal/rctitle`](./internal/rctitle) | Resolves a session's Remote Control name from its transcript — last user-set `customTitle` wins, else the last `aiTitle`. |
| [`internal/config`](./internal/config) | Parses `$RAVEN_HOME/config.sh` as plain `KEY=value`; a non-empty environment variable overrides the file. |
| [`internal/rlog`](./internal/rlog) | Fail-soft structured records appended to `logs/events.jsonl`, one append write each. |
| [`internal/transcript`](./internal/transcript) | Appends selected prompts and emitted captions to `spoken.jsonl` under `.transcript.lock`, retaining the last 200 lines. |
| [`internal/serve`](./internal/serve) | Serves `<home>/hls`, maintains the listener heartbeat, and implements the phone JSON API with ETags and locked selection writes. |
| [`internal/write`](./internal/write) | Emits uninterrupted 24 kHz mono s16le PCM, gates queue consumption on the listener heartbeat, drives ffmpeg pre-roll/idle/decode, and falls back to `say` when synthd is down. |
| [`internal/diagnose`](./internal/diagnose) | Renders the read-only health report and its verdict. |

Every package except `rctitle`, `rlog`, and `state` carries its own `_test.go`.

## Hook flow

```text
Claude Code hook JSON on stdin
            │
            ▼
  Parse payload and config
            │
            ▼
Update channels.json + selection.json
under .state.lock and prune stale channels
            │
            ├── UserPromptSubmit ──► record selected prompt; stop
            ├── SessionEnd ────────► remove channel, unstick pin; stop
            └── Stop
                 │
                 ▼
       Is a live tailer running? ──► yes: log stop_yield_to_tailer; stop
                 │ no
                 ▼
       Is this the selected channel?
       (or does `speak-all` exist?)
            │ no                 │ yes
            ▼                    ▼
       log gate_skip      clean assistant reply
                                 │
                                 ▼
                       write `<id>.caption.json`
                                 │
                                 ▼
                       rename `<id>.txt` atomically
                       as the queue commit marker
```

Registry maintenance happens before speech gating, so every active session stays
visible even when only one channel is selected. In follow mode,
`UserPromptSubmit` makes that session active. A phone-side pin survives until it
is changed or its session ends. Idle channels expire after `CHANNEL_TTL_HOURS`
unless pinned; the registry holds at most 50.

The queue protocol is deliberately ordered: caption metadata is committed first,
and the atomic `.txt` rename announces that an item is ready for synthesis. If
the text commit fails, the orphaned caption is removed.

### Load-bearing JSON invariant

`Channel.Recent` must always serialize as an empty array when it has no entries:

```json
{"recent":[]}
```

It must **never** serialize as `null`. The server iterates this field in
`/catchup`; `null` crashes that route. `internal/state` keeps the slice non-nil
specifically for this contract, and `TestRecentIsNeverNull` guards it.

## Build, test, install

```bash
cd "$RAVEN_HOME/cli"
go build -o raven .
go test ./...
go vet ./...
./install.sh          # do not cp over the live binary — see below
```

Use [`install.sh`](./install.sh) rather than `cp`. Replacing the executable
in place can make newly spawned processes die with `SIGKILL` on macOS while
long-lived Raven processes still map the old inode.

For an isolated run, point the hook at a temporary home. The directory must
already exist — the hook intentionally no-ops when it does not.

```bash
RAVEN_TEST_HOME="$(mktemp -d)"
mkdir -p "$RAVEN_TEST_HOME/queue"
printf '%s' '{"hook_event_name":"UserPromptSubmit","session_id":"demo","cwd":"/tmp/raven-demo","prompt":"Start the tests"}' \
  | RAVEN_HOME="$RAVEN_TEST_HOME" ./raven hook
```

The server binds `127.0.0.1:8080` by default; `RAVEN_BIND` or `--addr` overrides
it. `raven diagnose` accepts `--since-min` to widen its lookback window.

```bash
RAVEN_HOME=/path/to/isolated/raven raven serve --addr 127.0.0.1:8081
raven diagnose --since-min 60
```

### Configuration

Read from `$RAVEN_HOME/config.sh`, with a non-empty environment variable winning:

```bash
MAX_SPOKEN_CHARS=0    # byte cap; 0 means unlimited
CHANNEL_TTL_HOURS=6   # idle-channel retention backstop
IDLE_FLOOR=noise      # noise (proven) or silence between clips
LIVE_NARRATION=1      # 0 disables the tailer; Stop-hook speech resumes
```

This is a small `KEY=value` parser, not a shell interpreter: expansion, command
substitution, and `source` are not evaluated.

## Claude Code wiring

Append the same entry to each of `UserPromptSubmit`, `Stop`, and `SessionEnd` in
`~/.claude/settings.json`, preserving any hooks already configured there:

```json
{
  "matcher": "",
  "hooks": [
    {
      "type": "command",
      "command": "~/.local/bin/raven hook",
      "timeout": 2
    }
  ]
}
```

The two-second timeout is intentional. `raven hook` emits no user-facing output
and treats malformed payloads, missing state, logging failures, and queue
failures as no-ops, so speech can never block a Claude Code turn.

## Limits and deliberate tradeoffs

- The hook is deliberately fail-silent. That protects Claude Code's critical
  path, but failures must be diagnosed from Raven's state, queue, and logs
  rather than from hook stderr.
- Locking uses Unix `flock` via `syscall`; the state and transcript paths are
  Unix-specific.
- `RAVEN_HOME` must name an existing directory. Raven creates `queue/` and
  `logs/` beneath it, but a missing home is an immediate no-op.
- Caps are measured in bytes. Go backs off to a valid UTF-8 boundary when a cap
  lands inside a multibyte character.
- State and transcript updates are explicitly locked. Event logging relies on a
  single append write rather than an explicit `flock` — a weaker guarantee,
  adequate for line-oriented records on a local filesystem.

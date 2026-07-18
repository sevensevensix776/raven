# Raven, in Go

Raven speaks Claude Code’s replies through an iPhone so a session can keep moving while its user is driving. This repository is the Go port of Raven’s Claude Code hook—the latency-sensitive front door that runs on every prompt and completed reply. The port is protected by a parity harness that drives the original Bash hook and the Go hook with the same events, then compares their state, transcript, log, and queue output; all five current scenarios pass. Go is a practical fit for the hook’s two-second execution budget: the compiled program starts in roughly 1 ms instead of paying Python’s roughly 50–100 ms interpreter-and-import cost on every turn, ships as one standard-library-only binary, avoids virtual environments and installed-versus-repository drift, and follows the same local-binary convention as `hermes`. The ML boundary stays where it belongs: `synthd` still uses Python, Kokoro, and `mlx-audio`, for which there is no useful Go binding.

> **Current scope:** `raven hook` is implemented. `serve`, `write`, and `diagnose` appear in the command usage but are not implemented yet.

## Safety: parity before replacement

The Go port was not validated from a rewritten specification. [`parity_test.py`](./parity_test.py) runs the original `~/.claude/hooks/speak-reply.sh` and `./raven hook` against identical payload sequences and isolated `RAVEN_HOME` directories. It normalizes timestamps and timestamp-derived IDs, then compares the behaviorally meaningful output:

- `channels.json` and `selection.json`
- queued speech and caption files
- `spoken.jsonl`
- structured events in `logs/events.jsonl`

The five cases cover follow-mode selection and speech, rejection of a non-selected channel, pinned-session cleanup on `SessionEnd`, code/path cleaning, and the rolling three-reply catch-up history.

The original Bash hook remains installed as the rollback path.

## Architecture

The project has no third-party Go dependencies; `go.mod` declares only the module and Go version.

| Path | Responsibility |
| --- | --- |
| [`main.go`](./main.go) | CLI dispatch. Routes `raven hook`; rejects every other command for now. |
| [`internal/hook`](./internal/hook) | Reads Claude Code hook JSON from stdin, updates channel state, applies selection gating, cleans eligible replies, and commits queue files atomically. Resolves `RAVEN_HOME`, falling back to `~/speech`. |
| [`internal/clean`](./internal/clean) | Pure-Go port of the Bash `sed`/`tr` speech-cleaning pipeline: removes fenced code, inline code, Markdown punctuation, and long paths; collapses whitespace; applies a byte cap. |
| [`internal/state`](./internal/state) | Maintains `channels.json` and `selection.json` under `.state.lock`: follow/pin semantics, `SessionEnd` removal and unstick, TTL pruning, a 50-channel ceiling, and the last three replies per channel. Writes compact JSON through atomic renames. |
| [`internal/config`](./internal/config) | Reads `MAX_SPOKEN_CHARS` and `CHANNEL_TTL_HOURS` from `~/speech/config.sh` (or the overridden Raven home), with non-empty environment variables taking precedence. |
| [`internal/rlog`](./internal/rlog) | Appends fail-soft, Python-compatible structured records to `logs/events.jsonl`. Each record is emitted with one append write. |
| [`internal/transcript`](./internal/transcript) | Adds selected user prompts to `spoken.jsonl` as screen-only `role=user` entries. Serializes updates with `.transcript.lock`, writes atomically, and retains the last 200 lines. |
| [`internal/hook/hook_test.go`](./internal/hook/hook_test.go) | Unit coverage for follow selection, queueing, channel gating, `SessionEnd`, and the non-null catch-up invariant. |
| [`internal/clean/clean_test.go`](./internal/clean/clean_test.go) | Table-driven cleaning tests for code, Markdown, paths, whitespace, blank input, and byte caps. |
| [`parity_test.py`](./parity_test.py) | Cross-language behavior harness for the installed Bash hook and the local Go binary. |

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

Registry maintenance happens before speech gating, so every active session remains visible even when only one channel is selected. In follow mode, `UserPromptSubmit` makes that session active. A phone-side pin is preserved until it is changed or its session ends. `SessionEnd` removes the channel and returns an ended pinned selection to follow mode. Idle channels expire after `CHANNEL_TTL_HOURS` unless pinned; the registry retains at most 50 channels.

The queue protocol is deliberately ordered: caption metadata is committed first, and the atomic `.txt` rename announces that an item is ready for synthesis. If the text commit fails, the orphaned caption is removed.

### Load-bearing JSON invariant

`Channel.Recent` must always serialize as an empty array when it has no entries:

```json
{"recent":[]}
```

It must **never** serialize as `null`. Raven’s server iterates this field in `/catchup`; `null` crashes that route. `internal/state` initializes and preserves a non-nil slice specifically for this contract, and `TestRecentIsNeverNull` guards it.

## Build, test, and install

The module currently targets Go 1.25.

```bash
cd ~/code/experiments/raven-go
go build -o raven .
go test ./...
python3 parity_test.py
install -m 0755 raven ~/.local/bin/raven
```

Run the parity test after building: it expects `./raven`, the original hook at `~/.claude/hooks/speak-reply.sh`, and Raven’s Python compatibility helpers in `~/speech`.

For an isolated run, point the hook at a temporary Raven home. The directory must already exist; the hook intentionally no-ops when it does not.

```bash
RAVEN_TEST_HOME="$(mktemp -d)"
mkdir -p "$RAVEN_TEST_HOME/queue"
printf '%s' '{"hook_event_name":"UserPromptSubmit","session_id":"demo","cwd":"/tmp/raven-demo","prompt":"Start the tests"}' \
  | RAVEN_HOME="$RAVEN_TEST_HOME" ./raven hook
```

Without the override, Raven uses `~/speech`:

```bash
RAVEN_HOME=/path/to/isolated/speech ~/.local/bin/raven hook
```

### Configuration

The hook recognizes two values from `$RAVEN_HOME/config.sh`, with a non-empty environment variable overriding the file:

```bash
MAX_SPOKEN_CHARS=0   # byte cap; 0 means unlimited
CHANNEL_TTL_HOURS=6  # idle-channel retention backstop
```

This is intentionally a small `KEY=value` parser, not a shell interpreter.

## Claude Code wiring

Append the same hook entry to each of `UserPromptSubmit`, `Stop`, and `SessionEnd` in `~/.claude/settings.json`:

```json
{
  "matcher": "",
  "hooks": [
    {
      "type": "command",
      "command": "/Users/asifahmed/.local/bin/raven hook",
      "timeout": 2
    }
  ]
}
```

The entry belongs in each event’s array; preserve any other hooks already configured for those events. The two-second timeout is intentional. `raven hook` emits no user-facing output and treats malformed payloads, missing state, logging failures, and queue failures as no-ops so speech can never block a Claude Code turn.

## Rollback

The Bash implementation is retained at `~/.claude/hooks/speak-reply.sh`. To roll back, change the Raven command in all three Claude Code event entries—`UserPromptSubmit`, `Stop`, and `SessionEnd`—from:

```text
/Users/asifahmed/.local/bin/raven hook
```

to:

```text
bash /Users/asifahmed/.claude/hooks/speak-reply.sh
```

Keep the existing `"timeout": 2`, save `~/.claude/settings.json`, and start a fresh Claude Code session. No state migration is required: both hooks use the same files and compatible JSON formats.

## Port status

| Component | Current implementation | Status |
| --- | --- | --- |
| Claude Code hook | Go: `raven hook` | Ported, installed, and parity-tested 5/5 |
| Reply cleaning | Go: `internal/clean` | Ported and unit-tested; checked against the Bash pipeline |
| Channel registry and selection | Go: `internal/state` | Ported; compatible with the Python writer and phone-side selection |
| Event log and user transcript | Go: `internal/rlog`, `internal/transcript` | Ported in the hook path; output format remains compatible with Python consumers |
| Speech synthesis | Python: `synthd`, Kokoro, `mlx-audio` | Intentionally stays Python |
| HTTP server / phone surface | Existing Raven implementation | Not yet exposed as `raven serve` |
| Writer orchestration | Existing Raven implementation | Not yet exposed as `raven write` |
| Diagnostics | Existing Raven tooling | `raven diagnose` is advertised but not implemented |

## Roadmap

1. **`raven serve`** — move the non-ML server and phone-facing control surface behind the Go binary while preserving its current wire and state contracts.
2. **`raven write`** — port writer orchestration and queue consumption, keeping `synthd` as the Python ML process.
3. **`raven diagnose`** — consolidate health and state inspection once the serving and writing boundaries are stable.

Each port should follow the hook’s migration rule: establish compatibility fixtures first, compare old and new behavior on the same inputs, then change the installed entry point while retaining a rollback path.

## Limits and deliberate tradeoffs

- This repository is not a complete standalone Raven deployment. Today it supplies the Claude Code hook; the existing server, writer, and Python synthesizer still complete the path to the phone.
- The implementation uses Unix `flock` through `syscall`, so the state and transcript locking code is Unix-specific.
- The hook is intentionally fail-silent. That protects Claude Code’s critical path, but operational failures must be diagnosed from Raven’s state, queue, and logs rather than hook stderr.
- `RAVEN_HOME` must name an existing directory. Raven creates `queue/` and `logs/` beneath it, but a missing home causes an immediate no-op.
- `config.sh` parsing supports straightforward assignments and comments only; shell expansion, command substitution, and sourced files are not evaluated.
- Caps are measured in bytes for Bash compatibility. Go backs off to a valid UTF-8 boundary if a cap lands inside a multibyte character, a deliberate difference from raw `head -c` behavior.
- State and transcript updates are explicitly locked. Event logging currently uses a single append write rather than an explicit `flock`; concurrent records are expected to remain line-oriented on the local filesystem, but this is a weaker guarantee than the state path.
- The parity harness depends on the retained local Bash/Python installation. It is a migration safety test, not a hermetic cross-platform test suite.

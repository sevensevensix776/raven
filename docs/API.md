# Tailnet API and on-disk state

Reference for the HTTP surface `raven serve` exposes and the files the pipeline
reads and writes. For what Raven is and how to run it, see the
[README](../README.md).

> **No authentication.** Every endpoint below answers any request that reaches
> the port. The only boundary is the network — see [SECURITY.md](../SECURITY.md).

## HTTP endpoints

The server binds `127.0.0.1:8080` unless `RAVEN_BIND` says otherwise.
`RAVEN_BIND` is an environment variable, not a `config.sh` setting.

| Method and path | Request | Response and behaviour |
|---|---|---|
| `GET /stream.m3u8` | — | Live HLS playlist. Each GET refreshes `hls/.heartbeat`, marking a listener live for ten seconds. Segments are served from the same root. |
| `GET /channels` | Optional `If-None-Match` | Channels newest-first plus `{mode, session_id}` selection. `304` when the ETag matches. |
| `GET /transcript?limit=50` | Optional `If-None-Match`; `limit` clamped to 1–100 | Most recent emitted transcript lines as `{"lines":[...]}`. `304` when unchanged. |
| `GET /catchup` | — | The recent replies retained per channel. Iterates `Channel.Recent`, which is why that field must never serialize as `null`. |
| `POST /active` | `{"mode":"pinned","session_id":"…"}` or `{"mode":"follow","session_id":null}` | Pins a known session or restores follow mode. Locked and atomic. |
| `GET /health` | — | Heartbeat age, listener state, queue counts, selection, channel count, last spoken record. |
| `POST /log` | `{"device":"iphone","lines":["…"]}`; body limit 256 KiB | Appends up to 2,000 lines to `logs/phone.jsonl` and records a `phone/log_upload` event. |

## On-disk state

All paths are relative to `$RAVEN_HOME`. Everything here is gitignored.

| Path | Contents |
|---|---|
| `queue/` | Pending `.txt`, ready `.wav`/`.aiff`, and `.caption.json` jobs. Files older than ten minutes are discarded. |
| `channels.json` | Recent Claude sessions, project names, last activity, and a short last-line preview. |
| `selection.json` | Follow/pinned mode, selected session, and most recently prompted follow session. |
| `spoken.jsonl` | Last 200 transcript entries, rewritten atomically when emission starts. |
| `tail-state/<session>.json` | The tailer's durable byte cursor and bounded dedup set per session. |
| `logs/events.jsonl` | Unified structured hook, tailer, synthesis, writer, server, and phone-upload events. Trimmed to the newest 20,000 lines past a size threshold. |
| `logs/phone.jsonl` | Playback log lines uploaded from the iPhone. |
| `.detached.log` | Combined stdout/stderr from the detached Mac processes. |
| `pcm.fifo` | The writer's PCM output, read by the persistent encoder. |
| `hls/` | Live segments, playlist, and `.heartbeat`. |

### Locks and PID files

| File | Purpose |
|---|---|
| `.state.lock` | `fcntl` lock shared by the hook, the tailer, and the server for `channels.json` / `selection.json`. |
| `.transcript.lock` | Serializes `spoken.jsonl` writes. |
| `.write.pid`, `.ffmpeg.pid`, `.serve.pid`, `.synthd.pid`, `.tail.pid` | The five detached processes. `.tail.pid` doubles as the hook's liveness probe: if that process answers signal 0, `Stop` yields to the tailer. |

## Queue protocol

Producers write temporary files and rename them into place:

1. `<stamp>.caption.json` is committed **first** — metadata.
2. `<stamp>.txt` is renamed into place **last** — this rename is the ready marker.

If the text commit fails, the orphaned caption is removed. `synthd` publishes
`.wav` only after the complete file exists. `raven write` must never observe a
half-written job or clip.

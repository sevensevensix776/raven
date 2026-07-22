# Raven — Interrupt / Skip design (latest-wins)

> DECISION (Asif, 2026-07-17): **latest wins** — a newer reply for the
> selected channel cuts off the current one immediately. Build deferred until
> after a drive on the current pipeline. This doc is the ready-to-build design
> from a Codex (gpt-5.6-sol) consult.

## Core principle
Kill ONLY the per-clip decoder, NEVER the persistent HLS encoder (.ffmpeg.pid),
the FIFO, or the HLS timeline. Bridge every decoder handoff with cached raw PCM
(hls/.idle-500ms.pcm) so `ffmpeg -re` never underruns and the app never stalls.
Writer polls queue/.latest + queue/.interrupt every 50ms during clip playback.
POST /skip writes the interrupt token; the hook writes .latest on each new reply.
Preempted utterances stay in /transcript marked preempted/skipped (transcript_finish.py).

## Full design + code (from the consult)
```

codex
The right default is **(a) latest wins** for the selected channel. Kill only the per-utterance decoder, keep the persistent HLS encoder alive, immediately bridge with pre-generated idle PCM, and coalesce the queue around a `.latest` pointer.

“Finish current, then newest” is not enough here: Raven currently has a roughly **5m19s** WAV in the queue. That policy can still leave the driver trapped for minutes.

## 1. Preemption model

**Recommendation — Latest selected-channel reply wins; interrupt the current utterance immediately.**

Freshness matters more than perfect prose continuity while driving. The cut will be mildly jarring, but waiting several minutes is worse; later, you can make automatic preemption occur at Kokoro sentence/chunk boundaries while keeping manual Skip immediate.

**Code — atomically commit the new reply, mark it latest, and change the interrupt token.**

Replace the final `mv` pair in [speak-reply.sh](../hooks/speak-reply.sh) with:

```bash
# Queue commit + latest-wins signal share a lock with /skip.
if python3 - "$Q" "$stamp" "$text_tmp" "$meta_tmp" <<'PY'
import fcntl
import os
import pathlib
import sys
import tempfile
import time

queue = pathlib.Path(sys.argv[1])
stamp = sys.argv[2]
text_tmp = pathlib.Path(sys.argv[3])
meta_tmp = pathlib.Path(sys.argv[4])
lock_path = queue.parent / ".queue.lock"

def atomic_text(path, text):
    fd, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        with os.fdopen(fd, "w") as handle:
            handle.write(text)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
    finally:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass

with lock_path.open("a+") as lock:
    fcntl.flock(lock, fcntl.LOCK_EX)

    # Data first. The control token changes only after the new job exists.
    os.replace(meta_tmp, queue / f"{stamp}.caption.json")
    os.replace(text_tmp, queue / f"{stamp}.txt")
    atomic_text(queue / ".latest", stamp + "\n")
    atomic_text(
        queue / ".interrupt",
        f"new:{stamp}:{time.time_ns()}\n",
    )
PY
then
  exit 0
fi

rm -f "$text_tmp" "$meta_tmp" 2>/dev/null
exit 0
```

Also make `synthd.py` synthesize only `.latest`, and verify it is still latest before publishing:

```python
LATEST = QUEUE / ".latest"


def latest_id() -> str:
    try:
        return LATEST.read_text().strip()
    except OSError:
        return ""


def is_latest(stamp: str) -> bool:
    return bool(stamp) and latest_id() == stamp
```

In its main loop, replace the oldest-first selection:

```python
stamp = latest_id()
f = QUEUE / f"{stamp}.txt" if stamp else None
if f is None or not f.exists():
    time.sleep(0.2)
    continue
```

Guard Kokoro publication:

```python
superseded = False

if cfg["VOICE_BACKEND"] == "kokoro":
    try:
        if synth.kokoro(
            text,
            cfg["KOKORO_VOICE"],
            cfg["KOKORO_MODEL"],
            wav_part,
        ):
            if is_latest(stamp):
                os.replace(wav_part, wav)
                done = True
                backend = "kokoro"
            else:
                superseded = True
                wav_part.unlink(missing_ok=True)
    except Exception as e:
        ravenlog.log("synthd", "kokoro_fail", id=stamp, err=str(e)[:200])

if not done and not superseded:
    try:
        synth.say(text, cfg["SAY_VOICE"], wav)
        published_aiff = wav.with_suffix(".aiff")
        if is_latest(stamp):
            done = True
            backend = "say"
        else:
            superseded = True
            published_aiff.unlink(missing_ok=True)
    except Exception as e:
        ravenlog.log("synthd", "say_fail", id=stamp, err=str(e)[:200])
```

This prevents an old synthesis already underway from “resurrecting” after a newer reply or manual skip.

## 2. Server-side interrupt mechanism

**Recommendation — Kill the clip decoder, never the persistent HLS encoder; bridge immediately with cached PCM.**

This is viable because the parent writer still owns the FIFO’s write descriptor, so killing the child does not send EOF downstream. Poll a generation token every 50ms, terminate the child within 200ms, and immediately write a 500ms idle block.

**Code — core replacement for [writer.sh](../writer.sh).**

```bash
#!/bin/bash
set -uo pipefail

cd "$HOME/code/experiments/raven" || exit 1
[ -f config.sh ] && . ./config.sh

IDLE_FLOOR="${IDLE_FLOOR:-noise}"
HB="hls/.heartbeat"
LATEST="queue/.latest"
INTERRUPT="queue/.interrupt"
IDLE_PCM="hls/.idle-500ms.pcm"
PREROLL_PCM="hls/.preroll-350ms.pcm"
clip_pid=""

make_pcm_blocks() {
  local idle_source

  if [ "$IDLE_FLOOR" = "silence" ]; then
    idle_source="anullsrc=r=24000:cl=mono:d=0.5"
  else
    idle_source="anoisesrc=r=24000:c=pink:a=0.002:d=0.5"
  fi

  if [ ! -s "$IDLE_PCM" ]; then
    ffmpeg -y -loglevel quiet -f lavfi -i "$idle_source" \
      -f s16le -ar 24000 -ac 1 -acodec pcm_s16le "$IDLE_PCM"
  fi

  if [ ! -s "$PREROLL_PCM" ]; then
    ffmpeg -y -loglevel quiet \
      -f lavfi -i "anoisesrc=r=24000:c=pink:a=0.002:d=0.35" \
      -f s16le -ar 24000 -ac 1 -acodec pcm_s16le "$PREROLL_PCM"
  fi
}

latest_id() {
  [ -f "$LATEST" ] && tr -d '\r\n' < "$LATEST"
}

interrupt_token() {
  if [ -f "$INTERRUPT" ]; then
    cat "$INTERRUPT"
  else
    printf 'boot'
  fi
}

ready_path() {
  local id="$1"

  if [ -f "queue/$id.wav" ]; then
    printf 'queue/%s.wav' "$id"
  elif [ -f "queue/$id.aiff" ]; then
    printf 'queue/%s.aiff' "$id"
  fi
}

listener_is_live() {
  [ -f "$HB" ] || return 1
  local age
  age=$(( $(date +%s) - $(stat -f %m "$HB") ))
  [ "$age" -le 10 ]
}

stop_clip() {
  local pid="$1"

  kill -TERM "$pid" 2>/dev/null || true

  # Never let decoder shutdown hold the PCM handoff for long.
  for _ in 1 2 3 4; do
    kill -0 "$pid" 2>/dev/null || break
    sleep 0.05
  done

  if kill -0 "$pid" 2>/dev/null; then
    kill -KILL "$pid" 2>/dev/null || true
  fi

  wait "$pid" 2>/dev/null || true
}

cleanup_child() {
  if [ -n "$clip_pid" ] && kill -0 "$clip_pid" 2>/dev/null; then
    stop_clip "$clip_pid"
  fi
}

trap cleanup_child EXIT INT TERM
make_pcm_blocks

while true; do
  # Existing stale-item cleanup remains useful for abandoned artifacts.
  find queue \( \
      -name '*.txt' -o \
      -name '*.aiff' -o \
      -name '*.wav' -o \
      -name '*.caption.json' -o \
      -name '*.part' \
    \) -mmin +10 -delete 2>/dev/null

  id="$(latest_id)"
  f="$(ready_path "$id")"

  if ! listener_is_live || [ -z "$id" ] || [ -z "$f" ]; then
    cat "$IDLE_PCM"
    continue
  fi

  token_before="$(interrupt_token)"

  # Avoid starting a stale selection if a hook raced ready_path().
  if [ "$id" != "$(latest_id)" ] ||
     [ "$token_before" != "$(interrupt_token)" ]; then
    continue
  fi

  metadata="queue/$id.caption.json"
  cat "$PREROLL_PCM"

  python3 transcript_add.py "$metadata" 2>/dev/null
  python3 ravenlog.py writer emit id="$id" 2>/dev/null

  ffmpeg -nostdin -loglevel quiet -i "$f" \
    -f s16le -ar 24000 -ac 1 -acodec pcm_s16le - &
  clip_pid=$!

  outcome="completed"

  while kill -0 "$clip_pid" 2>/dev/null; do
    token_now="$(interrupt_token)"

    if [ "$token_now" != "$token_before" ]; then
      case "$token_now" in
        skip:*) outcome="skipped" ;;
        *)      outcome="preempted" ;;
      esac

      stop_clip "$clip_pid"
      clip_pid=""

      # A cached raw block starts writing with essentially no process startup.
      # It covers the handoff while the newest WAV becomes ready.
      cat "$IDLE_PCM"
      break
    fi

    sleep 0.05
  done

  if [ -n "$clip_pid" ]; then
    wait "$clip_pid" 2>/dev/null || true
    clip_pid=""
  fi

  python3 transcript_finish.py "$id" "$outcome" 2>/dev/null
  python3 ravenlog.py writer "$outcome" id="$id" 2>/dev/null

  rm -f "$f" "$metadata"
done
```

Do not kill `.ffmpeg.pid`, recreate `pcm.fifo`, restart HLS, or insert an HLS discontinuity. Only the per-clip decoder is disposable.

Automatic sentence-boundary interruption is a later refinement: have Kokoro publish 8–15-second sentence chunks, checking `.latest` between chunks. It will sound nicer, but I would first prove the simpler hard cut is stable.

## 3. Manual Skip

**Recommendation — Add `POST /skip`; client-only live-edge seeking cannot stop Mac-side production.**

A client seek only discards buffered HLS audio. If the writer is still emitting the old five-minute WAV, the new live edge is still that same WAV; however, after server-side interruption, a delayed live-edge seek is useful for throwing away the iPhone’s remaining 4–8 seconds of old buffer.

**Code — server endpoint.**

Add to [server.py](../server.py):

```python
QUEUE = SPEECH / "queue"
QUEUE_LOCK = SPEECH / ".queue.lock"
LATEST = QUEUE / ".latest"
INTERRUPT = QUEUE / ".interrupt"


@contextlib.contextmanager
def queue_lock():
    with QUEUE_LOCK.open("a+") as handle:
        fcntl.flock(handle, fcntl.LOCK_EX)
        try:
            yield
        finally:
            fcntl.flock(handle, fcntl.LOCK_UN)


def atomic_text(path, value):
    fd, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        with os.fdopen(fd, "w") as handle:
            handle.write(value)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
    finally:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass
```

Add this method to `HuginnHandler`:

```python
def handle_skip(self):
    token = f"skip:{time.time_ns()}"

    with queue_lock():
        # Stop selecting anything before removing queue artifacts.
        atomic_text(LATEST, "")

        for pattern in (
            "*.txt",
            "*.wav",
            "*.aiff",
            "*.caption.json",
            "*.part",
        ):
            for path in QUEUE.glob(pattern):
                try:
                    path.unlink()
                except FileNotFoundError:
                    pass

        # Written last: the writer sees the flush as one committed command.
        atomic_text(INTERRUPT, token + "\n")

    ravenlog.log("server", "skip", token=token)
    self.json_response(
        {"accepted": True, "token": token},
        conditional=False,
    )
```

Route it before `/active` body validation:

```python
def do_POST(self):
    path = urllib.parse.urlsplit(self.path).path

    if path == "/skip":
        self.handle_skip()
        return

    if path == "/log":
        self.handle_log_upload()
        return

    if path != "/active":
        self.send_error(404)
        return

    # Existing /active implementation follows.
```

Add to [HuginnAPI.swift](~/code/experiments/ear/Ear/HuginnAPI.swift):

```swift
func skip() async -> Bool {
    var request = URLRequest(url: baseURL.appendingPathComponent("skip"))
    request.httpMethod = "POST"
    request.timeoutInterval = 4

    do {
        let (_, response) = try await session.data(for: request)
        guard let http = response as? HTTPURLResponse,
              http.statusCode == 200 else {
            throw URLError(.badServerResponse)
        }
        errorText = nil
        return true
    } catch {
        errorText = "Could not skip"
        return false
    }
}
```

Expose the existing live-edge operation in [PlaybackController.swift](~/code/experiments/ear/Ear/PlaybackController.swift):

```swift
func jumpToLiveEdge() {
    runOnMain { [weak self] in
        self?.playAtLiveEdge()
    }
}
```

Add state and a button in [EarApp.swift](~/code/experiments/ear/Ear/EarApp.swift):

```swift
@State private var skipInFlight = false
```

Inside the transport `HStack`, before Mute:

```swift
TransportButton(
    symbol: "forward.end.fill",
    label: "Skip",
    selected: skipInFlight
) {
    guard !skipInFlight else { return }

    Task {
        skipInFlight = true
        defer { skipInFlight = false }

        guard await api.skip() else { return }

        // Current 2-second segments: allow the segment containing the
        // server-side cut to commit, then discard AVPlayer's stale buffer.
        try? await Task.sleep(nanoseconds: 2_500_000_000)
        playback.jumpToLiveEdge()
        await api.refreshTranscript()
    }
}
.disabled(skipInFlight)
```

For driving, the natural follow-up is wiring `MPRemoteCommandCenter.nextTrackCommand` to the same endpoint so steering-wheel/lock-screen Next acts as Skip.

## 4. Timeline continuity

**Recommendation — Keep one FIFO writer descriptor alive and bridge every decoder handoff with cached raw PCM.**

The writer process’s stdout remains open for its entire lifetime, so killing its child decoder does not produce FIFO EOF. The cached block avoids launching another `ffmpeg` during the critical handoff; 24kHz mono s16 PCM is 48,000 bytes/sec, so the 500ms block contributes exactly 24,000 bytes with identical format.

**Code — preserve this topology in `start.sh`.**

```bash
python3 spawn.py .writer.pid \
  bash -c 'exec "$HOME/code/experiments/raven/writer.sh" > "$HOME/code/experiments/raven/pcm.fifo"'

# This PID must survive skips and preemptions.
python3 spawn.py .ffmpeg.pid \
  ffmpeg -re \
    -f s16le -ar 24000 -ac 1 -i pcm.fifo \
    -c:a aac -b:a 32k \
    -f hls \
    -hls_time 2 \
    -hls_list_size 5 \
    -hls_flags delete_segments+omit_endlist+independent_segments \
    -hls_segment_type mpegts \
    hls/stream.m3u8
```

The expected interruption sequence is:

```text
old decoder writes PCM
→ interrupt token changes
→ TERM old decoder
→ KILL after at most 200ms
→ cached idle PCM writes immediately
→ newest decoder starts when ready
```

The persistent encoder sees one continuous raw format and monotonically generates timestamps. There may be a sub-200ms wall-clock input pause, but not EOF, format change, timestamp reset, or process restart—and the existing several-second HLS/client buffer should absorb it comfortably.

**Device test required:** run 20–50 interruptions at the beginning, middle, and end of long clips while locked/backgrounded and on the car route. Verify:

```bash
hls_pid_before="$(cat "$HOME/code/experiments/raven/.ffmpeg.pid")"

curl -fsS -X POST \
  "http://100.64.0.1:8080/skip"

hls_pid_after="$(cat "$HOME/code/experiments/raven/.ffmpeg.pid")"

test "$hls_pid_before" = "$hls_pid_after"
kill -0 "$hls_pid_after"
tail -50 "$HOME/code/experiments/raven/logs/events.jsonl"
```

Also confirm no 20-second watchdog event, no HLS PID replacement, and monotonically increasing `EXT-X-MEDIA-SEQUENCE`.

## 5. Transcript coherence

**Recommendation — Keep anything that began playing, marked `preempted` or `skipped`; omit replies that never began.**

Dropping a partly-heard reply makes the visible record lie by omission. Keep the full authored text for reference, but label it clearly so the UI does not imply that the entire text reached the listener.

**Code — set initial status in `transcript_add.py`.**

```python
entry["spoken_at_epoch"] = time.time()
entry["delivery_status"] = "playing"
```

Add `~/code/experiments/raven/transcript_finish.py`:

```python
#!/usr/bin/env python3
import json
import os
import pathlib
import sys
import tempfile
import time

speech = pathlib.Path.home() / "code" / "experiments" / "raven"
spoken = speech / "spoken.jsonl"

if len(sys.argv) != 3:
    raise SystemExit(0)

utterance_id = sys.argv[1]
status = sys.argv[2]

if status not in ("completed", "preempted", "skipped"):
    raise SystemExit(0)

try:
    entries = [
        json.loads(line)
        for line in spoken.read_text().splitlines()
        if line.strip()
    ]
except (OSError, json.JSONDecodeError):
    raise SystemExit(0)

for entry in reversed(entries):
    if entry.get("id") == utterance_id:
        entry["delivery_status"] = status
        entry["ended_at_epoch"] = time.time()
        break
else:
    raise SystemExit(0)

fd, temporary = tempfile.mkstemp(prefix=".spoken.", dir=speech)
try:
    with os.fdopen(fd, "w") as handle:
        for entry in entries[-200:]:
            handle.write(json.dumps(entry, separators=(",", ":")) + "\n")
        handle.flush()
        os.fsync(handle.fileno())
    os.replace(temporary, spoken)
finally:
    try:
        os.unlink(temporary)
    except FileNotFoundError:
        pass
```

Extend `SpokenLine`:

```swift
let deliveryStatus: String?

enum CodingKeys: String, CodingKey {
    case id
    case sessionID = "session_id"
    case project
    case text
    case spokenAtEpoch = "spoken_at_epoch"
    case deliveryStatus = "delivery_status"
}
```

Then show a small `Skipped` or `Preempted` badge in `TranscriptRow`. Do not add never-started/superseded queue items to `spoken.jsonl`.

## 6. Latency floor

**Recommendation — Try 1-second segments, but keep a 6–8-second playlist window; do not shrink the list aggressively.**

Changing two-second segments to one-second segments can lower server commit delay by roughly one second and encourage a tighter live position. A smaller playlist alone does not force AVPlayer closer to live—it mainly removes recovery room and increases the chance of segment 404s after network pauses.

**Code — first HLS latency experiment.**

```bash
ffmpeg -re \
  -f s16le -ar 24000 -ac 1 -i pcm.fifo \
  -c:a aac -b:a 32k \
  -f hls \
  -hls_time 1 \
  -hls_list_size 8 \
  -hls_delete_threshold 2 \
  -hls_flags delete_segments+omit_endlist+independent_segments \
  -hls_segment_type mpegts \
  hls/stream.m3u8
```

With one-second segments, change the post-skip client delay to approximately:

```swift
try? await Task.sleep(nanoseconds: 1_500_000_000)
playback.jumpToLiveEdge()
```

Keep `preferredForwardBufferDuration = 0` and `automaticallyWaitsToMinimizeStalling = true` initially. Disabling stall protection to chase another second is a poor trade for a background driving stream.

**Device test required:** one-second segments under screen lock, CarPlay/Bluetooth route changes, Tailscale jitter, and a brief network outage. If recovery worsens, return to two-second segments; true LL-HLS is not available here.

## Recommended default and minimal first implementation

Default behavior:

```text
new selected reply
→ mark it .latest
→ interrupt current decoder
→ emit 500ms idle bridge
→ wait idling if synthesis is not ready
→ speak newest reply
→ mark old transcript “preempted”
```

Manual Skip performs the same decoder interruption but clears everything and returns Raven to idle.

The minimal meaningful first implementation is:

1. Add `.latest` and `.interrupt` during hook commit.
2. Make `synthd.py` publish only if the job remains latest.
3. Run the clip decoder in the background and monitor the interrupt token.
4. Add `POST /skip`, the SwiftUI button, and delayed live-edge seek.
5. Keep two-second HLS until mid-clip interruption passes the device soak test.
6. Only then test one-second HLS.

I would **not** ship finish-current/backlog-flush as the solution. It is safe, but with uncapped multi-minute WAVs it avoids the hard part instead of solving it.
tokens used
74,852
The right default is **(a) latest wins** for the selected channel. Kill only the per-utterance decoder, keep the persistent HLS encoder alive, immediately bridge with pre-generated idle PCM, and coalesce the queue around a `.latest` pointer.

“Finish current, then newest” is not enough here: Raven currently has a roughly **5m19s** WAV in the queue. That policy can still leave the driver trapped for minutes.

## 1. Preemption model

**Recommendation — Latest selected-channel reply wins; interrupt the current utterance immediately.**

Freshness matters more than perfect prose continuity while driving. The cut will be mildly jarring, but waiting several minutes is worse; later, you can make automatic preemption occur at Kokoro sentence/chunk boundaries while keeping manual Skip immediate.

**Code — atomically commit the new reply, mark it latest, and change the interrupt token.**

Replace the final `mv` pair in [speak-reply.sh](../hooks/speak-reply.sh) with:

```bash
# Queue commit + latest-wins signal share a lock with /skip.
if python3 - "$Q" "$stamp" "$text_tmp" "$meta_tmp" <<'PY'
import fcntl
import os
import pathlib
import sys
import tempfile
import time

queue = pathlib.Path(sys.argv[1])
stamp = sys.argv[2]
text_tmp = pathlib.Path(sys.argv[3])
meta_tmp = pathlib.Path(sys.argv[4])
lock_path = queue.parent / ".queue.lock"

def atomic_text(path, text):
    fd, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        with os.fdopen(fd, "w") as handle:
            handle.write(text)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
    finally:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass

with lock_path.open("a+") as lock:
    fcntl.flock(lock, fcntl.LOCK_EX)

    # Data first. The control token changes only after the new job exists.
    os.replace(meta_tmp, queue / f"{stamp}.caption.json")
    os.replace(text_tmp, queue / f"{stamp}.txt")
    atomic_text(queue / ".latest", stamp + "\n")
    atomic_text(
        queue / ".interrupt",
        f"new:{stamp}:{time.time_ns()}\n",
    )
PY
then
  exit 0
fi

rm -f "$text_tmp" "$meta_tmp" 2>/dev/null
exit 0
```

Also make `synthd.py` synthesize only `.latest`, and verify it is still latest before publishing:

```python
LATEST = QUEUE / ".latest"


def latest_id() -> str:
    try:
        return LATEST.read_text().strip()
    except OSError:
        return ""


def is_latest(stamp: str) -> bool:
    return bool(stamp) and latest_id() == stamp
```

In its main loop, replace the oldest-first selection:

```python
stamp = latest_id()
f = QUEUE / f"{stamp}.txt" if stamp else None
if f is None or not f.exists():
    time.sleep(0.2)
    continue
```

Guard Kokoro publication:

```python
superseded = False

if cfg["VOICE_BACKEND"] == "kokoro":
    try:
        if synth.kokoro(
            text,
            cfg["KOKORO_VOICE"],
            cfg["KOKORO_MODEL"],
            wav_part,
        ):
            if is_latest(stamp):
                os.replace(wav_part, wav)
                done = True
                backend = "kokoro"
            else:
                superseded = True
                wav_part.unlink(missing_ok=True)
    except Exception as e:
        ravenlog.log("synthd", "kokoro_fail", id=stamp, err=str(e)[:200])

if not done and not superseded:
    try:
        synth.say(text, cfg["SAY_VOICE"], wav)
        published_aiff = wav.with_suffix(".aiff")
        if is_latest(stamp):
            done = True
            backend = "say"
        else:
            superseded = True
            published_aiff.unlink(missing_ok=True)
    except Exception as e:
        ravenlog.log("synthd", "say_fail", id=stamp, err=str(e)[:200])
```

This prevents an old synthesis already underway from “resurrecting” after a newer reply or manual skip.

## 2. Server-side interrupt mechanism

**Recommendation — Kill the clip decoder, never the persistent HLS encoder; bridge immediately with cached PCM.**

This is viable because the parent writer still owns the FIFO’s write descriptor, so killing the child does not send EOF downstream. Poll a generation token every 50ms, terminate the child within 200ms, and immediately write a 500ms idle block.

**Code — core replacement for [writer.sh](../writer.sh).**

```bash
#!/bin/bash
set -uo pipefail

cd "$HOME/code/experiments/raven" || exit 1
[ -f config.sh ] && . ./config.sh

IDLE_FLOOR="${IDLE_FLOOR:-noise}"
HB="hls/.heartbeat"
LATEST="queue/.latest"
INTERRUPT="queue/.interrupt"
IDLE_PCM="hls/.idle-500ms.pcm"
PREROLL_PCM="hls/.preroll-350ms.pcm"
clip_pid=""

make_pcm_blocks() {
  local idle_source

  if [ "$IDLE_FLOOR" = "silence" ]; then
    idle_source="anullsrc=r=24000:cl=mono:d=0.5"
  else
    idle_source="anoisesrc=r=24000:c=pink:a=0.002:d=0.5"
  fi

  if [ ! -s "$IDLE_PCM" ]; then
    ffmpeg -y -loglevel quiet -f lavfi -i "$idle_source" \
      -f s16le -ar 24000 -ac 1 -acodec pcm_s16le "$IDLE_PCM"
  fi

  if [ ! -s "$PREROLL_PCM" ]; then
    ffmpeg -y -loglevel quiet \
      -f lavfi -i "anoisesrc=r=24000:c=pink:a=0.002:d=0.35" \
      -f s16le -ar 24000 -ac 1 -acodec pcm_s16le "$PREROLL_PCM"
  fi
}

latest_id() {
  [ -f "$LATEST" ] && tr -d '\r\n' < "$LATEST"
}

interrupt_token() {
  if [ -f "$INTERRUPT" ]; then
    cat "$INTERRUPT"
  else
    printf 'boot'
  fi
}

ready_path() {
  local id="$1"

  if [ -f "queue/$id.wav" ]; then
    printf 'queue/%s.wav' "$id"
  elif [ -f "queue/$id.aiff" ]; then
    printf 'queue/%s.aiff' "$id"
  fi
}

listener_is_live() {
  [ -f "$HB" ] || return 1
  local age
  age=$(( $(date +%s) - $(stat -f %m "$HB") ))
  [ "$age" -le 10 ]
}

stop_clip() {
  local pid="$1"

  kill -TERM "$pid" 2>/dev/null || true

  # Never let decoder shutdown hold the PCM handoff for long.
  for _ in 1 2 3 4; do
    kill -0 "$pid" 2>/dev/null || break
    sleep 0.05
  done

  if kill -0 "$pid" 2>/dev/null; then
    kill -KILL "$pid" 2>/dev/null || true
  fi

  wait "$pid" 2>/dev/null || true
}

cleanup_child() {
  if [ -n "$clip_pid" ] && kill -0 "$clip_pid" 2>/dev/null; then
    stop_clip "$clip_pid"
  fi
}

trap cleanup_child EXIT INT TERM
make_pcm_blocks

while true; do
  # Existing stale-item cleanup remains useful for abandoned artifacts.
  find queue \( \
      -name '*.txt' -o \
      -name '*.aiff' -o \
      -name '*.wav' -o \
      -name '*.caption.json' -o \
      -name '*.part' \
    \) -mmin +10 -delete 2>/dev/null

  id="$(latest_id)"
  f="$(ready_path "$id")"

  if ! listener_is_live || [ -z "$id" ] || [ -z "$f" ]; then
    cat "$IDLE_PCM"
    continue
  fi

  token_before="$(interrupt_token)"

  # Avoid starting a stale selection if a hook raced ready_path().
  if [ "$id" != "$(latest_id)" ] ||
     [ "$token_before" != "$(interrupt_token)" ]; then
    continue
  fi

  metadata="queue/$id.caption.json"
  cat "$PREROLL_PCM"

  python3 transcript_add.py "$metadata" 2>/dev/null
  python3 ravenlog.py writer emit id="$id" 2>/dev/null

  ffmpeg -nostdin -loglevel quiet -i "$f" \
    -f s16le -ar 24000 -ac 1 -acodec pcm_s16le - &
  clip_pid=$!

  outcome="completed"

  while kill -0 "$clip_pid" 2>/dev/null; do
    token_now="$(interrupt_token)"

    if [ "$token_now" != "$token_before" ]; then
      case "$token_now" in
        skip:*) outcome="skipped" ;;
        *)      outcome="preempted" ;;
      esac

      stop_clip "$clip_pid"
      clip_pid=""

      # A cached raw block starts writing with essentially no process startup.
      # It covers the handoff while the newest WAV becomes ready.
      cat "$IDLE_PCM"
      break
    fi

    sleep 0.05
  done

  if [ -n "$clip_pid" ]; then
    wait "$clip_pid" 2>/dev/null || true
    clip_pid=""
  fi

  python3 transcript_finish.py "$id" "$outcome" 2>/dev/null
  python3 ravenlog.py writer "$outcome" id="$id" 2>/dev/null

  rm -f "$f" "$metadata"
done
```

Do not kill `.ffmpeg.pid`, recreate `pcm.fifo`, restart HLS, or insert an HLS discontinuity. Only the per-clip decoder is disposable.

Automatic sentence-boundary interruption is a later refinement: have Kokoro publish 8–15-second sentence chunks, checking `.latest` between chunks. It will sound nicer, but I would first prove the simpler hard cut is stable.

## 3. Manual Skip

**Recommendation — Add `POST /skip`; client-only live-edge seeking cannot stop Mac-side production.**

A client seek only discards buffered HLS audio. If the writer is still emitting the old five-minute WAV, the new live edge is still that same WAV; however, after server-side interruption, a delayed live-edge seek is useful for throwing away the iPhone’s remaining 4–8 seconds of old buffer.

**Code — server endpoint.**

Add to [server.py](../server.py):

```python
QUEUE = SPEECH / "queue"
QUEUE_LOCK = SPEECH / ".queue.lock"
LATEST = QUEUE / ".latest"
INTERRUPT = QUEUE / ".interrupt"


@contextlib.contextmanager
def queue_lock():
    with QUEUE_LOCK.open("a+") as handle:
        fcntl.flock(handle, fcntl.LOCK_EX)
        try:
            yield
        finally:
            fcntl.flock(handle, fcntl.LOCK_UN)


def atomic_text(path, value):
    fd, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        with os.fdopen(fd, "w") as handle:
            handle.write(value)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
    finally:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass
```

Add this method to `HuginnHandler`:

```python
def handle_skip(self):
    token = f"skip:{time.time_ns()}"

    with queue_lock():
        # Stop selecting anything before removing queue artifacts.
        atomic_text(LATEST, "")

        for pattern in (
            "*.txt",
            "*.wav",
            "*.aiff",
            "*.caption.json",
            "*.part",
        ):
            for path in QUEUE.glob(pattern):
                try:
                    path.unlink()
                except FileNotFoundError:
                    pass

        # Written last: the writer sees the flush as one committed command.
        atomic_text(INTERRUPT, token + "\n")

    ravenlog.log("server", "skip", token=token)
    self.json_response(
        {"accepted": True, "token": token},
        conditional=False,
    )
```

Route it before `/active` body validation:

```python
def do_POST(self):
    path = urllib.parse.urlsplit(self.path).path

    if path == "/skip":
        self.handle_skip()
        return

    if path == "/log":
        self.handle_log_upload()
        return

    if path != "/active":
        self.send_error(404)
        return

    # Existing /active implementation follows.
```

Add to [HuginnAPI.swift](~/code/experiments/ear/Ear/HuginnAPI.swift):

```swift
func skip() async -> Bool {
    var request = URLRequest(url: baseURL.appendingPathComponent("skip"))
    request.httpMethod = "POST"
    request.timeoutInterval = 4

    do {
        let (_, response) = try await session.data(for: request)
        guard let http = response as? HTTPURLResponse,
              http.statusCode == 200 else {
            throw URLError(.badServerResponse)
        }
        errorText = nil
        return true
    } catch {
        errorText = "Could not skip"
        return false
    }
}
```

Expose the existing live-edge operation in [PlaybackController.swift](~/code/experiments/ear/Ear/PlaybackController.swift):

```swift
func jumpToLiveEdge() {
    runOnMain { [weak self] in
        self?.playAtLiveEdge()
    }
}
```

Add state and a button in [EarApp.swift](~/code/experiments/ear/Ear/EarApp.swift):

```swift
@State private var skipInFlight = false
```

Inside the transport `HStack`, before Mute:

```swift
TransportButton(
    symbol: "forward.end.fill",
    label: "Skip",
    selected: skipInFlight
) {
    guard !skipInFlight else { return }

    Task {
        skipInFlight = true
        defer { skipInFlight = false }

        guard await api.skip() else { return }

        // Current 2-second segments: allow the segment containing the
        // server-side cut to commit, then discard AVPlayer's stale buffer.
        try? await Task.sleep(nanoseconds: 2_500_000_000)
        playback.jumpToLiveEdge()
        await api.refreshTranscript()
    }
}
.disabled(skipInFlight)
```

For driving, the natural follow-up is wiring `MPRemoteCommandCenter.nextTrackCommand` to the same endpoint so steering-wheel/lock-screen Next acts as Skip.

## 4. Timeline continuity

**Recommendation — Keep one FIFO writer descriptor alive and bridge every decoder handoff with cached raw PCM.**

The writer process’s stdout remains open for its entire lifetime, so killing its child decoder does not produce FIFO EOF. The cached block avoids launching another `ffmpeg` during the critical handoff; 24kHz mono s16 PCM is 48,000 bytes/sec, so the 500ms block contributes exactly 24,000 bytes with identical format.

**Code — preserve this topology in `start.sh`.**

```bash
python3 spawn.py .writer.pid \
  bash -c 'exec "$HOME/code/experiments/raven/writer.sh" > "$HOME/code/experiments/raven/pcm.fifo"'

# This PID must survive skips and preemptions.
python3 spawn.py .ffmpeg.pid \
  ffmpeg -re \
    -f s16le -ar 24000 -ac 1 -i pcm.fifo \
    -c:a aac -b:a 32k \
    -f hls \
    -hls_time 2 \
    -hls_list_size 5 \
    -hls_flags delete_segments+omit_endlist+independent_segments \
    -hls_segment_type mpegts \
    hls/stream.m3u8
```

The expected interruption sequence is:

```text
old decoder writes PCM
→ interrupt token changes
→ TERM old decoder
→ KILL after at most 200ms
→ cached idle PCM writes immediately
→ newest decoder starts when ready
```

The persistent encoder sees one continuous raw format and monotonically generates timestamps. There may be a sub-200ms wall-clock input pause, but not EOF, format change, timestamp reset, or process restart—and the existing several-second HLS/client buffer should absorb it comfortably.

**Device test required:** run 20–50 interruptions at the beginning, middle, and end of long clips while locked/backgrounded and on the car route. Verify:

```bash
hls_pid_before="$(cat "$HOME/code/experiments/raven/.ffmpeg.pid")"

curl -fsS -X POST \
  "http://100.64.0.1:8080/skip"

hls_pid_after="$(cat "$HOME/code/experiments/raven/.ffmpeg.pid")"

test "$hls_pid_before" = "$hls_pid_after"
kill -0 "$hls_pid_after"
tail -50 "$HOME/code/experiments/raven/logs/events.jsonl"
```

Also confirm no 20-second watchdog event, no HLS PID replacement, and monotonically increasing `EXT-X-MEDIA-SEQUENCE`.

## 5. Transcript coherence

**Recommendation — Keep anything that began playing, marked `preempted` or `skipped`; omit replies that never began.**

Dropping a partly-heard reply makes the visible record lie by omission. Keep the full authored text for reference, but label it clearly so the UI does not imply that the entire text reached the listener.

**Code — set initial status in `transcript_add.py`.**

```python
entry["spoken_at_epoch"] = time.time()
entry["delivery_status"] = "playing"
```

Add `~/code/experiments/raven/transcript_finish.py`:

```python
#!/usr/bin/env python3
import json
import os
import pathlib
import sys
import tempfile
import time

speech = pathlib.Path.home() / "code" / "experiments" / "raven"
spoken = speech / "spoken.jsonl"

if len(sys.argv) != 3:
    raise SystemExit(0)

utterance_id = sys.argv[1]
status = sys.argv[2]

if status not in ("completed", "preempted", "skipped"):
    raise SystemExit(0)

try:
    entries = [
        json.loads(line)
        for line in spoken.read_text().splitlines()
        if line.strip()
    ]
except (OSError, json.JSONDecodeError):
    raise SystemExit(0)

for entry in reversed(entries):
    if entry.get("id") == utterance_id:
        entry["delivery_status"] = status
        entry["ended_at_epoch"] = time.time()
        break
else:
    raise SystemExit(0)

fd, temporary = tempfile.mkstemp(prefix=".spoken.", dir=speech)
try:
    with os.fdopen(fd, "w") as handle:
        for entry in entries[-200:]:
            handle.write(json.dumps(entry, separators=(",", ":")) + "\n")
        handle.flush()
        os.fsync(handle.fileno())
    os.replace(temporary, spoken)
finally:
    try:
        os.unlink(temporary)
    except FileNotFoundError:
        pass
```

Extend `SpokenLine`:

```swift
let deliveryStatus: String?

enum CodingKeys: String, CodingKey {
    case id
    case sessionID = "session_id"
    case project
    case text
    case spokenAtEpoch = "spoken_at_epoch"
    case deliveryStatus = "delivery_status"
}
```

Then show a small `Skipped` or `Preempted` badge in `TranscriptRow`. Do not add never-started/superseded queue items to `spoken.jsonl`.

## 6. Latency floor

**Recommendation — Try 1-second segments, but keep a 6–8-second playlist window; do not shrink the list aggressively.**

Changing two-second segments to one-second segments can lower server commit delay by roughly one second and encourage a tighter live position. A smaller playlist alone does not force AVPlayer closer to live—it mainly removes recovery room and increases the chance of segment 404s after network pauses.

**Code — first HLS latency experiment.**

```bash
ffmpeg -re \
  -f s16le -ar 24000 -ac 1 -i pcm.fifo \
  -c:a aac -b:a 32k \
  -f hls \
  -hls_time 1 \
  -hls_list_size 8 \
  -hls_delete_threshold 2 \
  -hls_flags delete_segments+omit_endlist+independent_segments \
  -hls_segment_type mpegts \
  hls/stream.m3u8
```

With one-second segments, change the post-skip client delay to approximately:

```swift
try? await Task.sleep(nanoseconds: 1_500_000_000)
playback.jumpToLiveEdge()
```

Keep `preferredForwardBufferDuration = 0` and `automaticallyWaitsToMinimizeStalling = true` initially. Disabling stall protection to chase another second is a poor trade for a background driving stream.

**Device test required:** one-second segments under screen lock, CarPlay/Bluetooth route changes, Tailscale jitter, and a brief network outage. If recovery worsens, return to two-second segments; true LL-HLS is not available here.

## Recommended default and minimal first implementation

Default behavior:

```text
new selected reply
→ mark it .latest
→ interrupt current decoder
→ emit 500ms idle bridge
→ wait idling if synthesis is not ready
→ speak newest reply
→ mark old transcript “preempted”
```

Manual Skip performs the same decoder interruption but clears everything and returns Raven to idle.

The minimal meaningful first implementation is:

1. Add `.latest` and `.interrupt` during hook commit.
2. Make `synthd.py` publish only if the job remains latest.
3. Run the clip decoder in the background and monitor the interrupt token.
4. Add `POST /skip`, the SwiftUI button, and delayed live-edge seek.
5. Keep two-second HLS until mid-clip interruption passes the device soak test.
6. Only then test one-second HLS.

I would **not** ship finish-current/backlog-flush as the solution. It is safe, but with uncapped multi-minute WAVs it avoids the hard part instead of solving it.
```

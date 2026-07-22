#!/bin/bash
# End-to-end PCM continuity/parity check. This uses an isolated RAVEN_HOME and
# a temporary copy of writer.sh; it never touches or swaps the live runtime home
# writer, queue, FIFO, transcript, or logs.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")" && pwd)
SOURCE_WRITER=${RAVEN_WRITER_SH:-"$HOME/code/experiments/raven/writer.sh"}

for command in go ffmpeg python3 sed mkfifo; do
  command -v "$command" >/dev/null || {
    echo "missing required command: $command" >&2
    exit 1
  }
done
if [ ! -f "$SOURCE_WRITER" ]; then
  echo "writer.sh not found: $SOURCE_WRITER" >&2
  exit 1
fi

TMP=$(mktemp -d "${TMPDIR:-/tmp}/raven-write-integration.XXXXXX")
WRITER_PID=
cleanup() {
  if [ -n "$WRITER_PID" ]; then
    kill "$WRITER_PID" 2>/dev/null || true
    wait "$WRITER_PID" 2>/dev/null || true
  fi
  rm -rf "$TMP"
}
trap cleanup EXIT INT TERM

(cd "$ROOT" && GOCACHE="$TMP/gocache" go build -o "$TMP/raven" .)
ffmpeg -nostdin -loglevel quiet \
  -f lavfi -i "sine=frequency=1000:sample_rate=24000:duration=0.5" \
  -ar 24000 -ac 1 -c:a pcm_s16le "$TMP/clip.wav"

prepare_home() {
  home=$1
  floor=$2
  mkdir -p "$home/queue" "$home/hls"
  printf 'IDLE_FLOOR=%s\n' "$floor" > "$home/config.sh"
  cp "$TMP/clip.wav" "$home/queue/100.wav"
  cp "$TMP/clip.wav" "$home/queue/200.wav"
  printf '%s\n' '{"id":"100","session_id":"test","project":"integration","text":"first tone"}' \
    > "$home/queue/100.caption.json"
  printf '%s\n' '{"id":"200","session_id":"test","project":"integration","text":"second tone"}' \
    > "$home/queue/200.caption.json"
  touch "$home/hls/.heartbeat"
}

# Capture exactly three seconds at 24,000 mono s16le: 24,000 * 2 * 3 bytes.
# The fixed-size reader also applies FIFO backpressure, like the real HLS side.
capture() {
  implementation=$1
  home=$2
  output=$3
  fifo="$home/capture.fifo"
  mkfifo "$fifo"
  if [ "$implementation" = go ]; then
    RAVEN_HOME="$home" "$TMP/raven" write > "$fifo" &
  else
    RAVEN_HOME="$home" "$TMP/writer.sh" > "$fifo" &
  fi
  WRITER_PID=$!
  python3 - "$fifo" "$output" <<'PY'
import pathlib
import sys

remaining = 24_000 * 2 * 3
with open(sys.argv[1], "rb", buffering=0) as source, open(sys.argv[2], "wb") as output:
    while remaining:
        chunk = source.read(min(65_536, remaining))
        if not chunk:
            raise SystemExit(f"writer stopped with {remaining} PCM bytes still expected")
        output.write(chunk)
        remaining -= len(chunk)
PY
  kill "$WRITER_PID" 2>/dev/null || true
  wait "$WRITER_PID" 2>/dev/null || true
  WRITER_PID=
}

GO_HOME="$TMP/go-home"
BASH_HOME="$TMP/bash-home"
GO_NOISE_HOME="$TMP/go-noise-home"
BASH_NOISE_HOME="$TMP/bash-noise-home"
prepare_home "$GO_HOME" silence
prepare_home "$BASH_HOME" silence
prepare_home "$GO_NOISE_HOME" noise
prepare_home "$BASH_NOISE_HOME" noise

# Change only writer.sh's initial cwd in the temporary copy. Its loop, ffmpeg
# arguments, gating, ordering, and cleanup logic remain the source version.
# writer.sh self-locates its home from BASH_SOURCE; force the temp copy to the
# isolated test home instead. Anchors on the `cd … || exit 1` shape so it stays
# correct if writer.sh's cd form changes again.
sed 's#^cd .* || exit 1$#cd "$RAVEN_HOME" || exit 1#' \
  "$SOURCE_WRITER" > "$TMP/writer.sh"
chmod +x "$TMP/writer.sh"

capture go "$GO_HOME" "$TMP/go.pcm"
capture bash "$BASH_HOME" "$TMP/bash.pcm"
capture go "$GO_NOISE_HOME" "$TMP/go-noise.pcm"
capture bash "$BASH_NOISE_HOME" "$TMP/bash-noise.pcm"

# Both raw streams must also be acceptable to ffmpeg under the promised format.
for pcm in "$TMP/go.pcm" "$TMP/bash.pcm" "$TMP/go-noise.pcm" "$TMP/bash-noise.pcm"; do
  ffmpeg -nostdin -loglevel error -f s16le -ar 24000 -ac 1 -i "$pcm" \
    -f null -
done

python3 - "$TMP/go.pcm" "$TMP/bash.pcm" "$TMP/go-noise.pcm" "$TMP/bash-noise.pcm" <<'PY'
import array
import math
import pathlib
import sys

RATE = 24_000
EXPECTED_BYTES = RATE * 2 * 3
WINDOWS = {
    "pink_preroll": (0.05, 0.30),
    "speech_1": (0.42, 0.78),
    "pink_between": (0.90, 1.10),
    "speech_2": (1.27, 1.63),
    "idle_floor": (1.85, 2.50),
}

def analyze(path_string, floor):
    path = pathlib.Path(path_string)
    raw = path.read_bytes()
    assert len(raw) == EXPECTED_BYTES, (
        f"{path.name}: got {len(raw)} bytes, expected {EXPECTED_BYTES} "
        "(three continuous seconds of 24kHz mono s16le)"
    )
    assert len(raw) % 2 == 0, f"{path.name}: partial s16le sample"
    samples = array.array("h")
    samples.frombytes(raw)
    if sys.byteorder != "little":
        samples.byteswap()

    metrics = {}
    for name, (start, end) in WINDOWS.items():
        values = samples[int(start * RATE):int(end * RATE)]
        metrics[name] = math.sqrt(sum(v * v for v in values) / len(values))

    assert metrics["speech_1"] > 500, f"{path.name}: first clip not injected: {metrics}"
    assert metrics["speech_2"] > 500, f"{path.name}: second clip not injected: {metrics}"
    assert metrics["pink_preroll"] < metrics["speech_1"] * 0.15, (
        f"{path.name}: pre-roll is not a low floor: {metrics}"
    )
    assert metrics["pink_between"] < metrics["speech_1"] * 0.15, (
        f"{path.name}: low floor between clips is missing: {metrics}"
    )
    if floor == "silence":
        assert metrics["idle_floor"] < 1.0, f"{path.name}: silence floor is not silent: {metrics}"
    else:
        assert 1.0 < metrics["idle_floor"] < metrics["speech_1"] * 0.15, (
            f"{path.name}: noise idle is not a continuous low floor: {metrics}"
        )
    return metrics

go = analyze(sys.argv[1], "silence")
bash = analyze(sys.argv[2], "silence")
go_noise = analyze(sys.argv[3], "noise")
bash_noise = analyze(sys.argv[4], "noise")
for go_metrics, bash_metrics in ((go, bash), (go_noise, bash_noise)):
    for name in ("speech_1", "speech_2"):
        scale = max(go_metrics[name], bash_metrics[name], 1.0)
        assert abs(go_metrics[name] - bash_metrics[name]) / scale < 0.15, (
            f"Go/bash RMS diverged in {name}: "
            f"go={go_metrics[name]:.2f}, bash={bash_metrics[name]:.2f}"
        )

print("Go silence RMS:   " + ", ".join(f"{k}={v:.2f}" for k, v in go.items()))
print("Bash silence RMS: " + ", ".join(f"{k}={v:.2f}" for k, v in bash.items()))
print("Go noise RMS:     " + ", ".join(f"{k}={v:.2f}" for k, v in go_noise.items()))
print("Bash noise RMS:   " + ", ".join(f"{k}={v:.2f}" for k, v in bash_noise.items()))
print("PASS: 3.000s continuous PCM, two speech injections, low inter-clip floor, both idle modes, bash parity")
PY

test "$(wc -l < "$GO_HOME/spoken.jsonl" | tr -d ' ')" = 2
test "$(grep -c '"comp":"writer","event":"emit"' "$GO_HOME/logs/events.jsonl")" = 2
test ! -e "$GO_HOME/queue/100.wav"
test ! -e "$GO_HOME/queue/200.wav"
test ! -e "$GO_HOME/queue/100.caption.json"
test ! -e "$GO_HOME/queue/200.caption.json"
echo "PASS: Go writer recorded two Claude transcript lines/events and consumed clips+captions"

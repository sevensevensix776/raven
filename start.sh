#!/bin/bash
# Brings up the stream, fully detached from the launching shell.
#
# Learned the hard way: plain `&` leaves children in the caller's process
# group, so a SIGKILL to the caller takes the stream with it. And children
# inheriting the caller's stdout make the caller hang forever waiting on a
# pipe that never closes. spawn.py fixes both: os.setsid() + redirected fds.
# Relocatable: home is wherever this script lives. Exported so the Go binary
# (raven serve/write) and the Python daemon resolve the same runtime dir.
RAVEN_HOME="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export RAVEN_HOME
cd "$RAVEN_HOME" || exit 1

./stop.sh >/dev/null 2>&1
sleep 1

mkdir -p hls queue
[ -p pcm.fifo ] || mkfifo pcm.fifo
rm -f hls/*.ts hls/*.m3u8 hls/.heartbeat

# raven write's stdout must BE the fifo, hence the bash -c wrapper. spawn.py
# sets cwd to the home dir, so the relative pcm.fifo resolves there.
python3 spawn.py .writer.pid bash -c 'exec $HOME/.local/bin/raven write > pcm.fifo'

# -re is mandatory. Without it ffmpeg drains the FIFO at ~8x real time and
# destroys the live timeline (measured: MEDIA-SEQUENCE hit 80 in 20s).
# hls_time=1 (was 2): the phone player buffers whole segments before playing, so
# 1s segments roughly halve the live-edge latency vs 2s. list_size=8 keeps an ~8s
# playlist window so a brief dead zone can still reconnect without losing audio.
python3 spawn.py .ffmpeg.pid ffmpeg -re -f s16le -ar 24000 -ac 1 -i pcm.fifo \
  -c:a aac -b:a 32k \
  -f hls -hls_time 1 -hls_list_size 8 \
  -hls_flags delete_segments+omit_endlist+independent_segments \
  -hls_segment_type mpegts \
  hls/stream.m3u8

python3 spawn.py .server.pid "$HOME/.local/bin/raven" serve

# synthd: warm Kokoro synthesis daemon (keeps the model loaded → ~0.1s/reply).
# Uses the venv python that has mlx-audio + misaki installed.
python3 spawn.py .synthd.pid "$RAVEN_HOME/.venv/bin/python" synthd.py

# tail: live-narration transcript tailer — speaks completed assistant text blocks
# mid-turn (before the Stop hook). Gated by LIVE_NARRATION in config.sh; when off
# it only shadow-logs and never touches the queue.
python3 spawn.py .tail.pid "$HOME/.local/bin/raven" tail

sleep 4

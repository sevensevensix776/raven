#!/bin/bash
# Brings up the stream, fully detached from the launching shell.
#
# Learned the hard way: plain `&` leaves children in the caller's process
# group, so a SIGKILL to the caller takes the stream with it. And children
# inheriting the caller's stdout make the caller hang forever waiting on a
# pipe that never closes. spawn.py fixes both: os.setsid() + redirected fds.
cd ~/speech || exit 1

./stop.sh >/dev/null 2>&1
sleep 1

mkdir -p hls queue
[ -p pcm.fifo ] || mkfifo pcm.fifo
rm -f hls/*.ts hls/*.m3u8 hls/.heartbeat

# writer.sh's stdout must BE the fifo, hence the bash -c wrapper.
python3 spawn.py .writer.pid bash -c 'exec ~/speech/writer.sh > ~/speech/pcm.fifo'

# -re is mandatory. Without it ffmpeg drains the FIFO at ~8x real time and
# destroys the live timeline (measured: MEDIA-SEQUENCE hit 80 in 20s).
python3 spawn.py .ffmpeg.pid ffmpeg -re -f s16le -ar 24000 -ac 1 -i pcm.fifo \
  -c:a aac -b:a 32k \
  -f hls -hls_time 2 -hls_list_size 5 \
  -hls_flags delete_segments+omit_endlist+independent_segments \
  -hls_segment_type mpegts \
  hls/stream.m3u8

python3 spawn.py .server.pid python3 server.py

sleep 4

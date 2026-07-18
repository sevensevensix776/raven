#!/bin/bash
cd ~/speech || exit 1
for p in .writer.pid .ffmpeg.pid .server.pid; do
  [ -f "$p" ] && kill "$(cat "$p")" 2>/dev/null
  rm -f "$p"
done
# writer.sh spawns ffmpeg children in a loop; sweep them.
pkill -f 'anoisesrc=r=24000' 2>/dev/null
pkill -f 'speech/pcm.fifo' 2>/dev/null
pkill -f 'speech/server.py' 2>/dev/null
echo "stopped"

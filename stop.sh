#!/bin/bash
RAVEN_HOME="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$RAVEN_HOME" || exit 1
for p in .writer.pid .ffmpeg.pid .server.pid .synthd.pid .tail.pid; do
  [ -f "$p" ] && kill "$(cat "$p")" 2>/dev/null
  rm -f "$p"
done
# Sweep any orphans the pidfiles missed (process-name patterns, home-independent).
pkill -f 'anoisesrc=r=24000' 2>/dev/null   # writer's noise-floor ffmpeg children
pkill -f 'raven write' 2>/dev/null
pkill -f 'raven serve' 2>/dev/null
pkill -f 'raven tail' 2>/dev/null
pkill -f 'synthd.py' 2>/dev/null
echo "stopped"

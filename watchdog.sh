#!/bin/bash
# Keeps Raven alive across reboots, crashes, and logouts.
#
# Why this exists: on 2026-07-24 the Mac rebooted and Raven never came back.
# Nothing noticed for a day. Worse, the Claude Code hook is spawned per-event
# rather than being a daemon, so it happily kept queueing speech the whole time —
# 350 clips accumulated with no writer to drain them. From the driver's side a
# dead pipeline sounds exactly like Claude having nothing to say.
#
# Design: a periodic health check rather than a supervised foreground process.
# start.sh deliberately daemonizes and exits, so launchd's KeepAlive would read
# that exit as a crash and restart it forever. Instead launchd runs this script
# on an interval; it exits immediately when healthy, and calls start.sh when not.
# Idempotent, cheap, and it survives any way the pipeline can die.
#
# Install: ./install-watchdog.sh      Run by hand: ./watchdog.sh [--verbose]

RAVEN_HOME="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export RAVEN_HOME
cd "$RAVEN_HOME" || exit 1

VERBOSE=0
[ "$1" = "--verbose" ] && VERBOSE=1

log() {
  # One JSON object per line into the same events.jsonl everything else uses, so
  # `raven diagnose` and any log tooling see restarts alongside speech events.
  printf '{"ts":%s,"comp":"watchdog","event":"%s","detail":"%s"}\n' \
    "$(date +%s.%N)" "$1" "$2" >> "$RAVEN_HOME/logs/events.jsonl" 2>/dev/null
  [ "$VERBOSE" = "1" ] && echo "[watchdog] $1 $2"
}

# A process is alive if its pidfile names a live pid. Signal 0 checks existence
# and permission without delivering anything.
alive() {
  local pidfile="$RAVEN_HOME/$1"
  [ -f "$pidfile" ] || return 1
  local pid
  pid="$(tr -d '[:space:]' < "$pidfile")"
  [ -n "$pid" ] || return 1
  kill -0 "$pid" 2>/dev/null
}

dead=""
for p in .writer.pid .ffmpeg.pid .server.pid .synthd.pid .tail.pid; do
  alive "$p" || dead="$dead ${p#.}"
done

if [ -z "$dead" ]; then
  [ "$VERBOSE" = "1" ] && echo "[watchdog] healthy"
  exit 0
fi

# Partial death is still death: the processes form one pipeline (FIFO -> encoder
# -> server), so restart all of them rather than trying to revive one.
log "restarting" "dead:${dead# }"
mkdir -p "$RAVEN_HOME/logs"
./start.sh >> "$RAVEN_HOME/.detached.log" 2>&1

sleep 5
still_dead=""
for p in .writer.pid .ffmpeg.pid .server.pid .synthd.pid .tail.pid; do
  alive "$p" || still_dead="$still_dead ${p#.}"
done

if [ -n "$still_dead" ]; then
  log "restart_failed" "still dead:${still_dead# }"
  exit 1
fi
log "restarted" "ok"
exit 0

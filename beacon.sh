#!/bin/bash
# Speaks the time every 30s, forever.
#
# The point: a dead stream is indistinguishable from "Claude hasn't replied."
# A beacon makes silence diagnostic. Lock the phone, pocket it, and you'll
# know not just whether it died but exactly WHEN — the last time you hear is
# the moment iOS took the session.
#
#   ~/speech/beacon.sh          -> every 30s
#   ~/speech/beacon.sh 60       -> every 60s
INTERVAL="${1:-30}"
n=0
while true; do
  n=$((n+1))
  "$HOME/speech/say.sh" "Mark $n. $(date +'%-I %M and %S seconds')." >/dev/null
  echo "[$(date +%H:%M:%S)] mark $n spoken"
  python3 -c "import time;time.sleep($INTERVAL)"
done

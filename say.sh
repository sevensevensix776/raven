#!/bin/bash
# Inject a spoken line into the live stream.
#   ~/speech/say.sh "background playback is alive"
#
# mktemp + mv is load-bearing: writing straight into queue/ races writer.sh,
# which would grab a half-written file. mv within a filesystem is atomic.
Q="$HOME/speech/queue"
mkdir -p "$Q"
text="$*"
[ -z "${text// }" ] && { echo "usage: say.sh <text>"; exit 1; }

tmp=$(mktemp)
say -o "$tmp.aiff" "$text" 2>/dev/null || { echo "say failed"; exit 1; }
mv "$tmp.aiff" "$Q/$(date +%s%N).aiff"
rm -f "$tmp"
echo "queued: $text"

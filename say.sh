#!/bin/bash
# Inject a spoken line into the live stream.
#   ~/code/experiments/raven/say.sh "background playback is alive"
#
# mktemp + mv is load-bearing: writing straight into queue/ races the writer,
# which would grab a half-written file. mv within a filesystem is atomic.
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
Q="$HERE/queue"
mkdir -p "$Q"
text="$*"
[ -z "${text// }" ] && { echo "usage: say.sh <text>"; exit 1; }

tmp=$(mktemp)
say -o "$tmp.aiff" "$text" 2>/dev/null || { echo "say failed"; exit 1; }
mv "$tmp.aiff" "$Q/$(date +%s%N).aiff"
rm -f "$tmp"
echo "queued: $text"

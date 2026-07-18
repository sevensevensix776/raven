#!/bin/bash
# Speaks Claude's replies into the live HLS stream (~/speech) for drive-time listening.
#
# Registered on TWO events, and behaves differently for each:
#
#   UserPromptSubmit -> records this session as the one you're talking to.
#   Stop             -> speaks last_assistant_message, but ONLY if this session
#                       is the one you last talked to.
#
# That's the channel selection: it follows you. Talk to a session from the phone
# via Remote Control and narration switches to it, with nothing to tap in the car.
#
# HARD CONSTRAINT: hooks run with timeout=2. This must never do slow work.
# It writes text and exits; ~/speech/writer.sh does the TTS and encoding.
#
# Bypass: `touch ~/speech/speak-all` to hear every session regardless of focus.

set -uo pipefail

SPEECH="$HOME/speech"
Q="$SPEECH/queue"
ACTIVE="$SPEECH/active"

# If the stream isn't running, do nothing. Never block a turn.
[ -d "$SPEECH" ] || exit 0
mkdir -p "$Q" 2>/dev/null

payload=$(cat)

read -r event session cwd < <(
  printf '%s' "$payload" | python3 -c '
import sys, json
try:
    d = json.load(sys.stdin)
except Exception:
    print("- - -"); sys.exit(0)
print(
    d.get("hook_event_name") or "-",
    d.get("session_id") or "-",
    d.get("cwd") or "-",
)
' 2>/dev/null
)

case "$event" in
  UserPromptSubmit)
    printf '%s' "$session" > "$ACTIVE" 2>/dev/null
    exit 0
    ;;
  Stop) ;;
  *) exit 0 ;;
esac

# Channel gate: only the session you last spoke to gets the stream.
if [ ! -f "$SPEECH/speak-all" ] && [ -f "$ACTIVE" ]; then
  [ "$session" = "$(cat "$ACTIVE" 2>/dev/null)" ] || exit 0
fi

text=$(printf '%s' "$payload" | python3 -c '
import sys, json
try:
    print(json.load(sys.stdin).get("last_assistant_message") or "")
except Exception:
    pass
' 2>/dev/null)

[ -z "${text// }" ] && exit 0

# Strip fenced code, markdown punctuation, and bare paths — unspeakable aloud.
clean=$(printf '%s' "$text" \
  | sed -e '/^[[:space:]]*```/,/^[[:space:]]*```/d' \
        -e 's/`[^`]*`/ /g' \
        -e 's/[*_#>|]//g' \
        -e 's|/[A-Za-z0-9._/-]\{12,\}| that path |g' \
  | tr -s ' \n' ' ' \
  | head -c 700)

[ -z "${clean// }" ] && exit 0

project=$(basename "$cwd" 2>/dev/null)
[ "$project" = "-" ] || [ -z "$project" ] && project=""

# Drop TEXT, not audio. Synthesis happens in writer.sh.
# Calling `say` here cost 1.4s against a 2s hook budget — a long reply would
# blow the timeout and silently drop the speech. Writing text is ~10ms.
#
# mktemp + mv is load-bearing: writing straight into queue/ races writer.sh,
# which would pick up a half-written file. mv within a filesystem is atomic.
tmp=$(mktemp -t speak) || exit 0
printf '%s' "${project:+In $project. }$clean" > "$tmp"
mv "$tmp" "$Q/$(date +%s%N).txt" 2>/dev/null
rm -f "$tmp" 2>/dev/null
exit 0

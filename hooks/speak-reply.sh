#!/bin/bash
set -uo pipefail

SPEECH="$HOME/speech"
Q="$SPEECH/queue"
SELECTION="$SPEECH/selection.json"

[ -d "$SPEECH" ] || exit 0
[ -f "$SPEECH/config.sh" ] && . "$SPEECH/config.sh"
mkdir -p "$Q" 2>/dev/null
payload=$(cat)

read -r event session cwd < <(
  printf '%s' "$payload" | python3 -c '
import json, sys
try:
    data = json.load(sys.stdin)
except Exception:
    print("- - -"); raise SystemExit
print(data.get("hook_event_name") or "-", data.get("session_id") or "-", data.get("cwd") or "-")
' 2>/dev/null
)

raw_text=$(printf '%s' "$payload" | python3 -c '
import json, sys
try:
    data = json.load(sys.stdin)
    value = data.get("last_assistant_message") or data.get("prompt") or ""
    print(value)
except Exception:
    pass
' 2>/dev/null)
registry_line=$(printf '%s' "$raw_text" | tr '\n' ' ' | tr -s ' ' | head -c 180)

# Registry and follow-mode state share one lock with server.py. A phone pin and
# a UserPromptSubmit can no longer overwrite each other with torn mode/active files.
CHANNEL_TTL_HOURS="${CHANNEL_TTL_HOURS:-6}" python3 - "$SPEECH" "$event" "$session" "$cwd" "$registry_line" <<'PY' 2>/dev/null
import fcntl, json, os, pathlib, sys, tempfile, time

speech = pathlib.Path(sys.argv[1])
event, session, cwd = sys.argv[2:5]
last_line = sys.argv[5]
channels_path = speech / "channels.json"
selection_path = speech / "selection.json"

def read(path, default):
    try:
        return json.loads(path.read_text())
    except Exception:
        return default

def write(path, value):
    fd, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    with os.fdopen(fd, "w") as handle:
        json.dump(value, handle, separators=(",", ":"))
        handle.flush(); os.fsync(handle.fileno())
    os.replace(temporary, path)

ttl_hours = float(os.environ.get("CHANNEL_TTL_HOURS") or 6)

with (speech / ".state.lock").open("a+") as lock:
    fcntl.flock(lock, fcntl.LOCK_EX)
    now = time.time()
    state = read(selection_path, {
        "mode": "follow", "session_id": None, "follow_session_id": None
    })
    # Always drop the session's old row; re-add it below unless it just ended.
    channels = [
        channel for channel in read(channels_path, [])
        if channel.get("session_id") != session
    ]

    if event == "SessionEnd":
        # Session quit -> remove it from the picker and unstick any selection
        # that pointed at it, so narration doesn't cling to a dead channel.
        if state.get("follow_session_id") == session:
            state["follow_session_id"] = None
        if state.get("session_id") == session:
            state["mode"] = "follow"
            state["session_id"] = state.get("follow_session_id")
        write(selection_path, state)
    else:
        channels.append({
            "session_id": session,
            "project": pathlib.Path(cwd).name if cwd != "-" else "",
            "last_active_epoch": now,
            "last_line": last_line,
        })

    # Backstop for abrupt closes (terminal killed -> no SessionEnd): expire idle
    # rows after CHANNEL_TTL_HOURS. A pinned session is always kept.
    pinned = state.get("session_id") if state.get("mode") == "pinned" else None
    cutoff = now - ttl_hours * 3600
    channels = [
        channel for channel in channels
        if channel.get("last_active_epoch", 0) >= cutoff
        or channel.get("session_id") == pinned
    ]
    channels.sort(key=lambda channel: channel.get("last_active_epoch", 0), reverse=True)
    write(channels_path, channels[:50])

    if event == "UserPromptSubmit":
        state["follow_session_id"] = session
        if state.get("mode", "follow") == "follow":
            state["session_id"] = session
        write(selection_path, state)
PY

case "$event" in
  UserPromptSubmit)
    # Record the user's prompt in the transcript (screen only, NOT spoken) when
    # this session is the selected channel — turns the transcript into a
    # two-sided conversation. The registry block above already made this session
    # active in follow mode, so the selection check reflects that.
    sel=$(python3 - "$SELECTION" <<'PY' 2>/dev/null
import json, pathlib, sys
try:
    print(json.loads(pathlib.Path(sys.argv[1]).read_text()).get("session_id") or "")
except Exception:
    pass
PY
)
    if [ -f "$SPEECH/speak-all" ] || [ "$session" = "$sel" ]; then
      printf '%s' "$raw_text" | python3 "$SPEECH/transcript_user.py" "$session" "$(basename "$cwd")" 2>/dev/null
    fi
    exit 0 ;;
  Stop) ;;
  *) exit 0 ;;
esac

if [ ! -f "$SPEECH/speak-all" ]; then
  selected=$(python3 - "$SELECTION" <<'PY' 2>/dev/null
import json, pathlib, sys
try:
    print(json.loads(pathlib.Path(sys.argv[1]).read_text()).get("session_id") or "")
except Exception:
    pass
PY
)
  if [ -z "$selected" ] || [ "$session" != "$selected" ]; then
    python3 "$SPEECH/ravenlog.py" hook gate_skip session="$session" selected="$selected" project="$(basename "$cwd")" 2>/dev/null
    exit 0
  fi
fi

text="$raw_text"
[ -z "${text// }" ] && exit 0

# MAX_SPOKEN_CHARS=0 (or unset) => no cap: speak the whole reply.
cap="${MAX_SPOKEN_CHARS:-0}"
[ "$cap" -gt 0 ] 2>/dev/null || cap=100000000
clean=$(printf '%s' "$text" \
  | sed -e '/^[[:space:]]*```/,/^[[:space:]]*```/d' \
        -e 's/`[^`]*`/ /g' \
        -e 's/[*_#>|]//g' \
        -e 's|/[A-Za-z0-9._/-]\{12,\}| that path |g' \
  | tr -s ' \n' ' ' \
  | head -c "$cap")
[ -z "${clean// }" ] && exit 0

project=$(basename "$cwd" 2>/dev/null)
if [ "$project" = "-" ] || [ -z "$project" ]; then project=""; fi

stamp=$(date +%s%N)
text_tmp=$(mktemp "$Q/.text.XXXXXX") || exit 0
meta_tmp=$(mktemp "$Q/.meta.XXXXXX") || { rm -f "$text_tmp"; exit 0; }
printf '%s' "${project:+In $project. }$clean" > "$text_tmp"
printf '%s' "$clean" | SESSION_ID="$session" PROJECT="$project" STAMP="$stamp" \
  python3 -c '
import json, os, sys
json.dump({
    "id": os.environ["STAMP"],
    "session_id": os.environ["SESSION_ID"],
    "project": os.environ["PROJECT"],
    "text": sys.stdin.read(),
}, sys.stdout, separators=(",", ":"))
' > "$meta_tmp"

# Metadata first; the .txt rename is the queue commit marker.
mv "$meta_tmp" "$Q/$stamp.caption.json" && mv "$text_tmp" "$Q/$stamp.txt"
rm -f "$text_tmp" "$meta_tmp" 2>/dev/null
python3 "$SPEECH/ravenlog.py" hook queued id="$stamp" session="$session" project="$project" chars="${#clean}" 2>/dev/null
exit 0

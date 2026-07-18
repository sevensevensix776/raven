#!/usr/bin/env python3
"""Append the user's prompt to the transcript (role=user, text only — never
spoken). Called by the hook on UserPromptSubmit for the selected channel.

    printf '%s' "$prompt" | python3 transcript_user.py <session_id> <project>
"""
import os
import sys
import time

# Reuse the flocked, atomic appender from transcript_add.py regardless of cwd.
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from transcript_add import append  # noqa: E402

session = sys.argv[1] if len(sys.argv) > 1 else ""
project = sys.argv[2] if len(sys.argv) > 2 else ""
text = " ".join(sys.stdin.read().split())[:600]
if not text:
    raise SystemExit(0)

append({
    "id": f"u{time.time_ns()}",
    "session_id": session,
    "project": project,
    "text": text,
    "role": "user",
    "spoken_at_epoch": time.time(),
})

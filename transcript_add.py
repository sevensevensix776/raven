#!/usr/bin/env python3
"""Record Claude's utterance when writer.sh starts emitting it (role=claude)."""

import fcntl
import json
import os
import pathlib
import sys
import tempfile
import time

SPEECH = pathlib.Path.home() / "speech"
SPOKEN = SPEECH / "spoken.jsonl"
LOCK = SPEECH / ".transcript.lock"


def append(entry):
    """Flocked, atomic append to spoken.jsonl — the hook (user lines) and the
    writer (claude lines) both call this and must not clobber each other."""
    with LOCK.open("a+") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        try:
            existing = SPOKEN.read_text(encoding="utf-8").splitlines()[-199:]
        except OSError:
            existing = []
        existing.append(json.dumps(entry, separators=(",", ":"), ensure_ascii=False))
        fd, tmp = tempfile.mkstemp(prefix=".spoken.", dir=SPEECH)
        try:
            with os.fdopen(fd, "w", encoding="utf-8") as h:
                h.write("\n".join(existing) + "\n")
                h.flush()
                os.fsync(h.fileno())
            os.replace(tmp, SPOKEN)
        finally:
            try:
                os.unlink(tmp)
            except FileNotFoundError:
                pass


if __name__ == "__main__":
    metadata_path = pathlib.Path(sys.argv[1])
    try:
        entry = json.loads(metadata_path.read_text())
    except (OSError, json.JSONDecodeError):
        raise SystemExit(0)
    entry["id"] = entry.get("id") or metadata_path.name.split(".", 1)[0]
    entry["spoken_at_epoch"] = time.time()
    entry.setdefault("role", "claude")
    append(entry)

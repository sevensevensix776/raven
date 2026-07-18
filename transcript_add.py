#!/usr/bin/env python3
"""Record an utterance when writer.sh starts emitting it."""

import json
import os
import pathlib
import sys
import tempfile
import time

speech = pathlib.Path.home() / "speech"
spoken = speech / "spoken.jsonl"
metadata_path = pathlib.Path(sys.argv[1])

try:
    entry = json.loads(metadata_path.read_text())
except (OSError, json.JSONDecodeError):
    raise SystemExit(0)

entry["id"] = entry.get("id") or metadata_path.name.split(".", 1)[0]
entry["spoken_at_epoch"] = time.time()

try:
    existing = spoken.read_text().splitlines()[-199:]
except OSError:
    existing = []
existing.append(json.dumps(entry, separators=(",", ":")))

fd, temporary = tempfile.mkstemp(prefix=".spoken.", dir=speech)
try:
    with os.fdopen(fd, "w") as handle:
        handle.write("\n".join(existing) + "\n")
        handle.flush()
        os.fsync(handle.fileno())
    os.replace(temporary, spoken)
finally:
    try:
        os.unlink(temporary)
    except FileNotFoundError:
        pass

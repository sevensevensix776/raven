#!/usr/bin/env python3
"""Raven unified structured logger.

Every component (hook, synthd, writer, server, phone) appends one JSON object
per line to logs/events.jsonl. One log, correlatable by `id` (the utterance
stamp) across hook -> synth -> emit, so a diagnosis reads a single file.

Usage (python):   import ravenlog; ravenlog.log("synthd", "synth", id=stamp, ms=12, ok=True)
Usage (shell):    python3 ravenlog.py <comp> <event> k=v k=v ...
"""
import json
import os
import sys
import time
from pathlib import Path

LOGDIR = Path.home() / "speech" / "logs"
EVENTS = LOGDIR / "events.jsonl"
MAX_LINES = 20000  # trim threshold; keeps the file bounded without a cron


def log(comp: str, event: str, **fields):
    LOGDIR.mkdir(parents=True, exist_ok=True)
    rec = {"ts": round(time.time(), 3), "comp": comp, "event": event}
    for k, v in fields.items():
        if v is not None:
            rec[k] = v
    line = json.dumps(rec, separators=(",", ":"), ensure_ascii=False)
    with EVENTS.open("a", encoding="utf-8") as f:
        f.write(line + "\n")
    _maybe_trim()


def _maybe_trim():
    # Cheap probabilistic-ish trim: only when the file is large.
    try:
        if EVENTS.stat().st_size < 4_000_000:
            return
        lines = EVENTS.read_text(encoding="utf-8").splitlines()
        if len(lines) > MAX_LINES:
            keep = "\n".join(lines[-MAX_LINES:]) + "\n"
            tmp = EVENTS.with_suffix(".jsonl.tmp")
            tmp.write_text(keep, encoding="utf-8")
            os.replace(tmp, EVENTS)
    except OSError:
        pass


def _coerce(v: str):
    low = v.lower()
    if low in ("true", "false"):
        return low == "true"
    try:
        return int(v)
    except ValueError:
        pass
    try:
        return float(v)
    except ValueError:
        return v


if __name__ == "__main__":
    # CLI form for bash: ravenlog.py <comp> <event> k=v ...
    if len(sys.argv) < 3:
        sys.exit(0)
    comp, event = sys.argv[1], sys.argv[2]
    fields = {}
    for arg in sys.argv[3:]:
        if "=" in arg:
            k, v = arg.split("=", 1)
            fields[k] = _coerce(v)
    try:
        log(comp, event, **fields)
    except Exception:
        sys.exit(0)  # logging must never break the caller

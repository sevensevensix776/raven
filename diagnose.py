#!/usr/bin/env python3
"""Raven automatic diagnosis: one command that reads live state + the unified
event log and tells you whether the pipeline is healthy and what's wrong.

    python3 ~/code/experiments/raven/diagnose.py [--since-min N]
"""
import json
import os
import signal
import sys
import time
from collections import Counter
from pathlib import Path

SPEECH = Path(os.environ.get("RAVEN_HOME") or Path.home() / "code" / "experiments" / "raven")
EVENTS = SPEECH / "logs" / "events.jsonl"
PHONE = SPEECH / "logs" / "phone.jsonl"

since_min = 60
if "--since-min" in sys.argv:
    try:
        since_min = int(sys.argv[sys.argv.index("--since-min") + 1])
    except (ValueError, IndexError):
        pass
cutoff = time.time() - since_min * 60


def alive(pidfile):
    try:
        pid = int((SPEECH / pidfile).read_text().strip())
        os.kill(pid, 0)
        return pid
    except (OSError, ValueError):
        return None


def load_events():
    out = []
    try:
        for line in EVENTS.read_text(encoding="utf-8").splitlines():
            try:
                rec = json.loads(line)
            except json.JSONDecodeError:
                continue
            if rec.get("ts", 0) >= cutoff:
                out.append(rec)
    except OSError:
        pass
    return out


def hms(age):
    if age is None:
        return "never"
    if age < 90:
        return f"{age:.0f}s ago"
    if age < 5400:
        return f"{age/60:.0f}m ago"
    return f"{age/3600:.1f}h ago"


C = {"ok": "\033[32m", "warn": "\033[33m", "bad": "\033[31m", "dim": "\033[2m", "z": "\033[0m"}


def mark(good):
    return f"{C['ok']}OK{C['z']}" if good else f"{C['bad']}FAIL{C['z']}"


print(f"\n  RAVEN DIAGNOSIS  {C['dim']}(last {since_min}m){C['z']}\n" + "  " + "-" * 40)

# 1. Processes
print("\n  PROCESSES")
all_up = True
for role in ("writer", "ffmpeg", "server", "synthd"):
    pid = alive(f".{role}.pid")
    all_up = all_up and pid is not None
    print(f"    {mark(pid is not None):>16}  {role}" + (f"  {C['dim']}pid {pid}{C['z']}" if pid else ""))

# 2. Live state
now = time.time()
try:
    hb_age = now - (SPEECH / "hls" / ".heartbeat").stat().st_mtime
except OSError:
    hb_age = None
live = hb_age is not None and hb_age <= 10
print("\n  STREAM")
print(f"    listener (phone) polling:  {'yes' if live else 'no'}  {C['dim']}({hms(hb_age)}){C['z']}")
qdir = SPEECH / "queue"
pend = {e: len(list(qdir.glob(f"*.{e}"))) for e in ("txt", "wav", "aiff")}
print(f"    queue pending:             txt={pend['txt']} wav={pend['wav']} aiff={pend['aiff']}")
try:
    sel = json.loads((SPEECH / "selection.json").read_text())
    print(f"    channel:                   {sel.get('mode')} -> {sel.get('session_id')}")
except OSError:
    print("    channel:                   (none selected)")

# 3. Metrics from the event log
ev = load_events()
queued = [e for e in ev if e.get("event") == "queued"]
synths = [e for e in ev if e.get("event") == "synth"]
skips = [e for e in ev if e.get("event") == "gate_skip"]
fails = [e for e in ev if e.get("event") in ("kokoro_fail", "say_fail")]
backends = Counter(e.get("backend") for e in synths if e.get("ok"))
ms = [e.get("ms") for e in synths if e.get("ok") and isinstance(e.get("ms"), (int, float))]
print("\n  METRICS")
print(f"    replies spoken:            {len(queued)}")
print(f"    synth backends:            " + (", ".join(f"{k}={v}" for k, v in backends.items()) or "none"))
if ms:
    ms.sort()
    print(f"    synth latency:             median {ms[len(ms)//2]:.0f}ms  max {ms[-1]:.0f}ms")
print(f"    gate skips (other chans):  {len(skips)}")
fell_back = backends.get("say", 0)
fb_col = C['warn'] if fell_back else C['dim']
print(f"    {fb_col}kokoro->say fallbacks:      {fell_back}{C['z']}")

# 4. Phone
try:
    plines = PHONE.read_text(encoding="utf-8").splitlines()
    last_phone = json.loads(plines[-1]).get("line", "") if plines else ""
    print("\n  PHONE")
    print(f"    log lines uploaded:        {len(plines)}")
    print(f"    last:                      {C['dim']}{last_phone[:60]}{C['z']}")
except (OSError, IndexError, json.JSONDecodeError):
    print("\n  PHONE\n    no uploads yet (app hasn't posted /log)")

# 5. Errors — the headline
print("\n  ERRORS")
if fails:
    for e in fails[-3:]:
        print(f"    {C['bad']}{e.get('event')}{C['z']}  id={e.get('id')}  {C['dim']}{str(e.get('err'))[:70]}{C['z']}")
else:
    print(f"    {C['ok']}none{C['z']}")

# Verdict
print("\n  " + "-" * 40)
healthy = all_up and not fails
verdict = f"{C['ok']}HEALTHY{C['z']}" if healthy else f"{C['bad']}NEEDS ATTENTION{C['z']}"
print(f"  VERDICT: {verdict}\n")

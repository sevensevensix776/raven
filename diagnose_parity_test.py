#!/usr/bin/env python3
"""Compare diagnose.py and `raven diagnose` on one isolated seeded home.

Both the Python source and Go resolve the runtime home from RAVEN_HOME, so both
receive the same isolated seeded directory. No live Raven files are read or
changed.

    go build -o raven . && python3 diagnose_parity_test.py
"""

import json
import os
import re
import subprocess
import sys
import tempfile
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parent
GO_BIN = ROOT / "raven"
PYTHON_DIAGNOSE = Path.home() / "code" / "experiments" / "raven" / "diagnose.py"
ANSI = re.compile(r"\x1b\[[0-9;]*m")
HEARTBEAT_AGE = re.compile(r"\((-?\d+)s ago\)")


def seed(home: Path):
    (home / "hls").mkdir(parents=True)
    (home / "queue").mkdir()
    (home / "logs").mkdir()

    pid = os.getpid()
    for role in ("writer", "ffmpeg", "server", "synthd"):
        (home / f".{role}.pid").write_text(str(pid), encoding="utf-8")

    heartbeat = home / "hls" / ".heartbeat"
    heartbeat.touch()
    heartbeat_time = time.time() - 4
    os.utime(heartbeat, (heartbeat_time, heartbeat_time))

    for name in ("one.txt", "two.txt", "ready.wav", "legacy.aiff"):
        (home / "queue" / name).touch()

    (home / "selection.json").write_text(
        json.dumps({"mode": "pinned", "session_id": "session-a"}),
        encoding="utf-8",
    )

    now = time.time()
    events = [
        {"ts": now - 30, "comp": "hook", "event": "queued"},
        {"ts": now - 25, "comp": "hook", "event": "queued"},
        {"ts": now - 20, "comp": "synthd", "event": "synth", "ok": True, "backend": "kokoro", "ms": 120},
        {"ts": now - 15, "comp": "synthd", "event": "synth", "ok": True, "backend": "say", "ms": 240},
        {"ts": now - 10, "comp": "hook", "event": "gate_skip"},
        {"ts": now - 4000, "comp": "hook", "event": "queued"},
    ]
    with (home / "logs" / "events.jsonl").open("w", encoding="utf-8") as out:
        for event in events:
            out.write(json.dumps(event, separators=(",", ":")) + "\n")
        out.write("malformed event line\n")

    phone = [
        {"device": "iphone", "line": "connected"},
        {"device": "iphone", "line": "playing session-a"},
    ]
    with (home / "logs" / "phone.jsonl").open("w", encoding="utf-8") as out:
        for line in phone:
            out.write(json.dumps(line, separators=(",", ":")) + "\n")


def clean(output: str) -> tuple[str, int]:
    output = ANSI.sub("", output)
    match = HEARTBEAT_AGE.search(output)
    if not match:
        raise AssertionError("expected a seconds-old heartbeat in diagnosis output")
    age = int(match.group(1))
    return HEARTBEAT_AGE.sub("(<heartbeat age>)", output), age


def main():
    if not GO_BIN.exists():
        print("build first: go build -o raven .")
        return 1
    if not PYTHON_DIAGNOSE.exists():
        print(f"missing source of truth: {PYTHON_DIAGNOSE}")
        return 1

    with tempfile.TemporaryDirectory() as td:
        temp_root = Path(td)
        home = temp_root / "speech"
        seed(home)

        base_env = dict(os.environ)
        # Both the Python source and Go honor RAVEN_HOME; drive them identically.
        python_env = {**base_env, "RAVEN_HOME": str(home)}
        go_env = {**base_env, "RAVEN_HOME": str(home)}
        python_output = subprocess.run(
            [sys.executable, str(PYTHON_DIAGNOSE), "--since-min", "60"],
            env=python_env,
            capture_output=True,
            text=True,
            check=True,
        ).stdout
        go_output = subprocess.run(
            [str(GO_BIN), "diagnose", "--since-min", "60"],
            env=go_env,
            capture_output=True,
            text=True,
            check=True,
        ).stdout

        python_clean, python_age = clean(python_output)
        go_clean, go_age = clean(go_output)
        if python_clean != go_clean or abs(python_age - go_age) > 1:
            print("DIAGNOSE PARITY FAILED")
            print("\n--- Python ---\n" + python_clean)
            print("\n--- Go ---\n" + go_clean)
            return 1

    print("DIAGNOSE PARITY OK: section values and HEALTHY verdict match (heartbeat age within 1s)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

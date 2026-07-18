#!/usr/bin/env python3
"""Cross-language parity harness: run the bash hook and the Go hook against
IDENTICAL inputs + initial state, and assert the resulting Raven state is
semantically identical. Timestamps and stamp-derived ids/filenames are
normalized (they legitimately differ between two runs); everything else must
match exactly.

    python3 parity_test.py
"""
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

SPEECH_SRC = Path.home() / "speech"
BASH_HOOK = Path.home() / ".claude/hooks/speak-reply.sh"
GO_BIN = Path(__file__).parent / "raven"
HELPERS = ["ravenlog.py", "transcript_user.py", "transcript_add.py"]

# (name, initial_state, payloads[])  — payloads run in sequence against one dir.
CASES = [
    ("follow_sets_active_and_speaks", {}, [
        {"hook_event_name": "UserPromptSubmit", "session_id": "A", "cwd": "/Users/x/code/cerebro-api", "prompt": "Fix the migration please"},
        {"hook_event_name": "Stop", "session_id": "A", "cwd": "/Users/x/code/cerebro-api", "last_assistant_message": "Done. The migration is applied and tests pass."},
    ]),
    ("stop_non_selected_gate_skips", {"selection.json": {"mode": "pinned", "session_id": "A", "follow_session_id": "A"}, "channels.json": [{"session_id": "A", "project": "cerebro-api", "last_active_epoch": 1.0, "last_line": "", "recent": []}]}, [
        {"hook_event_name": "Stop", "session_id": "B", "cwd": "/x/forge", "last_assistant_message": "This should be gated out and not queued."},
    ]),
    ("session_end_removes_and_unsticks", {"selection.json": {"mode": "pinned", "session_id": "Y", "follow_session_id": "X"}, "channels.json": [{"session_id": "Y", "project": "forge", "last_active_epoch": 9e12, "last_line": "", "recent": []}, {"session_id": "X", "project": "api", "last_active_epoch": 9e12, "last_line": "", "recent": []}]}, [
        {"hook_event_name": "SessionEnd", "session_id": "Y", "cwd": "/x/forge", "reason": "clear"},
    ]),
    ("code_and_paths_cleaned", {"selection.json": {"mode": "pinned", "session_id": "A", "follow_session_id": "A"}, "channels.json": [{"session_id": "A", "project": "cerebro-api", "last_active_epoch": 9e12, "last_line": "", "recent": []}]}, [
        {"hook_event_name": "Stop", "session_id": "A", "cwd": "/Users/x/code/cerebro-api", "last_assistant_message": "Edit /Users/x/code/experiments/thing.go and run `yarn build`.\n```go\nfmt.Println(1)\n```\n**Done**."},
    ]),
    ("recent_accumulates_three", {"selection.json": {"mode": "pinned", "session_id": "A", "follow_session_id": "A"}, "channels.json": [{"session_id": "A", "project": "api", "last_active_epoch": 9e12, "last_line": "", "recent": []}]}, [
        {"hook_event_name": "Stop", "session_id": "A", "cwd": "/x/api", "last_assistant_message": "First reply about reading the schema."},
        {"hook_event_name": "Stop", "session_id": "A", "cwd": "/x/api", "last_assistant_message": "Second reply about the fix."},
        {"hook_event_name": "Stop", "session_id": "A", "cwd": "/x/api", "last_assistant_message": "Third reply, deploy green."},
        {"hook_event_name": "Stop", "session_id": "A", "cwd": "/x/api", "last_assistant_message": "Fourth reply should push out the first."},
    ]),
]


def setup(d: Path, initial: dict):
    if d.exists():
        shutil.rmtree(d)
    (d / "queue").mkdir(parents=True)
    for h in HELPERS:
        shutil.copy(SPEECH_SRC / h, d / h)
    # config.sh with the defaults the live system uses
    (d / "config.sh").write_text("MAX_SPOKEN_CHARS=0\nCHANNEL_TTL_HOURS=6\n")
    for name, val in initial.items():
        (d / name).write_text(json.dumps(val))


def run_hook(cmd, env, payload):
    subprocess.run(cmd, input=json.dumps(payload).encode(), env=env,
                   capture_output=True, timeout=30)


TS = re.compile(r"^\d+(\.\d+)?$")


def norm(obj):
    """Normalize timestamps / stamp-derived ids so two runs compare equal."""
    if isinstance(obj, dict):
        out = {}
        for k, v in obj.items():
            if k in ("last_active_epoch", "at", "spoken_at_epoch", "ts"):
                out[k] = "<TS>"
            elif k in ("id",):
                out[k] = "<ID>"
            else:
                out[k] = norm(v)
        return out
    if isinstance(obj, list):
        return [norm(x) for x in obj]
    return obj


def snapshot(d: Path):
    snap = {}
    for name in ("channels.json", "selection.json"):
        p = d / name
        snap[name] = norm(json.loads(p.read_text())) if p.exists() else None
    # queue: normalize stamp filenames, keep content
    q = {}
    for f in sorted((d / "queue").glob("*")):
        key = re.sub(r"\d{10,}", "<STAMP>", f.name)
        try:
            q[key] = norm(json.loads(f.read_text())) if f.suffix == ".json" else f.read_text()
        except json.JSONDecodeError:
            q[key] = f.read_text()
    snap["queue"] = q
    for name in ("spoken.jsonl",):
        p = d / name
        snap[name] = [norm(json.loads(l)) for l in p.read_text().splitlines() if l.strip()] if p.exists() else None
    # events: keep comp/event/session/project/selected/chars, drop id/ts
    ev = d / "logs/events.jsonl"
    if ev.exists():
        rows = []
        for l in ev.read_text().splitlines():
            if not l.strip():
                continue
            r = norm(json.loads(l))
            rows.append(r)
        snap["events"] = rows
    else:
        snap["events"] = None
    return snap


def main():
    if not GO_BIN.exists():
        print("build first: go build -o raven .")
        sys.exit(1)
    base_env = dict(os.environ)
    failures = 0
    for name, initial, payloads in CASES:
        with tempfile.TemporaryDirectory() as td:
            bash_dir = Path(td) / "bash"
            go_dir = Path(td) / "go"

            setup(bash_dir, initial)
            env = {**base_env, "RAVEN_HOME": str(bash_dir)}
            for pl in payloads:
                run_hook(["bash", str(BASH_HOOK)], env, pl)
            bash_snap = snapshot(bash_dir)

            setup(go_dir, initial)
            env = {**base_env, "RAVEN_HOME": str(go_dir)}
            for pl in payloads:
                run_hook([str(GO_BIN), "hook"], env, pl)
            go_snap = snapshot(go_dir)

            if bash_snap == go_snap:
                print(f"  PASS  {name}")
            else:
                failures += 1
                print(f"  FAIL  {name}")
                for key in bash_snap:
                    if bash_snap[key] != go_snap[key]:
                        print(f"        [{key}]")
                        print(f"          bash: {json.dumps(bash_snap[key])[:300]}")
                        print(f"          go  : {json.dumps(go_snap[key])[:300]}")
    print()
    if failures:
        print(f"PARITY FAILED: {failures}/{len(CASES)} cases differ")
        sys.exit(1)
    print(f"PARITY OK: {len(CASES)}/{len(CASES)} cases identical")


if __name__ == "__main__":
    main()

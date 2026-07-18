#!/usr/bin/env python3
"""Run Python server.py and `raven serve` against one seeded Raven home.

The current Python main hardcodes port 8080 and resolves its home from RAVEN_HOME, so
this harness imports its unmodified handler, redirects those globals to the
shared temporary RAVEN_HOME, and binds an ephemeral port. Production files are
never written.
"""

import hashlib
import http.client
import json
import os
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parent
PYTHON_SERVER = Path.home() / "code" / "experiments" / "raven" / "server.py"

PYTHON_RUNNER = r"""
import os
import pathlib
import sys

server_path = pathlib.Path(os.environ["RAVEN_PYTHON_SERVER"])
sys.path.insert(0, str(server_path.parent))
import server

home = pathlib.Path(os.environ["RAVEN_HOME"])
server.SPEECH = home
server.ROOT = home / "hls"
server.HB = server.ROOT / ".heartbeat"
server.CHANNELS = home / "channels.json"
server.SELECTION = home / "selection.json"
server.SPOKEN = home / "spoken.jsonl"
server.STATE_LOCK = home / ".state.lock"
server.ravenlog.LOGDIR = home / "logs"
server.ravenlog.EVENTS = server.ravenlog.LOGDIR / "events.jsonl"

host, port = os.environ["PARITY_ADDR"].rsplit(":", 1)
if os.environ.get("PARITY_FDS"):
    class OneShotServer:
        server_name = "localhost"
        server_port = 0

    import socket
    for raw_fd in os.environ["PARITY_FDS"].split(","):
        connection = socket.socket(fileno=int(raw_fd))
        server.HuginnHandler(connection, ("local", 0), OneShotServer())
        connection.close()
else:
    server.http.server.ThreadingHTTPServer.allow_reuse_address = True
    server.http.server.ThreadingHTTPServer((host, int(port)), server.HuginnHandler).serve_forever()
"""


def free_addr():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return f"127.0.0.1:{sock.getsockname()[1]}"


class SocketPairEndpoint:
    def __init__(self, connections):
        self.connections = connections

    def request(self, path, method, body, headers):
        if not self.connections:
            raise AssertionError("socketpair request budget exhausted")
        connection = self.connections.pop(0)
        request_headers = {"Host": "localhost", "Connection": "close", **headers}
        if body is not None:
            request_headers["Content-Length"] = str(len(body))
        head = [f"{method} {path} HTTP/1.1"]
        head.extend(f"{key}: {value}" for key, value in request_headers.items())
        connection.sendall(("\r\n".join(head) + "\r\n\r\n").encode() + (body or b""))
        response = http.client.HTTPResponse(connection)
        response.begin()
        raw = response.read()
        result = response.status, dict(response.getheaders()), raw, parse_json(raw)
        connection.close()
        return result


def socketpair_endpoint(count=48):
    clients, servers = [], []
    for _ in range(count):
        client, server = socket.socketpair()
        clients.append(client)
        servers.append(server)
    return SocketPairEndpoint(clients), servers


def seed(home: Path):
    (home / "hls").mkdir(parents=True)
    (home / "queue").mkdir()
    (home / "hls" / "stream.m3u8").write_text(
        "#EXTM3U\n#EXTINF:2.0,\nsegment000.ts\n", encoding="utf-8"
    )
    (home / "hls" / "segment000.ts").write_bytes(b"mpeg-segment")
    (home / "hls" / ".heartbeat").touch()
    old_heartbeat = time.time() - 5
    os.utime(home / "hls" / ".heartbeat", (old_heartbeat, old_heartbeat))
    channels = [
        {
            "session_id": "session-alpha-123",
            "project": "alpha",
            "last_active_epoch": 100,
            "last_line": "older",
            "recent": [{"text": "Alpha reply", "at": 1700000000.75}],
        },
        {
            "session_id": "session-beta-456",
            "project": "beta",
            "last_active_epoch": 200,
            "last_line": "newer",
            "recent": [
                {"text": "Beta one", "at": 1700000100},
                {"text": "Beta two", "at": 1700000200.5},
            ],
        },
    ]
    selection = {
        "mode": "follow",
        "session_id": "session-alpha-123",
        "follow_session_id": "session-alpha-123",
    }
    (home / "channels.json").write_text(json.dumps(channels, separators=(",", ":")))
    (home / "selection.json").write_text(json.dumps(selection, separators=(",", ":")))
    spoken = [
        {"id": "one", "session_id": "session-alpha-123", "project": "alpha", "text": "First", "role": "claude", "spoken_at_epoch": 1700000001},
        {"id": "two", "session_id": "session-beta-456", "project": "beta", "text": "Unicode café 🚗", "role": "claude", "spoken_at_epoch": 1700000002},
        {"id": "three", "session_id": "session-beta-456", "project": "beta", "text": "Last spoken line", "role": "claude", "spoken_at_epoch": 1700000003},
    ]
    (home / "spoken.jsonl").write_text(
        "\n".join(json.dumps(line, separators=(",", ":"), ensure_ascii=False) for line in spoken) + "\n"
    )
    for name in ("pending.txt", "ready.wav", "old.aiff"):
        (home / "queue" / name).write_bytes(b"")


def parse_json(raw):
    if not raw:
        return None
    try:
        return json.loads(raw)
    except json.JSONDecodeError:
        return None


def request(base, path, method="GET", payload=None, headers=None):
    body = None if payload is None else json.dumps(payload, separators=(",", ":")).encode()
    request_headers = dict(headers or {})
    if body is not None:
        request_headers["Content-Type"] = "application/json"
    if isinstance(base, SocketPairEndpoint):
        return base.request(path, method, body, request_headers)
    req = urllib.request.Request(base + path, data=body, method=method, headers=request_headers)
    if body is not None:
        req.add_header("Content-Type", "application/json")
    try:
        response = urllib.request.urlopen(req, timeout=5)
    except urllib.error.HTTPError as exc:
        response = exc
    raw = response.read()
    parsed = parse_json(raw)
    return response.status, dict(response.headers.items()), raw, parsed


def wait_ready(base, process, name):
    deadline = time.time() + 10
    while time.time() < deadline:
        if process.poll() is not None:
            stderr = process.stderr.read().decode(errors="replace")
            raise AssertionError(f"{name} exited during startup: {stderr}")
        try:
            if request(base, "/health")[0] == 200:
                return
        except OSError:
            pass
        time.sleep(0.05)
    raise AssertionError(f"{name} did not become ready")


# name/display/transcript_path are Go-only enrichments (local session title,
# readable transcript rendering, transcript tailing) with no Python counterpart.
# Strip them recursively so parity compares the shared contract, matching the
# same normalization in parity_test.py.
GO_ONLY_KEYS = ("name", "display", "transcript_path")


def strip_go_only(value):
    if isinstance(value, dict):
        return {k: strip_go_only(v) for k, v in value.items() if k not in GO_ONLY_KEYS}
    if isinstance(value, list):
        return [strip_go_only(v) for v in value]
    return value


def normalized_health(value):
    value = dict(value)
    value["ts"] = "<timestamp>"
    value["heartbeat_age_s"] = "<age>" if value["heartbeat_age_s"] is not None else None
    if value.get("last_spoken"):
        value["last_spoken"] = dict(value["last_spoken"])
        value["last_spoken"]["spoken_at_epoch"] = "<timestamp>"
    return value


def header(headers, name):
    wanted = name.lower()
    return next((value for key, value in headers.items() if key.lower() == wanted), None)


def assert_json_pair(python_base, go_base, path, method="GET", payload=None, normalize=lambda x: x):
    py = request(python_base, path, method, payload)
    go = request(go_base, path, method, payload)
    assert py[0] == go[0], f"{method} {path}: status Python={py[0]} Go={go[0]}"
    assert normalize(py[3]) == normalize(go[3]), (
        f"{method} {path}:\nPython={json.dumps(py[3], sort_keys=True)}\n"
        f"Go={json.dumps(go[3], sort_keys=True)}"
    )
    for result, name in ((py, "Python"), (go, "Go")):
        assert header(result[1], "Content-Type") == "application/json; charset=utf-8", (
            f"{name} {path}: wrong Content-Type {header(result[1], 'Content-Type')}"
        )
        assert header(result[1], "Cache-Control") == "no-cache"
        expected_etag = '"' + hashlib.sha256(result[2]).hexdigest()[:20] + '"'
        assert header(result[1], "ETag") == expected_etag, (
            f"{name} {path}: ETag {header(result[1], 'ETag')} != {expected_etag}"
        )
    print(f"  PASS  {method} {path}")
    return py, go


def assert_conditional(base, path, name):
    first = request(base, path)
    second = request(base, path, headers={"If-None-Match": header(first[1], "ETag")})
    assert second[0] == 304, f"{name} {path}: expected 304, got {second[0]}"
    assert second[2] == b""
    assert header(second[1], "ETag") == header(first[1], "ETag")
    assert header(second[1], "Cache-Control") == "no-cache"


def assert_status_pair(python_base, go_base, path, status, method="GET", payload=None):
    py = request(python_base, path, method, payload)
    go = request(go_base, path, method, payload)
    assert py[0] == go[0] == status, (
        f"{method} {path}: expected {status}, Python={py[0]} Go={go[0]}"
    )
    print(f"  PASS  {method} {path} -> {status}")


def main():
    if not PYTHON_SERVER.exists():
        raise SystemExit(f"missing source server: {PYTHON_SERVER}")
    with tempfile.TemporaryDirectory(prefix="raven-serve-parity-") as td:
        temp = Path(td)
        home = temp / "speech"
        seed(home)
        go_bin = temp / "raven"
        build_env = {**os.environ, "GOCACHE": str(temp / "go-cache")}
        subprocess.run(
            ["go", "build", "-o", str(go_bin), "."], cwd=ROOT, env=build_env, check=True
        )
        socketpair_mode = False
        try:
            python_addr, go_addr = free_addr(), free_addr()
        except PermissionError:
            socketpair_mode = True
            python_addr = go_addr = "127.0.0.1:0"
        common_env = {**os.environ, "RAVEN_HOME": str(home)}
        python_endpoint = go_endpoint = None
        python_sockets = go_sockets = []
        pass_python_fds = pass_go_fds = ()
        if socketpair_mode:
            python_endpoint, python_sockets = socketpair_endpoint()
            go_endpoint, go_sockets = socketpair_endpoint()
            pass_python_fds = tuple(sock.fileno() for sock in python_sockets)
            pass_go_fds = tuple(sock.fileno() for sock in go_sockets)
            print("  INFO  TCP bind unavailable; exercising both HTTP handlers over inherited socketpairs")

        python_process = subprocess.Popen(
            [sys.executable, "-c", PYTHON_RUNNER],
            env={
                **common_env,
                "RAVEN_PYTHON_SERVER": str(PYTHON_SERVER),
                "HUGINN_BIND": python_addr.split(":", 1)[0],
                "PARITY_ADDR": python_addr,
                "PARITY_FDS": ",".join(map(str, pass_python_fds)),
            },
            pass_fds=pass_python_fds,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.PIPE,
        )
        go_process = subprocess.Popen(
            [str(go_bin), "serve", "--addr", go_addr],
            env={
                **common_env,
                "RAVEN_BIND": go_addr,
                "RAVEN_SERVE_FDS": ",".join(map(str, pass_go_fds)),
            },
            pass_fds=pass_go_fds,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.PIPE,
        )
        processes = [python_process, go_process]
        for inherited in python_sockets + go_sockets:
            inherited.close()
        try:
            if socketpair_mode:
                python_base, go_base = python_endpoint, go_endpoint
            else:
                python_base, go_base = f"http://{python_addr}", f"http://{go_addr}"
            wait_ready(python_base, python_process, "Python server")
            wait_ready(go_base, go_process, "Go server")

            assert_json_pair(python_base, go_base, "/channels", normalize=strip_go_only)
            assert_json_pair(python_base, go_base, "/transcript?limit=2", normalize=strip_go_only)
            assert_json_pair(python_base, go_base, "/catchup?session=session-beta-456", normalize=strip_go_only)
            assert_json_pair(python_base, go_base, "/health", normalize=normalized_health)
            assert_json_pair(
                python_base, go_base, "/active", "POST",
                {"mode": "pinned", "session_id": "session-beta-456"},
            )
            assert_json_pair(
                python_base, go_base, "/active", "POST",
                {"mode": "follow", "session_id": None},
            )
            assert_json_pair(
                python_base, go_base, "/log", "POST",
                {"device": "iphone", "lines": ["played one", "café", "done"]},
            )
            assert_status_pair(
                python_base, go_base, "/active", 400, "POST",
                {"mode": "pinned", "session_id": "unknown-session"},
            )
            assert_status_pair(
                python_base, go_base, "/active", 400, "POST",
                {"mode": "invalid", "session_id": None},
            )
            assert_status_pair(python_base, go_base, "/active", 413, "POST")
            assert_status_pair(python_base, go_base, "/log", 413, "POST")

            for base, name in ((python_base, "Python"), (go_base, "Go")):
                assert_conditional(base, "/channels", name)
                assert_conditional(base, "/transcript?limit=2", name)

            before = (home / "hls" / ".heartbeat").stat().st_mtime_ns
            py_stream = request(python_base, "/stream.m3u8")
            go_stream = request(go_base, "/stream.m3u8")
            assert py_stream[0] == go_stream[0] == 200
            assert py_stream[2] == go_stream[2]
            assert header(py_stream[1], "Content-Type") == header(go_stream[1], "Content-Type") == "application/vnd.apple.mpegurl"
            assert header(py_stream[1], "Cache-Control") == header(go_stream[1], "Cache-Control") == "no-store"
            assert (home / "hls" / ".heartbeat").stat().st_mtime_ns > before

            py_segment = request(python_base, "/segment000.ts")
            go_segment = request(go_base, "/segment000.ts")
            assert py_segment[0] == go_segment[0] == 200
            assert py_segment[2] == go_segment[2] == b"mpeg-segment"
            assert header(py_segment[1], "Content-Type") == header(go_segment[1], "Content-Type") == "video/mp2t"

            phone_lines = [json.loads(line) for line in (home / "logs" / "phone.jsonl").read_text().splitlines()]
            assert len(phone_lines) == 6
            assert phone_lines[:3] == phone_lines[3:]
            events = [json.loads(line) for line in (home / "logs" / "events.jsonl").read_text().splitlines()]
            uploads = [event for event in events if event.get("comp") == "phone" and event.get("event") == "log_upload"]
            assert len(uploads) == 2 and all(event.get("n") == 3 for event in uploads)
            print("  PASS  HLS static files, heartbeat, phone log, and ravenlog event")
        finally:
            for process in processes:
                process.terminate()
            for process in processes:
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)

    print("\nSERVE PARITY OK: Python and Go responses are identical")


if __name__ == "__main__":
    main()

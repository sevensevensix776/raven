#!/usr/bin/env python3
"""Serve Huginn's HLS stream and small tailnet control API."""

import contextlib
import fcntl
import hashlib
import http.server
import json
import os
import pathlib
import tempfile
import time
import urllib.parse

import ravenlog

SPEECH = pathlib.Path(os.environ.get("RAVEN_HOME") or pathlib.Path.home() / "code" / "experiments" / "raven")
ROOT = SPEECH / "hls"
HB = ROOT / ".heartbeat"
CHANNELS = SPEECH / "channels.json"
SELECTION = SPEECH / "selection.json"
SPOKEN = SPEECH / "spoken.jsonl"
STATE_LOCK = SPEECH / ".state.lock"
TAILSCALE_IP = os.environ.get("HUGINN_BIND", "100.64.0.1")


def health_snapshot():
    """Live pipeline state for GET /health — the automatic-diagnosis view."""
    now = time.time()
    try:
        hb_age = round(now - HB.stat().st_mtime, 1)
    except OSError:
        hb_age = None
    queue = SPEECH / "queue"
    pending = {ext: len(list(queue.glob(f"*.{ext}"))) for ext in ("txt", "wav", "aiff")}
    try:
        last_spoken = json.loads(SPOKEN.read_text().splitlines()[-1])
        if isinstance(last_spoken.get("text"), str):
            last_spoken["chars"] = len(last_spoken["text"])
            last_spoken["text"] = last_spoken["text"][:120]
    except (OSError, IndexError, json.JSONDecodeError):
        last_spoken = None
    selection = read_json(SELECTION, {})
    return {
        "ts": round(now, 1),
        "heartbeat_age_s": hb_age,
        "listener_live": hb_age is not None and hb_age <= 10,
        "queue_pending": pending,
        "selection": {"mode": selection.get("mode"), "session_id": selection.get("session_id")},
        "channels": len(read_json(CHANNELS, [])),
        "last_spoken": last_spoken,
    }


@contextlib.contextmanager
def state_lock():
    with STATE_LOCK.open("a+") as handle:
        fcntl.flock(handle, fcntl.LOCK_EX)
        try:
            yield
        finally:
            fcntl.flock(handle, fcntl.LOCK_UN)


def read_json(path, default):
    try:
        return json.loads(path.read_text())
    except (FileNotFoundError, json.JSONDecodeError, OSError):
        return default


def atomic_json(path, value):
    fd, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        with os.fdopen(fd, "w") as handle:
            json.dump(value, handle, separators=(",", ":"))
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
    finally:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass


def selection_state():
    return read_json(
        SELECTION,
        {"mode": "follow", "session_id": None, "follow_session_id": None},
    )


def transcript_lines(limit):
    try:
        raw_lines = SPOKEN.read_text().splitlines()[-limit:]
    except OSError:
        return []
    result = []
    for raw in raw_lines:
        try:
            result.append(json.loads(raw))
        except json.JSONDecodeError:
            continue
    return result


class HuginnHandler(http.server.SimpleHTTPRequestHandler):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, directory=str(ROOT), **kwargs)

    def guess_type(self, path):
        if path.endswith(".m3u8"):
            return "application/vnd.apple.mpegurl"
        if path.endswith(".ts"):
            return "video/mp2t"
        return super().guess_type(path)

    def end_headers(self):
        if urllib.parse.urlsplit(self.path).path.endswith(".m3u8"):
            self.send_header("Cache-Control", "no-store")
        super().end_headers()

    def json_response(self, payload, status=200, conditional=True):
        body = json.dumps(payload, separators=(",", ":")).encode()
        etag = '"' + hashlib.sha256(body).hexdigest()[:20] + '"'
        if conditional and self.headers.get("If-None-Match") == etag:
            self.send_response(304)
            self.send_header("ETag", etag)
            self.send_header("Cache-Control", "no-cache")
            self.end_headers()
            return
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-cache")
        self.send_header("ETag", etag)
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        parsed = urllib.parse.urlsplit(self.path)
        if parsed.path == "/health":
            self.json_response(health_snapshot(), conditional=False)
            return

        if parsed.path == "/channels":
            with state_lock():
                channels = read_json(CHANNELS, [])
                selection = selection_state()
            channels.sort(key=lambda channel: channel.get("last_active_epoch", 0), reverse=True)
            self.json_response({
                "channels": channels,
                "selection": {
                    "mode": selection.get("mode", "follow"),
                    "session_id": selection.get("session_id"),
                },
            })
            return

        if parsed.path == "/transcript":
            query = urllib.parse.parse_qs(parsed.query)
            try:
                limit = max(1, min(100, int(query.get("limit", [50])[0])))
            except ValueError:
                limit = 50
            self.json_response({"lines": transcript_lines(limit)})
            return

        if parsed.path == "/catchup":
            # The last few replies of a session, for connect-time catch-up. Works
            # even for a session never listened to — the hook records every
            # session's replies into channels.json regardless of selection.
            session = urllib.parse.parse_qs(parsed.query).get("session", [""])[0]
            with state_lock():
                channels = read_json(CHANNELS, [])
            channel = next((c for c in channels if c.get("session_id") == session), None)
            lines = []
            for i, r in enumerate((channel or {}).get("recent", [])):
                lines.append({
                    "id": f"catchup-{session[:8]}-{i}-{int(r.get('at', 0))}",
                    "session_id": session,
                    "project": (channel or {}).get("project", ""),
                    "text": r.get("text", ""),
                    "role": "claude",
                    "catchup": True,
                    "spoken_at_epoch": r.get("at", 0),
                })
            self.json_response({"lines": lines}, conditional=False)
            return

        if parsed.path.endswith(".m3u8"):
            HB.touch()
        super().do_GET()

    def do_POST(self):
        path = urllib.parse.urlsplit(self.path).path
        if path == "/log":
            self.handle_log_upload()
            return
        if path != "/active":
            self.send_error(404)
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
        except ValueError:
            self.send_error(400, "Invalid Content-Length")
            return
        if not 0 < length <= 4096:
            self.send_error(413, "Body must be 1..4096 bytes")
            return
        try:
            requested = json.loads(self.rfile.read(length))
        except (json.JSONDecodeError, UnicodeDecodeError):
            self.send_error(400, "Invalid JSON")
            return

        mode = requested.get("mode")
        session_id = requested.get("session_id")
        if mode not in ("follow", "pinned"):
            self.send_error(400, "mode must be follow or pinned")
            return

        with state_lock():
            state = selection_state()
            if mode == "pinned":
                known = {
                    channel.get("session_id")
                    for channel in read_json(CHANNELS, [])
                }
                if not isinstance(session_id, str) or session_id not in known:
                    self.send_error(400, "Unknown session_id")
                    return
                state["mode"] = "pinned"
                state["session_id"] = session_id
            else:
                state["mode"] = "follow"
                state["session_id"] = state.get("follow_session_id")
            atomic_json(SELECTION, state)

        self.json_response(
            {"mode": state["mode"], "session_id": state.get("session_id")},
            conditional=False,
        )

    def handle_log_upload(self):
        """Phone POSTs its recent playback-log lines here so both sides of the
        pipeline land in one place on the Mac for automatic diagnosis."""
        try:
            length = int(self.headers.get("Content-Length", "0"))
        except ValueError:
            self.send_error(400)
            return
        if not 0 < length <= 262144:
            self.send_error(413, "Body must be 1..256KiB")
            return
        try:
            body = json.loads(self.rfile.read(length))
            lines = body.get("lines", [])
            device = body.get("device", "iphone")
        except (json.JSONDecodeError, UnicodeDecodeError, AttributeError):
            self.send_error(400, "Invalid JSON")
            return
        phone_log = SPEECH / "logs" / "phone.jsonl"
        phone_log.parent.mkdir(parents=True, exist_ok=True)
        with phone_log.open("a", encoding="utf-8") as f:
            for line in lines[:2000]:
                f.write(json.dumps({"device": device, "line": str(line)[:2000]},
                                   separators=(",", ":")) + "\n")
        ravenlog.log("phone", "log_upload", device=device, n=len(lines))
        self.json_response({"received": len(lines)}, conditional=False)

    def log_message(self, *_):
        pass


def main():
    http.server.ThreadingHTTPServer.allow_reuse_address = True
    http.server.ThreadingHTTPServer((TAILSCALE_IP, 8080), HuginnHandler).serve_forever()


if __name__ == "__main__":
    main()

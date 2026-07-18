#!/usr/bin/env python3
"""Serves the HLS stream + page. Binds the tailnet so the phone can reach it.

Two things here are load-bearing:
  - .m3u8 needs Cache-Control: no-store, or the player never sees new segments.
  - A playlist GET *is* the listener heartbeat; writer.sh gates the queue on it.
"""
import http.server
import pathlib
import socketserver

ROOT = pathlib.Path.home() / "speech" / "hls"
HB = ROOT / ".heartbeat"


class H(http.server.SimpleHTTPRequestHandler):
    def __init__(self, *a, **k):
        super().__init__(*a, directory=str(ROOT), **k)

    def guess_type(self, path):
        if path.endswith(".m3u8"):
            return "application/vnd.apple.mpegurl"
        if path.endswith(".ts"):
            return "video/mp2t"
        return super().guess_type(path)

    def end_headers(self):
        if self.path.endswith(".m3u8"):
            self.send_header("Cache-Control", "no-store")
        super().end_headers()

    def do_GET(self):
        if self.path.endswith(".m3u8"):
            HB.touch()
        super().do_GET()

    def log_message(self, *a):
        pass


socketserver.ThreadingTCPServer.allow_reuse_address = True
socketserver.ThreadingTCPServer(("0.0.0.0", 8080), H).serve_forever()

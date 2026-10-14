"""Minimal controller for the e2e run: serves /users from a state file."""

import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

STATE, PORT = sys.argv[1], int(sys.argv[2])


class Handler(BaseHTTPRequestHandler):
    def _send(self, status, body=b"{}"):
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path.startswith("/api/agent-server/v1/users/events"):
            # No event stream: the agent falls back to periodic sync.
            self._send(404)
        elif self.path.startswith("/api/agent-server/v1/users"):
            with open(STATE, "rb") as f:
                self._send(200, f.read())
        else:
            self._send(404)

    def do_POST(self):
        self.rfile.read(int(self.headers.get("Content-Length") or 0))
        self._send(200 if self.path.startswith("/api/agent-server/v1/status") else 404)

    def log_message(self, *args):
        pass


ThreadingHTTPServer(("127.0.0.1", PORT), Handler).serve_forever()

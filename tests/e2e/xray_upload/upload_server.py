"""Upload target behind the tunnel: returns the size and SHA-256 it received."""

import hashlib
import json
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(sys.argv[1])


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        remaining = int(self.headers.get("Content-Length") or 0)
        digest, size = hashlib.sha256(), 0
        while remaining:
            chunk = self.rfile.read(min(remaining, 1 << 16))
            if not chunk:
                break
            digest.update(chunk)
            size += len(chunk)
            remaining -= len(chunk)
        body = json.dumps({"size": size, "sha256": digest.hexdigest()}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


ThreadingHTTPServer(("127.0.0.1", PORT), Handler).serve_forever()

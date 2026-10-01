#!/usr/bin/env python3
"""Notes HTTP API, reachable only inside the session."""
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import db
import psycopg


class Handler(BaseHTTPRequestHandler):
    def reply(self, status, payload):
        data = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        try:
            if self.path == "/health":
                with db.connect() as conn:
                    db.validate_schema(conn)
                self.reply(200, {"status": "ready", "database": "ready"})
            elif self.path == "/notes":
                self.reply(200, {"notes": db.list_notes()})
            else:
                self.reply(404, {"error": "unknown path"})
        except db.SchemaError as exc:
            self.reply(503, {"error": str(exc)})
        except psycopg.Error:
            self.reply(503, {"error": "database unavailable"})

    def do_POST(self):
        if self.path != "/notes":
            self.reply(404, {"error": "unknown path"})
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
            if length <= 0 or length > 65536:
                raise ValueError("request body must be between 1 and 65536 bytes")
            payload = json.loads(self.rfile.read(length))
            if not isinstance(payload, dict):
                raise ValueError("request body must be a JSON object")
            note = db.create_note(payload.get("body"))
            self.reply(201, note)
            print(f"accepted note={note['id']} with durable queued job", flush=True)
        except (ValueError, UnicodeDecodeError) as exc:
            self.reply(400, {"error": str(exc)})
        except psycopg.Error:
            self.reply(503, {"error": "database unavailable; write outcome uncertain, inspect notes before retrying"})


if __name__ == "__main__":
    db.wait_ready()
    with ThreadingHTTPServer(("127.0.0.1", 8000), Handler) as server:
        print("notes web listening on 127.0.0.1:8000", flush=True)
        server.serve_forever()

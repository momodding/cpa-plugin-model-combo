#!/usr/bin/env python3
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

MODELS = ["dashboard-e2e-model-a", "dashboard-e2e-model-b"]


class Handler(BaseHTTPRequestHandler):
    def respond(self, status, body):
        encoded = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

    def authorized(self):
        return self.headers.get("Authorization") == "Bearer fake-provider-key"

    def do_GET(self):
        if self.path == "/health":
            self.respond(200, {"status": "ok"})
        elif not self.authorized():
            self.respond(401, {"error": {"message": "unauthorized"}})
        elif self.path == "/v1/models":
            self.respond(200, {"object": "list", "data": [{"id": model, "object": "model"} for model in MODELS]})
        else:
            self.respond(404, {"error": {"message": "not found"}})

    def stream(self, model):
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.end_headers()
        for choice in (
            {"id": "chatcmpl-fake", "object": "chat.completion.chunk", "created": 0, "model": model, "choices": [{"index": 0, "delta": {"role": "assistant", "content": "fake response"}, "finish_reason": None}]},
            {"id": "chatcmpl-fake", "object": "chat.completion.chunk", "created": 0, "model": model, "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}]},
        ):
            self.wfile.write(f"data: {json.dumps(choice)}\n\n".encode())
            self.wfile.flush()
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()

    def do_POST(self):
        if not self.authorized():
            self.respond(401, {"error": {"message": "unauthorized"}})
            return
        if self.path != "/v1/chat/completions":
            self.respond(404, {"error": {"message": "not found"}})
            return
        length = int(self.headers.get("Content-Length", "0"))
        request = json.loads(self.rfile.read(length) or b"{}")
        model = request.get("model", MODELS[0])
        if request.get("stream") is True:
            self.stream(model)
            return
        self.respond(200, {
            "id": "chatcmpl-fake",
            "object": "chat.completion",
            "created": 0,
            "model": model,
            "choices": [{"index": 0, "message": {"role": "assistant", "content": "fake response"}, "finish_reason": "stop"}],
            "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
        })

    def log_message(self, format, *args):
        pass


ThreadingHTTPServer(("0.0.0.0", 19080), Handler).serve_forever()

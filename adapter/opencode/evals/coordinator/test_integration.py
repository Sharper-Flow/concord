"""Opt-in sandbox integration with a synthetic loopback provider."""
import json
import os
import tempfile
import threading
import unittest
from argparse import Namespace
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import run
from test_production_parity import plugin_tool_module


LOOPBACK = ".".join(("127", "0", "0", "1"))


@unittest.skipUnless(os.environ.get("CONCORD_EVAL_OPENCODE_BIN"), "set CONCORD_EVAL_OPENCODE_BIN to the pinned binary")
class CoordinatorIntegrationTest(unittest.TestCase):
    def test_recording_double_and_export(self):
        class Provider(BaseHTTPRequestHandler):
            hits = 0

            def log_message(self, *args):
                pass

            def do_POST(self):
                json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                Provider.hits += 1
                first = Provider.hits == 1
                delta = ({"role": "assistant", "tool_calls": [{"index": 0, "id": "call_fixture", "type": "function",
                          "function": {"name": "runtime_status", "arguments": "{}"}}]} if first else
                         {"role": "assistant", "content": '{"status":"unresolved"}'})
                chunks = [{"id": "fixture", "object": "chat.completion.chunk", "model": "synthetic",
                           "choices": [{"index": 0, "delta": delta, "finish_reason": None}]},
                          {"id": "fixture", "object": "chat.completion.chunk", "model": "synthetic",
                           "choices": [{"index": 0, "delta": {}, "finish_reason": "tool_calls" if first else "stop"}],
                           "usage": {"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18}}]
                data = ("".join("data: " + json.dumps(c) + "\n\n" for c in chunks) + "data: [DONE]\n\n").encode()
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

        server = ThreadingHTTPServer((LOOPBACK, 0), Provider)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(thread.join)
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        with tempfile.TemporaryDirectory(prefix="coordinator-integration-") as directory:
            root = Path(directory)
            profile = {"model": "synthetic/synthetic", "plugins": [], "ro_binds": [], "env": {},
                       "providers": {"synthetic": {"name": "Synthetic", "package": "@opencode/ai/providers/openai-compatible",
                           "settings": {"baseURL": f"http://{LOOPBACK}:{server.server_port}/v1", "apiKey": "synthetic"},
                           "models": {"synthetic": {"name": "Synthetic", "limit": {"context": 10000, "input": 9000, "output": 1000}}}}}}
            args = Namespace(repo=Path(__file__).resolve().parents[4], sdk_tool=plugin_tool_module(),
                             model=profile["model"], artifacts_dir=root, profile=profile,
                             binary=os.environ["CONCORD_EVAL_OPENCODE_BIN"], profile_digest=run.sandbox.profile_digest(profile))
            result = run.run_case(args, "native-route-discovery", "Use the available tools.", {"rules.md": b"Synthetic rules."})
            artifact = Path(result["artifact_dir"])
            self.assertEqual(result["exit_code"], 0, (artifact / "events.stderr").read_text())
            self.assertGreaterEqual(Provider.hits, 2)
            calls = [json.loads(line) for line in (artifact / "calls.jsonl").read_text().splitlines()]
            self.assertEqual(calls[0]["tool"], "runtime_status")
            self.assertTrue(result["identity_verified"], result)
            self.assertNotIn("artifact_error", result)
            self.assertEqual(json.loads((artifact / "result.json").read_text()), result)

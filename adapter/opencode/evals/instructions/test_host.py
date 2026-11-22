"""Offline protocol tests. The stand-in never invokes a model provider."""

import json
import os
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from . import host


V2_STUB = r'''#!/usr/bin/env python3
import json, os, sys, threading, time, urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
if sys.argv[1:] == ["--version"]:
    print("opencode v2.3.2")
    sys.exit(0)
root = Path(os.environ["HOME"]).parent
messages = []
def bridge(path, value):
    request = urllib.request.Request(os.environ["HARNESS_BRIDGE"] + path,
        data=json.dumps(value).encode(), headers={"Content-Type": "application/json",
        "Authorization": "Bearer " + os.environ["HARNESS_TOKEN"]})
    with urllib.request.urlopen(request) as response:
        return json.load(response)
def reserve(kind):
    if os.environ.get("HARNESS_GUARDED") == "1":
        bridge("/reserve", {"sessionID": "synthetic", "agent": "instruction-probe",
               "model": {"providerID": "provider", "id": "model"}, "kind": kind,
               "request": {}, "limits": {"context": 10000, "input": 9000, "output": 1000}})
def settle(message):
    if os.environ.get("HARNESS_GUARDED") == "1":
        time.sleep(.4)
        bridge("/step", {"type": "session.step.ended", "sessionID": "synthetic",
                         "assistantMessageID": message["id"], "finish": message["finish"], "tokens": message["tokens"]})
class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args): pass
    def send(self, value):
        data = json.dumps(value).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)
    def record(self, body=None):
        with (root / "v2-requests.jsonl").open("a") as log:
            log.write(json.dumps({"method": self.command, "path": self.path,
                                  "body": body, "authorization": self.headers.get("Authorization", "")}) + "\n")
    def do_GET(self):
        self.record()
        if self.path == "/api/info":
            self.send({"version": "2.3.2"})
        elif self.path.startswith("/api/session/synthetic/message"):
            self.send({"data": messages, "cursor": None})
        else:
            self.send_error(404)
    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        body = json.loads(self.rfile.read(length) or b"{}")
        self.record(body)
        if self.path == "/api/session":
            self.send({"data": {"id": "synthetic"}})
        elif self.path == "/api/session/synthetic/prompt":
            if os.environ.get("SYNTHETIC_HANG") == "1":
                time.sleep(20)
            reserve("primary")
            messages.append({"id": "user-" + str(len(messages)), "type": "user", "text": body["text"]})
            bridge("/capture", {"kind": "system", "output": {"system": ["synthetic"]}})
            bridge("/capture", {"kind": "messages", "output": {"messages": messages}})
            bridge("/fixture", {"action": "read_work", "arguments": {}})
            message = {"id": "assistant-" + str(len(messages)), "type": "assistant",
                       "agent": "instruction-probe", "model": {"providerID": "provider", "id": "model"},
                       "time": {"created": 1, "completed": 2}, "finish": "stop",
                       "tokens": {"input": 10, "output": 2, "reasoning": 0, "cache": {"read": 0, "write": 0}},
                       "content": [{"type": "text", "text": "done"}]}
            messages.append(message)
            threading.Thread(target=settle, args=(message,), daemon=True).start()
            self.send({"data": message})
        elif self.path == "/api/experimental/session/synthetic/wait":
            self.send({"data": {}})
        elif self.path == "/api/session/synthetic/compact":
            reserve("compaction")
            messages.append({"id": "compact", "type": "compaction", "status": "completed", "reason": "manual",
                             "summary": "synthetic summary", "model": {"providerID": "provider", "id": "model"},
                             "time": {"created": 3}, "tokens": {"input": 10, "output": 2, "reasoning": 0,
                             "cache": {"read": 0, "write": 0}}})
            self.send({"data": {}})
        else:
            self.send_error(404)
hostname = sys.argv[sys.argv.index("--hostname") + 1]
port = int(sys.argv[sys.argv.index("--port") + 1])
ThreadingHTTPServer((hostname, port), Handler).serve_forever()
'''


class V2HostTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name)
        self.binary = self.base / "opencode"
        self.binary.write_text(V2_STUB, encoding="utf-8")
        self.binary.chmod(0o700)
        self.profile = self.base / "provider.json"
        self.profile.write_text(json.dumps({"plugins": [], "ro_binds": [], "env": {}, "model": "provider/model"}))
        self.environment = patch.dict(os.environ, {"PATH": str(self.base) + os.pathsep + os.environ["PATH"],
                                                    "CONCORD_EVAL_OPENCODE_BIN": str(self.binary)})
        self.environment.start()
        self.addCleanup(self.environment.stop)
        self.snapshot = {"role": "role body", "instructions": ["rule"], "catalog": "catalog", "provenance": {}}

    def test_v2_routes_config_and_wait_for_settlement(self):
        class Guard:
            def __init__(self):
                self.open_kind = None
                self.reservations = []
                self.settlements = []
                self.failed = False
            def reserve(self, event):
                if self.open_kind is not None:
                    raise ValueError("previous group not settled")
                self.open_kind = event["kind"]
                self.reservations.append(event)
                return len(self.reservations)
            def settle(self, message):
                if self.open_kind is None:
                    raise ValueError("settlement without reservation")
                self.settlements.append((self.open_kind, message))
                self.open_kind = None
            def verify(self):
                if self.open_kind is not None or self.failed:
                    raise ValueError("unsettled")

        guard = Guard()
        root = self.base / "run"
        result = host.run_session(root, self.snapshot, "provider/model", "S19", guard=guard,
                                  provider_profile=self.profile, timeout_seconds=30)
        requests = [json.loads(line) for line in (root / "v2-requests.jsonl").read_text().splitlines()]
        paths = [r["path"].split("?", 1)[0] for r in requests]
        self.assertIn("/api/info", paths)
        self.assertEqual(paths.count("/api/session"), 1)
        self.assertEqual(paths.count("/api/session/synthetic/prompt"), 2)
        self.assertGreaterEqual(paths.count("/api/experimental/session/synthetic/wait"), 2)
        self.assertGreaterEqual(paths.count("/api/session/synthetic/message"), 2)
        self.assertEqual(paths.count("/api/session/synthetic/compact"), 1)
        self.assertTrue(all(r["authorization"].startswith("Basic ") for r in requests))
        self.assertEqual([r["kind"] for r in guard.reservations], ["primary", "compaction", "primary"])
        self.assertEqual([kind for kind, _ in guard.settlements], ["primary", "compaction", "primary"])
        self.assertEqual(guard.settlements[1][1]["id"], "compact")
        guard.verify()
        self.assertTrue(result["compacted"])
        self.assertEqual(result["final_text"], "done")
        self.assertEqual([e.get("action", e.get("boundary")) for e in result["events"]], ["read_work", "compaction", "read_work"])
        self.assertTrue(all(Path(p).is_file() for p in result["capture_paths"]))
        self.assertEqual({json.loads(Path(p).read_text())["phase"] for p in result["capture_paths"]}, {"setup", "continuation"})
        self.assertTrue((root / "host-stderr.log").exists())
        state = json.loads((root / "failure-state.json").read_text())
        self.assertEqual(state["events"], result["events"])
        self.assertIn("files", state["artifacts"])
        config = json.loads((root / "config" / "opencode" / "opencode.json").read_text())
        self.assertEqual(config["agents"]["instruction-probe"]["system"], "role body")
        self.assertTrue(config["agents"]["title"]["disabled"])
        self.assertFalse(config["compaction"]["auto"])
        self.assertIsInstance(config["permissions"], list)
        self.assertTrue(config["plugins"])
        plugin = Path(config["plugins"][0])
        self.assertTrue(plugin.is_dir())
        source = "\n".join(path.read_text() for path in plugin.rglob("*.js"))
        self.assertIn('"http.request"', source)
        self.assertIn('"experimental.ws.handshake"', source)
        self.assertIn('"context"', source)

    def test_v2_wrong_version_refused_before_server_start(self):
        self.binary.write_text(V2_STUB.replace("opencode v2.3.2", "opencode v2.3.1"))
        root = self.base / "wrong-version"
        with self.assertRaises((ValueError, RuntimeError)):
            host.run_session(root, self.snapshot, "provider/model", "S18", provider_profile=self.profile)
        self.assertFalse((root / "v2-requests.jsonl").exists())


class HostTest(V2HostTest):
    def test_deadline_stops_server(self):
        root = self.base / "timeout"
        self.profile.write_text(json.dumps({"plugins": [], "ro_binds": [], "env": {"SYNTHETIC_HANG": "1"}, "model": "provider/model"}))
        processes = []
        real_popen = host.subprocess.Popen
        def start(*args, **kwargs):
            process = real_popen(*args, **kwargs)
            processes.append(process)
            return process
        with patch.object(host.subprocess, "Popen", side_effect=start):
            with self.assertRaises((TimeoutError, OSError)):
                host.run_session(root, self.snapshot, "provider/model", "S18", timeout_seconds=2, provider_profile=self.profile)
        self.assertGreaterEqual(len(processes), 2)
        self.assertTrue(all(process.poll() is not None for process in processes))
        state = json.loads((root / "failure-state.json").read_text())
        self.assertEqual(state["events"], [])
        self.assertIn("files", state["artifacts"])

    def test_call_bound_refuses_extra_capture(self):
        root = self.base / "limit"
        with self.assertRaises((OSError, ValueError)):
            host.run_session(root, self.snapshot, "provider/model", "S19", max_calls=1, provider_profile=self.profile)
        self.assertEqual(len(list((root / "captures").glob("*-messages.json"))), 1)
        state = json.loads((root / "failure-state.json").read_text())
        self.assertEqual([e.get("action", e.get("boundary")) for e in state["events"]], ["read_work", "compaction"])
        self.assertTrue(state["messages"])

    def test_v2_compaction_requires_completed_message_and_tokens(self):
        messages = [{"id": "user", "type": "user", "text": "hello"},
                    {"id": "compact", "type": "compaction", "status": "completed", "reason": "manual",
                     "model": {"providerID": "provider", "id": "model"},
                     "summary": "kept", "tokens": {"input": 10, "output": 2,
                     "reasoning": 0, "cache": {"read": 0, "write": 0}}}]
        self.assertEqual(host._compaction(messages), 1)
        messages[1]["status"] = "running"
        self.assertIsNone(host._compaction(messages))

    def test_v2_final_text_comes_from_inline_content(self):
        messages = [{"id": "assistant", "type": "assistant", "content": [{"type": "text", "text": "done"}]}]
        self.assertEqual(host._final_text(messages), "done")


if __name__ == "__main__":
    unittest.main()

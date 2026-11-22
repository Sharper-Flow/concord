"""Offline protocol tests. The stand-in never invokes a model provider."""

import json
import hashlib
import os
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import patch

from . import budget, host, pilot


STUB = r'''#!/usr/bin/env python3
import json, os, sys, threading, time, urllib.request
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
if sys.argv[1:] == ["--version"]:
    print("1.18.35")
    sys.exit(0)
root = Path(os.environ["HOME"]).parent
(root / "stub-pid").write_text(str(os.getpid()))
(root / "stub-env.json").write_text(json.dumps({k: os.environ.get(k) for k in [
    "HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME",
    "OPENCODE_DISABLE_DEFAULT_PLUGINS", "OPENCODE_DISABLE_EXTERNAL_SKILLS", "OPENCODE_DISABLE_CLAUDE_CODE_SKILLS",
    "OPENCODE_DISABLE_PROJECT_CONFIG", "OPENCODE_PURE"]}))
messages = []
def bridge(path, value):
    request = urllib.request.Request(os.environ["HARNESS_BRIDGE"] + path,
        data=json.dumps(value).encode(), headers={"Content-Type": "application/json",
        "Authorization": "Bearer " + os.environ["HARNESS_TOKEN"]})
    with urllib.request.urlopen(request) as response:
        return json.load(response)
def capture():
    for kind in ("system", "messages"):
        bridge("/capture", {"kind": kind, "input": {}, "output": {"messages": messages}})
    if os.environ.get("HARNESS_GUARDED") == "1":
        bridge("/reserve", {"synthetic": "request"})
def assistant():
    info = {"role": "assistant"}
    if os.environ.get("HARNESS_GUARDED") == "1":
        # OpenCode delivers usage through an unawaited event hook, after the prompt can return.
        info.update(id="assistant-" + str(len(messages)), time={"completed": 1})
        threading.Thread(target=lambda value=dict(info): (time.sleep(0.5), bridge("/usage", value))).start()
    return info
class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args): pass
    def send(self, value):
        data = json.dumps(value).encode()
        self.send_response(200)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)
    def do_GET(self):
        self.send({"version": "1.18.35"} if self.path == "/global/health" else messages)
    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        with (root / "stub-requests.jsonl").open("a") as log:
            log.write(json.dumps([self.path, body]) + "\n")
        if self.path == "/session":
            self.send({"id": "synthetic"})
        elif self.path.endswith("/summarize"):
            messages.append({"info": {"role": "user", "id": "compact-user"}, "parts": [{"type": "compaction", "auto": False}]})
            messages.append({"info": {"role": "assistant", "summary": True, "parentID": "compact-user", "finish": "stop"}, "parts": [{"type": "text", "text": "summary"}]})
            self.send(True)
        else:
            if (root.parent / "hang").exists():
                time.sleep(20)
            messages.append({"info": {"role": "user"}, "parts": body["parts"]})
            capture()
            bridge("/fixture", {"action": "read_work", "arguments": {}})
            messages.append({"info": assistant(), "parts": [{"type": "text", "text": "done"}]})
            self.send(messages[-1])
hostname = sys.argv[sys.argv.index("--hostname") + 1]
port = int(sys.argv[sys.argv.index("--port") + 1])
HTTPServer((hostname, port), Handler).serve_forever()
'''


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
        bridge("/usage", message)
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


class HostTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name)
        binary = self.base / "opencode"
        binary.write_text(STUB, encoding="utf-8")
        binary.chmod(0o700)
        self.env = patch.dict(os.environ, {"PATH": str(self.base) + os.pathsep + os.environ["PATH"],
                                          "OPENCODE_PURE": "1", "XDG_DATA_HOME": str(self.base / "data")})
        self.env.start()
        self.addCleanup(self.env.stop)
        self.snapshot = {"role": "role body", "instructions": ["rule"], "catalog": "catalog", "provenance": {"id": "test"}}

    def test_hold_protocol_and_isolation(self):
        root = self.base / "run"
        result = host.run_session(root, self.snapshot, "provider/model", "S19")
        requests = [json.loads(line) for line in (root / "stub-requests.jsonl").read_text().splitlines()]
        self.assertEqual([path for path, _ in requests], ["/session", "/session/synthetic/message", "/session/synthetic/summarize", "/session/synthetic/message"])
        self.assertEqual(requests[0][1]["title"], "Instruction fixture S19")
        self.assertEqual(requests[2][1], {"providerID": "provider", "modelID": "model", "auto": False})
        self.assertEqual(requests[1][1]["agent"], "instruction-probe")
        self.assertNotIn("system", requests[1][1])
        self.assertEqual(result["final_text"], "done")
        self.assertTrue(result["compacted"])
        self.assertEqual(result["messages"][result["compaction_index"]]["parts"][0]["type"], "compaction")
        self.assertEqual([e.get("action", e.get("boundary")) for e in result["events"]], ["read_work", "compaction", "read_work"])
        self.assertEqual(result["capture_stage"], "pre-provider-serialization")
        self.assertTrue(all(Path(p).is_file() for p in result["capture_paths"]))
        self.assertEqual({json.loads(Path(p).read_text())["phase"] for p in result["capture_paths"]}, {"setup", "continuation"})
        env = json.loads((root / "stub-env.json").read_text())
        self.assertEqual(env["HOME"], str(root / "home"))
        self.assertEqual(env["XDG_DATA_HOME"], str(self.base / "data"))
        # OpenCode's internal Codex plugin owns the existing OpenAI OAuth flow.
        self.assertIsNone(env["OPENCODE_DISABLE_DEFAULT_PLUGINS"])
        self.assertEqual(env["OPENCODE_DISABLE_EXTERNAL_SKILLS"], "1")
        self.assertEqual(env["OPENCODE_DISABLE_CLAUDE_CODE_SKILLS"], "1")
        self.assertEqual(env["OPENCODE_DISABLE_PROJECT_CONFIG"], "1")
        self.assertIsNone(env["OPENCODE_PURE"])
        config = json.loads((root / "config" / "opencode" / "opencode.json").read_text())
        self.assertEqual(config["$schema"], "https://opencode.ai/config.json")
        self.assertEqual(config["agent"]["instruction-probe"]["prompt"], "role body")
        sources = [Path(p).read_text() for p in config["instructions"]]
        self.assertEqual(sources, ["rule", "catalog"])
        self.assertEqual(result["identity"]["config_sha256"], hashlib.sha256((root / "config" / "opencode" / "opencode.json").read_bytes()).hexdigest())
        self.assertEqual(result["identity"]["instruction_sha256"], [hashlib.sha256(s.encode()).hexdigest() for s in sources])
        self.assertEqual(config["agent"]["instruction-probe"]["mode"], "primary")
        self.assertTrue(config["agent"]["build"]["disable"])
        self.assertEqual(config["compaction"], {"auto": False, "prune": False})
        self.assertEqual(config["permission"], {"*": "deny", "fixture": "allow"})
        plugin = (root / "config" / "opencode" / "capture-plugin.js").read_text()
        self.assertIn('import { tool } from "@opencode-ai/plugin"', plugin)
        self.assertIn('fixture: tool({', plugin)
        self.assertIn('action: tool.schema.string()', plugin)
        self.assertIn('arguments: tool.schema.record(tool.schema.string(), tool.schema.unknown())', plugin)
        self.assertTrue((root / "host-stderr.log").exists())
        with self.assertRaises(ProcessLookupError):
            os.kill(int((root / "stub-pid").read_text()), 0)
        state = json.loads((root / "failure-state.json").read_text())
        self.assertEqual(state["events"], result["events"])
        self.assertIn("files", state["artifacts"])

    def test_deadline_stops_server(self):
        root = self.base / "timeout"
        (self.base / "hang").touch()
        with self.assertRaises((TimeoutError, OSError)):
            host.run_session(root, self.snapshot, "provider/model", "S18", timeout_seconds=2)
        with self.assertRaises(ProcessLookupError):
            os.kill(int((root / "stub-pid").read_text()), 0)
        state = json.loads((root / "failure-state.json").read_text())
        self.assertEqual(state["events"], [])
        self.assertIn("files", state["artifacts"])

    def test_call_bound_refuses_extra_capture(self):
        root = self.base / "limit"
        with self.assertRaises((OSError, ValueError)):
            host.run_session(root, self.snapshot, "provider/model", "S19", max_calls=1)
        self.assertEqual(len(list((root / "captures").glob("*-messages.json"))), 1)
        state = json.loads((root / "failure-state.json").read_text())
        self.assertEqual([e.get("action", e.get("boundary")) for e in state["events"]], ["read_work", "compaction"])
        self.assertTrue(state["messages"])

    def test_guarded_session_waits_for_late_usage(self):
        class Guard:
            def __init__(self): self.events = []
            def reserve(self, value):
                self.events.append(("reserve", value))
                return len(self.events)
            def settle(self, value): self.events.append(("usage", value))
        guard = Guard()
        result = host.run_session(self.base / "guarded", self.snapshot, "provider/model", "S19", guard=guard)
        self.assertEqual(sorted(kind for kind, _ in guard.events), ["reserve", "reserve", "usage", "usage"])
        completed = [m["info"]["id"] for m in result["messages"] if m["info"].get("time")]
        self.assertEqual(len(completed), 2)
        self.assertEqual(sorted(value["id"] for kind, value in guard.events if kind == "usage"), sorted(completed))

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


@unittest.skipUnless(os.environ.get("CONCORD_NATIVE_PILOT_TEST") == "1", "opt-in real OpenCode loopback test")
class NativeGuardTest(unittest.TestCase):
    def test_guarded_retry_and_compaction(self):
        class Provider(BaseHTTPRequestHandler):
            calls = 0
            def log_message(self, *args): pass
            def do_POST(self):
                self.rfile.read(int(self.headers["Content-Length"]))
                Provider.calls += 1
                if Provider.calls == 1:
                    self.send_response(429)
                    self.send_header("Retry-After", "0")
                    self.end_headers()
                    self.wfile.write(b'{"error":{"message":"rate limited","type":"rate_limit_error"}}')
                    return
                delta = {"content": "synthetic response"}
                finish = "stop"
                if Provider.calls == 2:
                    # A tool step makes OpenCode start a second assistant message for the same user message.
                    delta = {"tool_calls": [{"index": 0, "id": "call_synthetic", "type": "function", "function": {
                        "name": "fixture", "arguments": json.dumps({"action": "read_work", "arguments": {}})}}]}
                    finish = "tool_calls"
                chunk = {"id": "synthetic", "object": "chat.completion.chunk", "created": 1, "model": "synthetic",
                         "choices": [{"index": 0, "delta": delta, "finish_reason": None}]}
                end = {"id": "synthetic", "object": "chat.completion.chunk", "created": 1, "model": "synthetic",
                       "choices": [{"index": 0, "delta": {}, "finish_reason": finish}],
                       "usage": {"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18}}
                data = ("data: " + json.dumps(chunk) + "\n\ndata: " + json.dumps(end) + "\n\ndata: [DONE]\n\n").encode()
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

        server = ThreadingHTTPServer((host.LOOPBACK, 0), Provider)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(thread.join)
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            limits = {"context": 10000, "input": 9000, "output": 1000}
            rates = {"input": 2, "output": 10, "cache_read": 0.1, "cache_write": 2.5}
            journal = budget.Journal(root)
            journal.create()
            journal.update(lambda data: data["attempts"].append({"id": "attempt", "status": "pending"}))
            guard = pilot.Guard(journal, "attempt", "fake/synthetic", limits, rates)
            original = host._json_bytes
            def configured(value):
                if isinstance(value, dict) and value.get("default_agent") == "instruction-probe":
                    value = {**value, "enabled_providers": ["fake"], "provider": {"fake": {
                        "npm": "@ai-sdk/openai-compatible", "name": "Fake",
                        "options": {"baseURL": f"http://{host.LOOPBACK}:{server.server_port}/v1", "apiKey": "synthetic"},
                        "models": {"synthetic": {"name": "Synthetic", "limit": limits}}}}}
                return original(value)
            snapshot = {"role": "synthetic", "instructions": [], "catalog": "", "provenance": {}}
            with patch.object(host, "_json_bytes", side_effect=configured), patch.dict(os.environ, {"XDG_DATA_HOME": str(root / "data")}):
                output = host.run_session(root / "session", snapshot, "fake/synthetic", "S19", timeout_seconds=600, guard=guard)
            guard.verify()
            self.assertTrue(output["compacted"])
            self.assertEqual([e.get("action", e.get("boundary")) for e in output["events"]], ["read_work", "compaction"])
            requests = journal.update(lambda data: data["requests"])
            self.assertEqual(Provider.calls, 5)
            self.assertEqual([r["status"] for r in requests], ["reserved", "settled", "settled", "settled", "settled"])
            self.assertEqual(requests[0]["assistant_id"], requests[1]["assistant_id"])
            self.assertEqual(requests[1]["user_id"], requests[2]["user_id"])
            self.assertNotEqual(requests[1]["assistant_id"], requests[2]["assistant_id"])
            for _ in range(284):
                journal.reserve("attempt", "other", "user", "session", budget.reservation(limits, rates))
            blocked = pilot.Guard(journal, "attempt", "fake/synthetic", limits, rates)
            with patch.object(host, "_json_bytes", side_effect=configured), patch.dict(os.environ, {"XDG_DATA_HOME": str(root / "data")}):
                try:
                    host.run_session(root / "blocked", snapshot, "fake/synthetic", "S18", timeout_seconds=600, guard=blocked)
                except (OSError, RuntimeError, ValueError):
                    pass
            self.assertTrue(blocked.failed)
            self.assertEqual(Provider.calls, 5)


if __name__ == "__main__":
    unittest.main()

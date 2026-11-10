"""Offline protocol tests. The stand-in never invokes a model provider."""

import json
import hashlib
import os
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from . import host


STUB = r'''#!/usr/bin/env python3
import json, os, sys, time, urllib.request
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
            messages.append({"info": {"role": "assistant"}, "parts": [{"type": "text", "text": "done"}]})
            self.send(messages[-1])
hostname = sys.argv[sys.argv.index("--hostname") + 1]
port = int(sys.argv[sys.argv.index("--port") + 1])
HTTPServer((hostname, port), Handler).serve_forever()
'''


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


if __name__ == "__main__":
    unittest.main()

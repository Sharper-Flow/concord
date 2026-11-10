"""Isolated OpenCode HTTP runner for the synthetic instruction fixtures.

Captures are hook output before provider serialization, not provider wire data.
The caller must put the run directory outside the repository and protect it as
potentially sensitive: captures and the reused OpenCode data store contain text.
"""

import hashlib
import base64
import json
import os
import re
import secrets
import signal
import socket
import subprocess
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

from .cases import ACTIONS, CASES, Fixture


VERSION = "1.18.35"
LOOPBACK = ".".join(("127", "0", "0", "1"))
STAGE = "pre-provider-serialization"
TOOL_SCHEMA = {"type": "object", "properties": {"action": {"type": "string"},
               "arguments": {"type": "object", "additionalProperties": {}}},
               "required": ["action", "arguments"], "additionalProperties": False}
PLUGIN = r'''import { tool } from "@opencode-ai/plugin";
export default async function () {
  const endpoint = process.env.HARNESS_BRIDGE;
  const token = process.env.HARNESS_TOKEN;
  async function send(path, value) {
    const response = await fetch(endpoint + path, {
      method: "POST", headers: {"content-type": "application/json", "authorization": "Bearer " + token},
      body: JSON.stringify(value)
    });
    if (!response.ok) throw new Error("harness bridge refused " + path + ": " + response.status);
    return await response.json();
  }
  return {
    tool: {fixture: tool({
      description: "Use the synthetic fixture. Actions: " + process.env.HARNESS_ACTIONS,
      args: {action: tool.schema.string(), arguments: tool.schema.record(tool.schema.string(), tool.schema.unknown())},
      execute: async (args) => JSON.stringify(await send("/fixture", args))
    })},
    "experimental.chat.system.transform": async (input, output) => {
      await send("/capture", {kind: "system", output});
    },
    "experimental.chat.messages.transform": async (input, output) => {
      await send("/capture", {kind: "messages", output});
    },
    "experimental.compaction.autocontinue": async (input, output) => { output.enabled = false; }
  };
}'''


def _digest(value):
    return hashlib.sha256(value).hexdigest()


def _json_bytes(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()


def _stderr_copy(pipe, path, sensitive_values):
    """Keep diagnostics private and drop lines likely to contain credentials."""
    sensitive = re.compile(r"authorization|bearer|cookie|header|password|secret|token|api.?key|credential", re.I)
    with path.open("w", encoding="utf-8") as output:
        os.chmod(path, 0o600)
        for line in iter(pipe.readline, b""):
            text = line.decode("utf-8", "replace")
            output.write("[sensitive diagnostic omitted]\n" if sensitive.search(text) or any(value in text for value in sensitive_values) else text)
            output.flush()
    pipe.close()


def _request(url, *, payload=None, timeout, password=None):
    data = None if payload is None else _json_bytes(payload)
    headers = {"Content-Type": "application/json"}
    if password:
        headers["Authorization"] = "Basic " + base64.b64encode(("opencode:" + password).encode()).decode()
    req = urllib.request.Request(url, data=data, headers=headers)
    with urllib.request.urlopen(req, timeout=timeout) as response:
        return json.load(response)


def _remaining(deadline):
    left = deadline - time.monotonic()
    if left <= 0:
        raise TimeoutError("OpenCode session deadline exceeded")
    return left


def _stop_group(process):
    if process is None:
        return
    try:
        os.killpg(process.pid, signal.SIGTERM)
    except ProcessLookupError:
        pass
    try:
        process.wait(timeout=2)
    except subprocess.TimeoutExpired:
        pass
    # Kill remaining descendants even when the group leader already exited.
    try:
        os.killpg(process.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    process.wait()


def _compaction(messages):
    for index, message in enumerate(messages):
        if any(part.get("type") == "compaction" for part in message.get("parts", [])):
            for summary in messages[index + 1:]:
                info = summary.get("info", {})
                if (info.get("role") == "assistant" and info.get("summary") is True
                        and info.get("parentID") == message.get("info", {}).get("id")
                        and info.get("finish") and not info.get("error")):
                    if any(p.get("type") == "text" and p.get("text", "").strip() for p in summary.get("parts", [])):
                        return index
            break
    return None


def _final_text(messages):
    for message in reversed(messages):
        info = message.get("info", {})
        if info.get("role") == "assistant" and not info.get("summary"):
            return "\n".join(p.get("text", "") for p in message.get("parts", []) if p.get("type") == "text")
    return ""


def run_session(root: Path, snapshot: dict, model: str, case_id: str, *,
                timeout_seconds: int = 180, max_steps: int = 6, max_calls: int = 12) -> dict:
    """Run one fixture. Errors fail closed; partial captures remain in root.

    max_calls is enforced conservatively at the messages transform hook. Host
    retries and other calls which do not enter that hook are not observable here.
    """
    if any(type(v) is not int or v < 1 for v in (timeout_seconds, max_steps, max_calls)):
        raise ValueError("timeout_seconds, max_steps and max_calls must be positive integers")
    if not isinstance(model, str) or "/" not in model:
        raise ValueError("model must be providerID/modelID")
    provider, model_id = model.split("/", 1)
    if not provider or not model_id:
        raise ValueError("model must be providerID/modelID")
    case = CASES[case_id]
    if not isinstance(snapshot.get("role"), str) or not isinstance(snapshot.get("catalog"), str) or not isinstance(snapshot.get("instructions"), list) or not all(isinstance(x, str) for x in snapshot["instructions"]):
        raise ValueError("invalid snapshot")
    start = time.monotonic()
    deadline = start + timeout_seconds
    root = Path(root).resolve()
    root.mkdir(parents=True, exist_ok=True)
    if any(root.iterdir()):
        raise ValueError("run root must be empty")
    root.chmod(0o700)
    fixture = Fixture(case_id, root / "repo")
    for name in ("home", "config", "cache", "state", "captures"):
        (root / name).mkdir(mode=0o700)
    config_dir = root / "config" / "opencode"
    config_dir.mkdir(mode=0o700)
    plugin_path = config_dir / "capture-plugin.js"
    plugin_path.write_text(PLUGIN, encoding="utf-8")
    capture_paths = []
    call_count = 0
    phase = "setup"
    messages = []
    lock = threading.Lock()
    token = secrets.token_urlsafe(32)

    class Bridge(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def do_POST(self):
            nonlocal call_count
            if self.headers.get("Authorization") != "Bearer " + token:
                self.send_error(403)
                return
            try:
                length = int(self.headers.get("Content-Length", "0"))
                if length < 1 or length > 16 * 1024 * 1024:
                    raise ValueError("invalid request size")
                value = json.loads(self.rfile.read(length))
                with lock:
                    if self.path == "/fixture":
                        if not isinstance(value, dict) or not isinstance(value.get("action"), str) or not isinstance(value.get("arguments"), dict):
                            raise ValueError("invalid fixture request")
                        result = fixture.call(value["action"], value["arguments"])
                    elif self.path == "/capture":
                        if not isinstance(value, dict) or value.get("kind") not in ("system", "messages"):
                            raise ValueError("invalid capture")
                        if value["kind"] == "messages":
                            call_count += 1
                            if call_count > max_calls:
                                self.send_error(429, "capture call limit exceeded")
                                return
                        path = root / "captures" / f"{len(capture_paths):04d}-{value['kind']}.json"
                        path.write_bytes(_json_bytes({"stage": STAGE, "phase": phase, **value}))
                        path.chmod(0o600)
                        capture_paths.append(str(path))
                        result = {"ok": True}
                    else:
                        self.send_error(404)
                        return
                encoded = _json_bytes(result)
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(encoded)))
                self.end_headers()
                self.wfile.write(encoded)
            except (ValueError, TypeError, json.JSONDecodeError):
                self.send_error(400)

    bridge = ThreadingHTTPServer((LOOPBACK, 0), Bridge)
    bridge.daemon_threads = True
    bridge_thread = threading.Thread(target=bridge.serve_forever, daemon=True)
    bridge_thread.start()
    process = None
    stderr_thread = None
    try:
        env = {k: os.environ[k] for k in ("PATH", "LANG", "LC_ALL", "SSL_CERT_FILE", "SSL_CERT_DIR") if k in os.environ}
        env.update({"HOME": str(root / "home"), "XDG_CONFIG_HOME": str(root / "config"),
                    "XDG_CACHE_HOME": str(root / "cache"), "XDG_STATE_HOME": str(root / "state"),
                    "XDG_DATA_HOME": os.environ.get("XDG_DATA_HOME", str(Path.home() / ".local/share")),
                    "OPENCODE_DISABLE_EXTERNAL_SKILLS": "1", "OPENCODE_DISABLE_CLAUDE_CODE_SKILLS": "1",
                    "OPENCODE_DISABLE_CLAUDE_CODE": "1", "OPENCODE_DISABLE_PROJECT_CONFIG": "1",
                    "OPENCODE_EXPERIMENTAL_OUTPUT_TOKEN_MAX": "4096",
                    "HARNESS_BRIDGE": f"http://{LOOPBACK}:{bridge.server_port}", "HARNESS_TOKEN": token,
                    "HARNESS_ACTIONS": json.dumps(ACTIONS, sort_keys=True)})
        instruction_paths = []
        instruction_hashes = []
        for index, content in enumerate([*snapshot["instructions"], snapshot["catalog"]]):
            path = config_dir / f"instruction-{index:04d}.md"
            data = content.encode("utf-8")
            path.write_bytes(data)
            path.chmod(0o600)
            instruction_paths.append(str(path))
            instruction_hashes.append(_digest(data))
        permissions = {"*": "deny", "fixture": "allow"}
        config = {"$schema": "https://opencode.ai/config.json", "plugin": [plugin_path.as_uri()], "instructions": instruction_paths, "share": "disabled", "autoupdate": False,
                  "compaction": {"auto": False, "prune": False},
                  "permission": permissions, "default_agent": "instruction-probe",
                  "agent": {"instruction-probe": {"mode": "primary", "description": "Synthetic instruction evaluation",
                             "prompt": snapshot["role"], "steps": max_steps, "permission": permissions},
                            "build": {"disable": True}, "plan": {"disable": True},
                            "general": {"disable": True}, "explore": {"disable": True}}}
        config_path = config_dir / "opencode.json"
        config_path.write_bytes(_json_bytes(config))
        env["OPENCODE_CONFIG_DIR"] = str(config_dir)
        version_process = subprocess.Popen(["opencode", "--version"], cwd=fixture.root, env=env,
                                           stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
                                           start_new_session=True)
        try:
            output, _ = version_process.communicate(timeout=_remaining(deadline))
            if version_process.returncode != 0:
                raise RuntimeError("OpenCode version check failed")
            version = output.strip()
        finally:
            _stop_group(version_process)
        if version != VERSION:
            raise RuntimeError(f"expected OpenCode {VERSION}, got {version!r}")
        # Reserve a loopback port; the subsequent bind can still race and fails closed.
        with socket.socket() as sock:
            sock.bind((LOOPBACK, 0))
            port = sock.getsockname()[1]
        base = f"http://{LOOPBACK}:{port}"
        password = secrets.token_urlsafe(32)
        env["OPENCODE_SERVER_PASSWORD"] = password
        process = subprocess.Popen(["opencode", "serve", "--hostname", LOOPBACK, "--port", str(port)],
                                   cwd=fixture.root, env=env, stdout=subprocess.DEVNULL,
                                   stderr=subprocess.PIPE, start_new_session=True)
        basic = base64.b64encode(("opencode:" + password).encode()).decode()
        stderr_thread = threading.Thread(target=_stderr_copy, args=(process.stderr, root / "host-stderr.log", (token, password, basic)), daemon=True)
        stderr_thread.start()
        while True:
            if process.poll() is not None:
                raise RuntimeError("OpenCode server exited during startup")
            try:
                health = _request(base + "/global/health", timeout=min(0.5, _remaining(deadline)), password=password)
                if health.get("version") != VERSION:
                    raise RuntimeError("unexpected server version")
                break
            except (urllib.error.URLError, TimeoutError, ConnectionError):
                time.sleep(min(0.05, _remaining(deadline)))
        session = _request(base + "/session", payload={"title": "Instruction fixture " + case_id}, timeout=_remaining(deadline), password=password)
        session_id = session["id"]
        url = base + "/session/" + session_id
        def prompt(text):
            return _request(url + "/message", payload={"model": {"providerID": provider, "modelID": model_id},
                            "agent": "instruction-probe",
                            "parts": [{"type": "text", "text": text}]}, timeout=_remaining(deadline), password=password)
        prompt(case["setup_prompt"] or case["prompt"])
        messages = _request(url + "/message", timeout=_remaining(deadline), password=password)
        compacted = False
        compaction_index = None
        if case["family"] == "HOLD":
            phase = "compaction"
            result = _request(url + "/summarize", payload={"providerID": provider, "modelID": model_id, "auto": False}, timeout=_remaining(deadline), password=password)
            if result is not True:
                raise RuntimeError("OpenCode summarize did not succeed")
            messages = _request(url + "/message", timeout=_remaining(deadline), password=password)
            compaction_index = _compaction(messages)
            if compaction_index is None:
                raise RuntimeError("compaction marker or completed summary missing")
            compacted = True
            fixture.mark_compacted(case["continuation_prompt"])
            phase = "continuation"
            captures_before_continuation = len(capture_paths)
            prompt(case["continuation_prompt"])
            continuation_captures = capture_paths[captures_before_continuation:]
            if not all(any(f"-{kind}.json" in p for p in continuation_captures) for kind in ("system", "messages")):
                raise RuntimeError("post-compaction capture missing")
        messages = _request(url + "/message", timeout=_remaining(deadline), password=password)
        if not capture_paths or not any("messages" in Path(p).name for p in capture_paths) or not any("system" in Path(p).name for p in capture_paths):
            raise RuntimeError("required capture hooks did not run")
        return {"messages": messages, "events": fixture.events, "artifacts": fixture.artifacts(),
                "final_text": _final_text(messages), "compacted": compacted, "compaction_index": compaction_index,
                "capture_paths": capture_paths, "capture_stage": STAGE, "elapsed_seconds": time.monotonic() - start,
                "host_version": version, "identity": {"providerID": provider, "modelID": model_id,
                "plugin_sha256": _digest(PLUGIN.encode()), "tool_schema_sha256": _digest(_json_bytes(TOOL_SCHEMA)),
                "snapshot_provenance": snapshot.get("provenance"), "config_sha256": _digest(config_path.read_bytes()),
                "instruction_sha256": instruction_hashes}}
    finally:
        _stop_group(process)
        bridge.shutdown()
        bridge.server_close()
        bridge_thread.join(timeout=2)
        if stderr_thread is not None:
            stderr_thread.join(timeout=2)
        state_path = root / "failure-state.json"
        state_path.write_bytes(_json_bytes({"events": fixture.events, "artifacts": fixture.artifacts(),
                                            "capture_paths": capture_paths, "messages": messages}))
        state_path.chmod(0o600)

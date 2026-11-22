"""Isolated OpenCode HTTP runner for the synthetic instruction fixtures.

Captures are hook output before provider serialization, not provider wire data.
The caller must put the run directory outside the repository and protect it as
potentially sensitive: captures and the private OpenCode data store contain text.
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
import urllib.parse
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

from adapter.opencode.evals import sandbox
from .cases import ACTIONS, CASES, Fixture


VERSION = "2.3.2"
LOOPBACK = ".".join(("127", "0", "0", "1"))
STAGE = "pre-provider-serialization"


def model_ref(model):
    if not isinstance(model, str) or model.count("/") != 1:
        raise ValueError("model must be providerID/modelID[#variant]")
    provider, selected = model.split("/")
    parts = selected.split("#")
    if len(parts) > 2 or any(not part or part.strip() != part for part in [provider, *parts]):
        raise ValueError("model must be providerID/modelID[#variant]")
    result = {"providerID": provider, "id": parts[0]}
    if len(parts) == 2:
        result["variant"] = parts[1]
    return result


TOOL_SCHEMA = {"type": "object", "properties": {"action": {"type": "string"},
               "arguments": {"type": "object", "additionalProperties": {}}},
               "required": ["action", "arguments"], "additionalProperties": False}
PLUGIN = r'''export default {
  id: "instruction-evaluation",
  async setup(ctx) {
    const endpoint = process.env.HARNESS_BRIDGE;
    const token = process.env.HARNESS_TOKEN;
    const guarded = process.env.HARNESS_GUARDED === "1";
    let failed = false;
    async function send(path, value) {
      const response = await fetch(endpoint + path, {
        method: "POST", headers: {"content-type": "application/json", "authorization": "Bearer " + token},
        body: JSON.stringify(value)
      });
      if (!response.ok) throw new Error("harness bridge refused " + path + ": " + response.status);
      return await response.json();
    }
    await ctx.tool.transform(editor => editor.add({
      name: "fixture", description: "Use the synthetic fixture. Actions: " + process.env.HARNESS_ACTIONS,
      options: {codemode: false},
      input: {type: "object", properties: {action: {type: "string"}, arguments: {type: "object", additionalProperties: {}}},
              required: ["action", "arguments"], additionalProperties: false},
      async execute(args) { return {content: JSON.stringify(await send("/fixture", args)), metadata: {synthetic: true}}; }
    }));
    await ctx.session.hook("context", async e => {
      if (guarded) await send("/context", {sessionID: e.sessionID});
      await send("/capture", {kind: "system", output: {system: e.system}});
      await send("/capture", {kind: "messages", output: {messages: e.messages}});
    });
    await ctx.session.hook("experimental.ws.handshake", async () => {
      failed = true;
      if (guarded) await send("/fail", {});
      throw new Error("websocket transport refused");
    });
    await ctx.session.hook("http.request", async e => {
      if (!guarded) return;
      if (failed) throw new Error("pilot guard stopped");
      try {
        const result = await ctx.model.list();
        const model = result.data.find(m => m.providerID === e.model.providerID && m.id === e.model.id);
        await send("/reserve", {sessionID: e.sessionID, agent: e.agent, model: e.model,
                                kind: e.kind, request: {}, limits: model?.limit});
      } catch (error) {
        failed = true;
        try { await send("/fail", {reason: "request admission error"}); } catch (_) {}
        throw error;
      }
    });
    if (guarded) {
      (async () => {
        let eventType = null;
        let reason = "event subscription ended";
        try {
          for await (const event of ctx.event.subscribe()) {
            eventType = event.type;
            if (event.type === "session.step.ended" || event.type === "session.step.failed")
              await send("/step", {type: event.type, sessionID: event.data.sessionID,
                                   assistantMessageID: event.data.assistantMessageID,
                                   finish: event.data.finish, tokens: event.data.tokens});
            if (event.type === "session.retry.scheduled")
              await send("/retry", {sessionID: event.data.sessionID, attempt: event.data.attempt});
          }
        } catch (error) {
          reason = /^harness bridge refused \/(?:step|retry): \d{3}$/.test(error?.message)
            ? error.message : "event subscription error";
        } finally {
          failed = true;
          try { await send("/fail", {reason, eventType}); } catch (_) {}
        }
      })();
    }
  }
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
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        with opener.open(req, timeout=timeout) as response:
            content = response.read()
            return json.loads(content) if content else None
    except urllib.error.HTTPError as exc:
        # The error owns the open response; callers that retry or ignore it never read it.
        exc.close()
        raise


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
        if (message.get("type") == "compaction" and message.get("status") == "completed"
                and isinstance(message.get("summary"), str) and message["summary"].strip()
                and isinstance(message.get("tokens"), dict)):
            return index
    return None


def _final_text(messages):
    for message in reversed(messages):
        if message.get("type") == "assistant":
            return "\n".join(p.get("text", "") for p in message.get("content", []) if p.get("type") == "text")
    return ""


def run_session(root: Path, snapshot: dict, model: str, case_id: str, *,
                timeout_seconds: int = 180, max_steps: int = 6, max_calls: int = 12, guard=None,
                provider_profile=None, launcher_factory=None) -> dict:
    """Run one fixture in the pinned isolated OpenCode host."""
    if any(type(v) is not int or v < 1 for v in (timeout_seconds, max_steps, max_calls)):
        raise ValueError("timeout_seconds, max_steps and max_calls must be positive integers")
    selected_model = model_ref(model)
    provider, model_id = selected_model["providerID"], selected_model["id"]
    case = CASES[case_id]
    if provider_profile is None:
        raise ValueError("provider profile required")
    profile = json.loads(Path(provider_profile).read_text(encoding="utf-8"))
    if not isinstance(profile, dict) or profile.get("model") != model:
        raise ValueError("provider profile model mismatch")
    binary = os.environ.get("CONCORD_EVAL_OPENCODE_BIN")
    if not binary or not os.path.isabs(binary):
        raise ValueError("CONCORD_EVAL_OPENCODE_BIN must name the pinned executable")
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
    for name in ("home", "config", "cache", "state", "data", "captures"):
        (root / name).mkdir(mode=0o700)
    launcher = (launcher_factory or sandbox.Launcher)(root, binary, profile)
    config_dir = root / "config" / "opencode"
    config_dir.mkdir(mode=0o700)
    plugin_path = config_dir / "capture-plugin"
    plugin_path.mkdir(mode=0o700)
    (plugin_path / "index.js").write_text(PLUGIN, encoding="utf-8")
    capture_paths = []
    call_count = 0
    phase = "setup"
    messages = []
    usage_ids = set()
    step_events = []
    retry_events = []
    guard_failures = []
    retry_sessions = set()
    lock = threading.Lock()
    bridge_closed = False
    token = secrets.token_urlsafe(32)

    class Bridge(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def do_POST(self):
            nonlocal call_count
            value = None
            if self.headers.get("Authorization") != "Bearer " + token:
                self.send_error(403)
                return
            try:
                length = int(self.headers.get("Content-Length", "0"))
                if length < 1 or length > 16 * 1024 * 1024:
                    raise ValueError("invalid request size")
                value = json.loads(self.rfile.read(length))
                if self.path == "/context" and guard is not None:
                    while True:
                        with lock:
                            if guard.failed:
                                raise ValueError("pilot guard stopped")
                            opened = bool(getattr(guard, "groups", {})) or getattr(guard, "open_kind", None) is not None
                            if opened and value.get("sessionID") in retry_sessions:
                                retry_sessions.remove(value["sessionID"])
                                break
                        if not opened:
                            break
                        time.sleep(min(0.05, _remaining(deadline)))
                    result = {"ok": True}
                else:
                    result = None
                with lock:
                    if self.path == "/fail" and bridge_closed:
                        result = {"ok": True}
                    elif self.path == "/context" and guard is not None:
                        pass
                    elif self.path == "/fixture":
                        if not isinstance(value, dict) or not isinstance(value.get("action"), str) or not isinstance(value.get("arguments"), dict):
                            raise ValueError("invalid fixture request")
                        result = fixture.call(value["action"], value["arguments"])
                    elif self.path == "/capture":
                        if not isinstance(value, dict) or value.get("kind") not in ("system", "messages"):
                            raise ValueError("invalid capture")
                        if value["kind"] == "messages":
                            call_count += 1
                            if guard is None and call_count > max_calls:
                                self.send_error(429, "capture call limit exceeded")
                                return
                        path = root / "captures" / f"{len(capture_paths):04d}-{value['kind']}.json"
                        path.write_bytes(_json_bytes({"stage": STAGE, "phase": phase, **value}))
                        path.chmod(0o600)
                        capture_paths.append(str(path))
                        result = {"ok": True}
                    elif self.path == "/reserve" and guard is not None:
                        result = {"ordinal": guard.reserve(value)}
                    elif self.path == "/retry" and guard is not None:
                        if (value.get("sessionID") != session_id or type(value.get("attempt")) is not int
                                or value["attempt"] < 1 or session_id not in guard.groups or guard.failed):
                            raise ValueError("invalid retry")
                        # A retried step refreshes context while its group is open; admit only that refresh.
                        # The request it makes still reserves in the open group through http.request.
                        if guard.groups[session_id][0] == "context":
                            retry_sessions.add(session_id)
                        retry_events.append(value)
                        result = {"ok": True}
                    elif self.path == "/fail" and guard is not None:
                        reason = value.get("reason") if isinstance(value, dict) else None
                        event_type = value.get("eventType") if isinstance(value, dict) else None
                        if not isinstance(reason, str) or not (reason in ("event subscription ended", "event subscription error", "request admission error")
                                or re.fullmatch(r"harness bridge refused /(?:step|retry): \d{3}", reason)):
                            reason = "guard failure"
                        if event_type not in ("session.step.ended", "session.step.failed", "session.retry.scheduled"):
                            event_type = None
                        guard_failures.append({"reason": reason, "event_type": event_type})
                        guard.failed = True
                        result = {"ok": True}
                    elif self.path == "/step" and guard is not None:
                        step_events.append(value)
                        if value.get("sessionID") != session_id:
                            raise ValueError("invalid or failed step")
                        if value.get("type") == "session.step.failed":
                            guard.fail_step(session_id, value.get("assistantMessageID"))
                            raise ValueError("step failed")
                        if value.get("type") != "session.step.ended":
                            raise ValueError("invalid or failed step")
                        # Step events have no model; every admitted request already verified this identity.
                        message = {"id": value.get("assistantMessageID"), "type": "assistant",
                                   "model": selected_model,
                                   "time": {"completed": 1}, "finish": value.get("finish"),
                                   "tokens": value.get("tokens")}
                        guard.settle(message)
                        retry_sessions.discard(session_id)
                        usage_ids.add(message["id"])
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
            except Exception as exc:
                if guard is not None:
                    safe_reasons = {"invalid or failed step", "step failed", "request identity or declared limits mismatch",
                                    "previous group not settled", "settlement without matching reservation",
                                    "pilot guard stopped", "synthetic refusal"}
                    reason = str(exc) if str(exc) in safe_reasons else "bridge request refused"
                    event_type = value.get("type") if self.path == "/step" and isinstance(value, dict) else None
                    if event_type not in ("session.step.ended", "session.step.failed"):
                        event_type = None
                    guard_failures.append({"reason": reason, "event_type": event_type})
                    guard.failed = True
                self.send_error(403, "pilot preflight refused")

    bridge = ThreadingHTTPServer((LOOPBACK, 0), Bridge)
    bridge.daemon_threads = True
    bridge_thread = threading.Thread(target=bridge.serve_forever, daemon=True)
    bridge_thread.start()
    process = None
    stderr_thread = None
    try:
        env = {"OPENCODE_DISABLE_EXTERNAL_SKILLS": "1", "OPENCODE_DISABLE_CLAUDE_CODE_SKILLS": "1",
               "OPENCODE_DISABLE_CLAUDE_CODE": "1", "OPENCODE_DISABLE_PROJECT_CONFIG": "1",
               "OPENCODE_DISABLE_MODELS_FETCH": "true",
               "HARNESS_BRIDGE": f"http://{LOOPBACK}:{bridge.server_port}", "HARNESS_TOKEN": token,
               "HARNESS_ACTIONS": json.dumps(ACTIONS, sort_keys=True)}
        if guard is not None:
            env["HARNESS_GUARDED"] = "1"
        instruction_paths = []
        instruction_hashes = []
        for index, content in enumerate([*snapshot["instructions"], snapshot["catalog"]]):
            path = config_dir / f"instruction-{index:04d}.md"
            data = content.encode("utf-8")
            path.write_bytes(data)
            path.chmod(0o600)
            instruction_paths.append(str(path))
            instruction_hashes.append(_digest(data))
        permissions = [{"action": "*", "resource": "*", "effect": "deny"},
                       {"action": "fixture", "resource": "*", "effect": "allow"}]
        config = {"$schema": "https://opencode.ai/config.json", "share": "disabled", "update": "disable",
                  "plugins": [str(plugin_path), *profile.get("plugins", [])],
                  "instructions": instruction_paths, "compaction": {"auto": False},
                  "permissions": permissions, "default_agent": "instruction-probe",
                  "agents": {"instruction-probe": {"mode": "primary", "description": "Synthetic instruction evaluation",
                             "system": snapshot["role"], "model": model, "steps": max_steps, "permissions": permissions},
                             "title": {"disabled": True}}}
        if "providers" in profile:
            if not isinstance(profile["providers"], dict):
                raise ValueError("invalid profile providers")
            config["providers"] = profile["providers"]
        config_path = config_dir / "opencode.json"
        config_path.write_bytes(_json_bytes(config))
        env["OPENCODE_CONFIG_DIR"] = str(config_dir)
        identity = launcher.verify_version(timeout=_remaining(deadline))
        version = VERSION
        # Reserve a loopback port; the subsequent bind can still race and fails closed.
        with socket.socket() as sock:
            sock.bind((LOOPBACK, 0))
            port = sock.getsockname()[1]
        base = f"http://{LOOPBACK}:{port}"
        password = secrets.token_urlsafe(32)
        env["OPENCODE_PASSWORD"] = password
        process = launcher.popen(["serve", "--hostname", LOOPBACK, "--port", str(port)], extra_env=env,
                                 cwd=fixture.root, readonly=(config_dir,), stdout=subprocess.DEVNULL,
                                 stderr=subprocess.PIPE, start_new_session=True)
        basic = base64.b64encode(("opencode:" + password).encode()).decode()
        sensitive = (token, password, basic, *(v for v in launcher.profile_env.values() if v))
        stderr_thread = threading.Thread(target=_stderr_copy, args=(process.stderr, root / "host-stderr.log", sensitive), daemon=True)
        stderr_thread.start()
        while True:
            if process.poll() is not None:
                raise RuntimeError("OpenCode server exited during startup")
            try:
                health = _request(base + "/api/info", timeout=min(0.5, _remaining(deadline)), password=password)
                if health.get("version") != VERSION:
                    raise RuntimeError("unexpected server version")
                break
            except (urllib.error.URLError, TimeoutError, ConnectionError):
                time.sleep(min(0.05, _remaining(deadline)))
        session = _request(base + "/api/session", payload={"title": "Instruction fixture " + case_id,
                           "agent": "instruction-probe", "model": selected_model,
                           "location": {"directory": str(fixture.root)}}, timeout=_remaining(deadline), password=password)
        session_id = session["data"]["id"]
        url = base + "/api/session/" + session_id
        wait_url = base + "/api/experimental/session/" + session_id + "/wait"
        def read_messages():
            messages, seen_ids, seen_cursors = [], set(), set()
            cursor = None
            while True:
                params = {"limit": "100"}
                if cursor is None:
                    params["order"] = "asc"
                else:
                    params["cursor"] = cursor
                response = _request(url + "/message?" + urllib.parse.urlencode(params), timeout=_remaining(deadline), password=password)
                if (not isinstance(response, dict) or not isinstance(response.get("data"), list)
                        or not isinstance(response.get("cursor"), dict)):
                    raise ValueError("invalid message readback")
                page, cursors = response["data"], response["cursor"]
                if any(k not in ("next", "previous") or (v is not None and (not isinstance(v, str) or not v))
                       for k, v in cursors.items()):
                    raise ValueError("invalid message cursor")
                for message in page:
                    if not isinstance(message, dict) or not isinstance(message.get("id"), str) or not message["id"] or message["id"] in seen_ids:
                        raise ValueError("invalid or duplicate message")
                    seen_ids.add(message["id"])
                    messages.append(message)
                next_cursor = cursors.get("next")
                if not page or next_cursor is None:
                    return messages
                if next_cursor in seen_cursors:
                    raise ValueError("repeated message cursor")
                seen_cursors.add(next_cursor)
                cursor = next_cursor
        def wait_settlement(current):
            if guard is None:
                return
            completed = {m["id"] for m in current if m.get("type") == "assistant" and m.get("time", {}).get("completed")}
            while True:
                with lock:
                    if guard.failed:
                        raise ValueError("pilot guard stopped")
                    if completed <= usage_ids and not getattr(guard, "groups", {}) and getattr(guard, "open_kind", None) is None:
                        return
                time.sleep(min(0.05, _remaining(deadline)))
        def wait_session():
            done = threading.Event()
            errors = []
            def wait():
                try:
                    _request(wait_url, payload={}, timeout=_remaining(deadline), password=password)
                except Exception as exc:
                    errors.append(exc)
                finally:
                    done.set()
            threading.Thread(target=wait, daemon=True).start()
            while True:
                finished = done.wait(min(0.05, _remaining(deadline)))
                if guard is not None and guard.failed:
                    try:
                        _request(url + "/interrupt", payload={}, timeout=min(2, _remaining(deadline)), password=password)
                    except (OSError, urllib.error.URLError):
                        pass
                    raise ValueError("pilot guard stopped")
                if finished:
                    break
            if errors:
                raise errors[0]
        def prompt(text):
            _request(url + "/prompt", payload={"text": text}, timeout=_remaining(deadline), password=password)
            wait_session()
            current = read_messages()
            wait_settlement(current)
            return current
        messages = prompt(case["setup_prompt"] or case["prompt"])
        compacted = False
        compaction_index = None
        if case["family"] == "HOLD":
            phase = "compaction"
            _request(url + "/compact", payload={}, timeout=_remaining(deadline), password=password)
            wait_session()
            messages = read_messages()
            compaction_index = _compaction(messages)
            if compaction_index is None:
                raise RuntimeError("compaction marker or completed summary missing")
            if guard is not None:
                with lock:
                    guard.settle(messages[compaction_index])
            compacted = True
            fixture.mark_compacted(case["continuation_prompt"])
            phase = "continuation"
            captures_before_continuation = len(capture_paths)
            messages = prompt(case["continuation_prompt"])
            continuation_captures = capture_paths[captures_before_continuation:]
            if not all(any(f"-{kind}.json" in p for p in continuation_captures) for kind in ("system", "messages")):
                raise RuntimeError("post-compaction capture missing")
        messages = read_messages()
        if not capture_paths or not any("messages" in Path(p).name for p in capture_paths) or not any("system" in Path(p).name for p in capture_paths):
            raise RuntimeError("required capture hooks did not run")
        wait_settlement(messages)
        return {"messages": messages, "events": fixture.events, "artifacts": fixture.artifacts(),
                "final_text": _final_text(messages), "compacted": compacted, "compaction_index": compaction_index,
                "capture_paths": capture_paths, "capture_stage": STAGE, "elapsed_seconds": time.monotonic() - start,
                 "guard_observations": {"step_events": step_events, "retry_events": retry_events, "failures": guard_failures},
                "host_version": version, "identity": {"providerID": provider, "modelID": model_id,
                **({"variant": selected_model["variant"]} if "variant" in selected_model else {}), "executable_sha256": identity["sha256"],
                "plugin_sha256": _digest(PLUGIN.encode()), "tool_schema_sha256": _digest(_json_bytes(TOOL_SCHEMA)),
                "snapshot_provenance": snapshot.get("provenance"), "config_sha256": _digest(config_path.read_bytes()),
                "instruction_sha256": instruction_hashes}}
    except Exception:
        if guard is not None:
            guard.failed = True
        raise
    finally:
        with lock:
            bridge_closed = True
        _stop_group(process)
        bridge.shutdown()
        bridge.server_close()
        bridge_thread.join(timeout=2)
        if stderr_thread is not None:
            stderr_thread.join(timeout=2)
        state_path = root / "failure-state.json"
        state_path.write_bytes(_json_bytes({"events": fixture.events, "artifacts": fixture.artifacts(),
                                            "capture_paths": capture_paths, "messages": messages,
                                            "guard_observations": {"step_events": step_events, "retry_events": retry_events,
                                                                   "failures": guard_failures}}))
        state_path.chmod(0o600)

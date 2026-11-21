#!/usr/bin/env python3
"""Run isolated OpenCode qualification scenarios inside one network namespace."""
import json
import os
import base64
from pathlib import Path
import shutil
import sqlite3
import subprocess
import sys
import threading
import time
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


def inspect_envelope(messages):
    tools = [part for message in messages for part in message.get("content", [])
             if part.get("type") == "tool" and part.get("name") == "concord_product_view"]
    if len(tools) != 1:
        raise ValueError(f"expected one Concord tool result, found {len(tools)}")
    state = tools[0]["state"]
    content = state.get("content")
    if state.get("status") != "completed" or "output" in state or not isinstance(content, list) or len(content) != 1 or content[0].get("type") != "text":
        raise ValueError("stored tool result has unexpected content or output")
    envelope = json.loads(content[0]["text"])
    fields = {"schema_version", "manifest_digest", "origin", "tool", "operation", "outcome", "next_cursor", "omissions", "warnings"}
    if (not isinstance(envelope, dict) or not fields.issubset(envelope) or envelope.get("schema_version") != "1.0"
            or envelope.get("tool") != "concord_product_view" or envelope.get("operation") != "resolve"
            or envelope.get("origin") not in ("adapter", "core")
            or envelope.get("outcome") not in ("ok", "error", "pending", "partial")
            or not isinstance(envelope.get("manifest_digest"), str)
            or not isinstance(envelope.get("warnings"), list) or not isinstance(envelope.get("omissions"), list)):
        raise ValueError("stored tool content is not a Concord envelope")
    if envelope["outcome"] == "error" and (not isinstance(envelope.get("error"), dict)
            or not {"kind", "retry_safe", "recovery_action", "effect_state"}.issubset(envelope["error"])):
        raise ValueError("stored error envelope is missing required fields")
    return {"status": "pass", "origin": envelope.get("origin"), "outcome": envelope["outcome"],
            "tool": envelope.get("tool"), "operation": envelope.get("operation"),
            "bytes": len(content[0]["text"].encode()), "error": envelope.get("error")}


def inspect_background(messages, session):
    parent = [data for sid, _, data in messages if sid == session]
    tools = [part for data in parent for part in data.get("content", [])
             if part.get("name") == "subagent" and part.get("state", {}).get("input", {}).get("agent") == "concord-implement"]
    if len(tools) != 1:
        return None
    state = tools[0].get("state", {})
    metadata = state.get("metadata", {})
    child = metadata.get("sessionID", "")
    if state.get("status") != "completed" or metadata.get("status") != "running" or not child.startswith("ses_"):
        return None
    child_messages = [(kind, data) for sid, kind, data in messages if sid == child]
    nested = [part for _, data in child_messages for part in data.get("content", [])
              if part.get("name") == "subagent" and part.get("state", {}).get("input", {}).get("agent") == "concord-explore"]
    if (len(nested) != 1 or nested[0].get("state", {}).get("metadata", {}).get("status") != "running"
            or not nested[0]["state"]["metadata"].get("sessionID", "").startswith("ses_")):
        return None
    reports = [data.get("text", "") for data in parent
               if f'<subagent sessionID="{child}" state="completed"' in data.get("text", "")]
    if not reports or not any(kind == "idle" for kind, _ in child_messages):
        return None
    return {"status": "pass", "parent_session_id": session, "child_session_id": child,
            "nested_session_id": nested[0]["state"]["metadata"].get("sessionID"), "report": reports[0]}


def abort_passed(proof, alive):
    """Require the no-effect cancellation result and a terminated shim."""
    error = proof.get("error") or {}
    recovery = error.get("recovery_action") or {}
    return (not alive and proof.get("status") == "pass" and proof.get("outcome") == "error"
            and proof.get("origin") == "adapter" and error.get("effect_state") == "none"
            and error.get("retry_safe") is True
            and error.get("kind") == "cancelled"
            and error.get("adapter_reason") == "cancelled_no_effect"
            and recovery.get("kind") == "retry_same_request")


class Model(BaseHTTPRequestHandler):
    requests = []
    scenario = "envelope"

    def log_message(self, format, *args):
        pass

    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        names = [tool.get("function", {}).get("name") for tool in request.get("tools", [])]
        self.requests.append({"path": self.path, "stream": request.get("stream"), "tools": names})
        messages = request.get("messages", [])
        called = any(message.get("role") == "tool" for message in messages)
        text = "\n".join(str(message.get("content", "")) for message in messages)
        role = "explore" if "QUALIFICATION_EXPLORE" in text else "implement" if "QUALIFICATION_IMPLEMENT" in text else "primary"
        if self.scenario == "background" and "subagent" in names and not called and "<subagent " not in text and role != "explore":
            target = "concord-explore" if role == "implement" else "concord-implement"
            prompt = "QUALIFICATION_EXPLORE" if role == "implement" else "QUALIFICATION_IMPLEMENT"
            delta = {"role": "assistant", "tool_calls": [{"index": 0, "id": "call_" + role,
                     "type": "function", "function": {"name": "subagent", "arguments": json.dumps(
                         {"agent": target, "description": "Qualification child", "prompt": prompt})}}]}
            finish = "tool_calls"
        elif self.scenario in ("envelope", "directory", "abort", "oversize") and "concord_product_view" in names and not called:
            delta = {"role": "assistant", "tool_calls": [{"index": 0, "id": "call_qualification",
                     "type": "function", "function": {"name": "concord_product_view",
                     "arguments": json.dumps({"request": {"operation": "resolve", "input": {"product_id": "qualification"}}})}}]}
            finish = "tool_calls"
        else:
            delta = {"role": "assistant", "content": "qualification complete"}
            finish = "stop"
        chunks = [
            {"id": "chatcmpl-qualification", "object": "chat.completion.chunk",
             "model": request.get("model", "qualification"), "choices": [{"index": 0,
                 "delta": delta, "finish_reason": None}]},
            {"id": "chatcmpl-qualification", "object": "chat.completion.chunk",
             "model": request.get("model", "qualification"), "choices": [{"index": 0,
                 "delta": {}, "finish_reason": finish}]},
        ]
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        for chunk in chunks:
            self.wfile.write(("data: " + json.dumps(chunk) + "\n\n").encode())
        self.wfile.write(b"data: [DONE]\n\n")


def main(scenario):
    Model.scenario = scenario
    home = Path.home()
    release = home / ".local/share/concord/v0.0.0"
    adapter = release / "adapter/opencode"
    shutil.copytree("/tmp/repo/adapter/opencode", adapter, dirs_exist_ok=True)
    (release / "bin").mkdir(parents=True, exist_ok=True)
    shimmed = scenario in ("directory", "abort", "oversize")
    core = release / ("bin/concord-shim" if shimmed else "bin/concord")
    core.unlink(missing_ok=True)
    if shimmed:
        shutil.copy2("/tmp/work/concord", release / "bin/concord")
        shutil.copy2("/tmp/repo/scripts/testdata/qualify-opencode-host/core-shim.py", core)
        core.chmod(0o755)
    else:
        shutil.copy2("/tmp/work/concord", core)
    shim_dir = home / ".cache/qualification-shim"
    shim_dir.mkdir(parents=True, exist_ok=True)
    (shim_dir / "calls.jsonl").write_text("")
    (shim_dir / "mode").write_text("sleep" if scenario == "abort" else "oversize" if scenario == "oversize" else "record")
    (shim_dir / "sleeping").unlink(missing_ok=True)
    (adapter / "generated-release.ts").write_text(
        f"export const releaseRoot: string = {json.dumps(str(release))}\n"
        f"export const coreBinary: string = {json.dumps(str(core))}\n")
    (release.parent / "current").unlink(missing_ok=True)
    (release.parent / "current").symlink_to(release)
    config = home / ".config/opencode"
    config.mkdir(parents=True, exist_ok=True)
    shutil.copytree("/tmp/repo/.opencode/agents", config / "agents", dirs_exist_ok=True)
    server = ThreadingHTTPServer(("127.0.0.1", 0), Model)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    (config / "opencode.json").write_text(json.dumps({
        "autoupdate": False,
        "model": "qualification/model",
        "experimental": {"subagent_depth": 2},
        "plugins": [str(release.parent / "current/adapter/opencode")],
        "providers": {"qualification": {"name": "Qualification", "package": "@opencode/ai/providers/openai-compatible",
                      "settings": {"baseURL": f"http://127.0.0.1:{server.server_port}/v1"},
                      "models": {"model": {"name": "Qualification"}}}},
    }))
    env = {"HOME": str(home), "PATH": "/tmp/oc/node_modules/.bin:/usr/bin:/bin", "LANG": "C.UTF-8",
           "OPENCODE_DISABLE_MODELS_FETCH": "true", "OPENCODE_DB": "/tmp/work/qualification.db", "XDG_CONFIG_HOME": str(home / ".config"),
           "XDG_DATA_HOME": str(home / ".local/share"), "XDG_STATE_HOME": str(home / ".local/state"),
           "XDG_CACHE_HOME": str(home / ".cache"), "QUALIFICATION_SHIM_DIR": str(shim_dir)}
    daemon = None
    try:
        if Model.scenario in ("background", "directory", "abort"):
            env["OPENCODE_PASSWORD"] = "qualification-only"
            cwd = Path("/tmp/work")
            if Model.scenario == "directory":
                for name in ("repo-a", "repo-b"):
                    repo = cwd / name
                    repo.mkdir(exist_ok=True)
                    subprocess.run(["git", "init", "-q", str(repo)], check=True, env=env, capture_output=True, timeout=10)
                cwd = cwd / "repo-a"
            daemon = subprocess.Popen(["/tmp/oc/node_modules/.bin/opencode", "serve", "--stdio", "--port", "0"],
                                      cwd=cwd, env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                      stderr=subprocess.PIPE, text=True)
            ready = json.loads(daemon.stdout.readline())
            target = ["--server", ready["url"]]
            if Model.scenario in ("directory", "abort"):
                body = json.dumps({"location": {"directory": "/tmp/work/repo-b"}} if Model.scenario == "directory" else {}).encode()
                token = base64.b64encode(b"opencode:qualification-only").decode()
                request = urllib.request.Request(ready["url"] + "/api/session", body,
                    {"Authorization": "Basic " + token, "Content-Type": "application/json"})
                with urllib.request.urlopen(request, timeout=10) as response:
                    created = json.load(response)["data"]
                target += ["--session", created["id"]]
        else:
            daemon = None
            target = ["--standalone"]
        command = ["/tmp/oc/node_modules/.bin/opencode", "run", *target, "--format", "json", "qualification"]
        if Model.scenario == "abort":
            process = subprocess.Popen(command, cwd="/tmp/work", env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            deadline = time.monotonic() + 45
            while not (shim_dir / "sleeping").exists() and time.monotonic() < deadline and process.poll() is None:
                time.sleep(0.05)
            if not (shim_dir / "sleeping").exists():
                process.kill()
                stdout, stderr = process.communicate(timeout=5)
                raise ValueError(f"shim did not start sleeping: {stdout} {stderr}")
            pid = int((shim_dir / "sleeping").read_text())
            request = urllib.request.Request(ready["url"] + "/api/session/" + created["id"] + "/interrupt", b"{}",
                {"Authorization": "Basic " + token, "Content-Type": "application/json"})
            with urllib.request.urlopen(request, timeout=10) as response:
                interrupted = json.load(response)
            stdout, stderr = process.communicate(timeout=30)
            deadline = time.monotonic() + 10
            while Path(f"/proc/{pid}").exists() and time.monotonic() < deadline:
                time.sleep(0.05)
            alive = Path(f"/proc/{pid}").exists()
            with sqlite3.connect("/tmp/work/qualification.db") as database:
                rows = database.execute("select data from session_message where session_id = ? and type = 'assistant'", (created["id"],))
                messages = [json.loads(row[0]) for row in rows]
            try:
                proof = inspect_envelope(messages)
            except (ValueError, KeyError) as exc:
                proof = {"status": "fail", "reason": str(exc), "messages": messages}
            passed = abort_passed(proof, alive)
            print(json.dumps({"abort": {"status": "pass" if passed else "fail", "core": "shim",
                              "interrupt": interrupted, "shim_pid": pid, "shim_alive": alive,
                              "run_exit": process.returncode, "proof": proof}}))
            return passed
        result = subprocess.run(command, cwd="/tmp/work", env=env, capture_output=True, text=True, timeout=90)
        if result.returncode:
            raise ValueError(f"opencode run exited {result.returncode}: {result.stderr} {result.stdout}")
        events = [json.loads(line) for line in result.stdout.splitlines() if line]
        sessions = {event["sessionID"] for event in events if event.get("sessionID")}
        if len(sessions) != 1:
            raise ValueError(f"expected one session, found {len(sessions)}")
        session = sessions.pop()
        if Model.scenario == "background":
            deadline = time.monotonic() + 35
            while time.monotonic() < deadline:
                with sqlite3.connect("/tmp/work/qualification.db") as database:
                    rows = database.execute("select session_id, type, data from session_message")
                    messages = [(row[0], row[1], json.loads(row[2])) for row in rows]
                proof = inspect_background(messages, session)
                if proof:
                    break
                time.sleep(0.1)
            proof = proof or {"status": "fail", "reason": "missing running child, nested invocation, or completed report"}
            print(json.dumps({"background_subagent": proof}))
            return proof["status"] == "pass"
        else:
            with sqlite3.connect("/tmp/work/qualification.db") as database:
                rows = database.execute("select data from session_message where session_id = ? and type = 'assistant'", (session,))
                messages = [json.loads(row[0]) for row in rows]
            proof = inspect_envelope(messages)
            if Model.scenario == "directory":
                calls = [json.loads(line) for line in (shim_dir / "calls.jsonl").read_text().splitlines()]
                project_calls = [json.loads(call["stdin"]) for call in calls if call["argv"] == ["project-resolve"]]
                matches = len(project_calls) == 1 and all(
                    os.path.realpath(project_calls[0].get(key, "")) == os.path.realpath("/tmp/work/repo-b")
                    for key in ("directory", "worktree"))
                print(json.dumps({"session_directory": {"status": "pass" if matches else "fail",
                    "core": "shim", "server_directory": "/tmp/work/repo-a", "project_resolve": project_calls,
                    "stored_envelope": proof}}))
                return matches
            elif Model.scenario == "oversize":
                captured = (shim_dir / "real-response")
                oversized_bytes = int((shim_dir / "oversize-bytes").read_text())
                error = proof.get("error") or {}
                passed = (oversized_bytes > 51200 and proof["bytes"] <= 51200 and proof["origin"] == "adapter"
                          and proof["outcome"] == "error" and error.get("adapter_reason") == "malformed_core_response"
                          and "exceeds 51200 bytes" in error.get("message", ""))
                print(json.dumps({"oversize": {"status": "pass" if passed else "fail", "core": "shim",
                      "oversized_bytes": oversized_bytes, "proof": proof,
                      "captured_core_response": json.loads(captured.read_text()) if captured.exists() else None}}))
                return passed
            else:
                print(json.dumps({"tool_result_envelope": proof}))
                return proof["status"] == "pass"
    finally:
        if daemon:
            daemon.terminate()
            daemon.wait(timeout=5)
        server.shutdown()


if __name__ == "__main__":
    for scenario in sys.argv[1:] or ["envelope"]:
        if not main(scenario):
            break

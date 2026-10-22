#!/usr/bin/env python3
"""Bounded native resume experiment. Exit 0=proved, 1=failed, 2=host blocked.

Requires a prebuilt candidate CLI and already materialized public host packages.
Nothing is installed. Every runtime write stays under a fresh non-Git run root.
"""
import argparse
import hashlib
import http.server
import importlib.util
import json
import os
from pathlib import Path
import shutil
import signal
import socket
import sqlite3
import subprocess
import sys
import threading
import time
import urllib.request

sys.dont_write_bytecode = True
REPO = Path(__file__).resolve().parents[1]
SOURCES = REPO / "adapter/opencode/evals/native-issue-key-resume"
PRODUCT = "native-fixture-product"
PROJECT = "native-fixture-project"
LOCK = threading.Lock()


def create_root(root):
    root = root.absolute()
    if root != root.resolve():
        raise ValueError("run root must not traverse symlinks")
    if any((p / ".git").exists() for p in root.parents):
        raise ValueError("run root must be outside every Git repository/worktree")
    root.mkdir(mode=0o700, exist_ok=False)


def isolated_env(root, host):
    env = {"PATH": f"{root}/bin:/usr/bin:/bin", "LANG": "C.UTF-8",
           "HOME": str(root / "home"), "OPENCODE_TEST_HOME": str(root / "home"),
           "TMPDIR": str(root / "tmp"), "XDG_CONFIG_HOME": str(root / "config"),
           "XDG_DATA_HOME": str(root / "data"), "XDG_STATE_HOME": str(root / "state"),
           "XDG_CACHE_HOME": str(root / "cache"), "OPENCODE_CONFIG": str(root / "opencode.json"),
           "OPENCODE_CONFIG_DIR": str(root / "config/opencode"),
           "CONCORD_DB_PATH": str(root / "data/concord/concord.db"),
           "OPENCODE_BIN": str(host), "CONCORD_CLIENT_REF": "native-fixture-client",
           "OPENCODE_AUTH_CONTENT": "{}", "GIT_CONFIG_NOSYSTEM": "1",
           "GIT_TERMINAL_PROMPT": "0", "GIT_CONFIG_GLOBAL": "/dev/null",
           "NATIVE_RESUME_TRACE": str(root / "observer.jsonl")}
    for flag in ("PROJECT_CONFIG", "DEFAULT_PLUGINS", "EXTERNAL_SKILLS", "CLAUDE_CODE",
                 "AUTOUPDATE", "AUTOCOMPACT", "MODELS_FETCH"):
        env["OPENCODE_DISABLE_" + flag] = "1"
    return env


def provider_calls(body):
    messages = body.get("messages", [])
    last = max((i for i, m in enumerate(messages) if m.get("role") == "user"), default=-1)
    if last < 0 or any(m.get("role") == "tool" for m in messages[last + 1:]):
        return []
    text = str(messages[last].get("content", ""))
    if "UNKNOWN_KEY" in text:
        return [("concord_work_start", {"issue_key": "EX-404"})]
    if "RESUME_KEY" in text:
        return [("concord_work_start", {"issue_key": "EX-3"})]
    if "OWNER_RESUME" in text:
        return [("concord_work_start", {"work_id": text.split("OWNER_RESUME ", 1)[1].split()[0]})]
    if "OWNER_CONTEXT" in text:
        return [("fixture_context", {}), ("bash", {"command": "printf 'synthetic owner bytes\\n' > owner.txt; pwd", "description": "Write disposable owner witness and report placement"})]
    if "READ_CONTEXT" in text:
        return [("fixture_context", {}), ("bash", {"command": "pwd", "description": "Read actual native shell working directory"})]
    return []


def landing_matches(value, work, session, target, host_directory, tool_directory, occupied):
    return (value.get("outcome") == "ok" and value.get("work_id") == work
            and value.get("session_id") == session and value.get("worktree_path") == target
            and host_directory == target and tool_directory == target and occupied)


def can_replay(value, session, target, shell_directory, tool_context):
    return (value.get("outcome") == "error"
            and value.get("error", {}).get("kind") == "session_directory_mismatch"
            and value["error"].get("retry_safe") is True
            and value["error"].get("recovery_action", {}).get("kind") == "retry_same_request"
            and shell_directory == target and tool_context.get("directory") == target
            and tool_context.get("sessionID") == session)


def classify(value):
    if value.get("outcome") == "ok":
        return "UNVERIFIED"
    error = value.get("error", {})
    # Work-start's public envelope has kind/message, not the internal reason code.
    if error.get("kind") == "unreachable" and "move-session route" in error.get("message", "") and "absent from this host" in error["message"]:
        return "BLOCKED"
    if error.get("kind") == "session_directory_mismatch" and "tool context still resolves" in error.get("message", ""):
        return "BLOCKED"
    return "FAIL"


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2) + "\n")


def stamp_adapter(adapter, release):
    spec = importlib.util.spec_from_file_location("fixture_installer", REPO / "scripts/install.py")
    installer = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = installer
    spec.loader.exec_module(installer)
    installer.stamp_release_constants(adapter, release)


def stage_agents(root):
    agents = root / "config/opencode/agents"
    agents.mkdir()
    for lane in ("research", "implement", "design", "review", "verify"):
        shutil.copy2(REPO / f".opencode/agents/concord-{lane}.md", agents)
    shutil.copy2(SOURCES / "fixture.md", agents)
    # session-prepare and the real host must resolve the same candidate bytes.
    (root / "home/.config").mkdir()
    (root / "home/.config/opencode").symlink_to(root / "config/opencode", target_is_directory=True)


def log(root, name, value):
    with LOCK, (root / name).open("a") as out:
        out.write(json.dumps(value) + "\n")


def command(root, env, argv, data=None, cwd=None, timeout=20, check=True):
    result = subprocess.run([str(x) for x in argv], input=None if data is None else json.dumps(data),
                            cwd=cwd or root, env=env, text=True, capture_output=True, timeout=timeout)
    log(root, "commands.jsonl", {"argv": [str(x) for x in argv], "cwd": str(cwd or root),
                                "exit": result.returncode, "stdout": result.stdout, "stderr": result.stderr})
    if check and result.returncode:
        raise RuntimeError(f"{argv[0]} {argv[1:]}: exit {result.returncode}: {result.stderr}")
    return result


def snapshot(db):
    # Observation only: all fixture authority is established through CLI commands.
    with sqlite3.connect(db.as_uri() + "?mode=ro", uri=True) as conn:
        return {name: conn.execute(f'SELECT * FROM "{name}" ORDER BY rowid').fetchall()
                for name in ("work_items", "worktree_entries", "worktree_occupancy", "linear_issue_links", "domain_events")}


def native_envelope(message):
    outputs = [p["state"].get("output", "") for p in message.get("parts", [])
               if p.get("type") == "tool" and p.get("tool") == "concord_work_start"]
    if len(outputs) != 1:
        raise RuntimeError(f"expected one actual native concord_work_start part: {message}")
    return json.loads(outputs[0].splitlines()[0])


def turn_message(final, history, session):
    info = final["info"]
    if info.get("sessionID") != session or not info.get("parentID"):
        raise RuntimeError("prompt response does not identify the requested session and turn")
    return {"info": info, "parts": [part for row in history
            if row.get("info", {}).get("sessionID") == session
            and row["info"].get("parentID") == info["parentID"]
            for part in row.get("parts", [])]}


def wait_for_host(root, opener, port, process, budget=15):
    # A listening Node socket precedes request-handler readiness. Probe only the
    # read-only health route here; never retry a native tool or a core mutation.
    deadline = time.monotonic() + budget
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError("host exited during startup; see host.log")
        request = urllib.request.Request(f"http://127.0.0.1:{port}/global/health")
        try:
            with opener.open(request, timeout=min(1, deadline - time.monotonic())) as response:
                value = json.loads(response.read())
            log(root, "readiness.jsonl", {"response": value})
            if value.get("healthy") is True:
                return
        except (OSError, ValueError) as error:
            log(root, "readiness.jsonl", {"error": str(error)})
        time.sleep(.1)
    raise TimeoutError("host did not become HTTP-ready within the explicit startup budget")


def run(args):
    root, host, cli, deps = (p.resolve() for p in (args.run_root, args.host, args.cli, args.host_deps))
    # Validate the caller's literal root before resolving away a symlink.
    create_root(args.run_root)
    for name in ("bin", "home", "tmp", "config/opencode", "data", "state", "cache", "repo", "release/bin", "release/adapter"):
        (root / name).mkdir(parents=True, exist_ok=True)
    env = isolated_env(root, host)
    write_json(root / "environment.json", env)
    if not (deps / "node_modules/@opencode-ai/plugin/package.json").is_file():
        raise ValueError("host-deps must contain already materialized @opencode-ai/plugin; no install is performed")
    host_version = command(root, env, [host, "--version"]).stdout.strip()
    package_version = json.loads((deps / "node_modules/@opencode-ai/plugin/package.json").read_text())["version"]
    if host_version != package_version:
        raise ValueError(f"public host package {package_version} does not match installed host {host_version}")
    shutil.copytree(deps / "node_modules", root / "config/opencode/node_modules", symlinks=False)
    # Only public dependency material is reused, never another run's config/auth.
    write_json(root / "config/opencode/package.json", {"dependencies": {"@opencode-ai/plugin": host_version}})
    stage_agents(root)
    shutil.copy2(cli, root / "release/bin/concord")
    binary = root / "release/bin/concord"
    for source in (REPO / "adapter/opencode").iterdir():
        if source.suffix in {".ts", ".js"} and not source.name.endswith((".test.ts", ".case.ts")):
            shutil.copy2(source, root / "release/adapter" / source.name)
    # Reuse the installer's pure stamp function on the disposable copy, not an install.
    stamp_adapter(root / "release/adapter", root / "release")
    shutil.copy2(SOURCES / "observer.ts", root / "config/opencode/observer.ts")
    (root / "bin/opencode").symlink_to(host)
    repo = root / "repo"

    def git(*argv):
        return command(root, env, ["git", *argv], cwd=repo).stdout.strip()

    def core(verb, data, cwd=repo):
        return json.loads(command(root, env, [binary, verb], data, cwd=cwd).stdout)

    (repo / "README.md").write_text("Synthetic native resume repository.\n")
    knowledge = repo / ".concord/docs/knowledge"
    knowledge.mkdir(parents=True)
    write_json(knowledge / "manifest.json", {"schema_version": "1.2", "supported_kinds": [], "indexed_kinds": [], "knowledge_roots": []})
    write_json(knowledge / "domain-registry.json", {"schema_version": "1.0", "product_key": PRODUCT, "root_domain_id": f"product-root:{PRODUCT}", "domains": [{"domain_id": f"product-root:{PRODUCT}", "name": "Synthetic root", "purpose": "Native fixture only", "status": "current", "architecture_relations": []}]})
    git("init", "-b", "main")
    git("add", ".")
    git("-c", "user.name=Synthetic", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "Synthetic fixture")
    git("init", "--bare", "--initial-branch=main", str(root / "origin.git"))
    git("remote", "add", "origin", "https://example.invalid/native-resume.git")
    git("config", f"url.{root / 'origin.git'}.insteadOf", "https://example.invalid/native-resume.git")
    git("push", "origin", "main")
    git("fetch", "origin")
    git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
    core("product-create", {"product_id": PRODUCT, "display_name": "Native fixture", "stage_maturity": "prototype", "stage_audience_commitment": "operator_only", "project_id": PROJECT, "project_display_name": "Synthetic repository", "role": "primary"})
    core("project-locator-add", {"project_id": PROJECT, "locator_id": "fixture-path", "kind": "canonical_path", "value": str(repo), "expected_version": 1})
    core("product-knowledge-home-designate", {"product_id": PRODUCT, "project_id": PROJECT, "locator_id": "fixture-path", "expected_version": 2})
    boot = core("work-bootstrap", {"product_id": PRODUCT, "project_id": PROJECT, "title": "Synthetic native resume", "value_statement": "Resolve a recorded key without replacing the host boundary.", "kind": "task", "task": "Exercise isolated native resume.", "idempotency_key": "native-fixture", "ref": "HEAD"})
    work, target = boot["work_id"], boot["worktree"]["path"]
    # Bootstrap uses the same supported operator setup and invoke routes as E2E tests.
    core("client-register", {"client_ref": env["CONCORD_CLIENT_REF"], "key_id": "fixture-key", "principal_ref": "fixture-operator", "public_key": "11qYAYKxCrfVS/7TyWQHOg7hcvPapiMlrwIaaPcHURo=", "capabilities": ["product_read", "work_define"], "product_scope": [PRODUCT], "project_scope": [PROJECT], "agent_scope": ["fixture"]})
    scope = core("project-resolve", {"directory": target, "worktree": target})
    import re
    digest = re.search(r'manifestDigest\s*=\s*"([^"]+)"', (root / "release/adapter/generated-contracts.ts").read_text()).group(1)
    linked = core("invoke", {"call_envelope": {"schema_version": "1.0", "request_id": "fixture-link", "client_ref": env["CONCORD_CLIENT_REF"], "principal_ref": "", "session_ref": "fixture-setup", "agent_ref": "fixture", "directory": target, "worktree": target, "ambient_project_id": PROJECT, "selected_product_id": PRODUCT, "scope_version": scope["scope_version"], "manifest_digest": digest}, "tool": "concord_work_define", "operation": "issue_link_record", "input": {"work_id": work, "human_key": "EX-3", "remote_issue_uuid": "00000000-0000-4000-8000-000000000003", "url": "https://linear.app/example/issue/EX-3", "idempotency_key": "fixture-link"}}, cwd=target)
    if linked.get("outcome") != "ok":
        raise RuntimeError(f"synthetic issue-link authority refused: {linked}")
    selected = core("work-resume", {"product_id": PRODUCT, "project_id": PROJECT, "issue_key": "EX-3"})
    assert selected["work_id"] == work and selected["worktree"]["path"] == target
    unknown = command(root, env, [binary, "work-resume"], {"product_id": PRODUCT, "project_id": PROJECT, "issue_key": "EX-404"}, cwd=repo, check=False)
    assert unknown.returncode == 2 and "unknown_scope" in unknown.stderr
    write_json(root / "setup.json", {"work_id": work, "target": target, "cli_selection": selected, "host_version": host_version, "candidate_source": command(root, env, ["git", "rev-parse", "HEAD"], cwd=REPO).stdout.strip(), "cli_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(), "adapter_sha256": {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in (root / "release/adapter").iterdir()}})

    class Fake(http.server.BaseHTTPRequestHandler):
        requests = 0

        def log_message(self, *unused):
            pass

        def do_POST(self):
            body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
            Fake.requests += 1
            calls = provider_calls(body) if Fake.requests <= 30 else []
            log(root, "provider.jsonl", {"path": self.path, "body": body, "calls": calls})
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()
            delta = {"tool_calls": [{"index": i, "id": f"call_fixture_{Fake.requests}_{i}", "type": "function", "function": {"name": name, "arguments": json.dumps(value)}} for i, (name, value) in enumerate(calls)]} if calls else {"content": "FIXTURE_DONE"}
            for part, finish in (({"role": "assistant"}, None), (delta, None), ({}, "tool_calls" if calls else "stop")):
                chunk = {"id": "fixture", "object": "chat.completion.chunk", "created": 1, "model": "fixture", "choices": [{"index": 0, "delta": part, "finish_reason": finish}]}
                self.wfile.write(("data: " + json.dumps(chunk) + "\n\n").encode())
            self.wfile.write(b"data: [DONE]\n\n")

    fake = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Fake)
    thread = threading.Thread(target=fake.serve_forever, daemon=True)
    thread.start()
    config = {"model": "fixture/fixture", "small_model": "fixture/fixture", "default_agent": "fixture", "autoupdate": False, "snapshot": False, "share": "disabled", "formatter": False, "lsp": False, "plugin": [(root / "release/adapter/concord-plugin.ts").as_uri(), (root / "config/opencode/observer.ts").as_uri()], "enabled_providers": ["fixture"], "provider": {"fixture": {"name": "Synthetic loopback provider", "env": [], "npm": "@ai-sdk/openai-compatible", "options": {"baseURL": f"http://127.0.0.1:{fake.server_port}/v1", "apiKey": "synthetic-not-a-credential"}, "models": {"fixture": {"name": "fixture", "tool_call": True, "limit": {"context": 100000, "output": 10000}}}}}, "permission": "allow", "agent": {"fixture": {"mode": "primary", "prompt": "Synthetic isolated host experiment. Follow scripted tool calls."}}}
    write_json(root / "opencode.json", config)
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    process = None
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def api(method, path, data=None, directory=str(repo)):
        req = urllib.request.Request(f"http://127.0.0.1:{port}" + path, data=None if data is None else json.dumps(data).encode(), method=method, headers={"Content-Type": "application/json", "x-opencode-directory": directory})
        with opener.open(req, timeout=35) as response:
            value = json.loads(response.read())
        log(root, "api.jsonl", {"method": method, "path": path, "directory": directory, "response": value})
        return value

    def prompt(session, text, directory=str(repo)):
        final = api("POST", f"/session/{session}/message", {"agent": "fixture", "model": {"providerID": "fixture", "modelID": "fixture"}, "parts": [{"type": "text", "text": text}]}, directory)
        # POST returns the final assistant, not the earlier tool-bearing steps.
        history = api("GET", f"/session/{session}/message?limit=100", directory=directory)
        return turn_message(final, history, session)

    with (root / "host.log").open("w") as output:
        try:
            process = subprocess.Popen([str(host), "serve", "--hostname", "127.0.0.1", "--port", str(port), "--print-logs"], cwd=repo, env=env, stdout=output, stderr=subprocess.STDOUT, start_new_session=True)
            wait_for_host(root, opener, port, process)
            loaded = api("GET", "/config")
            assert loaded.get("plugin") == config["plugin"] and not loaded.get("mcp")
            assert loaded.get("enabled_providers") == ["fixture"]
            owner = api("POST", "/session", {"title": "Synthetic live owner"}, target)["id"]
            prompt(owner, "OWNER_CONTEXT", target)
            owner_result = native_envelope(prompt(owner, "OWNER_RESUME " + work, target))
            sentinel = Path(target) / "owner.txt"
            assert sentinel.read_text() == "synthetic owner bytes\n"
            before = snapshot(Path(env["CONCORD_DB_PATH"]))
            unknown_id = api("POST", "/session", {"title": "Synthetic unknown key"})["id"]
            unknown_result = native_envelope(prompt(unknown_id, "UNKNOWN_KEY"))
            unknown_directory = api("GET", f"/session/{unknown_id}")["directory"]
            assert unknown_directory == str(repo) and snapshot(Path(env["CONCORD_DB_PATH"])) == before
            session = api("POST", "/session", {"title": "Synthetic keyed resume"})["id"]
            result = native_envelope(prompt(session, "RESUME_KEY"))

            def observe_context():
                readback = api("GET", f"/session/{session}")
                # Keep the original entry directory. Supplying the target header
                # would place an instance there independently of the native move.
                context = prompt(session, "READ_CONTEXT")
                shells = [p for p in context.get("parts", []) if p.get("tool") == "bash"]
                shell_directory = shells[0].get("state", {}).get("output", "").strip() if len(shells) == 1 else ""
                contexts = [p for p in context.get("parts", []) if p.get("tool") == "fixture_context"]
                tool_context = json.loads(contexts[0]["state"]["output"]) if len(contexts) == 1 else {}
                return readback, shell_directory, tool_context

            initial_result = result
            readback, shell_directory, tool_context = observe_context()
            # Follow only the named placement recovery, once, after execution
            # context (not merely metadata) proves the native move has landed.
            if can_replay(result, session, target, shell_directory, tool_context):
                result = native_envelope(prompt(session, "RESUME_KEY"))
                readback, shell_directory, tool_context = observe_context()
            after = snapshot(Path(env["CONCORD_DB_PATH"]))
            occupancy = after.get("worktree_occupancy", [])
            occupied = any(session in row for row in occupancy)
            preserved = sentinel.read_text() == "synthetic owner bytes\n" and api("GET", f"/session/{owner}", directory=target)["directory"] == target
            assert preserved
            verdict = classify(result)
            if verdict == "BLOCKED":
                expected = target if result["error"]["kind"] == "session_directory_mismatch" else str(repo)
                assert not occupied and before == after and readback["directory"] == expected
            elif (landing_matches(result, work, session, target, readback["directory"], shell_directory, occupied)
                  and tool_context.get("sessionID") == session and tool_context.get("directory") == target):
                # Own and foreign occupancy must both survive; an owner warning is not proof.
                owner_occupied = any(owner in row for row in occupancy)
                reread = core("work-resume", {"product_id": PRODUCT, "project_id": PROJECT, "work_id": work, "session_ref": session}, cwd=readback["directory"])
                verdict = "PASS" if owner_result.get("outcome") == "ok" and owner_occupied and reread["work_id"] == work and unknown_result.get("outcome") == "error" and unknown_result.get("error", {}).get("kind") == "resume_failure" and unknown_result["error"].get("effect_state") == "none" and "unknown_scope" in unknown_result["error"].get("message", "") else "FAIL"
            else:
                verdict = "FAIL"
            write_json(root / "result.json", {"verdict": verdict, "native_proof": verdict == "PASS", "work_id": work, "target": target, "session_id": session, "owner_session_id": owner, "owner_result": owner_result, "unknown_result": unknown_result, "initial_resume_result": initial_result, "resume_result": result, "host_readback": readback, "native_shell_directory": shell_directory, "native_context": tool_context, "owner_preserved": preserved, "before": before, "after": after, "limitations": ["No P3 history-removal or D13 migration proof", "CLI setup is synthetic setup, not native claim/landing evidence", "No real provider, planning service, or installed/live Concord store"]})
            print(json.dumps({"verdict": verdict, "result": str(root / "result.json"), "native_proof": verdict == "PASS"}), flush=True)
            return {"PASS": 0, "BLOCKED": 2}.get(verdict, 1)
        finally:
            if process:
                try:
                    os.killpg(process.pid, signal.SIGTERM)
                except ProcessLookupError:
                    pass
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    os.killpg(process.pid, signal.SIGKILL)
                    process.wait(timeout=5)
            fake.shutdown()
            fake.server_close()
            thread.join(timeout=2)
            ports_closed = []
            for number in (port, fake.server_port):
                with socket.socket() as sock:
                    ports_closed.append(sock.connect_ex(("127.0.0.1", number)) != 0)
            write_json(root / "cleanup.json", {"host_pid": process.pid if process else None, "host_reaped": process is None or process.poll() is not None, "provider_thread_stopped": not thread.is_alive(), "ports_closed": ports_closed})
            if not all(ports_closed) or thread.is_alive():
                raise RuntimeError("fixture cleanup incomplete")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run-root", type=Path, required=True)
    parser.add_argument("--host", type=Path, required=True)
    parser.add_argument("--cli", type=Path, required=True, help="prebuilt candidate CLI; never installed")
    parser.add_argument("--host-deps", type=Path, required=True, help="directory containing public node_modules, not config")
    parser.add_argument("--budget-seconds", type=int, default=210, choices=range(30, 241), metavar="30..240")
    args = parser.parse_args()

    def expired(*unused):
        raise TimeoutError("explicit native fixture budget exhausted")

    signal.signal(signal.SIGALRM, expired)
    signal.alarm(args.budget_seconds)
    try:
        return run(args)
    finally:
        signal.alarm(0)


if __name__ == "__main__":
    raise SystemExit(main())

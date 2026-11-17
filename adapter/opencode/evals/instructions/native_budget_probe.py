"""Opt-in, loopback-only OpenCode hook probe. This is not a spending guard."""

import argparse
import base64
import json
import os
import signal
import socket
import subprocess
import threading
import time
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


LOOPBACK = ".".join(("127", "0", "0", "1"))


PLUGIN = r'''export default async function () {
  const base = process.env.PROBE_BRIDGE;
  async function send(path, body) {
    const res = await fetch(base + path, {method: "POST", headers: {"content-type": "application/json"}, body: JSON.stringify(body)});
    if (!res.ok) throw new Error("probe preflight denied");
  }
  return {
    "chat.params": async (input, output) => { await send("/params", {input, output}); },
    event: async ({event}) => { if (event.type === "message.updated") await send("/event", event); }
  };
}'''


def request(url, data=None, timeout=45):
    raw = None if data is None else json.dumps(data).encode()
    auth = base64.b64encode(b"opencode:synthetic-probe-password").decode()
    req = urllib.request.Request(url, data=raw, headers={"Content-Type": "application/json", "Authorization": "Basic " + auth})
    with urllib.request.urlopen(req, timeout=timeout) as response:
        return json.load(response)


def run(root):
    root = Path(root).resolve()
    repository = Path(__file__).resolve().parents[4]
    if root == repository or repository in root.parents:
        raise ValueError("artifact directory must be outside repository")
    root.mkdir(mode=0o700, parents=True, exist_ok=False)
    for directory in ("home", "config", "data", "cache", "state", "repo"):
        (root / directory).mkdir(mode=0o700)
    events = []
    state = {"scenario": "setup", "calls": 0, "params": 0}
    lock = threading.Lock()

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def do_POST(self):
            body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
            with lock:
                scenario = state["scenario"]
                item = {"scenario": scenario, "path": self.path, "body": body}
                events.append(item)
                if self.path == "/params":
                    state["params"] += 1
                    item["request_id"] = state["params"]
                    status = 403 if scenario == "refuse" else 200
                    result = b'{}'
                elif self.path == "/event":
                    status, result = 200, b'{}'
                elif self.path == "/v1/chat/completions":
                    state["calls"] += 1
                    item["provider_call"] = state["calls"]
                    if scenario == "retry" and state["calls"] == 1:
                        status, result = 429, b'{"error":{"message":"rate limited","type":"rate_limit_error"}}'
                    else:
                        usage = {"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18,
                                 "prompt_tokens_details": {"cached_tokens": 0},
                                 "completion_tokens_details": {"reasoning_tokens": 0}}
                        if scenario == "absent":
                            usage = None
                        elif scenario == "partial":
                            usage = {"prompt_tokens": 11, "total_tokens": 11}
                        elif scenario == "zero":
                            usage = {"prompt_tokens": 11, "completion_tokens": 0, "total_tokens": 11}
                        elif scenario == "partial_nonzero":
                            usage = {"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18}
                        item["supplied_usage"] = usage
                        chunk = {"id": "synthetic", "object": "chat.completion.chunk", "created": 1,
                                 "model": "synthetic", "choices": [{"index": 0, "delta": {"content": "synthetic response"}, "finish_reason": None}]}
                        end = {"id": "synthetic", "object": "chat.completion.chunk", "created": 1,
                               "model": "synthetic", "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}]}
                        if usage is not None:
                            end["usage"] = usage
                        result = ("data: " + json.dumps(chunk) + "\n\ndata: " + json.dumps(end) + "\n\ndata: [DONE]\n\n").encode()
                        status = 200
                else:
                    status, result = 404, b'{}'
            self.send_response(status)
            self.send_header("Content-Type", "text/event-stream" if self.path.startswith("/v1/") and status == 200 else "application/json")
            if status == 429:
                self.send_header("Retry-After", "0")
            self.send_header("Content-Length", str(len(result)))
            self.end_headers()
            self.wfile.write(result)

    bridge = ThreadingHTTPServer((LOOPBACK, 0), Handler)
    thread = threading.Thread(target=bridge.serve_forever, daemon=True)
    thread.start()
    base = f"http://{LOOPBACK}:{bridge.server_port}"
    config_dir = root / "config" / "opencode"
    config_dir.mkdir(mode=0o700)
    plugin = config_dir / "probe.js"
    plugin.write_text(PLUGIN)
    config = {"plugin": [plugin.as_uri()], "enabled_providers": ["fake"], "autoupdate": False,
              "share": "disabled", "default_agent": "probe", "compaction": {"auto": False, "prune": False},
              "agent": {"probe": {"mode": "primary", "description": "Synthetic probe", "permission": {"*": "deny"}},
                        "title": {"disable": True}},
              "provider": {"fake": {"npm": "@ai-sdk/openai-compatible", "name": "Fake",
                                     "options": {"baseURL": base + "/v1", "apiKey": "synthetic"},
                                     "models": {"synthetic": {"name": "Synthetic", "limit": {"context": 10000, "output": 1000}}}}}}
    (config_dir / "opencode.json").write_text(json.dumps(config))
    env = {k: os.environ[k] for k in ("PATH", "LANG", "LC_ALL", "SSL_CERT_FILE", "SSL_CERT_DIR") if k in os.environ}
    env.update({"HOME": str(root / "home"), "XDG_CONFIG_HOME": str(root / "config"),
                "XDG_DATA_HOME": str(root / "data"), "XDG_CACHE_HOME": str(root / "cache"),
                "XDG_STATE_HOME": str(root / "state"), "OPENCODE_CONFIG_DIR": str(config_dir),
                "OPENCODE_DISABLE_MODELS_FETCH": "true", "OPENCODE_DISABLE_PROJECT_CONFIG": "true",
                "OPENCODE_DISABLE_EXTERNAL_SKILLS": "true", "OPENCODE_DISABLE_CLAUDE_CODE": "true",
                "OPENCODE_SERVER_PASSWORD": "synthetic-probe-password", "PROBE_BRIDGE": base})
    with socket.socket() as sock:
        sock.bind((LOOPBACK, 0))
        port = sock.getsockname()[1]
    server = f"http://{LOOPBACK}:{port}"
    process = None
    report = {"version_expected": "1.18.35", "scenarios": {}, "events": events}
    try:
        version = subprocess.run(["opencode", "--version"], env=env, capture_output=True, text=True, timeout=10, check=True).stdout.strip()
        report["version"] = version
        if version != "1.18.35":
            raise RuntimeError("unexpected OpenCode version")
        with (root / "server.log").open("w") as log:
            process = subprocess.Popen(["opencode", "serve", "--hostname", LOOPBACK, "--port", str(port)],
                                       cwd=root / "repo", env=env, stdout=log, stderr=log, start_new_session=True)
            deadline = time.monotonic() + 30
            while True:
                if process.poll() is not None:
                    raise RuntimeError("server exited")
                try:
                    request(server + "/global/health", timeout=1)
                    break
                except (OSError, TimeoutError):
                    if time.monotonic() >= deadline:
                        raise RuntimeError("server startup failed")
                    time.sleep(.1)
            for scenario in ("normal", "retry", "compact", "absent", "partial", "zero", "partial_nonzero", "refuse"):
                with lock:
                    state.update(scenario=scenario, calls=0)
                session = request(server + "/session", {} if scenario == "normal" else {"title": "synthetic"}, timeout=300)
                url = server + "/session/" + session["id"]
                result = {}
                try:
                    result["prompt"] = request(url + "/message", {"model": {"providerID": "fake", "modelID": "synthetic"},
                                               "agent": "probe", "parts": [{"type": "text", "text": "Reply briefly."}]}, timeout=90)
                    if scenario == "compact":
                        result["summarize"] = request(url + "/summarize", {"providerID": "fake", "modelID": "synthetic", "auto": False}, timeout=90)
                    result["messages"] = request(url + "/message")
                except Exception as exc:
                    result["exception"] = repr(exc)
                time.sleep(.5)
                with lock:
                    result["provider_calls"] = state["calls"]
                    result["params_calls"] = sum(e["path"] == "/params" and e["scenario"] == scenario for e in events)
                    result["message_events"] = sum(e["path"] == "/event" and e["scenario"] == scenario for e in events)
                report["scenarios"][scenario] = result
            expected = {"normal": (1, 1), "retry": (2, 2), "compact": (2, 2),
                        "absent": (1, 1), "partial": (1, 1), "zero": (1, 1),
                        "partial_nonzero": (1, 1), "refuse": (1, 0)}
            for name, (params, calls) in expected.items():
                result = report["scenarios"][name]
                if result.get("exception") or (result["params_calls"], result["provider_calls"]) != (params, calls):
                    raise RuntimeError("probe did not exercise expected hook and provider counts: " + name)
            for name in ("normal", "retry", "compact", "absent", "partial", "zero", "partial_nonzero"):
                info = report["scenarios"][name]["prompt"]["info"]
                if info.get("finish") != "stop" or info.get("error") or not info.get("time", {}).get("completed"):
                    raise RuntimeError("probe request did not complete: " + name)
            if not any(message.get("info", {}).get("summary") for message in report["scenarios"]["compact"]["messages"]):
                raise RuntimeError("probe did not complete compaction")
            def tokens(name):
                return report["scenarios"][name]["prompt"]["info"]["tokens"]
            if tokens("partial") != tokens("zero") or tokens("partial_nonzero") != tokens("normal"):
                raise RuntimeError("expected normalized-usage counterexample not observed")
    except Exception as exc:
        report["exception"] = repr(exc)
    finally:
        if process is not None:
            try:
                os.killpg(process.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait()
        bridge.shutdown()
        bridge.server_close()
        thread.join()
        (root / "report.json").write_text(json.dumps(report, indent=2))
    print(json.dumps({"artifact": str(root), "exception": report.get("exception"),
                      "counts": {k: {n: v.get(n) for n in ("provider_calls", "params_calls", "message_events", "exception")}
                                 for k, v in report["scenarios"].items()}}))
    return 1 if "exception" in report else 0


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--artifacts", required=True)
    raise SystemExit(run(parser.parse_args().artifacts))

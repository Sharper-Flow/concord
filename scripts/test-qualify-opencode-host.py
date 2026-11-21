#!/usr/bin/env python3
"""Pure checks for the OpenCode host qualification report."""
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time
from types import SimpleNamespace
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("qualify", Path(__file__).with_name("qualify-opencode-host.py"))
qualify = importlib.util.module_from_spec(spec)
spec.loader.exec_module(qualify)
inner_spec = importlib.util.spec_from_file_location("qualification_inner", Path(__file__).parent / "testdata/qualify-opencode-host/inner.py")
inner = importlib.util.module_from_spec(inner_spec)
inner_spec.loader.exec_module(inner)


class QualificationTests(unittest.TestCase):
    def setUp(self):
        self.first = {"action": "edit", "resource": "*", "effect": "deny"}
        self.second = {"action": "shell", "resource": "git *", "effect": "allow"}
        self.expected = {"agent": {"mode": "primary", "hidden": False,
                                   "permissions": [self.first, self.second]}}

    def registry(self, rules):
        return [{"id": "agent", "mode": "primary", "hidden": False, "permissions": rules}]

    def test_permissions_match_with_host_defaults(self):
        default = {"action": "*", "resource": "*", "effect": "allow"}
        self.assertEqual([], qualify.compare_agents(self.expected, self.registry([default, self.first, self.second])))

    def test_permissions_reordered(self):
        self.assertTrue(qualify.compare_agents(self.expected, self.registry([self.second, self.first])))

    def test_permissions_missing(self):
        self.assertTrue(qualify.compare_agents(self.expected, self.registry([self.first])))

    def test_extra_host_agent_allowed(self):
        registry = self.registry([self.first, self.second]) + [{"id": "host-default"}]
        self.assertEqual([], qualify.compare_agents(self.expected, registry))

    def test_exit_codes(self):
        report = {"obligations": {"permissions": {"status": "pass"}, "session_prepare": {"status": "not_run"}}}
        self.assertEqual(0, qualify.exit_code(report, ["permissions"]))
        self.assertEqual(1, qualify.exit_code(report, ["permissions", "session_prepare"]))
        report["environment_refusal"] = "bwrap unavailable"
        self.assertEqual(2, qualify.exit_code(report, ["permissions"]))

    def test_pin_parsing(self):
        document = {"packages": [{"name": "bun-types", "version": "1.4.0"},
                                 {"name": "@opencode/plugin", "version": "2.3.2"}]}
        self.assertEqual("2.3.2", qualify.pin_version(document))
        with self.assertRaises(ValueError):
            qualify.pin_version({"packages": []})
        with self.assertRaises(ValueError):
            qualify.pin_version({"packages": document["packages"] * 2})

    def test_bun_lock_trailing_commas_and_strings(self):
        lock = '{"packages":{"native":["x", {"example":"comma, }"}, "sha512-test",],},}'
        self.assertEqual("sha512-test", qualify.bun_lock_json(lock)["packages"]["native"][-1])

    def test_stored_tool_envelope(self):
        envelope = {"schema_version": "1.0", "manifest_digest": "sha256:example", "origin": "adapter",
                    "tool": "concord_product_view", "operation": "resolve", "outcome": "error",
                    "next_cursor": None, "omissions": [], "warnings": [],
                    "error": {"kind": "transport_failure", "retry_safe": True,
                              "recovery_action": {"kind": "contact_operator"}, "effect_state": "none"}}
        state = {"status": "completed", "content": [{"type": "text", "text": json.dumps(envelope)}]}
        messages = [{"content": [{"type": "tool", "name": "concord_product_view", "state": state}]}]
        self.assertEqual("pass", inner.inspect_envelope(messages)["status"])
        state["output"] = "duplicate"
        with self.assertRaises(ValueError):
            inner.inspect_envelope(messages)

    def test_session_prepare_rejects_skipped_go_test(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            agents = root / ".opencode/agents"
            agents.mkdir(parents=True)
            (agents / "Concord.md").write_text("---\nmode: primary\n---\n")
            skipped = SimpleNamespace(returncode=0, stdout="--- SKIP: TestHostRegistryQualification\nPASS\nok\tpackage\n", stderr="")
            with patch.object(qualify, "registry_probe", return_value="[]"), patch.object(qualify.subprocess, "run", return_value=skipped):
                result = qualify.qualify_session_prepare(root, root, root)
            self.assertEqual("fail", result["status"])
            passed = SimpleNamespace(returncode=0, stdout="--- PASS: TestHostRegistryQualification (0.01s)\nPASS\n", stderr="")
            with patch.object(qualify, "registry_probe", return_value="[]"), patch.object(qualify.subprocess, "run", return_value=passed) as runner:
                result = qualify.qualify_session_prepare(root, root, root)
            self.assertEqual("pass", result["status"])
            self.assertIn("-v", runner.call_args.args[0])
            passed.stdout += "    --- SKIP: TestHostRegistryQualification/missing.json (0.00s)\n"
            with patch.object(qualify, "registry_probe", return_value="[]"), patch.object(qualify.subprocess, "run", return_value=passed):
                result = qualify.qualify_session_prepare(root, root, root)
            self.assertEqual("fail", result["status"])

    def test_background_requires_child_idle_and_matching_report(self):
        def task(agent, child):
            return {"type": "tool", "name": "subagent", "state": {"status": "completed", "input": {"agent": agent},
                    "metadata": {"status": "running", "sessionID": child}}}
        messages = [("ses_parent", "assistant", {"content": [task("concord-implement", "ses_child")]}),
                    ("ses_child", "assistant", {"content": [task("concord-explore", "ses_nested")]}),
                    ("ses_child", "idle", {}),
                    ("ses_parent", "user", {"text": '<subagent sessionID="ses_child" state="completed">done</subagent>'})]
        self.assertEqual("pass", inner.inspect_background(messages, "ses_parent")["status"])
        self.assertIsNone(inner.inspect_background(messages[:-1], "ses_parent"))

    def abort_proof(self, **error):
        return {"status": "pass", "origin": "adapter", "outcome": "error",
                "error": {"kind": "cancelled", "retry_safe": True,
                          "recovery_action": {"kind": "retry_same_request"}, "effect_state": "none",
                          "adapter_reason": "cancelled_no_effect", **error}}

    def test_abort_accepts_cancelled_read(self):
        self.assertTrue(inner.abort_passed(self.abort_proof(), alive=False))

    def test_abort_rejects_unlawful_outcomes(self):
        self.assertFalse(inner.abort_passed(self.abort_proof(), alive=True))
        self.assertFalse(inner.abort_passed(self.abort_proof(effect_state="possible"), alive=False))
        self.assertFalse(inner.abort_passed(self.abort_proof(retry_safe=False), alive=False))
        self.assertFalse(inner.abort_passed(self.abort_proof(kind="operation_conflict", adapter_reason="unknown_effect"), alive=False))
        self.assertFalse(inner.abort_passed(self.abort_proof(kind="transport_failure", adapter_reason="io_failure"), alive=False))
        self.assertFalse(inner.abort_passed(self.abort_proof(recovery_action={"kind": "contact_operator"}), alive=False))
        self.assertFalse(inner.abort_passed(self.abort_proof(adapter_reason="spawn_failure"), alive=False))
        self.assertFalse(inner.abort_passed({**self.abort_proof(), "outcome": "ok"}, alive=False))
        self.assertFalse(inner.abort_passed({**self.abort_proof(), "status": "fail"}, alive=False))


STUB_CORE = """#!/usr/bin/env python3
import json
import sys

data = sys.stdin.buffer.read()
argv = sys.argv[1:]
if argv == ["host-lease"]:
    print(json.dumps({"note": "lease preamble"}))
    print(json.dumps({"pid": 1, "pid_start": 2, "release_root": "/release",
                      "core_binary": "/release/bin/concord", "schema_version": 1,
                      "manifest_digest": "sha256:stub"}))
elif argv == ["invoke"]:
    print(json.dumps({"schema_version": "1.0", "manifest_digest": "sha256:stub", "origin": "core",
                      "tool": "concord_product_view", "operation": "resolve", "outcome": "error",
                      "next_cursor": None, "omissions": [], "warnings": [],
                      "error": {"kind": "stale_context", "retry_safe": True,
                                "recovery_action": {"kind": "refresh_context"},
                                "effect_state": "none", "message": "scope version is stale"}}))
else:
    print(json.dumps({"argv": argv, "stdin": data.decode()}))
"""


class CoreShimTests(unittest.TestCase):
    def setUp(self):
        import subprocess
        self.subprocess = subprocess
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        root = Path(self.directory.name)
        self.shim_dir = root / "shim"
        self.shim_dir.mkdir()
        self.shim = root / "release/bin/concord-shim"
        self.shim.parent.mkdir(parents=True)
        shutil.copy(Path(__file__).parent / "testdata/qualify-opencode-host/core-shim.py", self.shim)
        self.real = self.shim.with_name("concord")
        self.real.write_text(STUB_CORE)
        self.real.chmod(0o755)
        self.mode = self.shim_dir / "mode"
        self.mode.write_text("record")
        self.env = dict(os.environ, QUALIFICATION_SHIM_DIR=str(self.shim_dir))

    def run_shim(self, *argv, stdin="{}"):
        return self.subprocess.run([sys.executable, str(self.shim), *argv], input=stdin,
                                   capture_output=True, text=True, timeout=20, env=self.env, check=False)

    def test_host_lease_rewrites_only_core_binary(self):
        result = self.run_shim("host-lease", stdin='{"pid": 123, "directory": "", "worktree": ""}')
        self.assertEqual(0, result.returncode, result.stderr)
        lines = result.stdout.splitlines()
        self.assertEqual(["lease preamble"], [json.loads(line)["note"] for line in lines[:-1]])
        lease = json.loads(lines[-1])
        self.assertEqual(str(self.shim), lease["core_binary"])
        self.assertEqual("/release", lease["release_root"])
        self.assertEqual("sha256:stub", lease["manifest_digest"])
        self.assertEqual(1, lease["pid"])
        calls = [json.loads(line) for line in (self.shim_dir / "calls.jsonl").read_text().splitlines()]
        self.assertEqual([["host-lease"]], [call["argv"] for call in calls])

    def test_record_mode_execs_the_real_core(self):
        result = self.run_shim("project-resolve", stdin="payload")
        self.assertEqual(0, result.returncode, result.stderr)
        self.assertEqual({"argv": ["project-resolve"], "stdin": "payload"}, json.loads(result.stdout))

    def test_sleep_mode_publishes_pid_before_sleeping(self):
        (self.shim_dir / "sleeping").unlink(missing_ok=True)
        self.mode.write_text("sleep")
        process = self.subprocess.Popen([sys.executable, str(self.shim), "project-resolve"], stdin=subprocess.PIPE,
                                        stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, env=self.env)
        try:
            process.stdin.write("{}")
            process.stdin.close()
            deadline = time.monotonic() + 10
            while not (self.shim_dir / "sleeping").exists() and time.monotonic() < deadline:
                time.sleep(0.02)
            sleeping = self.shim_dir / "sleeping"
            self.assertTrue(sleeping.exists(), "shim never announced its sleep")
            self.assertEqual(process.pid, int(sleeping.read_text()))
            self.assertIsNone(process.poll(), "shim exited before the interrupt could land")
        finally:
            process.kill()
            process.wait(timeout=10)

    def test_oversize_mode_fabricates_context_and_inflates_one_read(self):
        self.mode.write_text("oversize")
        resolved = self.run_shim("project-resolve")
        self.assertEqual(0, resolved.returncode, resolved.stderr)
        context = json.loads(resolved.stdout)
        self.assertEqual({"project_id", "product_ids", "scope_version", "main_worktree"}, set(context))
        self.assertEqual(["qualification"], context["product_ids"])
        inflated = self.run_shim("invoke", stdin='{"call_envelope": {}}')
        self.assertEqual(0, inflated.returncode, inflated.stderr)
        envelope = json.loads(inflated.stdout)
        self.assertEqual("core", envelope["origin"])
        self.assertEqual("error", envelope["outcome"])
        self.assertEqual(52000, len(envelope["error"]["details"]["qualification"]))
        oversized = len(inflated.stdout.encode())
        self.assertGreater(oversized, 51200)
        self.assertEqual(str(oversized), (self.shim_dir / "oversize-bytes").read_text())
        captured = json.loads((self.shim_dir / "real-response").read_text())
        self.assertEqual("stale_context", captured["error"]["kind"])
        self.assertNotIn("details", captured["error"])


if __name__ == "__main__":
    unittest.main()

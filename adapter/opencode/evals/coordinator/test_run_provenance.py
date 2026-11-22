"""Run artifact provenance tests with the model process stubbed, not behavioral evidence."""
import argparse
import copy
import hashlib
import io
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path
from unittest import mock

import run
from evaluation import typed_boundary
from scenarios import SCENARIOS, RUNTIME

REPO = Path(__file__).resolve().parents[4]


def sha(data):
    return hashlib.sha256(data).hexdigest()


class RunProvenanceTests(unittest.TestCase):
    def setUp(self):
        self.repo = Path(tempfile.mkdtemp(prefix="coordinator-repo-"))
        self.artifacts = Path(tempfile.mkdtemp(prefix="coordinator-artifacts-"))
        for relative in run.PRODUCTION_SOURCES:
            target = self.repo / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(REPO / relative, target)

    def tearDown(self):
        shutil.rmtree(self.repo, ignore_errors=True)
        shutil.rmtree(self.artifacts, ignore_errors=True)

    def args(self):
        return argparse.Namespace(repo=self.repo, sdk_tool=Path("/synthetic/sdk/index.js"),
                                  model="synthetic/model", artifacts_dir=self.artifacts,
                                  profile={"model": "synthetic/model"}, binary="/synthetic/bin", profile_digest="profile-hash")

    def run_case(self, during=None):
        def stub(launcher, command, root, stem, timeout):
            (root / f"{stem}.jsonl").write_text("")
            if during is not None:
                during()
            return 0
        with mock.patch.object(run, "command_to_files", side_effect=stub), mock.patch.object(run.sandbox, "Launcher") as launcher:
            launcher.return_value.verify_version.return_value = {"version": run.sandbox.VERSION, "sha256": "binary-hash"}
            return run.run_case(self.args(), "native-route-discovery", "coordinator body", {"rules.md": b"rule"})

    def test_model_run_refuses_missing_binary_or_unsafe_profile_before_artifacts(self):
        (self.repo / ".concord/instructions").mkdir(parents=True)
        (self.repo / ".concord/instructions/rules.md").write_bytes(b"rule")
        source = self.artifacts / "coordinator.md"
        source.write_text("coordinator body")
        argv = ["run.py", "--repo", str(self.repo), "--agent-source", str(source),
                "--sdk-tool", "/synthetic/sdk/tool.js", "--model", "synthetic/model",
                "--artifacts-dir", str(self.artifacts)]
        inside = self.repo / "provider.json"
        inside.write_text(json.dumps({"model": "synthetic/model"}))
        for extra, env, expected in (([], {}, "provider-profile"),
                                    (["--provider-profile", str(source)], {"CONCORD_EVAL_OPENCODE_BIN": ""}, "CONCORD_EVAL_OPENCODE_BIN"),
                                    (["--provider-profile", str(inside)], {"CONCORD_EVAL_OPENCODE_BIN": sys.executable},
                                     "outside the repository")):
            output = io.StringIO()
            with mock.patch.object(sys, "argv", argv + extra), mock.patch.dict(os.environ, env, clear=True), \
                    mock.patch.object(run.sandbox, "Launcher") as spawn, redirect_stderr(output), self.assertRaises(SystemExit):
                run.main()
            self.assertIn(expected, output.getvalue())
            spawn.assert_not_called()
            self.assertEqual(list(self.artifacts.iterdir()), [source])

    def test_run_and_export_use_same_sandbox_and_v2_identity(self):
        commands = []
        def stub(launcher, command, root, stem, timeout):
            commands.append((launcher, command))
            data = '{"sessionID":"synthetic-session"}\n' if stem == "events" else json.dumps({"messages": [
                {"type": "assistant", "agent": "coordinator-probe", "model": {"providerID": "synthetic", "id": "model"}}]})
            (root / f"{stem}.jsonl").write_text(data)
            return 0
        with mock.patch.object(run, "command_to_files", side_effect=stub), mock.patch.object(run.sandbox, "Launcher") as factory:
            factory.return_value.verify_version.return_value = {"version": run.sandbox.VERSION, "sha256": "binary-hash"}
            args = self.args()
            args.profile.update({"plugins": ["/synthetic/provider-plugin"], "providers": {"synthetic": {"name": "Fixture"}}})
            result = run.run_case(args, "native-route-discovery", "coordinator body", {"rules.md": b"rule"})
        self.assertTrue(result["identity_verified"])
        self.assertEqual(commands[0][1][:2], ["--standalone", "run"])
        self.assertEqual(commands[1][1], ["--standalone", "session", "export", "synthetic-session", "--sanitize"])
        self.assertIs(commands[0][0], commands[1][0])
        config = json.loads((Path(result["artifact_dir"]) / "opencode.json").read_text())
        self.assertEqual(config["share"], "disabled")
        self.assertEqual(config["update"], "disable")
        self.assertFalse(config["compaction"]["auto"])
        self.assertTrue(config["agents"]["title"]["disabled"])
        self.assertEqual(config["plugins"][-1], "/synthetic/provider-plugin")
        self.assertEqual(config["providers"], {"synthetic": {"name": "Fixture"}})
        self.assertEqual(result["opencode"]["sha256"], "binary-hash")
        self.assertEqual(result["provider_profile_sha256"], "profile-hash")

    def test_variant_model_identity_is_verified_against_the_base_model_and_variant(self):
        for requested, recorded, verified in (("synthetic/model#high", "high", True), ("synthetic/model#high", "low", False),
                                              ("synthetic/model#high", None, False), ("synthetic/model", "default", True)):
            def stub(launcher, command, root, stem, timeout):
                model = {"providerID": "synthetic", "id": "model", **({"variant": recorded} if recorded else {})}
                data = '{"sessionID":"synthetic-session"}\n' if stem == "events" else json.dumps({"messages": [
                    {"type": "assistant", "agent": "coordinator-probe", "model": model}]})
                (root / f"{stem}.jsonl").write_text(data)
                return 0
            with self.subTest(requested=requested, recorded=recorded), \
                    mock.patch.object(run, "command_to_files", side_effect=stub), mock.patch.object(run.sandbox, "Launcher") as factory:
                factory.return_value.verify_version.return_value = {"version": run.sandbox.VERSION, "sha256": "binary-hash"}
                args = self.args()
                args.model = args.profile["model"] = requested
                result = run.run_case(args, "native-route-discovery", "coordinator body", {"rules.md": b"rule"})
                self.assertIs(result["identity_verified"], verified)
                self.assertEqual(result["runtime_identities"], [{"agent": "coordinator-probe", "model": "synthetic/model", "variant": recorded}])

    def test_one_shot_commands_use_launcher_environment(self):
        binary = self.artifacts / "binary"
        binary.write_text("fixture")
        launcher = run.sandbox.Launcher(self.artifacts, binary, {"env": {"SYNTHETIC_KEY": "value"}})
        for command in (["--standalone", "run"],
                        ["--standalone", "session", "export", "fixture"]):
            with mock.patch.dict(os.environ, {"PRIVATE_CALLER_SECRET": "must-not-leak"}), \
                    mock.patch.object(run.subprocess, "Popen") as popen:
                popen.return_value.wait.return_value = 0
                self.assertEqual(run.command_to_files(launcher, command, self.artifacts, "fixture", 10), 0)
            argv = popen.call_args.args[0]
            self.assertEqual(popen.call_args.kwargs["env"], {})
            self.assertNotIn("must-not-leak", repr(popen.call_args))
            self.assertIn("--args", argv)

    def test_result_records_production_and_tool_source_hashes(self):
        result = self.run_case()
        root = Path(result["artifact_dir"])
        self.assertTrue(result["snapshots_unchanged"])
        self.assertEqual(set(result["production_source_sha256"]), set(run.PRODUCTION_SOURCES))
        for relative, value in result["production_source_sha256"].items():
            self.assertEqual(value, sha((self.repo / relative).read_bytes()))
            snapshot = root / "production-sources" / relative.replace("/", "__")
            self.assertEqual(sha(snapshot.read_bytes()), value)
        self.assertEqual(set(result["tool_source_sha256"]), {
            ".opencode/plugins/coordinator/index.ts",
            ".opencode/plugins/coordinator/concord-plugin.ts",
            ".opencode/plugins/coordinator/concord.ts",
            ".opencode/plugins/coordinator/runtime_status.ts",
            ".opencode/plugins/coordinator/recording-tool.ts"})
        for relative, value in result["tool_source_sha256"].items():
            self.assertEqual(sha((root / relative).read_bytes()), value)
        self.assertEqual(json.loads((root / "result.json").read_text())["production_source_sha256"],
                         result["production_source_sha256"])

    @unittest.skipUnless(shutil.which("bun"), "Bun is required to load the production module")
    def test_staged_production_module_loads_its_dependencies(self):
        result = self.run_case()
        module = Path(result["artifact_dir"]) / "production-tree/adapter/opencode/concord.ts"
        process = subprocess.run(["bun", "-e", "import(process.argv[1]).then(m => { if (!m.registeredInputSchema) process.exit(1) })", str(module)],
                                 capture_output=True, text=True, timeout=30)
        self.assertEqual(process.returncode, 0, process.stderr)

    def test_conduct_corpus_resolves_in_the_repository(self):
        corpus = [path.name for path in (REPO / run.CONDUCT_CORPUS).glob("*.md") if path.name != "README.md"]
        self.assertIn("evidence.md", corpus)

    def test_production_source_change_during_a_run_fails_the_snapshot_check(self):
        target = self.repo / "adapter/opencode/concord.ts"
        result = self.run_case(during=lambda: target.write_text(target.read_text() + "\n// changed\n"))
        self.assertFalse(result["snapshots_unchanged"])
        self.assertFalse(result["passed"])

    def test_tool_source_change_during_a_run_fails_the_snapshot_check(self):
        def change():
            [root] = list(self.artifacts.iterdir())
            tool = root / ".opencode/plugins/coordinator/concord.ts"
            tool.write_text(tool.read_text() + "\n// changed\n")
        result = self.run_case(during=change)
        self.assertFalse(result["snapshots_unchanged"])

    def test_result_json_carries_the_non_gating_advisory_object(self):
        # Contract v2: run.py writes the evaluator's advisory object to
        # result.json, and the strict output rule is no gating check.
        result = self.run_case()
        recorded = json.loads((Path(result["artifact_dir"]) / "result.json").read_text())
        self.assertEqual(recorded.get("advisory"), {"strict_output_compliance": False})
        self.assertNotIn("strict_output_compliance", recorded["checks"])

    def test_run_model_prints_the_advisory_object_on_the_per_case_line(self):
        # Contract v2: the printed per-case line reports the advisory object
        # beside the gating checks.
        (self.repo / ".concord/instructions").mkdir(parents=True)
        (self.repo / ".concord/instructions/rules.md").write_bytes(b"rule")
        source = self.artifacts / "coordinator.md"
        source.write_text("coordinator body")
        args = argparse.Namespace(repo=self.repo, agent_source=source, sdk_tool=Path("/synthetic/sdk/tool.js"),
                                  model="synthetic/model", artifacts_dir=self.artifacts, all=False, scenario=None)
        sdk = self.artifacts / "sdk.js"
        sdk.write_text("")
        profile = self.artifacts / "profile.json"
        profile.write_text(json.dumps({"model": "synthetic/model"}))
        args.sdk_tool, args.provider_profile = sdk, profile
        result = {"scenario": "input-correction", "passed": True, "artifact_dir": "/synthetic/dir",
                  "checks": {"final_response": True},
                  "advisory": {"strict_output_compliance": False},
                  "final_response": {"status": "completed", "work_id": "synthetic-work"}}
        buffer = io.StringIO()
        with mock.patch.object(run, "run_case", return_value=result), mock.patch.dict(os.environ, {"CONCORD_EVAL_OPENCODE_BIN": sys.executable}), \
                redirect_stdout(buffer):
            self.assertEqual(run.run_model(args, argparse.ArgumentParser()), 0)
        printed = json.loads(buffer.getvalue().strip())
        self.assertEqual(printed["advisory"], {"strict_output_compliance": False})
        self.assertNotIn("strict_output_compliance", printed["checks"])


# The boundary sources in the precedence evaluation.typed_boundary applies.
BOUNDARY_SOURCE_MARKERS = (
    "boundary field",
    "error's kind",
    "the name of the tool",
)


def format_boundary_clause():
    """The boundary clause of run.FORMAT, whitespace-normalized."""
    return " ".join(run.FORMAT.split("boundary:", 1)[1].split("cause:", 1)[0].split())


def served_error(result):
    error = result.get("error") if isinstance(result, dict) else None
    return error if isinstance(error, dict) else None


def named_string(error, key):
    value = error.get(key)
    return value if isinstance(value, str) and value else None


def instruction_boundary(case):
    """The boundary the FORMAT boundary instruction tells the coordinator to
    name for a served stopping observation, applying the sources in the
    order the clause states them."""
    clause = format_boundary_clause()
    required = case.get("required", [])
    if not required:
        return None
    tool = required[-1]
    error = served_error(case.get("responses", {}).get(tool))
    if error is None:
        return None  # a successful task names no boundary
    extractors = {
        "boundary field": lambda error: named_string(error, "boundary"),
        "error's kind": lambda error: named_string(error, "kind"),
        "the name of the tool": lambda error: tool,
    }
    stated = sorted((marker for marker in BOUNDARY_SOURCE_MARKERS if marker in clause), key=clause.find)
    for marker in stated:
        value = extractors[marker](error)
        if value is not None:
            return value
    return None


class FormatBoundaryInstructionTests(unittest.TestCase):
    """The served FORMAT boundary instruction must state the evaluator's
    precedence. The evaluator measures the boundary the served typed
    observation names, so an instruction that sends the model elsewhere is a
    defect in the instruction, not in the model."""

    def test_boundary_clause_states_the_evaluator_precedence(self):
        clause = format_boundary_clause()
        positions = {marker: clause.find(marker) for marker in BOUNDARY_SOURCE_MARKERS}
        missing = [marker for marker, position in positions.items() if position < 0]
        self.assertEqual(missing, [])
        ordered = [positions[marker] for marker in BOUNDARY_SOURCE_MARKERS]
        self.assertEqual(ordered, sorted(ordered),
                         f"boundary clause states sources out of precedence order: {positions}")
        self.assertIn("null when the task succeeded", clause)

    def test_boundary_clause_agrees_with_the_evaluator_on_served_observations(self):
        # Both sides read the same served error, never a model output.
        for name, case in SCENARIOS.items():
            with self.subTest(name=name):
                self.assertEqual(instruction_boundary(case), typed_boundary(case))

    def test_served_refusal_without_typed_names_agrees_on_the_tool_name(self):
        # The recording double's own refusal constant carries no boundary
        # field, no complete core diagnostic, and no kind; instruction and
        # evaluator then both fall back to the stopping tool name.
        case = copy.deepcopy(SCENARIOS["repair-readback"])
        case["responses"][RUNTIME] = {"outcome": "error", "error": {
            "reason_code": "authorization_denied", "effect_state": "none",
            "message": "Outside the fixture grant."}}
        self.assertEqual(instruction_boundary(case), RUNTIME)
        self.assertEqual(typed_boundary(case), RUNTIME)


if __name__ == "__main__":
    unittest.main()

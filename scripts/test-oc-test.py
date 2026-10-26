#!/usr/bin/env python3
"""Exercise the verification wrapper in an isolated fake command environment."""

import json
import importlib.util
import os
from pathlib import Path
import re
import resource
import shlex
import shutil
import subprocess
import sys
import tempfile
import unittest


_plan_spec = importlib.util.spec_from_file_location("local_test_plan", Path(__file__).with_name("local-test-plan.py"))
PLANNER = importlib.util.module_from_spec(_plan_spec)
_plan_spec.loader.exec_module(PLANNER)
_read_spec = importlib.util.spec_from_file_location("read_local_go_results", Path(__file__).with_name("read-local-go-results.py"))
GO_READER = importlib.util.module_from_spec(_read_spec)
_read_spec.loader.exec_module(GO_READER)


# The deployed oc-test-gate accepts these admission classes and exits 2 on any
# other first argument. The fake gate below reproduces that contract, so a tier
# name passed where an admission class belongs fails here instead of on a
# developer machine.
GATE_ADMISSION_CLASSES = ("targeted", "smoke", "full")

FAKE_GATE = f"""#!/bin/bash
case "$1" in
  {'|'.join(GATE_ADMISSION_CLASSES)})
    printf '%s' "$1" >"$GATE_CLASS_RECORD"
    ;;
  *)
    echo "unknown tier: $1" >&2
    exit 2
    ;;
esac
while [[ "$1" != -- ]]; do shift; done
shift
exec "$@"
"""

FAKE_COMMAND = f"""#!{sys.executable}
import json
import os
from pathlib import Path
import resource
import sys

name = Path(sys.argv[0]).name
if name == "python3" and len(sys.argv) > 1 and sys.argv[1].endswith("/scripts/local-test-plan.py"):
    os.execv(sys.executable, [sys.executable, *sys.argv[1:]])
with open(os.environ["COMMAND_RECORD"], "a") as record:
    record.write(json.dumps({{
        "command": name,
        "args": sys.argv[1:],
        "cwd": os.getcwd(),
        "selected_product": os.environ.get("CONCORD_SELECTED_PRODUCT_ID"),
        "require_bun": os.environ.get("CONCORD_REQUIRE_BUN"),
        "conformance_long": os.environ.get("CONCORD_CONFORMANCE_LONG"),
        "fsize": resource.getrlimit(resource.RLIMIT_FSIZE),
    }}) + "\\n")
if os.environ.get("FAIL_COMMAND") == name:
    sys.exit(7)
if name == "go" and sys.argv[1:2] == ["list"]:
    print(os.environ["GO_LIST_OUTPUT"])
"""

REPOSITORY = Path(__file__).resolve().parents[1]
MODULE = "github.com/sharper-flow/concord"
# What the fake `go list ./...` reports: the two packages CI tests in shard
# jobs, and two it tests in one run.
GO_LIST_OUTPUT = "\n".join(f"{MODULE}/{package}" for package in (
    "cmd/concord", "internal/agent", "internal/store", "scripts/support",
))
# The wrapper keeps a stricter limit inherited from whatever runs this suite.
_INHERITED_HARD = resource.getrlimit(resource.RLIMIT_FSIZE)[1]
DEFAULT_FILE_LIMIT = [1024 * 1024 * 1024 if _INHERITED_HARD == resource.RLIM_INFINITY
                      else min(1024 * 1024 * 1024, _INHERITED_HARD)] * 2
# Writes 3 MiB, then exits 0: past a 1 MiB limit, yet bounded if no limit holds.
RUNAWAY_WRITER = ["/bin/bash", "-c", 'for _ in $(seq 3072); do printf "%1024s" ""; done >"$SCRATCH/runaway"']
UNSHARDED_PACKAGES = [f"{MODULE}/internal/agent", f"{MODULE}/scripts/support"]
CI_UNSHARDED_STEP = "Test every package without its own shard job"


def workflow_step_run(workflow: str, step_name: str) -> str:
    """The single-line `run:` of the workflow step with this exact `name:`."""
    lines = workflow.splitlines()
    starts = [index for index, line in enumerate(lines)
              if re.fullmatch(r"\s*- name: " + re.escape(step_name), line)]
    if len(starts) != 1:
        raise AssertionError(f"expected one step named {step_name!r}, found {len(starts)}")
    start = starts[0]
    step_indent = len(lines[start]) - len(lines[start].lstrip())
    for line in lines[start + 1:]:
        indent = len(line) - len(line.lstrip())
        if line.strip() and indent <= step_indent:
            break
        if indent == step_indent + 2 and line.lstrip().startswith("run:"):
            command = line.split("run:", 1)[1].strip()
            tokens = shlex.split(command)
            # The native-results producer owns reporting, not the workload.
            # Accept only its declared CLI, not arbitrary wrapper stripping.
            if tokens[:3] == ["python3", "scripts/go-test-results.py", "--output-dir"]:
                if len(tokens) < 7 or tokens[4:7] != ["--", "go", "test"]:
                    raise AssertionError(f"unexpected results wrapper: {command}")
                return command.split(" -- ", 1)[1]
            return command
    raise AssertionError(f"step {step_name!r} has no run")


def smoke_go_command(wrapper: str) -> str:
    """The Go line of the smoke tier's script in bin/oc-test."""
    block = re.search(r"^  smoke\)\n(.*?)^    ;;$", wrapper, re.MULTILINE | re.DOTALL)
    if block is None:
        raise AssertionError("bin/oc-test has no smoke tier")
    commands = [line.strip() for line in block.group(1).splitlines() if line.strip().startswith("go ")]
    if len(commands) != 1:
        raise AssertionError(f"expected one Go command in the smoke tier, found {commands}")
    return commands[0]


class PhaseReportingTest(unittest.TestCase):
    def run_conformance(self, failure, with_gate=True):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "bin").mkdir()
            shutil.copyfile(REPOSITORY / "bin/oc-test", root / "bin/oc-test")
            for name in ("go",):
                command = root / "bin" / name
                command.write_text('#!/bin/bash\nif [[ "${FAIL_STAGE:-}" == "$1" ]]; then exit 7; fi\nexit 0\n')
                command.chmod(0o700)
            record = root / "gate-class"
            if with_gate:
                gate = root / "bin/oc-test-gate"
                gate.write_text(FAKE_GATE)
                gate.chmod(0o700)
            path = str(root / "bin") + os.pathsep + (os.environ["PATH"] if with_gate else "/usr/bin:/bin")
            env = dict(os.environ, PATH=path, FAIL_STAGE=failure, GATE_CLASS_RECORD=str(record))
            result = subprocess.run(["bash", str(root / "bin/oc-test"), "conformance"], env=env, text=True, capture_output=True, check=False)
            result.gate_class = record.read_text() if record.exists() else ""
            return result

    def test_success_reports_conformance_command(self):
        result = self.run_conformance("")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("START conformance", result.stdout)
        self.assertIn("PASS conformance", result.stdout)

    def test_failure_preserves_exit_code_and_stops(self):
        result = self.run_conformance("test")
        self.assertEqual(result.returncode, 7)
        self.assertIn("FAIL conformance", result.stderr)
        self.assertNotIn("PASS conformance", result.stdout)

    def test_gate_receives_a_supported_admission_class(self):
        result = self.run_conformance("")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn(result.gate_class, GATE_ADMISSION_CLASSES)
        self.assertNotIn("unknown tier", result.stderr)

    def test_missing_gate_refuses_heavy_work(self):
        result = self.run_conformance("", with_gate=False)
        self.assertEqual(result.returncode, 69, result.stderr)
        self.assertIn("refusing an unthrottled heavy workload", result.stderr)
        self.assertNotIn("PASS conformance", result.stdout)


class TierRoutingTest(unittest.TestCase):
    def run_wrapper(self, *args, failure="", with_gate=True, commands=("go", "bun", "python3", "probe", "lefthook"),
                    env_extra=None, preexec_fn=None):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "bin").mkdir()
            (root / "scripts").mkdir()
            shutil.copyfile(REPOSITORY / "bin/oc-test", root / "bin/oc-test")
            shutil.copyfile(REPOSITORY / "scripts/local-test-plan.py", root / "scripts/local-test-plan.py")
            for name in commands:
                command = root / "bin" / name
                command.write_text(FAKE_COMMAND)
                command.chmod(0o700)
            gate_record = root / "gate-class"
            command_record = root / "commands.jsonl"
            if with_gate:
                gate = root / "bin/oc-test-gate"
                gate.write_text(FAKE_GATE)
                gate.chmod(0o700)
            env = dict(os.environ,
                       PATH=str(root / "bin") + os.pathsep + "/usr/bin:/bin",
                       FAIL_COMMAND=failure,
                       GATE_CLASS_RECORD=str(gate_record),
                       COMMAND_RECORD=str(command_record),
                       GO_LIST_OUTPUT=GO_LIST_OUTPUT,
                       CONCORD_SELECTED_PRODUCT_ID="poison-product")
            for key in ("CONCORD_REQUIRE_BUN", "CONCORD_CONFORMANCE_LONG", "OC_TEST_MAX_FILE_MIB"):
                env.pop(key, None)
            env.update(env_extra or {}, SCRATCH=str(root))
            result = subprocess.run(["/bin/bash", str(root / "bin/oc-test"), *args],
                                    cwd=root / "bin", env=env, text=True,
                                    capture_output=True, check=False, preexec_fn=preexec_fn)
            result.gate_class = gate_record.read_text() if gate_record.exists() else ""
            result.calls = [json.loads(line) for line in command_record.read_text().splitlines()] if command_record.exists() else []
            result.repo_root = str(root)
            runaway = root / "runaway"
            result.runaway_size = runaway.stat().st_size if runaway.exists() else None
            return result

    def test_smoke_runs_go_adapter_and_json_under_smoke_admission(self):
        result = self.run_wrapper("smoke")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.gate_class, "smoke")
        self.assertEqual([(call["command"], call["args"]) for call in result.calls], [
            ("go", ["list", "./..."]),
            ("go", ["test", "-timeout=15m", *UNSHARDED_PACKAGES]),
            ("bun", ["test", "adapter/opencode/"]),
            ("python3", ["scripts/check-json.py", "--adapter-tests-external"]),
        ])
        self.assertTrue(all(call["cwd"] == result.repo_root for call in result.calls))
        self.assertIn("START smoke", result.stdout)
        self.assertIn("WAIT admission: smoke", result.stdout)
        self.assertIn("PASS smoke", result.stdout)

    def test_smoke_go_step_is_the_ci_step_for_unsharded_packages(self):
        # CI owns which packages run as shard jobs. Smoke runs CI's own
        # command for the rest, so a package CI moves into shards leaves
        # smoke in the same change, and smoke cannot time out on it.
        workflow = (REPOSITORY / ".github/workflows/ci.yml").read_text()
        wrapper = (REPOSITORY / "bin/oc-test").read_text()
        self.assertEqual(smoke_go_command(wrapper), workflow_step_run(workflow, CI_UNSHARDED_STEP))

    def test_smoke_docs_name_the_ci_shards(self):
        # The header and README name the packages smoke leaves out and give a
        # one-shard command. Both copy CI's shard jobs, so they must change
        # when a package is sharded or its shard count moves.
        workflow = (REPOSITORY / ".github/workflows/ci.yml").read_text()
        counts = {}
        for package, _, count in re.findall(r"go-test-shard\.py \./(\S+) (\d+) (\d+)\)", workflow):
            counts.setdefault(package, set()).add(count)
        self.assertIn("internal/store", counts)
        store_count = counts["internal/store"].pop()
        example = f'go-test-shard.py ./internal/store 1 {store_count})" ./internal/store'
        prose = {"bin/oc-test": f"shard 1 of {store_count}:", "README.md": f"shard 1 of the {store_count} `internal/store` shards"}
        for path, phrase in prose.items():
            with self.subTest(path=path):
                text = (REPOSITORY / path).read_text()
                for package in counts:
                    name = f"`{package}`" if path == "README.md" else package
                    self.assertTrue(name in text, f"{path} does not name the sharded package {name}")
                self.assertTrue(example in text, f"{path} lacks the one-shard example {example}")
                self.assertEqual(set(re.findall(r"go-test-shard\.py \./internal/store \d+ (\d+)", text)), {store_count})
                self.assertTrue(phrase in text, f"{path} does not say {phrase}")

    def test_smoke_isolates_product_selection_for_adapter_and_json(self):
        result = self.run_wrapper("smoke")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual([call["selected_product"] for call in result.calls],
                         ["poison-product", "poison-product", None, None])
        self.assertEqual([call["require_bun"] for call in result.calls],
                         [None, None, None, "1"])

    def test_smoke_stops_at_each_failed_stage_and_preserves_exit_code(self):
        sequence = ["go", "go", "bun", "python3"]
        for name, ran in (("go", 2), ("bun", 3), ("python3", 4)):
            with self.subTest(command=name):
                result = self.run_wrapper("smoke", failure=name)
                self.assertEqual(result.returncode, 7, result.stderr)
                self.assertEqual([call["command"] for call in result.calls], sequence[:ran])
                self.assertIn("FAIL smoke (exit 7)", result.stderr)
                self.assertNotIn("PASS smoke", result.stdout)

    def test_smoke_without_gate_refuses_before_commands(self):
        result = self.run_wrapper("smoke", with_gate=False)
        self.assertEqual(result.returncode, 69, result.stderr)
        self.assertEqual(result.gate_class, "")
        self.assertEqual(result.calls, [])
        self.assertIn("refusing an unthrottled heavy workload", result.stderr)

    def test_broad_passes_command_arguments_without_shell_interpretation(self):
        args = ["test", "a file.go", "", "$(exit 99)", "--flag"]
        result = self.run_wrapper("broad", "--", "probe", *args)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.gate_class, "full")
        self.assertEqual(len(result.calls), 1)
        self.assertEqual(result.calls[0]["command"], "probe")
        self.assertEqual(result.calls[0]["args"], args)
        self.assertEqual(result.calls[0]["cwd"], result.repo_root)
        self.assertEqual(result.calls[0]["selected_product"], "poison-product")
        self.assertIn("PASS broad", result.stdout)
        self.assertIn("WAIT admission", result.stdout)

    def test_targeted_preserves_failure(self):
        result = self.run_wrapper("targeted", "--", "python3", "scripts/test-probe.py", failure="python3")
        self.assertEqual(result.returncode, 7, result.stderr)
        self.assertIn("FAIL targeted (exit 7)", result.stderr)
        self.assertNotIn("PASS targeted", result.stdout)

    def test_targeted_without_gate_passes_through(self):
        result = self.run_wrapper("targeted", "--", "go", "test", "-run=^TestOne$", "./internal/store", with_gate=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.calls[0]["args"], ["test", "-run=^TestOne$", "./internal/store"])
        self.assertEqual(result.gate_class, "")
        self.assertIn("oc-test-gate not found", result.stderr)

    def test_targeted_shapes_preserve_native_arguments(self):
        for command in (
            ["go", "test", "-timeout=2m", "-run", "^(TestOne|TestNew)$", "./internal/store", "-race"],
            ["env", "-u", "CONCORD_SELECTED_PRODUCT_ID", "bun", "test", "adapter/opencode/worker.test.ts"],
            ["env", "GOMAXPROCS=2", "go", "test", "./internal/agent", "-run=^TestOne$"],
            ["python3", "scripts/test-oc-test.py"],
            ["python3", "scripts/check-doc-links.py"],
        ):
            with self.subTest(command=command):
                result = self.run_wrapper("targeted", "--", *command)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.gate_class, "targeted")
                self.assertEqual(len(result.calls), 1)
                self.assertIn("named test execution not verified", result.stdout)

    def test_broad_and_unknown_shapes_cannot_use_targeted(self):
        for command in (
            ["go", "test", "./internal/store"],
            ["go", "test", "-run=^TestOne$", "./..."],
            ["go", "test", "-run", "", "./internal/store"],
            ["go", "test", "-run=.*", "./internal/store"],
            ["go", "test", "-run=TestOne", "./internal/store"],
            ["go", "test", "-run=^$", "./internal/store"],
            ["go", "test", "-run=^TestOne$", "-bench=.", "./internal/store"],
            ["go", "test", "-run=^TestOne$", "-args", "-run=.*"],
            ["go", "test", "-run=^TestOne$", "-run=^TestTwo$", "./internal/store"],
            ["bun", "test", "adapter/opencode/"],
            ["env", "-u", "PRODUCT", "bun", "test", "adapter/opencode/"],
            ["python3", "scripts/run-script-tests.py"],
            ["python3", "scripts/check-json.py"],
            ["bash", "-c", "go test ./..."], ["probe"], ["env", "--split-string=go test ./..."],
        ):
            with self.subTest(command=command):
                result = self.run_wrapper("targeted", "--", *command)
                self.assertEqual(result.returncode, 64, result.stderr)
                self.assertEqual(result.calls, [])
                self.assertEqual(result.gate_class, "")
                self.assertIn("bin/oc-test broad", result.stderr)

    def test_plan_never_executes_or_discovers(self):
        result = self.run_wrapper("plan", "targeted", "--", "go", "test", "-run=^TestNew$", "./internal/store")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.calls, [])
        self.assertEqual(result.gate_class, "")
        self.assertEqual(json.loads(result.stdout)["expected_tests"], ["TestNew"])
        self.assertEqual(json.loads(result.stdout)["admission"], "targeted")

    def test_supported_documentation_examples_remain_targeted(self):
        for path in ("README.md", ".concord/docs/installation.md",
                     "adapter/opencode/evals/native-issue-key-resume/README.md", "TESTING.md"):
            text = (REPOSITORY / path).read_text().replace("\\\n", "")
            for line in text.splitlines():
                if not line.startswith("bin/oc-test targeted -- "):
                    continue
                command = shlex.split(line)[3:]
                if "2>&1" in command:
                    command = command[:command.index("2>&1")]
                with self.subTest(path=path, command=command):
                    self.assertEqual(PLANNER.plan("targeted", command)["admission"], "targeted")

    def test_logging_keeps_complete_diagnostics_and_failure(self):
        result = self.run_wrapper("broad", "--", "bash", "-euo", "pipefail", "-c",
                                  '(printf "first\\nlast diagnostic\\n"; exit 7) 2>&1 | tee "$SCRATCH/diagnostics"')
        self.assertEqual(result.returncode, 7, result.stderr)
        self.assertIn("first\nlast diagnostic\n", result.stdout)
        self.assertIn("FAIL broad (exit 7)", result.stderr)

    def test_cancellation_is_forwarded_to_host_gate(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "bin").mkdir()
            shutil.copyfile(REPOSITORY / "bin/oc-test", root / "bin/oc-test")
            gate = root / "bin/oc-test-gate"
            gate.write_text('#!/bin/bash\ntrap \'printf "CANCELLED\\n"; exit 143\' TERM\nprintf "READY\\n"\nwhile :; do read -r -t 1 _ || true; done\n')
            gate.chmod(0o700)
            child = subprocess.Popen(["bash", str(root / "bin/oc-test"), "broad", "--", "probe"],
                env=dict(os.environ, PATH=f"{root / 'bin'}:/usr/bin:/bin"),
                stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            try:
                # These three flushed lines establish that admission reached the
                # host fake before cancellation; no sleep or timing assertion.
                lines = [child.stdout.readline() for _ in range(3)]
                self.assertEqual(lines[-1], "READY\n")
                child.terminate()
                stdout, stderr = child.communicate(timeout=5)
                self.assertEqual(child.returncode, 143, stderr)
                self.assertIn("CANCELLED", stdout)
            finally:
                if child.poll() is None:
                    child.terminate()
                    child.communicate(timeout=5)

    def test_broad_preserves_failure_and_requires_gate(self):
        failed = self.run_wrapper("broad", "--", "probe", failure="probe")
        self.assertEqual(failed.returncode, 7, failed.stderr)
        self.assertIn("FAIL broad (exit 7)", failed.stderr)
        missing = self.run_wrapper("broad", "--", "probe", with_gate=False)
        self.assertEqual(missing.returncode, 69, missing.stderr)
        self.assertEqual(missing.calls, [])

    def test_invalid_arguments_do_not_start_commands_or_request_admission(self):
        for args in ((), ("unknown",), ("full",), ("smoke", "extra"),
                     ("conformance", "extra"), ("targeted",), ("targeted", "--"),
                     ("preflight", "extra"), ("preflight", "--"), ("broad",), ("plan",)):
            with self.subTest(args=args):
                result = self.run_wrapper(*args)
                self.assertEqual(result.returncode, 64, result.stderr)
                self.assertEqual(result.calls, [])
                self.assertEqual(result.gate_class, "")

    def test_usage_describes_supported_tiers(self):
        result = self.run_wrapper()
        self.assertEqual(result.returncode, 64)
        for tier in ("smoke", "targeted", "broad", "plan", "conformance", "preflight"):
            self.assertIn(tier, result.stderr)

    def test_conformance_keeps_exact_command_environment_and_admission(self):
        result = self.run_wrapper("conformance")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.gate_class, "full")
        self.assertEqual(len(result.calls), 1)
        self.assertEqual(result.calls[0]["command"], "go")
        self.assertEqual(result.calls[0]["args"], [
            "test", "-count=1", "-run", "^TestTenProcessConformance$", "./internal/store", "-v",
        ])
        self.assertEqual(result.calls[0]["conformance_long"], "1")

    def test_preflight_uses_the_installed_lefthook_binary(self):
        result = self.run_wrapper("preflight")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.gate_class, "smoke")
        self.assertEqual(len(result.calls), 1)
        self.assertEqual(result.calls[0]["command"], "lefthook")
        self.assertEqual(result.calls[0]["args"], ["run", "pre-push"])
        self.assertEqual(result.calls[0]["cwd"], result.repo_root)
        self.assertEqual(result.calls[0]["selected_product"], "poison-product")
        self.assertIn("START preflight", result.stdout)
        self.assertIn("PASS preflight", result.stdout)
        self.assertIn("WAIT admission", result.stdout)

    def test_preflight_without_lefthook_runs_the_pinned_module(self):
        result = self.run_wrapper("preflight", commands=("go", "bun", "python3", "probe"))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.gate_class, "smoke")
        self.assertEqual(len(result.calls), 1)
        self.assertEqual(result.calls[0]["command"], "go")
        self.assertEqual(result.calls[0]["args"], [
            "run", "github.com/evilmartians/lefthook/v2@v2.1.14", "run", "pre-push",
        ])

    def test_preflight_failure_propagates(self):
        result = self.run_wrapper("preflight", failure="lefthook")
        self.assertEqual(result.returncode, 7, result.stderr)
        self.assertIn("FAIL preflight (exit 7)", result.stderr)
        self.assertNotIn("PASS preflight", result.stdout)


class FileSizeLimitTest(unittest.TestCase):
    run_wrapper = TierRoutingTest.run_wrapper

    def require_hard_limit(self, minimum):
        if _INHERITED_HARD != resource.RLIM_INFINITY and _INHERITED_HARD < minimum:
            self.skipTest(f"the inherited RLIMIT_FSIZE hard limit is below this fixture's {minimum} bytes")

    def test_runaway_write_is_stopped_at_the_limit_and_reported(self):
        self.require_hard_limit(1024 * 1024)
        result = self.run_wrapper("broad", "--", *RUNAWAY_WRITER, env_extra={"OC_TEST_MAX_FILE_MIB": "1"})
        self.assertEqual(result.runaway_size, 1024 * 1024)
        self.assertEqual(result.returncode, 153, result.stderr)
        self.assertIn("FAIL broad (exit 153)", result.stderr)
        self.assertIn("SIGXFSZ", result.stderr)
        self.assertIn("1024 KiB per-file limit", result.stderr)
        self.assertNotIn("PASS broad", result.stdout)

    def test_every_tier_runs_under_the_default_limit_with_and_without_the_gate(self):
        for args in (("smoke",), ("conformance",), ("preflight",), ("broad", "--", "probe"),
                     ("targeted", "--", "python3", "scripts/test-probe.py")):
            for with_gate in (True, False):
                with self.subTest(args=args, with_gate=with_gate):
                    result = self.run_wrapper(*args, with_gate=with_gate)
                    if not with_gate and args[0] != "targeted":
                        self.assertEqual(result.returncode, 69, result.stderr)
                        self.assertEqual(result.calls, [])
                        continue
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertTrue(result.calls)
                    self.assertEqual({tuple(call["fsize"]) for call in result.calls}, {tuple(DEFAULT_FILE_LIMIT)})

    def test_a_test_process_cannot_raise_the_limit(self):
        result = self.run_wrapper("broad", "--", "/bin/bash", "-c", "ulimit -f unlimited")
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn("FAIL broad", result.stderr)

    def test_one_run_sets_the_limit_to_its_stated_size(self):
        self.require_hard_limit(2 * 1024 * 1024)
        result = self.run_wrapper("broad", "--", "probe", env_extra={"OC_TEST_MAX_FILE_MIB": "2"})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.calls[0]["fsize"], [2 * 1024 * 1024] * 2)

    def test_a_stricter_inherited_limit_is_kept(self):
        self.require_hard_limit(1024 * 1024)
        stricter = 512 * 1024
        result = self.run_wrapper("broad", "--", "probe", preexec_fn=lambda: resource.setrlimit(resource.RLIMIT_FSIZE, (stricter, stricter)))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.calls[0]["fsize"], [stricter, stricter])

    def test_an_invalid_limit_refuses_before_any_command_or_admission(self):
        for value in ("0", "-1", "1.5", "abc", "unlimited", "10000000", "1 "):
            with self.subTest(value=value):
                result = self.run_wrapper("targeted", "--", "probe", env_extra={"OC_TEST_MAX_FILE_MIB": value})
                self.assertEqual(result.returncode, 64, result.stderr)
                self.assertIn("OC_TEST_MAX_FILE_MIB", result.stderr)
                self.assertEqual(result.calls, [])
                self.assertEqual(result.gate_class, "")


class GoResultsConsumerTest(unittest.TestCase):
    def read_fixture(self, *, expected=None, events=None, code=0, complete=True,
                     compile_only=False, interrupted=False, cached=False):
        package = "example.invalid/synthetic"
        if events is None:
            events = [
                {"Action": "run", "Package": package, "Test": "TestOne"},
                {"Action": "pass", "Package": package, "Test": "TestOne", "Elapsed": 2},
                {"Action": "output", "Package": package,
                 "Output": f"ok  {package} " + ("(cached)\n" if cached else "2.0s\n")},
                {"Action": "pass", "Package": package, "Elapsed": 2},
            ]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "events.jsonl").write_text("".join(json.dumps(event) + "\n" for event in events))
            (root / "stderr.log").write_text("complete diagnostics\n")
            (root / "results.json").write_text(json.dumps({"schema_version": 1, "process_exit_code": code,
                "complete": complete, "compile_only": compile_only}))
            (root / "metadata.json").write_text(json.dumps({"schema_version": 1, "process_exit_code": code,
                "source_applicability": "unproved", "interrupted_signal": 15 if interrupted else None}))
            return GO_READER.read(root, [f"{package}={name}" for name in (expected or [])])

    def test_named_selection_uses_shared_reader_and_retains_applicability(self):
        result, code = self.read_fixture(expected=["TestOne"])
        self.assertEqual(code, 0)
        self.assertEqual(result["outcome"], "pass")
        self.assertTrue(result["selection"]["named_claim"])
        self.assertEqual(result["provenance"]["source_applicability"], "unproved")
        self.assertEqual(result["raw_events"], "events.jsonl")

    def test_exit_zero_without_expected_name_is_not_named_pass(self):
        result, code = self.read_fixture(expected=["TestNew"])
        self.assertEqual(code, 1)
        self.assertEqual(result["outcome"], "selection_unproved")
        self.assertEqual(result["selection"]["missing"][0]["name"], "TestNew")

    def test_skipped_expected_and_truncated_events_are_not_passes(self):
        package = "example.invalid/synthetic"
        for terminal in ("skip", None):
            events = [{"Action": "run", "Package": package, "Test": "TestOne"}]
            if terminal:
                events += [{"Action": terminal, "Package": package, "Test": "TestOne"},
                           {"Action": "pass", "Package": package}]
            result, code = self.read_fixture(expected=["TestOne"], events=events)
            self.assertEqual(code, 1)
            self.assertNotEqual(result["outcome"], "pass")

    def test_compile_only_is_explicit_and_cannot_satisfy_named_expectations(self):
        events = [{"Action": "pass", "Package": "example.invalid/synthetic"}]
        result, code = self.read_fixture(events=events, compile_only=True)
        self.assertEqual((result["outcome"], code), ("compile_only", 0))
        result, code = self.read_fixture(events=events, compile_only=True, expected=["TestOne"])
        self.assertEqual((result["outcome"], code), ("selection_unproved", 1))

    def test_process_failure_and_interruption_never_become_success(self):
        result, code = self.read_fixture(code=7)
        self.assertEqual((result["outcome"], code), ("fail", 7))
        result, code = self.read_fixture(interrupted=True)
        self.assertFalse(result["complete"])
        self.assertNotEqual(code, 0)

    def test_cached_measurements_remain_reused(self):
        result, code = self.read_fixture(cached=True)
        self.assertEqual(code, 0)
        self.assertTrue(result["packages"][0]["cached"])
        self.assertEqual(result["slow_tests"][0]["measurement"], "reused")

    def test_missing_artifact_is_visible(self):
        with tempfile.TemporaryDirectory() as directory:
            self.assertEqual(GO_READER.main([directory]), 1)


if __name__ == "__main__":
    unittest.main()

#!/usr/bin/env python3
"""Exercise the verification wrapper in an isolated fake command environment."""

import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


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
import sys

name = Path(sys.argv[0]).name
with open(os.environ["COMMAND_RECORD"], "a") as record:
    record.write(json.dumps({{
        "command": name,
        "args": sys.argv[1:],
        "cwd": os.getcwd(),
        "selected_product": os.environ.get("CONCORD_SELECTED_PRODUCT_ID"),
        "require_bun": os.environ.get("CONCORD_REQUIRE_BUN"),
        "conformance_long": os.environ.get("CONCORD_CONFORMANCE_LONG"),
    }}) + "\\n")
if os.environ.get("FAIL_COMMAND") == name:
    sys.exit(7)
"""


class PhaseReportingTest(unittest.TestCase):
    def run_conformance(self, failure, with_gate=True):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "bin").mkdir()
            shutil.copyfile(Path(__file__).resolve().parents[1] / "bin/oc-test", root / "bin/oc-test")
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

    def test_missing_gate_warns_and_runs_unthrottled(self):
        result = self.run_conformance("", with_gate=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("oc-test-gate not found", result.stderr)
        self.assertIn("PASS conformance", result.stdout)


class TierRoutingTest(unittest.TestCase):
    def run_wrapper(self, *args, failure="", with_gate=True, commands=("go", "bun", "python3", "probe", "lefthook")):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "bin").mkdir()
            shutil.copyfile(Path(__file__).resolve().parents[1] / "bin/oc-test", root / "bin/oc-test")
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
                       CONCORD_SELECTED_PRODUCT_ID="poison-product")
            for key in ("CONCORD_REQUIRE_BUN", "CONCORD_CONFORMANCE_LONG"):
                env.pop(key, None)
            result = subprocess.run(["/bin/bash", str(root / "bin/oc-test"), *args],
                                    cwd=root / "bin", env=env, text=True,
                                    capture_output=True, check=False)
            result.gate_class = gate_record.read_text() if gate_record.exists() else ""
            result.calls = [json.loads(line) for line in command_record.read_text().splitlines()] if command_record.exists() else []
            result.repo_root = str(root)
            return result

    def test_smoke_runs_go_adapter_and_json_under_smoke_admission(self):
        result = self.run_wrapper("smoke")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.gate_class, "smoke")
        self.assertEqual([(call["command"], call["args"]) for call in result.calls], [
            ("go", ["test", "-timeout=15m", "./..."]),
            ("bun", ["test", "adapter/opencode/"]),
            ("python3", ["scripts/check-json.py", "--adapter-tests-external"]),
        ])
        self.assertTrue(all(call["cwd"] == result.repo_root for call in result.calls))
        self.assertIn("START smoke", result.stdout)
        self.assertIn("WAIT admission: smoke", result.stdout)
        self.assertIn("PASS smoke", result.stdout)

    def test_smoke_isolates_product_selection_for_adapter_and_json(self):
        result = self.run_wrapper("smoke")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual([call["selected_product"] for call in result.calls],
                         ["poison-product", None, None])
        self.assertEqual([call["require_bun"] for call in result.calls],
                         [None, None, "1"])

    def test_smoke_stops_at_each_failed_stage_and_preserves_exit_code(self):
        for index, name in enumerate(("go", "bun", "python3")):
            with self.subTest(command=name):
                result = self.run_wrapper("smoke", failure=name)
                self.assertEqual(result.returncode, 7, result.stderr)
                self.assertEqual([call["command"] for call in result.calls],
                                 ["go", "bun", "python3"][:index + 1])
                self.assertIn("FAIL smoke (exit 7)", result.stderr)
                self.assertNotIn("PASS smoke", result.stdout)

    def test_smoke_without_gate_runs_the_same_sequence(self):
        result = self.run_wrapper("smoke", with_gate=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.gate_class, "")
        self.assertEqual([call["command"] for call in result.calls], ["go", "bun", "python3"])
        self.assertIn("oc-test-gate not found", result.stderr)
        self.assertNotIn("WAIT admission", result.stdout)
        self.assertIn("PASS smoke", result.stdout)

    def test_targeted_passes_command_arguments_without_shell_interpretation(self):
        args = ["test", "a file.go", "", "$(exit 99)", "--flag"]
        result = self.run_wrapper("targeted", "--", "probe", *args)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.gate_class, "targeted")
        self.assertEqual(len(result.calls), 1)
        self.assertEqual(result.calls[0]["command"], "probe")
        self.assertEqual(result.calls[0]["args"], args)
        self.assertEqual(result.calls[0]["cwd"], result.repo_root)
        self.assertEqual(result.calls[0]["selected_product"], "poison-product")
        self.assertIn("PASS targeted", result.stdout)
        self.assertNotIn("WAIT admission", result.stdout)

    def test_targeted_preserves_failure(self):
        result = self.run_wrapper("targeted", "--", "probe", failure="probe")
        self.assertEqual(result.returncode, 7, result.stderr)
        self.assertIn("FAIL targeted (exit 7)", result.stderr)
        self.assertNotIn("PASS targeted", result.stdout)

    def test_targeted_without_gate_passes_through(self):
        result = self.run_wrapper("targeted", "--", "probe", "test", with_gate=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.calls[0]["args"], ["test"])
        self.assertEqual(result.gate_class, "")
        self.assertIn("oc-test-gate not found", result.stderr)

    def test_invalid_arguments_do_not_start_commands_or_request_admission(self):
        for args in ((), ("unknown",), ("full",), ("smoke", "extra"),
                     ("conformance", "extra"), ("targeted",), ("targeted", "--"),
                     ("preflight", "extra"), ("preflight", "--")):
            with self.subTest(args=args):
                result = self.run_wrapper(*args)
                self.assertEqual(result.returncode, 64, result.stderr)
                self.assertEqual(result.calls, [])
                self.assertEqual(result.gate_class, "")

    def test_usage_describes_supported_tiers(self):
        result = self.run_wrapper()
        self.assertEqual(result.returncode, 64)
        for tier in ("smoke", "targeted", "conformance", "preflight"):
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


if __name__ == "__main__":
    unittest.main()

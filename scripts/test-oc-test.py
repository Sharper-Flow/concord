#!/usr/bin/env python3
"""Exercise the verification wrapper in an isolated fake command environment."""

import os
from pathlib import Path
import shutil
import subprocess
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


if __name__ == "__main__":
    unittest.main()

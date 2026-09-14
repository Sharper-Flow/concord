#!/usr/bin/env python3
"""Exercise the verification wrapper in an isolated fake command environment."""

import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


class PhaseReportingTest(unittest.TestCase):
    def run_full(self, failure):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "bin").mkdir()
            shutil.copyfile(Path(__file__).resolve().parents[1] / "bin/oc-test", root / "bin/oc-test")
            for name in ("python3", "go", "git", "gofmt"):
                command = root / "bin" / name
                command.write_text('#!/bin/bash\nif [[ "${FAIL_STAGE:-}" == "$1" ]]; then exit 7; fi\nexit 0\n')
                command.chmod(0o700)
            gate = root / "bin/oc-test-gate"
            gate.write_text('#!/bin/bash\nwhile [[ "$1" != -- ]]; do shift; done\nshift\nexec "$@"\n')
            gate.chmod(0o700)
            env = dict(os.environ, PATH=str(root / "bin") + os.pathsep + os.environ["PATH"], FAIL_STAGE=failure)
            return subprocess.run(["bash", str(root / "bin/oc-test"), "full"], env=env, text=True, capture_output=True, check=False)

    def test_success_reports_each_phase(self):
        result = self.run_full("")
        self.assertEqual(result.returncode, 0, result.stderr)
        for phase in ("public-content", "doc-links", "json", "gofmt", "module-tidiness", "module-diff", "vet", "race-tests"):
            self.assertIn("START " + phase, result.stdout)
            self.assertIn("PASS " + phase, result.stdout)

    def test_failure_preserves_exit_code_and_stops(self):
        result = self.run_full("vet")
        self.assertEqual(result.returncode, 7)
        self.assertIn("FAIL vet", result.stderr)
        self.assertNotIn("START race-tests", result.stdout)

    def test_formatter_failure_is_not_suppressed(self):
        result = self.run_full("-l")
        self.assertEqual(result.returncode, 7)
        self.assertIn("FAIL gofmt", result.stderr)
        self.assertNotIn("START module-tidiness", result.stdout)


if __name__ == "__main__":
    unittest.main()

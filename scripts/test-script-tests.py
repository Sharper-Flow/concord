#!/usr/bin/env python3
"""Tests for the script-test coverage checker."""

from __future__ import annotations

import importlib.util
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("check-script-tests.py")
SPEC = importlib.util.spec_from_file_location("script_tests_checker", SCRIPT)
assert SPEC and SPEC.loader
checker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(checker)


class ScriptTestCoverageTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.root = Path(self.tempdir.name)
        (self.root / "scripts").mkdir()
        (self.root / ".github/workflows").mkdir(parents=True)

    def tearDown(self) -> None:
        self.tempdir.cleanup()

    def suite(self, name: str) -> None:
        (self.root / "scripts" / name).write_text("#!/usr/bin/env python3\n", encoding="utf-8")

    def workflow(self, name: str, body: str) -> None:
        (self.root / ".github/workflows" / name).write_text(body, encoding="utf-8")

    def test_every_suite_referenced_passes(self) -> None:
        self.suite("test-alpha.py")
        self.suite("test-beta.py")
        self.workflow("ci.yml", "        run: python3 scripts/test-alpha.py && python3 scripts/test-beta.py\n")

        self.assertEqual(checker.check(root=self.root), [])

    def test_unrun_suite_is_reported(self) -> None:
        self.suite("test-alpha.py")
        self.suite("test-orphan.py")
        self.workflow("ci.yml", "        run: python3 scripts/test-alpha.py\n")

        findings = checker.check(root=self.root)

        self.assertEqual(len(findings), 1)
        self.assertIn("unrun-suite", findings[0])
        self.assertIn("test-orphan.py", findings[0])

    def test_stale_reference_is_reported(self) -> None:
        self.suite("test-alpha.py")
        self.workflow("ci.yml", "        run: python3 scripts/test-alpha.py && python3 scripts/test-gone.py\n")

        findings = checker.check(root=self.root)

        self.assertEqual(len(findings), 1)
        self.assertIn("stale-reference", findings[0])
        self.assertIn("test-gone.py", findings[0])

    def test_reference_from_any_workflow_counts(self) -> None:
        self.suite("test-alpha.py")
        self.workflow("ci.yml", "        run: echo nothing\n")
        self.workflow("release.yml", "        run: python3 scripts/test-alpha.py\n")

        self.assertEqual(checker.check(root=self.root), [])

    def test_non_test_scripts_are_ignored(self) -> None:
        self.suite("test-alpha.py")
        (self.root / "scripts/check-alpha.py").write_text("#\n", encoding="utf-8")
        self.workflow("ci.yml", "        run: python3 scripts/test-alpha.py\n")

        self.assertEqual(checker.check(root=self.root), [])

    def test_repository_itself_passes(self) -> None:
        self.assertEqual(checker.check(root=Path(__file__).resolve().parents[1]), [])

    def test_wrong_run_indent_is_reported(self) -> None:
        self.suite("test-alpha.py")
        self.workflow("ci.yml", "         run: python3 scripts/test-alpha.py\n")

        findings = checker.check(root=self.root)

        self.assertEqual(len(findings), 1)
        self.assertIn("workflow-structure", findings[0])
        self.assertIn("indented 9 spaces", findings[0])

    def test_wrong_step_name_indent_is_reported(self) -> None:
        self.suite("test-alpha.py")
        self.workflow("ci.yml", "       - name: Check\n        run: python3 scripts/test-alpha.py\n")

        findings = checker.check(root=self.root)

        self.assertTrue(
            any(
                "workflow-structure" in finding and "`- name:`" in finding
                for finding in findings
            ),
            findings,
        )

    def test_unparseable_workflow_is_reported(self) -> None:
        if checker.yaml is None:
            self.skipTest("PyYAML unavailable")
        self.suite("test-alpha.py")
        self.workflow("ci.yml", "        run: [unbalanced\n")

        findings = checker.check(root=self.root)

        self.assertTrue(any("workflow-parse" in finding for finding in findings))


if __name__ == "__main__":
    unittest.main()

#!/usr/bin/env python3
"""Tests for the CD allocation preflight checker."""

from __future__ import annotations

import importlib.util
import json
import subprocess
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("check-cd-allocation.py")
SPEC = importlib.util.spec_from_file_location("cd_allocation_checker", SCRIPT)
assert SPEC and SPEC.loader
checker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(checker)


def git(root: Path, *arguments: str) -> None:
    subprocess.run(["git", *arguments], cwd=root, check=True, capture_output=True)


class CDAllocationTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.root = Path(self.tempdir.name)
        (self.root / "docs").mkdir()
        git(self.root, "init", "--quiet", "--initial-branch=main")
        git(self.root, "config", "user.name", "CD allocation tests")
        git(self.root, "config", "user.email", "cd-allocation-tests@example.invalid")
        self.write_manifest(["CD-0001"])
        self.commit("baseline")

    def tearDown(self) -> None:
        self.tempdir.cleanup()

    def write_manifest(self, identifiers: list[str]) -> None:
        (self.root / "docs/concord-knowledge-index.v1.json").write_text(
            json.dumps({"records": [{"id": identifier} for identifier in identifiers]})
            + "\n",
            encoding="utf-8",
        )

    def commit(self, message: str) -> None:
        git(self.root, "add", "docs/concord-knowledge-index.v1.json")
        git(self.root, "commit", "--quiet", "-m", message)

    def test_clean_new_record_passes(self) -> None:
        self.write_manifest(["CD-0001", "CD-0002"])
        self.assertEqual(
            checker.check(root=self.root, against="main", no_fetch=True), []
        )

    def test_removed_record_is_reported(self) -> None:
        self.write_manifest(["CD-0001", "CD-0002"])
        self.commit("add second baseline record")
        self.write_manifest(["CD-0001"])

        findings = checker.check(root=self.root, against="main", no_fetch=True)

        self.assertEqual(len(findings), 1)
        self.assertIn("removed-records", findings[0])
        self.assertIn("CD-0002", findings[0])
        self.assertIn("explicit superseding record", findings[0])

    def test_missing_ref_in_offline_mode_fails(self) -> None:
        findings = checker.check(root=self.root, against="missing", no_fetch=True)

        self.assertEqual(len(findings), 1)
        self.assertIn("comparison ref 'missing' is unavailable", findings[0])
        self.assertIn("rerun without --no-fetch", findings[0])

    def test_against_synthetic_ref(self) -> None:
        git(self.root, "branch", "synthetic")
        self.write_manifest(["CD-0001", "CD-0002"])

        self.assertEqual(
            checker.check(root=self.root, against="synthetic", no_fetch=True), []
        )

    def test_duplicate_new_id_is_reported(self) -> None:
        self.write_manifest(["CD-0001", "CD-0002", "CD-0002"])

        findings = checker.check(root=self.root, against="main", no_fetch=True)

        self.assertEqual(len(findings), 1)
        self.assertIn("duplicate-new", findings[0])
        self.assertIn("new CD id CD-0002 appears 2 times", findings[0])

    def test_non_cd_records_are_ignored(self) -> None:
        self.write_manifest(["CD-0001", "lesson-1", "lesson-1"])

        self.assertEqual(
            checker.check(root=self.root, against="main", no_fetch=True), []
        )

    def test_duplicate_json_keys_are_rejected(self) -> None:
        self.write_manifest(["CD-0001"])
        (self.root / "docs/concord-knowledge-index.v1.json").write_text(
            '{"records": [], "records": []}\n', encoding="utf-8"
        )

        findings = checker.check(root=self.root, against="main", no_fetch=True)

        self.assertEqual(len(findings), 1)
        self.assertIn("duplicate JSON key: records", findings[0])


if __name__ == "__main__":
    unittest.main()

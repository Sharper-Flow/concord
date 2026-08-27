#!/usr/bin/env python3
"""Negative tests for the approval consequence closure checker."""
from __future__ import annotations

import importlib.util
import json
import shutil
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
SCRIPT = Path(__file__).with_name("check-approval-consequence.py")
SPEC = importlib.util.spec_from_file_location("approval_consequence_checker", SCRIPT)
assert SPEC and SPEC.loader
checker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(checker)


class ApprovalConsequenceFixture(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory(prefix="approval-consequence-")
        self.root = Path(self.tempdir.name)
        for relative in (
            "contracts/agent-tool-surface.v1.json",
            "internal/store/schema.go",
        ):
            destination = self.root / relative
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(REPO_ROOT / relative, destination)

    def tearDown(self) -> None:
        self.tempdir.cleanup()

    def read_surface(self) -> dict:
        return json.loads((self.root / "contracts/agent-tool-surface.v1.json").read_text(encoding="utf-8"))

    def write_surface(self, surface: dict) -> None:
        (self.root / "contracts/agent-tool-surface.v1.json").write_text(
            json.dumps(surface, indent=2) + "\n", encoding="utf-8"
        )

    def findings(self) -> list[str]:
        return checker.check(self.root)

    def test_clean_fixture_passes(self) -> None:
        self.assertEqual(self.findings(), [])

    def test_dropped_research_from_check_is_reported(self) -> None:
        source = self.root / "internal/store/schema.go"
        text = source.read_text(encoding="utf-8").replace("'recovery','research','claim'", "'recovery','claim'", 1)
        source.write_text(text, encoding="utf-8")
        self.assertTrue(any(item.startswith("migration-consequence-closure:") for item in self.findings()))

    def test_bogus_surface_value_is_reported(self) -> None:
        surface = self.read_surface()
        surface["operations"].append({"id": "test.bogus", "consequence": "bogus"})
        self.write_surface(surface)
        self.assertTrue(any(item.startswith("migration-consequence-closure:") for item in self.findings()))

    def test_desynchronized_checks_are_reported(self) -> None:
        source = self.root / "internal/store/schema.go"
        text = source.read_text(encoding="utf-8").replace("'claim'", "'desynchronized'", 1)
        source.write_text(text, encoding="utf-8")
        findings = self.findings()
        self.assertTrue(any(item.startswith("migration-consequence-closure:") for item in findings))


if __name__ == "__main__":
    unittest.main()

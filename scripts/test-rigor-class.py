#!/usr/bin/env python3
"""Negative tests for the rigor-class closure checker."""
from __future__ import annotations

import importlib.util
import json
import shutil
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
SCRIPT = Path(__file__).with_name("check-rigor-class.py")
SPEC = importlib.util.spec_from_file_location("rigor_class_checker", SCRIPT)
assert SPEC and SPEC.loader
checker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(checker)


class RigorClassFixture(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory(prefix="rigor-class-")
        self.root = Path(self.tempdir.name)
        for relative in (
            "contracts/workflow-definition.schema.json",
            "contracts/agent-tool-surface-payloads.schema.json",
            "contracts/workflow-engine-scenarios.schema.json",
            "internal/store/schema.go",
        ):
            destination = self.root / relative
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(REPO_ROOT / relative, destination)

    def tearDown(self) -> None:
        self.tempdir.cleanup()

    def read_json(self, relative: str) -> dict:
        return json.loads((self.root / relative).read_text(encoding="utf-8"))

    def write_json(self, relative: str, value: object) -> None:
        (self.root / relative).write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8")

    def findings(self) -> list[str]:
        return checker.check(self.root)

    def test_clean_fixture_passes(self) -> None:
        self.assertEqual(self.findings(), [])

    def test_dropped_trigger_value_is_reported(self) -> None:
        source = self.root / "internal/store/schema.go"
        text = source.read_text(encoding="utf-8").replace("'critical_safety_critical'", "", 1)
        source.write_text(text, encoding="utf-8")
        self.assertTrue(any(item.startswith("migration-rigor-class-closure:") for item in self.findings()))

    def test_extra_payload_enum_member_is_reported(self) -> None:
        surface = self.read_json("contracts/agent-tool-surface-payloads.schema.json")
        surface["$defs"]["rigor_class"]["enum"].append("extra_rigor")
        self.write_json("contracts/agent-tool-surface-payloads.schema.json", surface)
        self.assertTrue(any(item.startswith("agent-rigor-class-closure:") for item in self.findings()))

    def test_workflow_contract_rigor_class_reference_is_required(self) -> None:
        surface = self.read_json("contracts/agent-tool-surface-payloads.schema.json")
        surface["$defs"]["workflow_contract"]["properties"]["rigor_class"] = {"type": "string"}
        self.write_json("contracts/agent-tool-surface-payloads.schema.json", surface)
        self.assertTrue(any(item.startswith("agent-rigor-class-reference:") for item in self.findings()))

    def test_changed_maturity_member_is_reported(self) -> None:
        definition = self.read_json("contracts/workflow-definition.schema.json")
        definition["$defs"]["rigorRule"]["properties"]["maturity"]["enum"][0] = "experimental"
        self.write_json("contracts/workflow-definition.schema.json", definition)
        self.assertTrue(any(item.startswith("agent-rigor-class-closure:") for item in self.findings()))


if __name__ == "__main__":
    unittest.main()

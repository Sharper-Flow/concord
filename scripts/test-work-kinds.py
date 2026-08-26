#!/usr/bin/env python3
"""Negative tests for the work-kind closure checker."""
from __future__ import annotations

import importlib.util
import json
import shutil
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
SCRIPT = Path(__file__).with_name("check-work-kinds.py")
SPEC = importlib.util.spec_from_file_location("work_kinds_checker", SCRIPT)
assert SPEC and SPEC.loader
checker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(checker)


class WorkKindsFixture(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory(prefix="work-kinds-")
        self.root = Path(self.tempdir.name)
        for relative in (
            "contracts/work-kinds.v1.json",
            "contracts/work-kinds.schema.json",
            "contracts/agent-tool-surface-payloads.schema.json",
            "contracts/workflow-engine-scenarios.schema.json",
            "contracts/work-kinds.digest",
            "internal/store/generated_work_kinds.go",
            "internal/store/schema.go",
            "internal/store/reconstruction.go",
            "internal/store/lifecycle.go",
            "internal/agent/mutations.go",
            "scripts/generate-work-kinds.py",
            "scripts/vocabulary_utils.py",
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

    def test_capture_enum_drift_is_reported(self) -> None:
        surface = self.read_json("contracts/agent-tool-surface-payloads.schema.json")
        surface["$defs"]["work_kind"]["enum"].remove("bug")
        self.write_json("contracts/agent-tool-surface-payloads.schema.json", surface)
        self.assertTrue(any(item.startswith("agent-capture-closure:") for item in self.findings()))

    def test_migration_row_drift_is_reported(self) -> None:
        source = self.root / "internal/store/schema.go"
        text = source.read_text(encoding="utf-8").replace("('bug',1,1,1,1);", "('bug',1,1,0,1);")
        source.write_text(text, encoding="utf-8")
        self.assertTrue(any(item.startswith("migration-work-kind-rows:") for item in self.findings()))

    def test_missing_trigger_predicate_is_reported(self) -> None:
        source = self.root / "internal/store/schema.go"
        text = source.read_text(encoding="utf-8").replace(
            "WHEN NOT EXISTS (SELECT 1 FROM work_kinds WHERE kind=NEW.kind AND stored=1)",
            "WHEN 0",
            1,
        )
        source.write_text(text, encoding="utf-8")
        self.assertTrue(any(item.startswith("migration-trigger-coverage:") for item in self.findings()))

    def test_missing_registry_immutability_trigger_is_reported(self) -> None:
        source = self.root / "internal/store/schema.go"
        text = source.read_text(encoding="utf-8").replace(
            "CREATE TRIGGER work_kinds_registry_no_delete",
            "CREATE TRIGGER work_kinds_registry_delete_missing",
        )
        source.write_text(text, encoding="utf-8")
        self.assertTrue(any(item.startswith("migration-registry-immutability:") for item in self.findings()))

    def test_scenario_enum_drift_is_reported(self) -> None:
        scenario = self.read_json("contracts/workflow-engine-scenarios.schema.json")
        scenario["$defs"]["operatorWorkKind"]["enum"].append("implementation")
        self.write_json("contracts/workflow-engine-scenarios.schema.json", scenario)
        self.assertTrue(any(item.startswith("scenario-work-kind-closure:") for item in self.findings()))

    def test_mixed_scenario_payload_definition_is_reported(self) -> None:
        scenario = self.read_json("contracts/workflow-engine-scenarios.schema.json")
        scenario["$defs"]["workCreatedPayload"]["allOf"][1]["oneOf"][1]["properties"]["work_kind"]["$ref"] = "#/$defs/workflowFamily"
        self.write_json("contracts/workflow-engine-scenarios.schema.json", scenario)
        self.assertTrue(any(item.startswith("scenario-payload-split:") for item in self.findings()))

    def test_scenario_payload_kind_bypass_is_reported(self) -> None:
        scenario = self.read_json("contracts/workflow-engine-scenarios.schema.json")
        scenario["$defs"]["workCreatedPayload"]["allOf"][1].pop("oneOf")
        self.write_json("contracts/workflow-engine-scenarios.schema.json", scenario)
        self.assertTrue(any(item.startswith("scenario-payload-split:") for item in self.findings()))

    def test_stale_runtime_literal_is_reported(self) -> None:
        source = self.root / "internal/store/reconstruction.go"
        text = source.read_text(encoding="utf-8").replace("VALUES (?, 'task', ?", "VALUES (?, 'reconstruction', ?")
        source.write_text(text, encoding="utf-8")
        self.assertTrue(any(item.startswith("runtime-work-kind-literal:") for item in self.findings()))

    def test_stale_runtime_guard_is_reported(self) -> None:
        source = self.root / "internal/agent/mutations.go"
        text = source.read_text(encoding="utf-8").replace(
            "WorkKindAgentCaptureAllowed(in.Kind)",
            "in.Kind == \"task\"",
        )
        source.write_text(text, encoding="utf-8")
        self.assertTrue(any(item.startswith("runtime-guard:") for item in self.findings()))

    def test_generator_drift_is_reported(self) -> None:
        path = self.root / "internal/store/generated_work_kinds.go"
        path.write_text(path.read_text(encoding="utf-8") + "\n", encoding="utf-8")
        self.assertTrue(any(item.startswith("generator-freshness:") for item in self.findings()))


if __name__ == "__main__":
    unittest.main()

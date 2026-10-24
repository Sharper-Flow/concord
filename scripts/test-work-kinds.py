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
            "contracts/work-kinds.digest",
            "internal/store/generated_work_kinds.go",
            "internal/store/schema.go",
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

    def test_migration_spacing_passes(self) -> None:
        source = self.root / "internal/store/schema.go"
        original = source.read_text(encoding="utf-8")
        header = (
            'Version:  49,\n\t\tName:     "work_kind_and_native_run_vocabularies",\n'
            '\t\tBreaking: true,\n\t\tSQL: `'
        )
        self.assertEqual(original.count(header), 1)
        before, body = original.split(header)
        sql, after = body.split("`", 1)
        after = after.replace(",\n\t},", " ,\t } ,", 1)
        for replacement in (
            'Version:49,Name:"work_kind_and_native_run_vocabularies",Breaking:true,SQL:`',
            'Version \t: 49 , Name \t: "work_kind_and_native_run_vocabularies" , Breaking : true , SQL : `',
            'Version:\t\t49,\n\t\tName:\t\t"work_kind_and_native_run_vocabularies",\n\t\tBreaking:\ttrue,\n\t\tSQL:\t\t`',
        ):
            with self.subTest(header=replacement):
                text = before + replacement + sql + "`" + after
                source.write_text(text, encoding="utf-8")
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

    def test_stored_kind_enum_drift_is_reported(self) -> None:
        surface = self.read_json("contracts/agent-tool-surface-payloads.schema.json")
        surface["$defs"]["stored_work_kind"]["enum"].remove("bug")
        self.write_json("contracts/agent-tool-surface-payloads.schema.json", surface)
        self.assertTrue(any(item.startswith("agent-stored-kind-closure:") for item in self.findings()))

    def test_work_kind_reference_drift_is_reported(self) -> None:
        relative = "contracts/agent-tool-surface-payloads.schema.json"
        original = self.read_json(relative)
        for definition in ("work_define_capture_input", "work_define_revise_input"):
            with self.subTest(definition=definition):
                surface = json.loads(json.dumps(original))
                surface["$defs"][definition]["properties"]["kind"]["$ref"] = "#/$defs/stored_work_kind"
                self.write_json(relative, surface)
                self.assertIn(
                    f"agent-work-kind-reference: {definition}.kind must reference #/$defs/work_kind",
                    self.findings(),
                )

    def test_work_kind_reference_bypass_is_reported(self) -> None:
        relative = "contracts/agent-tool-surface-payloads.schema.json"
        original = self.read_json(relative)
        for definition in ("work_define_capture_input", "work_define_revise_input"):
            for replacement in ({}, {"enum": original["$defs"]["work_kind"]["enum"]}, None):
                with self.subTest(definition=definition, replacement=replacement):
                    surface = json.loads(json.dumps(original))
                    properties = surface["$defs"][definition]["properties"]
                    if replacement is None:
                        properties.pop("kind")
                    else:
                        properties["kind"] = replacement
                    self.write_json(relative, surface)
                    self.assertIn(
                        f"agent-work-kind-reference: {definition}.kind must reference #/$defs/work_kind",
                        self.findings(),
                    )

    def test_stale_runtime_literal_is_reported(self) -> None:
        source = self.root / "internal/store/stale_literal.go"
        source.write_text(
            "package store\n\nconst staleInsert = `INSERT INTO work_items (id, kind, title) VALUES (?, 'undeclared', ?)`\n",
            encoding="utf-8",
        )
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

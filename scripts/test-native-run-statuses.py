#!/usr/bin/env python3
"""Negative tests for the native-run status closure checker."""
from __future__ import annotations

import importlib.util
import shutil
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
SCRIPT = Path(__file__).with_name("check-native-run-statuses.py")
SPEC = importlib.util.spec_from_file_location("native_run_statuses_checker", SCRIPT)
assert SPEC and SPEC.loader
checker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(checker)


class NativeRunStatusesFixture(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory(prefix="native-run-statuses-")
        self.root = Path(self.tempdir.name)
        for relative in (
            "contracts/native-run-statuses.v1.json",
            "contracts/native-run-statuses.schema.json",
            "contracts/native-run-statuses.digest",
            "internal/store/generated_native_run_statuses.go",
            "internal/store/schema.go",
            "internal/store/native_runs.go",
            "scripts/generate-native-run-statuses.py",
            "scripts/vocabulary_utils.py",
        ):
            destination = self.root / relative
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(REPO_ROOT / relative, destination)

    def tearDown(self) -> None:
        self.tempdir.cleanup()

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

    def test_migration_row_drift_is_reported(self) -> None:
        source = self.root / "internal/store/schema.go"
        text = source.read_text(encoding="utf-8").replace("('health','degraded',0);", "('health','degraded',1);")
        source.write_text(text, encoding="utf-8")
        self.assertTrue(any(item.startswith("migration-native-run-rows:") for item in self.findings()))

    def test_missing_trigger_predicate_is_reported(self) -> None:
        source = self.root / "internal/store/schema.go"
        text = source.read_text(encoding="utf-8").replace(
            "WHEN NOT EXISTS (SELECT 1 FROM workflow_native_run_statuses WHERE phase=NEW.phase AND status=NEW.status)",
            "WHEN 0",
            1,
        )
        source.write_text(text, encoding="utf-8")
        self.assertTrue(any(item.startswith("migration-trigger-coverage:") for item in self.findings()))

    def test_missing_registry_immutability_trigger_is_reported(self) -> None:
        source = self.root / "internal/store/schema.go"
        text = source.read_text(encoding="utf-8").replace(
            "CREATE TRIGGER workflow_native_run_statuses_registry_no_update",
            "CREATE TRIGGER workflow_native_run_statuses_registry_update_missing",
        )
        source.write_text(text, encoding="utf-8")
        self.assertTrue(any(item.startswith("migration-registry-immutability:") for item in self.findings()))

    def test_stale_runtime_guard_is_reported(self) -> None:
        source = self.root / "internal/store/native_runs.go"
        text = source.read_text(encoding="utf-8").replace(
            "NativeRunStatusAllowed(payload.Phase, payload.Status)",
            "payload.Status == \"started\"",
        )
        source.write_text(text, encoding="utf-8")
        self.assertTrue(any(item.startswith("runtime-guard:") for item in self.findings()))

    def test_generator_drift_is_reported(self) -> None:
        path = self.root / "internal/store/generated_native_run_statuses.go"
        path.write_text(path.read_text(encoding="utf-8") + "\n", encoding="utf-8")
        self.assertTrue(any(item.startswith("generator-freshness:") for item in self.findings()))


if __name__ == "__main__":
    unittest.main()

#!/usr/bin/env python3
"""Negative tests for the approval consequence closure checker."""
from __future__ import annotations

import importlib.util
import json
import shutil
import tempfile
import unittest
from pathlib import Path

from migration_source import migration_sql


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

    def test_migration_spacing_passes(self) -> None:
        source = self.root / "internal/store/schema.go"
        original = source.read_text(encoding="utf-8")
        for version, name, header in (
            (9, "agent_authority_and_approvals", 'Version: 9,\n\t\tName:    "agent_authority_and_approvals",\n\t\tSQL: `'),
            (55, "approval_consequence_surface_closure", 'Version:  55,\n\t\tName:     "approval_consequence_surface_closure",\n\t\tBreaking: true,\n\t\tSQL: `'),
        ):
            self.assertEqual(original.count(header), 1)
            before, body = original.split(header)
            sql, after = body.split("`", 1)
            after = after.replace(",\n\t},", " ,\t } ,", 1)
            breaking = "Breaking:true," if version == 55 else ""
            for replacement in (
                f'Version:{version},Name:"{name}",{breaking}SQL:`',
                f'Version \t: {version} , Name \t: "{name}" , {breaking}SQL : `',
                f'Version:\t\t{version},\n\t\tName:\t\t"{name}",\n\t\t{breaking}SQL:\t\t`',
            ):
                with self.subTest(version=version, header=replacement):
                    text = before + replacement + sql + "`" + after
                    source.write_text(text, encoding="utf-8")
                    self.assertEqual(self.findings(), [])

    def test_reformatted_migration_does_not_hide_other_checks(self) -> None:
        source = self.root / "internal/store/schema.go"
        text = source.read_text(encoding="utf-8").replace("`,\n\t},", "` ,\t } ,")
        text += "\nconst outsideApprovalSQL = `consequence TEXT NOT NULL CHECK(consequence IN ('unexpected'))`\n"
        source.write_text(text, encoding="utf-8")
        self.assertTrue(any(item.startswith("schema-consequence-closure:") for item in self.findings()))

    def test_sql_span_is_only_the_named_raw_literal(self) -> None:
        source = '''// π precedes the migration's character offsets.
const decoy = `Version:55,Name:"approval_consequence_surface_closure",SQL:unrelated`
var migrations = []migration{
    {Name:"approval_consequence_surface_closure", FoldMaintained:"", Version:55,
     SQL:`SELECT 'target';\r\n`, Breaking:true},
    {Version:56,Name:"other",SQL:`SELECT 'outside';`},
}
'''
        migration = migration_sql(source, 55, "approval_consequence_surface_closure")
        self.assertIsNotNone(migration)
        assert migration is not None
        literal = "SELECT 'target';\r\n"
        start = source.index(literal)
        self.assertEqual(migration.span, (start, start + len(literal)))
        self.assertEqual(source[slice(*migration.span)], literal)
        self.assertEqual(migration.sql, literal.replace("\r", ""))

    def test_sql_span_requires_the_named_migration_and_raw_sql(self) -> None:
        for fields in (
            'Version:55,Name:"other",SQL:`SELECT 1;`',
            'Version:54,Name:"approval_consequence_surface_closure",SQL:`SELECT 1;`',
            'Version:55,Name:"approval_consequence_surface_closure",SQL:"SELECT 1;"',
            'Version:55,Name:"approval_consequence_surface_closure",SQL:`SELECT 1;` + suffix',
        ):
            with self.subTest(fields=fields):
                source = "var migrations = []migration{{" + fields + "},}"
                self.assertIsNone(migration_sql(source, 55, "approval_consequence_surface_closure"))

    def test_unused_declared_consequence_passes(self) -> None:
        surface = self.read_surface()
        unused = surface["consequences"][0]
        operations = surface["operations"]
        surface["operations"] = [operation for operation in operations if operation["consequence"] != unused]
        self.assertLess(len(surface["operations"]), len(operations))
        self.assertTrue(surface["operations"])
        self.write_surface(surface)
        self.assertEqual(self.findings(), [])

    def test_undeclared_operation_consequence_is_reported(self) -> None:
        surface = self.read_surface()
        operation = surface["operations"][0]
        operation["consequence"] = "undeclared"
        self.write_surface(surface)
        self.assertIn(
            f"migration-consequence-closure: operation {operation['id']} uses undeclared consequence 'undeclared'",
            self.findings(),
        )

    def test_missing_declaration_is_reported(self) -> None:
        surface = self.read_surface()
        del surface["consequences"]
        self.write_surface(surface)
        self.assertTrue(any(item.startswith("surface-vocabulary:") for item in self.findings()))

    def test_duplicate_declaration_is_reported(self) -> None:
        surface = self.read_surface()
        surface["consequences"].append(surface["consequences"][0])
        self.write_surface(surface)
        self.assertIn("surface-vocabulary: consequences must be unique", self.findings())

    def test_malformed_declaration_is_reported(self) -> None:
        for declaration in (None, "read", {}, [], ["read", 1], ["read", None], [""]):
            with self.subTest(declaration=declaration):
                surface = self.read_surface()
                surface["consequences"] = declaration
                self.write_surface(surface)
                self.assertIn(
                    "surface-vocabulary: consequences must be a non-empty array of non-empty strings",
                    self.findings(),
                )

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

#!/usr/bin/env python3
"""Negative tests for the storage vocabulary authority checker."""
from __future__ import annotations

import importlib.util
import json
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = Path(__file__).with_name("check-vocabulary-authority.py")
SPEC = importlib.util.spec_from_file_location("vocabulary_authority_checker", SCRIPT)
assert SPEC and SPEC.loader
checker = importlib.util.module_from_spec(SPEC)
sys.modules["vocabulary_authority_checker"] = checker
SPEC.loader.exec_module(checker)


class VocabularyAuthorityFixture(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory(prefix="vocabulary-authority-")
        self.root = Path(self.tempdir.name)
        self.schema = self.root / "schema.sql"
        self.manifest = self.root / "manifest.json"
        self.write_schema("CREATE TABLE example (kind TEXT NOT NULL CHECK(kind = 'one'));\n")
        self.write_manifest(
            [{"table": "example", "column": "kind", "authority": "check"}]
        )

    def tearDown(self) -> None:
        self.tempdir.cleanup()

    def write_schema(self, value: str) -> None:
        self.schema.write_text(value, encoding="utf-8")

    def write_manifest(self, entries: list[dict[str, object]]) -> None:
        self.manifest.write_text(
            json.dumps(
                {
                    "$schema": "https://json-schema.org/draft/2020-12/schema",
                    "schema_version": "1.0",
                    "summary": "fixture",
                    "entries": entries,
                }
            ),
            encoding="utf-8",
        )

    def findings(self) -> list[str]:
        return checker.check(self.root, self.schema, self.manifest)

    def assert_finding(self, prefix: str) -> None:
        findings = self.findings()
        self.assertTrue(any(finding.startswith(prefix) for finding in findings), findings)

    def test_clean_fixture_passes(self) -> None:
        self.assertEqual(self.findings(), [])

    def test_undeclared_vocabulary_column_fails_inverse_coverage(self) -> None:
        self.write_schema(
            "CREATE TABLE example (kind TEXT NOT NULL CHECK(kind = 'one'), status TEXT NOT NULL);\n"
        )
        self.assert_finding("undeclared: example.status")

    def test_stale_manifest_entry_fails(self) -> None:
        self.write_manifest(
            [
                {"table": "example", "column": "kind", "authority": "check"},
                {"table": "example", "column": "status", "authority": "check"},
            ]
        )
        self.assert_finding("stale: example.status")

    def test_drop_column_removes_column_and_keeps_remaining(self) -> None:
        self.write_schema(
            "CREATE TABLE example (kind TEXT NOT NULL CHECK(kind = 'one'), mode TEXT NOT NULL);\n"
            "ALTER TABLE example DROP COLUMN mode;\n"
        )
        self.assertEqual(self.findings(), [])

    def test_drop_column_accepts_quoted_identifiers(self) -> None:
        for quote in ('"', "`"):
            self.write_schema(
                f"CREATE TABLE {quote}example{quote} (kind TEXT NOT NULL CHECK(kind = 'one'), {quote}mode{quote} TEXT NOT NULL);\n"
                f"ALTER TABLE {quote}example{quote} DROP COLUMN {quote}mode{quote};\n"
            )
            self.assertEqual(self.findings(), [])

    def test_stale_entry_for_dropped_column_fails(self) -> None:
        self.write_schema(
            "CREATE TABLE example (kind TEXT NOT NULL CHECK(kind = 'one'), mode TEXT NOT NULL);\n"
            "ALTER TABLE example DROP COLUMN mode;\n"
        )
        self.write_manifest(
            [
                {"table": "example", "column": "kind", "authority": "check"},
                {"table": "example", "column": "mode", "authority": "check"},
            ]
        )
        self.assert_finding("stale: example.mode")

    def test_drop_column_keeps_column_with_matching_prefix(self) -> None:
        self.write_schema(
            "CREATE TABLE example (kind TEXT NOT NULL CHECK(kind = 'one'), mode TEXT NOT NULL, mode_class TEXT NOT NULL);\n"
            "ALTER TABLE example DROP COLUMN mode;\n"
        )
        self.assert_finding("undeclared: example.mode_class")

    def test_drop_column_for_unknown_table_is_ignored(self) -> None:
        self.write_schema(
            "CREATE TABLE example (kind TEXT NOT NULL CHECK(kind = 'one'));\n"
            "CREATE TABLE other (id TEXT NOT NULL);\n"
            "ALTER TABLE other DROP COLUMN id;\n"
        )
        self.assertEqual(self.findings(), [])

    def test_drop_column_without_semicolon_is_not_modeled(self) -> None:
        self.write_schema(
            "CREATE TABLE example (kind TEXT NOT NULL CHECK(kind = 'one'), mode TEXT NOT NULL);\n"
            "ALTER TABLE example DROP COLUMN mode\n"
        )
        self.assert_finding("undeclared: example.mode")

    def test_false_check_claim_fails(self) -> None:
        self.write_schema("CREATE TABLE example (kind TEXT NOT NULL);\n")
        self.assert_finding("false-claim: example.kind claims check")

    def test_open_authority_requires_rationale(self) -> None:
        self.write_schema("CREATE TABLE example (mode TEXT NOT NULL);\n")
        self.write_manifest(
            [{"table": "example", "column": "mode", "authority": "open"}]
        )
        self.assert_finding("rationale: example.mode")


if __name__ == "__main__":
    unittest.main()

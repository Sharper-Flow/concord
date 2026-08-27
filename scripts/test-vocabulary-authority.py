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

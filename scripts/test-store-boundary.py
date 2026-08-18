#!/usr/bin/env python3
"""Focused fixture tests for scripts/check-store-boundary.py."""

from __future__ import annotations

import importlib.util
import sys
import tempfile
from pathlib import Path

spec = importlib.util.spec_from_file_location(
    "check_store_boundary", Path(__file__).with_name("check-store-boundary.py")
)
guard = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = guard
spec.loader.exec_module(guard)


def fixture(files: dict[str, str], schema: str = "CREATE TABLE widgets (id INTEGER);") -> list[str]:
    with tempfile.TemporaryDirectory() as directory:
        root = Path(directory)
        (root / "internal" / "store").mkdir(parents=True)
        (root / "internal" / "store" / "schema.go").write_text(
            f"package store\nvar schema = `{schema}`\n", encoding="utf-8"
        )
        for name, content in files.items():
            path = root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(content, encoding="utf-8")
        return guard.scan(root)


def test_raw_db_type_is_flagged() -> None:
    findings = fixture({"cmd/main.go": "package main\nvar db *sql.DB\n"})
    assert any("raw *sql.DB type" in item for item in findings)


def test_raw_tx_type_is_flagged() -> None:
    findings = fixture({"internal/agent/use.go": "package agent\nvar tx *sql.Tx\n"})
    assert any("raw *sql.Tx type" in item for item in findings)


def test_database_sql_import_alias_is_flagged() -> None:
    source = 'package main\nimport sqlpkg "database/sql"\nfunc open() { _, _ = sqlpkg.Open("sqlite", "") }\n'
    findings = fixture({"cmd/main.go": source})
    assert any("database/sql import" in item for item in findings)


def test_database_accessors_are_flagged() -> None:
    findings = fixture({"internal/agent/use.go": "package agent\nfunc f(s *Store) { s.DB(); s.DatabaseForTesting() }\n"})
    assert sum("raw database accessor identifier" in item for item in findings) == 2


def test_begin_tx_is_flagged() -> None:
    findings = fixture({"internal/agent/use.go": "package agent\nfunc f(db *sql.DB) { db.BeginTx(nil, nil) }\n"})
    assert any("direct BeginTx" in item for item in findings)


def test_literal_sql_operations_against_owned_table_are_flagged() -> None:
    source = """package agent
var a = `SELECT id FROM widgets`
var b = `INSERT INTO widgets(id) VALUES (1)`
var c = `UPDATE widgets SET id = 2`
var d = `DELETE FROM widgets WHERE id = 2`
var e = `REPLACE INTO widgets(id) VALUES (3)`
"""
    findings = fixture({"internal/agent/use.go": source})
    assert sum("literal SQL" in item for item in findings) == 5


def test_owned_tables_after_joins_and_in_subqueries_are_flagged() -> None:
    source = """package agent
var joined = `SELECT other.id FROM other JOIN widgets ON widgets.id = other.id`
var nested = `UPDATE other SET id = (SELECT max(id) FROM widgets)`
"""
    findings = fixture({"internal/agent/use.go": source})
    assert sum("literal SQL" in item for item in findings) == 2


def test_multiline_sql_and_sql_comments_are_handled() -> None:
    source = """package agent
var query = `SELECT id
-- SELECT id FROM widgets
FROM
  widgets`
var prose = `/* INSERT INTO widgets(id) VALUES (1) */`
"""
    findings = fixture({"cmd/main.go": source})
    sql_findings = [item for item in findings if "literal SQL" in item]
    assert len(sql_findings) == 1
    assert ":5:" in sql_findings[0]


def test_comments_do_not_trip_structural_rules() -> None:
    source = """package agent
// *sql.DB; *sql.Tx; s.DB(); s.DatabaseForTesting(); db.BeginTx(nil, nil)
/* SELECT id FROM widgets; INSERT INTO widgets(id) VALUES (1) */
var prose = "not code: *sql.DB *sql.Tx .DB() BeginTx("
"""
    assert fixture({"internal/agent/use.go": source}) == []


def test_store_ownership_is_allowed_except_store_db_method() -> None:
    source = """package store
import "database/sql"
func owned(db *sql.DB) { db.BeginTx(nil, nil); db.Query(`SELECT id FROM widgets`) }
"""
    assert fixture({"internal/store/owned.go": source}) == []


def test_store_db_method_is_forbidden() -> None:
    source = """package store
func (s *Store) DB() *sql.DB { return nil }
"""
    findings = fixture({"internal/store/owned.go": source})
    assert any("Store.DB method" in item for item in findings)


def test_test_files_are_allowed() -> None:
    source = """package agent
func testOnly(db *sql.DB) { db.BeginTx(nil, nil); db.Query(`SELECT id FROM widgets`) }
"""
    assert fixture({"internal/agent/use_test.go": source}) == []


def test_pm1fixture_is_allowed() -> None:
    source = """package pm1fixture
func fixture(db *sql.DB) { db.BeginTx(nil, nil); db.Query(`SELECT id FROM widgets`) }
"""
    assert fixture({"internal/pm1fixture/fixture.go": source}) == []


def test_production_database_for_testing_identifiers_are_forbidden() -> None:
    findings = fixture({"cmd/main.go": "package main\nfunc f(s *Store) { s.DatabaseForTesting() }\n"})
    assert any("raw database accessor identifier" in item for item in findings)


def test_production_database_for_testing_method_values_are_forbidden() -> None:
    source = "package main\nfunc f(s *Store) { get := s.DatabaseForTesting; db := get() ; _ = db }\n"
    findings = fixture({"cmd/main.go": source})
    assert any("raw database accessor identifier" in item for item in findings)


def test_full_repo_passes_after_production_refactor() -> None:
    assert guard.main() == 0


if __name__ == "__main__":
    failures = 0
    for name, function in sorted(globals().items()):
        if name.startswith("test_") and callable(function):
            try:
                function()
                print(f"ok  {name}")
            except AssertionError as error:
                failures += 1
                print(f"FAIL {name}: {error}")
    raise SystemExit(1 if failures else 0)

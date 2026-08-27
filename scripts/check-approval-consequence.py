#!/usr/bin/env python3
"""Check approval consequence closure against the agent tool surface."""
from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SURFACE = Path("contracts/agent-tool-surface.v1.json")
SCHEMA_SOURCE = Path("internal/store/schema.go")
EXPECTED_TABLES = ("agent_approvals", "agent_approval_challenges")


def load_json(root: Path, relative: Path, findings: list[str]) -> object:
    try:
        return json.loads((root / relative).read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
        findings.append(f"json-load: {relative}: invalid JSON: {exc}")
        return None


def migration_match(source: str, version: int, name: str) -> re.Match[str] | None:
    return re.search(
        r'Version: '
        + str(version)
        + r',\s+Name:\s+"'
        + re.escape(name)
        + r'",\s+SQL: `(?P<sql>.*?)`,\s+\},',
        source,
        re.S,
    )


def check_values(table_sql: str, table: str) -> list[str] | None:
    table_match = re.search(
        rf"CREATE TABLE {re.escape(table)}\s*\((?P<body>.*?)\);",
        table_sql,
        re.S,
    )
    if table_match is None:
        return None
    consequence = re.search(
        r"consequence\s+TEXT\s+NOT\s+NULL\s+CHECK\s*\(\s*consequence\s+IN\s*\((?P<values>.*?)\)\s*\)",
        table_match.group("body"),
        re.S,
    )
    if consequence is None:
        return None
    return re.findall(r"'([^']+)'", consequence.group("values"))


def check(root: Path) -> list[str]:
    findings: list[str] = []
    surface = load_json(root, SURFACE, findings)
    if not isinstance(surface, dict):
        return findings
    operations = surface.get("operations")
    if not isinstance(operations, list):
        findings.append("surface-vocabulary: operations must be an array")
        return findings
    if not all(isinstance(operation, dict) for operation in operations):
        findings.append("surface-vocabulary: operations must contain objects")
        return findings
    consequences = [
        operation.get("consequence")
        for operation in operations
    ]
    if not consequences or not all(isinstance(value, str) for value in consequences):
        findings.append("surface-vocabulary: operations must declare non-empty string consequences")
        return findings
    expected = sorted(set(consequences))
    if not expected or len(expected) != len(set(expected)):
        findings.append(f"surface-vocabulary: consequence set is not non-empty and unique: {expected}")

    try:
        source = (root / SCHEMA_SOURCE).read_text(encoding="utf-8")
    except OSError as exc:
        findings.append(f"schema-source: unable to read {SCHEMA_SOURCE}: {exc}")
        return findings

    migration = migration_match(source, 55, "approval_consequence_surface_closure")
    if migration is None:
        findings.append("migration-coverage: missing migration 55 approval_consequence_surface_closure")
        return findings
    migration_sql = migration.group("sql")
    for table in EXPECTED_TABLES:
        actual = check_values(migration_sql, table)
        if actual is None:
            findings.append(f"migration-consequence-closure: missing CHECK for {table}")
        elif sorted(actual) != expected or len(actual) != len(set(actual)):
            findings.append(
                f"migration-consequence-closure: {table} expected {expected}, got {sorted(actual)}"
            )

    legacy = migration_match(source, 9, "agent_authority_and_approvals")
    allowed_ranges = [migration.span("sql")]
    if legacy is not None:
        allowed_ranges.append(legacy.span("sql"))
    all_checks = re.finditer(
        r"consequence\s+TEXT\s+NOT\s+NULL\s+CHECK\s*\(\s*consequence\s+IN\s*\((?P<values>.*?)\)\s*\)",
        source,
        re.S,
    )
    for match in all_checks:
        if any(start <= match.start() < end for start, end in allowed_ranges):
            continue
        actual = re.findall(r"'([^']+)'", match.group("values"))
        if sorted(actual) != expected or len(actual) != len(set(actual)):
            findings.append(
                f"schema-consequence-closure: unexpected CHECK list {sorted(actual)}, expected {expected}"
            )
    return findings


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--root", type=Path, default=ROOT)
    args = parser.parse_args(argv)
    findings = check(args.root.resolve())
    for finding in findings:
        print(finding)
    if findings:
        print(f"approval-consequence check failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    print("approval-consequence check passed", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))

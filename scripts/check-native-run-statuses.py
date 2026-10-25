#!/usr/bin/env python3
"""Check the native-run status closure, literal migration coverage, and runtime guards."""
from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

from migration_source import migration_sql
from vocabulary_utils import generator_freshness, load_json_finding


ROOT = Path(__file__).resolve().parents[1]
VOCABULARY = Path("contracts/native-run-statuses.v1.json")
SCHEMA_SOURCE = Path("internal/store/schema.go")

def trigger_block(sql: str, name: str) -> str | None:
    match = re.search(rf"CREATE TRIGGER {re.escape(name)}\b(?P<body>.*?)\bEND;", sql, re.S)
    return match.group("body") if match else None


def check(root: Path) -> list[str]:
    findings: list[str] = []
    generator_freshness(root, "generate-native-run-statuses.py", findings)
    vocabulary = load_json_finding(root, VOCABULARY, findings)
    if not isinstance(vocabulary, dict):
        return findings
    phases = vocabulary.get("phases")
    if not isinstance(phases, list):
        findings.append("vocabulary-shape: phases must be an array")
        return findings
    try:
        source = (root / SCHEMA_SOURCE).read_text(encoding="utf-8")
    except OSError as exc:
        findings.append(f"migration-source: unable to read {SCHEMA_SOURCE}: {exc}")
        return findings
    migration = migration_sql(source, 49, "work_kind_and_native_run_vocabularies")
    if migration is None:
        findings.append("migration-coverage: missing migration 49 work_kind_and_native_run_vocabularies")
        return findings
    sql = migration.sql
    rows: set[tuple[str, str, int]] = set()
    for match in re.finditer(r"INSERT INTO workflow_native_run_statuses\s*\([^)]*\)\s*VALUES\s*\('([^']+)',\s*'([^']+)',\s*(0|1)\);", sql):
        rows.add((match.group(1), match.group(2), int(match.group(3))))
    expected_rows = {(phase["phase"], status["status"], int(status["failure"])) for phase in phases for status in phase["statuses"]}
    if rows != expected_rows:
        findings.append(f"migration-native-run-rows: expected {sorted(expected_rows)}, got {sorted(rows)}")
    trigger_predicate = re.compile(
        r"WHEN\s+NOT\s+EXISTS\s*\(\s*SELECT\s+1\s+FROM\s+workflow_native_run_statuses\s+"
        r"WHERE\s+phase\s*=\s*NEW\.phase\s+AND\s+status\s*=\s*NEW\.status\s*\)",
        re.I | re.S,
    )
    for trigger in ("workflow_native_runs_status_registry_insert", "workflow_native_runs_status_registry_update"):
        block = trigger_block(sql, trigger)
        if block is None or not trigger_predicate.search(block):
            findings.append(f"migration-trigger-coverage: missing native registry guard {trigger}")
    for trigger in (
        "workflow_native_run_statuses_registry_no_insert",
        "workflow_native_run_statuses_registry_no_update",
        "workflow_native_run_statuses_registry_no_delete",
    ):
        block = trigger_block(sql, trigger)
        if block is None or "workflow_native_run_statuses registry is immutable" not in block:
            findings.append(f"migration-registry-immutability: missing native-run registry guard {trigger}")
    runtime_markers = {
        Path("internal/store/native_runs.go"): "NativeRunStatusAllowed(payload.Phase, payload.Status)",
    }
    for relative, marker in runtime_markers.items():
        try:
            source = (root / relative).read_text(encoding="utf-8")
        except OSError as exc:
            findings.append(f"runtime-guard: unable to read {relative}: {exc}")
            continue
        if marker not in source:
            findings.append(f"runtime-guard: {relative} does not use generated vocabulary marker {marker}")
    return findings


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--root", type=Path, default=ROOT)
    args = parser.parse_args(argv)
    findings = check(args.root.resolve())
    for finding in findings:
        print(finding)
    if findings:
        print(f"native-run status vocabulary check failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    print("native-run status vocabulary check passed", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))

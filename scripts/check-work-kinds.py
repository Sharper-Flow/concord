#!/usr/bin/env python3
"""Check work-kind projections, agent closure, and literal migration coverage."""
from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

from migration_source import migration_sql
from vocabulary_utils import generator_freshness, load_json_finding


ROOT = Path(__file__).resolve().parents[1]
VOCABULARY = Path("contracts/work-kinds.v1.json")
SURFACE_SCHEMA = Path("contracts/agent-tool-surface-payloads.schema.json")
SCHEMA_SOURCE = Path("internal/store/schema.go")
RUNTIME_SOURCE = Path("internal/store")



def enum_at(surface: object, path: list[str], findings: list[str]) -> list[str]:
    value = surface
    for part in path:
        if not isinstance(value, dict):
            findings.append(f"schema-enum: {'/'.join(path)}: parent is not an object")
            return []
        value = value.get(part)
    if not isinstance(value, list) or not all(isinstance(item, str) for item in value):
        findings.append(f"schema-enum: {'/'.join(path)}: missing string array")
        return []
    return value


def migration_rows(sql: str, table: str) -> dict[tuple[str, ...], tuple[int, ...]]:
    rows: dict[tuple[str, ...], tuple[int, ...]] = {}
    pattern = re.compile(
        rf"INSERT INTO {table}\s*\([^)]*\)\s*VALUES\s*\((?P<values>[^)]*)\);"
    )
    for match in pattern.finditer(sql):
        values = [part.strip().strip("'") for part in match.group("values").split(",")]
        if table == "work_kinds" and len(values) == 5:
            rows[(values[0],)] = tuple(int(value) for value in values[1:])
        if table == "workflow_native_run_statuses" and len(values) == 3:
            rows[(values[0], values[1])] = (int(values[2]),)
    return rows


def trigger_block(sql: str, name: str) -> str | None:
    match = re.search(rf"CREATE TRIGGER {re.escape(name)}\b(?P<body>.*?)\bEND;", sql, re.S)
    return match.group("body") if match else None


def check_runtime_work_kind_literals(root: Path, stored: set[str], findings: list[str]) -> None:
    pattern = re.compile(
        r"INSERT(?:\s+OR\s+IGNORE)?\s+INTO\s+work_items\s*"
        r"\((?P<columns>[^)]*)\)\s*VALUES\s*\((?P<values>[^)]*)\)",
        re.I | re.S,
    )
    for path in sorted((root / RUNTIME_SOURCE).glob("*.go")):
        if path.name.endswith("_test.go"):
            continue
        source = path.read_text(encoding="utf-8")
        for match in pattern.finditer(source):
            columns = [value.strip().lower() for value in match.group("columns").split(",")]
            values = [value.strip() for value in match.group("values").split(",")]
            if "kind" not in columns or len(columns) != len(values):
                continue
            value = values[columns.index("kind")]
            literal = re.fullmatch(r"'([^']+)'", value)
            if literal and literal.group(1) not in stored:
                findings.append(
                    f"runtime-work-kind-literal: {path.relative_to(root)} uses undeclared stored kind {literal.group(1)!r}"
                )


def check(root: Path) -> list[str]:
    findings: list[str] = []
    generator_freshness(root, "generate-work-kinds.py", findings)
    vocabulary = load_json_finding(root, VOCABULARY, findings)
    surface = load_json_finding(root, SURFACE_SCHEMA, findings)
    if not isinstance(vocabulary, dict) or not isinstance(surface, dict):
        return findings
    kinds = vocabulary.get("kinds")
    if not isinstance(kinds, list):
        findings.append("vocabulary-shape: kinds must be an array")
        return findings
    expected = {
        item["kind"]: (int(item["stored"]), int(item["fold_create"] == "allowed"), int(item["fold_revise"] == "allowed"), int(item["agent_capture"] == "allowed"))
        for item in kinds if isinstance(item, dict) and isinstance(item.get("kind"), str)
    }
    actual = enum_at(surface, ["$defs", "work_kind", "enum"], findings)
    capture = sorted(item["kind"] for item in kinds if item.get("agent_capture") == "allowed")
    if sorted(actual) != capture:
        findings.append(f"agent-capture-closure: expected {capture}, got {sorted(actual)}")
    stored = sorted(item["kind"] for item in kinds if item.get("stored") is True)
    actual_stored = enum_at(surface, ["$defs", "stored_work_kind", "enum"], findings)
    if sorted(actual_stored) != stored:
        findings.append(f"agent-stored-kind-closure: expected {stored}, got {sorted(actual_stored)}")
    definitions = surface.get("$defs")
    for definition in ("work_define_capture_input", "work_define_revise_input"):
        node = definitions.get(definition) if isinstance(definitions, dict) else None
        properties = node.get("properties", {}) if isinstance(node, dict) else {}
        kind = properties.get("kind") if isinstance(properties, dict) else None
        if not isinstance(kind, dict) or kind.get("$ref") != "#/$defs/work_kind":
            findings.append(f"agent-work-kind-reference: {definition}.kind must reference #/$defs/work_kind")
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
    rows = migration_rows(sql, "work_kinds")
    expected_rows = { (name,): values for name, values in expected.items() }
    if rows != expected_rows:
        findings.append(f"migration-work-kind-rows: expected {expected_rows}, got {rows}")
    trigger_predicate = re.compile(
        r"WHEN\s+NOT\s+EXISTS\s*\(\s*SELECT\s+1\s+FROM\s+work_kinds\s+"
        r"WHERE\s+kind\s*=\s*NEW\.kind\s+AND\s+stored\s*=\s*1\s*\)",
        re.I | re.S,
    )
    for trigger in ("work_items_kind_registry_insert", "work_items_kind_registry_update"):
        block = trigger_block(sql, trigger)
        if block is None or not trigger_predicate.search(block):
            findings.append(f"migration-trigger-coverage: missing stored registry guard {trigger}")
    for trigger in (
        "work_kinds_registry_no_insert",
        "work_kinds_registry_no_update",
        "work_kinds_registry_no_delete",
    ):
        block = trigger_block(sql, trigger)
        if block is None or "work_kinds registry is immutable" not in block:
            findings.append(f"migration-registry-immutability: missing work-kind registry guard {trigger}")
    runtime_markers = {
        Path("internal/agent/mutations.go"): (
            "WorkKindAgentCaptureAllowed(in.Kind)",
            "WorkKindFoldReviseAllowed(in.Kind)",
        ),
        Path("internal/store/lifecycle.go"): (
            "WorkKindFoldCreateAllowed(payload.WorkKind)",
            "WorkKindFoldReviseAllowed(payload.Kind)",
        ),
    }
    for relative, markers in runtime_markers.items():
        try:
            runtime_source = (root / relative).read_text(encoding="utf-8")
        except OSError as exc:
            findings.append(f"runtime-guard: unable to read {relative}: {exc}")
            continue
        findings.extend(
            f"runtime-guard: {relative} does not use generated vocabulary marker {marker}"
            for marker in markers
            if marker not in runtime_source
        )
    check_runtime_work_kind_literals(root, set(stored), findings)
    return findings


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--root", type=Path, default=ROOT)
    args = parser.parse_args(argv)
    findings = check(args.root.resolve())
    for finding in findings:
        print(finding)
    if findings:
        print(f"work-kind vocabulary check failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    print("work-kind vocabulary check passed", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))

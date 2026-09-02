#!/usr/bin/env python3
"""Check archived-work trigger vocabulary against the knowledge-index schema."""
from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCHEMA = Path("contracts/concord-knowledge-index.v1.schema.json")
SCHEMA_SOURCE = Path("internal/store/schema.go")
MIGRATION_RE = re.compile(
    r'Version:\s+56,\s+Name:\s+"archived_work_kind_vocabulary",\s+(?:Breaking:\s+true,\s+)?SQL:\s+`(?P<sql>.*?)`,\s+\},',
    re.S,
)


def load_json(root: Path, relative: Path, findings: list[str]) -> object | None:
    try:
        return json.loads((root / relative).read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
        findings.append(f"schema-load: {relative}: invalid JSON: {exc}")
        return None


def supported_kinds(schema: object, findings: list[str]) -> set[str] | None:
    try:
        enum = schema["properties"]["supported_kinds"]["items"]["enum"]  # type: ignore[index]
    except (KeyError, TypeError):
        findings.append("schema-enum: properties.supported_kinds.items.enum is missing")
        return None
    if not isinstance(enum, list) or not enum or not all(isinstance(item, str) for item in enum):
        findings.append("schema-enum: properties.supported_kinds.items.enum is not a non-empty string array")
        return None
    if len(set(enum)) != len(enum):
        findings.append("schema-enum: properties.supported_kinds.items.enum contains duplicates")
        return None
    return set(enum)


def trigger_block(sql: str, name: str) -> str | None:
    match = re.search(rf"CREATE TRIGGER {re.escape(name)}\b(?P<body>.*?)\bEND;", sql, re.S)
    return match.group("body") if match else None


def trigger_kinds(block: str, name: str, findings: list[str]) -> set[str] | None:
    match = re.search(r"WHEN\s+NEW\.type\s+NOT\s+IN\s*\((?P<values>.*?)\)", block, re.S | re.I)
    if match is None:
        findings.append(f"migration-trigger: {name} has no literal type IN-list")
        return None
    values = [value.strip() for value in match.group("values").split(",")]
    literals = []
    for value in values:
        literal = re.fullmatch(r"'([^']+)'", value)
        if literal is None:
            findings.append(f"migration-trigger: {name} contains a non-literal IN-list value {value!r}")
            continue
        literals.append(literal.group(1))
    if len(set(literals)) != len(literals):
        findings.append(f"migration-trigger: {name} contains duplicate kinds")
    return set(literals)


def check(root: Path) -> list[str]:
    findings: list[str] = []
    schema = load_json(root, SCHEMA, findings)
    if schema is None:
        return findings
    expected = supported_kinds(schema, findings)
    if expected is None:
        return findings
    try:
        source = (root / SCHEMA_SOURCE).read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError) as exc:
        findings.append(f"migration-source: {SCHEMA_SOURCE}: cannot read: {exc}")
        return findings
    migration = MIGRATION_RE.search(source)
    if migration is None:
        findings.append("migration-source: missing migration 56 archived_work_kind_vocabulary")
        return findings
    sql = migration.group("sql")
    for name in ("archived_work_kind_insert", "archived_work_kind_update"):
        block = trigger_block(sql, name)
        if block is None:
            findings.append(f"migration-trigger: missing {name}")
            continue
        actual = trigger_kinds(block, name, findings)
        if actual is not None and actual != expected:
            findings.append(f"migration-trigger: {name} expected {sorted(expected)}, got {sorted(actual)}")
    return findings


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--root", type=Path, default=ROOT)
    args = parser.parse_args(argv)
    findings = check(args.root.resolve())
    for finding in findings:
        print(finding)
    if findings:
        print(f"archived-work kind vocabulary check failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    print("archived-work kind vocabulary check passed", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))

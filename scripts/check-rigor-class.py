#!/usr/bin/env python3
"""Check the closed rigor-class vocabulary across law, schemas, and SQL."""
from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
DEFINITION_SCHEMA = Path("contracts/workflow-definition.schema.json")
SURFACE_SCHEMA = Path("contracts/agent-tool-surface-payloads.schema.json")
SCENARIO_SCHEMA = Path("contracts/workflow-engine-scenarios.schema.json")
SCHEMA_SOURCE = Path("internal/store/schema.go")
TRIGGERS = (
    "workflow_contracts_rigor_class_insert",
    "workflow_contracts_rigor_class_update",
)


def load_json(root: Path, relative: Path, findings: list[str]) -> object:
    try:
        return json.loads((root / relative).read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
        findings.append(f"json-load: {relative}: invalid JSON: {exc}")
        return None


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


def migration_block(source: str) -> str | None:
    match = re.search(
        r'Version:\s+54,\s+Name:\s+"rigor_class_vocabulary",\s+(?:Breaking:\s+true,\s+)?SQL:\s+`(?P<sql>.*?)`,\s+\},',
        source,
        re.S,
    )
    return match.group("sql") if match else None


def trigger_block(sql: str, name: str) -> str | None:
    match = re.search(rf"CREATE TRIGGER {re.escape(name)}\b(?P<body>.*?)\bEND;", sql, re.S)
    return match.group("body") if match else None


def trigger_values(sql: str, name: str, findings: list[str]) -> list[str]:
    block = trigger_block(sql, name)
    if block is None:
        findings.append(f"migration-trigger-coverage: missing trigger {name}")
        return []
    match = re.search(
        r"WHEN\s+NEW\.rigor_class\s+NOT\s+IN\s*\((?P<values>.*?)\)",
        block,
        re.S,
    )
    if match is None:
        findings.append(f"migration-trigger-coverage: {name} has no rigor_class IN-list")
        return []
    return re.findall(r"'([^']+)'", match.group("values"))


def check(root: Path) -> list[str]:
    findings: list[str] = []
    definition = load_json(root, DEFINITION_SCHEMA, findings)
    surface = load_json(root, SURFACE_SCHEMA, findings)
    scenario = load_json(root, SCENARIO_SCHEMA, findings)
    if not all(isinstance(value, dict) for value in (definition, surface, scenario)):
        return findings

    maturity = enum_at(
        definition,
        ["$defs", "rigorRule", "properties", "maturity", "enum"],
        findings,
    )
    audience = enum_at(
        definition,
        ["$defs", "rigorRule", "properties", "audience_band", "enum"],
        findings,
    )
    if len(set(maturity)) != len(maturity) or len(set(audience)) != len(audience):
        findings.append("definition-vocabulary: maturity and audience_band enums must be unique")
    expected = sorted(f"{maturity_name}_{audience_name}" for maturity_name in maturity for audience_name in audience)
    if len(expected) != 12:
        findings.append(f"definition-vocabulary: expected 12 compositions, got {len(expected)}")

    actual_surface = enum_at(surface, ["$defs", "rigor_class", "enum"], findings)
    if sorted(actual_surface) != expected:
        findings.append(f"agent-rigor-class-closure: expected {expected}, got {sorted(actual_surface)}")

    surface_defs = surface.get("$defs")
    workflow_contract = surface_defs.get("workflow_contract") if isinstance(surface_defs, dict) else None
    workflow_properties = workflow_contract.get("properties") if isinstance(workflow_contract, dict) else None
    rigor_property = workflow_properties.get("rigor_class") if isinstance(workflow_properties, dict) else None
    if not isinstance(rigor_property, dict) or rigor_property.get("$ref") != "#/$defs/rigor_class":
        findings.append("agent-rigor-class-reference: workflow_contract.rigor_class must reference #/$defs/rigor_class")

    actual_scenario = enum_at(
        scenario,
        ["$defs", "eventPayload", "properties", "rigor_class", "enum"],
        findings,
    )
    if sorted(actual_scenario) != expected:
        findings.append(f"scenario-rigor-class-closure: expected {expected}, got {sorted(actual_scenario)}")

    try:
        source = (root / SCHEMA_SOURCE).read_text(encoding="utf-8")
    except OSError as exc:
        findings.append(f"migration-source: unable to read {SCHEMA_SOURCE}: {exc}")
        return findings
    sql = migration_block(source)
    if sql is None:
        findings.append("migration-coverage: missing migration 54 rigor_class_vocabulary")
        return findings
    for name in TRIGGERS:
        actual = sorted(trigger_values(sql, name, findings))
        if actual != expected:
            findings.append(f"migration-rigor-class-closure: {name} expected {expected}, got {actual}")
    return findings


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--root", type=Path, default=ROOT)
    args = parser.parse_args(argv)
    findings = check(args.root.resolve())
    for finding in findings:
        print(finding)
    if findings:
        print(f"rigor-class vocabulary check failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    print("rigor-class vocabulary check passed", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))

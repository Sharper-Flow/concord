#!/usr/bin/env python3
"""Check that relation vocabulary projections stay closed and consistent."""
from __future__ import annotations

import argparse
import json
import subprocess
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
VOCABULARY = Path("contracts/relation-vocabulary.v1.json")
SURFACE_SCHEMA = Path("contracts/agent-tool-surface-payloads.schema.json")
MAX_FINDINGS = 1000


def _load_json(root: Path, relative: Path, findings: list[str]) -> object | None:
    path = root / relative
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
        findings.append(f"json-load: {relative}: invalid JSON: {exc}")
        return None


def _run_generator(root: Path, findings: list[str]) -> None:
    command = [sys.executable, str(root / "scripts/generate-relation-vocabulary.py"), "--check"]
    try:
        result = subprocess.run(
            command,
            cwd=root,
            capture_output=True,
            text=True,
            check=False,
        )
    except OSError as exc:
        findings.append(f"generator-freshness: unable to run generator: {exc}")
        return
    if result.returncode == 0:
        return
    detail = result.stderr.strip() or result.stdout.strip() or f"exit status {result.returncode}"
    findings.append(f"generator-freshness: {detail.replace(chr(10), ' ')}")


def _enum(definitions: object, name: str, findings: list[str]) -> list[str]:
    if not isinstance(definitions, dict):
        findings.append(f"schema-enum: $defs.{name}: definitions are not an object")
        return []
    definition = definitions.get(name)
    if not isinstance(definition, dict) or not isinstance(definition.get("enum"), list):
        findings.append(f"schema-enum: $defs.{name}.enum: missing array")
        return []
    return [value for value in definition["enum"] if isinstance(value, str)]


def _property_enum(
    definitions: object, definition_name: str, property_name: str, findings: list[str]
) -> list[str]:
    if not isinstance(definitions, dict):
        findings.append(f"schema-enum: $defs.{definition_name}.properties.{property_name}.enum: definitions are not an object")
        return []
    definition = definitions.get(definition_name)
    properties = definition.get("properties") if isinstance(definition, dict) else None
    property_schema = properties.get(property_name) if isinstance(properties, dict) else None
    if not isinstance(property_schema, dict) or not isinstance(property_schema.get("enum"), list):
        findings.append(
            f"schema-enum: $defs.{definition_name}.properties.{property_name}.enum: missing array"
        )
        return []
    return [value for value in property_schema["enum"] if isinstance(value, str)]


def _relation_kinds(vocabulary: object, findings: list[str]) -> list[dict[str, object]]:
    if not isinstance(vocabulary, dict) or not isinstance(vocabulary.get("kinds"), list):
        findings.append("vocabulary-shape: kinds must be an array")
        return []
    kinds: list[dict[str, object]] = []
    for index, value in enumerate(vocabulary["kinds"]):
        if not isinstance(value, dict) or not isinstance(value.get("kind"), str):
            findings.append(f"vocabulary-shape: kinds[{index}] must declare a string kind")
            continue
        kinds.append(value)
    return kinds


def _report_set_difference(
    label: str, expected: list[str], actual: list[str], findings: list[str]
) -> None:
    missing = sorted(set(expected) - set(actual))
    extra = sorted(set(actual) - set(expected))
    if missing:
        findings.append(f"{label}: missing member(s): {missing}")
    if extra:
        findings.append(f"{label}: extra member(s): {extra}")


def check(root: Path) -> list[str]:
    findings: list[str] = []
    _run_generator(root, findings)

    vocabulary = _load_json(root, VOCABULARY, findings)
    surface_path = root / SURFACE_SCHEMA
    surface = _load_json(root, SURFACE_SCHEMA, findings)
    kinds = _relation_kinds(vocabulary, findings)
    definitions = surface.get("$defs") if isinstance(surface, dict) else None

    names = [kind["kind"] for kind in kinds if isinstance(kind.get("kind"), str)]
    addressable = sorted(
        kind["kind"]
        for kind in kinds
        if kind.get("link") in {"allowed", "refused"} and isinstance(kind.get("kind"), str)
    )
    query_labels = sorted(
        name
        for kind in kinds
        for name in [kind.get("kind"), kind.get("inverse_label")]
        if isinstance(name, str)
    )
    link_enum = sorted(_enum(definitions, "relation_link_kind", findings))
    query_enum = sorted(_enum(definitions, "relation_query_label", findings))
    _report_set_difference("link-kind-closure", addressable, link_enum, findings)
    _report_set_difference("query-label-closure", query_labels, query_enum, findings)

    declared_labels = set(query_labels)
    for label in query_enum:
        if label not in declared_labels:
            findings.append(
                f"query-label-orphan: label {label!r} does not resolve to a declared kind or inverse label"
            )

    overlap_kinds = {
        kind["kind"]
        for kind in kinds
        if isinstance(kind.get("kind"), str)
        and isinstance(kind.get("written_by"), list)
        and "workflow.overlap_resolved" in kind["written_by"]
    }
    overlap_enum = _property_enum(
        definitions,
        "work_relate_resolve_overlap_input",
        "resolution_kind",
        findings,
    )
    for member in sorted(set(overlap_enum) - overlap_kinds):
        findings.append(
            f"overlap-resolution-subset: member {member!r} is not written by workflow.overlap_resolved"
        )

    try:
        schema_text = surface_path.read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError) as exc:
        findings.append(f"stale-ref: unable to read {SURFACE_SCHEMA}: {exc}")
    else:
        if "#/$defs/relation_kind" in schema_text:
            findings.append(
                "stale-ref: contracts/agent-tool-surface-payloads.schema.json contains #/$defs/relation_kind"
            )

    inverse_labels: dict[str, str] = {}
    name_set = set(names)
    for kind in kinds:
        name = kind.get("kind")
        inverse = kind.get("inverse_label")
        if not isinstance(name, str) or not isinstance(inverse, str):
            continue
        if inverse in inverse_labels:
            findings.append(
                f"inverse-label-uniqueness: inverse label {inverse!r} is declared by both "
                f"{inverse_labels[inverse]!r} and {name!r}"
            )
        else:
            inverse_labels[inverse] = name
        if inverse in name_set:
            findings.append(
                f"inverse-label-uniqueness: inverse label {inverse!r} collides with a kind name"
            )

    return findings


def report(findings: list[str]) -> int:
    for finding in findings[:MAX_FINDINGS]:
        print(finding)
    if len(findings) > MAX_FINDINGS:
        print(f"... {len(findings) - MAX_FINDINGS} additional finding(s) omitted")
    if findings:
        print(f"relation vocabulary check failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    print("relation vocabulary check passed", file=sys.stderr)
    return 0


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--root", type=Path, default=ROOT, help="repository root")
    args = parser.parse_args(argv)
    return report(check(args.root.resolve()))


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))

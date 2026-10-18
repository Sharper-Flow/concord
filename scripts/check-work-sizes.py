#!/usr/bin/env python3
"""Check the work-size vocabulary closure over its generated Go, the stored CHECK, and the agent schemas."""
from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

from migration_source import migrations
from vocabulary_utils import generator_freshness, load_json_finding


ROOT = Path(__file__).resolve().parents[1]
VOCABULARY = Path("contracts/work-sizes.v1.json")
SCHEMA_SOURCE = Path("internal/store/schema.go")
STORE_SOURCE = Path("internal/store")
GENERATED = "generated_work_sizes.go"
PAYLOAD_SCHEMA = Path("contracts/agent-tool-surface-payloads.schema.json")
HOST_SURFACE = Path("contracts/host-tool-surface.v1.json")
# A capture declares what the change touches; the model decides the size, so
# no size_inputs object may carry a size an agent could propose.
PROPOSED_SIZE_FIELDS = {"size", "size_class", "model_size"}
SIZE_CHECK = re.compile(
    r"\bsize_class\s+TEXT\s+NOT\s+NULL\s+CHECK\s*\(\s*size_class\s+IN\s*\((?P<values>[^)]*)\)\s*\)",
    re.I,
)
TABLE = re.compile(r"\bCREATE\s+TABLE\s+work_size_assessments\b", re.I)
GO_SIZE_CONSTANT = re.compile(r"\bWorkSize\s*=\s*\"")


def stored_sizes(root: Path, findings: list[str]) -> list[str] | None:
    """Read the size CHECK from the last migration that creates the table,
    so a later table rebuild, not the first definition, is what is compared."""
    try:
        source = (root / SCHEMA_SOURCE).read_text(encoding="utf-8")
        entries = migrations(source)
    except (OSError, ValueError) as exc:
        findings.append(f"migration-source: unable to read {SCHEMA_SOURCE}: {exc}")
        return None
    creating = [sql for _, _, sql in entries if TABLE.search(sql)]
    if not creating:
        findings.append("migration-coverage: no migration creates work_size_assessments")
        return None
    match = SIZE_CHECK.search(creating[-1])
    if match is None:
        findings.append("migration-check-coverage: work_size_assessments.size_class has no literal IN CHECK")
        return None
    return [value.strip().strip("'") for value in match.group("values").split(",")]


def check_hand_written_constants(root: Path, findings: list[str]) -> None:
    for path in sorted((root / STORE_SOURCE).glob("*.go")):
        if path.name == GENERATED or path.name.endswith("_test.go"):
            continue
        if GO_SIZE_CONSTANT.search(path.read_text(encoding="utf-8")):
            findings.append(f"go-closure: {path.relative_to(root)} declares a WorkSize constant outside {GENERATED}")


def check_payload_schema(root: Path, declared: list[str], findings: list[str]) -> None:
    payload = load_json_finding(root, PAYLOAD_SCHEMA, findings)
    if not isinstance(payload, dict):
        return
    defs = payload.get("$defs", {})
    size = defs.get("work_size_summary", {}).get("properties", {}).get("size", {})
    if not isinstance(size.get("enum"), list):
        findings.append("schema-coverage: $defs/work_size_summary.size has no enum")
    elif size["enum"] != declared:
        findings.append(f"schema-closure: work_size_summary.size expected {declared}, got {size['enum']}")
    if defs.get("work_summary", {}).get("properties", {}).get("size") != {"$ref": "#/$defs/work_size_summary"}:
        findings.append("schema-coverage: work_summary.size does not reference $defs/work_size_summary")
    check_no_proposed_size(defs.get("work_size_inputs"), f"{PAYLOAD_SCHEMA}#/$defs/work_size_inputs", findings)


def check_host_surface(root: Path, findings: list[str]) -> None:
    host = load_json_finding(root, HOST_SURFACE, findings)
    if not isinstance(host, dict):
        return
    tools = [tool for tool in host.get("tools", []) if isinstance(tool, dict) and tool.get("name") == "concord_work_start"]
    branches = tools[0].get("args", {}).get("oneOf", []) if tools else []
    if not branches or not isinstance(branches[0], dict):
        findings.append("host-coverage: concord_work_start has no capture branch")
        return
    check_no_proposed_size(branches[0].get("properties", {}).get("size_inputs"), f"{HOST_SURFACE} concord_work_start size_inputs", findings)


def check_no_proposed_size(node: object, where: str, findings: list[str]) -> None:
    if not isinstance(node, dict) or not isinstance(node.get("properties"), dict):
        findings.append(f"schema-coverage: {where} is not declared")
        return
    if node.get("additionalProperties") is not False:
        findings.append(f"schema-closure: {where} is not closed")
    proposed = sorted(PROPOSED_SIZE_FIELDS & set(node["properties"]))
    if proposed:
        findings.append(f"schema-closure: {where} declares a proposed size field {proposed}")


def check(root: Path) -> list[str]:
    findings: list[str] = []
    generator_freshness(root, "generate-work-sizes.py", findings)
    vocabulary = load_json_finding(root, VOCABULARY, findings)
    if not isinstance(vocabulary, dict) or not isinstance(vocabulary.get("sizes"), list):
        findings.append("vocabulary-shape: sizes must be an array")
        return findings
    declared = [item.get("size") for item in vocabulary["sizes"] if isinstance(item, dict)]
    stored = stored_sizes(root, findings)
    if stored is not None and sorted(stored) != sorted(declared):
        findings.append(f"migration-check-closure: expected {declared}, got {stored}")
    check_hand_written_constants(root, findings)
    check_payload_schema(root, declared, findings)
    check_host_surface(root, findings)
    return findings


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--root", type=Path, default=ROOT)
    args = parser.parse_args(argv)
    findings = check(args.root.resolve())
    for finding in findings:
        print(finding)
    if findings:
        print(f"work-size vocabulary check failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    print("work-size vocabulary check passed", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))

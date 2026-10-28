#!/usr/bin/env python3
"""Check the work-size vocabulary closure over its generated Go, the stored CHECK, and the agent schemas."""
from __future__ import annotations

import argparse
import copy
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
PACKET_SCHEMA = Path("contracts/agent-lane-packet.schema.json")
HOST_SURFACE = Path("contracts/host-tool-surface.v1.json")
# A capture declares what the change touches; the model decides the size, so
# no size_inputs object may carry a size an agent could propose.
PROPOSED_SIZE_FIELDS = {"size", "size_class", "model_size"}
SIZE_CHECK = re.compile(
    r"\bsize_class\s+TEXT\s+NOT\s+NULL\s+CHECK\s*\(\s*size_class\s+IN\s*\((?P<values>[^)]*)\)\s*\)",
    re.I,
)
# Every table that stores a size; each one's CHECK must equal the vocabulary.
SIZE_TABLES = ("work_size_assessments", "work_size_overrides")
TABLE_BODY = r"\bCREATE\s+TABLE\s+{table}\s*\((?P<body>.*?)\n\);"
GO_SIZE_CONSTANT = re.compile(r"\bWorkSize\s*=\s*\"")


def stored_sizes(entries: list, table: str, findings: list[str]) -> list[str] | None:
    """Read the table's size CHECK from the last migration that creates it,
    so a later table rebuild, not the first definition, is what is compared."""
    pattern = re.compile(TABLE_BODY.format(table=table), re.I | re.S)
    bodies = [match.group("body") for _, _, sql in entries if (match := pattern.search(sql))]
    if not bodies:
        findings.append(f"migration-coverage: no migration creates {table}")
        return None
    match = SIZE_CHECK.search(bodies[-1])
    if match is None:
        findings.append(f"migration-check-coverage: {table}.size_class has no literal IN CHECK")
        return None
    return [value.strip().strip("'") for value in match.group("values").split(",")]


def check_stored_sizes(root: Path, declared: list[str], findings: list[str]) -> None:
    try:
        entries = migrations((root / SCHEMA_SOURCE).read_text(encoding="utf-8"))
    except (OSError, ValueError) as exc:
        findings.append(f"migration-source: unable to read {SCHEMA_SOURCE}: {exc}")
        return
    for table in SIZE_TABLES:
        stored = stored_sizes(entries, table, findings)
        if stored is not None and sorted(stored) != sorted(declared):
            findings.append(f"migration-check-closure: {table} expected {declared}, got {stored}")


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
    size_class = defs.get("work_size_class", {})
    if size_class.get("enum") != declared:
        findings.append(f"schema-closure: work_size_class expected {declared}, got {size_class.get('enum')}")
    revise = defs.get("work_define_revise_input", {}).get("properties", {})
    history = defs.get("work_event_page", {}).get("properties", {}).get("entries", {}).get("items", {}).get("properties", {})
    references = {
        "work_size_summary.model_size": defs.get("work_size_summary", {}).get("properties", {}).get("model_size"),
        "work_size_summary.operator_size": defs.get("work_size_summary", {}).get("properties", {}).get("operator_size"),
        "work_define_revise_input.operator_size": revise.get("operator_size"),
        "work_event_page.conflict_application.size": history.get("conflict_application", {}).get("properties", {}).get("size"),
    }
    for where, node in references.items():
        if not isinstance(node, dict) or node.get("$ref") != "#/$defs/work_size_class":
            findings.append(f"schema-coverage: {where} does not reference $defs/work_size_class")
    # A rerun declares inputs exactly as a capture does, so it shares the
    # capture's closed object and can propose no size either.
    for operation in ("work_define_capture_input", "work_define_revise_input"):
        node = defs.get(operation, {}).get("properties", {}).get("size_inputs")
        if not isinstance(node, dict) or node.get("$ref") != "#/$defs/work_size_inputs":
            findings.append(f"schema-coverage: {operation}.size_inputs does not reference $defs/work_size_inputs")
    check_no_proposed_size(defs.get("work_size_inputs"), f"{PAYLOAD_SCHEMA}#/$defs/work_size_inputs", findings)
    check_review_size_schema(root, defs, findings)


def check_review_size_schema(root: Path, payload_defs: dict, findings: list[str]) -> None:
    packet = load_json_finding(root, PACKET_SCHEMA, findings)
    if not isinstance(packet, dict):
        return
    defs = packet.get("$defs", {})
    reference = defs.get("lane_review_input", {}).get("properties", {}).get("size", {})
    if reference.get("$ref") != "#/$defs/lane_work_size_summary":
        findings.append("schema-coverage: lane_review_input.size does not reference $defs/lane_work_size_summary")
    if defs.get("lane_work_size_class") != payload_defs.get("work_size_class"):
        findings.append("schema-closure: lane_work_size_class differs from the canonical work_size_class")
    expected = copy.deepcopy(payload_defs.get("work_size_summary", {}))
    for member in ("model_size", "operator_size"):
        expected.get("properties", {})[member] = {"$ref": "#/$defs/lane_work_size_class"}
    if defs.get("lane_work_size_summary") != expected:
        findings.append("schema-closure: lane_work_size_summary differs from the canonical work_size_summary")


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
    check_stored_sizes(root, declared, findings)
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

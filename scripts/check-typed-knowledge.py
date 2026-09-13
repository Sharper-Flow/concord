#!/usr/bin/env python3
"""Validate the proposed typed-knowledge records and retrieval contract.

This checker is deliberately separate from the accepted knowledge-index checker.
The files under ``.concord/`` are review artifacts. They do not register law,
move existing documents, or change runtime retrieval.
"""

from __future__ import annotations

import importlib.util
import json
import re
import sys
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parents[1]
SCHEMA = ROOT / ".concord/schemas/knowledge-record.v1.schema.json"
EVALUATION_SCHEMA = ROOT / ".concord/schemas/knowledge-retrieval-evaluation.v1.schema.json"
FIXTURES = ROOT / ".concord/scenarios/knowledge-record-fixtures.v1.json"
EVALUATION_FIXTURE = ROOT / ".concord/scenarios/knowledge-retrieval-evaluation.v1.json"
KINDS = {"constitution", "decision", "spec", "lesson", "research", "reference", "work_note"}
CYCLIC_RELATIONS = {"depends_on", "refines", "supersedes", "implements", "derived_from", "conflicts_with"}
ELEMENT_RE = re.compile(r"^(section|requirement):[a-z][a-z0-9_-]{2,120}$")


def _load_schema_validator() -> Any:
    path = ROOT / "scripts/generate-agent-contracts.py"
    spec = importlib.util.spec_from_file_location("concord_schema_validator", path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load {path}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module.schema_validate


schema_validate = _load_schema_validator()


def load_json(path: Path) -> object:
    return json.loads(path.read_text(encoding="utf-8"))


def schema_findings(value: object, schema: object, label: str) -> list[str]:
    try:
        schema_validate(value, schema, schema, label)
    except ValueError as exc:
        return [f"{label}: schema validation failed: {exc}"]
    return []


def _record_elements(record: dict[str, Any]) -> tuple[set[str], set[str], list[dict[str, Any]]]:
    sections: set[str] = set()
    requirements: set[str] = set()
    all_requirements: list[dict[str, Any]] = []
    for section in record.get("body", {}).get("sections", []):
        section_id = section.get("id")
        sections.add(section_id)
        for requirement in section.get("requirements", []):
            requirements.add(requirement.get("id"))
            all_requirements.append(requirement)
    return sections, requirements, all_requirements


def validate_record(record: object, records_by_id: dict[str, dict[str, Any]] | None = None) -> list[str]:
    """Return deterministic schema and cross-record findings for one record."""
    findings = schema_findings(record, load_json(SCHEMA), "record")
    if findings or not isinstance(record, dict):
        return findings

    identifier = record["id"]
    kind = record["kind"]
    scope = record["scope"]
    provenance = record["provenance"]
    status = record["status"]
    expected = f".concord/knowledge/{kind}/{identifier}.json"
    if provenance["location_authority"] == "canonical":
        if provenance["canonical_path"] != expected:
            findings.append(f"record {identifier}: canonical_path must be {expected}")
    else:
        override = provenance.get("override")
        if not override or override["path"] == expected:
            findings.append(f"record {identifier}: operator override must name a distinct path")
        elif provenance["canonical_path"] != override["path"]:
            findings.append(f"record {identifier}: canonical_path must equal the approved override path")
        if scope["mode"] != "explicit":
            findings.append(f"record {identifier}: an operator override requires explicit scope")
        elif override["scope"] == "product" and not scope["product_ids"]:
            findings.append(f"record {identifier}: product override has no product scope")
        elif override["scope"] == "project" and not scope["project_ids"]:
            findings.append(f"record {identifier}: project override has no project scope")
        elif override["scope"] == "work" and not scope["work_ids"]:
            findings.append(f"record {identifier}: work override has no work scope")

    if kind in {"constitution", "decision", "spec"} and status["state"] not in {"current", "superseded"}:
        findings.append(f"record {identifier}: law-bearing kind has invalid status")
    if kind not in {"constitution", "decision", "spec"} and status["state"] not in {"current", "historical", "superseded"}:
        findings.append(f"record {identifier}: non-law kind has invalid status")

    sections, requirements, all_requirements = _record_elements(record)
    body = record["body"]
    seen_sections: set[str] = set()
    seen_requirements: set[str] = set()
    for section in body["sections"]:
        if section["id"] in seen_sections:
            findings.append(f"record {identifier}: duplicate section ID {section['id']}")
        seen_sections.add(section["id"])
        for requirement in section["requirements"]:
            if requirement["id"] in seen_requirements:
                findings.append(f"record {identifier}: duplicate requirement ID {requirement['id']}")
            seen_requirements.add(requirement["id"])
    if kind == "spec":
        for criterion in body["acceptance_criteria"]:
            if criterion["id"] not in seen_requirements:
                findings.append(f"record {identifier}: acceptance criterion {criterion['id']} has no stable requirement element")
    reference_ids = [reference["id"] for reference in record["references"]]
    if len(reference_ids) != len(set(reference_ids)):
        findings.append(f"record {identifier}: duplicate typed reference ID")
    source_by_id = {source["id"]: source for source in body["normative_sources"]}
    if len(source_by_id) != len(body["normative_sources"]):
        findings.append(f"record {identifier}: duplicate normative source ID")
    for source in body["normative_sources"]:
        for element_id in source["supports"]:
            if not ELEMENT_RE.fullmatch(element_id) or element_id not in sections | requirements:
                findings.append(f"record {identifier}: source {source['id']} has a dangling element reference {element_id}")
    for requirement in all_requirements:
        if requirement["normative"] and not requirement["source_ids"]:
            findings.append(f"record {identifier}: normative requirement {requirement['id']} has no source proof")
        for source_id in requirement["source_ids"]:
            source = source_by_id.get(source_id)
            if source is None:
                findings.append(f"record {identifier}: requirement {requirement['id']} has a dangling source reference {source_id}")
            elif requirement["id"] not in source["supports"]:
                findings.append(f"record {identifier}: source {source_id} does not prove {requirement['id']}")
        for ref_id in requirement["verification_refs"]:
            if records_by_id is not None and ref_id not in records_by_id:
                findings.append(f"record {identifier}: requirement {requirement['id']} has a dangling verification record {ref_id}")

    if records_by_id is not None:
        for reference in record["references"]:
            target_id = reference["target_record_id"]
            target = records_by_id.get(target_id)
            if target is None:
                findings.append(f"record {identifier}: dangling reference target {target_id}")
                continue
            target_sections, target_requirements, _ = _record_elements(target)
            target_element = reference.get("target_element_id")
            if target_element is not None and target_element not in target_sections | target_requirements:
                findings.append(f"record {identifier}: dangling target element {target_element} on {target_id}")
            if reference["relation"] == "supersedes":
                if target["status"]["state"] != "superseded" or target["status"].get("successor_id") != identifier:
                    findings.append(f"record {identifier}: supersedes target {target_id} lacks matching successor proof")

    return findings


def relation_cycle_findings(records: list[dict[str, Any]]) -> list[str]:
    graph: dict[str, set[str]] = {}
    for record in records:
        source = record["id"]
        for reference in record["references"]:
            if reference["relation"] in CYCLIC_RELATIONS:
                graph.setdefault(source, set()).add(reference["target_record_id"])
    visiting: set[str] = set()
    visited: set[str] = set()

    def visit(node: str) -> bool:
        if node in visiting:
            return True
        if node in visited:
            return False
        visiting.add(node)
        if any(visit(child) for child in graph.get(node, set())):
            return True
        visiting.remove(node)
        visited.add(node)
        return False

    if any(visit(node) for node in sorted(graph)):
        return ["records: relevant typed-reference relation contains a cycle"]
    return []


def validate_record_fixture() -> list[str]:
    value = load_json(FIXTURES)
    if not isinstance(value, dict):
        return ["knowledge-record-fixtures.v1.json: top-level value must be an object"]
    if value.get("schema_version") != "1.0" or value.get("contract_status") != "proposed":
        return ["knowledge-record-fixtures.v1.json: fixture metadata is not proposed v1"]
    valid = value.get("valid_records")
    invalid = value.get("invalid_cases")
    if not isinstance(valid, list) or len(valid) != 7:
        return ["knowledge-record-fixtures.v1.json: exactly seven valid kind fixtures are required"]
    if not isinstance(invalid, list) or len(invalid) < 28:
        return ["knowledge-record-fixtures.v1.json: missing-required-field, unknown-field, wrong-kind/status, and integrity cases are required for every kind"]
    findings: list[str] = []
    by_id: dict[str, dict[str, Any]] = {}
    for record in valid:
        if isinstance(record, dict):
            if record.get("id") in by_id:
                findings.append(f"valid records: duplicate ID {record.get('id')}")
            by_id[record.get("id")] = record
    if {record.get("kind") for record in valid if isinstance(record, dict)} != KINDS:
        findings.append("valid records: fixtures must cover the seven closed kinds")
    paths = [record.get("provenance", {}).get("canonical_path") for record in valid if isinstance(record, dict)]
    if len(paths) != len(set(paths)):
        findings.append("valid records: canonical locations must be unique")
    for record in valid:
        findings.extend(validate_record(record, by_id))
    findings.extend(relation_cycle_findings(valid))
    expected_categories = {"missing-required-field", "unknown-field", "wrong-kind/status", "reference-integrity"}
    categories = {case.get("category") for case in invalid if isinstance(case, dict)}
    if not expected_categories.issubset(categories):
        findings.append(f"invalid cases: missing categories {sorted(expected_categories - categories)}")
    for case in invalid:
        if not isinstance(case, dict) or not isinstance(case.get("id"), str) or not isinstance(case.get("record"), dict):
            findings.append("invalid cases: each case needs an ID and record")
            continue
        case_findings = validate_record(case["record"], by_id)
        if not case_findings:
            findings.append(f"invalid case {case['id']}: expected a validation failure")
    return findings


def validate_evaluation_fixture() -> list[str]:
    value = load_json(EVALUATION_FIXTURE)
    schema = load_json(EVALUATION_SCHEMA)
    findings = schema_findings(value, schema, "retrieval evaluation")
    if findings or not isinstance(value, dict):
        return findings
    workload = value["workload"]
    floor = workload["pm1_metadata_floor"]
    if workload["id"] != "pm1-metadata-queries" or workload["scale_multiplier"] != 10:
        findings.append("retrieval evaluation: PM1 metadata workload must be identified at 10x scale")
    if floor["threshold_ms"] != 100 or floor["status"] != "proposed":
        findings.append("retrieval evaluation: PM1 metadata P99 floor must remain a proposed 100 ms target")
    if value["protocol"]["privacy"]["private_content_egress"] != "forbidden_by_default" or not value["protocol"]["privacy"]["operator_permission_required"]:
        findings.append("retrieval evaluation: private content egress must require operator permission")
    targets = {target["id"]: target for target in value["proposed_targets"]}
    for result in value["measured_results"]:
        if result["target_id"] not in targets:
            findings.append(f"retrieval evaluation: measured result targets unknown proposal {result['target_id']}")
        elif result["metric"] != targets[result["target_id"]]["metric"]:
            findings.append(f"retrieval evaluation: measured result {result['id']} changes the target metric")
    return findings


def main() -> int:
    findings = validate_record_fixture() + validate_evaluation_fixture()
    for finding in findings:
        print(finding, file=sys.stderr)
    if findings:
        return 1
    print("typed knowledge draft validation passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

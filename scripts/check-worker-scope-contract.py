#!/usr/bin/env python3
"""Check that the worker-scope contract stays closed against the lane registry.

The worker-scope contract assigns every registered lane the one result a
worker attempt completes, named by the closed evidence obligation whose
discharge evidences that result. This check enforces four invariants the
schema alone cannot express: the assigned-result vocabulary equals the lane
registry's evidence-obligation enum, every registered lane carries exactly
one assignment, each named result is declared by that lane's evidence
obligations, and the design lane stays scoped to visual results.
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
CONTRACT = Path("contracts/worker-scope.v1.json")
CONTRACT_SCHEMA = Path("contracts/worker-scope.schema.json")
LANE_REGISTRY = Path("contracts/agent-lanes.v1.json")
LANE_SCHEMA = Path("contracts/agent-lanes.schema.json")
MAX_FINDINGS = 100
DESIGN_LANE = "design"
DESIGN_RESULT = "visual_artifacts"


def _load_json(root: Path, relative: Path, findings: list[str]) -> object | None:
    path = root / relative
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
        findings.append(f"json-load: {relative}: invalid JSON: {exc}")
        return None


def _enum(schema: object, defs_key: str, label: str, findings: list[str]) -> list[str]:
    if not isinstance(schema, dict):
        findings.append(f"schema-shape: {label}: schema is not an object")
        return []
    definitions = schema.get("$defs")
    definition = definitions.get(defs_key) if isinstance(definitions, dict) else None
    if not isinstance(definition, dict) or not isinstance(definition.get("enum"), list):
        findings.append(f"schema-shape: {label}: $defs.{defs_key}.enum is missing")
        return []
    return [value for value in definition["enum"] if isinstance(value, str)]


def _result_enum(
    contract_schema: object, lane_schema: object, findings: list[str]
) -> list[str]:
    contract_enum = _enum(contract_schema, "result_obligation", str(CONTRACT_SCHEMA), findings)
    lane_enum = _enum(lane_schema, "evidence_obligation", str(LANE_SCHEMA), findings)
    if contract_enum and lane_enum and contract_enum != lane_enum:
        findings.append(
            "vocabulary-drift: the worker-scope result enum and the lane evidence-obligation enum differ; "
            "assigned results must draw from one closed vocabulary"
        )
    return contract_enum


def _premise_consts(contract_schema: object, findings: list[str]) -> dict[str, str]:
    if not isinstance(contract_schema, dict):
        return {}
    properties = contract_schema.get("properties")
    premise_schema = properties.get("premise") if isinstance(properties, dict) else None
    premise_properties = premise_schema.get("properties") if isinstance(premise_schema, dict) else None
    if not isinstance(premise_properties, dict):
        findings.append("schema-shape: worker-scope.schema.json: premise properties are missing")
        return {}
    consts: dict[str, str] = {}
    for key, schema in premise_properties.items():
        if isinstance(schema, dict) and isinstance(schema.get("const"), str):
            consts[key] = schema["const"]
        else:
            findings.append(f"schema-shape: worker-scope.schema.json: premise.{key} carries no const clause")
    return consts


def validate(
    contract: object,
    contract_schema: object,
    lanes: object,
    lane_schema: object,
) -> list[str]:
    findings: list[str] = []
    result_enum = _result_enum(contract_schema, lane_schema, findings)
    premise_consts = _premise_consts(contract_schema, findings)

    if not isinstance(contract, dict):
        findings.append("contract-shape: the worker-scope contract is not an object")
        return findings
    if contract.get("schema_version") != "1.0" or contract.get("registry") != "worker_scope" or contract.get("version") != 1:
        findings.append("contract-shape: schema_version, registry, and version must be 1.0, worker_scope, and 1")
    summary = contract.get("summary")
    if not isinstance(summary, str) or not (2 <= len(summary) <= 1024):
        findings.append("contract-shape: summary must be a string of 2..1024 characters")

    premise = contract.get("premise")
    if not isinstance(premise, dict):
        findings.append("premise-clause: the premise is not an object")
    else:
        for key, const in premise_consts.items():
            if premise.get(key) != const:
                findings.append(f"premise-clause: premise.{key} does not carry the approved objective verbatim")
        for key in premise:
            if key not in premise_consts:
                findings.append(f"premise-clause: premise.{key} is not a declared clause")

    if not isinstance(lanes, dict) or not isinstance(lanes.get("lanes"), list):
        findings.append("lane-registry: agent-lanes.v1.json carries no lanes array")
        return findings
    registry_lanes: dict[str, dict] = {}
    for value in lanes["lanes"]:
        if isinstance(value, dict) and isinstance(value.get("id"), str):
            registry_lanes[value["id"]] = value
        else:
            findings.append("lane-registry: a registered lane carries no string id")

    assignments = contract.get("assignments")
    if not isinstance(assignments, list) or not assignments:
        findings.append("assignment-shape: assignments must be a non-empty array")
        return findings

    enum_values = set(result_enum)
    seen: dict[str, str] = {}
    for entry in assignments:
        if not isinstance(entry, dict):
            findings.append("assignment-shape: an assignment is not an object")
            continue
        lane_id = entry.get("lane_id")
        result = entry.get("result")
        if not isinstance(lane_id, str) or not isinstance(result, str):
            findings.append("assignment-shape: lane_id and result must be strings")
            continue
        if lane_id in seen:
            findings.append(f"assignment-duplicate-lane: {lane_id} carries more than one assigned result")
            continue
        seen[lane_id] = result
        if lane_id not in registry_lanes:
            findings.append(f"assignment-unknown-lane: {lane_id} is not a registered lane")
            continue
        if result not in enum_values:
            findings.append(f"assignment-unknown-result: {result} is not in the closed evidence-obligation vocabulary")
        lane_obligations = registry_lanes[lane_id].get("evidence_obligations")
        if not isinstance(lane_obligations, list) or result not in lane_obligations:
            findings.append(
                f"assignment-undeclared-obligation: lane {lane_id} assigns result {result}, "
                "which the lane registry does not declare among its evidence obligations"
            )

    for lane_id in sorted(set(registry_lanes) - set(seen)):
        findings.append(f"assignment-missing-lane: registered lane {lane_id} carries no assigned result")

    design_result = seen.get(DESIGN_LANE)
    if design_result is not None and design_result != DESIGN_RESULT:
        findings.append(
            f"design-scope: the design lane is scoped to visual results, "
            f"but its assigned result is {design_result}"
        )
    return findings


def run(root: Path) -> list[str]:
    findings: list[str] = []
    contract = _load_json(root, CONTRACT, findings)
    contract_schema = _load_json(root, CONTRACT_SCHEMA, findings)
    lanes = _load_json(root, LANE_REGISTRY, findings)
    lane_schema = _load_json(root, LANE_SCHEMA, findings)
    if contract is not None and contract_schema is not None and lanes is not None and lane_schema is not None:
        findings.extend(validate(contract, contract_schema, lanes, lane_schema))
    return findings


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--root", type=Path, default=ROOT, help="repository root")
    args = parser.parse_args()

    findings = run(args.root.resolve())
    for finding in findings[:MAX_FINDINGS]:
        print(finding)
    if len(findings) > MAX_FINDINGS:
        print(f"... {len(findings) - MAX_FINDINGS} additional finding(s) omitted")
    if findings:
        print(f"worker scope contract check failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    print("worker scope contract check passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

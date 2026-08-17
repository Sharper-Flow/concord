#!/usr/bin/env python3
"""Validate the TS1 agent-jobs scenario corpus against its closed schema."""
from __future__ import annotations

import copy
import importlib.util
import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SCHEMA_PATH = ROOT / "contracts/agent-jobs-scenarios.schema.json"
CORPUS_PATH = ROOT / "scenarios/agent-jobs.v1.json"
ENVELOPE_PATH = ROOT / "contracts/agent-tool-envelope.schema.json"
EXPECTED_SCENARIO_COUNT = 21
EXPECTED_CONTRACT = "TS1"
EXPECTED_CONTRACT_STATUS = "accepted"
ERROR_KIND_PATH = "error.kind"


def _load_generator() -> object:
    spec = importlib.util.spec_from_file_location(
        "concord_contract_generator", ROOT / "scripts/generate-agent-contracts.py"
    )
    if spec is None or spec.loader is None:
        raise RuntimeError("unable to load shared JSON Schema validator")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


_generator = _load_generator()
schema_validate = _generator.schema_validate
check_schema_keywords = _generator.check_schema_keywords


def _load_json(path: Path) -> object:
    return json.loads(path.read_text(encoding="utf-8"))


def _check_closed_schema(path: Path, required: set[str]) -> list[str]:
    findings: list[str] = []
    try:
        value = _load_json(path)
    except (OSError, json.JSONDecodeError) as exc:
        return [f"{path.relative_to(ROOT)}: {exc}"]
    if not isinstance(value, dict) or value.get("type") != "object":
        findings.append(f"{path.relative_to(ROOT)}: root must be an object schema")
    if isinstance(value, dict) and set(value.get("required", [])) != required:
        findings.append(
            f"{path.relative_to(ROOT)}: required keys do not match {sorted(required)}"
        )

    def visit(node: object, location: str) -> None:
        if not isinstance(node, dict):
            return
        if (
            node.get("type") == "object"
            and "additionalProperties" not in node
            and "propertyNames" not in node
        ):
            findings.append(
                f"{path.relative_to(ROOT)}:{location}: object is not closed"
            )
        if node.get("type") == "array":
            if not isinstance(node.get("minItems"), int) or not isinstance(
                node.get("maxItems"), int
            ):
                findings.append(
                    f"{path.relative_to(ROOT)}:{location}: array lacks explicit bounds"
                )
            elif node["minItems"] < 0 or node["maxItems"] < node["minItems"]:
                findings.append(
                    f"{path.relative_to(ROOT)}:{location}: invalid array bounds"
                )
        if (
            node.get("type") == "string"
            and "const" not in node
            and "enum" not in node
            and "$ref" not in node
        ):
            if not isinstance(node.get("minLength"), int) or not isinstance(
                node.get("maxLength"), int
            ):
                findings.append(
                    f"{path.relative_to(ROOT)}:{location}: string lacks explicit bounds"
                )
        for key, child in node.items():
            visit(child, f"{location}/{key}")

    visit(value, "$")
    return findings


def _validate_instance(schema_path: Path, value: object) -> str | None:
    schema = _load_json(schema_path)
    try:
        schema_validate(value, schema, schema, "instance")
    except ValueError as exc:
        return str(exc)
    return None


def _live_error_kinds() -> set[str]:
    envelope = _load_json(ENVELOPE_PATH)
    kinds = envelope["$defs"]["typedError"]["properties"]["kind"]["enum"]
    return {str(kind) for kind in kinds}


def _addresses_error_kind(path: str) -> bool:
    if not isinstance(path, str):
        return False
    return path == ERROR_KIND_PATH


def _find_assertions(scenario: dict) -> list[dict]:
    return list(scenario.get("expected", {}).get("assertions", []))


def _check_corpus(corpus: object) -> list[str]:
    findings: list[str] = []
    if not isinstance(corpus, dict):
        findings.append(
            f"{CORPUS_PATH.relative_to(ROOT)}: structural validation failed: corpus must be an object"
        )
        return findings

    corpus_error = _validate_instance(SCHEMA_PATH, corpus)
    if corpus_error:
        findings.append(
            f"{CORPUS_PATH.relative_to(ROOT)}: schema validation failed: {corpus_error}"
        )

    if corpus.get("contract") != EXPECTED_CONTRACT or corpus.get("contract_status") != EXPECTED_CONTRACT_STATUS:
        findings.append(
            f"{CORPUS_PATH.relative_to(ROOT)}: contract metadata is not accepted {EXPECTED_CONTRACT}"
        )

    scenarios = corpus.get("scenarios", [])
    if not isinstance(scenarios, list):
        findings.append(
            f"{CORPUS_PATH.relative_to(ROOT)}: structural validation failed: scenarios must be an array"
        )
        return findings

    if len(scenarios) != EXPECTED_SCENARIO_COUNT:
        findings.append(
            f"{CORPUS_PATH.relative_to(ROOT)}: expected {EXPECTED_SCENARIO_COUNT} scenarios, got {len(scenarios)}"
        )

    scenario_ids = [
        scenario.get("id") for scenario in scenarios if isinstance(scenario, dict)
    ]
    if len(set(scenario_ids)) != len(scenario_ids) or any(
        not isinstance(item, str) or not item for item in scenario_ids
    ):
        findings.append(
            f"{CORPUS_PATH.relative_to(ROOT)}: scenario IDs must be unique and nonempty"
        )

    contract = corpus.get("assertion_contract") or {}
    declared_targets = set(contract.get("targets", []))
    declared_ops = set(contract.get("ops", []))
    required_fields = list(contract.get("required_fields", []))
    shared_invariants = set((corpus.get("shared_invariants") or {}).keys())

    jobs = corpus.get("jobs") or []
    declared_jobs = {
        job.get("id")
        for job in jobs
        if isinstance(job, dict) and isinstance(job.get("id"), str)
    }
    referenced_jobs = {
        scenario.get("job_id")
        for scenario in scenarios
        if isinstance(scenario, dict) and isinstance(scenario.get("job_id"), str)
    }
    unused_jobs = declared_jobs - referenced_jobs
    if unused_jobs:
        findings.append(
            f"{CORPUS_PATH.relative_to(ROOT)}: declared job(s) referenced by no scenario: {sorted(unused_jobs)}"
        )
    unknown_jobs = referenced_jobs - declared_jobs
    if unknown_jobs:
        findings.append(
            f"{CORPUS_PATH.relative_to(ROOT)}: scenario(s) reference undeclared job(s): {sorted(unknown_jobs)}"
        )

    live_error_kinds = _live_error_kinds()
    for scenario in scenarios:
        if not isinstance(scenario, dict):
            continue
        scenario_id = scenario.get("id")
        assertions = _find_assertions(scenario)
        if len(assertions) < 1:
            findings.append(
                f"{CORPUS_PATH.relative_to(ROOT)}: scenario {scenario_id} has no assertions"
            )
            continue

        distinct_targets = {assertion.get("target") for assertion in assertions}
        durable = any(
            assertion.get("target") in {"effects", "authority"}
            or assertion.get("target") == "state"
            for assertion in assertions
        )
        if (
            len(assertions) < 2
            or len(distinct_targets) < 2
            or not distinct_targets & {"state", "effects", "authority"}
            or not durable
        ):
            findings.append(
                f"{CORPUS_PATH.relative_to(ROOT)}: stub-weak assertion set in {scenario_id}"
            )

        for assertion in assertions:
            if assertion.get("target") not in declared_targets:
                findings.append(
                    f"{CORPUS_PATH.relative_to(ROOT)}: closed assertion target violation in {scenario_id}"
                )
            if assertion.get("op") not in declared_ops:
                findings.append(
                    f"{CORPUS_PATH.relative_to(ROOT)}: closed assertion op violation in {scenario_id}"
                )
            for required_field in required_fields:
                if required_field not in assertion:
                    findings.append(
                        f"{CORPUS_PATH.relative_to(ROOT)}: missing required assertion field {required_field} in {scenario_id}"
                    )
            if _addresses_error_kind(assertion.get("path", "")):
                value = assertion.get("value")
                if not isinstance(value, str) or value not in live_error_kinds:
                    findings.append(
                        f"{CORPUS_PATH.relative_to(ROOT)}: undeclared error kind for path {assertion.get('path', '')} in {scenario_id}"
                    )

        invariants = (scenario.get("expected") or {}).get("invariants") or []
        unknown_invariants = [
            name for name in invariants if name not in shared_invariants
        ]
        if unknown_invariants:
            findings.append(
                f"{CORPUS_PATH.relative_to(ROOT)}: unknown shared invariant(s) in {scenario_id}: {sorted(unknown_invariants)}"
            )

    return findings


def _negative_self_tests(corpus: dict) -> list[str]:
    findings: list[str] = []
    scenarios = corpus.get("scenarios") or []
    if not scenarios:
        return findings

    mutated = copy.deepcopy(corpus)
    mutated["scenarios"][0]["expected"]["assertions"][0]["target"] = "not_a_real_target"
    negatives = _check_corpus(mutated)
    if not any("closed assertion target violation" in finding for finding in negatives):
        findings.append(
            "agent-jobs-negative-target: validator failed to reject unknown assertion target"
        )

    mutated = copy.deepcopy(corpus)
    mutated["scenarios"][0]["expected"].setdefault("invariants", []).append(
        "not_a_real_invariant"
    )
    negatives = _check_corpus(mutated)
    if not any("unknown shared invariant" in finding for finding in negatives):
        findings.append(
            "agent-jobs-negative-invariant: validator failed to reject unknown shared invariant"
        )

    mutated = copy.deepcopy(corpus)
    mutated["scenarios"][0]["id"] = mutated["scenarios"][1]["id"]
    negatives = _check_corpus(mutated)
    if not any("scenario IDs must be unique and nonempty" in finding for finding in negatives):
        findings.append(
            "agent-jobs-negative-duplicate-id: validator failed to reject duplicate scenario id"
        )

    mutated = copy.deepcopy(corpus)
    mutated["scenarios"][0]["job_id"] = "AJ99"
    negatives = _check_corpus(mutated)
    if not any("undeclared job" in finding for finding in negatives):
        findings.append(
            "agent-jobs-negative-undeclared-job: validator failed to reject undeclared job reference"
        )

    mutated = copy.deepcopy(corpus)
    error_kind_target = None
    for scenario in mutated.get("scenarios") or []:
        for assertion in scenario.get("expected", {}).get("assertions", []):
            if assertion.get("path") == ERROR_KIND_PATH:
                error_kind_target = assertion
                break
        if error_kind_target:
            break
    if error_kind_target is None:
        findings.append(
            "agent-jobs-negative-error-kind: no error.kind assertion available to mutate"
        )
    else:
        error_kind_target["value"] = "not_a_real_error_kind"
        negatives = _check_corpus(mutated)
        if not any("undeclared error kind" in finding for finding in negatives):
            findings.append(
                "agent-jobs-negative-error-kind: validator failed to reject undeclared error kind"
            )

    return findings


def check_agent_jobs() -> list[str]:
    findings: list[str] = []

    try:
        check_schema_keywords(_load_json(SCHEMA_PATH), str(SCHEMA_PATH.relative_to(ROOT)))
    except (OSError, json.JSONDecodeError, ValueError) as exc:
        findings.append(
            f"{SCHEMA_PATH.relative_to(ROOT)}: unsupported or invalid schema keyword: {exc}"
        )

    findings += _check_closed_schema(
        SCHEMA_PATH,
        {
            "schema_version",
            "contract",
            "contract_status",
            "fixture_sources",
            "assertion_contract",
            "runner_requirements",
            "shared_invariants",
            "jobs",
            "scenarios",
        },
    )

    try:
        corpus = _load_json(CORPUS_PATH)
    except (OSError, json.JSONDecodeError) as exc:
        findings.append(f"{CORPUS_PATH.relative_to(ROOT)}: structural validation failed: {exc}")
        return findings

    findings.extend(_check_corpus(corpus))
    findings.extend(_negative_self_tests(corpus))
    return findings


def main() -> int:
    findings = check_agent_jobs()
    if findings:
        for finding in findings:
            print(finding, file=sys.stderr)
        return 1
    print("agent jobs scenario check passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

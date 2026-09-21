#!/usr/bin/env python3
"""Focused tests for the proposed typed-knowledge contract."""

from __future__ import annotations

import copy
import importlib.util
import json
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("typed_knowledge_checker", ROOT / "scripts/check-typed-knowledge.py")
assert SPEC and SPEC.loader
checker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(checker)


def corpus() -> dict:
    return checker.load_json(checker.FIXTURES)


def test_all_seven_kinds_have_valid_records() -> None:
    value = corpus()
    records = value["valid_records"]
    assert {record["kind"] for record in records} == checker.KINDS
    by_id = {record["id"]: record for record in records}
    for record in records:
        assert checker.validate_record(record, by_id) == []


def test_negative_matrix_has_four_cases_per_kind() -> None:
    cases = corpus()["invalid_cases"]
    expected = {"missing-required-field", "unknown-field", "wrong-kind/status", "reference-integrity"}
    observed = {(case["kind"], case["category"]) for case in cases}
    assert observed == {(kind, category) for kind in checker.KINDS for category in expected}
    assert all(checker.validate_record(case["record"], {record["id"]: record for record in corpus()["valid_records"]}) for case in cases)


def test_normative_source_proof_and_element_integrity_are_required() -> None:
    record = copy.deepcopy(corpus()["valid_records"][0])
    record["body"]["normative_sources"][0]["supports"].remove("requirement:constitution-source")
    findings = checker.validate_record(record, {record["id"]: record})
    assert any("does not prove requirement:constitution-source" in finding for finding in findings)


def test_legislated_authority_requires_contract_proof() -> None:
    record = copy.deepcopy(corpus()["valid_records"][0])
    del record["authority"]["legislated_by"]
    assert checker.validate_record(record, {record["id"]: record})


def test_relevant_typed_reference_cycles_are_rejected() -> None:
    records = copy.deepcopy(corpus()["valid_records"])
    by_id = {record["id"]: record for record in records}
    by_id["constitution-authority"]["references"].append({"id": "ref:constitution-decision", "relation": "depends_on", "target_record_id": "decision-storage-shape", "scope": "contextual"})
    findings = checker.relation_cycle_findings(records)
    assert findings == ["records: relevant typed-reference relation contains a cycle"]


def test_scoped_operator_override_is_explicit_and_bound_to_location() -> None:
    record = copy.deepcopy(corpus()["valid_records"][-1])
    record["scope"] = {"mode": "explicit", "product_ids": ["product-alpha"], "project_ids": [], "domain_ids": [], "work_ids": [], "tags": []}
    record["provenance"]["location_authority"] = "operator_override"
    record["provenance"]["canonical_path"] = ".concord/products/product-alpha/knowledge/work-note-schema-draft.json"
    record["provenance"]["override"] = {"scope": "product", "path": record["provenance"]["canonical_path"], "approved_by": "operator", "reason": "Product-local record has explicit scope."}
    records_by_id = {item["id"]: item for item in corpus()["valid_records"]}
    records_by_id[record["id"]] = record
    assert checker.validate_record(record, records_by_id) == []


def test_evaluation_keeps_proposals_separate_from_results() -> None:
    value = checker.load_json(checker.EVALUATION_FIXTURE)
    assert checker.validate_evaluation_fixture() == []
    assert value["measured_results"] == []
    assert all(target["status"] == "proposed" for target in value["proposed_targets"])


def run_tests() -> None:
    tests = [value for name, value in globals().items() if name.startswith("test_")]
    for test in tests:
        test()
    print(f"typed knowledge tests passed: {len(tests)}")


if __name__ == "__main__":
    run_tests()

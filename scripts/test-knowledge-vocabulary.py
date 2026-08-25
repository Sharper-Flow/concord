#!/usr/bin/env python3
"""Focused tests for the schema-to-checker vocabulary binding."""

from __future__ import annotations

import copy
import importlib.util
import json
from pathlib import Path
from unittest import mock


SCRIPT = Path(__file__).with_name("check-knowledge-vocabulary.py")
SPEC = importlib.util.spec_from_file_location("knowledge_vocabulary", SCRIPT)
assert SPEC and SPEC.loader
binding = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(binding)

CHECKER = binding.load_checker()
DOC_CONTRACT = binding.load_doc_contract_checker()
CLOSURE = binding.load_closure_checker()
GENERATOR = binding.load_generator()
SCHEMA = json.loads(binding.SCHEMA.read_text(encoding="utf-8"))


def validate(schema: dict) -> list[str]:
    return binding.validate(schema, CHECKER, DOC_CONTRACT, CLOSURE)


def validate_generator(schema: dict) -> list[str]:
    findings: list[str] = []
    binding.validate_generator(schema, GENERATOR, findings)
    return findings


def test_repository_schema_and_checker_agree() -> None:
    assert validate(copy.deepcopy(SCHEMA)) == []


def test_repository_schema_and_generator_agree() -> None:
    assert validate_generator(copy.deepcopy(SCHEMA)) == []


def test_generator_record_kind_divergence_is_reported() -> None:
    """The generator validates every shard, so a kind it rejects is unauthorable."""
    schema = copy.deepcopy(SCHEMA)
    schema["$defs"]["record"]["properties"]["kind"]["enum"].append("charter")
    findings = validate_generator(schema)
    assert findings == ["generator KINDS: declared in schema, absent from the checker: charter"]


def test_generator_accepting_an_undeclared_kind_is_reported() -> None:
    schema = copy.deepcopy(SCHEMA)
    with mock.patch.object(GENERATOR, "KINDS", GENERATOR.KINDS | {"charter"}):
        findings = validate_generator(schema)
    assert findings == ["generator KINDS: enforced by the checker, absent from the schema: charter"]


def test_generator_record_path_pattern_divergence_is_reported() -> None:
    schema = copy.deepcopy(SCHEMA)
    schema["$defs"]["record"]["properties"]["path"]["pattern"] = "^docs/(?!work/).*\\.md$"
    findings = validate_generator(schema)
    assert len(findings) == 1
    assert findings[0].startswith("generator RECORD_PATH_RE: schema declares")


def test_generator_root_key_divergence_is_reported() -> None:
    schema = copy.deepcopy(SCHEMA)
    del schema["properties"]["dispositions"]
    findings = validate_generator(schema)
    assert findings == ["generator ALLOWED_ROOT: enforced by the checker, absent from the schema: dispositions"]


def test_schema_record_property_absent_from_checker_is_reported() -> None:
    schema = copy.deepcopy(SCHEMA)
    schema["$defs"]["record"]["properties"]["provenance"] = {"type": "string"}
    findings = validate(schema)
    assert findings == ["ALLOWED_RECORD: declared in schema, absent from the checker: provenance"]


def test_checker_record_property_absent_from_schema_is_reported() -> None:
    schema = copy.deepcopy(SCHEMA)
    with mock.patch.object(CHECKER, "ALLOWED_RECORD", CHECKER.ALLOWED_RECORD | {"provenance"}):
        findings = validate(schema)
    assert findings == ["ALLOWED_RECORD: enforced by the checker, absent from the schema: provenance"]


def test_schema_record_property_removal_is_reported() -> None:
    schema = copy.deepcopy(SCHEMA)
    del schema["$defs"]["record"]["properties"]["evidence"]
    findings = validate(schema)
    assert findings == ["ALLOWED_RECORD: enforced by the checker, absent from the schema: evidence"]


def test_schema_root_property_divergence_is_reported() -> None:
    schema = copy.deepcopy(SCHEMA)
    del schema["properties"]["doc_contract"]
    findings = validate(schema)
    assert findings == ["ALLOWED_ROOT: enforced by the checker, absent from the schema: doc_contract"]


def test_record_kind_divergence_is_reported() -> None:
    schema = copy.deepcopy(SCHEMA)
    schema["$defs"]["record"]["properties"]["kind"]["enum"].append("policy")
    findings = validate(schema)
    assert findings == ["RECORD_KINDS: declared in schema, absent from the checker: policy"]


def test_supported_kind_divergence_is_reported() -> None:
    schema = copy.deepcopy(SCHEMA)
    schema["properties"]["supported_kinds"]["items"]["enum"].remove("research")
    findings = validate(schema)
    assert findings == ["KINDS: enforced by the checker, absent from the schema: research"]


def test_indexed_kind_divergence_is_reported() -> None:
    """A kind supported but not indexable is a kind no record can carry."""
    schema = copy.deepcopy(SCHEMA)
    schema["properties"]["indexed_kinds"]["items"]["enum"].remove("reference")
    findings = validate(schema)
    assert findings == ["KINDS (indexed_kinds): enforced by the checker, absent from the schema: reference"]


def test_law_bearing_tier_divergence_is_reported() -> None:
    schema = copy.deepcopy(SCHEMA)
    for clause in schema["$defs"]["record"]["allOf"]:
        status = clause.get("then", {}).get("properties", {}).get("status", {})
        if "accepted" in status.get("enum", []):
            clause["if"]["properties"]["kind"]["enum"].remove("constitution")
            break
    findings = validate(schema)
    assert findings == [
        "LAW_BEARING_KINDS: enforced by the checker, absent from the schema: constitution"
    ]


def test_missing_status_tier_clause_is_reported_not_silently_passed() -> None:
    schema = copy.deepcopy(SCHEMA)
    schema["$defs"]["record"]["allOf"] = [
        clause
        for clause in schema["$defs"]["record"]["allOf"]
        if "accepted" not in clause.get("then", {}).get("properties", {}).get("status", {}).get("enum", [])
    ]
    findings = validate(schema)
    assert "schema: $defs.record.allOf declares no kinds restricted to status 'accepted'" in findings


def test_disposition_field_divergence_is_reported() -> None:
    schema = copy.deepcopy(SCHEMA)
    schema["$defs"]["disposition"]["properties"]["decided_at"] = {"type": "string"}
    findings = validate(schema)
    assert findings == ["ALLOWED_DISPOSITION: declared in schema, absent from the checker: decided_at"]


def test_disposition_vocabulary_divergence_is_reported() -> None:
    schema = copy.deepcopy(SCHEMA)
    schema["$defs"]["disposition"]["properties"]["disposition"]["enum"] = ["archived", "deferred"]
    findings = validate(schema)
    assert findings == ["DISPOSITIONS: declared in schema, absent from the checker: deferred"]


def test_disposition_path_pattern_divergence_is_reported() -> None:
    schema = copy.deepcopy(SCHEMA)
    schema["$defs"]["disposition"]["properties"]["path"]["pattern"] = "^.*$"
    findings = validate(schema)
    assert len(findings) == 1 and findings[0].startswith("DISPOSITION_PATH_RE: schema declares '^.*$'")


def test_record_path_pattern_divergence_is_reported() -> None:
    schema = copy.deepcopy(SCHEMA)
    schema["$defs"]["record"]["properties"]["path"]["pattern"] = r"^docs/(?!work/).*\.md$"
    findings = validate(schema)
    assert any(finding.startswith("RECORD_PATH_RE: schema declares") for finding in findings), findings


def test_record_path_shape_change_is_reported_for_the_go_binding() -> None:
    """internal/store reads the ineligibility alternation out of this pattern.
    A restructure must fail here rather than leave the Go side matching
    nothing."""
    schema = copy.deepcopy(SCHEMA)
    schema["$defs"]["record"]["properties"]["path"]["pattern"] = r"^docs/.*\.md$"
    findings = validate(schema)
    assert any("is no longer the" in finding for finding in findings), findings


def test_repealed_files_are_absent_from_the_schema_alternation() -> None:
    """The two accepted contracts must not reappear as named exclusions."""
    pattern = SCHEMA["$defs"]["record"]["properties"]["path"]["pattern"]
    assert "product-coordination-view" not in pattern
    assert "terminal-launcher-contract" not in pattern


def test_class_exclusions_remain_in_the_schema_alternation() -> None:
    pattern = SCHEMA["$defs"]["record"]["properties"]["path"]["pattern"]
    for member in ("work/", "research/", "[Gg][Ee][Nn][Ee][Rr][Aa][Tt][Ee][Dd]"):
        assert member in pattern, member


def test_exclusion_pattern_divergence_is_reported() -> None:
    schema = copy.deepcopy(SCHEMA)
    schema["$defs"]["knowledgeExclusions"]["items"]["pattern"] = "^.*/$"
    findings = validate(schema)
    assert len(findings) == 1 and findings[0].startswith("EXCLUSION_RE: schema declares '^.*/$'")


def test_doc_contract_field_divergence_is_reported() -> None:
    schema = copy.deepcopy(SCHEMA)
    schema["$defs"]["docContract"]["properties"]["work_note"] = {"$ref": "#/$defs/docContractKind"}
    findings = validate(schema)
    assert findings == ["DOC_CONTRACT_FIELDS: declared in schema, absent from the checker: work_note"]


def test_law_kind_divergence_is_reported() -> None:
    schema = copy.deepcopy(SCHEMA)
    schema["$defs"]["lawRelation"]["properties"]["kind"]["enum"] = ["supersedes"]
    findings = validate(schema)
    assert findings == [
        "LAW_KINDS: enforced by the checker, absent from the schema: "
        "conflicts_with, refines, subordinate_to"
    ]


def test_scope_union_divergence_is_reported() -> None:
    schema = copy.deepcopy(SCHEMA)
    del schema["$defs"]["scopeCommon"]["properties"]["tag_ids"]
    findings = validate(schema)
    assert findings == [
        "ALLOWED_SCOPES_V12: enforced by the checker, absent from the schema: tag_ids"
    ]


def test_missing_schema_node_is_reported_not_silently_passed() -> None:
    schema = copy.deepcopy(SCHEMA)
    del schema["$defs"]["record"]
    findings = validate(schema)
    assert "schema: missing $defs.record" in findings
    assert "ALLOWED_RECORD: enforced by the checker, absent from the schema: evidence" not in findings


def test_empty_property_set_is_reported() -> None:
    schema = copy.deepcopy(SCHEMA)
    schema["$defs"]["domain"]["properties"] = {}
    findings = validate(schema)
    assert findings == ["schema: $defs.domain.properties is not a non-empty object"]


def test_non_string_enum_is_reported() -> None:
    schema = copy.deepcopy(SCHEMA)
    schema["$defs"]["lawRelation"]["properties"]["kind"]["enum"] = [1, 2]
    findings = validate(schema)
    assert findings == ["schema: $defs.lawRelation.properties.kind.enum is not a non-empty string array"]


if __name__ == "__main__":
    for name, function in sorted(globals().items()):
        if name.startswith("test_"):
            function()
    print("knowledge vocabulary tests passed")

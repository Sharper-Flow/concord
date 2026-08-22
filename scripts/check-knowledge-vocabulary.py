#!/usr/bin/env python3
"""Bind the knowledge-index checker's vocabulary to its JSON Schema.

The manifest vocabulary is declared once, in
contracts/concord-knowledge-index.v1.schema.json. check-knowledge-index.py
enforces it with plain Python sets because CI carries no JSON Schema
validator. This check keeps the two identical, in both directions: a member
the schema declares and the checker lacks is as much a defect as a member the
checker accepts and the schema forbids.
"""

from __future__ import annotations

import importlib.util
import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SCHEMA = ROOT / "contracts/concord-knowledge-index.v1.schema.json"
CHECKER = ROOT / "scripts/check-knowledge-index.py"
DOC_CONTRACT_CHECKER = ROOT / "scripts/check-doc-contract.py"
CLOSURE_CHECKER = ROOT / "scripts/check-knowledge-closure.py"
GENERATOR = ROOT / "scripts/generate-knowledge-index.py"


def load_module(name: str, path: Path):
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load {path}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def load_checker():
    return load_module("knowledge_checker", CHECKER)


def load_doc_contract_checker():
    return load_module("doc_contract_checker", DOC_CONTRACT_CHECKER)


def load_closure_checker():
    return load_module("closure_checker", CLOSURE_CHECKER)


def load_generator():
    return load_module("knowledge_generator", GENERATOR)


def walk(node: object, path: list[str], findings: list[str]) -> object | None:
    """Resolve a chain of object keys, reporting a missing link as a finding."""
    current = node
    for index, key in enumerate(path):
        if not isinstance(current, dict) or key not in current:
            findings.append(f"schema: missing {'.'.join(path[: index + 1])}")
            return None
        current = current[key]
    return current


def schema_properties(schema: object, path: list[str], findings: list[str]) -> set[str] | None:
    node = walk(schema, [*path, "properties"], findings)
    if node is None:
        return None
    if not isinstance(node, dict) or not node:
        findings.append(f"schema: {'.'.join(path)}.properties is not a non-empty object")
        return None
    return set(node)


def schema_enum(schema: object, path: list[str], findings: list[str]) -> set[str] | None:
    node = walk(schema, [*path, "enum"], findings)
    if node is None:
        return None
    if not isinstance(node, list) or not node or not all(isinstance(item, str) for item in node):
        findings.append(f"schema: {'.'.join(path)}.enum is not a non-empty string array")
        return None
    return set(node)


def compare(subject: str, declared: set[str] | None, enforced: set[str], findings: list[str]) -> None:
    if declared is None:
        return
    missing = sorted(declared - enforced)
    extra = sorted(enforced - declared)
    if missing:
        findings.append(f"{subject}: declared in schema, absent from the checker: {', '.join(missing)}")
    if extra:
        findings.append(f"{subject}: enforced by the checker, absent from the schema: {', '.join(extra)}")


def schema_string(schema: object, path: list[str], findings: list[str]) -> str | None:
    node = walk(schema, path, findings)
    if node is None:
        return None
    if not isinstance(node, str) or not node:
        findings.append(f"schema: {'.'.join(path)} is not a non-empty string")
        return None
    return node


def compare_pattern(subject: str, declared: str | None, enforced: str, findings: list[str]) -> None:
    """Bind a checker regex to the schema pattern it restates.

    Neither validator can run a JSON Schema, so each one re-implements the
    schema's patterns as a Python regex. That restatement is only safe while
    the two texts are identical.
    """
    if declared is None:
        return
    if declared != enforced:
        findings.append(f"{subject}: schema declares {declared!r}, the checker enforces {enforced!r}")


# The Go model cannot compile $defs.record.path: RE2 has no lookahead. It
# decomposes the alternation into prefix and substring rules instead, and
# internal/store/knowledge_vocabulary_test.go binds that decomposition by
# reading the alternation back out of this same pattern. That reader only works
# while the pattern keeps this shape, so a restructure must fail here rather
# than silently leave the Go side matching nothing.
RECORD_PATH_SHAPE = re.compile(r"^\^docs/\(\?!(?P<alternation>.+)\)\.\*\\\.md\$$")


def check_record_path_decomposable(schema: object, findings: list[str]) -> None:
    pattern = schema_string(schema, ["$defs", "record", "properties", "path", "pattern"], findings)
    if pattern is None:
        return
    shape = RECORD_PATH_SHAPE.fullmatch(pattern)
    if shape is None:
        findings.append(
            "record path ineligibility: schema pattern is no longer the "
            "'^docs/(?!<alternation>).*\\.md$' shape the Go binding decomposes: "
            f"{pattern!r}"
        )
        return
    members = shape.group("alternation").split("|")
    if not all(members):
        findings.append(f"record path ineligibility: schema pattern has an empty alternation member: {pattern!r}")


def status_tier_kinds(schema: object, status: str, findings: list[str]) -> set[str] | None:
    """Read the record kinds a conditional status clause restricts to `status`.

    The two status tiers are declared in the schema as kind-to-status
    implications. Reading the tier back out of those clauses keeps the
    checker's LAW_BEARING_KINDS from becoming a second declaration.
    """
    clauses = walk(schema, ["$defs", "record", "allOf"], findings)
    if not isinstance(clauses, list):
        findings.append("schema: $defs.record.allOf is not an array")
        return None
    for clause in clauses:
        if not isinstance(clause, dict):
            continue
        kinds = ((clause.get("if") or {}).get("properties") or {}).get("kind") or {}
        statuses = ((clause.get("then") or {}).get("properties") or {}).get("status") or {}
        kind_enum = kinds.get("enum") if isinstance(kinds, dict) else None
        status_enum = statuses.get("enum") if isinstance(statuses, dict) else None
        if not isinstance(kind_enum, list) or not isinstance(status_enum, list):
            continue
        if status in status_enum:
            return set(kind_enum)
    findings.append(f"schema: $defs.record.allOf declares no kinds restricted to status {status!r}")
    return None


def validate_generator(schema: object, generator: object, findings: list[str]) -> None:
    """Bind the aggregate generator's vocabulary to the same schema.

    scripts/generate-knowledge-index.py validates every shard before it derives
    docs/concord-knowledge-index.v1.json, so it is a second enforcement point
    for the same contract, not a formatter. A kind the schema declares and the
    generator rejects makes that kind unauthorable no matter what the checker
    allows, and a kind the generator accepts and the schema forbids reaches the
    aggregate unchallenged.
    """
    compare("generator ALLOWED_ROOT", schema_properties(schema, [], findings), generator.ALLOWED_ROOT, findings)
    compare("generator ALLOWED_RECORD", schema_properties(schema, ["$defs", "record"], findings), generator.ALLOWED_RECORD, findings)
    compare("generator SUPPORTED_KINDS", schema_enum(schema, ["properties", "supported_kinds", "items"], findings), generator.SUPPORTED_KINDS, findings)
    compare("generator KINDS", schema_enum(schema, ["$defs", "record", "properties", "kind"], findings), generator.KINDS, findings)
    compare("generator LAW_KINDS", schema_enum(schema, ["$defs", "lawRelation", "properties", "kind"], findings), generator.LAW_RELATION_KINDS, findings)
    compare("generator LAW_BEARING_KINDS", status_tier_kinds(schema, "accepted", findings), generator.LAW_BEARING_KINDS, findings)
    compare_pattern(
        "generator RECORD_PATH_RE",
        schema_string(schema, ["$defs", "record", "properties", "path", "pattern"], findings),
        generator.RECORD_PATH_RE.pattern,
        findings,
    )


def validate(schema: object, checker: object, doc_contract: object = None, closure: object = None, generator: object = None) -> list[str]:
    findings: list[str] = []
    compare("ALLOWED_ROOT", schema_properties(schema, [], findings), checker.ALLOWED_ROOT, findings)
    compare("ALLOWED_RECORD", schema_properties(schema, ["$defs", "record"], findings), checker.ALLOWED_RECORD, findings)
    compare("ALLOWED_DISPOSITION", schema_properties(schema, ["$defs", "disposition"], findings), checker.ALLOWED_DISPOSITION, findings)
    compare("DISPOSITIONS", schema_enum(schema, ["$defs", "disposition", "properties", "disposition"], findings), checker.DISPOSITIONS, findings)
    compare_pattern(
        "DISPOSITION_PATH_RE",
        schema_string(schema, ["$defs", "disposition", "properties", "path", "pattern"], findings),
        checker.DISPOSITION_PATH_RE.pattern,
        findings,
    )
    compare_pattern(
        "RECORD_PATH_RE",
        schema_string(schema, ["$defs", "record", "properties", "path", "pattern"], findings),
        checker.RECORD_PATH_RE.pattern,
        findings,
    )
    check_record_path_decomposable(schema, findings)
    compare("LAW_BEARING_KINDS", status_tier_kinds(schema, "accepted", findings), checker.LAW_BEARING_KINDS, findings)
    compare("ALLOWED_DOMAIN_REGISTRY", schema_properties(schema, ["$defs", "domainRegistry"], findings), checker.ALLOWED_DOMAIN_REGISTRY, findings)
    compare("ALLOWED_DOMAIN", schema_properties(schema, ["$defs", "domain"], findings), checker.ALLOWED_DOMAIN, findings)
    compare("ALLOWED_ARCHITECTURE_RELATION", schema_properties(schema, ["$defs", "architectureRelation"], findings), checker.ALLOWED_ARCHITECTURE_RELATION, findings)
    compare("KINDS", schema_enum(schema, ["properties", "supported_kinds", "items"], findings), checker.KINDS, findings)
    # supported_kinds and indexed_kinds draw from one vocabulary. Binding both
    # to the same checker set is what stops a kind from becoming declarable but
    # not indexable, which no record could then carry.
    compare("KINDS (indexed_kinds)", schema_enum(schema, ["properties", "indexed_kinds", "items"], findings), checker.KINDS, findings)
    compare("RECORD_KINDS", schema_enum(schema, ["$defs", "record", "properties", "kind"], findings), checker.RECORD_KINDS, findings)
    compare("LAW_KINDS", schema_enum(schema, ["$defs", "lawRelation", "properties", "kind"], findings), checker.LAW_KINDS, findings)
    # The scope variants split one schema object across two version-specific
    # refinements, so their union — not either alone — is what scopeCommon
    # declares.
    compare(
        "ALLOWED_SCOPES_V10 | ALLOWED_SCOPES_V12",
        schema_properties(schema, ["$defs", "scopeCommon"], findings),
        checker.ALLOWED_SCOPES_V10 | checker.ALLOWED_SCOPES_V12,
        findings,
    )
    if doc_contract is not None:
        compare(
            "DOC_CONTRACT_FIELDS",
            schema_properties(schema, ["$defs", "docContract"], findings),
            doc_contract.DOC_CONTRACT_FIELDS,
            findings,
        )
    if closure is not None:
        compare_pattern(
            "ROOT_RE",
            schema_string(schema, ["$defs", "knowledgeRoots", "items", "pattern"], findings),
            closure.ROOT_RE.pattern,
            findings,
        )
        compare_pattern(
            "EXCLUSION_RE",
            schema_string(schema, ["$defs", "knowledgeExclusions", "items", "pattern"], findings),
            closure.EXCLUSION_RE.pattern,
            findings,
        )
    if generator is not None:
        validate_generator(schema, generator, findings)
    return findings


def main() -> int:
    try:
        schema = json.loads(SCHEMA.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
        print(f"{SCHEMA.relative_to(ROOT)}: invalid JSON: {exc}")
        print("knowledge vocabulary validation failed: 1 finding(s)", file=sys.stderr)
        return 1
    findings = validate(schema, load_checker(), load_doc_contract_checker(), load_closure_checker(), load_generator())
    for finding in findings:
        print(finding)
    if findings:
        print(f"knowledge vocabulary validation failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    print("knowledge vocabulary validation passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

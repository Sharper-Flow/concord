#!/usr/bin/env python3
"""Validate and mechanically hash Concord's authored durable-knowledge registry."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import sys
import tempfile
from datetime import datetime
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import shard_format  # noqa: E402

ROOT = Path(__file__).resolve().parents[1]
MANIFEST = ROOT / "docs/concord-knowledge-index.v1.json"
MAX_MANIFEST_PATH = 512  # JSON Schema maxLength and Python Unicode scalar count.
ALLOWED_ROOT = {"schema_version", "supported_kinds", "indexed_kinds", "domain_registry", "knowledge_roots", "exclusions", "dispositions", "doc_contract", "records"}
ALLOWED_DISPOSITION = {"path", "disposition", "reason"}
ALLOWED_RECORD = {"id", "kind", "path", "status", "date", "title", "summary", "tags", "scopes", "successor", "sha256", "law_relations", "evidence", "home_domain_id", "applies_to_domain_ids", "product_wide_rationale"}
ALLOWED_SCOPES_V10 = {"mode", "product_ids", "project_ids", "component_ids", "tag_ids"}
ALLOWED_SCOPES_V12 = {"mode", "product_ids", "project_ids", "domain_ids", "tag_ids"}
ALLOWED_DOMAIN_REGISTRY = {"schema_version", "product_key", "root_domain_id", "domains"}
ALLOWED_DOMAIN = {"domain_id", "name", "purpose", "parent_domain_id", "status", "architecture_relations"}
ALLOWED_ARCHITECTURE_RELATION = {"kind", "target_domain_id", "governing_law_ids", "state"}
KINDS = {"work_note", "constitution", "decision", "spec", "lesson", "reference", "research"}
RECORD_KINDS = {"constitution", "decision", "spec", "lesson", "reference", "research"}
# The record kinds split into two status tiers. A law-bearing record is accepted
# or superseded; every other kind is published or superseded. `reference` covers
# navigation and reference material, so there is no separate navigation kind.
LAW_BEARING_KINDS = {"constitution", "decision", "spec"}
# The law-relation graph, the domain registry's governing_law_ids, and the
# supersedes symmetry rule are defined over decisions and specs alone. A
# constitution is law-bearing for status purposes without joining that graph.
LAW_RELATION_SUBJECTS = {"decision", "spec"}
LAW_KINDS = {"supersedes", "refines", "subordinate_to", "conflicts_with"}
DISPOSITIONS = {"archived"}
MAX_DISPOSITIONS = 1000
MAX_DISPOSITION_REASON = 4096
DISPOSITION_PATH_RE = re.compile(r"^(?![\s\S]*\.\.)[a-zA-Z0-9._-]+(?:/[a-zA-Z0-9._-]+)*\.md$")
# Which authored docs paths may carry a record is declared once, as the
# $defs.record.path pattern in contracts/concord-knowledge-index.v1.schema.json.
# Python re carries the lookahead that JSON Schema uses, so this restates the
# pattern verbatim rather than decomposing it; check-knowledge-vocabulary.py
# binds the two texts. `generated` is spelled as per-letter character classes
# because the Go model matches it ASCII case-insensitively.
RECORD_PATH_RE = re.compile(r"^docs/(?!work/|research/|.*[Gg][Ee][Nn][Ee][Rr][Aa][Tt][Ee][Dd]).*\.md$")


class DuplicateKeyError(ValueError):
    pass


def reject_duplicate_pairs(pairs: list[tuple[str, object]]) -> dict[str, object]:
    result: dict[str, object] = {}
    for key, value in pairs:
        if key in result:
            raise DuplicateKeyError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def fail(findings: list[str], message: str) -> None:
    findings.append(message)


def load(path: Path, findings: list[str]) -> object:
    try:
        return json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=reject_duplicate_pairs)
    except (OSError, UnicodeDecodeError, json.JSONDecodeError, DuplicateKeyError) as exc:
        fail(findings, f"{path.relative_to(ROOT)}: invalid JSON: {exc}")
        return None


def unique_string_list(value: object, maximum: int) -> bool:
    if not isinstance(value, list) or len(value) > maximum or not all(isinstance(item, str) for item in value):
        return False
    return len(value) == len(set(value))


def valid_id(value: object) -> bool:
    return isinstance(value, str) and 0 < len(value) <= 256 and value == value.strip()


def bounded_text(value: object, maximum: int) -> bool:
    """A stated string: non-empty, untrimmed at both ends, and bounded."""
    return isinstance(value, str) and 0 < len(value) <= maximum and value == value.strip()


def validate_domain_registry(registry: object, findings: list[str]) -> set[str]:
    if not isinstance(registry, dict) or set(registry) != ALLOWED_DOMAIN_REGISTRY:
        fail(findings, "manifest.domain_registry: invalid closed root")
        return set()
    if registry.get("schema_version") != "1.0":
        fail(findings, "manifest.domain_registry: schema_version must be 1.0")
    product_key = registry.get("product_key")
    if not isinstance(product_key, str) or not re.fullmatch(r"[a-z][a-z0-9-]{1,63}", product_key):
        fail(findings, "manifest.domain_registry: product_key is not a clean slug")
    if registry.get("root_domain_id") != f"product-root:{product_key}":
        fail(findings, "manifest.domain_registry: root_domain_id does not match product_key")
    domains = registry.get("domains")
    if not isinstance(domains, list) or len(domains) > 64:
        fail(findings, "manifest.domain_registry: domains must be a bounded non-null array")
        return set()
    by_id: dict[str, dict[str, object]] = {}
    parent_graph: dict[str, list[str]] = {}
    depends_graph: dict[str, list[str]] = {}
    replaces_graph: dict[str, list[str]] = {}
    relation_keys: set[tuple[str, str, str]] = set()
    for number, domain in enumerate(domains):
        prefix = f"manifest.domain_registry.domains[{number}]"
        if not isinstance(domain, dict) or set(domain) - ALLOWED_DOMAIN:
            fail(findings, f"{prefix}: unknown fields or non-object domain")
            continue
        missing = {"domain_id", "name", "purpose", "status", "architecture_relations"} - set(domain)
        if missing:
            fail(findings, f"{prefix}: missing fields: {sorted(missing)}")
            continue
        if not valid_id(domain.get("domain_id")) or not isinstance(domain.get("name"), str) or not 0 < len(domain["name"]) <= 256 or domain["name"] != domain["name"].strip() or not isinstance(domain.get("purpose"), str) or not 0 < len(domain["purpose"]) <= 4096 or domain["purpose"] != domain["purpose"].strip() or domain.get("status") not in {"current", "deprecated"}:
            fail(findings, f"{prefix}: invalid domain metadata")
        if domain.get("domain_id") in by_id:
            fail(findings, f"{prefix}: duplicate domain_id")
        else:
            by_id[domain.get("domain_id")] = domain
        if "parent_domain_id" in domain:
            parent = domain.get("parent_domain_id")
            if not valid_id(parent) or parent == domain.get("domain_id"):
                fail(findings, f"{prefix}: invalid or self-referential parent_domain_id")
            parent_graph.setdefault(domain.get("domain_id"), []).append(parent)
        relations = domain.get("architecture_relations")
        if not isinstance(relations, list) or len(relations) > 64:
            fail(findings, f"{prefix}: architecture_relations must be a bounded non-null array")
            continue
        for relation_number, relation in enumerate(relations):
            relation_prefix = f"{prefix}.architecture_relations[{relation_number}]"
            if not isinstance(relation, dict) or set(relation) - ALLOWED_ARCHITECTURE_RELATION or not {"kind", "target_domain_id"}.issubset(relation):
                fail(findings, f"{relation_prefix}: invalid closed relation")
                continue
            kind = relation["kind"]
            target = relation["target_domain_id"]
            if kind not in {"depends_on", "shares_contract_with", "replaces"} or not valid_id(target) or target == domain.get("domain_id"):
                fail(findings, f"{relation_prefix}: invalid kind, target, or self-edge")
                continue
            if kind in {"depends_on", "shares_contract_with"}:
                laws = relation.get("governing_law_ids")
                if not unique_string_list(laws, 64) or not laws:
                    fail(findings, f"{relation_prefix}: relation requires non-empty unique governing_law_ids")
                if "state" in relation:
                    fail(findings, f"{relation_prefix}: state is only valid for replaces")
            else:
                if "governing_law_ids" in relation:
                    fail(findings, f"{relation_prefix}: replaces forbids governing_law_ids")
                if relation.get("state") not in {"declared", "building", "coexisting", "cutover", "retired"}:
                    fail(findings, f"{relation_prefix}: replaces requires a closed state")
            if kind == "shares_contract_with" and target < domain.get("domain_id"):
                fail(findings, f"{relation_prefix}: shares_contract_with pair is not canonical")
            key = (kind, domain.get("domain_id"), target)
            if key in relation_keys:
                fail(findings, f"{relation_prefix}: duplicate architecture relation")
            relation_keys.add(key)
            if kind == "depends_on":
                depends_graph.setdefault(domain.get("domain_id"), []).append(target)
            elif kind == "replaces":
                replaces_graph.setdefault(domain.get("domain_id"), []).append(target)
    root = registry.get("root_domain_id")
    if root not in by_id:
        fail(findings, "manifest.domain_registry: root domain is not declared")
    else:
        root_domain = by_id[root]
        if root_domain.get("status") != "current" or "parent_domain_id" in root_domain:
            fail(findings, "manifest.domain_registry: root domain must be current and parentless")
    for domain_id, domain in by_id.items():
        if "parent_domain_id" in domain and domain["parent_domain_id"] not in by_id:
            fail(findings, f"manifest.domain_registry.domains[{domain_id}]: dangling parent_domain_id")
        for relation in domain.get("architecture_relations", []):
            if not isinstance(relation, dict):
                continue
            target_id = relation.get("target_domain_id")
            if target_id not in by_id:
                fail(findings, f"manifest.domain_registry.domains[{domain_id}]: dangling relation target")
            elif relation.get("kind") == "depends_on" and by_id[target_id].get("status") != "current":
                fail(findings, f"manifest.domain_registry.domains[{domain_id}]: depends_on target must be current")
    if has_cycle(parent_graph) or has_cycle(depends_graph) or has_cycle(replaces_graph):
        fail(findings, "manifest.domain_registry: hierarchy, dependency, or replacement relation contains a cycle")
    return set(by_id)


def has_cycle(graph: dict[str, list[str]]) -> bool:
    visiting: set[str] = set()
    visited: set[str] = set()

    def visit(node: str) -> bool:
        if node in visiting:
            return True
        if node in visited:
            return False
        visiting.add(node)
        if any(visit(child) for child in graph.get(node, [])):
            return True
        visiting.remove(node)
        visited.add(node)
        return False

    return any(visit(node) for node in graph)


def validate_dispositions(dispositions: object, record_paths: set[str], findings: list[str]) -> None:
    """Enforce the record/disposition exclusion over the manifest.

    A disposition is the opposite of a record: a record makes a document
    knowledge, a disposition states that the document will never become
    knowledge and why. A path claimed by both has no answer to give, so the
    manifest is rejected rather than resolved by precedence.
    """
    if dispositions is None:
        return
    if not isinstance(dispositions, list) or len(dispositions) > MAX_DISPOSITIONS:
        fail(findings, "manifest: dispositions must be a bounded array")
        return
    seen: set[str] = set()
    for number, entry in enumerate(dispositions):
        prefix = f"manifest.dispositions[{number}]"
        if not isinstance(entry, dict) or set(entry) != ALLOWED_DISPOSITION:
            fail(findings, f"{prefix}: disposition must carry exactly path, disposition, and reason")
            continue
        path = entry["path"]
        if (
            not isinstance(path, str)
            or len(path) > MAX_MANIFEST_PATH
            or not DISPOSITION_PATH_RE.fullmatch(path)
        ):
            fail(findings, f"{prefix}: forbidden or unsafe path: {path}")
            continue
        if entry["disposition"] not in DISPOSITIONS:
            fail(findings, f"{prefix}: disposition is not closed: {entry['disposition']}")
        reason = entry["reason"]
        if not isinstance(reason, str) or not 0 < len(reason) <= MAX_DISPOSITION_REASON or reason != reason.strip():
            fail(findings, f"{prefix}: reason must be a bounded non-empty statement")
        if path in seen:
            fail(findings, f"{prefix}: duplicate disposition path {path}")
        seen.add(path)
        if path in record_paths:
            fail(findings, f"{prefix}: path is both a record and a disposition: {path}")


def validate(data: object, *, check_hashes: bool = True) -> list[str]:
    findings: list[str] = []
    if not isinstance(data, dict):
        return ["manifest: top-level value must be an object"]
    unknown = set(data) - ALLOWED_ROOT
    if unknown:
        fail(findings, f"manifest: unknown fields: {sorted(unknown)}")
    schema_version = data.get("schema_version")
    if schema_version not in {"1.0", "1.1", "1.2"}:
        fail(findings, "manifest: schema_version must be 1.0, 1.1, or 1.2")
    supported = data.get("supported_kinds")
    indexed = data.get("indexed_kinds")
    records = data.get("records")
    if not unique_string_list(supported, len(KINDS)) or not all(kind in KINDS for kind in supported):
        fail(findings, "manifest: supported_kinds is not a unique closed bounded array")
        supported = []
    if not unique_string_list(indexed, len(KINDS)) or not all(kind in KINDS for kind in indexed):
        fail(findings, "manifest: indexed_kinds is not a unique closed bounded array")
        indexed = []
    if not set(indexed).issubset(supported):
        fail(findings, "manifest: indexed_kinds contains an unsupported kind")
    if not isinstance(records, list) or len(records) > 1000:
        fail(findings, "manifest: records must be a bounded array")
        records = []

    registry = data.get("domain_registry")
    if schema_version == "1.2":
        if not isinstance(registry, dict):
            fail(findings, "manifest: schema 1.2 requires a domain_registry object")
            registry = {}
        domain_ids = validate_domain_registry(registry, findings)
    else:
        if "domain_registry" in data:
            fail(findings, "manifest: domain_registry requires schema_version 1.2")
        domain_ids = set()

    ids: set[str] = set()
    paths: set[str] = set()
    for number, record in enumerate(records):
        prefix = f"manifest.records[{number}]"
        if not isinstance(record, dict):
            fail(findings, f"{prefix}: record must be an object")
            continue
        unknown = set(record) - ALLOWED_RECORD
        if unknown:
            fail(findings, f"{prefix}: unknown fields: {sorted(unknown)}")
        required = ALLOWED_RECORD - {"successor", "law_relations", "evidence", "home_domain_id", "applies_to_domain_ids", "product_wide_rationale"}
        missing = required - set(record)
        if missing:
            fail(findings, f"{prefix}: missing fields: {sorted(missing)}")
            continue

        if "law_relations" in record and schema_version not in {"1.1", "1.2"}:
            fail(findings, f"{prefix}: law_relations require schema_version 1.1 or 1.2")
        relations = record.get("law_relations", [])
        if not isinstance(relations, list) or len(relations) > 32:
            fail(findings, f"{prefix}: law_relations must be a bounded array")
        elif not isinstance(record.get("kind"), str) or record.get("kind") not in LAW_RELATION_SUBJECTS:
            if relations:
                fail(findings, f"{prefix}: law_relations are only allowed on decision/spec records")
        else:
            for relation in relations:
                if not isinstance(relation, dict) or set(relation) != {"kind", "target_id"} or relation.get("kind") not in LAW_KINDS or not valid_id(relation.get("target_id")):
                    fail(findings, f"{prefix}: invalid law relation")

        identifier = record["id"]
        if not valid_id(identifier):
            fail(findings, f"{prefix}: invalid ID")
        elif identifier in ids:
            fail(findings, f"{prefix}: duplicate ID {identifier}")
        else:
            ids.add(identifier)

        kind = record["kind"]
        if not isinstance(kind, str) or kind not in RECORD_KINDS or kind not in indexed:
            fail(findings, f"{prefix}: record kind is not indexed: {kind}")
        path = record["path"]
        if (
            not isinstance(path, str)
            or len(path) > MAX_MANIFEST_PATH
            or (isinstance(path, str) and "\x00" in path)
            or path in {"docs/concord-knowledge-index.v1.json"}
            or not RECORD_PATH_RE.fullmatch(path)
        ):
            fail(findings, f"{prefix}: forbidden or unsafe path: {path}")
            continue
        if Path(path).as_posix() != path or ".." in Path(path).parts:
            fail(findings, f"{prefix}: forbidden or unsafe path: {path}")
            continue
        if path in paths:
            fail(findings, f"{prefix}: duplicate path {path}")
        else:
            paths.add(path)
        if kind == "decision" and (not isinstance(path, str) or not re.fullmatch(r"docs/decisions/CD-[0-9]{4}(?:-.*)?\.md", path)):
            fail(findings, f"{prefix}: decision is outside the canonical CD decision path")

        status = record["status"]
        law_bearing = kind in LAW_BEARING_KINDS
        if (
            not isinstance(status, str)
            or status not in {"accepted", "published", "superseded"}
            or (status == "published" and law_bearing)
            or (status == "accepted" and not law_bearing)
        ):
            fail(findings, f"{prefix}: invalid status/kind combination")
        successor = record.get("successor")
        if status == "superseded" and not valid_id(successor):
            fail(findings, f"{prefix}: superseded record requires a clean successor")
        if status != "superseded" and "successor" in record:
            fail(findings, f"{prefix}: successor is only valid for superseded records")

        try:
            datetime.fromisoformat(record["date"].replace("Z", "+00:00"))
        except (AttributeError, ValueError):
            fail(findings, f"{prefix}: date is not RFC3339")
        for field, maximum in (("title", 256), ("summary", 4096)):
            if not isinstance(record[field], str) or not 0 < len(record[field]) <= maximum or record[field] != record[field].strip():
                fail(findings, f"{prefix}: invalid bounded {field}")
        if not unique_string_list(record["tags"], 64) or not all(valid_id(item) for item in record["tags"]):
            fail(findings, f"{prefix}: invalid tags")

        scopes = record["scopes"]
        allowed_scopes = ALLOWED_SCOPES_V12 if schema_version == "1.2" else ALLOWED_SCOPES_V10
        if not isinstance(scopes, dict) or set(scopes) != allowed_scopes or scopes.get("mode") not in {"home", "explicit"}:
            fail(findings, f"{prefix}: invalid closed scopes")
        else:
            for field in sorted(allowed_scopes - {"mode"}):
                values = scopes[field]
                if not unique_string_list(values, 64) or not all(valid_id(item) for item in values):
                    fail(findings, f"{prefix}: invalid {field}")
            if scopes["mode"] == "home" and any(scopes[field] for field in allowed_scopes - {"mode"}):
                fail(findings, f"{prefix}: home scopes cannot contain explicit IDs")
            if schema_version == "1.2" and any(domain_id not in domain_ids for domain_id in scopes["domain_ids"]):
                fail(findings, f"{prefix}: scope domain is dangling")

        if schema_version != "1.2" and ("home_domain_id" in record or "applies_to_domain_ids" in record or "product_wide_rationale" in record):
            fail(findings, f"{prefix}: law-home fields require schema_version 1.2")
        elif schema_version == "1.2":
            if not law_bearing and ("home_domain_id" in record or "applies_to_domain_ids" in record or "product_wide_rationale" in record):
                fail(findings, f"{prefix}: non-law records cannot author law-home fields")
            # The root is the only home reachable by deciding nothing, so it
            # carries a stated claim. A child home has already decided and a
            # rationale there would assert a reach the home contradicts.
            root_homed = record.get("home_domain_id") == registry.get("root_domain_id")
            rationale = record.get("product_wide_rationale")
            if root_homed and not bounded_text(rationale, 512):
                fail(findings, f"{prefix}: law homed to the root Domain must state product_wide_rationale")
            if not root_homed and "product_wide_rationale" in record:
                fail(findings, f"{prefix}: only root-homed law states product_wide_rationale")
            if law_bearing and status == "accepted" and ("home_domain_id" not in record or not valid_id(record.get("home_domain_id"))):
                fail(findings, f"{prefix}: an accepted law-bearing record requires one clean home_domain_id")
            if "home_domain_id" in record and (not valid_id(record["home_domain_id"]) or record["home_domain_id"] not in domain_ids):
                fail(findings, f"{prefix}: home domain is dangling or invalid")
            if "applies_to_domain_ids" in record:
                if "home_domain_id" not in record:
                    fail(findings, f"{prefix}: applies_to_domain_ids requires home_domain_id")
                values = record["applies_to_domain_ids"]
                if not unique_string_list(values, 64) or not all(valid_id(item) and item in domain_ids for item in values):
                    fail(findings, f"{prefix}: invalid or dangling applies_to_domain_ids")
                if "home_domain_id" in record and record["home_domain_id"] in values:
                    fail(findings, f"{prefix}: applies_to_domain_ids repeats home_domain_id")

        if not isinstance(record["sha256"], str) or not re.fullmatch(r"sha256:[0-9a-f]{64}", record["sha256"]):
            fail(findings, f"{prefix}: invalid sha256 proof")

        # CD-0026 drift audit: evidence paths must stay reachable. A decision
        # or lesson whose named implementation evidence rots is surfaced here,
        # before the recorded law and the current implementation drift apart
        # silently.
        evidence = record.get("evidence", [])
        if not isinstance(evidence, list) or len(evidence) > 32 or any(
            not isinstance(item, str) or not (1 <= len(item) <= 512) or item.startswith("/") or ".." in item
            for item in evidence
        ):
            fail(findings, f"{prefix}: invalid evidence paths")
        else:
            for item in evidence:
                target = ROOT / item
                if target.is_symlink() or not target.is_file():
                    fail(findings, f"{prefix}: dangling evidence path: {item}")
        target = ROOT / path if isinstance(path, str) else ROOT / "missing"
        if isinstance(path, str) and (target.is_symlink() or not target.is_file()):
            fail(findings, f"{prefix}: dangling path: {path}")
        elif check_hashes and target.is_file() and isinstance(record.get("sha256"), str) and re.fullmatch(r"sha256:[0-9a-f]{64}", record["sha256"]):
            actual = "sha256:" + hashlib.sha256(target.read_bytes()).hexdigest()
            if actual != record["sha256"]:
                fail(findings, f"{prefix}: hash drift for {path}")

    validate_dispositions(data.get("dispositions"), paths, findings)

    by_id = {record.get("id"): record for record in records if isinstance(record, dict) and isinstance(record.get("id"), str)}

    # A record's sha256 binds it to the bytes of its target document, but title
    # and summary are free prose that no hash covers. A record authored by
    # copying a sibling therefore resolves to the right document while
    # describing a different decision, and concord_knowledge answers with the
    # wrong summary for a correctly-hashed law.
    #
    # Distinctness is the enforceable invariant. Matching a title against the
    # target document's H1 is not: 18 of 69 records deliberately carry a short
    # label rather than the document heading ("Concord state authority" for
    # "SQLite as Concord's sole durable authority"), so heading equality would
    # reject the convention rather than the defect.
    for field in ("title", "summary"):
        seen: dict[str, str] = {}
        for record in records:
            if not isinstance(record, dict):
                continue
            value, identifier = record.get(field), record.get("id")
            if not isinstance(value, str) or not value.strip() or not isinstance(identifier, str):
                continue
            owner = seen.setdefault(value.strip(), identifier)
            if owner != identifier:
                fail(findings, f"manifest.records[{identifier}]: {field} duplicates {owner}; each record must describe its own law")
    if schema_version == "1.2" and isinstance(registry, dict):
        accepted_laws = {
            record.get("id")
            for record in records
            if isinstance(record, dict) and record.get("kind") in LAW_RELATION_SUBJECTS and record.get("status") == "accepted"
        }
        for domain in registry.get("domains", []) if isinstance(registry.get("domains"), list) else []:
            if not isinstance(domain, dict):
                continue
            for relation in domain.get("architecture_relations", []) if isinstance(domain.get("architecture_relations"), list) else []:
                if not isinstance(relation, dict) or relation.get("kind") not in {"depends_on", "shares_contract_with"}:
                    continue
                for law_id in relation.get("governing_law_ids", []) if isinstance(relation.get("governing_law_ids"), list) else []:
                    if law_id not in accepted_laws:
                        fail(findings, "manifest.domain_registry: governing law must be a current accepted decision/spec")
    relation_keys: set[tuple[str, str, str]] = set()
    graph: dict[str, list[str]] = {}
    for number, record in enumerate(records):
        if not isinstance(record, dict):
            continue
        prefix = f"manifest.records[{number}]"
        for relation in record.get("law_relations", []) if isinstance(record.get("law_relations", []), list) else []:
            if not isinstance(relation, dict) or relation.get("kind") not in LAW_KINDS:
                continue
            target = by_id.get(relation.get("target_id"))
            if target is None or target.get("kind") not in LAW_RELATION_SUBJECTS:
                fail(findings, f"{prefix}: law relation target is not a declared decision/spec record")
                continue
            kind = relation["kind"]
            left, right = record.get("id"), relation.get("target_id")
            if not isinstance(left, str) or not isinstance(right, str):
                continue
            if kind == "conflicts_with" and left > right:
                left, right = right, left
            key = (kind, left, right)
            if key in relation_keys:
                fail(findings, f"{prefix}: duplicate law relation, including reverse conflict")
            relation_keys.add(key)
            if kind == "supersedes":
                if record.get("status") != "accepted" or target.get("status") != "superseded" or target.get("successor") != record.get("id"):
                    fail(findings, f"{prefix}: supersedes relation disagrees with target successor")
            if kind in {"supersedes", "refines", "subordinate_to"}:
                graph.setdefault(record.get("id"), []).append(relation.get("target_id"))
    if schema_version in {"1.1", "1.2"}:
        for record in records:
            if not isinstance(record, dict) or not record.get("successor"):
                continue
            successor = by_id.get(record["successor"])
            if not any(isinstance(r, dict) and r.get("kind") == "supersedes" and r.get("target_id") == record.get("id") for r in (successor or {}).get("law_relations", [])):
                fail(findings, f"manifest.records: successor {record['successor']} lacks matching supersedes relation")

    def has_cycle(graph: dict[str, list[str]]) -> bool:
        visiting: set[str] = set()
        visited: set[str] = set()
        def visit(node: str) -> bool:
            if node in visiting:
                return True
            if node in visited:
                return False
            visiting.add(node)
            if any(visit(child) for child in graph.get(node, [])):
                return True
            visiting.remove(node)
            visited.add(node)
            return False
        return any(visit(node) for node in graph)

    if has_cycle(graph):
        fail(findings, "manifest: directed law relations contain a cycle")
    for number, record in enumerate(records):
        if not isinstance(record, dict) or record.get("status") != "superseded" or not isinstance(record.get("successor"), str):
            continue
        prefix = f"manifest.records[{number}]"
        successor = record["successor"]
        if successor == record.get("id"):
            fail(findings, f"{prefix}: successor cannot be self")
            continue
        target = by_id.get(successor)
        if target is None:
            fail(findings, f"{prefix}: successor is not declared in this manifest: {successor}")
            continue
        if target.get("kind") != record.get("kind"):
            fail(findings, f"{prefix}: successor kind does not match")
        expected_status = "accepted" if record.get("kind") in LAW_BEARING_KINDS else "published"
        if target.get("status") != expected_status:
            fail(findings, f"{prefix}: successor status is incompatible")

    decision_paths: dict[str, list[str]] = {}
    for path in sorted((ROOT / "docs/decisions").glob("CD-*.md"), key=lambda item: item.as_posix()):
        match = re.fullmatch(r"(CD-[0-9]{4})(?:-.*)?\.md", path.name)
        relative = path.relative_to(ROOT).as_posix()
        if match is None:
            fail(findings, f"manifest: decision file has a non-canonical name: {relative}")
            continue
        decision_paths.setdefault(match.group(1), []).append(relative)
    expected_decisions: dict[str, str] = {}
    for identifier, paths_for_id in decision_paths.items():
        if len(paths_for_id) != 1:
            fail(findings, f"manifest: decision {identifier} has multiple canonical files: {', '.join(paths_for_id)}")
            continue
        expected_decisions[identifier] = paths_for_id[0]
    for identifier, expected_path in expected_decisions.items():
        matches = [record for record in records if isinstance(record, dict) and record.get("id") == identifier]
        if len(matches) != 1:
            fail(findings, f"manifest: decision {identifier} must map exactly once")
            continue
        record = matches[0]
        if record.get("path") != expected_path or record.get("kind") != "decision":
            fail(findings, f"manifest: decision {identifier} does not map to its exact decision path/kind")
        if record.get("status") not in {"accepted", "superseded"}:
            fail(findings, f"manifest: decision {identifier} has invalid status")
    discovered_decision_paths = {candidate for candidates in decision_paths.values() for candidate in candidates}
    for record in records:
        if not isinstance(record, dict):
            continue
        path = record.get("path")
        identifier = record.get("id")
        if isinstance(path, str) and path.startswith("docs/decisions/") and path not in discovered_decision_paths:
            fail(findings, f"manifest: extra decision path is forbidden: {path}")
        if isinstance(identifier, str) and identifier.startswith("CD-") and identifier not in decision_paths:
            fail(findings, f"manifest: extra decision ID is forbidden: {identifier}")
    return findings


def atomic_write(path: Path, content: str) -> None:
    temporary: str | None = None
    try:
        with tempfile.NamedTemporaryFile("w", encoding="utf-8", dir=path.parent, prefix=f".{path.name}.", suffix=".tmp", delete=False) as handle:
            temporary = handle.name
            handle.write(content)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
        temporary = None
    finally:
        if temporary is not None:
            try:
                os.unlink(temporary)
            except FileNotFoundError:
                pass


def _load_generator() -> object:
    import importlib.util

    spec = importlib.util.spec_from_file_location(
        "generate_knowledge_index", Path(__file__).resolve().with_name("generate-knowledge-index.py")
    )
    if spec is None or spec.loader is None:
        raise RuntimeError("unable to load generate-knowledge-index.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def check_shard_encoding(findings: list[str]) -> None:
    """Authored shards already carry the byte form shard_format owns.

    A shard differing only in indent, key order, or trailing newline still
    parses, so every other check passes and the drift stays invisible. The
    generators reprove it only under a manual --check run; this is the path CI
    takes, so it holds on every push. Coverage shards have no other live check
    at all, which is why they are held here too.
    """
    shard_dirs = {
        "record shard": ROOT / "docs/knowledge/records",
        "coverage shard": ROOT / "docs/knowledge/coverage",
    }
    for label, shard_dir in shard_dirs.items():
        if not shard_dir.is_dir():
            continue
        for shard_path in sorted(shard_dir.glob("*.json")):
            raw = shard_path.read_text(encoding="utf-8")
            try:
                parsed = json.loads(raw)
            except json.JSONDecodeError:
                continue
            if raw.encode("utf-8") != shard_format.canonical_bytes(parsed):
                fail(
                    findings,
                    f"manifest: {label} is not canonically encoded: {shard_path.relative_to(ROOT)}; "
                    "run python3 scripts/generate-knowledge-index.py --update "
                    "and python3 scripts/generate-law-coverage.py --update",
                )


def check_aggregate_freshness(findings: list[str]) -> None:
    shard_dir = ROOT / "docs/knowledge/records"
    if not shard_dir.is_dir():
        return
    try:
        generator = _load_generator()
        derived = generator.derive_aggregate(ROOT, [])
    except (OSError, ValueError, RuntimeError) as exc:
        findings.append(f"knowledge index aggregate freshness: {exc}")
        return
    if derived is None:
        findings.append(
            "knowledge index aggregate freshness: generator rejected shards; see above"
        )
        return
    if not MANIFEST.is_file():
        findings.append("knowledge index aggregate freshness: aggregate is missing")
        return
    actual = MANIFEST.read_bytes()
    if derived != actual:
        findings.append(
            "knowledge index aggregate is stale relative to shards; "
            "run python3 scripts/generate-knowledge-index.py --update"
        )


def update_manifest(data: object) -> list[str]:
    findings = validate(data, check_hashes=False)
    if findings:
        return findings
    assert isinstance(data, dict)
    records = data["records"]
    shard_dir = ROOT / "docs/knowledge/records"
    if not shard_dir.is_dir():
        return ["manifest: knowledge record shard directory is missing"]
    shards: dict[str, dict[str, object]] = {}
    for record in records:
        assert isinstance(record, dict)
        target = ROOT / record["path"]
        shard_path = shard_dir / f"{record['id']}.json"
        try:
            shard = load(shard_path, [])
        except OSError:
            shard = None
        if not isinstance(shard, dict):
            return [f"manifest: missing or invalid record shard: {shard_path.relative_to(ROOT)}"]
        original = dict(record)
        original.pop("sha256", None)
        shard_without_hash = dict(shard)
        shard_without_hash.pop("sha256", None)
        if shard_without_hash != original:
            return [f"manifest: record shard disagrees with aggregate: {shard_path.relative_to(ROOT)}"]
        digest = "sha256:" + hashlib.sha256(target.read_bytes()).hexdigest()
        record["sha256"] = digest
        shard["sha256"] = digest
        shards[record["id"]] = shard
    findings = validate(data, check_hashes=True)
    if findings:
        return findings
    try:
        generator = _load_generator()
        for identifier, shard in shards.items():
            shard_path = shard_dir / f"{identifier}.json"
            atomic_write(shard_path, shard_format.canonical_bytes(shard).decode("utf-8"))
        derived = generator.derive_aggregate(ROOT, [], template=data)
        if derived is None:
            return ["manifest: knowledge record shards failed generator validation"]
        atomic_write(MANIFEST, derived.decode("utf-8"))
    except OSError as exc:
        return [f"manifest: atomic update failed: {exc}"]
    return []


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--update", action="store_true", help="recompute hashes for an already-valid authored manifest")
    args = parser.parse_args()
    findings: list[str] = []
    data = load(MANIFEST, findings)
    if data is not None and args.update and not findings:
        findings = update_manifest(data)
    elif data is not None:
        findings = validate(data)
        check_shard_encoding(findings)
        check_aggregate_freshness(findings)
    for finding in findings:
        print(finding)
    if findings:
        print(f"Knowledge index validation failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    print("Knowledge index validation passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

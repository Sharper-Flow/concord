#!/usr/bin/env python3
"""Generate the durable knowledge index from per-record source shards."""

from __future__ import annotations

import argparse
import difflib
import json
import re
import sys
from datetime import datetime
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import shard_format  # noqa: E402

ROOT = Path(__file__).resolve().parents[1]
SHARD_DIR = Path("docs/knowledge/records")
DOMAIN_REGISTRY = Path("docs/knowledge/domain-registry.json")
AGGREGATE = Path("docs/concord-knowledge-index.v1.json")

ALLOWED_ROOT = {
    "schema_version",
    "supported_kinds",
    "indexed_kinds",
    "domain_registry",
    "knowledge_roots",
    "exclusions",
    "dispositions",
    "doc_contract",
    "records",
}
ALLOWED_RECORD = {
    "id",
    "kind",
    "path",
    "status",
    "date",
    "title",
    "summary",
    "tags",
    "scopes",
    "successor",
    "sha256",
    "law_relations",
    "evidence",
    "criterion_bindings",
    "home_domain_id",
    "applies_to_domain_ids",
    "product_wide_rationale",
}
REQUIRED_RECORD = ALLOWED_RECORD - {
    "successor",
    "law_relations",
    "evidence",
    "criterion_bindings",
    "home_domain_id",
    "applies_to_domain_ids",
    "product_wide_rationale",
}
SUPPORTED_KINDS = {"work_note", "constitution", "decision", "spec", "lesson", "reference", "research"}
KINDS = {"constitution", "decision", "spec", "lesson", "reference", "research"}
# A law-bearing record takes status accepted or superseded; every other record
# kind takes published or superseded. The law-relation graph is narrower still:
# it is defined over decisions and specs, so a constitution is law-bearing for
# status purposes without joining that graph.
LAW_BEARING_KINDS = {"constitution", "decision", "spec"}
LAW_RELATION_SUBJECTS = {"decision", "spec"}
LAW_RELATION_KINDS = {"supersedes", "refines", "subordinate_to", "conflicts_with"}
# Which authored docs path may carry a record is declared once, as the
# $defs.record.path pattern in contracts/concord-knowledge-index.v1.schema.json.
# check-knowledge-vocabulary.py binds this restatement to that pattern text.
RECORD_PATH_RE = re.compile(r"^docs/(?!work/|research/|.*[Gg][Ee][Nn][Ee][Rr][Aa][Tt][Ee][Dd]).*\.md$")
SCOPE_FIELDS_V10 = {"mode", "product_ids", "project_ids", "component_ids", "tag_ids"}
SCOPE_FIELDS_V12 = {"mode", "product_ids", "project_ids", "domain_ids", "tag_ids"}


class DuplicateKeyError(ValueError):
    pass


def reject_duplicate_pairs(pairs: list[tuple[str, object]]) -> dict[str, object]:
    result: dict[str, object] = {}
    for key, value in pairs:
        if key in result:
            raise DuplicateKeyError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def load_json(path: Path, findings: list[str]) -> object | None:
    try:
        return json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=reject_duplicate_pairs)
    except (OSError, UnicodeDecodeError, json.JSONDecodeError, DuplicateKeyError) as exc:
        findings.append(f"{path}: invalid JSON: {exc}")
        return None


def clean_text(value: object, maximum: int) -> bool:
    return isinstance(value, str) and 0 < len(value) <= maximum and value == value.strip()


def unique_ids(value: object, maximum: int = 64) -> bool:
    return (
        isinstance(value, list)
        and len(value) <= maximum
        and all(clean_text(item, 256) for item in value)
        and len(value) == len(set(value))
    )


def validate_scopes(scopes: object, schema_version: str, prefix: str, findings: list[str]) -> None:
    allowed = SCOPE_FIELDS_V12 if schema_version == "1.2" else SCOPE_FIELDS_V10
    if not isinstance(scopes, dict):
        findings.append(f"{prefix}: scopes must be an object")
        return
    unknown = set(scopes) - allowed
    missing = allowed - set(scopes)
    if unknown:
        findings.append(f"{prefix}: unknown scope fields: {sorted(unknown)}")
    if missing:
        findings.append(f"{prefix}: missing scope fields: {sorted(missing)}")
    if scopes.get("mode") not in {"home", "explicit"}:
        findings.append(f"{prefix}: scope mode must be home or explicit")
    for field in allowed - {"mode"}:
        if field in scopes and not unique_ids(scopes[field]):
            findings.append(f"{prefix}: invalid {field}")
    if scopes.get("mode") == "home" and any(scopes.get(field) for field in allowed - {"mode"}):
        findings.append(f"{prefix}: home scopes cannot contain explicit IDs")


def validate_criterion_bindings(record: dict[str, object], prefix: str, findings: list[str]) -> None:
    bindings = record.get("criterion_bindings")
    if bindings is None:
        return
    if record.get("kind") != "spec":
        findings.append(f"{prefix}: criterion_bindings are only allowed on spec records")
    if not isinstance(bindings, list) or len(bindings) > 1000:
        findings.append(f"{prefix}: criterion_bindings must be a bounded array")
        return
    seen: set[int] = set()
    for number, binding in enumerate(bindings):
        binding_prefix = f"{prefix}.criterion_bindings[{number}]"
        if not isinstance(binding, dict) or set(binding) not in ({"criterion", "scenario"}, {"criterion", "exemption"}):
            findings.append(f"{binding_prefix}: binding must carry criterion and exactly one scenario or exemption")
            continue
        criterion = binding.get("criterion")
        if not isinstance(criterion, int) or isinstance(criterion, bool) or criterion < 1:
            findings.append(f"{binding_prefix}: criterion must be a positive integer")
        elif criterion in seen:
            findings.append(f"{binding_prefix}: duplicate criterion index {criterion}")
        else:
            seen.add(criterion)
        if "scenario" in binding and not clean_text(binding.get("scenario"), 256):
            findings.append(f"{binding_prefix}: scenario must be a clean bounded ID")
        if "exemption" in binding and (
            not clean_text(binding.get("exemption"), 512)
            or len(binding["exemption"]) < 12
        ):
            findings.append(f"{binding_prefix}: exemption must be a trimmed reason of 12-512 characters")


def validate_record(record: object, schema_version: str, domain_ids: set[str], prefix: str, findings: list[str]) -> None:
    if not isinstance(record, dict):
        findings.append(f"{prefix}: shard must be an object")
        return
    unknown = set(record) - ALLOWED_RECORD
    missing = REQUIRED_RECORD - set(record)
    if unknown:
        findings.append(f"{prefix}: unknown fields: {sorted(unknown)}")
    if missing:
        findings.append(f"{prefix}: missing fields: {sorted(missing)}")
        return

    identifier = record["id"]
    if not clean_text(identifier, 256):
        findings.append(f"{prefix}: invalid ID")
    kind = record["kind"]
    if kind not in KINDS:
        findings.append(f"{prefix}: invalid kind")
    status = record["status"]
    law_bearing = kind in LAW_BEARING_KINDS
    expected_statuses = {"accepted", "superseded"} if law_bearing else {"published", "superseded"}
    if status not in expected_statuses:
        findings.append(f"{prefix}: invalid status/kind combination")
    if status == "superseded" and not clean_text(record.get("successor"), 256):
        findings.append(f"{prefix}: superseded record requires a successor")
    if status != "superseded" and "successor" in record:
        findings.append(f"{prefix}: successor is only valid for superseded records")
    path = record["path"]
    if (
        not isinstance(path, str)
        or len(path) > 512
        or "\x00" in path
        or ".." in Path(path).parts
        or not RECORD_PATH_RE.fullmatch(path)
    ):
        findings.append(f"{prefix}: forbidden or unsafe path: {path}")
    try:
        datetime.fromisoformat(str(record["date"]).replace("Z", "+00:00"))
    except (TypeError, ValueError):
        findings.append(f"{prefix}: date is not RFC3339")
    for field, maximum in (("title", 256), ("summary", 4096)):
        if not clean_text(record[field], maximum):
            findings.append(f"{prefix}: invalid bounded {field}")
    if not unique_ids(record["tags"]):
        findings.append(f"{prefix}: invalid tags")
    validate_scopes(record["scopes"], schema_version, prefix, findings)
    validate_criterion_bindings(record, prefix, findings)

    if not isinstance(record["sha256"], str) or len(record["sha256"]) != 71 or not record["sha256"].startswith("sha256:") or any(char not in "0123456789abcdef" for char in record["sha256"][7:]):
        findings.append(f"{prefix}: invalid sha256 proof")
    if "law_relations" in record:
        relations = record["law_relations"]
        if kind not in LAW_RELATION_SUBJECTS or not isinstance(relations, list) or len(relations) > 32:
            findings.append(f"{prefix}: invalid law_relations")
        elif any(not isinstance(item, dict) or set(item) != {"kind", "target_id"} or item["kind"] not in LAW_RELATION_KINDS or not clean_text(item["target_id"], 256) for item in relations):
            findings.append(f"{prefix}: invalid law relation")
    if "evidence" in record and (not isinstance(record["evidence"], list) or len(record["evidence"]) > 32 or any(not isinstance(item, str) or not 1 <= len(item) <= 512 or item.startswith("/") or ".." in item for item in record["evidence"])):
        findings.append(f"{prefix}: invalid evidence paths")
    if schema_version != "1.2" and ({"home_domain_id", "applies_to_domain_ids"} & set(record)):
        findings.append(f"{prefix}: law-home fields require schema_version 1.2")
    if schema_version == "1.2":
        if not law_bearing and ({"home_domain_id", "applies_to_domain_ids"} & set(record)):
            findings.append(f"{prefix}: non-law records cannot author law-home fields")
        if law_bearing and status == "accepted" and not clean_text(record.get("home_domain_id"), 256):
            findings.append(f"{prefix}: an accepted law-bearing record requires one home domain")
        for field in ("home_domain_id",):
            if field in record and record[field] not in domain_ids:
                findings.append(f"{prefix}: home domain is dangling")
        if "applies_to_domain_ids" in record:
            values = record["applies_to_domain_ids"]
            if not unique_ids(values) or any(value not in domain_ids for value in values):
                findings.append(f"{prefix}: invalid or dangling applies_to_domain_ids")
            if "home_domain_id" not in record:
                findings.append(f"{prefix}: applies_to_domain_ids requires home_domain_id")
            elif record["home_domain_id"] in values:
                findings.append(f"{prefix}: applies_to_domain_ids repeats home_domain_id")


def canonical_scopes(scopes: dict[str, object]) -> dict[str, object]:
    return dict(sorted(scopes.items()))


def canonical_record(record: dict[str, object]) -> dict[str, object]:
    canonical = dict(sorted(record.items()))
    if isinstance(canonical.get("scopes"), dict):
        canonical["scopes"] = canonical_scopes(canonical["scopes"])
    if isinstance(canonical.get("law_relations"), list):
        canonical["law_relations"] = [dict(sorted(item.items())) for item in canonical["law_relations"] if isinstance(item, dict)]
    return canonical


def canonical_domain_registry(registry: dict[str, object]) -> dict[str, object]:
    fields = ["schema_version", "product_key", "root_domain_id", "domains"]
    result: dict[str, object] = {field: registry[field] for field in fields if field in registry}
    domains: list[dict[str, object]] = []
    for domain in registry.get("domains", []):
        if not isinstance(domain, dict):
            domains.append(domain)
            continue
        domain_fields = ["domain_id", "name", "purpose", "parent_domain_id", "status", "architecture_relations"]
        item = {field: domain[field] for field in domain_fields if field in domain}
        relations: list[dict[str, object]] = []
        for relation in domain.get("architecture_relations", []):
            if isinstance(relation, dict):
                relation_fields = ["kind", "target_domain_id", "governing_law_ids", "state"]
                relations.append({field: relation[field] for field in relation_fields if field in relation})
        if "architecture_relations" in item:
            item["architecture_relations"] = relations
        domains.append(item)
    if "domains" in result:
        result["domains"] = domains
    return result


def load_records(root: Path, schema_version: str, domain_ids: set[str], findings: list[str]) -> list[dict[str, object]]:
    directory = root / SHARD_DIR
    if not directory.is_dir():
        findings.append(f"shard directory missing: {SHARD_DIR}")
        return []
    paths = sorted(directory.glob("*.json"))
    if not paths:
        findings.append(f"no shards found in {SHARD_DIR}")
        return []
    records: list[dict[str, object]] = []
    seen: set[str] = set()
    for path in paths:
        record = load_json(path, findings)
        if not isinstance(record, dict):
            continue
        identifier = record.get("id")
        prefix = f"{SHARD_DIR / path.name}"
        validate_record(record, schema_version, domain_ids, prefix, findings)
        if not isinstance(identifier, str):
            continue
        if path.stem != identifier:
            findings.append(f"{prefix}: filename must match record id {identifier}")
        if identifier in seen:
            findings.append(f"duplicate shard id: {identifier}")
            continue
        seen.add(identifier)
        records.append(record)
    records.sort(key=lambda record: str(record.get("id", "")))
    return records


def template_for(root: Path, findings: list[str], template: dict[str, object] | None) -> dict[str, object] | None:
    if template is None:
        aggregate = root / AGGREGATE
        if aggregate.is_file():
            loaded = load_json(aggregate, findings)
            if not isinstance(loaded, dict):
                return None
            template = loaded
        else:
            template = {
                "schema_version": "1.2",
                "supported_kinds": sorted(SUPPORTED_KINDS),
                "indexed_kinds": sorted(SUPPORTED_KINDS),
            }
    unknown = set(template) - ALLOWED_ROOT
    if unknown:
        findings.append(f"aggregate template has unknown keys: {sorted(unknown)}")
    if template.get("schema_version") not in {"1.0", "1.1", "1.2"}:
        findings.append("aggregate template has invalid schema_version")
    if not isinstance(template.get("supported_kinds"), list) or not isinstance(template.get("indexed_kinds"), list):
        findings.append("aggregate template is missing kind arrays")
    return dict(template)


def derive_aggregate(root: Path, findings: list[str], template: dict[str, object] | None = None) -> bytes | None:
    root_template = template_for(root, findings, template)
    if root_template is None or findings:
        return None
    schema_version = str(root_template["schema_version"])
    registry_path = root / DOMAIN_REGISTRY
    registry = load_json(registry_path, findings) if registry_path.is_file() else root_template.get("domain_registry")
    if schema_version == "1.2" and not isinstance(registry, dict):
        findings.append(f"domain registry missing: {DOMAIN_REGISTRY}")
        return None
    if isinstance(registry, dict):
        unknown = set(registry) - {"schema_version", "product_key", "root_domain_id", "domains"}
        if unknown:
            findings.append(f"domain registry has unknown keys: {sorted(unknown)}")
    domain_ids = {
        domain.get("domain_id")
        for domain in registry.get("domains", [])
        if isinstance(domain, dict) and isinstance(domain.get("domain_id"), str)
    } if isinstance(registry, dict) else set()
    records = load_records(root, schema_version, domain_ids, findings)
    if findings:
        return None
    aggregate = dict(root_template)
    if isinstance(registry, dict):
        aggregate["domain_registry"] = canonical_domain_registry(registry)
    aggregate["records"] = [canonical_record(record) for record in records]
    return (json.dumps(aggregate, ensure_ascii=False, sort_keys=False, indent=2) + "\n").encode("utf-8")


def bounded_diff(actual: bytes, derived: bytes) -> list[str]:
    lines = list(difflib.unified_diff(
        actual.decode("utf-8", errors="replace").splitlines(),
        derived.decode("utf-8", errors="replace").splitlines(),
        fromfile=str(AGGREGATE),
        tofile="derived",
        lineterm="",
    ))
    return lines[:80]


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--check", action="store_true", help="fail when the aggregate differs from shards")
    parser.add_argument("--update", action="store_true", help="write the aggregate derived from shards")
    parser.add_argument("--root", type=Path, default=ROOT, help="repository root")
    args = parser.parse_args()
    findings: list[str] = []
    aggregate_path = args.root / AGGREGATE
    derived = derive_aggregate(args.root, findings)
    if derived is None:
        for finding in findings[:80]:
            print(finding)
        print(f"knowledge index generation failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    shards = sorted((args.root / SHARD_DIR).glob("*.json"))
    if args.check:
        if not aggregate_path.is_file():
            print(f"aggregate missing: {AGGREGATE}", file=sys.stderr)
            return 1
        unformatted = shard_format.drifted(shards)
        if unformatted:
            print("knowledge shard format drift: regenerate to normalise", file=sys.stderr)
            for path in unformatted[:20]:
                print(f"  {path.relative_to(args.root)}", file=sys.stderr)
            return 1
        actual = aggregate_path.read_bytes()
        if actual != derived:
            print("knowledge index aggregate drift: shards and aggregate disagree", file=sys.stderr)
            for line in bounded_diff(actual, derived):
                print(line, file=sys.stderr)
            return 1
        print("knowledge index aggregate is up to date")
        return 0
    for path in shard_format.normalise(shards):
        print(f"normalised {path.relative_to(args.root)}")
    aggregate_path.parent.mkdir(parents=True, exist_ok=True)
    aggregate_path.write_bytes(derived)
    print(f"wrote {AGGREGATE}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

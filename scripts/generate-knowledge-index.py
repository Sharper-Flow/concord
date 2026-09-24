#!/usr/bin/env python3
"""Compose the durable knowledge index from its shards and keep them canonical.

The shards under docs/knowledge/ are the committed authority (CD-0114). No
aggregate file is written; readers compose the index through
scripts/knowledge_index.py.
"""

from __future__ import annotations

import argparse
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
HEAD = Path("docs/knowledge/manifest.json")

# The bounded set the parser reads, mirroring knowledgeManifestSchemaAccepted in
# internal/store/knowledge_manifest.go. 1.2 predates the authority tier and 1.3
# requires it on every record.
SCHEMA_VERSION_LEGACY = "1.2"
SCHEMA_VERSION_CURRENT = "1.3"
SCHEMA_VERSIONS = {SCHEMA_VERSION_LEGACY, SCHEMA_VERSION_CURRENT}

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
    "authority",
    "law_relations",
    "evidence",
    "criterion_bindings",
    "home_domain_id",
    "applies_to_domain_ids",
    "product_wide_rationale",
    "doc_contract_profile",
}
# CD-0175: the authored outline generation a decision record carries, and the
# closed historical set that bounds the legacy profile. The set is frozen in
# contracts/concord-knowledge-index.v1.schema.json ($defs.legacyDecisionProfileId)
# and bound here by scripts/check-knowledge-vocabulary.py, so authoring a
# profile claim is never enough to escape the current outline.
DECISION_PROFILES = {"legacy", "current"}
LEGACY_DECISION_PROFILE_IDS = frozenset({"CD-0002", "CD-0003", "CD-0005", "CD-0006", "CD-0007", "CD-0008", "CD-0009", "CD-0010", "CD-0011", "CD-0012", "CD-0013", "CD-0014", "CD-0015", "CD-0016", "CD-0017", "CD-0018", "CD-0019", "CD-0020", "CD-0021", "CD-0022", "CD-0023", "CD-0024", "CD-0025", "CD-0026", "CD-0027", "CD-0028", "CD-0029", "CD-0030", "CD-0031", "CD-0033", "CD-0034", "CD-0035", "CD-0036", "CD-0037", "CD-0038", "CD-0039", "CD-0040", "CD-0041", "CD-0042", "CD-0043", "CD-0044", "CD-0045", "CD-0046", "CD-0047", "CD-0048", "CD-0049", "CD-0050", "CD-0051", "CD-0052", "CD-0053", "CD-0054", "CD-0055", "CD-0056", "CD-0057", "CD-0058", "CD-0059", "CD-0060", "CD-0061", "CD-0062", "CD-0063", "CD-0064", "CD-0065", "CD-0066", "CD-0067", "CD-0068", "CD-0069", "CD-0070", "CD-0071", "CD-0072", "CD-0073", "CD-0074", "CD-0075", "CD-0076", "CD-0077", "CD-0078", "CD-0079", "CD-0080", "CD-0081", "CD-0082", "CD-0083", "CD-0084", "CD-0085", "CD-0086", "CD-0087", "CD-0088", "CD-0089", "CD-0090", "CD-0091", "CD-0092", "CD-0093", "CD-0094", "CD-0095", "CD-0096", "CD-0097", "CD-0098", "CD-0102", "CD-0103", "CD-0104", "CD-0105", "CD-0106", "CD-0108", "CD-0109", "CD-0110", "CD-0111", "CD-0112", "CD-0113", "CD-0114", "CD-0115", "CD-0116", "CD-0117", "CD-0118", "CD-0119", "CD-0120", "CD-0121", "CD-0122", "CD-0124", "CD-0128", "CD-0129", "CD-0130", "CD-0132", "CD-0133", "CD-0134", "CD-0137", "CD-0138", "CD-0139", "CD-0140", "CD-0142", "CD-0143", "CD-0144", "CD-0145", "CD-0146", "CD-0147", "CD-0148", "CD-0149", "CD-0150", "CD-0151", "CD-0152", "CD-0153", "CD-0154", "CD-0155", "CD-0156", "CD-0157", "CD-0158", "CD-0159", "CD-0160", "CD-0161", "CD-0162", "CD-0163", "CD-0164", "CD-0165", "CD-0166", "CD-0167", "CD-0168", "CD-0169", "CD-0170", "CD-0171", "CD-0172", "CD-0173", "CD-0174"})
REQUIRED_RECORD = ALLOWED_RECORD - {
    "successor",
    "law_relations",
    "evidence",
    "criterion_bindings",
    "home_domain_id",
    "applies_to_domain_ids",
    "product_wide_rationale",
    "doc_contract_profile",
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
    allowed = SCOPE_FIELDS_V12
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


def validate_record(record: object, schema_version: str, domain_ids: set[str], prefix: str, findings: list[str], profiles_enforced: bool = False) -> None:
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
    validate_authority(record, prefix, findings)

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
    if not law_bearing and ({"home_domain_id", "applies_to_domain_ids", "product_wide_rationale"} & set(record)):
        findings.append(f"{prefix}: non-law records cannot author law-home fields")
    if law_bearing and status == "accepted" and not clean_text(record.get("home_domain_id"), 256):
        findings.append(f"{prefix}: an accepted law-bearing record requires one home domain")
    for field in ("home_domain_id",):
        if field in record and record[field] not in domain_ids:
            findings.append(f"{prefix}: home domain is dangling")
    # CD-0175: every decision names its outline generation in its own shard.
    # The legacy profile is bounded by the closed historical set in both
    # directions: a set member cannot claim the current outline it never
    # carried, and a record outside the set cannot claim the legacy outline,
    # so a newly accepted decision cannot exempt itself.
    profile = record.get("doc_contract_profile")
    if kind == "decision":
        if profile is None:
            if profiles_enforced:
                findings.append(f"{prefix}: decision requires a doc_contract_profile of 'legacy' or 'current'")
        elif profile not in DECISION_PROFILES:
            findings.append(f"{prefix}: decision requires a doc_contract_profile of 'legacy' or 'current'")
        elif (profile == "legacy") != (identifier in LEGACY_DECISION_PROFILE_IDS):
            findings.append(f"{prefix}: doc_contract_profile {profile!r} contradicts the closed legacy decision set for {identifier}")
    elif "doc_contract_profile" in record:
        findings.append(f"{prefix}: doc_contract_profile is only valid on decision records")
    if "applies_to_domain_ids" in record:
        values = record["applies_to_domain_ids"]
        if not unique_ids(values) or any(value not in domain_ids for value in values):
            findings.append(f"{prefix}: invalid or dangling applies_to_domain_ids")
        if "home_domain_id" not in record:
            findings.append(f"{prefix}: applies_to_domain_ids requires home_domain_id")
        elif record["home_domain_id"] in values:
            findings.append(f"{prefix}: applies_to_domain_ids repeats home_domain_id")


AUTHORITY_TIERS = {"legislated", "derived"}
AUTHORITY_FIELDS = {"tier", "legislated_by", "contract_version"}


def validate_authority(record: dict[str, object], prefix: str, findings: list[str]) -> None:
    """CD-0159 D3: a record carries its standing beside its lifecycle.

    The same rule check-knowledge-index.py enforces over the composed
    manifest, applied where the shards are validated before composition.
    """
    authority = record["authority"]
    if not isinstance(authority, dict) or set(authority) - AUTHORITY_FIELDS or authority.get("tier") not in AUTHORITY_TIERS:
        findings.append(f"{prefix}: invalid authority object")
        return
    if authority["tier"] == "legislated":
        version = authority.get("contract_version")
        if not clean_text(authority.get("legislated_by"), 256) or not isinstance(version, int) or isinstance(version, bool) or version < 1:
            findings.append(f"{prefix}: a legislated record requires clean legislated_by and a positive contract_version")
    elif "legislated_by" in authority or "contract_version" in authority:
        findings.append(f"{prefix}: a derived record carries no legislative fields")


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


def load_records(root: Path, schema_version: str, domain_ids: set[str], profiles_enforced: bool, findings: list[str]) -> list[dict[str, object]]:
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
        validate_record(record, schema_version, domain_ids, prefix, findings, profiles_enforced)
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
        head = root / HEAD
        if not head.is_file():
            findings.append(f"manifest head missing: {HEAD}")
            return None
        loaded = load_json(head, findings)
        if not isinstance(loaded, dict):
            return None
        template = loaded
    unknown = set(template) - (ALLOWED_ROOT - {"domain_registry", "records"})
    if unknown:
        findings.append(f"manifest head has unknown keys: {sorted(unknown)}")
    if template.get("schema_version") not in SCHEMA_VERSIONS:
        findings.append(f"manifest head schema_version must be one of {sorted(SCHEMA_VERSIONS)}")
    if not isinstance(template.get("supported_kinds"), list) or not isinstance(template.get("indexed_kinds"), list):
        findings.append("manifest head is missing kind arrays")
    return dict(template)


def derive_aggregate(root: Path, findings: list[str], template: dict[str, object] | None = None) -> bytes | None:
    root_template = template_for(root, findings, template)
    if root_template is None or findings:
        return None
    schema_version = str(root_template["schema_version"])
    registry_path = root / DOMAIN_REGISTRY
    registry = load_json(registry_path, findings) if registry_path.is_file() else root_template.get("domain_registry")
    if not isinstance(registry, dict):
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
    doc_contract = root_template.get("doc_contract")
    decision_head = doc_contract.get("decision") if isinstance(doc_contract, dict) else None
    profiles_enforced = isinstance(decision_head, dict) and "current_required_sections" in decision_head
    records = load_records(root, schema_version, domain_ids, profiles_enforced, findings)
    if findings:
        return None
    aggregate = dict(root_template)
    if isinstance(registry, dict):
        aggregate["domain_registry"] = canonical_domain_registry(registry)
    aggregate["records"] = [canonical_record(record) for record in records]
    return (json.dumps(aggregate, ensure_ascii=False, sort_keys=False, indent=2) + "\n").encode("utf-8")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--check", action="store_true", help="fail when the shards do not compose or are not canonical")
    parser.add_argument("--update", action="store_true", help="normalise the shards to their canonical encoding")
    parser.add_argument("--root", type=Path, default=ROOT, help="repository root")
    args = parser.parse_args()
    findings: list[str] = []
    derived = derive_aggregate(args.root, findings)
    if derived is None:
        for finding in findings[:80]:
            print(finding)
        print(f"knowledge index composition failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    shards = sorted((args.root / SHARD_DIR).glob("*.json")) + [args.root / HEAD]
    if args.check:
        unformatted = shard_format.drifted(shards)
        if unformatted:
            print("knowledge shard format drift: run --update to normalise", file=sys.stderr)
            for path in unformatted[:20]:
                print(f"  {path.relative_to(args.root)}", file=sys.stderr)
            return 1
        print(f"knowledge index composes from {len(shards) - 1} record shard(s)")
        return 0
    for path in shard_format.normalise(shards):
        print(f"normalised {path.relative_to(args.root)}")
    print(f"knowledge index composes from {len(shards) - 1} record shard(s)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

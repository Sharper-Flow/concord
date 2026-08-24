#!/usr/bin/env python3
"""Validate that every recorded law declares how it is proved.

scripts/check-knowledge-index.py proves record integrity: that the manifest
describes real, unmodified documents, and that no CD file escapes the index.
That is a different proposition from whether any of those documents is
implemented. Its own comment says it fires "before the recorded law and the
current implementation drift apart silently"; the check below that comment is
`target.is_file()`.

This validator owns the missing proposition. Every record in the knowledge
index carries a coverage state, and a `satisfied` state cites typed anchors
that resolve and — where the anchor is executable — that a required check
actually runs. A repository path is not an anchor: paths are exactly what the
existing drift audit accepts, and accepting them here would restate the defect
in new syntax (CD-0047 D3).

The subject set comes from the knowledge index rather than from this manifest,
so a record cannot escape coverage by being absent (CD-0047 D1). Anchor
resolution lives in `scripts/evidence_anchors.py` so the same proof machinery
is shared with `scripts/check-floor-readiness.py`, and outstanding-pointer
liveness lives in `scripts/coverage_state.py` so it is shared with every other
plane that declares the same states (#451).
"""
from __future__ import annotations

import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))

from coverage_state import (  # noqa: E402
    MAX_EVIDENCE,
    STATES,
    bounded_text,
    check_outstanding_pointer,
    check_state_obligations,
    check_subject_set,
    load_issue_states,
    load_json,
    report,
)
from evidence_anchors import (  # noqa: E402
    ANCHOR_KINDS,
    check_anchor,
    deferred_scenarios,
)

MANIFEST = ROOT / "docs/law-coverage.v1.json"
SCHEMA = ROOT / "contracts/law-coverage.schema.json"
INDEX = ROOT / "docs/concord-knowledge-index.v1.json"

ALLOWED_ROOT = {"schema_version", "source", "records"}
ALLOWED_RECORD = {"id", "state", "evidence", "issue", "reason"}


def _load_generator() -> object:
    import importlib.util

    spec = importlib.util.spec_from_file_location(
        "generate_law_coverage", ROOT / "scripts/generate-law-coverage.py"
    )
    if spec is None or spec.loader is None:
        raise RuntimeError("unable to load generate-law-coverage.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def check_aggregate_freshness(findings: list[str]) -> None:
    shard_dir = ROOT / "docs/knowledge/coverage"
    if not shard_dir.is_dir():
        return
    try:
        generator = _load_generator()
        derived = generator.derive_aggregate(ROOT, [])
    except (OSError, ValueError, RuntimeError) as exc:
        findings.append(f"law coverage aggregate freshness: {exc}")
        return
    if derived is None:
        findings.append(
            "law coverage aggregate freshness: generator rejected shards; see above"
        )
        return
    actual = MANIFEST.read_bytes()
    if derived != actual:
        findings.append(
            "law coverage aggregate is stale relative to shards; "
            "run python3 scripts/generate-law-coverage.py --update"
        )


def indexed_record_ids(findings: list[str]) -> list[str]:
    index = load_json(INDEX, findings)
    if not isinstance(index, dict):
        findings.append("knowledge index is not an object")
        return []
    records = index.get("records")
    if not isinstance(records, list):
        findings.append("knowledge index has no records array")
        return []
    ids: list[str] = []
    for record in records:
        if isinstance(record, dict) and isinstance(record.get("id"), str):
            ids.append(record["id"])
    return ids


def check_schema_states() -> list[str]:
    """The published schema must enumerate the shared vocabulary, not a copy of it."""
    findings: list[str] = []
    document = load_json(SCHEMA, findings)
    if not isinstance(document, dict):
        return findings
    enum = (
        document.get("properties", {})
        .get("records", {})
        .get("items", {})
        .get("properties", {})
        .get("state", {})
        .get("enum")
    )
    if enum != list(STATES):
        findings.append(
            f"{SCHEMA.name}: state enum must equal the shared vocabulary {list(STATES)}, got {enum!r}"
        )
    return findings


def main() -> int:
    findings: list[str] = check_schema_states()
    manifest = load_json(MANIFEST, findings)
    if not isinstance(manifest, dict):
        return report(findings, "law coverage")

    unknown = set(manifest) - ALLOWED_ROOT
    if unknown:
        findings.append(f"unknown manifest keys: {sorted(unknown)}")
    if manifest.get("schema_version") != "1.0":
        findings.append("schema_version must be \"1.0\"")

    records = manifest.get("records")
    if not isinstance(records, list) or not records:
        findings.append("records must be a non-empty array")
        return report(findings, "law coverage")

    declared: list[str] = []
    issue_states: dict[str, str] | None = load_issue_states(findings)
    for record in records:
        if not isinstance(record, dict):
            findings.append("record must be an object")
            continue
        identifier = record.get("id")
        if not bounded_text(identifier, 2, 128):
            findings.append(f"record id must be trimmed text: {identifier!r}")
            continue
        declared.append(identifier)
        prefix = f"record {identifier}"

        unknown_fields = set(record) - ALLOWED_RECORD
        if unknown_fields:
            findings.append(f"{prefix}: unknown fields: {sorted(unknown_fields)}")

        if not check_state_obligations(record, prefix, findings):
            continue

        if record.get("state") == "outstanding":
            check_outstanding_pointer(record, prefix, issue_states, findings)

        if record.get("state") == "satisfied":
            evidence = record.get("evidence")
            if not isinstance(evidence, list) or not evidence:
                findings.append(f"{prefix}: evidence must be a non-empty array of anchors")
            elif len(evidence) > MAX_EVIDENCE:
                findings.append(f"{prefix}: evidence must carry at most {MAX_EVIDENCE} anchors")
            else:
                for position, anchor in enumerate(evidence):
                    check_anchor(anchor, f"{prefix} anchor {position}", findings)

    check_subject_set(declared, indexed_record_ids(findings), "law record", findings)
    check_aggregate_freshness(findings)
    return report(findings, "law coverage")


if __name__ == "__main__":
    raise SystemExit(main())
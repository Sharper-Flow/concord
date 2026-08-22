#!/usr/bin/env python3
"""Deterministically generate docs/law-coverage.v1.json from per-record shards.

Every coverage record is authored as a single JSON object under
docs/knowledge/coverage/<id>.json. This script globs those shards, validates
their closed field set and state obligations, sorts the resulting records by
id, and emits the aggregate manifest that check-law-coverage.py continues to
read. The aggregate shape is unchanged: {schema_version, source, records}.

Run without flags to regenerate docs/law-coverage.v1.json in place. Run with
--check to fail when the aggregate differs from the derived bytes. Run with
--update as an explicit alias for the default regenerate behaviour.
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SHARD_DIR = ROOT / "docs/knowledge/coverage"
AGGREGATE = ROOT / "docs/law-coverage.v1.json"

SOURCE = {
    "path": "docs/concord-knowledge-index.v1.json",
    "description": (
        "Every record indexed as Concord law is a subject here. This manifest "
        "never decides what counts as law; check-law-coverage.py derives the "
        "subject set from the index and fails on any record the manifest omits."
    ),
}

ALLOWED_RECORD = {"id", "state", "evidence", "issue", "reason"}
ALLOWED_ANCHOR = {"kind", "value"}
ANCHOR_KINDS = {"go_test", "scenario", "validator", "generated"}

sys.path.insert(0, str(ROOT / "scripts"))
from coverage_state import (  # noqa: E402
    MAX_EVIDENCE,
    STATES,
    bounded_text,
    check_state_obligations,
    load_json,
)


def validate_anchor(anchor: object, prefix: str, findings: list[str]) -> None:
    if not isinstance(anchor, dict):
        findings.append(f"{prefix}: anchor must be an object")
        return
    unknown = set(anchor) - ALLOWED_ANCHOR
    if unknown:
        findings.append(f"{prefix}: unknown anchor fields: {sorted(unknown)}")
    kind = anchor.get("kind")
    if kind not in ANCHOR_KINDS:
        findings.append(f"{prefix}: anchor kind must be one of {sorted(ANCHOR_KINDS)}, got {kind!r}")
    value = anchor.get("value")
    if not bounded_text(value, 3, 512):
        findings.append(f"{prefix}: anchor value must be trimmed text of 3-512 characters")


def validate_shard(record: object, findings: list[str]) -> dict | None:
    if not isinstance(record, dict):
        findings.append("shard must be an object")
        return None

    identifier = record.get("id")
    if not bounded_text(identifier, 2, 128):
        findings.append(f"shard id must be trimmed text: {identifier!r}")
        return None
    prefix = f"shard {identifier}"

    unknown = set(record) - ALLOWED_RECORD
    if unknown:
        findings.append(f"{prefix}: unknown fields: {sorted(unknown)}")

    if not check_state_obligations(record, prefix, findings):
        return None

    if record.get("state") == "satisfied":
        evidence = record.get("evidence")
        if not isinstance(evidence, list) or not evidence:
            findings.append(f"{prefix}: evidence must be a non-empty array of anchors")
        elif len(evidence) > MAX_EVIDENCE:
            findings.append(f"{prefix}: evidence must carry at most {MAX_EVIDENCE} anchors")
        else:
            for position, anchor in enumerate(evidence):
                validate_anchor(anchor, f"{prefix} anchor {position}", findings)

    return record


def load_records(root: Path, findings: list[str]) -> list[dict]:
    shard_dir = root / "docs/knowledge/coverage"
    records: list[dict] = []
    if not shard_dir.is_dir():
        findings.append(f"shard directory missing: {shard_dir.relative_to(root)}")
        return records

    paths = sorted(shard_dir.glob("*.json"))
    if not paths:
        findings.append(f"no shards found in {shard_dir.relative_to(root)}")
        return records

    seen: set[str] = set()
    for path in paths:
        record = load_json(path, findings)
        if record is None:
            continue
        validated = validate_shard(record, findings)
        if validated is None:
            continue
        identifier = validated["id"]
        if identifier in seen:
            findings.append(f"duplicate shard id: {identifier}")
            continue
        seen.add(identifier)
        records.append(validated)

    records.sort(key=lambda r: r["id"])
    return records


def build_aggregate(records: list[dict]) -> dict:
    return {
        "schema_version": "1.0",
        "source": dict(SOURCE),
        "records": records,
    }


def format_aggregate(aggregate: dict) -> bytes:
    return (
        json.dumps(aggregate, ensure_ascii=False, sort_keys=False, indent=2)
        + "\n"
    ).encode("utf-8")


def derive_aggregate(root: Path, findings: list[str]) -> bytes | None:
    records = load_records(root, findings)
    if findings:
        return None
    return format_aggregate(build_aggregate(records))


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--check",
        action="store_true",
        help="verify the aggregate matches the derived bytes and exit non-zero on drift",
    )
    parser.add_argument(
        "--update",
        action="store_true",
        help="write the derived aggregate to docs/law-coverage.v1.json (default behaviour)",
    )
    parser.add_argument(
        "--root",
        type=Path,
        default=ROOT,
        help="repository root (default: the generator's repository)",
    )
    args = parser.parse_args()

    findings: list[str] = []
    derived = derive_aggregate(args.root, findings)
    if derived is None:
        for finding in findings:
            print(finding)
        print(f"law coverage generation failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1

    aggregate_path = args.root / "docs/law-coverage.v1.json"
    if args.check:
        if not aggregate_path.is_file():
            print(f"aggregate missing: {aggregate_path.relative_to(args.root)}", file=sys.stderr)
            return 1
        actual = aggregate_path.read_bytes()
        if actual != derived:
            print("law coverage aggregate drift: shards and aggregate disagree", file=sys.stderr)
            return 1
        print("law coverage aggregate is up to date")
        return 0

    aggregate_path.write_bytes(derived)
    print(f"wrote {aggregate_path.relative_to(args.root)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

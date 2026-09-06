#!/usr/bin/env python3
"""Compose the law-coverage record from its shards and keep them canonical.

The shards under docs/knowledge/coverage/ are the committed authority
(CD-0114). No aggregate file is written; readers compose the record through
scripts/knowledge_index.py.

Every coverage record is authored as a single JSON object under
docs/knowledge/coverage/<id>.json. This script globs those shards, validates
their closed field set and state obligations, sorts the resulting records by
id, and emits the aggregate manifest that check-law-coverage.py continues to
read. The aggregate shape is unchanged: {schema_version, source, records}.

"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import shard_format  # noqa: E402

ROOT = Path(__file__).resolve().parents[1]
SHARD_DIR = ROOT / "docs/knowledge/coverage"

SOURCE = {
    "path": "docs/knowledge",
    "description": (
        "Every record indexed as Concord law is a subject here. This manifest "
        "never decides what counts as law; check-law-coverage.py derives the "
        "subject set from the index and fails on any record the manifest omits."
    ),
}

ALLOWED_RECORD = {"id", "state", "evidence", "issue", "reason"}
ALLOWED_ANCHOR = {"kind", "value"}
ANCHOR_KINDS = {"go_test", "scenario", "validator", "generated", "adapter_test"}

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
    parser.add_argument("--check", action="store_true", help="fail when the shards do not compose or are not canonical")
    parser.add_argument("--update", action="store_true", help="normalise the shards to their canonical encoding")
    parser.add_argument("--root", type=Path, default=ROOT, help="repository root (default: the generator's repository)")
    args = parser.parse_args()

    findings: list[str] = []
    derived = derive_aggregate(args.root, findings)
    if derived is None:
        for finding in findings:
            print(finding)
        print(f"law coverage composition failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1

    shards = sorted((args.root / "docs/knowledge/coverage").glob("*.json"))
    if args.check:
        unformatted = shard_format.drifted(shards)
        if unformatted:
            print("coverage shard format drift: run --update to normalise", file=sys.stderr)
            for path in unformatted[:20]:
                print(f"  {path.relative_to(args.root)}", file=sys.stderr)
            return 1
        print(f"law coverage composes from {len(shards)} shard(s)")
        return 0

    for path in shard_format.normalise(shards):
        print(f"normalised {path.relative_to(args.root)}")
    print(f"law coverage composes from {len(shards)} shard(s)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

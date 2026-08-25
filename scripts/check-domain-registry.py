#!/usr/bin/env python3
"""Validate that the Domain registry describes a partition law actually uses.

scripts/check-knowledge-index.py proves the registry is schema-valid: the
domain records carry the required fields and the relation kinds obey their
conditional requirements. That is a statement about shape. It is satisfied by a
registry whose Domains own nothing, point at Domains that do not exist, and
name governing law that was superseded years ago.

This validator owns the referential propositions the schema cannot express,
because JSON Schema cannot reach across from the registry to the record set.

CD-0060 D4 is the reason it exists. "No Domain enters the registry empty. A
boundary that owns nothing is a label, and it cannot be checked against
anything." That invariant is the one the rejected six-Domain draft violated,
and nothing in the schema or the index checker would have caught it.

The checks:

  D4  every declared Domain is the home of at least one current law record
  D3  every home_domain_id and applies_to_domain_ids entry resolves, and
      applicability never restates the home, since that would imply a second
      owner where CD-0041 D3 allows exactly one
  D5  every relation target resolves, is not the Domain itself, and every
      governing_law_ids entry names a current law record
  D2  the root Domain has no parent and every child parents to the root

The subject set comes from the aggregate manifest rather than the registry
shard, so a Domain cannot escape the emptiness check by being declared only in
the shard; scripts/generate-knowledge-index.py --check owns shard-to-aggregate
agreement separately.
"""
from __future__ import annotations

import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
AGGREGATE = ROOT / "docs/concord-knowledge-index.v1.json"

def report(findings: list[str], subject: str) -> int:
    if findings:
        print(f"{subject} check failed: {len(findings)} finding(s)")
        for finding in findings:
            print(f"  {finding}")
        return 1
    print(f"{subject} check passed")
    return 0


def main() -> int:
    findings: list[str] = []
    manifest = json.loads(AGGREGATE.read_text(encoding="utf-8"))
    registry = manifest.get("domain_registry")
    if not isinstance(registry, dict):
        return report(["docs/concord-knowledge-index.v1.json: no domain_registry"], "domain registry")

    root_id = registry.get("root_domain_id")
    domains = registry.get("domains", [])
    declared = {domain.get("domain_id") for domain in domains}
    records = [record for record in manifest.get("records", []) if isinstance(record, dict)]
    current = {record.get("id") for record in records if record.get("status") == "accepted"}

    homes: dict[str, int] = {domain_id: 0 for domain_id in declared}
    rationales: dict[str, str] = {}
    for record in records:
        record_id = record.get("id")
        home = record.get("home_domain_id")
        rationale = record.get("product_wide_rationale")
        if isinstance(rationale, str):
            if rationale in rationales:
                findings.append(
                    f"record {record_id}: product_wide_rationale is identical to {rationales[rationale]!r}; "
                    "a reused sentence states nothing about this record"
                )
            else:
                rationales[rationale] = record_id
        if home is None:
            continue
        if home not in declared:
            findings.append(f"record {record_id}: home_domain_id {home!r} is not a declared Domain")
        else:
            homes[home] += 1
        applies = record.get("applies_to_domain_ids") or []
        for domain_id in applies:
            if domain_id not in declared:
                findings.append(
                    f"record {record_id}: applies_to_domain_ids names undeclared Domain {domain_id!r}"
                )
            if domain_id == home:
                findings.append(
                    f"record {record_id}: applies_to_domain_ids restates its home {home!r}; "
                    "applicability does not create another owner (CD-0041 D3)"
                )

    for domain in domains:
        domain_id = domain.get("domain_id")
        prefix = f"domain {domain_id}"

        if homes.get(domain_id, 0) == 0:
            findings.append(
                f"{prefix}: declared but owns no law record; a Domain is declared in the "
                "same change that writes its first law (CD-0060 D4)"
            )

        parent = domain.get("parent_domain_id")
        if domain_id == root_id:
            if parent is not None:
                findings.append(f"{prefix}: the root Domain must not declare a parent")
        elif parent != root_id:
            findings.append(f"{prefix}: parent_domain_id must be the root {root_id!r}, found {parent!r}")

        for position, relation in enumerate(domain.get("architecture_relations", [])):
            anchor = f"{prefix} relation {position}"
            target = relation.get("target_domain_id")
            if target not in declared:
                findings.append(f"{anchor}: target {target!r} is not a declared Domain")
            if target == domain_id:
                findings.append(f"{anchor}: a Domain cannot declare a relation to itself")
            for law_id in relation.get("governing_law_ids", []):
                if law_id not in current:
                    findings.append(
                        f"{anchor}: governing_law_ids names {law_id!r}, which is not a current law record"
                    )

    return report(findings, "domain registry")


if __name__ == "__main__":
    raise SystemExit(main())

#!/usr/bin/env python3
"""Validate that the Domain registry describes a partition law actually uses.

The store's committed law-folder read proves the registry is schema-valid at
read time: the domain records carry the required fields and the relation kinds
obey their conditional requirements. That is a statement about shape. It is
satisfied by a registry whose Domains own nothing, point at Domains that do
not exist, and name governing law that was superseded years ago.

This validator owns the referential propositions the schema cannot express,
because JSON Schema cannot reach across from the registry to the law folder.

CD-0060 D4 is the reason it exists. "No Domain enters the registry empty. A
boundary that owns nothing is a label, and it cannot be checked against
anything." That invariant is the one the rejected six-Domain draft violated,
and nothing in the schema or the committed read would have caught it.

The checks:

  D4  every declared Domain is the home of at least one current law record
  D3  every home_domain_id and applies_to_domain_ids entry resolves, and
      applicability never restates the home, since that would imply a second
      owner where CD-0222 D3 allows exactly one
  D5  every relation target resolves, is not the Domain itself, and every
      governing_law_ids entry names a current law record
  D2  the root Domain has no parent and every child has a declared, acyclic parent

The subject set comes from the current law folder under `.concord/law/` —
its closed JSON frontmatter is the authored record metadata — so a Domain
cannot escape the emptiness check by being declared only in the registry
file. Folder membership supplies law status
(law-folder-authority): every law file in the folder is a current record.
"""
from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
REGISTRY_PATH = Path(".concord/docs/knowledge/domain-registry.json")
LAW_ROOT = Path(".concord/law")

FRONT_MATTER_RE = re.compile(r"\A---\r?\n(.*?)\r?\n---\r?\n", re.DOTALL)


def report(findings: list[str], subject: str) -> int:
    if findings:
        print(f"{subject} check failed: {len(findings)} finding(s)")
        for finding in findings:
            print(f"  {finding}")
        return 1
    print(f"{subject} check passed")
    return 0


def load_registry(root: Path) -> dict | None:
    try:
        data = json.loads((root / REGISTRY_PATH).read_text(encoding="utf-8"))
    except OSError:
        return None
    except json.JSONDecodeError as exc:
        print(f"{REGISTRY_PATH.as_posix()}: invalid JSON: {exc}", file=sys.stderr)
        return None
    return data if isinstance(data, dict) else None


def load_law_records(root: Path, findings: list[str]) -> list[dict]:
    """Every law file's frontmatter as one record object.

    Folder membership supplies law status, so every file under the law root
    is a subject. A file without a closed frontmatter block cannot state its
    home and is a named finding, never a silent omission.
    """
    law_root = root / LAW_ROOT
    records: list[dict] = []
    if not law_root.is_dir():
        findings.append(f"{LAW_ROOT.as_posix()}: law folder is missing")
        return records
    for path in sorted(law_root.rglob("*.md")):
        match = FRONT_MATTER_RE.match(path.read_text(encoding="utf-8"))
        if not match:
            findings.append(
                f"{path.relative_to(root).as_posix()}: no closed --- JSON frontmatter block"
            )
            continue
        try:
            record = json.loads(match.group(1))
        except json.JSONDecodeError as exc:
            findings.append(
                f"{path.relative_to(root).as_posix()}: frontmatter is not valid JSON: {exc}"
            )
            continue
        if isinstance(record, dict):
            records.append(record)
    return records


def validate(root: Path) -> list[str]:
    findings: list[str] = []
    registry = load_registry(root)
    if registry is None:
        return [f"{REGISTRY_PATH.as_posix()}: no readable Domain registry"]

    root_id = registry.get("root_domain_id")
    domains = registry.get("domains", [])
    declared = {domain.get("domain_id") for domain in domains}
    parents = {domain.get("domain_id"): domain.get("parent_domain_id") for domain in domains}
    records = load_law_records(root, findings)
    current = {record.get("id") for record in records}

    homes: dict[str, int] = dict.fromkeys(declared, 0)
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
                    "applicability does not create another owner (CD-0222 D3)"
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
        elif parent not in declared:
            findings.append(f"{prefix}: parent_domain_id {parent!r} is not a declared Domain")
        else:
            ancestors = {domain_id}
            while parent != root_id and parent in parents:
                if parent in ancestors:
                    findings.append(f"{prefix}: parent hierarchy contains a cycle")
                    break
                ancestors.add(parent)
                parent = parents[parent]

        for position, relation in enumerate(domain.get("architecture_relations", [])):
            anchor = f"{prefix} relation {position}"
            target = relation.get("target_domain_id")
            if target not in declared:
                findings.append(f"{anchor}: target {target!r} is not a declared Domain")
            if target == domain_id:
                findings.append(f"{anchor}: a Domain cannot declare a relation to itself")
            findings.extend(
                f"{anchor}: governing_law_ids names {law_id!r}, which is not a current law record"
                for law_id in relation.get("governing_law_ids", [])
                if law_id not in current
            )

    return findings


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--root", type=Path, default=ROOT, help="repository root")
    args = parser.parse_args()

    findings = validate(args.root.resolve())
    return report(findings, "domain registry")


if __name__ == "__main__":
    raise SystemExit(main())

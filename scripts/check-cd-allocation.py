#!/usr/bin/env python3
"""Check CD allocation against a comparison manifest."""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from collections import Counter
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
MANIFEST = Path("docs/concord-knowledge-index.v1.json")
CD_ID_RE = re.compile(r"^CD-[0-9]{4}$")


class DuplicateKeyError(ValueError):
    pass


def reject_duplicate_pairs(pairs: list[tuple[str, object]]) -> dict[str, object]:
    result: dict[str, object] = {}
    for key, value in pairs:
        if key in result:
            raise DuplicateKeyError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def load_manifest(raw: bytes, source: str, findings: list[str]) -> dict[str, object] | None:
    try:
        data = json.loads(raw.decode("utf-8"), object_pairs_hook=reject_duplicate_pairs)
    except (UnicodeDecodeError, json.JSONDecodeError, DuplicateKeyError) as exc:
        findings.append(f"{source}: invalid JSON: {exc}")
        return None
    if not isinstance(data, dict):
        findings.append(f"{source}: top-level value must be an object")
        return None
    if not isinstance(data.get("records"), list):
        findings.append(f"{source}: records must be an array")
        return None
    return data


def load_tree_manifest(root: Path, findings: list[str]) -> dict[str, object] | None:
    try:
        raw = (root / MANIFEST).read_bytes()
    except OSError as exc:
        findings.append(f"{MANIFEST}: could not read manifest: {exc}")
        return None
    return load_manifest(raw, MANIFEST.as_posix(), findings)


def git_show(root: Path, ref: str) -> bytes | None:
    result = subprocess.run(
        ["git", "show", f"{ref}:{MANIFEST.as_posix()}"],
        cwd=root,
        capture_output=True,
        check=False,
    )
    return result.stdout if result.returncode == 0 else None


def load_comparison_manifest(
    root: Path, ref: str, no_fetch: bool, findings: list[str]
) -> dict[str, object] | None:
    raw = git_show(root, ref)
    if raw is None and not no_fetch:
        subprocess.run(["git", "fetch", "origin"], cwd=root, capture_output=True, check=False)
        raw = git_show(root, ref)
    if raw is None:
        if no_fetch:
            detail = "fetch it or rerun without --no-fetch"
        else:
            detail = "git fetch origin did not make it available; verify the ref and remote"
        findings.append(f"comparison ref {ref!r} is unavailable; {detail}")
        return None
    return load_manifest(raw, f"{ref}:{MANIFEST.as_posix()}", findings)


def cd_id_counts(data: dict[str, object]) -> Counter[str]:
    counts: Counter[str] = Counter()
    records = data["records"]
    assert isinstance(records, list)
    for record in records:
        if not isinstance(record, dict):
            continue
        identifier = record.get("id")
        if isinstance(identifier, str) and CD_ID_RE.fullmatch(identifier):
            counts[identifier] += 1
    return counts


def check(
    *, root: Path = ROOT, against: str = "origin/main", no_fetch: bool = False
) -> list[str]:
    findings: list[str] = []
    tree = load_tree_manifest(root, findings)
    comparison = load_comparison_manifest(root, against, no_fetch, findings)
    if tree is None or comparison is None:
        return findings

    tree_counts = cd_id_counts(tree)
    against_ids = set(cd_id_counts(comparison))
    new_ids = sorted(set(tree_counts) - against_ids)

    for identifier in new_ids:
        count = tree_counts[identifier]
        if count > 1:
            findings.append(
                f"duplicate-new: tree manifest: new CD id {identifier} appears {count} times"
            )
    for identifier in sorted(against_ids - set(tree_counts)):
        findings.append(
            f"removed-records: comparison manifest: CD id {identifier} was removed from tree; "
            "CDs are durable and removal needs an explicit superseding record"
        )
    return findings


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--against",
        default="origin/main",
        help="comparison ref containing the baseline manifest (default: origin/main)",
    )
    parser.add_argument(
        "--no-fetch",
        action="store_true",
        help="do not fetch when the comparison ref is unavailable",
    )
    args = parser.parse_args()

    findings = check(against=args.against, no_fetch=args.no_fetch)
    for finding in findings:
        print(finding)
    if findings:
        print(f"CD allocation check failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    print("CD allocation check passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

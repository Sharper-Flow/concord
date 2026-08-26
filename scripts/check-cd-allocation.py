#!/usr/bin/env python3
"""Check CD allocation against a comparison manifest and every pushed branch.

Comparing only against the comparison ref makes two branches allocating from
the same free pointer invisible to each other: each is correct until one lands,
and the merge queue is the first place the collision appears. This module also
reads the manifest at every peer ref, so a claim on an unmerged branch is
visible at the first check that runs after both branches are pushed.

Ownership is deterministic. The earliest claim on a CD id keeps it, so exactly
one branch is told to renumber and the other proceeds unchanged.
"""

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
HEADING_CD_RE = re.compile(r"^#\s+(CD-[0-9]{4})\b")
PEER_NAMESPACE = "refs/remotes/origin"


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


def git_text(root: Path, *arguments: str) -> str | None:
    result = subprocess.run(
        ["git", *arguments], cwd=root, capture_output=True, check=False, text=True
    )
    return result.stdout if result.returncode == 0 else None


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


def cd_record_paths(data: dict[str, object]) -> dict[str, str]:
    """Map each CD id to the record path claiming it.

    A CD id names one record. Two refs carrying the same id and the same path
    are one claim observed twice, not two branches racing, so the path is what
    distinguishes a collision from a duplicate observation.
    """
    paths: dict[str, str] = {}
    records = data["records"]
    assert isinstance(records, list)
    for record in records:
        if not isinstance(record, dict):
            continue
        identifier = record.get("id")
        path = record.get("path")
        if isinstance(identifier, str) and CD_ID_RE.fullmatch(identifier) and isinstance(path, str):
            paths.setdefault(identifier, path)
    return paths


def heading_findings(root: Path, data: dict[str, object]) -> list[str]:
    """Require each decision document's first heading to name its record id.

    The manifest binds a CD id to a document path, and nothing compared the
    two before: a document renumbered by filename alone kept a heading naming
    another decision. The heading is where a reader lands first, so a mismatch
    cites the wrong record. Unreadable paths stay with the knowledge-index
    checker that owns path existence.
    """
    findings: list[str] = []
    for identifier, path in sorted(cd_record_paths(data).items()):
        try:
            text = (root / path).read_text(encoding="utf-8")
        except OSError:
            continue
        heading = next((line for line in text.splitlines() if line.startswith("#")), "")
        match = HEADING_CD_RE.match(heading)
        if match is None:
            findings.append(
                f"h1-mismatch: {path}: first heading does not name a CD record id"
            )
        elif match.group(1) != identifier:
            findings.append(
                f"h1-mismatch: {path}: heading names {match.group(1)}, "
                f"manifest record id is {identifier}"
            )
    return findings


def is_ancestor(root: Path, ref: str) -> bool:
    result = subprocess.run(
        ["git", "merge-base", "--is-ancestor", ref, "HEAD"],
        cwd=root,
        capture_output=True,
        check=False,
    )
    return result.returncode == 0


def peer_refs(root: Path, namespace: str, against: str) -> list[str]:
    listing = git_text(root, "for-each-ref", "--format=%(refname)", namespace)
    if listing is None:
        return []
    against_oid = git_text(root, "rev-parse", against)
    refs: list[str] = []
    for line in listing.splitlines():
        ref = line.strip()
        if not ref or ref.endswith("/HEAD"):
            continue
        if against_oid is not None and git_text(root, "rev-parse", ref) == against_oid:
            continue
        if is_ancestor(root, ref):
            continue
        refs.append(ref)
    return refs


def claim_time(root: Path, ref: str) -> int:
    stamp = git_text(root, "log", "-1", "--format=%ct", ref, "--", MANIFEST.as_posix())
    if stamp is None or not stamp.strip():
        return 0
    return int(stamp.strip())


def local_claim_time(root: Path) -> int:
    dirty = git_text(root, "status", "--porcelain", "--", MANIFEST.as_posix())
    if dirty is None or dirty.strip():
        return sys.maxsize
    return claim_time(root, "HEAD") or sys.maxsize


def local_ref_name(root: Path) -> str:
    name = git_text(root, "rev-parse", "--abbrev-ref", "HEAD")
    return (name or "HEAD").strip()


def peer_claims(root: Path, against: str, namespace: str) -> dict[str, tuple[str, int, str]]:
    baseline: set[str] = set()
    raw = git_show(root, against)
    if raw is not None:
        parsed = load_manifest(raw, against, [])
        if parsed is not None:
            baseline = set(cd_id_counts(parsed))
    claims: dict[str, tuple[str, int, str]] = {}
    for ref in peer_refs(root, namespace, against):
        payload = git_show(root, ref)
        if payload is None:
            continue
        parsed = load_manifest(payload, ref, [])
        if parsed is None:
            continue
        when = claim_time(root, ref)
        peer_paths = cd_record_paths(parsed)
        for identifier in set(cd_id_counts(parsed)) - baseline:
            held = claims.get(identifier)
            if held is None or (when, ref) < (held[1], held[0]):
                claims[identifier] = (ref, when, peer_paths.get(identifier, ""))
    return claims


def collision_findings(
    root: Path,
    new_ids: list[str],
    claims: dict[str, tuple[str, int, str]],
    tree_paths: dict[str, str],
) -> list[str]:
    """Report a CD id two different records claim.

    Ref identity cannot decide this. One branch reaches the peer namespace
    under more than one ref — a merge queue builds a temporary ref per attempt,
    and a mirror or a rename leaves a second ref behind — and every such ref
    carries the same claim with a later timestamp than the branch it came from.
    Comparing refs alone reports that branch as colliding with itself, and
    renumbering cannot resolve it because the next push reproduces the pair at
    the new id.

    The record path decides it instead. One id claimed by one path is a single
    record however many refs carry it; one id claimed by two paths is the race
    this check exists to catch.
    """
    if not claims:
        return []
    mine = (local_claim_time(root), local_ref_name(root))
    findings: list[str] = []
    for identifier in new_ids:
        held = claims.get(identifier)
        if held is None:
            continue
        if held[2] and held[2] == tree_paths.get(identifier):
            continue
        theirs = (held[1], held[0])
        if theirs >= mine:
            continue
        findings.append(
            f"concurrent-claim: CD id {identifier} is already claimed by {held[0]}, "
            "which claimed it first; this branch is the later claimant and must renumber "
            f"(python3 scripts/renumber-cd.py {identifier} CD-XXXX)"
        )
    return findings


def highest_allocated(root: Path, against: str, namespace: str, tree_ids: set[str]) -> int:
    identifiers = set(tree_ids) | set(peer_claims(root, against, namespace))
    raw = git_show(root, against)
    if raw is not None:
        parsed = load_manifest(raw, against, [])
        if parsed is not None:
            identifiers |= set(cd_id_counts(parsed))
    numbers = [int(identifier[3:]) for identifier in identifiers if CD_ID_RE.fullmatch(identifier)]
    return max(numbers, default=0)


def check(
    *,
    root: Path = ROOT,
    against: str = "origin/main",
    no_fetch: bool = False,
    peer_namespace: str = PEER_NAMESPACE,
) -> list[str]:
    findings: list[str] = []
    tree = load_tree_manifest(root, findings)
    comparison = load_comparison_manifest(root, against, no_fetch, findings)
    if tree is not None:
        findings.extend(heading_findings(root, tree))
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
    claims = peer_claims(root, against, peer_namespace)
    findings.extend(collision_findings(root, new_ids, claims, cd_record_paths(tree)))
    return findings


def next_free(
    *,
    root: Path = ROOT,
    against: str = "origin/main",
    peer_namespace: str = PEER_NAMESPACE,
) -> str:
    tree = load_tree_manifest(root, [])
    tree_ids = set(cd_id_counts(tree)) if tree is not None else set()
    return f"CD-{highest_allocated(root, against, peer_namespace, tree_ids) + 1:04d}"


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
    parser.add_argument(
        "--peer-namespace",
        default=PEER_NAMESPACE,
        help=f"ref namespace holding peer branches (default: {PEER_NAMESPACE})",
    )
    parser.add_argument(
        "--next",
        action="store_true",
        help="print the next CD id free across the comparison ref and every pushed branch",
    )
    args = parser.parse_args()

    if args.next:
        print(next_free(against=args.against, peer_namespace=args.peer_namespace))
        return 0

    findings = check(
        against=args.against,
        no_fetch=args.no_fetch,
        peer_namespace=args.peer_namespace,
    )
    for finding in findings:
        print(finding)
    if findings:
        print(f"CD allocation check failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    print("CD allocation check passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

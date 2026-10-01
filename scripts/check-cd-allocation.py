#!/usr/bin/env python3
"""Check CD allocation against a comparison manifest and every pushed branch.

Comparing only against the comparison ref makes two branches allocating from
the same free pointer invisible to each other: each is correct until one lands,
and the merge queue is the first place the collision appears. This module also
reads the manifest at every peer ref, so a claim on an unmerged branch is
visible at the first check that runs after both branches are pushed.

Git cannot see a store reservation: a contract holds its CD id from approval
until the reservation is released, so an unmerged reservation is invisible to
every peer ref. When a reachable concord binary answers ``cd-reservations`` for this
checkout, this module folds those reservations into ``--next`` and reports two
store-backed warnings. The warnings never change the exit status, and a
missing binary, an older binary, or an unreachable store leaves the check
git-only with a notice on stderr, so CI stays deterministic.

Ownership is deterministic. The earliest claim on a CD id keeps it, so exactly
one branch is told to renumber and the other proceeds unchanged.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
from collections import Counter
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
import knowledge_index  # noqa: E402

MANIFEST = Path(knowledge_index.KNOWLEDGE_ROOT)
CD_ID_RE = re.compile(r"^CD-[0-9]{4}$")
HEADING_CD_RE = re.compile(r"^#\s+(CD-[0-9]{4})\b")
PEER_NAMESPACE = "refs/remotes/origin"
CONCORD = os.environ.get("CONCORD_BIN", "concord")
STORE_PROBE_TIMEOUT = 30


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
        data = knowledge_index.raw_manifest(root)
    except knowledge_index.ComposeError as exc:
        findings.extend(exc.findings)
        return None
    return load_manifest(json.dumps(data).encode("utf-8"), MANIFEST.as_posix(), findings)


def git_text(root: Path, *arguments: str) -> str | None:
    result = subprocess.run(
        ["git", *arguments], cwd=root, capture_output=True, check=False, text=True
    )
    return result.stdout if result.returncode == 0 else None


def git_show(root: Path, ref: str) -> bytes | None:
    """The manifest at a ref as canonical bytes, or None when the ref is
    unavailable. A ref that predates the shards carries the aggregate file."""
    if subprocess.run(["git", "rev-parse", "--verify", "--quiet", f"{ref}^{{commit}}"], cwd=root, capture_output=True, check=False).returncode != 0:
        return None
    try:
        return json.dumps(knowledge_index.raw_manifest_at(root, ref), ensure_ascii=False).encode("utf-8")
    except (subprocess.CalledProcessError, knowledge_index.ComposeError, knowledge_index.DuplicateKeyError, json.JSONDecodeError):
        return None


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
    stamp = git_text(root, "log", "-1", "--format=%ct", ref, "--", *knowledge_index.manifest_paths_at(root, ref))
    if stamp is None or not stamp.strip():
        return 0
    return int(stamp.strip())


def local_claim_time(root: Path) -> int:
    dirty = git_text(root, "status", "--porcelain", "--", knowledge_index.KNOWLEDGE_ROOT)
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


def store_reservations(root: Path) -> tuple[dict[str, object] | None, str]:
    """Ask the concord CLI for the calling checkout's law-addition
    reservations.

    Returns the parsed ``cd-reservations`` payload, or None with a reason when
    the binary is absent, too old, or the store is unreachable. The caller
    turns every None into a git-only run with a stderr notice, so a checkout
    with no Concord store behaves exactly like CI.
    """
    try:
        result = subprocess.run(
            [CONCORD, "cd-reservations"],
            input=json.dumps({"directory": str(root)}).encode("utf-8"),
            capture_output=True,
            check=False,
            timeout=STORE_PROBE_TIMEOUT,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        return None, f"the concord CLI is unavailable: {exc}"
    if result.returncode != 0:
        lines = [
            line
            for line in result.stderr.decode("utf-8", "replace").strip().splitlines()
            if line.strip()
        ]
        reason = lines[0] if lines else f"exit {result.returncode}"
        return None, reason
    try:
        payload = json.loads(result.stdout.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        return None, f"unreadable reservation payload: {exc}"
    if not isinstance(payload, dict) or not isinstance(payload.get("reservations"), list):
        return None, "reservation payload is missing its reservations array"
    try:
        reservation_rows(payload)
    except ReservationPayloadError as exc:
        return None, str(exc)
    return payload, ""


class ReservationPayloadError(ValueError):
    """A reservation row the reduction cannot trust.

    ``--next`` and the reservation warnings fold the payload wholesale, so a
    partial reservation presented as authoritative would hand out an id the
    store holds. The caller degrades the whole payload to a git-only run
    instead of keeping the readable rows.
    """


def reservation_rows(payload: dict[str, object]) -> list[tuple[str, str]]:
    """Reduce the payload to (law id, owner work id) pairs this module uses.

    A row whose law id is a string but not a CD id cannot collide with a CD
    allocation, so the reduction drops it rather than widening the check to
    non-CD laws. Any other malformed row makes the payload untrustworthy and
    raises ReservationPayloadError: keeping the readable rows would present
    partial reservations as the whole answer.
    """
    rows: list[tuple[str, str]] = []
    reservations = payload.get("reservations")
    assert isinstance(reservations, list)
    for index, row in enumerate(reservations):
        if not isinstance(row, dict):
            raise ReservationPayloadError(f"reservation row {index} is not an object")
        law_id = row.get("law_id")
        owner = row.get("owner_work_id")
        if not isinstance(law_id, str) or not isinstance(owner, str):
            raise ReservationPayloadError(
                f"reservation row {index} is missing its law id or owner work id"
            )
        if not CD_ID_RE.fullmatch(law_id):
            continue
        rows.append((law_id, owner))
    return rows


def store_findings(
    root: Path,
    payload: dict[str, object],
    against: str,
    peer_namespace: str,
) -> list[str]:
    """Report what the store knows that git cannot see.

    Two warnings, both local-only: a new tree CD id that another work
    reserves is a collision waiting for the merge queue, and a reservation of
    the work that owns this checkout that the branch does not add is the
    contract the renumber left behind. The caller prints these findings
    without counting them, so the exit status stays a git-only decision.
    """
    tree = load_tree_manifest(root, [])
    comparison = load_comparison_manifest(root, against, True, [])
    if tree is None or comparison is None:
        return []
    tree_counts = cd_id_counts(tree)
    new_ids = sorted(set(tree_counts) - set(cd_id_counts(comparison)))
    checkout = payload.get("checkout_work_id")
    checkout_work = checkout if isinstance(checkout, str) else ""
    rows = reservation_rows(payload)
    owners = dict(rows)
    warnings: list[str] = []
    for identifier in new_ids:
        owner = owners.get(identifier)
        if owner is not None and owner != checkout_work:
            warnings.append(
                f"store-reservation: CD id {identifier} is reserved by {owner}, which does not own "
                "this checkout; the branch adds it too, so one claim must renumber "
                f"(python3 scripts/renumber-cd.py {identifier} CD-XXXX)"
            )
    added = set(new_ids)
    for law_id, owner in sorted(rows):
        if checkout_work and owner == checkout_work and law_id not in added:
            warnings.append(
                f"store-reservation: CD id {law_id} is reserved by this checkout's work {owner} "
                "but the branch does not add it; check the current work pin for the admitted "
                "contract-correction route"
            )
    return warnings


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
    findings.extend(
        f"removed-records: comparison manifest: CD id {identifier} was removed from tree; "
        "CDs are durable and removal needs an explicit superseding record"
        for identifier in sorted(against_ids - set(tree_counts))
    )
    claims = peer_claims(root, against, peer_namespace)
    findings.extend(collision_findings(root, new_ids, claims, cd_record_paths(tree)))
    return findings


def next_free(
    *,
    root: Path = ROOT,
    against: str = "origin/main",
    peer_namespace: str = PEER_NAMESPACE,
    reserved: set[str] | None = None,
) -> str:
    """Return the smallest CD id above the highest git-allocated id that no
    reservation holds.

    A reservation removes only its own id from the allocation, so a sparse
    reservation leaves the gap below it allocatable: with git maximum CD-0193
    and reservation CD-0197, the next free id is CD-0194.
    """
    tree = load_tree_manifest(root, [])
    tree_ids = set(cd_id_counts(tree)) if tree is not None else set()
    taken = {identifier for identifier in (reserved or set()) if CD_ID_RE.fullmatch(identifier)}
    candidate = highest_allocated(root, against, peer_namespace, tree_ids) + 1
    while f"CD-{candidate:04d}" in taken:
        candidate += 1
    return f"CD-{candidate:04d}"


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
        help="print the next CD id free across the comparison ref, every pushed branch, and every store reservation",
    )
    args = parser.parse_args()

    payload, note = store_reservations(ROOT)
    if payload is None:
        print(f"store reservations unavailable ({note}); CD allocation stays git-only", file=sys.stderr)

    if args.next:
        reserved = {law_id for law_id, _ in reservation_rows(payload)} if payload is not None else None
        print(next_free(against=args.against, peer_namespace=args.peer_namespace, reserved=reserved))
        return 0

    if payload is not None:
        for warning in store_findings(ROOT, payload, args.against, args.peer_namespace):
            print(warning)

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

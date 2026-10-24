#!/usr/bin/env python3
"""Assert the cohort asset contains the surviving application census members.

CD-0159 D5: the frozen legislated-by-default cohort in
internal/store/knowledge_authority_defaults.v1.json must equal, member for
member, the surviving (record_id, kind) pairs from the named application tree
whose authority names the asset's vehicle contract at the asset's version.
Deleted records have no current authority membership (CD-0242 D10).
The census reads Git objects at the historical docs/knowledge/
records/ path, so it needs full history: assign it only to the
fetch-depth-0 verify-contracts job. Every production surface stays
history-independent; this script is the one history-dependent assertion.

A missing anchor is a failure, never a skip: an unreachable application
snapshot must block the cutover rather than silently passing.
"""

from __future__ import annotations

import json
import subprocess
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import knowledge_authority_defaults  # noqa: E402
import knowledge_index  # noqa: E402

# The application snapshot predates the .concord/ move, so the records live
# at the historical path inside that tree.
HISTORICAL_RECORDS_DIR = "docs/knowledge/records"


def git(root: Path, *args: str) -> str:
    result = subprocess.run(["git", *args], cwd=root, capture_output=True, text=True)
    if result.returncode != 0:
        raise RuntimeError(f"git {' '.join(args)}: {result.stderr.strip()}")
    return result.stdout


def census(root: Path, defaults: knowledge_authority_defaults.AuthorityDefaults) -> list[str]:
    """Compare the asset with surviving application records; return findings."""
    findings: list[str] = []
    try:
        git(root, "cat-file", "-t", defaults.application_sha)
    except RuntimeError as exc:
        findings.append(f"application anchor is unreachable: {exc}")
        return findings
    tree = f"{defaults.application_sha}:{HISTORICAL_RECORDS_DIR}"
    try:
        listing = git(root, "ls-tree", "--format=%(objectname) %(objecttype) %(path)", tree).splitlines()
    except RuntimeError as exc:
        findings.append(f"the application records tree is unreachable: {exc}")
        return findings
    historical: set[tuple[str, str]] = set()
    shards = 0
    for line in listing:
        objectname, objecttype, name = line.split(" ", 2)
        if objecttype != "blob":
            continue
        shards += 1
        try:
            record = json.loads(git(root, "show", f"{defaults.application_sha}:{HISTORICAL_RECORDS_DIR}/{name}"))
        except (RuntimeError, json.JSONDecodeError) as exc:
            findings.append(f"{HISTORICAL_RECORDS_DIR}/{name}: unreadable shard: {exc}")
            continue
        authority = record.get("authority") or {}
        if (
            authority.get("tier") == "legislated"
            and authority.get("legislated_by") == defaults.vehicle_work_id
            and authority.get("contract_version") == defaults.vehicle_contract_version
        ):
            historical.add((record["id"], record["kind"]))
    try:
        current = {(record["id"], record["kind"]) for record in knowledge_index.raw_manifest(root)["records"]}
    except (knowledge_index.ComposeError, KeyError, TypeError) as exc:
        return findings + [f"current knowledge records are unreadable: {exc}"]
    surviving = historical & current
    asset = set(defaults.members)
    for record_id, kind in sorted(surviving - asset):
        findings.append(f"missing cohort member: {(record_id, kind)} is vehicle-claimed in the application tree but absent from the asset")
    for record_id, kind in sorted(asset - surviving):
        findings.append(f"expanded cohort member: {(record_id, kind)} is in the asset but not a surviving vehicle-claimed application record")
    if not findings:
        print(f"census matched: {len(asset)} members across {shards} application shards at {defaults.application_sha}")
    return findings


def main() -> int:
    try:
        defaults = knowledge_authority_defaults.load()
    except knowledge_authority_defaults.AuthorityDefaultsError as exc:
        print(f"knowledge authority defaults: {exc}")
        return 1
    findings = census(knowledge_authority_defaults.REPO_ROOT, defaults)
    for finding in findings:
        print(finding)
    if findings:
        print(f"Knowledge authority census failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

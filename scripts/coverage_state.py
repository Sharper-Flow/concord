#!/usr/bin/env python3
"""The one coverage-state vocabulary, shared by every plane that declares it.

CD-0047 D2 reuses the state vocabulary already accepted in
contracts/floor-readiness.schema.json rather than inventing a second enum for
the same idea. This module is how "the same vocabulary" is guaranteed
structurally: the states and their obligations are defined once and imported,
so two planes cannot drift into two dialects. A test asserting two copies stay
equal would only detect the drift; a single definition prevents it.

The obligations are state-conditional and exclusive in both directions. A
satisfied subject must carry evidence and must not carry an issue or a reason;
an outstanding subject must carry an issue and nothing else. Forbidding the
unused fields matters as much as requiring the used one — a record carrying
both `evidence` and `reason` reads as though it were justified twice and is
really justified by neither.

Planes differ in what evidence *is*: a typed anchor for accepted law (CD-0047
D3), a computed caller for reachable mechanism (CD-0047 D4). They do not differ
in what the states mean, so only the evidence resolver is plane-specific.
"""
from __future__ import annotations

import json
import sys
from pathlib import Path

# CD-0047 D2. Identical to contracts/floor-readiness.schema.json's item states.
STATES = ("satisfied", "outstanding", "unmeasured", "out_of_scope")

# The field each state requires, and by omission the fields it forbids.
STATE_OBLIGATION = {
    "satisfied": "evidence",
    "outstanding": "issue",
    "unmeasured": "reason",
    "out_of_scope": "reason",
}

OBLIGATION_FIELDS = frozenset(STATE_OBLIGATION.values())

MAX_REASON = 1024
MAX_EVIDENCE = 32


class DuplicateKeyError(ValueError):
    """A JSON object repeated a key, so one value silently won."""


def reject_duplicate_pairs(pairs: list[tuple[str, object]]) -> dict[str, object]:
    result: dict[str, object] = {}
    for key, value in pairs:
        if key in result:
            raise DuplicateKeyError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def load_json(path: Path, findings: list[str]) -> object:
    """Parse JSON, rejecting duplicate keys rather than letting the last win."""
    try:
        return json.loads(
            path.read_text(encoding="utf-8"), object_pairs_hook=reject_duplicate_pairs
        )
    except (OSError, UnicodeDecodeError, json.JSONDecodeError, DuplicateKeyError) as exc:
        findings.append(f"{path.name}: invalid JSON: {exc}")
        return None


def bounded_text(value: object, minimum: int, maximum: int) -> bool:
    return (
        isinstance(value, str)
        and minimum <= len(value) <= maximum
        and value == value.strip()
    )


def check_state_obligations(record: dict, prefix: str, findings: list[str]) -> bool:
    """Validate a record's state and its state-conditional fields.

    Returns True when the state is a known value, so callers can skip
    plane-specific evidence resolution on a record that is already malformed.
    """
    state = record.get("state")
    if state not in STATES:
        findings.append(f"{prefix}: state must be one of {list(STATES)}, got {state!r}")
        return False

    required = STATE_OBLIGATION[state]
    if required not in record:
        findings.append(f"{prefix}: state {state!r} requires {required!r}")
    for field in OBLIGATION_FIELDS - {required}:
        if field in record:
            findings.append(f"{prefix}: state {state!r} forbids {field!r}")

    if required == "reason" and "reason" in record:
        if not bounded_text(record["reason"], 12, MAX_REASON):
            findings.append(
                f"{prefix}: reason must be trimmed text of 12-{MAX_REASON} characters"
            )
    if required == "issue" and "issue" in record:
        issue = record["issue"]
        if not isinstance(issue, int) or isinstance(issue, bool) or issue < 1:
            findings.append(f"{prefix}: issue must be a positive integer")

    return True


def check_subject_set(
    declared: list[str], discovered: list[str], noun: str, findings: list[str]
) -> None:
    """Enforce CD-0047 D1 in both directions.

    The validator derives the complete subject set rather than iterating what
    the manifest happens to list, so a subject the manifest forgets is a
    finding. A declared subject that no longer exists is equally a finding:
    stale declarations accumulate silently and widen the exception surface
    without anyone choosing to widen it.
    """
    declared_set = set(declared)
    discovered_set = set(discovered)
    if len(declared_set) != len(declared):
        duplicates = sorted({item for item in declared if declared.count(item) > 1})
        findings.append(f"duplicate {noun} declarations: {duplicates}")
    for missing in sorted(discovered_set - declared_set):
        findings.append(f"undeclared {noun}: {missing}")
    for stale in sorted(declared_set - discovered_set):
        findings.append(f"declared {noun} no longer exists: {stale}")


def report(findings: list[str], noun: str, limit: int = 200) -> int:
    for finding in findings[:limit]:
        print(finding)
    if len(findings) > limit:
        print(f"... {len(findings) - limit} additional finding(s) omitted")
    if findings:
        print(f"{noun} check failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    print(f"{noun} check passed")
    return 0

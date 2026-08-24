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

Requiring an `issue` field is only half of what `outstanding` claims. The other
half is that the issue is still open, because a record's accountability dies
with its owning issue, and a pointer at a closed one tracks nothing while
reading as though it tracks something. That liveness rule belongs to the
vocabulary rather than to any single plane, so it lives here beside the
obligation it completes. #219 closed while reachability-exceptions.v1.json
still cited it, and the plane holding a private copy of this check could not
see the plane that held none — the drift CD-0047 D3 gave evidence_anchors.py
to prevent (#451).

CI has no network guarantee, so the deterministic form reads a committed
snapshot, and scripts/update-issue-state.py is the authoritative online form
that refreshes it (#324).
"""
from __future__ import annotations

import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]

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

ISSUE_STATE = ROOT / "docs/issue-state.v1.json"
ISSUE_REPO = "Sharper-Flow/concord"
REFRESH_COMMAND = "scripts/update-issue-state.py"

# Every manifest whose outstanding records point at an issue, named by the key
# holding those records. One snapshot serves all of them. A per-plane snapshot
# would let a plane be added without its pointers being covered, which is the
# absence this registry exists to make impossible.
ISSUE_STATE_MANIFESTS = (
    (ROOT / "docs/law-coverage.v1.json", "records"),
    (ROOT / "docs/reachability-exceptions.v1.json", "exceptions"),
)


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


def load_issue_states(findings: list[str]) -> dict[str, str] | None:
    """Load the offline snapshot that backs pointer validation.

    Returns None when the snapshot is unusable, so a caller skips pointer
    checking rather than passing every record vacuously.
    """
    relative = ISSUE_STATE.relative_to(ROOT)
    if not ISSUE_STATE.is_file():
        findings.append(
            f"issue-state snapshot is missing: {relative} (run {REFRESH_COMMAND})"
        )
        return None
    try:
        document = json.loads(ISSUE_STATE.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
        findings.append(f"issue-state snapshot is unreadable: {exc}")
        return None
    if (
        not isinstance(document, dict)
        or document.get("schema_version") != "1.0"
        or not isinstance(document.get("generated_at"), str)
        or not isinstance(document.get("issues"), dict)
    ):
        findings.append(
            "issue-state snapshot must carry schema_version 1.0, generated_at, "
            "and an issues map"
        )
        return None
    states: dict[str, str] = {}
    for key, value in document["issues"].items():
        if not isinstance(key, str) or not key.isdigit() or value not in ("open", "closed"):
            findings.append(
                f"issue-state snapshot entry {key!r} must be a decimal issue number "
                "mapped to open or closed"
            )
            continue
        states[key] = value
    return states


def check_outstanding_pointer(
    record: dict,
    prefix: str,
    issue_states: dict[str, str] | None,
    findings: list[str],
) -> None:
    """An outstanding record survives only while its owning issue is live."""
    if issue_states is None:
        return
    pointer = str(record.get("issue"))
    if pointer not in issue_states:
        findings.append(
            f"{prefix}: outstanding issue {pointer} is absent from the issue-state "
            f"snapshot (run {REFRESH_COMMAND})"
        )
    elif issue_states[pointer] != "open":
        findings.append(
            f"{prefix}: outstanding issue {pointer} is {issue_states[pointer]}; "
            "an outstanding record must point at a live issue"
        )


def collect_outstanding_issues() -> list[int]:
    """Every issue an outstanding record points at, across every declared plane."""
    numbers: set[int] = set()
    for path, key in ISSUE_STATE_MANIFESTS:
        document = json.loads(path.read_text(encoding="utf-8"))
        for record in document.get(key, []):
            if (
                isinstance(record, dict)
                and record.get("state") == "outstanding"
                and isinstance(record.get("issue"), int)
            ):
                numbers.add(record["issue"])
    return sorted(numbers)


def write_issue_state(states: dict[str, str], generated_at: str) -> None:
    snapshot = {
        "schema_version": "1.0",
        "generated_at": generated_at,
        "issues": dict(sorted(states.items(), key=lambda item: int(item[0]))),
    }
    ISSUE_STATE.write_text(json.dumps(snapshot, indent=2) + "\n", encoding="utf-8")


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

#!/usr/bin/env python3
"""The one coverage-state vocabulary, shared by every plane that declares it.

CD-0249 D2 gives every declaring plane one state vocabulary rather than a
second enum for the same idea. This module is how "the same vocabulary" is guaranteed
structurally: the states and their obligations are defined once and imported,
so two planes cannot drift into two dialects. A test asserting two copies stay
equal would only detect the drift; a single definition prevents it.

The obligations are state-conditional and exclusive in both directions. A
satisfied subject must carry evidence and must not carry an issue or a reason;
an outstanding subject must carry an issue and nothing else. Forbidding the
unused fields matters as much as requiring the used one — a record carrying
both `evidence` and `reason` reads as though it were justified twice and is
really justified by neither.

Requiring an `issue` field is only half of what `outstanding` claims. The other
half is that the issue is still open, because a record's accountability dies
with its owning issue, and a pointer at a closed one tracks nothing while
reading as though it tracks something. That liveness rule belongs to the
vocabulary rather than to any single plane, so it lives here beside the
obligation it completes (CD-0249 D2).

CI has no network guarantee, so the deterministic form reads a committed
snapshot. scripts/update-issue-state.py assembles it from agent-captured Linear
MCP observations and GitHub issue reads, retaining each Linear read time.
"""
from __future__ import annotations

import json
import re
import sys
from datetime import datetime
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]

# CD-0249 D2.
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

ISSUE_STATE = ROOT / ".concord/docs/issue-state.v1.json"
ISSUE_REPO = "Sharper-Flow/concord"
REFRESH_COMMAND = "scripts/update-issue-state.py"


# Every manifest whose outstanding records point at an issue, named by the key
# holding those records. One snapshot serves all of them. A per-plane snapshot
# would let a plane be added without its pointers being covered, which is the
# absence this registry exists to make impossible.
ISSUE_STATE_MANIFESTS = (
    (ROOT / ".concord/docs/reachability-exceptions.v1.json", "exceptions"),
)

LINEAR_ISSUE = re.compile(r"^[A-Z][A-Z0-9]*-[1-9][0-9]*$")


def valid_issue_pointer(value: object) -> bool:
    """Accept legacy GitHub numbers and provider-issued Linear identifiers."""
    if isinstance(value, int) and not isinstance(value, bool):
        return value >= 1
    return isinstance(value, str) and LINEAR_ISSUE.fullmatch(value) is not None


def load_plane(path: Path) -> dict:
    """The document a registered plane composes, read as JSON."""
    return json.loads(path.read_text(encoding="utf-8"))


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
    findings.extend(
        f"{prefix}: state {state!r} forbids {field!r}"
        for field in OBLIGATION_FIELDS - {required}
        if field in record
    )

    if required == "reason" and "reason" in record and not bounded_text(record["reason"], 12, MAX_REASON):
        findings.append(
            f"{prefix}: reason must be trimmed text of 12-{MAX_REASON} characters"
        )
    if required == "issue" and "issue" in record:
        issue = record["issue"]
        if not valid_issue_pointer(issue):
            findings.append(
                f"{prefix}: issue must be a positive integer or a Linear issue identifier"
            )

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
        if not isinstance(key, str) or (
            not key.isdigit() and LINEAR_ISSUE.fullmatch(key) is None
        ) or value not in ("open", "closed"):
            findings.append(
                f"issue-state snapshot entry {key!r} must be a decimal issue number "
                "or Linear issue identifier mapped to open or closed"
            )
            continue
        states[key] = value
    read_times = document.get("linear_read_at")
    if not isinstance(read_times, dict) or set(read_times) != {key for key in states if LINEAR_ISSUE.fullmatch(key)}:
        findings.append("issue-state snapshot linear_read_at must cover exactly its Linear identifiers")
        return None
    try:
        generated = parse_utc_time(document["generated_at"])
        for identifier, read_at in read_times.items():
            if parse_utc_time(read_at) > generated:
                raise ValueError(f"read time for {identifier} is after generated_at")
    except ValueError as exc:
        findings.append(f"issue-state snapshot has invalid read metadata: {exc}")
        return None
    return states


def parse_utc_time(value: object) -> datetime:
    """A recorded observation is a UTC instant, not an issue modification time."""
    if not isinstance(value, str) or not re.fullmatch(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z", value):
        raise ValueError("expected a UTC timestamp ending in Z")
    return datetime.fromisoformat(value[:-1] + "+00:00")


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


def collect_outstanding_issues() -> list[int | str]:
    """Every tracking record an outstanding record points at, across all planes."""
    pointers: set[int | str] = set()
    for path, key in ISSUE_STATE_MANIFESTS:
        document = load_plane(path)
        for record in document.get(key, []):
            if (
                isinstance(record, dict)
                and record.get("state") == "outstanding"
                and valid_issue_pointer(record.get("issue"))
            ):
                pointers.add(record["issue"])
    return sorted(pointers, key=lambda value: (isinstance(value, str), str(value)))


def write_issue_state(states: dict[str, str], generated_at: str, linear_read_at: dict[str, str]) -> None:
    snapshot = {
        "schema_version": "1.0",
        "generated_at": generated_at,
        "linear_read_at": dict(sorted(linear_read_at.items())),
        "issues": dict(
            sorted(
                states.items(),
                key=lambda item: (
                    not item[0].isdigit(),
                    int(item[0]) if item[0].isdigit() else item[0],
                ),
            )
        ),
    }
    ISSUE_STATE.write_text(json.dumps(snapshot, indent=2) + "\n", encoding="utf-8")


def check_subject_set(
    declared: list[str], discovered: list[str], noun: str, findings: list[str]
) -> None:
    """Enforce CD-0249 D2 in both directions.

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
    findings.extend(f"undeclared {noun}: {missing}" for missing in sorted(discovered_set - declared_set))
    findings.extend(
        f"declared {noun} no longer exists: {stale}" for stale in sorted(declared_set - discovered_set)
    )


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

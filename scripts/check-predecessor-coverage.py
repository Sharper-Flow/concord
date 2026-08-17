#!/usr/bin/env python3
"""Validate Concord's authorizing predecessor operational coverage table.

The coverage document gates first-usable floor condition 6. Its authority comes
from mechanical checking rather than assertion: a covered outcome must name an
existing repository path, an excluded outcome must carry a reason, and the
stated tally must agree with the rows it summarises.
"""

from __future__ import annotations

import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DOCUMENT = ROOT / "docs/predecessor-operational-coverage.md"
FLOOR_MANIFEST = ROOT / "docs/floor-readiness.v1.json"

# Closed state vocabulary. A qualified state is deliberate and must be added
# here, so a new qualifier cannot enter the table unnoticed.
COVERED_STATES = frozenset(
    {
        "Covered",
        "Covered by composition",
        "Covered for waiting",
        "Covered in path, unverified in split",
    }
)
NOT_COVERED_STATES = frozenset({"Not covered"})
EXCLUDED_STATES = frozenset({"Excluded"})
STATES = COVERED_STATES | NOT_COVERED_STATES | EXCLUDED_STATES

SECTION = re.compile(r"^## ([1-7])\. (.+)$")
OTHER_HEADING = re.compile(r"^#{1,3} ")
TABLE_ROW = re.compile(r"^\|(.+)\|\s*$")
SEPARATOR = re.compile(r"^\|[\s:|-]+\|$")
HEADER_CELL = "Outcome"

# A backticked span or link target counts as a path candidate when it looks
# like one. Extension-less spans such as `approve_contract` are action names,
# not paths, and are skipped rather than guessed at.
BACKTICK = re.compile(r"`([^`]+)`")
LINK_TARGET = re.compile(r"\]\(([^)]+)\)")
PATH_SHAPE = re.compile(r"^[A-Za-z0-9._/-]+$")
EXTENSIONS = (".go", ".py", ".ts", ".md", ".json", ".yaml", ".yml", ".sql")

TALLY_ROW = re.compile(r"^\|\s*(Covered|Not covered|Excluded with reason)\s*\|\s*([0-9]+)\s*\|$")
TALLY_TOTAL = re.compile(r"^\*\*Total enumerated outcomes: ([0-9]+)\.\*\*$")

# This table and docs/floor-readiness.v1.json are both authorizing records, and
# rows here cite manifest items by identifier. Nothing previously compared the
# cited state against the manifest, so the two drifted silently: a row claimed
# `fc2-context-freshness` was `unmeasured` after issue #110 had measured it.
FLOOR_ITEM = re.compile(r"`(fc[1-9][0-9]*-[a-z0-9-]+)`")
FLOOR_STATES = frozenset({"satisfied", "outstanding", "unmeasured", "out_of_scope"})
FLOOR_STATE_CLAIM = re.compile(r"`(satisfied|outstanding|unmeasured|out_of_scope)`")

MIN_REASON_LENGTH = 40
MAX_ROWS = 500
MAX_FINDINGS = 200


def is_path_candidate(token: str) -> bool:
    token = token.strip()
    if not token or not PATH_SHAPE.match(token) or ".." in token:
        return False
    if token.startswith("/"):
        return False
    return "/" in token or token.endswith(EXTENSIONS)


def existing_paths(cell: str) -> list[str]:
    found: list[str] = []
    for token in BACKTICK.findall(cell) + LINK_TARGET.findall(cell):
        token = token.strip()
        if token.startswith("./"):
            token = "docs/" + token[2:]
        if not is_path_candidate(token):
            continue
        if (ROOT / token).exists():
            found.append(token)
    return found


def parse_rows(lines, findings):
    """Return (line number, section, outcome, state, evidence) for table rows."""
    rows = []
    section = ""
    for number, line in enumerate(lines, start=1):
        heading = SECTION.match(line)
        if heading:
            section = "\u00a7" + heading.group(1)
            continue
        if OTHER_HEADING.match(line):
            if not line.startswith("### "):
                section = ""
            continue
        if not section:
            continue
        match = TABLE_ROW.match(line)
        if not match or SEPARATOR.match(line):
            continue
        cells = [cell.strip() for cell in match.group(1).split("|")]
        if cells and cells[0] == HEADER_CELL:
            continue
        if len(cells) != 3:
            findings.append(f"line {number}: {section} row has {len(cells)} cells, expected 3")
            continue
        rows.append((number, section, cells[0], cells[1], cells[2]))
    return rows


def check_rows(rows, findings):
    counts = {"Covered": 0, "Not covered": 0, "Excluded with reason": 0}
    seen = {}
    for number, section, outcome, state, evidence in rows:
        if not outcome:
            findings.append(f"line {number}: {section} row has an empty outcome")
            continue
        previous = seen.get(outcome.lower())
        if previous is not None:
            findings.append(f"line {number}: outcome duplicates line {previous}: {outcome!r}")
            continue
        seen[outcome.lower()] = number

        if state not in STATES:
            findings.append(f"line {number}: unknown state {state!r} for {outcome!r}")
            continue

        if not evidence:
            findings.append(f"line {number}: {state} outcome carries no evidence or reason: {outcome!r}")
            continue

        if state in COVERED_STATES:
            counts["Covered"] += 1
            if not existing_paths(evidence):
                findings.append(
                    f"line {number}: covered outcome names no existing repository path: {outcome!r}"
                )
        elif state in EXCLUDED_STATES:
            counts["Excluded with reason"] += 1
            if len(evidence) < MIN_REASON_LENGTH:
                findings.append(
                    f"line {number}: excluded outcome reason is too short to be a reason: {outcome!r}"
                )
        else:
            counts["Not covered"] += 1
    return counts


def load_floor_states(findings):
    """Return floor-manifest item states, or None when the manifest is unusable."""
    if not FLOOR_MANIFEST.is_file():
        findings.append(f"{FLOOR_MANIFEST.name}: missing, cannot resolve cited floor items")
        return None
    try:
        manifest = json.loads(FLOOR_MANIFEST.read_text(encoding="utf-8"))
    except ValueError as exc:
        findings.append(f"{FLOOR_MANIFEST.name}: is not valid JSON: {exc}")
        return None
    states = {}
    for item in manifest.get("items", []):
        identifier, state = item.get("id"), item.get("state")
        if isinstance(identifier, str) and isinstance(state, str):
            states[identifier] = state
    if not states:
        findings.append(f"{FLOOR_MANIFEST.name}: declares no resolvable items")
        return None
    return states


def check_floor_references(rows, floor_states, findings):
    """Every cited floor item must exist, and any state it claims must match.

    A row may cite an item without naming a state. Naming one is a claim about
    another authorizing record, so it is checked rather than trusted.
    """
    for number, _section, outcome, _state, evidence in rows:
        cited = FLOOR_ITEM.findall(evidence)
        if not cited:
            continue
        claims = FLOOR_STATE_CLAIM.findall(evidence)
        if len(claims) > 1:
            findings.append(
                f"line {number}: cites {len(claims)} floor states, so the claim is ambiguous: {outcome!r}"
            )
            continue
        for identifier in cited:
            if identifier not in floor_states:
                findings.append(
                    f"line {number}: cites floor item {identifier!r}, which the manifest does not declare"
                )
                continue
            if not claims:
                continue
            if len(cited) > 1:
                findings.append(
                    f"line {number}: claims state {claims[0]!r} while citing {len(cited)} floor items: {outcome!r}"
                )
                break
            if claims[0] != floor_states[identifier]:
                findings.append(
                    f"line {number}: claims {identifier} is {claims[0]!r}, manifest records {floor_states[identifier]!r}"
                )


def check_tally(lines, counts, findings):
    stated = {}
    total = None
    for line in lines:
        row = TALLY_ROW.match(line.strip())
        if row:
            stated[row.group(1)] = int(row.group(2))
            continue
        declared = TALLY_TOTAL.match(line.strip())
        if declared:
            total = int(declared.group(1))

    for state, expected in counts.items():
        if state not in stated:
            findings.append(f"coverage tally omits {state!r}")
        elif stated[state] != expected:
            findings.append(
                f"coverage tally claims {stated[state]} {state!r}, table has {expected}"
            )

    counted = sum(counts.values())
    if total is None:
        findings.append("coverage tally declares no total")
    elif total != counted:
        findings.append(f"coverage tally declares {total} outcomes, table has {counted}")


def main():
    findings = []

    if not DOCUMENT.is_file():
        print(f"{DOCUMENT.name}: missing", file=sys.stderr)
        return 1

    lines = DOCUMENT.read_text(encoding="utf-8").split("\n")
    rows = parse_rows(lines, findings)

    if not rows:
        findings.append("coverage document declares no outcome rows")
    elif len(rows) > MAX_ROWS:
        findings.append(f"coverage document declares {len(rows)} rows, over the {MAX_ROWS} bound")
    else:
        counts = check_rows(rows, findings)
        check_tally(lines, counts, findings)
        floor_states = load_floor_states(findings)
        if floor_states is not None:
            check_floor_references(rows, floor_states, findings)

    if findings:
        for finding in findings[:MAX_FINDINGS]:
            print(f"predecessor coverage: {finding}", file=sys.stderr)
        if len(findings) > MAX_FINDINGS:
            print(f"predecessor coverage: {len(findings) - MAX_FINDINGS} further findings", file=sys.stderr)
        return 1

    print(f"Predecessor coverage validation passed ({len(rows)} outcomes)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

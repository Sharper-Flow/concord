#!/usr/bin/env python3
"""Validate the conflict-rule table against goals.md Appendix A (CD-0230 D1, D2, D6).

The table cites each rule by ID and never copies its text. This checker derives
the A1 cells, A2 rows, and rule IDs the table must hold from goals.md itself,
and refuses the table when the goals.md bytes differ from the recorded hash.

Each rule, A2 row, and duty is either enforced or a gap. Enforced entries reuse
the coverage `satisfied` shape: a mechanism kind plus executable evidence
anchors, so a prose instruction cannot count as a mechanism. A gap reuses the
`outstanding` shape: it names the live follow-up item that builds its
mechanism. Every gap is printed, so the count measures progress on goal F.
"""

from __future__ import annotations

import argparse
import hashlib
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))

from coverage_state import (  # noqa: E402
    MAX_EVIDENCE,
    check_outstanding_pointer,
    check_state_obligations,
    load_issue_states,
    load_json,
    report,
)
from evidence_anchors import check_anchor  # noqa: E402

TABLE = ROOT / "contracts/conflict-rules.v1.json"
GOALS_PATH = ".concord/docs/goals.md"
SCHEMA_VERSION = "1.0"

MECHANISMS = ("programmed_check", "recorded_decision", "operator_boundary")
ACTORS = ("agent", "coordinator", "operator", "decision_model")
SIZES = ("Small", "Larger", "Crucial")

# The rules and A2 rows whose goals.md text files follow-up items. This reads
# the goals text at the recorded hash; a goals.md edit refuses the table until
# this set and the table are checked against the new text.
FOLLOW_UP_RULES = frozenset({"R1", "P4", "P6", "P7", "P9"})
FOLLOW_UP_ROWS = frozenset({1, 5, 6})

RULE_ID = re.compile(r"\b(?:R[1-9][0-9]*|P[1-9][0-9]*|S|U)\b")
RULE_DEFINITION = re.compile(r"^- ((?:R|P)[1-9][0-9]*|S|U)\b", re.MULTILINE)
DUTY_ID = re.compile(r"^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$")

ALLOWED_ROOT = {"schema_version", "source", "rules", "a1", "a2", "duties"}
MECHANISM_FIELDS = {"state", "mechanism", "evidence", "issue"}
ALLOWED_RULE = {"id", "actors", "follow_up_label"} | MECHANISM_FIELDS
ALLOWED_ROW = {"row", "actors", "follow_up_label"} | MECHANISM_FIELDS
ALLOWED_DUTY = {"id"} | MECHANISM_FIELDS


def section(text: str, heading: str, level: str) -> str:
    """The body under a heading that starts with `heading`, up to the next heading of the same level."""
    match = re.search(rf"^{level} {re.escape(heading)}.*$", text, re.MULTILINE)
    if not match:
        return ""
    rest = text[match.end():]
    end = re.search(rf"^{level} ", rest, re.MULTILINE)
    return rest[: end.start()] if end else rest


def table_rows(text: str) -> list[list[str]]:
    """Markdown table rows as trimmed cells, without the separator row."""
    rows = []
    for line in text.splitlines():
        line = line.strip()
        if not line.startswith("|") or re.fullmatch(r"\|(?:\s*:?-+:?\s*\|)+", line):
            continue
        rows.append([cell.strip() for cell in line.strip("|").split("|")])
    return rows


def parse_appendix(goals: str) -> tuple[dict[tuple[str, str], str], dict[int, set[str]], set[str], list[str]]:
    """The A1 cells, A2 rows with the rule IDs their size cells cite, and every rule ID Appendix A names."""
    findings: list[str] = []
    appendix = section(goals, "Appendix A.", "##")
    a1_rows = table_rows(section(appendix, "A1.", "###"))
    a2_rows = table_rows(section(appendix, "A2.", "###"))
    if len(a1_rows) < 2:
        findings.append("goals.md Appendix A has no A1 chart")
    if len(a2_rows) < 2:
        findings.append("goals.md Appendix A has no A2 table")
    cells: dict[tuple[str, str], str] = {}
    if a1_rows:
        columns = a1_rows[0][1:]
        for row in a1_rows[1:]:
            name = row[0].strip("*")
            for column, value in zip(columns, row[1:]):
                if value:
                    cells[(name, column)] = value
    rows: dict[int, set[str]] = {}
    if a2_rows:
        header = a2_rows[0]
        size_columns = [header.index(size) for size in SIZES if size in header]
        if len(size_columns) != len(SIZES):
            findings.append(f"goals.md A2 table must carry the columns {list(SIZES)}")
        for row in a2_rows[1:]:
            if not row[0].isdigit():
                findings.append(f"goals.md A2 row {row[0]!r} is not numbered")
                continue
            rows[int(row[0])] = {rule for index in size_columns for rule in RULE_ID.findall(row[index])}
    rules = set(cells.values()) | set(RULE_DEFINITION.findall(appendix))
    rules.update(rule for cited in rows.values() for rule in cited)
    return cells, rows, rules, findings


def check_mechanism(entry: dict, prefix: str, issue_states: dict[str, str] | None, findings: list[str]) -> bool:
    """Validate an enforced or gap entry. Returns True for a gap."""
    state = entry.get("state")
    if state not in ("satisfied", "outstanding"):
        findings.append(f"{prefix}: state must be satisfied (a mechanism) or outstanding (a gap), got {state!r}")
        return False
    check_state_obligations(entry, prefix, findings)
    if state == "outstanding":
        if "mechanism" in entry:
            findings.append(f"{prefix}: a gap names its follow-up item, not a mechanism kind")
        check_outstanding_pointer(entry, prefix, issue_states, findings)
        return True
    if entry.get("mechanism") not in MECHANISMS:
        findings.append(f"{prefix}: mechanism must be one of {list(MECHANISMS)}, got {entry.get('mechanism')!r}")
    evidence = entry.get("evidence")
    if not isinstance(evidence, list) or not 1 <= len(evidence) <= MAX_EVIDENCE:
        findings.append(f"{prefix}: a mechanism requires 1-{MAX_EVIDENCE} evidence anchors")
    else:
        for index, anchor in enumerate(evidence):
            check_anchor(anchor, f"{prefix} evidence[{index}]", findings)
    return False


def check_entry_shape(entry: object, allowed: set[str], prefix: str, findings: list[str]) -> bool:
    if not isinstance(entry, dict):
        findings.append(f"{prefix}: must be an object")
        return False
    extra = sorted(set(entry) - allowed)
    if extra:
        findings.append(f"{prefix}: unknown field(s) {extra}")
    return True


def check_actors(entry: dict, prefix: str, findings: list[str]) -> None:
    actors = entry.get("actors")
    if (
        not isinstance(actors, list)
        or not actors
        or len(set(map(str, actors))) != len(actors)
        or any(actor not in ACTORS for actor in actors)
    ):
        findings.append(f"{prefix}: actors must be a non-empty list of distinct values from {list(ACTORS)}")


def check_label(entry: dict, prefix: str, label: str, files_follow_ups: bool, findings: list[str]) -> None:
    present = entry.get("follow_up_label")
    if files_follow_ups and present != label:
        findings.append(f"{prefix}: files follow-up items, so follow_up_label must be {label!r}, got {present!r}")
    if not files_follow_ups and present is not None:
        findings.append(f"{prefix}: files no follow-up items, so it carries no follow_up_label")


def entries(value: dict, key: str, findings: list[str]) -> list:
    items = value.get(key)
    if not isinstance(items, list):
        findings.append(f"{key} must be a list")
        return []
    return items


def validate(
    value: object,
    goals: str,
    issue_states: dict[str, str] | None,
    follow_up_rules: frozenset[str] | set[str] = FOLLOW_UP_RULES,
    follow_up_rows: frozenset[int] | set[int] = FOLLOW_UP_ROWS,
) -> tuple[list[str], list[str]]:
    """Findings, and one "<id> -> <issue>" line per gap."""
    findings: list[str] = []
    gaps: list[str] = []
    if not isinstance(value, dict):
        return ["table must be an object"], gaps
    extra = sorted(set(value) - ALLOWED_ROOT)
    if extra:
        findings.append(f"table: unknown field(s) {extra}")
    if value.get("schema_version") != SCHEMA_VERSION:
        findings.append(f"schema_version must be {SCHEMA_VERSION!r}")
    expected_hash = "sha256:" + hashlib.sha256(goals.encode("utf-8")).hexdigest()
    source = value.get("source")
    if not isinstance(source, dict) or set(source) != {"path", "sha256"} or source.get("path") != GOALS_PATH:
        findings.append(f"source must carry exactly path {GOALS_PATH!r} and sha256")
    elif source["sha256"] != expected_hash:
        findings.append(
            f"source sha256 {source['sha256']} differs from {GOALS_PATH} ({expected_hash}); "
            "check the table and this checker's follow-up sets against the new text, then record the hash"
        )

    cells, rows, rule_ids, parse_findings = parse_appendix(goals)
    findings.extend(parse_findings)

    seen_rules: set[str] = set()
    for index, rule in enumerate(entries(value, "rules", findings)):
        prefix = f"rule {rule.get('id', index) if isinstance(rule, dict) else index}"
        if not check_entry_shape(rule, ALLOWED_RULE, prefix, findings):
            continue
        identifier = rule.get("id")
        if identifier in seen_rules:
            findings.append(f"{prefix}: duplicate rule")
        seen_rules.add(identifier)
        if identifier not in rule_ids:
            findings.append(f"{prefix}: goals.md Appendix A names no such rule")
        check_actors(rule, prefix, findings)
        check_label(rule, prefix, f"rule:{identifier}", identifier in follow_up_rules, findings)
        if check_mechanism(rule, prefix, issue_states, findings):
            gaps.append(f"{identifier} -> {rule.get('issue')}")
    findings.extend(f"rule {identifier}: named by goals.md Appendix A but absent from the table" for identifier in sorted(rule_ids - seen_rules))

    seen_cells: dict[tuple[str, str], str] = {}
    for index, cell in enumerate(entries(value, "a1", findings)):
        if not check_entry_shape(cell, {"row", "column", "rule"}, f"a1[{index}]", findings):
            continue
        key = (cell.get("row"), cell.get("column"))
        if key in seen_cells:
            findings.append(f"A1 cell {key[0]}/{key[1]}: duplicate")
        seen_cells[key] = cell.get("rule")
    for key in sorted(set(cells) | set(seen_cells), key=str):
        where = f"A1 cell {key[0]}/{key[1]}"
        if key not in seen_cells:
            findings.append(f"{where}: in goals.md but absent from the table")
        elif key not in cells:
            findings.append(f"{where}: absent from goals.md")
        elif seen_cells[key] != cells[key]:
            findings.append(f"{where}: goals.md names {cells[key]}, the table names {seen_cells[key]!r}")

    seen_rows: set[int] = set()
    for index, row in enumerate(entries(value, "a2", findings)):
        number = row.get("row") if isinstance(row, dict) else None
        prefix = f"A2-{number}" if isinstance(number, int) and not isinstance(number, bool) else f"a2[{index}]"
        if not check_entry_shape(row, ALLOWED_ROW, prefix, findings):
            continue
        if number in seen_rows:
            findings.append(f"{prefix}: duplicate row")
        seen_rows.add(number)
        if number not in rows:
            findings.append(f"{prefix}: goals.md A2 has no such row")
        check_actors(row, prefix, findings)
        check_label(row, prefix, f"rule:{prefix}", number in follow_up_rows, findings)
        if check_mechanism(row, prefix, issue_states, findings):
            gaps.append(f"{prefix} -> {row.get('issue')}")
    findings.extend(f"A2 row {number}: in goals.md but absent from the table" for number in sorted(set(rows) - seen_rows))

    seen_duties: set[str] = set()
    for index, duty in enumerate(entries(value, "duties", findings)):
        prefix = f"duty {duty.get('id', index) if isinstance(duty, dict) else index}"
        if not check_entry_shape(duty, ALLOWED_DUTY, prefix, findings):
            continue
        identifier = duty.get("id")
        if not isinstance(identifier, str) or not DUTY_ID.fullmatch(identifier) or identifier in seen_duties:
            findings.append(f"{prefix}: id must be a distinct lowercase hyphenated name")
        seen_duties.add(identifier)
        if check_mechanism(duty, prefix, issue_states, findings):
            gaps.append(f"duty {identifier} -> {duty.get('issue')}")
    return findings, gaps


def main(argv: list[str] | None = None) -> int:
    argparse.ArgumentParser(description=__doc__.splitlines()[0]).parse_args(argv)
    findings: list[str] = []
    value = load_json(TABLE, findings)
    try:
        goals = (ROOT / GOALS_PATH).read_bytes().decode("utf-8")
    except (OSError, UnicodeDecodeError) as exc:
        findings.append(f"{GOALS_PATH}: unreadable: {exc}")
        goals = None
    issue_states = load_issue_states(findings)
    if value is not None and goals is not None:
        table_findings, gaps = validate(value, goals, issue_states)
        findings.extend(table_findings)
        for gap in gaps:
            print(f"gap: {gap}")
        print(f"conflict rule gaps: {len(gaps)}")
    return report(findings, "conflict rule table")


if __name__ == "__main__":
    raise SystemExit(main())

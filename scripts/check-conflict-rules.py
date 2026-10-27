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
SCHEMA_VERSION = "1.1"

MECHANISMS = ("programmed_check", "recorded_decision", "operator_boundary")
ACTORS = ("agent", "coordinator", "operator", "decision_model")
SIZES = ("Small", "Larger", "Crucial")
SAME = "Same"

# The rules and A2 rows whose goals.md text files follow-up items. This reads
# the goals text at the recorded hash; a goals.md edit refuses the table until
# this set and the table are checked against the new text.
FOLLOW_UP_RULES = frozenset({"R1", "P4", "P6", "P7", "P9"})
FOLLOW_UP_ROWS = frozenset({1, 5, 6})

RULE_ID = re.compile(r"\b(?:R[1-9][0-9]*|P[1-9][0-9]*|S|U)\b")
RULE_DEFINITION = re.compile(r"^- ((?:R|P)[1-9][0-9]*|S|U)\b", re.MULTILINE)
DUTY_ID = re.compile(r"^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$")
# CD-0230 D6: the label duty is listed until a check enforces the label.
REQUIRED_DUTIES = frozenset({"follow-up-label"})

ALLOWED_ROOT = {"schema_version", "source", "rules", "a1", "a2", "duties", "catalog_generation", "approval_points"}
MECHANISM_FIELDS = {"state", "mechanism", "evidence", "issue"}
ALLOWED_RULE = {"id", "actors", "follow_up_label"} | MECHANISM_FIELDS
ALLOWED_ROW = {"row", "sizes", "actors", "follow_up_label"} | MECHANISM_FIELDS
ALLOWED_DUTY = {"id"} | MECHANISM_FIELDS

# CD-0230 D5: the approval-point inventory labels decisions,
# it never computes consent. A row carries exactly one owner triple, a
# non-empty reachability over distinct retained contexts, and exactly one of
# a D2 class, a structural disposition, or a named implementation gap.
ALLOWED_APPROVAL = {"id", "owner", "holding_law", "reachability", "classification", "evidence"}
APPROVAL_CLASSES = frozenset({"product", "ui_ux", "production_data", "security", "increased_spending", "legal"})
PRESERVED_GATES = frozenset({
    "unrelinquished_attempt_takeover", "separately_required_dispatch_approval",
    "operator_size_override", "worktree_content_waiver", "worktree_occupancy_waiver",
    "unstarted_reclaim_override",
})
APPROVAL_REACHABILITY = frozenset({"live", "legacy_pinned", "retired", "caller_registered"})


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


def parse_appendix(goals: str) -> tuple[dict[tuple[str, str], str], dict[int, dict[str, list[str]]], set[str], list[str]]:
    """The A1 cells, the rule IDs each A2 size cell cites ("Same" cites the Small cell), and every rule ID Appendix A names."""
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
    rows: dict[int, dict[str, list[str]]] = {}
    if a2_rows:
        header = a2_rows[0]
        size_columns = [header.index(size) for size in SIZES if size in header]
        if len(size_columns) != len(SIZES):
            findings.append(f"goals.md A2 table must carry the columns {list(SIZES)}")
        for row in a2_rows[1:]:
            if not row[0].isdigit():
                findings.append(f"goals.md A2 row {row[0]!r} is not numbered")
                continue
            sizes: dict[str, list[str]] = {}
            for size, index in zip(SIZES, size_columns):
                cell = row[index] if index < len(row) else ""
                sizes[size.lower()] = sizes["small"] if cell == SAME and "small" in sizes else sorted(set(RULE_ID.findall(cell)))
            rows[int(row[0])] = sizes
    rules = set(cells.values()) | set(RULE_DEFINITION.findall(appendix))
    rules.update(rule for sizes in rows.values() for cited in sizes.values() for rule in cited)
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
        or any(not isinstance(actor, str) or actor not in ACTORS for actor in actors)
        or len(set(actors)) != len(actors)
    ):
        findings.append(f"{prefix}: actors must be a non-empty list of distinct values from {list(ACTORS)}")


def check_label(entry: dict, prefix: str, label: str, files_follow_ups: bool, findings: list[str]) -> None:
    present = entry.get("follow_up_label")
    if files_follow_ups and present != label:
        findings.append(f"{prefix}: files follow-up items, so follow_up_label must be {label!r}, got {present!r}")
    if not files_follow_ups and present is not None:
        findings.append(f"{prefix}: files no follow-up items, so it carries no follow_up_label")


def check_classification_shape(classification: object, prefix: str, findings: list[str]) -> str | None:
    """Validate a closed disposition, never infer an instance's approval class."""
    if not isinstance(classification, dict):
        findings.append(f"{prefix}: classification must be a D2 class, structural disposition, or named gap")
        return None
    if "class" in classification and "gap_issue" in classification:
        findings.append(f"{prefix}: classification carries both a class and a gap_issue")
        return None
    if "class" in classification:
        class_value = classification["class"]
        if (
            set(classification) != {"class"}
            or not isinstance(class_value, str)
            or class_value not in APPROVAL_CLASSES
        ):
            findings.append(
                f"{prefix}: class must be exactly one of {sorted(APPROVAL_CLASSES)}, got {class_value!r}"
            )
        return None
    if "gap_issue" in classification:
        gap_issue = classification.get("gap_issue")
        if (
            set(classification) != {"gap_issue", "reason"}
            or not isinstance(gap_issue, str)
            or not re.fullmatch(r"CON-[1-9][0-9]*", gap_issue)
        ):
            findings.append(f"{prefix}: a gap names gap_issue CON-n and a non-blank reason, got {classification!r}")
            return None
        if not isinstance(classification.get("reason"), str) or not classification["reason"].strip():
            findings.append(f"{prefix}: a gap carries a non-blank reason")
        return gap_issue
    if "classes" in classification:
        classes = classification["classes"]
        if (set(classification) != {"classes"} or not isinstance(classes, list) or not classes
                or any(not isinstance(item, str) or item not in APPROVAL_CLASSES for item in classes)
                or len(set(classes)) != len(classes)):
            findings.append(f"{prefix}: classes must be a non-empty distinct list of the six D2 classes")
        return None
    for disposition in ("agent_owned", "preserved_gate", "legacy"):
        if disposition not in classification:
            continue
        value = classification[disposition]
        valid = (isinstance(value, str) and value in PRESERVED_GATES) if disposition == "preserved_gate" else value is True
        if (set(classification) != {disposition, "reason"} or not valid
                or not isinstance(classification.get("reason"), str) or not classification["reason"].strip()):
            findings.append(f"{prefix}: {disposition} must carry its exact disposition and a non-blank reason")
        return None
    findings.append(f"{prefix}: classification is neither a D2 class, structural disposition, nor a named gap")
    return None


def check_approval_points(
    value: dict,
    issue_states: dict[str, str] | None,
    findings: list[str],
    gaps: list[str],
) -> None:
    """The operator approval inventory: one row per owning source site."""
    points = value.get("approval_points")
    if not isinstance(points, list) or not points:
        findings.append("approval_points must be a non-empty list of inventory rows")
        return
    seen: set[str] = set()
    for index, point in enumerate(points):
        prefix = f"approval-point {point.get('id', index) if isinstance(point, dict) else index}"
        if not check_entry_shape(point, ALLOWED_APPROVAL, prefix, findings):
            continue
        identifier = point.get("id")
        if not isinstance(identifier, str) or not identifier.strip():
            findings.append(f"{prefix}: id must be a non-blank string")
            continue
        if identifier in seen:
            findings.append(f"{prefix}: duplicate approval point")
        seen.add(identifier)
        owner = point.get("owner")
        if not isinstance(owner, dict) or set(owner) != {"path", "symbol", "branch"}:
            findings.append(f"{prefix}: owner must carry exactly path, symbol, and branch")
        else:
            for field in ("path", "symbol", "branch"):
                text = owner.get(field)
                if not isinstance(text, str) or not text.strip() or "*" in text:
                    findings.append(f"{prefix}: owner {field} must be a non-blank exact name, got {text!r}")
            owner_path = owner.get("path")
            resolved = ROOT / str(owner_path) if isinstance(owner_path, str) and owner_path.strip() else None
            if resolved is None or not resolved.is_file():
                findings.append(f"{prefix}: owner path {owner_path!r} does not exist in the repository")
        gap_issue = check_classification_shape(point.get("classification"), prefix, findings)
        holding = point.get("holding_law")
        if holding is None:
            # A null holding record is honest only on an explicit gap whose
            # reason names the unresolved holder and whose evidence links the
            # actual source consent declaration (table-only shape refinement).
            classification = point.get("classification")
            if not isinstance(classification, dict) or not isinstance(classification.get("gap_issue"), str):
                findings.append(f"{prefix}: holding_law may be null only on an explicit gap row")
            classification = point.get("classification")
            reason = classification.get("reason") if isinstance(classification, dict) else None
            if not isinstance(reason, str) or "unresolved holder" not in reason:
                findings.append(f"{prefix}: a null holding_law gap must name the unresolved holder in its reason")
            if not isinstance(point.get("evidence"), list) or not point.get("evidence"):
                findings.append(
                    f"{prefix}: a null holding_law gap carries evidence linking the actual source consent declaration"
                )
        elif isinstance(holding, str) and holding.strip():
            match = re.fullmatch(r"(CD-[0-9]{4})(?: D[0-9]+)?", holding.strip())
            decision_file = ROOT / ".concord/docs/decisions"
            resolved = [path for path in decision_file.glob(match.group(1) + "-*.md")] if match else []
            if not resolved:
                findings.append(
                    f"{prefix}: holding_law {holding!r} resolves to no decision record under .concord/docs/decisions/"
                )
        else:
            findings.append(f"{prefix}: holding_law must name the law or decision that holds the consent")
        reachability = point.get("reachability")
        if (
            not isinstance(reachability, list)
            or not reachability
            or any(not isinstance(item, str) or item not in APPROVAL_REACHABILITY for item in reachability)
            or len(set(reachability)) != len(reachability)
        ):
            findings.append(
                f"{prefix}: reachability must be a non-empty list of distinct values from {sorted(APPROVAL_REACHABILITY)}"
            )
        evidence = point.get("evidence")
        if evidence is not None:
            if (
                not isinstance(evidence, list)
                or not 1 <= len(evidence) <= 8
                or any(not isinstance(item, str) or not item.strip() or "*" in item for item in evidence)
            ):
                findings.append(f"{prefix}: evidence must be 1-8 non-blank citations without wildcards")
        if gap_issue is not None:
            check_outstanding_pointer({"issue": gap_issue}, prefix, issue_states, findings)
            gaps.append(f"approval-point {identifier} -> {gap_issue}")


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
    generation = value.get("catalog_generation")
    if not isinstance(generation, int) or isinstance(generation, bool) or generation < 1:
        findings.append("catalog_generation must be a positive integer per-revision identity")
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
        if not isinstance(identifier, str):
            findings.append(f"{prefix}: id must be a string")
            continue
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
        if not all(isinstance(part, str) for part in key):
            findings.append(f"a1[{index}]: row and column must be strings")
            continue
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
        if not prefix.startswith("A2-"):
            findings.append(f"{prefix}: row must be an integer")
            continue
        if number in seen_rows:
            findings.append(f"{prefix}: duplicate row")
        seen_rows.add(number)
        if number not in rows:
            findings.append(f"{prefix}: goals.md A2 has no such row")
        elif row.get("sizes") != rows[number]:
            findings.append(f"{prefix}: sizes must cite the rule IDs of each goals.md size cell, {rows[number]}, got {row.get('sizes')!r}")
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
            continue
        seen_duties.add(identifier)
        if check_mechanism(duty, prefix, issue_states, findings):
            gaps.append(f"duty {identifier} -> {duty.get('issue')}")
    findings.extend(f"duty {identifier}: required but absent from the table" for identifier in sorted(REQUIRED_DUTIES - seen_duties))
    check_approval_points(value, issue_states, findings, gaps)
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

#!/usr/bin/env python3
"""Focused tests for the predecessor operational coverage checker."""

from __future__ import annotations

import importlib.util
from pathlib import Path

SCRIPT = Path(__file__).with_name("check-predecessor-coverage.py")
SPEC = importlib.util.spec_from_file_location("coverage_checker", SCRIPT)
assert SPEC and SPEC.loader
checker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(checker)


def document(*rows: str) -> list[str]:
    body = [
        "## 1. Territory",
        "",
        "| Outcome | State | Evidence or reason |",
        "|---|---|---|",
    ]
    body.extend(rows)
    return body


def tally(covered: int, not_covered: int, excluded: int, total: int) -> list[str]:
    return [
        "## Coverage tally",
        "",
        "| State | Count |",
        "|---|---|",
        f"| Covered | {covered} |",
        f"| Not covered | {not_covered} |",
        f"| Excluded with reason | {excluded} |",
        "",
        f"**Total enumerated outcomes: {total}.**",
    ]


COVERED = "| An outcome | Covered | `scripts/check-predecessor-coverage.py` |"
EXCLUDED = (
    "| Another outcome | Excluded | A deliberate exclusion carrying a reason long "
    "enough to be a real reason. |"
)


def run(lines: list[str]) -> list[str]:
    findings: list[str] = []
    rows = checker.parse_rows(lines, findings)
    counts = checker.check_rows(rows, findings)
    checker.check_tally(lines, counts, findings)
    return findings


def test_real_document_passes() -> None:
    assert checker.main() == 0


def test_minimal_document_is_accepted() -> None:
    assert run(document(COVERED, EXCLUDED) + tally(1, 0, 1, 2)) == []


def test_covered_outcome_requires_an_existing_path() -> None:
    row = "| An outcome | Covered | `scripts/does-not-exist.py` |"
    findings = run(document(row) + tally(1, 0, 0, 1))
    assert any("names no existing repository path" in f for f in findings), findings


def test_covered_outcome_rejects_tool_identity_as_evidence() -> None:
    row = "| An outcome | Covered | `concord_work_define.capture` |"
    findings = run(document(row) + tally(1, 0, 0, 1))
    assert any("names no existing repository path" in f for f in findings), findings


def test_excluded_outcome_requires_a_reason() -> None:
    findings = run(document("| An outcome | Excluded | Too short. |") + tally(0, 0, 1, 1))
    assert any("too short to be a reason" in f for f in findings), findings


def test_unknown_state_is_rejected() -> None:
    findings = run(document("| An outcome | Probably fine | `docs/priorities.md` |") + tally(0, 0, 0, 0))
    assert any("unknown state" in f for f in findings), findings


def test_qualified_covered_states_are_closed() -> None:
    assert "Covered by composition" in checker.COVERED_STATES
    assert "Covered when convenient" not in checker.STATES


def test_empty_evidence_is_rejected() -> None:
    findings = run(document("| An outcome | Covered |  |") + tally(1, 0, 0, 1))
    assert any("carries no evidence or reason" in f for f in findings), findings


def test_duplicate_outcome_is_rejected() -> None:
    findings = run(document(COVERED, COVERED) + tally(2, 0, 0, 2))
    assert any("duplicates line" in f for f in findings), findings


def test_tally_must_match_counted_rows() -> None:
    findings = run(document(COVERED, EXCLUDED) + tally(9, 0, 1, 10))
    assert any("tally claims 9 'Covered', table has 1" in f for f in findings), findings


def test_declared_total_must_match_counted_rows() -> None:
    findings = run(document(COVERED, EXCLUDED) + tally(1, 0, 1, 99))
    assert any("declares 99 outcomes, table has 2" in f for f in findings), findings


def test_malformed_row_is_reported() -> None:
    findings: list[str] = []
    checker.parse_rows(document("| Two | Cells |"), findings)
    assert any("expected 3" in f for f in findings), findings


def test_row_missing_trailing_pipe_is_not_silently_dropped() -> None:
    # A row without its closing pipe renders wrong and must not vanish from the
    # count; the tally cross-check is what surfaces it.
    findings = run(document(COVERED, "| Lost | Covered | `docs/priorities.md`") + tally(2, 0, 0, 2))
    assert any("table has 1" in f for f in findings), findings


def test_rows_outside_numbered_sections_are_ignored() -> None:
    lines = ["## Coverage states", "", "| State | Meaning |", "|---|---|", "| Covered | Something. |"]
    findings: list[str] = []
    assert checker.parse_rows(lines, findings) == []
    assert findings == []


def test_path_candidates_exclude_traversal_and_absolute_paths() -> None:
    assert not checker.is_path_candidate("../secrets.txt")
    assert not checker.is_path_candidate("/etc/passwd")
    assert not checker.is_path_candidate("approve_contract")
    assert checker.is_path_candidate("internal/store/store.go")
    assert checker.is_path_candidate("AGENTS.md")


def test_relative_doc_links_resolve() -> None:
    assert checker.existing_paths("[`priorities.md`](./priorities.md)") == ["docs/priorities.md"]


def main() -> int:
    tests = [value for name, value in sorted(globals().items()) if name.startswith("test_") and callable(value)]
    for test in tests:
        test()
    print(f"predecessor coverage checker tests passed: {len(tests)} test(s)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

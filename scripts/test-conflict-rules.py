#!/usr/bin/env python3
"""Focused tests for the conflict-rule table checker."""

from __future__ import annotations

import copy
import hashlib
import importlib.util
from pathlib import Path

SCRIPT = Path(__file__).with_name("check-conflict-rules.py")
SPEC = importlib.util.spec_from_file_location("conflict_rules_checker", SCRIPT)
assert SPEC and SPEC.loader
checker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(checker)

# A synthetic Appendix A in the shape goals.md uses: one A1 chart and one A2
# table. The checker derives the cells and rows it requires from this text.
GOALS = """# Goals

## Appendix A. Conflict rules

### A1. Goal-by-goal chart

|  | Arch | Speed |
|---|---|---|
| **Law** | P1 | R3 |
| **Arch** |  | S |

### A2. Conflict situations by change size

| # | Conflict | Small | Larger | Crucial | Applied by |
|---|---|---|---|---|---|
| 1 | A fix meets bloat | R1 | Same | Same | Agent |
| 2 | A change breaks a product law | R3, with P12 for "serious" | Same | Always consult the operator | Agent; operator if serious |

## Appendix B. Other
"""

GOALS_HASH = "sha256:" + hashlib.sha256(GOALS.encode("utf-8")).hexdigest()
ISSUES = {"CON-9001": "open", "CON-9002": "closed"}
ANCHOR = {"kind": "validator", "value": "scripts/check-json.py"}


def gap(issue: str = "CON-9001") -> dict:
    return {"state": "outstanding", "issue": issue}


def table() -> dict:
    return {
        "schema_version": "1.0",
        "source": {"path": ".concord/docs/goals.md", "sha256": GOALS_HASH},
        "rules": [
            {"id": "P1", "actors": ["agent"], **gap()},
            {"id": "P12", "actors": ["agent"], **gap()},
            {"id": "R1", "actors": ["agent"], "follow_up_label": "rule:R1", **gap()},
            {"id": "R3", "actors": ["agent", "operator"], **gap()},
            {"id": "S", "actors": ["agent"], "state": "satisfied", "mechanism": "programmed_check", "evidence": [dict(ANCHOR)]},
        ],
        "a1": [
            {"row": "Law", "column": "Arch", "rule": "P1"},
            {"row": "Law", "column": "Speed", "rule": "R3"},
            {"row": "Arch", "column": "Speed", "rule": "S"},
        ],
        "a2": [
            {"row": 1, "actors": ["agent"], "follow_up_label": "rule:A2-1", **gap()},
            {"row": 2, "actors": ["agent", "operator"], **gap()},
        ],
        "duties": [
            {"id": "follow-up-label", **gap()},
        ],
    }


def validate(value: dict, goals: str = GOALS, issues: dict | None = None):
    return checker.validate(value, goals, ISSUES if issues is None else issues, follow_up_rules={"R1"}, follow_up_rows={1})


def test_valid_table_has_no_findings() -> None:
    findings, gaps = validate(table())
    assert findings == [], findings
    assert len(gaps) == 7, gaps


def test_missing_a1_cell_is_refused() -> None:
    value = table()
    value["a1"] = [cell for cell in value["a1"] if cell["column"] != "Speed" or cell["row"] != "Law"]
    findings, _ = validate(value)
    assert any("A1 cell Law/Speed" in finding for finding in findings), findings


def test_a1_cell_naming_a_different_rule_is_refused() -> None:
    value = table()
    value["a1"][0]["rule"] = "R3"
    findings, _ = validate(value)
    assert any("A1 cell Law/Arch" in finding for finding in findings), findings


def test_missing_a2_row_is_refused() -> None:
    value = table()
    value["a2"] = value["a2"][:1]
    findings, _ = validate(value)
    assert any("A2 row 2" in finding for finding in findings), findings


def test_rule_named_by_goals_but_absent_from_table_is_refused() -> None:
    value = table()
    value["rules"] = [rule for rule in value["rules"] if rule["id"] != "P12"]
    findings, _ = validate(value)
    assert any("rule P12" in finding for finding in findings), findings


def test_rule_with_no_mechanism_kind_is_refused() -> None:
    value = table()
    del value["rules"][4]["mechanism"]
    findings, _ = validate(value)
    assert any("S" in finding and "mechanism" in finding for finding in findings), findings
    value = table()
    value["rules"][4]["mechanism"] = "prose_instruction"
    findings, _ = validate(value)
    assert any("mechanism" in finding for finding in findings), findings


def test_mechanism_without_resolving_evidence_is_refused() -> None:
    value = table()
    value["rules"][4]["evidence"] = []
    findings, _ = validate(value)
    assert findings, "a mechanism with no evidence anchor must be refused"


def test_gap_must_name_a_live_follow_up_item() -> None:
    value = table()
    del value["rules"][0]["issue"]
    assert validate(value)[0], "a gap with no follow-up item must be refused"
    value = table()
    value["rules"][0]["issue"] = "CON-9002"
    findings, _ = validate(value)
    assert any("closed" in finding for finding in findings), findings


def test_gap_may_not_carry_a_mechanism_kind() -> None:
    value = table()
    value["rules"][0]["mechanism"] = "programmed_check"
    assert validate(value)[0], "a gap row names no mechanism kind"


def test_stale_goals_hash_is_refused() -> None:
    findings, _ = validate(table(), GOALS + "\nedited\n")
    assert any("sha256" in finding for finding in findings), findings


def test_every_gap_is_reported() -> None:
    _, gaps = validate(table())
    assert "P1 -> CON-9001" in gaps and "A2-2 -> CON-9001" in gaps and "duty follow-up-label -> CON-9001" in gaps, gaps


def test_rule_that_files_follow_ups_must_name_its_label() -> None:
    value = table()
    del value["rules"][2]["follow_up_label"]
    findings, _ = validate(value)
    assert any("R1" in finding and "follow_up_label" in finding for finding in findings), findings
    value = table()
    del value["a2"][0]["follow_up_label"]
    findings, _ = validate(value)
    assert any("A2-1" in finding and "follow_up_label" in finding for finding in findings), findings
    value = table()
    value["rules"][2]["follow_up_label"] = "tech-debt"
    findings, _ = validate(value)
    assert any("rule:R1" in finding for finding in findings), findings


def test_label_on_a_rule_that_files_no_follow_ups_is_refused() -> None:
    value = table()
    value["rules"][0]["follow_up_label"] = "rule:P1"
    assert validate(value)[0], "only a rule that files follow-up items names a label"


def test_unknown_actor_is_refused() -> None:
    value = table()
    value["rules"][0]["actors"] = ["reviewer"]
    assert validate(value)[0]


def test_repository_table_passes() -> None:
    assert checker.main([]) == 0


def main() -> int:
    tests = [value for name, value in sorted(globals().items()) if name.startswith("test_") and callable(value)]
    for test in tests:
        test()
    print(f"conflict rule checker tests passed: {len(tests)} test(s)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

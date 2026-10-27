#!/usr/bin/env python3
"""Focused tests for the conflict-rule table checker."""

from __future__ import annotations

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
        "schema_version": "1.1",
        "catalog_generation": 1,
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
            {"row": 1, "sizes": {"small": ["R1"], "larger": ["R1"], "crucial": ["R1"]}, "actors": ["agent"], "follow_up_label": "rule:A2-1", **gap()},
            {"row": 2, "sizes": {"small": ["P12", "R3"], "larger": ["P12", "R3"], "crucial": []}, "actors": ["agent", "operator"], **gap()},
        ],
        # A class-shaped point adds no gap row, so every existing expectation
        # below keeps its exact counts.
        "approval_points": [
            {
                "id": "registry-approve-contract",
                "owner": {"path": "internal/store/workflow_registry.go", "symbol": "builtinActionPolicies", "branch": "approve_contract"},
                "holding_law": "CD-0033",
                "reachability": ["live", "legacy_pinned", "retired"],
                "classification": {"class": "product"},
                "evidence": ["internal/store/workflow_registry.go:1758"],
            },
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


def test_a2_sizes_must_cite_each_goals_size_cell() -> None:
    value = table()
    value["a2"][1]["sizes"]["crucial"] = ["R3"]
    findings, _ = validate(value)
    assert any("A2-2" in finding and "must cite" in finding for finding in findings), findings
    value = table()
    del value["a2"][0]["sizes"]
    findings, _ = validate(value)
    assert any("A2-1" in finding and "must cite" in finding for finding in findings), findings


def test_a2_row_must_be_an_integer() -> None:
    value = table()
    value["a2"][0]["row"] = True
    findings, _ = validate(value)
    assert any("row must be an integer" in finding for finding in findings), findings


def test_label_duty_must_be_listed() -> None:
    value = table()
    value["duties"] = []
    findings, _ = validate(value)
    assert any("follow-up-label" in finding for finding in findings), findings


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


def test_recorded_review_uses_existing_evidence_anchors_for_every_entry_kind() -> None:
    for key in ("rules", "a2", "duties"):
        value = table()
        entry = value[key][0]
        entry.pop("issue")
        entry.update(state="satisfied", mechanism="recorded_review", evidence=[dict(ANCHOR)])
        findings, _ = validate(value)
        assert findings == [], (key, findings)


def test_recorded_review_requires_resolving_evidence_not_prose() -> None:
    for evidence in (None, [], [{"kind": "validator", "value": "scripts/check-no-such-review.py"}],
                     [{"kind": "prose", "value": "review applied S"}]):
        value = table()
        entry = value["rules"][4]
        entry["mechanism"] = "recorded_review"
        if evidence is None:
            del entry["evidence"]
        else:
            entry["evidence"] = evidence
        findings, _ = validate(value)
        assert any("evidence" in finding for finding in findings), findings
        assert not any("mechanism must be" in finding for finding in findings), findings


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


# approval_points negatives: closed owner triples, distinct ids, exactly one
# of a D5 class or a named gap, a resolvable holding record, and a closed
# reachability. Each test asserts the specific refusal.
APPROVAL_OWNER = {"path": "internal/store/workflow_registry.go", "symbol": "builtinActionPolicies", "branch": "approve_contract"}


def approval_point(**overrides) -> dict:
    point = {
        "id": "contract-approval-registry",
        "owner": dict(APPROVAL_OWNER),
        "holding_law": "CD-0033",
        # One owner serves many definition contexts, so reachability is a
        # non-empty list over the closed vocabulary.
        "reachability": ["live"],
        "classification": {"class": "product"},
    }
    point.update(overrides)
    return point


def approval_points_table(points=None) -> dict:
    value = table()
    value["approval_points"] = [approval_point()] if points is None else points
    return value


def test_approval_points_table_has_no_findings() -> None:
    findings, _ = validate(approval_points_table())
    assert findings == [], findings


def test_approval_point_with_omitted_owner_field_is_refused() -> None:
    value = approval_points_table()
    del value["approval_points"][0]["owner"]["symbol"]
    findings, _ = validate(value)
    assert any("owner" in finding and "symbol" in finding for finding in findings), findings


def test_duplicate_approval_point_id_is_refused() -> None:
    value = approval_points_table(points=[approval_point(), approval_point(id="contract-approval-registry", owner={**APPROVAL_OWNER, "branch": "confirm_premise"})])
    findings, _ = validate(value)
    assert any("duplicate" in finding for finding in findings), findings

def test_unhashable_actor_is_reported_not_raised() -> None:
    value = table()
    value["rules"][0]["actors"] = [["agent"]]
    findings, _ = validate(value)
    assert any("actors must be a non-empty list" in finding for finding in findings), findings


def test_unknown_holding_record_is_refused() -> None:
    value = approval_points_table()
    value["approval_points"][0]["holding_law"] = "CD-9999 D1"
    findings, _ = validate(value)
    assert any("resolves to no decision record" in finding for finding in findings), findings
    value = approval_points_table()
    value["approval_points"][0]["holding_law"] = "goals prose"
    findings, _ = validate(value)
    assert any("resolves to no decision record" in finding for finding in findings), findings


def test_null_holding_is_accepted_only_on_reasoned_gaps_with_evidence() -> None:
    value = approval_points_table()
    value["approval_points"][0]["classification"] = {"gap_issue": "CON-9001", "reason": "unresolved holder; the source consent declaration is cited in evidence"}
    value["approval_points"][0]["holding_law"] = None
    value["approval_points"][0]["evidence"] = ["internal/agent/mutations.go:1442"]
    findings, _ = validate(value)
    assert findings == [], findings


def test_null_holding_with_non_object_classification_is_refused_not_raised() -> None:
    value = approval_points_table()
    value["approval_points"][0]["holding_law"] = None
    value["approval_points"][0]["classification"] = ["gap_issue"]
    findings, _ = validate(value)
    assert any("holding_law may be null only on an explicit gap row" in finding for finding in findings), findings


def test_non_object_classification_with_a_holder_is_refused_normally() -> None:
    value = approval_points_table()
    value["approval_points"][0]["classification"] = ["gap_issue"]
    findings, _ = validate(value)
    assert any("classification must be a D2 class" in finding for finding in findings), findings


def test_null_holding_on_a_class_row_is_refused() -> None:
    value = approval_points_table()
    value["approval_points"][0]["holding_law"] = None
    findings, _ = validate(value)
    assert any("holding_law may be null only on an explicit gap row" in finding for finding in findings), findings


def test_null_holding_without_an_unresolved_holder_reason_is_refused() -> None:
    value = approval_points_table()
    value["approval_points"][0]["classification"] = {"gap_issue": "CON-9001", "reason": "consent retained"}
    value["approval_points"][0]["holding_law"] = None
    findings, _ = validate(value)
    assert any("unresolved holder" in finding for finding in findings), findings
    value = approval_points_table()
    value["approval_points"][0]["classification"] = {"gap_issue": "CON-9001", "reason": "unresolved holder; the source consent declaration is cited in evidence"}
    value["approval_points"][0]["holding_law"] = None
    findings, _ = validate(value)
    assert any("evidence" in finding for finding in findings), findings


def test_wildcard_evidence_is_refused() -> None:
    value = approval_points_table()
    value["approval_points"][0]["evidence"] = ["internal/agent/*"]
    findings, _ = validate(value)
    assert any("evidence" in finding for finding in findings), findings


def test_non_string_class_is_reported_not_raised() -> None:
    value = approval_points_table()
    value["approval_points"][0]["classification"] = {"class": []}
    findings, _ = validate(value)
    assert any("class must be exactly one of" in finding for finding in findings), findings


def test_non_string_reachability_is_reported_not_raised() -> None:
    value = approval_points_table()
    value["approval_points"][0]["reachability"] = [["live"]]
    findings, _ = validate(value)
    assert any("reachability must be a non-empty list" in finding for finding in findings), findings


def test_catalog_generation_must_be_a_positive_identity() -> None:
    value = approval_points_table()
    del value["catalog_generation"]
    findings, _ = validate(value)
    assert any("catalog_generation" in finding for finding in findings), findings
    value = approval_points_table()
    value["catalog_generation"] = 0
    findings, _ = validate(value)
    assert any("catalog_generation" in finding for finding in findings), findings


def test_seventh_approval_class_is_refused() -> None:
    value = approval_points_table()
    value["approval_points"][0]["classification"] = {"class": "convenience"}
    findings, _ = validate(value)
    assert any("convenience" in finding for finding in findings), findings


def test_six_classes_and_structural_dispositions() -> None:
    classes = ["product", "ui_ux", "production_data", "security", "increased_spending", "legal"]
    classifications = [{"class": name} for name in classes] + [
        {"classes": classes},
        {"agent_owned": True, "reason": "ordinary coordination within scope"},
        {"preserved_gate": "worktree_content_waiver", "reason": "explicit operator-only waiver"},
        {"legacy": True, "reason": "released metadata, not a new approval class"},
    ]
    for classification in classifications:
        value = approval_points_table()
        value["approval_points"][0]["classification"] = classification
        assert validate(value)[0] == [], classification


def test_structural_dispositions_refuse_unknown_mixed_or_empty_shapes() -> None:
    for classification in [
        {"classes": []}, {"classes": ["product", "product"]},
        {"classes": ["serious_law"]}, {"class": "product_preference"},
        {"class": "destructive_verified_recovery"},
        {"agent_owned": False, "reason": "ordinary"},
        {"legacy": True}, {"preserved_gate": "", "reason": "waiver"},
        {"preserved_gate": "convenience", "reason": "not an existing gate"},
        {"agent_owned": True, "reason": ""},
        {"classes": ["security"], "agent_owned": True, "reason": "mixed"},
        {"class": "security", "legacy": True, "reason": "mixed"},
    ]:
        value = approval_points_table()
        value["approval_points"][0]["classification"] = classification
        assert validate(value)[0], classification


def test_approval_point_needs_exactly_one_classification() -> None:
    value = approval_points_table()
    value["approval_points"][0]["classification"] = {}
    findings, _ = validate(value)
    assert any("classification" in finding for finding in findings), findings
    value = approval_points_table()
    value["approval_points"][0]["classification"] = {"class": "serious_law", "gap_issue": "CON-9001", "reason": "both"}
    findings, _ = validate(value)
    assert any("classification" in finding for finding in findings), findings


def test_approval_point_gap_must_name_a_reasoned_issue() -> None:
    value = approval_points_table()
    value["approval_points"][0]["classification"] = {"gap_issue": "CON-9001"}
    findings, _ = validate(value)
    assert any("gap_issue" in finding or "reason" in finding for finding in findings), findings
    value = approval_points_table()
    value["approval_points"][0]["classification"] = {"gap_issue": "CON-9002", "reason": "closed owner"}
    findings, _ = validate(value)
    assert any("closed" in finding for finding in findings), findings


def test_approval_point_owner_path_must_exist_in_the_repository() -> None:
    value = approval_points_table()
    value["approval_points"][0]["owner"] = {**APPROVAL_OWNER, "path": "internal/store/no-such-owner-file.go"}
    findings, _ = validate(value)
    assert any("owner path" in finding for finding in findings), findings


def test_unknown_approval_reachability_is_refused() -> None:
    value = approval_points_table()
    value["approval_points"][0]["reachability"] = ["wildcard_legacy"]
    findings, _ = validate(value)
    assert any("reachability" in finding for finding in findings), findings


def test_scalar_approval_reachability_is_refused() -> None:
    value = approval_points_table()
    value["approval_points"][0]["reachability"] = "live"
    findings, _ = validate(value)
    assert any("reachability" in finding for finding in findings), findings


def test_every_approval_gap_is_reported() -> None:
    gap_point = approval_point(
        id="takeover-consent",
        owner={**APPROVAL_OWNER, "branch": "record_takeover"},
        classification={"gap_issue": "CON-9001", "reason": "consent retained until the holding record is amended"},
    )
    _, gaps = validate(approval_points_table(points=[approval_point(), gap_point]))
    assert "approval-point takeover-consent -> CON-9001" in gaps, gaps


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

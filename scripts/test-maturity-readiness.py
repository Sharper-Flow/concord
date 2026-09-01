#!/usr/bin/env python3
"""Focused tests for the maturity-readiness checker (CD-0091)."""

from __future__ import annotations

import importlib.util
import json
from pathlib import Path

SCRIPT = Path(__file__).with_name("check-maturity-readiness.py")
SPEC = importlib.util.spec_from_file_location("maturity_checker", SCRIPT)
assert SPEC and SPEC.loader
checker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(checker)

# A real, resolving go_test anchor in this repository. The shared
# evidence_anchors machinery reads from the actual repo root.
RESOLVED_ANCHOR = {"kind": "go_test", "value": "internal/store.TestOpenAppliesRequiredPragmas"}
# A real decision path that resolves at the repository root.
SOURCE_PATH = "docs/decisions/CD-0091-maturity-promotion-ladder.md"


def fixture() -> dict:
    return {
        "schema_version": "1.0",
        "rung": "alpha",
        "source": {"path": SOURCE_PATH, "section": "Decision"},
        "conditions": [
            {"id": "a1", "ordinal": 1, "title": "First condition"},
            {"id": "a2", "ordinal": 2, "title": "Second condition"},
        ],
        "items": [
            {
                "id": "a1-satisfied",
                "condition": "a1",
                "title": "Satisfied",
                "requirement": "Something checkable is already true.",
                "state": "satisfied",
                "evidence": [dict(RESOLVED_ANCHOR)],
            },
            {
                "id": "a2-outstanding",
                "condition": "a2",
                "title": "Outstanding",
                "requirement": "Something checkable is not yet true.",
                "state": "outstanding",
                "issue": 637,
            },
        ],
    }


def test_valid_fixture_has_no_findings() -> None:
    findings, tally = checker.validate(fixture())
    assert findings == [], findings
    assert tally["satisfied"] == 1 and tally["outstanding"] == 1


def test_satisfied_with_issue_is_rejected() -> None:
    value = fixture()
    value["items"][0]["issue"] = 5
    findings, _ = checker.validate(value)
    assert any("must not carry issue or reason" in f for f in findings), findings


def test_satisfied_without_evidence_is_rejected() -> None:
    value = fixture()
    del value["items"][0]["evidence"]
    findings, _ = checker.validate(value)
    assert any("requires bounded non-empty evidence" in f for f in findings), findings


def test_unresolvable_anchor_is_rejected() -> None:
    value = fixture()
    value["items"][0]["evidence"] = [{"kind": "go_test", "value": "internal/store.TestDoesNotExistAtAll"}]
    findings, _ = checker.validate(value)
    assert any("does not resolve" in f for f in findings), findings


def test_outstanding_without_issue_is_rejected() -> None:
    value = fixture()
    del value["items"][1]["issue"]
    findings, _ = checker.validate(value)
    assert any("requires a tracking issue number" in f for f in findings), findings


def test_out_of_scope_requires_reason() -> None:
    value = fixture()
    value["items"][1] = {
        "id": "a2-scoped-out",
        "condition": "a2",
        "title": "Scoped out",
        "requirement": "This is deliberately out of scope for the rung.",
        "state": "out_of_scope",
    }
    findings, _ = checker.validate(value)
    assert any("requires a bounded reason" in f for f in findings), findings


def test_condition_prefix_must_match_rung() -> None:
    value = fixture()
    value["conditions"][0]["id"] = "b1"
    value["items"][0]["id"] = "a1-satisfied"
    findings, _ = checker.validate(value)
    assert any("does not match rung prefix" in f for f in findings), findings


def test_item_prefix_must_match_condition() -> None:
    value = fixture()
    value["items"][0]["id"] = "a2-mislabelled"
    findings, _ = checker.validate(value)
    assert any("id prefix disagrees with condition" in f for f in findings), findings


def test_non_contiguous_ordinals_are_rejected() -> None:
    value = fixture()
    value["conditions"][1]["ordinal"] = 3
    findings, _ = checker.validate(value)
    assert any("ordinals must be contiguous" in f for f in findings), findings


def test_unknown_rung_is_rejected() -> None:
    value = fixture()
    value["rung"] = "gamma"
    findings, _ = checker.validate(value)
    assert any("rung must be one of" in f for f in findings), findings


def test_condition_without_item_is_rejected() -> None:
    value = fixture()
    value["items"] = [value["items"][0]]
    findings, _ = checker.validate(value)
    assert any("carry no item" in f for f in findings), findings


def test_real_manifest_validates() -> None:
    manifest = json.loads(checker.MANIFEST.read_text(encoding="utf-8"))
    findings, _ = checker.validate(manifest)
    assert findings == [], findings


def main() -> int:
    tests = [value for name, value in sorted(globals().items()) if name.startswith("test_") and callable(value)]
    for test in tests:
        test()
    print(f"maturity readiness checker tests passed: {len(tests)} test(s)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

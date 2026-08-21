#!/usr/bin/env python3
"""Focused tests for the first-usable-floor readiness checker."""

from __future__ import annotations

import importlib.util
import json
import tempfile
from pathlib import Path


SCRIPT = Path(__file__).with_name("check-floor-readiness.py")
SPEC = importlib.util.spec_from_file_location("floor_checker", SCRIPT)
assert SPEC and SPEC.loader
checker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(checker)


# A real, resolving anchor in this repository. The shared evidence_anchors
# machinery reads from the actual repo ROOT, so a temp test root can use
# any anchor that resolves there.
RESOLVED_ANCHOR = {"kind": "go_test", "value": "internal/store.TestOpenAppliesRequiredPragmas"}


def fixture() -> dict:
    return {
        "schema_version": "2.0",
        "source": {"path": "docs/priorities.md", "section": "First-usable floor"},
        "conditions": [
            {"id": "fc1", "ordinal": 1, "title": "First condition"},
            {"id": "fc2", "ordinal": 2, "title": "Second condition"},
        ],
        "items": [
            {
                "id": "fc1-satisfied-item",
                "condition": "fc1",
                "title": "Satisfied",
                "requirement": "Something checkable is already true.",
                "state": "satisfied",
                "evidence": [dict(RESOLVED_ANCHOR)],
            },
            {
                "id": "fc2-outstanding-item",
                "condition": "fc2",
                "title": "Outstanding",
                "requirement": "Something checkable is not yet true.",
                "state": "outstanding",
                "issue": 74,
            },
        ],
    }


def build_root(directory: str) -> Path:
    root = Path(directory)
    (root / "docs").mkdir()
    (root / "docs/priorities.md").write_text(
        "## First-usable floor\n\n"
        "Use this when it is usable:\n\n"
        "1. First condition\n"
        "2. Second condition\n",
        encoding="utf-8",
    )
    return root


def findings_for(value: dict) -> list[str]:
    with tempfile.TemporaryDirectory() as directory:
        root = build_root(directory)
        found, _ = checker.validate(value, root=root)
        return found


def assert_rejected(value: dict, fragment: str) -> None:
    found = findings_for(value)
    assert found, f"expected rejection containing {fragment!r}"
    assert any(fragment in finding for finding in found), f"{fragment!r} not in {found}"


def test_fixture_is_accepted() -> None:
    with tempfile.TemporaryDirectory() as directory:
        root = build_root(directory)
        found, tally = checker.validate(fixture(), root=root)
        assert found == [], found
        assert tally == {"satisfied": 1, "outstanding": 1, "unmeasured": 0, "out_of_scope": 0}


def test_duplicate_keys_at_every_level() -> None:
    cases = [
        '{"schema_version":"2.0","schema_version":"2.0"}',
        '{"source":{"path":"a","path":"b"}}',
        '{"items":[{"id":"a","id":"b"}]}',
    ]
    with tempfile.TemporaryDirectory() as directory:
        path = Path(directory) / "duplicate.json"
        for raw in cases:
            path.write_text(raw, encoding="utf-8")
            found: list[str] = []
            assert checker.load(path, found) is None
            assert found and "invalid JSON" in found[0]


def test_evidence_as_bare_path_string_is_rejected() -> None:
    # Issue #187: "it cannot become satisfied from a cited path alone".
    value = fixture()
    value["items"][0]["evidence"] = ["docs/priorities.md"]
    assert_rejected(value, "evidence must be typed anchors, not paths")


def test_unknown_anchor_kind_is_rejected() -> None:
    value = fixture()
    value["items"][0]["evidence"] = [{"kind": "path", "value": "docs/priorities.md"}]
    assert_rejected(value, "anchor kind must be one of")


def test_go_test_anchor_for_missing_test_is_rejected() -> None:
    value = fixture()
    value["items"][0]["evidence"] = [
        {"kind": "go_test", "value": "internal/store.TestThisDoesNotExistAnywhere"}
    ]
    assert_rejected(value, "go_test anchor does not resolve")


def test_validator_anchor_for_missing_script_is_rejected() -> None:
    value = fixture()
    value["items"][0]["evidence"] = [
        {"kind": "validator", "value": "scripts/check-does-not-exist.py"}
    ]
    assert_rejected(value, "validator anchor")


def test_validator_anchor_for_uninvoked_script_is_rejected(
    tmp_name: str = "check-orphan-fixture.py",
) -> None:
    # A checker that exists but is wired into nothing proves nothing, so the
    # fixture is a real on-disk script that no required workflow invokes. A
    # script that gains a workflow invocation cannot serve here; the orphan
    # must stay synthetic.
    orphan = Path(__file__).resolve().parents[1] / "scripts" / tmp_name
    orphan.write_text("#!/usr/bin/env python3\n", encoding="utf-8")
    try:
        value = fixture()
        value["items"][0]["evidence"] = [
            {"kind": "validator", "value": f"scripts/{tmp_name}"}
        ]
        assert_rejected(
            value, "validator anchor is not invoked by a required workflow"
        )
    finally:
        orphan.unlink()


def test_schema_version_1_0_is_rejected() -> None:
    value = fixture()
    value["schema_version"] = "1.0"
    assert_rejected(value, "schema_version must be 2.0")


def test_satisfied_item_requires_evidence() -> None:
    value = fixture()
    del value["items"][0]["evidence"]
    assert_rejected(value, "requires bounded non-empty evidence")


def test_satisfied_item_rejects_issue_and_reason() -> None:
    for field, content in (("issue", 74), ("reason", "a reason long enough to pass")):
        value = fixture()
        value["items"][0][field] = content
        assert_rejected(value, "must not carry issue or reason")


def test_outstanding_item_requires_issue() -> None:
    value = fixture()
    del value["items"][1]["issue"]
    assert_rejected(value, "requires a tracking issue number")


def test_outstanding_item_rejects_evidence() -> None:
    value = fixture()
    value["items"][1]["evidence"] = [dict(RESOLVED_ANCHOR)]
    assert_rejected(value, "evidence is only valid for a satisfied item")


def test_unmeasured_item_requires_reason() -> None:
    value = fixture()
    value["items"][1] = {
        "id": "fc2-unmeasured-item",
        "condition": "fc2",
        "title": "Unmeasured",
        "requirement": "Something checkable has not been established either way.",
        "state": "unmeasured",
    }
    assert_rejected(value, "requires a bounded reason")


def test_unmeasured_item_rejects_issue() -> None:
    value = fixture()
    value["items"][1] = {
        "id": "fc2-unmeasured-item",
        "condition": "fc2",
        "title": "Unmeasured",
        "requirement": "Something checkable has not been established either way.",
        "state": "unmeasured",
        "reason": "This has not been audited in any pass so far.",
        "issue": 74,
    }
    assert_rejected(value, "issue is only valid for an outstanding item")


def test_duplicate_evidence_is_rejected() -> None:
    value = fixture()
    value["items"][0]["evidence"] = [dict(RESOLVED_ANCHOR), dict(RESOLVED_ANCHOR)]
    assert_rejected(value, "evidence contains duplicates")


def test_every_condition_requires_an_item() -> None:
    value = fixture()
    value["items"] = [value["items"][0]]
    assert_rejected(value, "floor conditions carry no item")


def test_item_id_prefix_must_match_condition() -> None:
    value = fixture()
    value["items"][0]["id"] = "fc2-mismatched-prefix"
    assert_rejected(value, "id prefix disagrees with condition")


def test_undeclared_condition_is_rejected() -> None:
    value = fixture()
    value["items"][0]["condition"] = "fc9"
    assert_rejected(value, "is not declared")


def test_condition_id_must_agree_with_ordinal() -> None:
    value = fixture()
    value["conditions"][1] = {"id": "fc5", "ordinal": 2, "title": "Mismatched"}
    assert_rejected(value, "disagrees with ordinal")


def test_ordinals_must_be_contiguous() -> None:
    value = fixture()
    value["conditions"][1] = {"id": "fc3", "ordinal": 3, "title": "Gap"}
    value["items"][1]["condition"] = "fc3"
    value["items"][1]["id"] = "fc3-outstanding-item"
    assert_rejected(value, "ordinals must be contiguous")


def test_duplicate_item_ids_are_rejected() -> None:
    value = fixture()
    value["items"][1]["id"] = value["items"][0]["id"]
    assert_rejected(value, "duplicate item id")


def test_unknown_fields_are_rejected() -> None:
    cases = [
        (lambda v: v.update({"extra": 1}), "manifest: unknown fields"),
        (lambda v: v["source"].update({"extra": 1}), "manifest.source: unknown fields"),
        (lambda v: v["conditions"][0].update({"extra": 1}), "unknown fields"),
        (lambda v: v["items"][0].update({"extra": 1}), "unknown fields"),
    ]
    for mutate, fragment in cases:
        value = fixture()
        mutate(value)
        assert_rejected(value, fragment)


def test_source_path_must_exist() -> None:
    value = fixture()
    value["source"]["path"] = "docs/missing.md"
    assert_rejected(value, "path does not exist")


def test_invalid_state_is_rejected() -> None:
    value = fixture()
    value["items"][0]["state"] = "probably-fine"
    assert_rejected(value, "state must be one of")


def test_boolean_is_not_an_issue_number() -> None:
    value = fixture()
    value["items"][1]["issue"] = True
    assert_rejected(value, "requires a tracking issue number")


def test_repository_schema_matches_validator_constants() -> None:
    # The schema is the part of the contract the validator owns; this
    # checks that the published schema and the validator constants stay in
    # lock-step so a drift becomes a failing test rather than a passing
    # validator against an unsynchronized contract.
    assert checker.SCHEMA.is_file()
    schema = json.loads(checker.SCHEMA.read_text(encoding="utf-8"))
    assert set(schema["properties"]) == checker.ALLOWED_ROOT
    assert set(schema["properties"]["items"]["items"]["properties"]) == checker.ALLOWED_ITEM
    assert set(schema["properties"]["items"]["items"]["required"]) == checker.REQUIRED_ITEM
    assert tuple(schema["properties"]["items"]["items"]["properties"]["state"]["enum"]) == checker.STATES
    assert schema["properties"]["schema_version"]["const"] == checker.SCHEMA_VERSION


def test_repository_manifest_passes_validation() -> None:
    # The repository manifest must pass its own validator: every satisfied
    # item carries executable anchors that resolve and are invoked by a
    # required workflow. A failure here means a cited check stopped
    # existing or stopped running — exactly the drift this manifest exists
    # to catch at review time rather than at claim time.
    data = json.loads(checker.MANIFEST.read_text(encoding="utf-8"))
    findings, _tally = checker.validate(data)
    assert findings == [], findings


def test_condition_correspondence_dropped_condition() -> None:
    # The fixture has fc1 and fc2; the source has 2 items. Drop fc2 and the
    # count diverges. The validator must report a non-pass.
    value = fixture()
    value["conditions"] = [value["conditions"][0]]
    value["items"] = [value["items"][0]]
    assert_rejected(value, "manifest declares 1 condition")


def test_condition_correspondence_added_condition() -> None:
    # The fixture has 2 conditions; the source has 2 items. Add a third
    # condition whose id/ordinal/title also match the third source item so
    # only the count mis-match vs. source can be detected. The fixture's
    # source only has 2 items, so the count divergence is the failure mode.
    value = fixture()
    value["conditions"].append({"id": "fc3", "ordinal": 3, "title": "Third condition"})
    value["items"].append(
        {
            "id": "fc3-third-item",
            "condition": "fc3",
            "title": "Third",
            "requirement": "Third item is satisfied and checkable.",
            "state": "satisfied",
            "evidence": [dict(RESOLVED_ANCHOR)],
        }
    )
    assert_rejected(value, "manifest declares 3 condition")


def test_condition_correspondence_reordered_condition() -> None:
    # Swap the titles of the two declared conditions; the source ordering
    # must be preserved so the second condition's title must equal the
    # second source item.
    value = fixture()
    value["conditions"][0]["title"] = "Second condition"
    value["conditions"][1]["title"] = "First condition"
    assert_rejected(value, "title does not equal first sentence")


def test_condition_correspondence_reworded_title() -> None:
    # Reword a single title; the source's first sentence must remain exact.
    value = fixture()
    value["conditions"][0]["title"] = "First condition reworded."
    assert_rejected(value, "title does not equal first sentence")


def test_condition_correspondence_unresolvable_section() -> None:
    # Point the manifest-level source at a section that does not exist in
    # the priorities.md stub. The validator must fail rather than pass
    # vacuously because there is no such section.
    value = fixture()
    value["source"]["section"] = "Does not exist"
    assert_rejected(value, "section 'Does not exist' not found")


def test_condition_correspondence_override_section() -> None:
    # A condition may declare its own source override. When the override
    # section does not exist, that condition is rejected with the section
    # not-found message.
    value = fixture()
    value["conditions"][0]["source"] = {
        "path": "docs/priorities.md",
        "section": "Does not exist",
    }
    assert_rejected(value, "section 'Does not exist' not found")


def test_condition_correspondence_section_with_no_items() -> None:
    # The override source resolves to a section that exists but has no
    # numbered or bulleted items. The validator must report that as a
    # failure, not a vacuous pass.
    value = fixture()
    with tempfile.TemporaryDirectory() as directory:
        root = build_root(directory)
        # Add an empty section after the existing one.
        root.joinpath("docs/priorities.md").write_text(
            "## First-usable floor\n\n"
            "1. First condition\n"
            "2. Second condition\n\n"
            "---\n\n"
            "## Empty section\n\n"
            "Just prose, no items.\n",
            encoding="utf-8",
        )
        value["conditions"][0]["source"] = {
            "path": "docs/priorities.md",
            "section": "Empty section",
        }
        found, _ = checker.validate(value, root=root)
        assert any("has no items" in finding for finding in found), found


def test_condition_correspondence_override_to_bulleted_source() -> None:
    # A condition can override its source to a section that uses bullets
    # instead of numbered items. The first sentence of the bullet must
    # match the title verbatim.
    value = fixture()
    with tempfile.TemporaryDirectory() as directory:
        root = build_root(directory)
        root.joinpath("docs/priorities.md").write_text(
            "## First-usable floor\n\n"
            "1. First condition\n"
            "2. Second condition\n\n"
            "---\n\n"
            "## Bulleted section\n\n"
            "- bullet one is exact.\n"
            "- bullet two is exact.\n",
            encoding="utf-8",
        )
        value["conditions"][0]["source"] = {
            "path": "docs/priorities.md",
            "section": "Bulleted section",
        }
        value["conditions"][0]["title"] = "bullet one is exact."
        value["conditions"][1]["source"] = {
            "path": "docs/priorities.md",
            "section": "Bulleted section",
        }
        value["conditions"][1]["title"] = "bullet two is exact."
        # Manifest-level source still has to resolve; the priority stub
        # already has the section we built earlier, so an unused but valid
        # path is sufficient.
        value["source"] = {"path": "docs/priorities.md", "section": "First-usable floor"}
        found, _ = checker.validate(value, root=root)
        assert found == [], found


def main() -> int:
    tests = [value for name, value in sorted(globals().items()) if name.startswith("test_") and callable(value)]
    for test in tests:
        test()
    print(f"floor readiness checker tests passed: {len(tests)} test(s)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
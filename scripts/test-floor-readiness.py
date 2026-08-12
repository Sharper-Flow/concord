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


def fixture() -> dict:
    return {
        "schema_version": "1.0",
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
                "evidence": ["docs/priorities.md"],
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
    (root / "docs/priorities.md").write_text("priorities\n", encoding="utf-8")
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
        '{"schema_version":"1.0","schema_version":"1.0"}',
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


def test_satisfied_item_requires_existing_evidence() -> None:
    value = fixture()
    value["items"][0]["evidence"] = ["docs/does-not-exist.md"]
    assert_rejected(value, "evidence path does not exist")


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
    value["items"][1]["evidence"] = ["docs/priorities.md"]
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


def test_unsafe_evidence_paths_are_rejected() -> None:
    for path in ("../secrets.md", "/etc/passwd", "docs/../docs/priorities.md"):
        value = fixture()
        value["items"][0]["evidence"] = [path]
        assert_rejected(value, "unsafe evidence path")


def test_duplicate_evidence_is_rejected() -> None:
    value = fixture()
    value["items"][0]["evidence"] = ["docs/priorities.md", "docs/priorities.md"]
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


def test_repository_manifest_and_schema_are_valid() -> None:
    assert checker.SCHEMA.is_file()
    data = json.loads(checker.MANIFEST.read_text(encoding="utf-8"), object_pairs_hook=checker.reject_duplicate_pairs)
    found, tally = checker.validate(data)
    assert found == [], found
    assert sum(tally.values()) > 0
    schema = json.loads(checker.SCHEMA.read_text(encoding="utf-8"))
    assert set(schema["properties"]) == checker.ALLOWED_ROOT
    assert set(schema["properties"]["items"]["items"]["properties"]) == checker.ALLOWED_ITEM
    assert set(schema["properties"]["items"]["items"]["required"]) == checker.REQUIRED_ITEM
    assert tuple(schema["properties"]["items"]["items"]["properties"]["state"]["enum"]) == checker.STATES


def main() -> int:
    tests = [value for name, value in sorted(globals().items()) if name.startswith("test_") and callable(value)]
    for test in tests:
        test()
    print(f"floor readiness checker tests passed: {len(tests)} test(s)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

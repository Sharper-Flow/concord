#!/usr/bin/env python3
"""Focused tests for the maturity-readiness checker (CD-0091)."""

from __future__ import annotations

import importlib.util
import io
import json
import tempfile
from contextlib import contextmanager, redirect_stderr, redirect_stdout
from pathlib import Path
from unittest import mock

import coverage_state
import knowledge_index

SCRIPT = Path(__file__).with_name("check-maturity-readiness.py")
SPEC = importlib.util.spec_from_file_location("maturity_checker", SCRIPT)
assert SPEC and SPEC.loader
checker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(checker)

# A real, resolving go_test anchor in this repository. The shared
# evidence_anchors machinery reads from the actual repo root.
RESOLVED_ANCHOR = {"kind": "go_test", "value": "internal/store.TestOpenAppliesRequiredPragmas"}
# A real decision path that resolves at the repository root.
SOURCE_PATH = ".concord/docs/decisions/CD-0091-maturity-promotion-ladder.md"


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


def validate_fixture(value: dict):
    with mock.patch.object(checker, "load_issue_states", return_value={
        str(pointer): "open" for pointer in (637, 1000001, "CON-1070", "ENG2-5")
    }):
        return checker.validate(value)


def test_valid_fixture_has_no_findings() -> None:
    findings, tally, _ = validate_fixture(fixture())
    assert findings == [], findings
    assert tally["satisfied"] == 1 and tally["outstanding"] == 1


def test_satisfied_with_issue_is_rejected() -> None:
    value = fixture()
    value["items"][0]["issue"] = 5
    findings, _, _ = validate_fixture(value)
    assert any("must not carry issue or reason" in f for f in findings), findings


def test_satisfied_without_evidence_is_rejected() -> None:
    value = fixture()
    del value["items"][0]["evidence"]
    findings, _, _ = validate_fixture(value)
    assert any("requires bounded non-empty evidence" in f for f in findings), findings


def test_unresolvable_anchor_is_rejected() -> None:
    value = fixture()
    value["items"][0]["evidence"] = [{"kind": "go_test", "value": "internal/store.TestDoesNotExistAtAll"}]
    findings, _, _ = validate_fixture(value)
    assert any("does not resolve" in f for f in findings), findings


def test_outstanding_without_issue_is_rejected() -> None:
    value = fixture()
    del value["items"][1]["issue"]
    findings, _, _ = validate_fixture(value)
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
    findings, _, _ = validate_fixture(value)
    assert any("requires a bounded reason" in f for f in findings), findings


def test_condition_prefix_must_match_rung() -> None:
    value = fixture()
    value["conditions"][0]["id"] = "b1"
    value["items"][0]["id"] = "a1-satisfied"
    findings, _, _ = validate_fixture(value)
    assert any("does not match rung prefix" in f for f in findings), findings


def test_item_prefix_must_match_condition() -> None:
    value = fixture()
    value["items"][0]["id"] = "a2-mislabelled"
    findings, _, _ = validate_fixture(value)
    assert any("id prefix disagrees with condition" in f for f in findings), findings


def test_non_contiguous_ordinals_are_rejected() -> None:
    value = fixture()
    value["conditions"][1]["ordinal"] = 3
    findings, _, _ = validate_fixture(value)
    assert any("ordinals must be contiguous" in f for f in findings), findings


def test_unknown_rung_is_rejected() -> None:
    value = fixture()
    value["rung"] = "gamma"
    findings, _, _ = validate_fixture(value)
    assert any("rung must be one of" in f for f in findings), findings


def test_condition_without_item_is_rejected() -> None:
    value = fixture()
    value["items"] = [value["items"][0]]
    findings, _, _ = validate_fixture(value)
    assert any("carry no item" in f for f in findings), findings


def test_real_manifest_validates() -> None:
    manifest = json.loads(checker.MANIFEST.read_text(encoding="utf-8"))
    findings, _, _ = checker.validate(manifest)
    assert findings == [], findings



def test_real_manifests_validate_with_unique_rungs() -> None:
    paths = coverage_state.maturity_manifest_paths(checker.ROOT)
    assert paths, "no rung manifests found"
    rungs = []
    for path in paths:
        manifest = json.loads(path.read_text(encoding="utf-8"))
        findings, _, _ = checker.validate(manifest)
        assert findings == [], f"{path.name}: {findings}"
        rungs.append(manifest["rung"])
    assert len(rungs) == len(set(rungs)), "a rung is measured twice"
    assert "alpha" in rungs and "beta" in rungs


def beta_fixture() -> dict:
    value = fixture()
    value["rung"] = "beta"
    for number, condition in enumerate(value["conditions"], 1):
        condition["id"] = f"b{number}"
    value["items"][0].update(id="b1-satisfied", condition="b1")
    value["items"][1] = {
        "id": "b2-law-coverage-proved",
        "condition": "b2",
        "title": "Law coverage is proved",
        "requirement": "Every coverage record is proved or declared, with no outstanding or unmeasured record.",
        "state": "satisfied",
        "evidence": [{"kind": "validator", "value": "scripts/check-law-coverage.py"}],
    }
    return value


def coverage_record(identifier: str, state: str) -> dict:
    record = {"id": identifier, "state": state}
    if state == "satisfied":
        record["evidence"] = [dict(RESOLVED_ANCHOR)]
    elif state == "outstanding":
        record["issue"] = 637
    else:
        record["reason"] = "Synthetic coverage declaration with a bounded reason."
    return record


@contextmanager
def coverage_fixture(records: list[dict], indexed: list[str] | None = None):
    with tempfile.TemporaryDirectory(prefix="maturity-coverage-") as directory:
        root = Path(directory)
        snapshot = root / "issue-state.json"
        snapshot.write_text(json.dumps({
            "schema_version": "1.0", "generated_at": "2026-01-01T00:00:00Z",
            "issues": {"637": "open", "CON-1070": "open"},
            "linear_read_at": {"CON-1070": "2026-01-01T00:00:00Z"},
        }), encoding="utf-8")
        subjects = indexed if indexed is not None else [record["id"] for record in records]
        with (
            mock.patch.object(knowledge_index, "compose_law_coverage", return_value={
                "schema_version": "1.0", "source": {}, "records": records,
            }),
            mock.patch.object(knowledge_index, "compose_manifest", return_value={
                "records": [{"id": identifier, "status": "published"} for identifier in subjects],
            }) as index,
            mock.patch.object(coverage_state, "ROOT", root),
            mock.patch.object(coverage_state, "ISSUE_STATE", snapshot),
        ):
            yield root, index


def run_manifest(value: dict, root: Path) -> tuple[int, str, str]:
    source = root / SOURCE_PATH
    source.parent.mkdir(parents=True, exist_ok=True)
    source.write_text("Synthetic maturity source.\n", encoding="utf-8")
    name = "maturity-readiness.v1.json" if value["rung"] == "alpha" else f"maturity-readiness.{value['rung']}.v1.json"
    manifest = root / ".concord/docs" / name
    manifest.write_text(json.dumps(value), encoding="utf-8")
    out, err = io.StringIO(), io.StringIO()
    with mock.patch.object(checker, "ROOT", root), redirect_stdout(out), redirect_stderr(err):
        code = checker.main()
    return code, out.getvalue(), err.getvalue()


def test_beta_rejects_valid_outstanding_coverage() -> None:
    with coverage_fixture([coverage_record("fixture-outstanding", "outstanding")]):
        findings = checker.validate(beta_fixture())[0]
    assert any("claims satisfied" in finding for finding in findings), findings


def outstanding_fixture(rung: str, issue: int | str) -> dict:
    value = beta_fixture() if rung == "beta" else fixture()
    if rung == "production":
        value["rung"] = rung
        for condition in value["conditions"]:
            condition["id"] = "p" + condition["id"][1:]
        for item in value["items"]:
            item["id"] = "p" + item["id"][1:]
            item["condition"] = "p" + item["condition"][1:]
    item = value["items"][1]
    item.pop("evidence", None)
    item.update(state="outstanding", issue=issue)
    return value


def test_maturity_outstanding_pointer_must_be_live() -> None:
    for rung in ("alpha", "beta", "production"):
        for issue in ("CON-1070", 637):
            for state in ("closed", None, "open"):
                value = outstanding_fixture(rung, issue)
                with coverage_fixture([coverage_record("fixture-process", "out_of_scope")]) as (root, _):
                    snapshot = coverage_state.ISSUE_STATE
                    document = json.loads(snapshot.read_text(encoding="utf-8"))
                    if state is None:
                        document["issues"].pop(str(issue))
                        document["linear_read_at"].pop(str(issue), None)
                    else:
                        document["issues"][str(issue)] = state
                    snapshot.write_text(json.dumps(document), encoding="utf-8")
                    code, out, err = run_manifest(value, root)
                assert code == (0 if state == "open" else 1), (rung, issue, state, out, err)
                if state != "open":
                    name = "maturity-readiness.v1.json" if rung == "alpha" else f"maturity-readiness.{rung}.v1.json"
                    assert f".concord/docs/{name}:" in err, err
                    assert value["items"][1]["id"] in err, err
                    assert f"outstanding issue {issue}" in err, err
                    assert ("is closed" if state == "closed" else "absent from the issue-state snapshot") in err, err


def test_maturity_rejects_an_unusable_issue_snapshot() -> None:
    for contents in (None, "{"):
        with coverage_fixture([coverage_record("fixture-process", "out_of_scope")]) as (root, _):
            if contents is None:
                coverage_state.ISSUE_STATE.unlink()
            else:
                coverage_state.ISSUE_STATE.write_text(contents, encoding="utf-8")
            code, out, err = run_manifest(fixture(), root)
        assert code == 1, (out, err)
        assert ".concord/docs/maturity-readiness.v1.json:" in err and "issue-state snapshot" in err, err


def test_beta_rejects_valid_unmeasured_coverage() -> None:
    with coverage_fixture([coverage_record("fixture-unmeasured", "unmeasured")]):
        findings = checker.validate(beta_fixture())[0]
    assert any("claims satisfied" in finding for finding in findings), findings


def test_beta_ready_uses_validated_complete_population() -> None:
    records = [coverage_record("fixture-proved", "satisfied"), coverage_record("fixture-process", "out_of_scope")]
    with coverage_fixture(records):
        findings, _, blockers = checker.validate(beta_fixture())
    assert findings == [], findings
    assert blockers == {"outstanding": [], "unmeasured": []}, blockers


def test_beta_invalid_coverage_never_produces_readiness() -> None:
    bad_anchor = coverage_record("fixture-unproved", "satisfied")
    bad_anchor["evidence"] = [{"kind": "go_test", "value": "internal/store.TestMissingCoverageProof"}]
    for records, subjects, expected in (
        ([coverage_record("fixture-proved", "satisfied")], ["fixture-proved", "fixture-missing"], "undeclared law record"),
        ([bad_anchor], ["fixture-unproved"], "does not resolve"),
        ([], [], "non-empty array"),
    ):
        with coverage_fixture(records, subjects):
            findings, _, blockers = checker.validate(beta_fixture())
        assert any(expected in finding for finding in findings), findings
        assert blockers is None, blockers
    with coverage_fixture([]), mock.patch.object(
        knowledge_index, "compose_law_coverage", side_effect=knowledge_index.ComposeError(["coverage shards unreadable"]),
    ):
        findings, _, blockers = checker.validate(beta_fixture())
    assert any("coverage shards unreadable" in finding for finding in findings), findings
    assert blockers is None, blockers


def test_beta_cli_lists_every_blocker_in_sorted_order() -> None:
    records = [coverage_record(f"fixture-{number * 2:02d}", "outstanding") for number in range(7)]
    records += [coverage_record(f"fixture-{number * 2 + 1:02d}", "unmeasured") for number in range(9)]
    with coverage_fixture(list(reversed(records))) as (root, _):
        code, out, err = run_manifest(beta_fixture(), root)
    assert code == 1, (out, err)
    assert "claims satisfied" in err, err
    assert "NOT READY (7 outstanding, 9 unmeasured)" in out, out
    lines = [line for line in out.splitlines() if line.startswith("b2 blocker:")]
    assert lines == [f"b2 blocker: {record['id']} ({record['state']})" for record in sorted(records, key=lambda record: record["id"])], lines


def test_beta_canonical_condition_and_item_cannot_be_bypassed() -> None:
    for change in ("missing-condition", "renamed-condition", "missing-item", "renamed-item", "duplicate-item", "moved-item", "out-of-scope"):
        value = beta_fixture()
        item = value["items"][1]
        if change == "missing-condition":
            value["conditions"].pop()
        elif change == "renamed-condition":
            value["conditions"][1]["id"] = "b3"
            item.update(id="b3-law-coverage-proved", condition="b3")
        elif change == "missing-item":
            value["items"].pop()
        elif change == "renamed-item":
            item["id"] = "b2-another-item"
        elif change == "duplicate-item":
            value["items"].append(dict(item))
        elif change == "moved-item":
            item["condition"] = "b1"
        else:
            item.update(state="out_of_scope", reason="A mandatory rung condition cannot be scoped away.")
            del item["evidence"]
        with coverage_fixture([coverage_record("fixture-proved", "satisfied")]) as (root, _):
            code, out, err = run_manifest(value, root)
        assert code == 1, (change, out, err)


def test_beta_partial_manifest_reports_validity_not_promotion() -> None:
    for state in ("outstanding", "unmeasured"):
        value = beta_fixture()
        item = value["items"][1]
        del item["evidence"]
        item["state"] = state
        item.update({"issue": "CON-1070"} if state == "outstanding" else {"reason": "The target condition has not been measured yet."})
        with coverage_fixture([coverage_record("fixture-blocker", "outstanding")]) as (root, _):
            code, out, err = run_manifest(value, root)
        assert code == 0, (out, err)
        assert "manifest validity" in out and "NOT READY" in out, out
        assert "maturity readiness (beta" not in out, out
        assert err == "", err


def test_beta_recomputes_after_a_green_population() -> None:
    records = [coverage_record("fixture-process", "out_of_scope")]
    with coverage_fixture(records) as (_, index):
        assert checker.validate(beta_fixture())[0] == []
        records.append(coverage_record("fixture-new", "outstanding"))
        index.return_value["records"].append({"id": "fixture-new", "status": "published"})
        findings = checker.validate(beta_fixture())[0]
    assert any("claims satisfied" in finding for finding in findings), findings


def test_outstanding_issue_vocabulary_matches_coverage_schema() -> None:
    for issue in (637, 1000001, "CON-1070", "ENG2-5"):
        value = fixture()
        value["items"][1]["issue"] = issue
        assert validate_fixture(value)[0] == [], issue
    for issue in (False, True, 0, -1, "con-1", "CON-0", "CON-01", "637", "CON-1\n", None):
        value = fixture()
        value["items"][1]["issue"] = issue
        assert any("tracking issue" in finding for finding in validate_fixture(value)[0]), issue
    root = SCRIPT.parents[1]
    maturity = json.loads((root / "contracts/maturity-readiness.schema.json").read_text(encoding="utf-8"))
    coverage = json.loads((root / "contracts/law-coverage.schema.json").read_text(encoding="utf-8"))
    assert maturity["properties"]["items"]["items"]["properties"]["issue"]["oneOf"] == coverage["properties"]["records"]["items"]["properties"]["issue"]["oneOf"]


def main() -> int:
    tests = [value for name, value in sorted(globals().items()) if name.startswith("test_") and callable(value)]
    for test in tests:
        test()
    print(f"maturity readiness checker tests passed: {len(tests)} test(s)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

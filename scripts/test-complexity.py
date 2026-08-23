#!/usr/bin/env python3
"""Tests for scripts/check-complexity.py.

The analysis itself belongs to gocognit and is not re-tested here. What is
tested is the budget semantics around it: that an allowance is permission for a
function to be a specific complexity rather than permission to be complex, and
that the manifest cannot go stale in either direction.

Only test_repository_budget_passes invokes the Go toolchain. Every other test
drives compare() and validate_manifest() against synthetic records, so the
suite stays fast and does not depend on the tree it is checking.
"""
import importlib.util
import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]

spec = importlib.util.spec_from_file_location(
    "check_complexity", Path(__file__).with_name("check-complexity.py")
)
guard = importlib.util.module_from_spec(spec)
spec.loader.exec_module(guard)

MANIFEST = ROOT / "docs/complexity-budget.v1.json"
SCHEMA = ROOT / "contracts/complexity-budget.schema.json"


def budget(*allowances: dict, threshold: int = 100) -> dict:
    return {"schema_version": "1.0", "threshold": threshold, "allowances": list(allowances)}


def allowance(name: str = "internal/agent.big", measured: int = 150, **overrides) -> dict:
    base = {
        "function": name,
        "measured": measured,
        "state": "outstanding",
        "issue": 406,
        "reason": "a reason long enough to satisfy the contract",
    }
    base.update(overrides)
    return base


def record(name: str, score: int, directory: str = "internal/agent") -> dict:
    return {
        "PkgName": directory.rsplit("/", 1)[-1],
        "FuncName": name,
        "Complexity": score,
        "Pos": {"Filename": f"{directory}/file.go", "Line": 1, "Column": 1},
    }


def compare(manifest: dict, records: list[dict]) -> list[str]:
    findings: list[str] = []
    guard.compare(manifest, records, findings)
    return findings


def test_unallowed_function_over_threshold_is_a_finding() -> None:
    findings = compare(budget(), [record("big", 150)])
    assert any("is not allowed" in f for f in findings), findings


def test_unallowed_function_under_threshold_passes() -> None:
    assert compare(budget(), [record("small", 99)]) == []


def test_allowance_at_its_measured_score_passes() -> None:
    assert compare(budget(allowance()), [record("big", 150)]) == []


def test_allowance_that_grew_is_a_regression() -> None:
    findings = compare(budget(allowance()), [record("big", 151)])
    assert any("grew from 150 to 151" in f for f in findings), findings


def test_allowance_that_shrank_is_also_a_finding() -> None:
    # A budget that overstates the debt stops describing the code, so an
    # improvement must be recorded rather than silently absorbed.
    findings = compare(budget(allowance()), [record("big", 120)])
    assert any("fell from 150 to 120" in f for f in findings), findings


def test_spent_allowance_is_a_finding() -> None:
    findings = compare(budget(allowance()), [record("big", 40)])
    assert any("fell from 150 to 40" in f for f in findings), findings


def test_allowance_for_a_vanished_function_is_a_finding() -> None:
    findings = compare(budget(allowance()), [record("other", 10)])
    assert any("no longer reports it" in f for f in findings), findings


def test_identity_survives_a_function_moving_down_its_file() -> None:
    moved = record("big", 150)
    moved["Pos"]["Line"] = 9000
    assert compare(budget(allowance()), [moved]) == []


def test_identity_distinguishes_same_name_in_different_directories() -> None:
    findings = compare(budget(allowance()), [record("big", 150, directory="internal/store")])
    assert any("no longer reports it" in f for f in findings), findings
    assert any("internal/store.big" in f and "not allowed" in f for f in findings), findings


def test_allowance_at_or_below_threshold_is_refused() -> None:
    findings: list[str] = []
    guard.validate_manifest(budget(allowance(measured=100)), findings)
    assert any("delete the allowance" in f for f in findings), findings


def test_outstanding_allowance_requires_an_issue() -> None:
    findings: list[str] = []
    entry = allowance()
    del entry["issue"]
    guard.validate_manifest(budget(entry), findings)
    assert any("requires an issue" in f for f in findings), findings


def test_non_outstanding_allowance_refuses_an_issue() -> None:
    findings: list[str] = []
    guard.validate_manifest(budget(allowance(state="unmeasured")), findings)
    assert any("only valid for an outstanding" in f for f in findings), findings


def test_duplicate_allowance_is_refused() -> None:
    findings: list[str] = []
    guard.validate_manifest(budget(allowance(), allowance()), findings)
    assert any("duplicate allowance" in f for f in findings), findings


def test_reason_must_say_something() -> None:
    findings: list[str] = []
    guard.validate_manifest(budget(allowance(reason="too short")), findings)
    assert any("reason must explain" in f for f in findings), findings


def test_pinned_version_cannot_become_latest() -> None:
    manifest = json.loads(MANIFEST.read_text(encoding="utf-8"))
    version = manifest["analysis"]["version"]
    assert version.startswith("v") and version != "vlatest", version
    assert "latest" not in version, version


def test_analysis_excludes_test_files() -> None:
    # Test functions are legitimately long; the budget describes shipped code.
    manifest = json.loads(MANIFEST.read_text(encoding="utf-8"))
    assert "_test\\.go" in manifest["analysis"]["args"], manifest["analysis"]["args"]


def test_schema_and_manifest_agree_on_declared_states() -> None:
    schema = json.loads(SCHEMA.read_text(encoding="utf-8"))
    states = set(
        schema["properties"]["allowances"]["items"]["properties"]["state"]["enum"]
    )
    manifest = json.loads(MANIFEST.read_text(encoding="utf-8"))
    for entry in manifest["allowances"]:
        assert entry["state"] in states, entry


def test_repository_budget_passes() -> None:
    findings: list[str] = []
    manifest = json.loads(MANIFEST.read_text(encoding="utf-8"))
    assert guard.validate_manifest(manifest, findings), findings


def main() -> int:
    failures = 0
    for name, function in sorted(globals().items()):
        if not name.startswith("test_") or not callable(function):
            continue
        try:
            function()
            print(f"ok  {name}")
        except AssertionError as err:
            failures += 1
            print(f"FAIL {name}: {err}")
    if failures:
        print(f"{failures} test(s) failed", file=sys.stderr)
        return 1
    print("complexity budget tests passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

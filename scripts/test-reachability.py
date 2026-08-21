#!/usr/bin/env python3
"""Tests for scripts/check-reachability.py.

The analysis itself belongs to deadcode and is not re-tested here. What is
tested is everything around it: that the manifest cannot go stale in either
direction, that the pinned command cannot quietly become unpinned, and that the
symbol identity survives edits that move a function down a file.

Tests that would otherwise invoke the Go toolchain set
CONCORD_SKIP_REACHABILITY_ANALYSIS, which checks manifest shape only. The full
path is exercised by test_repository_manifest_passes and by CI.
"""
import importlib.util
import json
import os
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]

spec = importlib.util.spec_from_file_location(
    "check_reachability", Path(__file__).with_name("check-reachability.py")
)
guard = importlib.util.module_from_spec(spec)
spec.loader.exec_module(guard)

MANIFEST = ROOT / "docs/reachability-exceptions.v1.json"


def load_manifest() -> dict:
    return json.loads(MANIFEST.read_text(encoding="utf-8"))


def run_cli(env_extra: dict[str, str] | None = None) -> subprocess.CompletedProcess[str]:
    env = dict(os.environ)
    env.update(env_extra or {})
    return subprocess.run(
        [sys.executable, str(ROOT / "scripts/check-reachability.py")],
        capture_output=True,
        text=True,
        env=env,
        cwd=ROOT,
    )


def with_manifest(document: dict, env_extra: dict[str, str] | None = None):
    original = MANIFEST.read_text(encoding="utf-8")
    try:
        MANIFEST.write_text(json.dumps(document, indent=2) + "\n", encoding="utf-8")
        return run_cli(env_extra)
    finally:
        MANIFEST.write_text(original, encoding="utf-8")


def test_symbol_identity_ignores_line_and_column() -> None:
    """Pinning position would make every edit above a function a manifest diff."""
    assert guard.symbol_for("internal/store/backup.go", "Backup") == "internal/store.Backup"
    assert (
        guard.symbol_for("internal/agent/authority.go", "Service.RegisterClient")
        == "internal/agent.Service.RegisterClient"
    )


def test_finding_pattern_parses_deadcode_output() -> None:
    match = guard.FINDING.fullmatch(
        "internal/store/backup.go:85:6: unreachable func: Backup"
    )
    assert match and match.group("file") == "internal/store/backup.go"
    assert match.group("func") == "Backup"
    assert guard.FINDING.fullmatch("not a finding line") is None


def test_undeclared_unreachable_function_is_a_finding() -> None:
    findings: list[str] = []
    guard.check_subject_set(
        ["internal/store.Backup"],
        ["internal/store.Backup", "internal/store.RestoreBackup"],
        "unreachable function",
        findings,
    )
    assert any("undeclared unreachable function" in f for f in findings)


def test_declaration_that_became_reachable_is_a_finding() -> None:
    """A stale exception widens the surface without anyone choosing to."""
    findings: list[str] = []
    guard.check_subject_set(
        ["internal/store.Backup", "internal/store.NowWiredUp"],
        ["internal/store.Backup"],
        "unreachable function",
        findings,
    )
    assert any("no longer exists" in f for f in findings)


def test_repository_manifest_passes_with_the_real_analysis() -> None:
    assert guard.main() == 0, "the seeded manifest must match the pinned analysis"


def test_satisfied_state_is_refused_for_an_exception() -> None:
    document = load_manifest()
    document["exceptions"].append(
        {
            "id": "bogus-satisfied",
            "state": "satisfied",
            "functions": ["internal/store.Backup"],
        }
    )
    result = with_manifest(document, {"CONCORD_SKIP_REACHABILITY_ANALYSIS": "1"})
    assert result.returncode == 1
    assert "meaningless for an exception" in result.stdout


def test_unpinned_analysis_version_is_refused() -> None:
    document = load_manifest()
    document["analysis"]["version"] = "latest"
    result = with_manifest(document)
    assert result.returncode == 1
    assert "exact semver tag" in result.stdout


def test_missing_analysis_field_is_refused() -> None:
    document = load_manifest()
    del document["analysis"]["entrypoints"]
    result = with_manifest(document)
    assert result.returncode == 1
    assert "analysis requires" in result.stdout


def test_state_obligations_are_enforced_for_exceptions() -> None:
    document = load_manifest()
    document["exceptions"][0] = {
        "id": "missing-reason",
        "state": "out_of_scope",
        "functions": ["internal/store.Backup"],
    }
    result = with_manifest(document, {"CONCORD_SKIP_REACHABILITY_ANALYSIS": "1"})
    assert result.returncode == 1
    assert "requires 'reason'" in result.stdout


def test_outstanding_exception_requires_an_issue() -> None:
    document = load_manifest()
    document["exceptions"][0] = {
        "id": "missing-issue",
        "state": "outstanding",
        "functions": ["internal/store.Backup"],
        "reason": "this should not be accepted in place of an issue",
    }
    result = with_manifest(document, {"CONCORD_SKIP_REACHABILITY_ANALYSIS": "1"})
    assert result.returncode == 1
    assert "forbids 'reason'" in result.stdout or "requires 'issue'" in result.stdout


def test_malformed_symbol_is_refused() -> None:
    document = load_manifest()
    document["exceptions"][0]["functions"] = ["backup.go:85:6"]
    result = with_manifest(document, {"CONCORD_SKIP_REACHABILITY_ANALYSIS": "1"})
    assert result.returncode == 1
    assert "must read <package>.<Name>" in result.stdout


def test_schema_state_enum_excludes_satisfied() -> None:
    assert guard.check_schema_states() == []
    schema = json.loads(
        (ROOT / "contracts/reachability-exceptions.schema.json").read_text(encoding="utf-8")
    )
    enum = schema["properties"]["exceptions"]["items"]["properties"]["state"]["enum"]
    assert "satisfied" not in enum
    assert enum == list(guard.EXCEPTION_STATES)


def test_skip_switch_produces_no_verdict() -> None:
    """The escape hatch must not be able to hide a finding."""
    result = run_cli({"CONCORD_SKIP_REACHABILITY_ANALYSIS": "1"})
    assert result.returncode == 0
    assert "skipped by request" in result.stdout
    workflow = (ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8")
    assert "CONCORD_SKIP_REACHABILITY_ANALYSIS" not in workflow


def test_every_declared_function_is_uniquely_owned() -> None:
    document = load_manifest()
    seen: dict[str, str] = {}
    for exception in document["exceptions"]:
        for symbol in exception["functions"]:
            assert symbol not in seen, f"{symbol} declared by {seen.get(symbol)} and {exception['id']}"
            seen[symbol] = exception["id"]


if __name__ == "__main__":
    failures = 0
    for name, fn in sorted(globals().items()):
        if name.startswith("test_") and callable(fn):
            try:
                fn()
                print(f"ok  {name}")
            except AssertionError as err:
                failures += 1
                print(f"FAIL {name}: {err}")
    sys.exit(1 if failures else 0)

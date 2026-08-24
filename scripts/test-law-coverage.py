#!/usr/bin/env python3
"""Tests for scripts/check-law-coverage.py.

The validator's whole claim is that a satisfied record is proved rather than
asserted, so these tests are mostly negative: each anchor kind must be shown to
reject a value that looks right and resolves to nothing. A validator that only
accepts its own seeded manifest would be the defect it exists to close.
"""
import importlib.util
import json
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]

spec = importlib.util.spec_from_file_location(
    "check_law_coverage", Path(__file__).with_name("check-law-coverage.py")
)
guard = importlib.util.module_from_spec(spec)
spec.loader.exec_module(guard)


def anchor_findings(kind: str, value: str) -> list[str]:
    findings: list[str] = []
    guard.check_anchor({"kind": kind, "value": value}, "test", findings)
    return findings


def test_repository_manifest_passes() -> None:
    assert guard.main() == 0, "the seeded manifest must validate"


def test_every_anchor_kind_is_exercised_by_the_manifest() -> None:
    """A resolver nothing uses is a resolver nothing proves."""
    manifest = json.loads((ROOT / "docs/law-coverage.v1.json").read_text(encoding="utf-8"))
    used = {
        anchor["kind"]
        for record in manifest["records"]
        for anchor in record.get("evidence", [])
    }
    unused = set(guard.ANCHOR_KINDS) - used
    assert not unused, f"anchor kinds declared but never used: {sorted(unused)}"


def test_go_test_anchor_rejects_a_missing_test() -> None:
    assert anchor_findings("go_test", "internal/store.TestThisDoesNotExistAnywhere")
    assert not anchor_findings("go_test", "internal/store.TestOpenAppliesRequiredPragmas")


def test_go_test_anchor_rejects_a_test_in_a_different_package() -> None:
    # The test exists, but not in the package the anchor names.
    assert anchor_findings("go_test", "internal/agent.TestOpenAppliesRequiredPragmas")


def test_go_test_anchor_rejects_a_malformed_value() -> None:
    for value in ("TestNoPackage", "internal/store.NotATest", "internal/store."):
        assert anchor_findings("go_test", value), value


def test_scenario_anchor_rejects_a_deferred_scenario() -> None:
    """The bug this resolver was written with, and the reason it is tested.

    A deferred scenario is present in the corpus and executed by nothing.
    Accepting it as evidence would assert a rule that no test enforces —
    presence standing in for enforcement, which is precisely the defect
    CD-0047 exists to close. The corpus is fully bound today, so the test
    seeds its own deferral rather than depending on the repo retaining one.
    """
    seeded = guard.ROOT / "internal" / "agent" / "zz_seeded_deferral_test.go"
    original = seeded.exists()
    backup = seeded.read_text(encoding="utf-8") if original else None
    seeded.write_text(
        'package agent\n\nvar seededForCoverageTest = true\n\nfunc init() { _ = jobDeferrals }\n\nconst zz = `jobDeferrals["ZZ-seeded-deferred"] = "coverage fixture"`\n',
        encoding="utf-8",
    )
    try:
        assert "ZZ-seeded-deferred" in guard.deferred_scenarios(), "seeded deferral not discovered"
        assert anchor_findings("scenario", "ZZ-seeded-deferred"), "deferred scenario accepted as evidence"
    finally:
        if original:
            seeded.write_text(backup, encoding="utf-8")
        else:
            seeded.unlink()


def test_scenario_anchor_accepts_a_bound_scenario() -> None:
    assert not anchor_findings("scenario", "AJ5-resolve-domain-overlap")
    assert not anchor_findings("scenario", "WF04-weaker-delivery")


def test_scenario_anchor_rejects_an_unknown_id() -> None:
    assert anchor_findings("scenario", "WF99-no-such-scenario")


def test_validator_anchor_requires_ci_invocation() -> None:
    assert not anchor_findings("validator", "scripts/check-public-content.py")
    # Nested through check-json.py rather than named in the workflow.
    assert not anchor_findings("validator", "scripts/check-knowledge-index.py")
    assert anchor_findings("validator", "scripts/check-not-a-real-validator.py")


def test_validator_anchor_rejects_an_uninvoked_script(tmp_name: str = "check-orphan.py") -> None:
    """A checker that exists but is wired into nothing proves nothing."""
    orphan = ROOT / "scripts" / tmp_name
    orphan.write_text("#!/usr/bin/env python3\n", encoding="utf-8")
    try:
        assert anchor_findings("validator", f"scripts/{tmp_name}")
    finally:
        orphan.unlink()


def test_generated_anchor_requires_the_generator_marker() -> None:
    assert not anchor_findings(
        "generated", "internal/store/generated_agent_lanes.go#LaneRegistryManifestDigest"
    )
    assert anchor_findings(
        "generated", "internal/store/generated_agent_lanes.go#NoSuchSymbolHere"
    )
    # A hand-written file carries no generator guarantee, whatever it contains.
    assert anchor_findings("generated", "internal/store/store.go#Store")


def test_unknown_anchor_kind_is_rejected() -> None:
    findings: list[str] = []
    guard.check_anchor({"kind": "path", "value": "docs/priorities.md"}, "test", findings)
    assert findings, "a repository path must not be usable as an anchor"


def test_anchor_must_carry_exactly_kind_and_value() -> None:
    findings: list[str] = []
    guard.check_anchor(
        {"kind": "go_test", "value": "internal/store.TestOpenAppliesRequiredPragmas", "note": "x"},
        "test",
        findings,
    )
    assert findings


def test_subject_set_is_derived_from_the_index_not_the_manifest() -> None:
    """CD-0047 D1: a record the manifest forgets must be a finding."""
    findings: list[str] = []
    indexed = guard.indexed_record_ids(findings)
    assert not findings and len(indexed) > 40
    guard.check_subject_set(indexed[:-1], indexed, "law record", findings)
    assert any("undeclared law record" in finding for finding in findings)


def test_a_stale_declaration_is_a_finding() -> None:
    findings: list[str] = []
    guard.check_subject_set(["CD-0002", "CD-9999"], ["CD-0002"], "law record", findings)
    assert any("no longer exists" in finding for finding in findings)


def test_duplicate_declarations_are_rejected() -> None:
    findings: list[str] = []
    guard.check_subject_set(["CD-0002", "CD-0002"], ["CD-0002"], "law record", findings)
    assert any("duplicate" in finding for finding in findings)


def test_schema_state_enum_matches_the_shared_vocabulary() -> None:
    assert guard.check_schema_states() == []
    schema = json.loads((ROOT / "contracts/law-coverage.schema.json").read_text(encoding="utf-8"))
    enum = schema["properties"]["records"]["items"]["properties"]["state"]["enum"]
    assert enum == list(guard.STATES)


def test_state_obligations_are_exclusive_in_both_directions() -> None:
    findings: list[str] = []
    guard.check_state_obligations(
        {"state": "satisfied", "evidence": [], "reason": "x" * 20}, "r", findings
    )
    assert any("forbids" in finding for finding in findings)

    findings = []
    guard.check_state_obligations({"state": "outstanding"}, "r", findings)
    assert any("requires 'issue'" in finding for finding in findings)

    findings = []
    guard.check_state_obligations({"state": "nonsense"}, "r", findings)
    assert findings


def test_outstanding_issue_pointer_must_be_live() -> None:
    """An outstanding record dies with its issue: closed and absent pointers fail (issue #324)."""
    manifest = ROOT / "docs/law-coverage.v1.json"
    shard = ROOT / "docs/knowledge/coverage/CD-0006.json"
    snapshot = ROOT / "docs/issue-state.v1.json"
    originals = (
        manifest.read_text(encoding="utf-8"),
        shard.read_text(encoding="utf-8"),
        snapshot.read_text(encoding="utf-8"),
    )
    try:
        snapshot_document = json.loads(originals[2])
        snapshot_document["issues"]["219"] = "closed"
        snapshot.write_text(json.dumps(snapshot_document, indent=2) + "\n", encoding="utf-8")

        def run_with_issue(number: int) -> subprocess.CompletedProcess:
            for target in (manifest, shard):
                document = json.loads(target.read_text(encoding="utf-8"))
                pool = document["records"] if "records" in document else [document]
                for record in pool:
                    if record.get("id") == "CD-0006":
                        record["issue"] = number
                target.write_text(json.dumps(document, indent=2) + "\n", encoding="utf-8")
            return subprocess.run(
                [sys.executable, str(ROOT / "scripts/check-law-coverage.py")],
                capture_output=True,
                text=True,
            )

        closed = run_with_issue(219)
        assert closed.returncode == 1, closed.stdout
        assert "outstanding issue 219 is closed" in closed.stdout

        absent = run_with_issue(999999)
        assert absent.returncode == 1, absent.stdout
        assert "absent from the issue-state snapshot" in absent.stdout
    finally:
        for target, content in zip((manifest, shard, snapshot), originals):
            target.write_text(content, encoding="utf-8")


def test_issue_snapshot_presence_and_shape() -> None:
    snapshot = ROOT / "docs/issue-state.v1.json"
    original = snapshot.read_text(encoding="utf-8")
    try:
        snapshot.unlink()
        missing = subprocess.run(
            [sys.executable, str(ROOT / "scripts/check-law-coverage.py")],
            capture_output=True,
            text=True,
        )
        assert missing.returncode == 1, missing.stdout
        assert "issue-state snapshot is missing" in missing.stdout

        snapshot.write_text(
            json.dumps({"schema_version": "1.0", "generated_at": "x", "issues": {"not-a-number": "open"}}) + "\n",
            encoding="utf-8",
        )
        malformed = subprocess.run(
            [sys.executable, str(ROOT / "scripts/check-law-coverage.py")],
            capture_output=True,
            text=True,
        )
        assert malformed.returncode == 1, malformed.stdout
        assert "decimal issue number" in malformed.stdout
    finally:
        snapshot.write_text(original, encoding="utf-8")


def test_green_repository_passes_offline_pointer_validation() -> None:
    result = subprocess.run(
        [sys.executable, str(ROOT / "scripts/check-law-coverage.py")],
        capture_output=True,
        text=True,
    )
    assert result.returncode == 0, result.stdout


def test_cli_exits_nonzero_on_a_broken_manifest() -> None:
    manifest = ROOT / "docs/law-coverage.v1.json"
    original = manifest.read_text(encoding="utf-8")
    document = json.loads(original)
    document["records"] = document["records"][:-1]
    try:
        manifest.write_text(json.dumps(document, indent=2) + "\n", encoding="utf-8")
        result = subprocess.run(
            [sys.executable, str(ROOT / "scripts/check-law-coverage.py")],
            capture_output=True,
            text=True,
        )
        assert result.returncode == 1, result.stdout
        assert "undeclared law record" in result.stdout
    finally:
        manifest.write_text(original, encoding="utf-8")


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

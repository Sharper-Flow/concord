#!/usr/bin/env python3
"""Tests for scripts/check-commit-title.py — the guard must reject the subjects
that release silently, accept the vocabulary already on main, and agree with
scripts/release.py about what each accepted subject releases."""
import importlib.util
import subprocess
import sys
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parent.parent

spec = importlib.util.spec_from_file_location(
    "check_commit_title", Path(__file__).with_name("check-commit-title.py")
)
guard = importlib.util.module_from_spec(spec)
spec.loader.exec_module(guard)


def rejects(subject: str) -> bool:
    return bool(guard.findings_for(subject))


def test_non_conventional_subject_is_rejected() -> None:
    # The exact shape that reached main as `Update priorities (#61)`.
    assert rejects("Update priorities"), "a bare imperative subject must be rejected"


def test_near_miss_type_is_rejected() -> None:
    # release.CONVENTIONAL_HEADER parses these cleanly and bumps nothing, which
    # is precisely why an open type vocabulary is unsafe here.
    for subject in ("feature: add worker evidence", "fixes: close the boundary"):
        assert rejects(subject), f"{subject!r} parses but releases nothing"
        parsed = guard.release.parse_commit("0" * 40, subject, "")
        assert parsed.bump is None, "near-miss types must be proven bump-free"


def test_types_in_use_on_main_are_accepted() -> None:
    for commit_type in ("feat", "fix", "docs", "test", "refactor", "ci"):
        subject = f"{commit_type}(store): record worker attempt evidence"
        assert not rejects(subject), f"{subject!r} must be accepted"


def test_remaining_standard_types_are_accepted() -> None:
    for commit_type in ("build", "chore", "perf", "revert", "style"):
        assert not rejects(f"{commit_type}: adjust packaging"), commit_type


def test_scopeless_and_breaking_forms_are_accepted() -> None:
    assert not rejects("feat: add worker evidence")
    assert not rejects("feat!: drop the legacy grant path")
    assert not rejects("feat(store)!: drop the legacy grant path")


def test_breaking_marker_reports_major() -> None:
    assert "major" in guard.describe("feat(store)!: drop the legacy grant path")


def test_describe_matches_release_bump_mapping() -> None:
    assert "minor" in guard.describe("feat: add worker evidence")
    assert "patch" in guard.describe("fix: close the boundary")
    assert "no release bump" in guard.describe("docs: explain the boundary")


def test_malformed_shapes_are_rejected() -> None:
    for subject in (
        "",
        "   ",
        "feat",
        "feat:",
        "feat: ",
        "feat(): add worker evidence",
        "feat:no space after colon",
        "feat: Add worker evidence",
        "feat: add worker evidence.",
        "feat: add worker evidence\nsecond line",
    ):
        assert rejects(subject), f"{subject!r} must be rejected"


def test_acronym_opener_is_accepted() -> None:
    # Sentence-capitalisation is rejected; an acronym is not capitalisation.
    assert not rejects("feat: CLI accepts a seconds budget")


def test_title_length_reserves_room_for_the_squash_reference() -> None:
    body = "a" * (guard.MAX_TITLE_BYTES - len("feat: "))
    assert not rejects(f"feat: {body}")
    assert rejects(f"feat: {body}a")
    assert guard.MAX_TITLE_BYTES < guard.MAX_SUBJECT_BYTES


def test_parser_is_shared_with_release() -> None:
    # A second regex would drift from the one that computes the release.
    source = Path(guard.__file__).read_text(encoding="utf-8")
    assert "release.CONVENTIONAL_HEADER" in source
    assert "re.compile" not in source, "the guard must not define its own grammar"


def test_releasing_types_agree_with_release_module() -> None:
    for commit_type, expected in guard.RELEASING_TYPES.items():
        parsed = guard.release.parse_commit("0" * 40, f"{commit_type}: subject", "")
        assert parsed.bump == expected, f"{commit_type} should bump {expected}"
    for commit_type in guard.NON_RELEASING_TYPES:
        parsed = guard.release.parse_commit("0" * 40, f"{commit_type}: subject", "")
        assert parsed.bump is None, f"{commit_type} must not bump"


def test_cli_exit_codes() -> None:
    script = ROOT / "scripts" / "check-commit-title.py"
    ok = subprocess.run(
        [sys.executable, str(script), "feat: add worker evidence"],
        capture_output=True,
        text=True,
    )
    assert ok.returncode == 0, ok.stderr
    bad = subprocess.run(
        [sys.executable, str(script), "Update priorities"],
        capture_output=True,
        text=True,
    )
    assert bad.returncode == 1, bad.stdout
    missing = subprocess.run(
        [sys.executable, str(script)],
        capture_output=True,
        text=True,
        env={"PATH": "/usr/bin:/bin"},
    )
    assert missing.returncode == 2, "a missing subject must not pass"


def workflow_text() -> str:
    return (ROOT / ".github" / "workflows" / "pr-title.yml").read_text(
        encoding="utf-8"
    )


def workflow_document() -> dict:
    """Parse pr-title.yml so the workflow tests assert structure, not text.

    The assertions below navigate the parsed YAML tree (CD-0055 D1): a deleted
    merge_group trigger, or a step whose condition is edited to `if: false`,
    fails here instead of certifying a gate that will not fire. BaseLoader
    keeps every scalar a string, so the `on:` key stays "on" and a disabled
    condition stays visible as the literal "false". Matching inside a step's
    run value is the tree's boundary: the shell command is a leaf.
    """
    return yaml.load(workflow_text(), Loader=yaml.BaseLoader)


def test_workflow_declares_merge_group() -> None:
    # The pull_request trigger alone cannot see the subject that lands: the
    # merge queue builds the squashed commit before a late rename is checked.
    triggers = workflow_document()["on"]
    assert "merge_group" in triggers, "pr-title.yml must trigger on merge_group"
    assert triggers["merge_group"]["types"] == ["checks_requested"], (
        "the merge queue reports required checks through checks_requested"
    )
    assert "pull_request" in triggers, "pr-title.yml must still gate pull requests"
    assert "edited" in triggers["pull_request"]["types"], (
        "a renamed title must re-run the pull-request check"
    )


def test_workflow_keeps_the_pull_request_title_check() -> None:
    steps = workflow_document()["jobs"]["title"]["steps"]
    gates = [
        step
        for step in steps
        if step.get("if") == "github.event_name == 'pull_request'"
    ]
    assert len(gates) == 1, "exactly one pull_request-gated validation step"
    assert gates[0]["env"]["COMMIT_TITLE"] == (
        "${{ github.event.pull_request.title }}"
    )
    assert gates[0]["run"] == "python3 scripts/check-commit-title.py"


def test_workflow_validates_the_queue_head_subject() -> None:
    # The landed-subject gate must be live on merge_group: a step whose
    # condition is disabled, or whose command drops the guard, cannot certify
    # the subject the queue is about to land.
    job = workflow_document()["jobs"]["title"]
    assert job["name"] == "title", "the required check registers under this name"
    gates = [
        step
        for step in job["steps"]
        if step.get("if") == "github.event_name == 'merge_group'"
    ]
    assert len(gates) == 1, "exactly one merge_group-gated validation step"
    run = gates[0]["run"]
    assert run.startswith(
        "python3 scripts/check-commit-title.py --landed-subject"
    ), "the merge_group step must validate the queue head through the guard"
    assert "git log -1 --format=%s" in run, (
        "the merge_group step must read the queue head commit's subject"
    )


def test_landed_subject_strips_the_squash_reference() -> None:
    assert (
        guard.landed_subject("feat: add worker evidence (#123)")
        == "feat: add worker evidence"
    )
    assert guard.landed_subject("feat: add worker evidence") == (
        "feat: add worker evidence"
    )
    # Only the appended reference is stripped, not a description that ends in
    # a parenthesised word without a reference number.
    assert (
        guard.landed_subject("feat: handle the boundary (edge)")
        == "feat: handle the boundary (edge)"
    )


def test_landed_subject_path_rejects_a_violating_subject() -> None:
    # The exact shape that reached main as `Update priorities (#61)`: stripping
    # the reference must not rescue a subject outside the grammar.
    assert rejects(guard.landed_subject("Update priorities (#61)"))
    long_body = "a" * (guard.MAX_TITLE_BYTES - len("feat: ") + 1)
    assert rejects(guard.landed_subject(f"feat: {long_body} (#1473)"))


def test_landed_subject_path_accepts_a_valid_subject() -> None:
    raw = "fix(store): consume a correction record only when the retry lands (#1473)"
    assert not rejects(guard.landed_subject(raw))
    # The accepted subject measures within the same budget a PR title has.
    assert len(guard.landed_subject(raw).encode("utf-8")) <= guard.MAX_TITLE_BYTES


def test_landed_subject_cli_mode() -> None:
    script = ROOT / "scripts" / "check-commit-title.py"
    ok = subprocess.run(
        [
            sys.executable,
            str(script),
            "--landed-subject",
            "feat: add worker evidence (#12)",
        ],
        capture_output=True,
        text=True,
    )
    assert ok.returncode == 0, ok.stderr
    bad = subprocess.run(
        [sys.executable, str(script), "--landed-subject", "Update priorities (#61)"],
        capture_output=True,
        text=True,
    )
    assert bad.returncode == 1, bad.stdout
    missing = subprocess.run(
        [sys.executable, str(script), "--landed-subject"],
        capture_output=True,
        text=True,
        env={"PATH": "/usr/bin:/bin"},
    )
    assert missing.returncode == 2, "a missing subject must not pass"


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

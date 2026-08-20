#!/usr/bin/env python3
"""Tests for scripts/check-commit-title.py — the guard must reject the subjects
that release silently, accept the vocabulary already on main, and agree with
scripts/release.py about what each accepted subject releases."""
import importlib.util
import subprocess
import sys
from pathlib import Path

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


def test_repository_history_since_the_guard_is_clean() -> None:
    # Every subject the guard will govern going forward must already pass, so
    # the guard cannot be introduced red.
    subjects = subprocess.run(
        ["git", "log", "--format=%s", "-40", "--no-merges"],
        cwd=ROOT,
        capture_output=True,
        text=True,
    )
    if subjects.returncode:
        return
    for subject in subjects.stdout.splitlines():
        # Strip the reference GitHub appends when squashing.
        trimmed = subject.rsplit(" (#", 1)[0] if subject.endswith(")") else subject
        assert not rejects(trimmed), f"history subject rejected: {subject!r}"


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

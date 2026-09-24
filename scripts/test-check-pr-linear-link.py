#!/usr/bin/env python3
"""Tests for scripts/check-pr-linear-link.py — the guard must require exactly
one non-closing Related-to line on work/work-* pull requests, refuse closing
phrases, and leave every other branch and event untouched."""
import importlib.util
import json
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent

spec = importlib.util.spec_from_file_location(
    "check_pr_linear_link", Path(__file__).with_name("check-pr-linear-link.py")
)
guard = importlib.util.module_from_spec(spec)
spec.loader.exec_module(guard)


def test_work_branch_without_related_line_is_rejected() -> None:
    ok, detail = guard.check_pr_link("work/work-421f82d67e7aa19c64a35bda", "feat: add a thing")
    assert not ok, "a work pull request without the line must be rejected"
    assert "Related to <TEAM>-<n>" in detail, detail


def test_related_to_line_is_accepted() -> None:
    body = "feat: add a thing\n\nRelated to CON-427\n"
    ok, detail = guard.check_pr_link("work/work-abc", body)
    assert ok, detail
    assert "CON-427" in detail, detail


def test_related_to_requires_the_whole_line() -> None:
    body = "This change relates to CON-427 and more.\nRelated to stuff CON-427 done"
    ok, _ = guard.check_pr_link("work/work-abc", body)
    assert not ok, "an inline mention must not satisfy the contract"


def test_team_key_vocabulary() -> None:
    ok, _ = guard.check_pr_link("work/work-abc", "Related to ENG-123\n")
    assert ok, "alphanumeric team keys must be accepted"
    ok, _ = guard.check_pr_link("work/work-abc", "Related to con-427\n")
    assert not ok, "a lowercase key is not a Linear issue key"


def test_closing_phrase_is_rejected_even_with_related_line() -> None:
    body = "Related to CON-427\n\nFixes CON-999\n"
    ok, detail = guard.check_pr_link("work/work-abc", body)
    assert not ok, "a closing phrase must fail the check"
    assert "CON-999" in detail, detail


# Linear's closing magic words, verbatim from https://linear.app/docs/github.
LINEAR_CLOSING_WORDS = [
    "close", "closes", "closed", "closing",
    "fix", "fixes", "fixed", "fixing",
    "resolve", "resolves", "resolved", "resolving",
    "complete", "completes", "completed", "completing",
    "implement", "implements", "implemented", "implementing",
    "linear issue",
]


def test_every_linear_closing_word_is_rejected_mid_line() -> None:
    for word in LINEAR_CLOSING_WORDS:
        body = f"Related to CON-427\n\nThe merge {word} CON-999 outright.\n"
        ok, detail = guard.check_pr_link("work/work-abc", body)
        assert not ok, f"{word!r} mid-line before the key must fail the check: {detail}"
        assert "CON-999" in detail, (word, detail)


def test_every_linear_closing_word_is_rejected_at_line_start() -> None:
    for word in LINEAR_CLOSING_WORDS:
        body = f"Related to CON-427\n\n{word.capitalize()}: CON-999\n"
        ok, detail = guard.check_pr_link("work/work-abc", body)
        assert not ok, f"{word!r} at line start must fail the check: {detail}"
        assert "CON-999" in detail, (word, detail)


def test_closing_detection_is_case_insensitive() -> None:
    for word in ("FIXES", "Linear Issue", "Resolving"):
        ok, detail = guard.check_pr_link("work/work-abc", f"{word} CON-999\n")
        assert not ok, f"{word!r} must fail the check: {detail}"
        assert "CON-999" in detail, (word, detail)


def test_a_key_before_the_word_is_not_a_closing_phrase() -> None:
    body = "Related to CON-427\n\nCON-999 was closed last quarter by a different change.\n"
    ok, detail = guard.check_pr_link("work/work-abc", body)
    assert ok, "the word after the key is prose, not a closing phrase"


def test_a_closing_word_without_a_key_after_it_is_not_a_closing_phrase() -> None:
    body = "Related to CON-427\n\nThis change fixes the flaky drain test suite.\n"
    ok, detail = guard.check_pr_link("work/work-abc", body)
    assert ok, "a closing word with no issue key after it must stay accepted"


def test_a_word_that_merely_contains_a_closing_word_is_not_a_closing_phrase() -> None:
    body = "Related to CON-427\n\nThe fixer CON-999 ships in a follow-up.\n"
    ok, detail = guard.check_pr_link("work/work-abc", body)
    assert ok, "fixer is not a closing word"


def test_non_work_branch_passes_without_a_body() -> None:
    ok, detail = guard.check_pr_link("feature/thing", "")
    assert ok, detail
    assert "not a Concord work branch" in detail, detail


QUEUE_REF = "refs/heads/gh-readonly-queue/main/pr-1350-6dc40f1b3d345e898fec2f0e7a9fe9518c9336bd"


def test_merge_group_resolves_the_queued_pull_request() -> None:
    calls = []

    def runner(argv, **kwargs):
        calls.append(argv)
        payload = {"headRefName": "work/work-abc", "body": "Related to CON-425\n"}
        return subprocess.CompletedProcess(argv, 0, stdout=json.dumps(payload), stderr="")

    head_ref, body = guard.resolve_pr("merge_group", {"merge_group": {"head_ref": QUEUE_REF}}, runner)
    assert head_ref == "work/work-abc", head_ref
    assert body == "Related to CON-425\n", body
    assert calls == [["gh", "pr", "view", "1350", "--json", "headRefName,body"]], calls
    ok, _ = guard.check_pr_link(head_ref, body)
    assert ok, "a merge queue entry carrying the line must pass"


def test_merge_group_checks_the_queued_work_branch() -> None:
    def runner(argv, **kwargs):
        payload = {"headRefName": "work/work-abc", "body": "no link"}
        return subprocess.CompletedProcess(argv, 0, stdout=json.dumps(payload), stderr="")

    head_ref, body = guard.resolve_pr("merge_group", {"merge_group": {"head_ref": QUEUE_REF}}, runner)
    ok, _ = guard.check_pr_link(head_ref, body)
    assert not ok, "a queued work branch without the line must fail"


def test_unrecognized_merge_group_ref_refuses() -> None:
    try:
        guard.resolve_pr("merge_group", {"merge_group": {"head_ref": "refs/heads/work/work-abc"}}, None)
    except SystemExit as err:
        assert "merge queue head" in str(err), err
    else:
        raise AssertionError("an unrecognized merge queue ref must refuse closed")


def test_gh_failure_refuses() -> None:
    def runner(argv, **kwargs):
        return subprocess.CompletedProcess(argv, 1, stdout="", stderr="no pull request found")

    try:
        guard.resolve_pr("merge_group", {"merge_group": {"head_ref": QUEUE_REF}}, runner)
    except SystemExit as err:
        assert "cannot read pull request" in str(err), err
    else:
        raise AssertionError("a failed gh read must refuse closed")


def test_cli_passes_on_non_pull_request_events() -> None:
    with tempfile.TemporaryDirectory() as directory:
        event_path = Path(directory) / "event.json"
        event_path.write_text(json.dumps({}), encoding="utf-8")
        exit_code = subprocess.run(
            [sys.executable, str(Path(__file__).with_name("check-pr-linear-link.py")), "push", str(event_path)],
            capture_output=True,
            text=True,
        ).returncode
    assert exit_code == 0, "a push event has no pull request to check"


def test_cli_rejects_work_pull_request_missing_line() -> None:
    with tempfile.TemporaryDirectory() as directory:
        event_path = Path(directory) / "event.json"
        event_path.write_text(
            json.dumps({"pull_request": {"head": {"ref": "work/work-abc"}, "body": "no line here"}}),
            encoding="utf-8",
        )
        result = subprocess.run(
            [sys.executable, str(Path(__file__).with_name("check-pr-linear-link.py")), "pull_request", str(event_path)],
            capture_output=True,
            text=True,
        )
    assert result.returncode == 1, result.stdout
    assert "Related to <TEAM>-<n>" in result.stdout, result.stdout


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

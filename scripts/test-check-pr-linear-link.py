#!/usr/bin/env python3
"""Tests for scripts/check-pr-linear-link.py — the guard must require exactly
one non-closing Related-to line on work/work-* pull requests, refuse closing
phrases in the title and the body (Linear parses both), and leave every other
branch and event untouched."""
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

# The check owns `.github/workflows/pr-linear-link.yml` because a pull
# request `types` list is a workflow-level trigger field: adding `edited`
# inside ci.yml would re-run every CI job on a body edit.
GUARD_WORKFLOW = ROOT / ".github/workflows/pr-linear-link.yml"
CI_WORKFLOW = ROOT / ".github/workflows/ci.yml"


def test_work_branch_without_related_line_is_rejected() -> None:
    ok, detail = guard.check_pr_link("work/work-421f82d67e7aa19c64a35bda", "feat: add a thing", "feat: add a thing")
    assert not ok, "a work pull request without the line must be rejected"
    assert "Related to <TEAM>-<n>" in detail, detail


def test_related_to_line_is_accepted() -> None:
    body = "feat: add a thing\n\nRelated to CON-427\n"
    ok, detail = guard.check_pr_link("work/work-abc", "feat: add a thing", body)
    assert ok, detail
    assert "CON-427" in detail, detail


def test_related_to_requires_the_whole_line() -> None:
    body = "This change relates to CON-427 and more.\nRelated to stuff CON-427 done"
    ok, _ = guard.check_pr_link("work/work-abc", "feat: add a thing", body)
    assert not ok, "an inline mention must not satisfy the contract"


def test_team_key_vocabulary() -> None:
    ok, _ = guard.check_pr_link("work/work-abc", "feat: add a thing", "Related to ENG-123\n")
    assert ok, "alphanumeric team keys must be accepted"
    ok, _ = guard.check_pr_link("work/work-abc", "feat: add a thing", "Related to con-427\n")
    assert not ok, "a lowercase key is not a Linear issue key"


def test_closing_phrase_is_rejected_even_with_related_line() -> None:
    body = "Related to CON-427\n\nFixes CON-999\n"
    ok, detail = guard.check_pr_link("work/work-abc", "feat: add a thing", body)
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
        ok, detail = guard.check_pr_link("work/work-abc", "feat: add a thing", body)
        assert not ok, f"{word!r} mid-line before the key must fail the check: {detail}"
        assert "CON-999" in detail, (word, detail)


def test_every_linear_closing_word_is_rejected_at_line_start() -> None:
    for word in LINEAR_CLOSING_WORDS:
        body = f"Related to CON-427\n\n{word.capitalize()}: CON-999\n"
        ok, detail = guard.check_pr_link("work/work-abc", "feat: add a thing", body)
        assert not ok, f"{word!r} at line start must fail the check: {detail}"
        assert "CON-999" in detail, (word, detail)


def test_closing_detection_is_case_insensitive() -> None:
    for word in ("FIXES", "Linear Issue", "Resolving"):
        ok, detail = guard.check_pr_link("work/work-abc", "feat: add a thing", f"{word} CON-999\n")
        assert not ok, f"{word!r} must fail the check: {detail}"
        assert "CON-999" in detail, (word, detail)


def test_a_key_before_the_word_is_not_a_closing_phrase() -> None:
    body = "Related to CON-427\n\nCON-999 was closed last quarter by a different change.\n"
    ok, _ = guard.check_pr_link("work/work-abc", "feat: add a thing", body)
    assert ok, "the word after the key is prose, not a closing phrase"


def test_a_closing_word_without_a_key_after_it_is_not_a_closing_phrase() -> None:
    body = "Related to CON-427\n\nThis change fixes the flaky drain test suite.\n"
    ok, _ = guard.check_pr_link("work/work-abc", "feat: add a thing", body)
    assert ok, "a closing word with no issue key after it must stay accepted"


def test_a_word_that_merely_contains_a_closing_word_is_not_a_closing_phrase() -> None:
    body = "Related to CON-427\n\nThe fixer CON-999 ships in a follow-up.\n"
    ok, _ = guard.check_pr_link("work/work-abc", "feat: add a thing", body)
    assert ok, "fixer is not a closing word"


# Linear accepts a closing magic word before a Linear issue URL as well as a
# bare key (https://linear.app/docs/github): `Fixes
# https://linear.app/workspace/issue/ENG-123/title` closes ENG-123 too.


def test_every_linear_closing_word_is_rejected_before_a_linear_issue_url() -> None:
    for word in LINEAR_CLOSING_WORDS:
        body = (
            "Related to CON-427\n\n"
            f"{word.capitalize()} https://linear.app/example/issue/CON-999/some-slug\n"
        )
        ok, detail = guard.check_pr_link("work/work-abc", "feat: add a thing", body)
        assert not ok, f"{word!r} before a Linear issue URL must fail the check: {detail}"
        assert "CON-999" in detail, (word, detail)


def test_a_closing_word_before_a_slugless_linear_issue_url_is_rejected() -> None:
    body = "Related to CON-427\n\nFixes https://linear.app/example/issue/CON-999\n"
    ok, detail = guard.check_pr_link("work/work-abc", "feat: add a thing", body)
    assert not ok, "a Linear issue URL without a slug still closes the issue"
    assert "CON-999" in detail, detail


def test_a_closing_word_before_a_linear_issue_url_in_the_title_is_rejected() -> None:
    ok, detail = guard.check_pr_link(
        "work/work-abc",
        "Fixes https://linear.app/example/issue/CON-77/title",
        "Related to CON-427\n",
    )
    assert not ok, "a Linear issue URL after a closing word in the title must fail the check"
    assert "title" in detail and "CON-77" in detail, detail


def test_a_closing_word_before_a_non_linear_url_is_not_a_closing_phrase() -> None:
    body = "Related to CON-427\n\nFixes https://github.com/example/repo/issues/999\n"
    ok, _ = guard.check_pr_link("work/work-abc", "feat: add a thing", body)
    assert ok, "a GitHub issue URL is not a Linear issue and closes nothing here"


def test_a_linear_issue_url_without_a_closing_word_is_prose() -> None:
    body = "Related to CON-427\n\nSee https://linear.app/example/issue/CON-999/some-slug for context.\n"
    ok, _ = guard.check_pr_link("work/work-abc", "feat: add a thing", body)
    assert ok, "a bare Linear issue URL with no closing word links, but closes nothing"


def test_a_linear_issue_url_that_is_not_an_issue_page_is_not_a_closing_phrase() -> None:
    body = "Related to CON-427\n\nFixes https://linear.app/example/project/CON-999\n"
    ok, _ = guard.check_pr_link("work/work-abc", "feat: add a thing", body)
    assert ok, "a linear.app URL without /issue/ does not name an issue key"


# Linear parses a closing magic word in the pull request title as well as the
# body (https://linear.app/docs/github), so the title gets the same scan.


def test_a_closing_phrase_in_the_title_is_rejected() -> None:
    ok, detail = guard.check_pr_link("work/work-abc", "Fixes CON-427", "Related to CON-999\n")
    assert not ok, "a work pull request titled with a closing phrase must be rejected"
    assert "title" in detail, detail
    assert "CON-427" in detail, detail


def test_every_linear_closing_word_is_rejected_in_the_title() -> None:
    for word in LINEAR_CLOSING_WORDS:
        ok, detail = guard.check_pr_link("work/work-abc", f"{word.capitalize()} CON-999", "Related to CON-427\n")
        assert not ok, f"{word!r} in the title must fail the check: {detail}"
        assert "CON-999" in detail, (word, detail)


def test_title_closing_detection_is_case_insensitive() -> None:
    ok, detail = guard.check_pr_link("work/work-abc", "FIXES CON-999", "Related to CON-427\n")
    assert not ok, "an uppercase closing word in the title must fail the check"
    assert "CON-999" in detail, detail


def test_a_title_without_a_closing_phrase_passes() -> None:
    ok, detail = guard.check_pr_link("work/work-abc", "concord: add the thing", "Related to CON-427\n")
    assert ok, detail


def test_a_title_closing_word_without_a_key_is_prose() -> None:
    ok, _ = guard.check_pr_link("work/work-abc", "Fix the flaky drain suite", "Related to CON-427\n")
    assert ok, "a closing word with no issue key after it must stay accepted"


def test_a_title_key_before_the_word_is_prose() -> None:
    ok, _ = guard.check_pr_link("work/work-abc", "CON-999 was fixed elsewhere", "Related to CON-427\n")
    assert ok, "the word after the key is prose, not a closing phrase"


def test_a_title_mention_is_not_the_related_line() -> None:
    ok, _ = guard.check_pr_link("work/work-abc", "Related to CON-427", "no line here")
    assert not ok, "the Related-to line is required in the body, not the title"


def test_non_work_branch_passes_without_a_body() -> None:
    ok, detail = guard.check_pr_link("feature/thing", "", "")
    assert ok, detail
    assert "not a Concord work branch" in detail, detail


QUEUE_REF = "refs/heads/gh-readonly-queue/main/pr-1350-6dc40f1b3d345e898fec2f0e7a9fe9518c9336bd"


def test_merge_group_resolves_the_queued_pull_request() -> None:
    calls = []

    def runner(argv, **kwargs):
        calls.append(argv)
        payload = {"headRefName": "work/work-abc", "title": "feat: add a thing", "body": "Related to CON-425\n"}
        return subprocess.CompletedProcess(argv, 0, stdout=json.dumps(payload), stderr="")

    head_ref, title, body = guard.resolve_pr("merge_group", {"merge_group": {"head_ref": QUEUE_REF}}, runner)
    assert head_ref == "work/work-abc", head_ref
    assert title == "feat: add a thing", title
    assert body == "Related to CON-425\n", body
    assert calls == [["gh", "pr", "view", "1350", "--json", "headRefName,title,body"]], calls
    ok, _ = guard.check_pr_link(head_ref, title, body)
    assert ok, "a merge queue entry carrying the line must pass"


def test_merge_group_checks_the_queued_work_branch() -> None:
    def runner(argv, **kwargs):
        payload = {"headRefName": "work/work-abc", "title": "feat: add a thing", "body": "no link"}
        return subprocess.CompletedProcess(argv, 0, stdout=json.dumps(payload), stderr="")

    head_ref, title, body = guard.resolve_pr("merge_group", {"merge_group": {"head_ref": QUEUE_REF}}, runner)
    ok, _ = guard.check_pr_link(head_ref, title, body)
    assert not ok, "a queued work branch without the line must fail"


def test_merge_group_checks_the_queued_title() -> None:
    def runner(argv, **kwargs):
        payload = {"headRefName": "work/work-abc", "title": "Fixes CON-77", "body": "Related to CON-425\n"}
        return subprocess.CompletedProcess(argv, 0, stdout=json.dumps(payload), stderr="")

    head_ref, title, body = guard.resolve_pr("merge_group", {"merge_group": {"head_ref": QUEUE_REF}}, runner)
    ok, detail = guard.check_pr_link(head_ref, title, body)
    assert not ok, "a queued work branch whose title closes an issue must fail"
    assert "title" in detail and "CON-77" in detail, detail


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


def test_cli_rejects_work_pull_request_closing_title() -> None:
    with tempfile.TemporaryDirectory() as directory:
        event_path = Path(directory) / "event.json"
        event_path.write_text(
            json.dumps({"pull_request": {"head": {"ref": "work/work-abc"}, "title": "Fixes CON-427", "body": "Related to CON-427\n"}}),
            encoding="utf-8",
        )
        result = subprocess.run(
            [sys.executable, str(Path(__file__).with_name("check-pr-linear-link.py")), "pull_request", str(event_path)],
            capture_output=True,
            text=True,
        )
    assert result.returncode == 1, result.stdout
    assert "title" in result.stdout and "CON-427" in result.stdout, result.stdout


def test_guard_workflow_reruns_on_a_body_edit() -> None:
    text = GUARD_WORKFLOW.read_text(encoding="utf-8")
    assert "pull_request:" in text, "the guard workflow must trigger on pull_request"
    types = text.split("types:", 1)[1].split("\n", 1)[0]
    for event_type in ("opened", "synchronize", "reopened", "edited"):
        assert event_type in types, f"pull_request types must include {event_type}: {types}"


def test_guard_workflow_carries_the_merge_queue_gate() -> None:
    text = GUARD_WORKFLOW.read_text(encoding="utf-8")
    assert "merge_group:" in text, "the required check must report on merge queue entries"


def test_guard_job_runs_the_check_with_scoped_permission() -> None:
    text = GUARD_WORKFLOW.read_text(encoding="utf-8")
    assert "verify-pr-linear-link:" in text, "the job name is the required check name"
    assert "pull-requests: read" in text, "only the merge queue path needs pull_requests read"
    assert "python3 scripts/check-pr-linear-link.py" in text, "the job must run the guard"


def test_ci_workflow_keeps_its_jobs_off_body_edits() -> None:
    text = CI_WORKFLOW.read_text(encoding="utf-8")
    assert "verify-pr-linear-link:" not in text, "the check moved to its own workflow"
    assert "types:" not in text, "ci.yml keeps the default pull_request types; edited must not re-run its jobs"


def test_ci_workflow_no_longer_grants_pull_requests_read() -> None:
    text = CI_WORKFLOW.read_text(encoding="utf-8")
    assert "pull-requests: read" not in text, "no ci.yml job reads a pull request through gh"


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

#!/usr/bin/env python3
"""CD-0213 D5 pull request linkage check.

Every Concord-managed pull request carries one non-closing `Related to
<TEAM>-<n>` line in its body. The phrase links the pull request to its Linear
issue without a status change. CD-0213 D5 leaves issue status changes to the
Linear MCP server or the operator; D6 routes foreign-Product planning to that
Product's Linear team. A closing phrase such as `Fixes` would let the merge
change issue status outside that route, so this check refuses one. Linear
parses a closing magic word in the pull request title as well as the body
(https://linear.app/docs/github), so both surfaces get the same scan.

The branch name marks Concord-managed work, not the author. Linkage applies
only to `work/work-*`; other branches have no linkage requirement.

On `pull_request` events the body and head branch come from the event
payload. A `merge_group` payload names only the queue branch
`gh-readonly-queue/<base>/pr-<n>-<sha>`, so the queued pull request's head
branch and body are read with `gh pr view <n>`; the job grants
`pull-requests: read` for that call.

Usage in CI reads GITHUB_EVENT_NAME and GITHUB_EVENT_PATH. For local runs:
    check-pr-linear-link.py <event-name> <event-payload.json>
"""
from __future__ import annotations

import json
import os
import re
import subprocess
import sys

# CD-0213 D5 requires the relation phrase on its own line, so an inline
# mention does not satisfy the contract.
RELATED_LINE = re.compile(r"^[ \t]*Related to[ \t]+([A-Z][A-Z0-9]*-[0-9]+)[ \t]*$", re.MULTILINE)

# Closing phrases on an issue key would let the merge close or move the issue.
# CD-0213 D5 forbids them, even when a Related-to line also exists. The words
# are Linear's closing magic words, verbatim from https://linear.app/docs/github.
# Linear accepts a closing word
# before a bare issue key or before a Linear issue URL that contains the key
# (for example `Fixes https://linear.app/workspace/issue/ENG-123/title`), so
# both tails close. Linear parses a closing phrase in the pull request title
# as well as the body, so both surfaces get the same scan: the word may appear
# anywhere as long as the key follows it directly or inside a linear.app issue
# URL; a key before the word, or a word with no key or Linear URL after it, is
# prose and stays accepted.
LINEAR_ISSUE_URL = r"https://linear\.app/[^/\s]+/issue/([A-Z][A-Z0-9]*-[0-9]+)"
CLOSING_LINE = re.compile(
    r"\b(?:close|closes|closed|closing|fix|fixes|fixed|fixing"
    r"|resolve|resolves|resolved|resolving"
    r"|complete|completes|completed|completing"
    r"|implement|implements|implemented|implementing"
    r"|linear[ \t]+issue)\b[ \t]*:?[ \t]+(?:"
    r"([A-Z][A-Z0-9]*-[0-9]+)|" + LINEAR_ISSUE_URL + r")",
    re.IGNORECASE,
)

WORK_BRANCH = re.compile(r"^work/work-")

# A merge queue entry runs on a queue branch that names the queued pull
# request, never on the work branch itself.
QUEUE_REF = re.compile(r"^(?:refs/heads/)?gh-readonly-queue/[^/]+(?:/[^/]+)*/pr-([0-9]+)-[0-9a-f]+$")


def related_key(body: str) -> str | None:
    """Return the issue key of the first Related-to line, or None."""
    match = RELATED_LINE.search(body or "")
    return match.group(1) if match else None


def closing_phrases(text: str) -> list[tuple[str, str]]:
    """Return (phrase, key) for every closing phrase, from a bare key or a Linear issue URL.

    Any <TEAM>-<n> token counts. Whether Linear acts on one depends on the
    teams and issues its workspace holds, which this check does not read, so
    prose of the same shape such as `complete UTF-8` refuses as well.
    """
    return [
        (match.group(0), match.group(1) or match.group(2))
        for match in CLOSING_LINE.finditer(text or "")
    ]


def closing_refusal(where: str, phrases: list[tuple[str, str]]) -> tuple[bool, str]:
    """Build the refusal for the closing phrases found on one pull request surface."""
    keys = sorted({key for _, key in phrases})
    quoted = ", ".join(f"'{phrase}'" for phrase, _ in phrases)
    return False, (
        f"the {where} has the closing phrase{'' if len(phrases) == 1 else 's'} {quoted}, "
        f"which Linear treats as closing {' and '.join(keys)} if such an issue exists; "
        "CD-0213 D5 leaves issue status changes to the Linear MCP server or the operator. "
        "This check cannot see which Linear teams and issues exist, so it treats any "
        "<TEAM>-<n> token directly after a closing word as an issue key. If the token is "
        "ordinary text, put another word between the closing word and the token, or drop "
        "the closing word. If it names an issue, link it with a 'Related to <TEAM>-<n>' "
        "line in the body instead"
    )


def check_pr_link(head_ref: str, title: str, body: str) -> tuple[bool, str]:
    """Check one pull request. Returns (ok, detail)."""
    if not WORK_BRANCH.match(head_ref or ""):
        return True, f"head branch {head_ref!r} is not a Concord work branch; no linkage required"
    # Linear parses closing magic words in the PR title as well as the body,
    # so a work-branch PR titled 'Fixes ENG-123' would change issue status without
    # the Linear MCP server or the operator. Both surfaces get the same refusal.
    for where, text in (("title", title), ("body", body)):
        closing = closing_phrases(text)
        if closing:
            return closing_refusal(where, closing)
    key = related_key(body)
    if key is None:
        return False, (
            "a Concord work pull request must carry a line of the form "
            "'Related to <TEAM>-<n>' naming its Linear issue"
        )
    return True, f"linked to {key}"


def queued_pr_number(queue_ref: str) -> str:
    """Return the pull request number a merge queue head branch names."""
    match = QUEUE_REF.search(queue_ref or "")
    if match is None:
        raise SystemExit(f"cannot identify the pull request of merge queue head {queue_ref!r}")
    return match.group(1)


def read_queued_pr(queue_ref: str, runner=subprocess.run) -> dict:
    """Read metadata for the pull request behind a merge queue entry."""
    number = queued_pr_number(queue_ref)
    result = runner(
        ["gh", "pr", "view", number, "--json", "headRefName,title,body"],
        capture_output=True,
        text=True,
        timeout=30,
    )
    if result.returncode != 0:
        raise SystemExit(f"cannot read pull request {number}: {result.stderr.strip()}")
    try:
        pull_request = json.loads(result.stdout)
    except json.JSONDecodeError as err:
        raise SystemExit(f"cannot read pull request {number}: {err}") from err
    if not isinstance(pull_request, dict):
        raise SystemExit(f"cannot read pull request {number}: expected an object")
    return pull_request


def resolve_pr(event_name: str, event: dict, runner=subprocess.run) -> dict:
    """Normalize metadata for the pull request this run validates."""
    if event_name == "pull_request":
        pull_request = event.get("pull_request") or {}
        metadata = {
            "headRefName": pull_request.get("head", {}).get("ref", ""),
            "title": pull_request.get("title") or "",
            "body": pull_request.get("body") or "",
        }
    elif event_name == "merge_group":
        merge_group = event.get("merge_group") or {}
        metadata = read_queued_pr(merge_group.get("head_ref", ""), runner)
    else:
        return {}
    head_ref = metadata.get("headRefName")
    if not isinstance(head_ref, str) or not head_ref:
        raise SystemExit("cannot read pull request metadata: invalid or missing headRefName")
    return metadata


def main(argv: list[str]) -> int:
    if len(argv) == 2:
        event_name, event_path = argv
    else:
        event_name = os.environ.get("GITHUB_EVENT_NAME", "")
        event_path = os.environ.get("GITHUB_EVENT_PATH", "")
    if event_name not in ("pull_request", "merge_group"):
        print(f"no pull request to check for event {event_name!r}; ok")
        return 0
    try:
        with open(event_path, encoding="utf-8") as handle:
            event = json.load(handle)
    except (OSError, json.JSONDecodeError) as err:
        print(f"cannot read the event payload at {event_path!r}: {err}")
        return 1
    pull_request = resolve_pr(event_name, event)
    head_ref = pull_request.get("headRefName") or ""
    link_ok, link_detail = check_pr_link(head_ref, pull_request.get("title") or "", pull_request.get("body") or "")
    report = f"pull request {head_ref!r}: {link_detail}\n"
    print(report, end="")
    summary = os.environ.get("GITHUB_STEP_SUMMARY")
    if summary:
        with open(summary, "a", encoding="utf-8") as handle:
            handle.write(report)
    return 0 if link_ok else 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))

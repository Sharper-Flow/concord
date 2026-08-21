#!/usr/bin/env python3
"""Release-authority guard for the subject line that reaches main.

scripts/release.py computes the next version from Conventional Commit history,
so a merged subject line is release authority, not style. The repository merges
by squash with squash_merge_commit_title=PR_TITLE, which makes the pull-request
title the verbatim commit subject on main.

The failure this guard closes is silent rather than loud. release.parse_commit
maps an unparseable subject to commit_type=None and bump=None, and it maps an
unrecognised type the same way. A feature merged as "Add worker evidence", or
as "feature: add worker evidence", therefore contributes no minor bump and no
changelog entry, and the release still succeeds — at the wrong version. Nothing
in CI observes the difference. `Update priorities (#61)` reached main that way.

The parser is imported from scripts/release.py rather than restated here. A
second regex would be a second vocabulary, and the two would drift; the point
of the guard is that what CI accepts and what the release reads are the same
grammar by construction.

The type vocabulary is the one already in use on main (feat, fix, docs, test,
refactor, ci) plus the remaining standard Conventional Commit types. It is
closed on purpose: release.py's header regex accepts any identifier as a type,
so an unclosed vocabulary would let "feature:" and "fixes:" pass here and still
bump nothing downstream.

Usage:
    check-commit-title.py "feat(store): add worker evidence"
    check-commit-title.py --stdin < title.txt
"""
import os
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "scripts"))

import release  # noqa: E402  the release module is the grammar authority

# Types that bump the version. release.parse_commit is the authority for the
# mapping; this set exists so the guard can explain the consequence.
RELEASING_TYPES = {"feat": "minor", "fix": "patch"}

# Types accepted with no version bump. Additions here are a deliberate widening
# of release vocabulary and belong in review, not in a passing build.
NON_RELEASING_TYPES = {
    "build",
    "chore",
    "ci",
    "docs",
    "perf",
    "refactor",
    "revert",
    "style",
    "test",
}

ALLOWED_TYPES = set(RELEASING_TYPES) | NON_RELEASING_TYPES

# GitHub appends " (#123)" to the squashed subject, so the title checked here
# is shorter than the subject that lands on main. Reserve room for a five-digit
# reference rather than measuring a length that is not the one git will store.
MAX_SUBJECT_BYTES = 100
SQUASH_REFERENCE_RESERVE = len(" (#99999)")
MAX_TITLE_BYTES = MAX_SUBJECT_BYTES - SQUASH_REFERENCE_RESERVE


def findings_for(subject: str) -> list[str]:
    """Return the reasons this subject is unfit to become a commit on main."""
    findings: list[str] = []
    stripped = subject.strip()
    if not stripped:
        return ["empty subject: a release cannot be computed from a blank title"]
    if "\n" in subject:
        return ["multi-line subject: the commit subject must be a single line"]

    match = release.CONVENTIONAL_HEADER.fullmatch(stripped)
    if match is None:
        return [
            f"not a Conventional Commit subject: {stripped!r}\n"
            "  expected <type>[(scope)][!]: <subject>, for example "
            "'feat(store): record worker attempt evidence'\n"
            "  release.py reads this subject verbatim; an unparseable subject "
            "contributes no version bump and no changelog entry"
        ]

    commit_type = match.group("type").lower()
    if commit_type not in ALLOWED_TYPES:
        findings.append(
            f"unknown commit type {commit_type!r}\n"
            f"  allowed: {', '.join(sorted(ALLOWED_TYPES))}\n"
            "  release.py bumps only on 'feat' and 'fix'; an unrecognised type "
            "parses cleanly and silently releases nothing"
        )

    scope = match.group("scope")
    if scope is not None and not scope.strip():
        findings.append("empty scope: write '<type>: ...' rather than '<type>(): ...'")

    body = match.group("subject")
    if not body.strip():
        findings.append("empty description after the colon")
    elif body[0].isupper() and not body.split(" ", 1)[0].isupper():
        # Reject "feat: Add x" but allow acronym openers such as "feat: CLI ...".
        findings.append(
            f"description should not be sentence-capitalised: {body!r}"
        )
    elif body.rstrip().endswith("."):
        findings.append("description should not end with a period")

    if len(stripped.encode("utf-8")) > MAX_TITLE_BYTES:
        findings.append(
            f"title is {len(stripped.encode('utf-8'))} bytes; keep it within "
            f"{MAX_TITLE_BYTES} so the squashed subject stays within "
            f"{MAX_SUBJECT_BYTES} once GitHub appends the pull-request reference"
        )

    return findings


def describe(subject: str) -> str:
    """Report the release consequence of an accepted subject."""
    commit = release.parse_commit("0" * 40, subject.strip(), "")
    if commit.breaking:
        return f"{subject.strip()!r} -> major release bump"
    if commit.bump:
        return f"{subject.strip()!r} -> {commit.bump} release bump"
    return f"{subject.strip()!r} -> no release bump"


def main(argv: list[str] | None = None) -> int:
    args = list(sys.argv[1:] if argv is None else argv)
    if args and args[0] == "--stdin":
        lines = sys.stdin.read().splitlines()
        subject = lines[0] if lines else ""
    elif args:
        subject = args[0]
    else:
        subject = os.environ.get("COMMIT_TITLE", "")

    if not subject:
        print(
            "commit title check failed: no subject supplied "
            "(pass one as an argument, via --stdin, or in COMMIT_TITLE)",
            file=sys.stderr,
        )
        return 2

    findings = findings_for(subject)
    for finding in findings:
        print(finding)
    if findings:
        print(
            f"commit title check failed: {len(findings)} finding(s)", file=sys.stderr
        )
        return 1
    print(f"commit title check passed: {describe(subject)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

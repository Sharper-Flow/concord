#!/usr/bin/env python3
"""Keep AGENTS.md a bounded pointer file rather than a drifting copy of its sources.

AGENTS.md is the only repository file loaded into every agent's context
automatically, so its size is a permanent tax and its accuracy is unverified by
any other check. scripts/check-doc-links.py validates Markdown and HTML link
forms only, which leaves backticked paths and reproduced command lines checked
by nothing. Three structural rules close that gap:

  budget    the file may not exceed MAX_LINES lines
  paths     a backticked repository path must resolve on disk
  commands  a CI command line may not be reproduced verbatim
"""

from __future__ import annotations

import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
MAX_FINDINGS = 200
MAX_LINES = 140

AGENTS_MD = ROOT / "AGENTS.md"
CI_WORKFLOW = ROOT / ".github" / "workflows" / "ci.yml"

CODE_SPAN_RE = re.compile(r"`([^`\n]+)`")
RUN_SCALAR_RE = re.compile(r"^(\s*)run:\s*(\S.*)$")
RUN_BLOCK_RE = re.compile(r"^(\s*)run:\s*[|>][-+]?\s*$")

# A token carrying any of these is a pattern, placeholder, or glob rather than a
# concrete path, so it cannot be resolved against the filesystem.
PATH_PLACEHOLDERS = ("*", "{", "}", "NNNN")


def repository_files() -> list[Path]:
    result = subprocess.run(
        ["git", "ls-files", "-co", "--exclude-standard"],
        cwd=ROOT,
        check=True,
        capture_output=True,
        text=True,
    )
    return sorted({ROOT / line for line in result.stdout.splitlines() if line})


def tracked_roots() -> set[str]:
    """First path segment of every repository file, closing the path namespace.

    A backticked token is treated as a repository path only when it opens with
    one of these. That keeps prose such as `s.db` or `go test` out of the path
    rules without maintaining a hand-written exclusion list.
    """
    return {path.relative_to(ROOT).parts[0] for path in repository_files()}


def ci_command_lines(workflow: Path) -> set[str]:
    """Every command CI runs, plus each &&-joined segment of a chained step."""
    if not workflow.is_file():
        return set()

    commands: list[str] = []
    lines = workflow.read_text(encoding="utf-8").splitlines()
    index = 0
    while index < len(lines):
        line = lines[index]
        block = RUN_BLOCK_RE.match(line)
        if block:
            indent = len(block.group(1))
            index += 1
            while index < len(lines):
                nested = lines[index]
                if nested.strip() and len(nested) - len(nested.lstrip()) <= indent:
                    break
                commands.append(nested.strip())
                index += 1
            continue
        scalar = RUN_SCALAR_RE.match(line)
        if scalar:
            commands.append(scalar.group(2).strip())
        index += 1

    banned: set[str] = set()
    for command in commands:
        for segment in [command, *command.split("&&")]:
            candidate = segment.strip()
            # Require an argument. A bare program name is a legitimate mention;
            # a program with its arguments is a reproduction of the CI contract.
            if " " in candidate:
                banned.add(candidate)
    return banned


def check_budget(lines: list[str], findings: list[str]) -> None:
    if len(lines) > MAX_LINES:
        findings.append(
            f"AGENTS.md:{MAX_LINES + 1}: exceeds the context budget of "
            f"{MAX_LINES} lines: file has {len(lines)}"
        )


def check_paths(lines: list[str], roots: set[str], findings: list[str]) -> None:
    for number, line in enumerate(lines, start=1):
        for span in CODE_SPAN_RE.findall(line):
            token = span.strip()
            if not token or any(mark in token for mark in PATH_PLACEHOLDERS):
                continue
            if any(character.isspace() for character in token):
                continue
            candidate = token.rstrip("/")
            if "/" not in candidate and candidate not in roots:
                continue
            if candidate.split("/")[0] not in roots:
                continue
            if not (ROOT / candidate).exists():
                findings.append(
                    f"AGENTS.md:{number}: named path does not exist: {candidate}"
                )


def check_ci_commands(
    lines: list[str], banned: set[str], findings: list[str]
) -> None:
    for number, line in enumerate(lines, start=1):
        for command in banned:
            if command in line:
                findings.append(
                    f"AGENTS.md:{number}: reproduces a CI command line; link to "
                    f".github/workflows/ci.yml instead: {command}"
                )


def main() -> int:
    findings: list[str] = []

    if not AGENTS_MD.is_file():
        print("AGENTS.md: missing", file=sys.stderr)
        return 1

    lines = AGENTS_MD.read_text(encoding="utf-8").splitlines()
    check_budget(lines, findings)
    check_paths(lines, tracked_roots(), findings)
    check_ci_commands(lines, ci_command_lines(CI_WORKFLOW), findings)

    for finding in sorted(findings)[:MAX_FINDINGS]:
        print(finding)
    if len(findings) > MAX_FINDINGS:
        print(f"... {len(findings) - MAX_FINDINGS} additional finding(s) omitted")
    if findings:
        print(
            f"AGENTS.md check failed: {len(findings)} finding(s)",
            file=sys.stderr,
        )
        return 1

    print("AGENTS.md check passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

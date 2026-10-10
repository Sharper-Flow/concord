#!/usr/bin/env python3
"""Reject planning identifiers and history narration in added comments and docs.

The conduct corpus owns the rule (.concord/instructions/change.md): a committed
comment or document carries durable content, and change history and provenance
belong in commits, pull requests, and work records. This check enforces the
mechanical part of that rule on the lines a change adds. It diffs the working
tree against the merge base of --base-ref and HEAD, and counts every line of an
untracked file as added, so content already on the base never fails a change.

Inspected text, by file kind:

- Markdown: every added line.
- Go, TypeScript, and JavaScript: comment text. The whole new file is lexed, so
  string, raw-string, and template contents are never read as comments. A
  regular-expression literal is not recognized; a `//` inside one reads as a
  comment start.
- Python: comments and docstrings, read with tokenize and ast.
- Shell, YAML, TOML, and extensionless shebang files: full-line `#` comments.

Every other file kind is not inspected. The EXEMPT table lists each exempt path
class explicitly; the check applies no other exemption.
"""

from __future__ import annotations

import argparse
import ast
import fnmatch
import io
import re
import subprocess
import sys
import tokenize
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]

EXEMPT: dict[str, tuple[str, ...]] = {
    # Regenerated from their inputs; the inputs are inspected instead.
    "generated": (
        ".opencode/agents/*",
        "adapter/opencode/generated-*",
        "internal/*/generated_*.go",
        ".concord/navigation/*",
        ".concord/docs/agent-lanes-contract.md",
        ".concord/docs/generated-agent-tool-surface.md",
    ),
    # Quoted inputs and expected outputs that tests compare byte for byte.
    "test fixture": (
        "testdata/*",
        "*/testdata/*",
        "*.golden",
        ".concord/scenarios/*",
        "adapter/opencode/evals/*",
    ),
    # Record kinds whose grammar cites issues, pull requests, and decisions.
    "identifier record": (
        ".concord/docs/decisions/*",
        ".concord/docs/knowledge/*",
        "CHANGELOG*",
        "*/CHANGELOG*",
    ),
}

RULES: tuple[tuple[str, re.Pattern[str]], ...] = (
    ("planning-identifier", re.compile(r"\bCON-[0-9]+\b")),
    ("planning-identifier", re.compile(r"\bwork-[0-9a-f]{8,}\b")),
    ("planning-identifier", re.compile(r"(?<![\w#&/])#[1-9][0-9]*(?![\w-])")),
    ("planning-identifier", re.compile(r"github\.com/[\w.-]+/[\w.-]+/(?:issues|pull)/[0-9]+")),
    (
        "history-narration",
        re.compile(r"\b(?:used to be|formerly|renamed from|was renamed|in this change|this pull request)\b", re.IGNORECASE),
    ),
    ("history-narration", re.compile(r"\bthis PR\b")),
)

SUFFIX_KINDS = {
    ".md": "text",
    ".go": "go",
    ".ts": "script",
    ".tsx": "script",
    ".js": "script",
    ".mjs": "script",
    ".py": "python",
    ".sh": "hash",
    ".bash": "hash",
    ".yml": "hash",
    ".yaml": "hash",
    ".toml": "hash",
}

HUNK_RE = re.compile(r"^@@ -[0-9]+(?:,[0-9]+)? \+(?P<start>[0-9]+)(?:,[0-9]+)? @@")


class ContentCheckError(RuntimeError):
    """The diff or a file cannot be read; the check fails closed."""


def git(root: Path, *arguments: str) -> str:
    result = subprocess.run(
        ["git", "-c", "core.quotepath=off", *arguments], cwd=root, capture_output=True, text=True, check=False
    )
    if result.returncode:
        raise ContentCheckError(f"git {' '.join(arguments)}: {result.stderr.strip() or result.returncode}")
    return result.stdout


def exemption(path: str) -> str | None:
    for reason, patterns in EXEMPT.items():
        if any(fnmatch.fnmatchcase(path, pattern) for pattern in patterns):
            return reason
    return None


def kind_of(path: str, text: str) -> str | None:
    suffix = Path(path).suffix
    if suffix:
        return SUFFIX_KINDS.get(suffix)
    first = text.split("\n", 1)[0]
    if not first.startswith("#!"):
        return None
    return "python" if "python" in first else "hash"


def added_lines(root: Path, base: str) -> dict[str, set[int]]:
    added: dict[str, set[int]] = {}
    path: str | None = None
    number = 0
    diff = git(
        root, "diff", "--no-color", "--no-ext-diff", "--unified=0", "--find-renames", "--diff-filter=AMR", base, "--"
    )
    for line in diff.splitlines():
        if line.startswith("+++ "):
            path = line[len("+++ b/") :] if line.startswith("+++ b/") else None
        elif line.startswith("@@"):
            match = HUNK_RE.match(line)
            if match is None:
                raise ContentCheckError(f"unparseable hunk header: {line}")
            number = int(match.group("start"))
        elif line.startswith("+") and path is not None:
            added.setdefault(path, set()).add(number)
            number += 1
    for untracked in git(root, "ls-files", "--others", "--exclude-standard", "-z").split("\0"):
        if untracked:
            text = (root / untracked).read_text(encoding="utf-8", errors="replace")
            added[untracked] = set(range(1, text.count("\n") + 2))
    return added


def c_like_comments(text: str, *, templates: bool) -> dict[int, str]:
    """Comment text by line. Go backtick strings are raw; script backticks are templates."""
    comments: dict[int, list[str]] = {}
    stack: list[object] = []  # "raw" or "template" string, or brace depth of a ${} expression
    index, line, size = 0, 1, len(text)
    while index < size:
        char = text[index]
        if stack and isinstance(stack[-1], str):
            if char == "`":
                stack.pop()
            elif char == "\\" and stack[-1] == "template":
                line += text[index + 1 : index + 2] == "\n"
                index += 1
            elif stack[-1] == "template" and text.startswith("${", index):
                stack.append(0)
                index += 1
            elif char == "\n":
                line += 1
            index += 1
            continue
        if text.startswith("//", index):
            end = text.find("\n", index)
            end = size if end < 0 else end
            comments.setdefault(line, []).append(text[index:end])
            index = end
        elif text.startswith("/*", index):
            end = text.find("*/", index + 2)
            end = size if end < 0 else end + 2
            for offset, part in enumerate(text[index:end].split("\n")):
                comments.setdefault(line + offset, []).append(part)
            line += text.count("\n", index, end)
            index = end
        elif char in "\"'":
            index += 1
            while index < size and text[index] not in (char, "\n"):
                if text[index] == "\\":
                    line += text[index + 1 : index + 2] == "\n"
                    index += 1
                index += 1
            index += 1
        elif char == "`":
            stack.append("template" if templates else "raw")
            index += 1
        else:
            if stack and char == "{":
                stack[-1] += 1  # type: ignore[operator]
            elif stack and char == "}":
                if stack[-1] == 0:
                    stack.pop()
                else:
                    stack[-1] -= 1  # type: ignore[operator]
            elif char == "\n":
                line += 1
            index += 1
    return {number: " ".join(parts) for number, parts in comments.items()}


def python_comments(path: str, text: str) -> dict[int, str]:
    comments: dict[int, list[str]] = {}
    lines = text.splitlines()
    try:
        for token in tokenize.generate_tokens(io.StringIO(text).readline):
            if token.type == tokenize.COMMENT:
                comments.setdefault(token.start[0], []).append(token.string)
        tree = ast.parse(text)
    except (SyntaxError, tokenize.TokenError) as error:
        raise ContentCheckError(f"{path}: cannot parse Python source: {error}") from error
    for node in ast.walk(tree):
        if not isinstance(node, (ast.Module, ast.ClassDef, ast.FunctionDef, ast.AsyncFunctionDef)) or not node.body:
            continue
        first = node.body[0]
        if isinstance(first, ast.Expr) and isinstance(first.value, ast.Constant) and isinstance(first.value.value, str):
            for number in range(first.lineno, (first.end_lineno or first.lineno) + 1):
                comments.setdefault(number, []).append(lines[number - 1])
    return {number: " ".join(parts) for number, parts in comments.items()}


def inspected_text(path: str, kind: str, text: str) -> dict[int, str]:
    if kind == "text":
        return dict(enumerate(text.splitlines(), start=1))
    if kind == "go":
        return c_like_comments(text, templates=False)
    if kind == "script":
        return c_like_comments(text, templates=True)
    if kind == "python":
        return python_comments(path, text)
    return {
        number: line
        for number, line in enumerate(text.splitlines(), start=1)
        if line.lstrip().startswith("#") and not line.startswith("#!")
    }


def check(root: Path, base_ref: str) -> list[str]:
    base = git(root, "merge-base", base_ref, "HEAD").strip()
    findings: list[str] = []
    for path, numbers in sorted(added_lines(root, base).items()):
        if exemption(path) is not None:
            continue
        text = (root / path).read_text(encoding="utf-8", errors="replace")
        kind = kind_of(path, text)
        if kind is None:
            continue
        inspected = inspected_text(path, kind, text)
        for number in sorted(numbers & inspected.keys()):
            for rule, pattern in RULES:
                for match in pattern.finditer(inspected[number]):
                    findings.append(f"{path}:{number}: {rule}: `{match.group(0)}` in {inspected[number].strip()[:160]}")
    return findings


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--root", type=Path, default=ROOT, help="repository root")
    parser.add_argument("--base-ref", default="HEAD", help="change base; added lines are measured from its merge base with HEAD")
    args = parser.parse_args(argv)
    try:
        findings = check(args.root.resolve(), args.base_ref)
    except ContentCheckError as error:
        print(f"committed content check could not run: {error}", file=sys.stderr)
        return 2
    for finding in findings:
        print(finding)
    if findings:
        print(
            f"committed content check failed: {len(findings)} finding(s); move provenance and history to the "
            "commit, pull request, or work record (.concord/instructions/change.md)",
            file=sys.stderr,
        )
        return 1
    print("committed content check passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

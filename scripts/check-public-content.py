#!/usr/bin/env python3
"""Reject common private content and credential shapes before publication."""

from __future__ import annotations

import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
MAX_FINDINGS = 200
SKIP = {"scripts/check-public-content.py", "scripts/check-doc-links.py", "scripts/check-json.py"}
TEXT_NAMES = {".editorconfig", ".gitattributes", ".gitignore", "LICENSE"}
TEXT_SUFFIXES = {".go", ".json", ".md", ".py", ".toml", ".txt", ".yml", ".yaml"}

PATTERNS = [
    ("private filesystem path", re.compile(r"(?:/home/|/Users/|[A-Za-z]:[\\/]Users[\\/]|/tmp/opencode/|/private/|/var/run/)")),
    ("private host", re.compile(r"(?:\blocalhost\b|\b127\.0\.0\.1\b|\b0\.0\.0\.0\b|\b10\.(?:\d{1,3}\.){2}\d{1,3}\b|\b192\.168\.(?:\d{1,3}\.)\d{1,3}\b|\b172\.(?:1[6-9]|2\d|3[01])(?:\.\d{1,3}){2}\b|\b[a-z0-9-]+\.(?:local|internal|lan)\b)")),
    ("private port", re.compile(r"(?:\bport\s*[:=]?\s*\d{2,5}\b|\b(?:localhost|127\.0\.0\.1|0\.0\.0\.0):\d{2,5}\b)")),
    ("credential shape", re.compile(r"\b(?:gh[pousr]_|github_pat_|sk-|xox[baprs]-|AKIA[0-9A-Z]{12}|eyJ[A-Za-z0-9_-]{20,}\.)[A-Za-z0-9_./+=-]{12,}")),
    ("credential assignment", re.compile(r"\b(?:api[_ -]?key|access[_ -]?token|secret|password|private[_ -]?key)\b\s*[:=]\s*[\"']?(?!<|\$\{|REDACTED\b|example\b|your[-_]|changeme\b)[A-Za-z0-9+/=_-]{16,}", re.IGNORECASE)),
]


def repository_files() -> list[Path]:
    result = subprocess.run(
        ["git", "ls-files", "-co", "--exclude-standard"],
        cwd=ROOT,
        check=True,
        capture_output=True,
        text=True,
    )
    return sorted({ROOT / line for line in result.stdout.splitlines() if line})


def is_candidate(path: Path) -> bool:
    relative = path.relative_to(ROOT).as_posix()
    return path.name in TEXT_NAMES or path.suffix.lower() in TEXT_SUFFIXES or relative.startswith(".github/")


def main() -> int:
    findings: list[str] = []
    for path in repository_files():
        relative = path.relative_to(ROOT).as_posix()
        if relative in SKIP or not is_candidate(path) or not path.is_file():
            continue
        try:
            lines = path.read_text(encoding="utf-8").splitlines()
        except UnicodeDecodeError:
            continue
        for line_number, line in enumerate(lines, 1):
            for label, pattern in PATTERNS:
                if pattern.search(line):
                    findings.append(f"{relative}:{line_number}: {label}")
            if path.suffix.lower() == ".json" and '"$id"' in line:
                if "https://raw.githubusercontent.com/Sharper-Flow/concord/main/" not in line:
                    findings.append(f"{relative}:{line_number}: non-public schema ID")
            if "JRedeker" in line and relative != ".github/CODEOWNERS":
                findings.append(f"{relative}:{line_number}: maintainer identity is only permitted in .github/CODEOWNERS")

    for finding in findings[:MAX_FINDINGS]:
        print(finding)
    if len(findings) > MAX_FINDINGS:
        print(f"... {len(findings) - MAX_FINDINGS} additional finding(s) omitted")
    if findings:
        print(f"Public-content audit failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    print("Public-content audit passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

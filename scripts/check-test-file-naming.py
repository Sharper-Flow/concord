#!/usr/bin/env python3
"""Reject ticket identifiers in Go test file names."""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
TICKET_IN_NAME = re.compile(r"(?:^|[-_])(cd|issue)[-_]?\d+", re.IGNORECASE)


def test_files(root: Path) -> list[Path]:
    return sorted(
        path
        for path in root.rglob("*_test.go")
        if ".git" not in path.parts and "vendor" not in path.parts
    )


def check(*, root: Path = ROOT) -> list[str]:
    findings: list[str] = []
    for path in test_files(root):
        match = TICKET_IN_NAME.search(path.stem)
        if match:
            relative = path.relative_to(root).as_posix()
            kind = "decision record" if match.group(1).lower() == "cd" else "issue"
            findings.append(f"test-file-name: {relative} encodes a {kind} number")
    return findings


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=ROOT, help="repository root")
    args = parser.parse_args()
    findings = check(root=args.root.resolve())
    for finding in findings:
        print(finding)
    if findings:
        print(f"test file naming check failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    print(f"test file naming check passed: {len(test_files(args.root.resolve()))} file(s)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

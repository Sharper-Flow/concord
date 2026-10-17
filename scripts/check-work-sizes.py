#!/usr/bin/env python3
"""Check the work-size vocabulary closure over its generated Go and the stored CHECK."""
from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

from migration_source import migrations
from vocabulary_utils import generator_freshness, load_json_finding


ROOT = Path(__file__).resolve().parents[1]
VOCABULARY = Path("contracts/work-sizes.v1.json")
SCHEMA_SOURCE = Path("internal/store/schema.go")
STORE_SOURCE = Path("internal/store")
GENERATED = "generated_work_sizes.go"
SIZE_CHECK = re.compile(
    r"\bsize_class\s+TEXT\s+NOT\s+NULL\s+CHECK\s*\(\s*size_class\s+IN\s*\((?P<values>[^)]*)\)\s*\)",
    re.I,
)
TABLE = re.compile(r"\bCREATE\s+TABLE\s+work_size_assessments\b", re.I)
GO_SIZE_CONSTANT = re.compile(r"\bWorkSize\s*=\s*\"")


def stored_sizes(root: Path, findings: list[str]) -> list[str] | None:
    """Read the size CHECK from the last migration that creates the table,
    so a later table rebuild, not the first definition, is what is compared."""
    try:
        source = (root / SCHEMA_SOURCE).read_text(encoding="utf-8")
        entries = migrations(source)
    except (OSError, ValueError) as exc:
        findings.append(f"migration-source: unable to read {SCHEMA_SOURCE}: {exc}")
        return None
    creating = [sql for _, _, sql in entries if TABLE.search(sql)]
    if not creating:
        findings.append("migration-coverage: no migration creates work_size_assessments")
        return None
    match = SIZE_CHECK.search(creating[-1])
    if match is None:
        findings.append("migration-check-coverage: work_size_assessments.size_class has no literal IN CHECK")
        return None
    return [value.strip().strip("'") for value in match.group("values").split(",")]


def check_hand_written_constants(root: Path, findings: list[str]) -> None:
    for path in sorted((root / STORE_SOURCE).glob("*.go")):
        if path.name == GENERATED or path.name.endswith("_test.go"):
            continue
        if GO_SIZE_CONSTANT.search(path.read_text(encoding="utf-8")):
            findings.append(f"go-closure: {path.relative_to(root)} declares a WorkSize constant outside {GENERATED}")


def check(root: Path) -> list[str]:
    findings: list[str] = []
    generator_freshness(root, "generate-work-sizes.py", findings)
    vocabulary = load_json_finding(root, VOCABULARY, findings)
    if not isinstance(vocabulary, dict) or not isinstance(vocabulary.get("sizes"), list):
        findings.append("vocabulary-shape: sizes must be an array")
        return findings
    declared = [item.get("size") for item in vocabulary["sizes"] if isinstance(item, dict)]
    stored = stored_sizes(root, findings)
    if stored is not None and sorted(stored) != sorted(declared):
        findings.append(f"migration-check-closure: expected {declared}, got {stored}")
    check_hand_written_constants(root, findings)
    return findings


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--root", type=Path, default=ROOT)
    args = parser.parse_args(argv)
    findings = check(args.root.resolve())
    for finding in findings:
        print(finding)
    if findings:
        print(f"work-size vocabulary check failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    print("work-size vocabulary check passed", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))

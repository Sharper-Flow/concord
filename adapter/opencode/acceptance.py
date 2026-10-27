#!/usr/bin/env python3
"""Grade native Bun JUnit for literal files and optional --expect [file#]name.

Without --expect, every requested file must report a nonempty executed pass.
Skipped optional tests remain compatible with the dispatch checks. An expected
name must pass exactly once in its requested file, never merely be registered.
Native parameterized tests may repeat a display name: file-only checks retain
every occurrence and refuse conflicting outcomes; named checks refuse repeats.
"""
import argparse
import json
import re
import subprocess
import sys
import tempfile
import xml.etree.ElementTree as ET
from pathlib import Path


def validate_suite_properties(properties: ET.Element) -> None:
    if properties.attrib or (properties.text or "").strip():
        raise ValueError("malformed native Bun suite properties")
    for item in properties:
        if (item.tag != "property" or set(item.attrib) != {"name", "value"}
                or not item.get("name", "").strip() or len(item)
                or (item.text or "").strip() or (item.tail or "").strip()):
            raise ValueError("malformed native Bun suite property")


def grade_report(report: Path, files: list[str], expected: list[str]) -> list[dict[str, str]]:
    if not report.is_file() or report.stat().st_size > 2 * 1024 * 1024:
        raise ValueError("missing or oversized native Bun JUnit report")
    raw = report.read_bytes()
    if b"<!" in raw:
        raise ValueError("native Bun JUnit must not contain declarations")
    try:
        root = ET.fromstring(raw)
    except ET.ParseError as error:
        raise ValueError(f"malformed native Bun JUnit: {error}") from error
    if root.tag != "testsuites" or any(suite.tag != "testsuite" for suite in root):
        raise ValueError("native Bun JUnit requires testsuites/testsuite structure")
    observed = []
    seen = {}
    pending = list(root)
    while pending:
        suite = pending.pop()
        for case in suite:
            if case.tag == "properties":
                validate_suite_properties(case)
                continue
            if case.tag == "testsuite":
                pending.append(case)
                continue
            if case.tag != "testcase":
                raise ValueError(f"unexpected native Bun JUnit suite entry: {case.tag}")
            file, name = case.get("file", "").removeprefix("./"), case.get("name", "")
            if file not in files or not name.strip() or len(name.encode()) > 1024:
                raise ValueError(f"unbound native Bun test file/name: {file!r}#{name!r}")
            if len(observed) >= 4096:
                raise ValueError("oversized native Bun test inventory")
            outcomes = [child.tag for child in case]
            if case.get("status") is not None or outcomes not in ([], ["skipped"]):
                raise ValueError(f"nonpassing or unknown native Bun outcome: {file}#{name}: {outcomes}")
            outcome = "skipped" if outcomes else "pass"
            if (file, name) in seen and seen[file, name] != outcome:
                raise ValueError(f"conflicting native Bun outcomes: {file}#{name}")
            seen[file, name] = outcome
            observed.append({"file": file, "name": name, "outcome": outcome})
    for file in files:
        if not any(case["file"] == file and case["outcome"] == "pass" for case in observed):
            raise ValueError(f"no actually passed native Bun test in {file}")
    for selected in expected:
        matches = [case for case in observed if selected == f'{case["file"]}#{case["name"]}'
                   or ("#" not in selected and selected == case["name"])]
        if len(matches) != 1 or matches[0]["outcome"] != "pass":
            raise ValueError(f"expected native Bun test did not pass unambiguously: {selected}")
    return sorted(observed, key=lambda case: (case["file"], case["name"]))


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("files", nargs="+")
    parser.add_argument("--expect", action="append", default=[])
    args = parser.parse_args()
    files = args.files
    if len(files) > 64 or len(set(files)) != len(files) or any(
        not re.fullmatch(r"[A-Za-z0-9_][A-Za-z0-9._-]*\.test\.ts", name) for name in files
    ):
        raise SystemExit("acceptance requires literal adapter .test.ts file names")
    if len(args.expect) > 128 or len(set(args.expect)) != len(args.expect) or any(
        not name.strip() or name != name.strip() or len(name.encode()) > 1024 for name in args.expect
    ):
        raise SystemExit("acceptance requires bounded unique exact expected names")
    directory = Path(__file__).resolve().parent
    try:
        with tempfile.TemporaryDirectory(prefix=".acceptance-", dir=directory) as temporary:
            report = Path(temporary) / "native.xml"
            result = subprocess.run(
                ["bun", "test", "--preload", "./acceptance-preload.ts", "--reporter=junit",
                 f"--reporter-outfile={report}", *[f"./{name}" for name in files]],
                cwd=directory, check=False, timeout=120,
            )
            if result.returncode:
                return result.returncode
            observed = grade_report(report, files, args.expect)
            print(json.dumps({"observed": observed}, sort_keys=True), flush=True)
    except (OSError, ValueError, subprocess.TimeoutExpired) as error:
        print(f"acceptance refused: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())

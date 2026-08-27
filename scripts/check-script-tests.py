#!/usr/bin/env python3
"""Check that every script test suite runs in CI.

A `scripts/test-*.py` suite that no workflow invokes passes forever, because it
never executes. #416 found three such suites and added them by hand; this check
makes the next one fail instead of hiding.

The same read also guards workflow structure: a workflow file GitHub cannot
parse silently skips every check it names. Two merges in one sequence shipped a
9-space `run:` key this way, so step keys carry exact indent expectations and
each workflow must parse as YAML when PyYAML is available.
"""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
WORKFLOWS = Path(".github/workflows")
SCRIPTS = Path("scripts")
REFERENCE_RE = re.compile(r"scripts/(test-[a-z0-9-]+\.py)")
RUN_KEY_RE = re.compile(r"^(?P<indent> +)run:")
STEP_NAME_RE = re.compile(r"^(?P<indent> +)- name:")
RUN_KEY_INDENT = 8
STEP_NAME_INDENT = 6

try:
    import yaml
except ImportError:  # pragma: no cover - runner images carry PyYAML
    yaml = None


def referenced(root: Path) -> set[str]:
    names: set[str] = set()
    for workflow in sorted((root / WORKFLOWS).glob("*.yml")):
        try:
            text = workflow.read_text(encoding="utf-8")
        except (OSError, UnicodeDecodeError):
            continue
        names.update(REFERENCE_RE.findall(text))
    return names


def structure_findings(workflow: Path, text: str) -> list[str]:
    findings: list[str] = []
    for number, line in enumerate(text.splitlines(), start=1):
        run_match = RUN_KEY_RE.match(line)
        if run_match and len(run_match.group("indent")) != RUN_KEY_INDENT:
            findings.append(
                f"workflow-structure: {WORKFLOWS / workflow.name} line {number}: "
                f"step `run:` key indented {len(run_match.group('indent'))} spaces, "
                f"expected {RUN_KEY_INDENT}; GitHub will not parse this workflow"
            )
        step_match = STEP_NAME_RE.match(line)
        if step_match and len(step_match.group("indent")) != STEP_NAME_INDENT:
            findings.append(
                f"workflow-structure: {WORKFLOWS / workflow.name} line {number}: "
                f"step `- name:` item indented {len(step_match.group('indent'))} spaces, "
                f"expected {STEP_NAME_INDENT}; GitHub will not parse this workflow"
            )
    if yaml is not None:
        try:
            yaml.safe_load(text)
        except yaml.YAMLError as exc:
            findings.append(
                f"workflow-parse: {WORKFLOWS / workflow.name} is not valid YAML: {exc}"
            )
    return findings


def present(root: Path) -> set[str]:
    return {path.name for path in (root / SCRIPTS).glob("test-*.py")}


def check(*, root: Path = ROOT) -> list[str]:
    findings: list[str] = []
    on_disk = present(root)
    in_workflows = referenced(root)

    for workflow in sorted((root / WORKFLOWS).glob("*.yml")):
        try:
            text = workflow.read_text(encoding="utf-8")
        except (OSError, UnicodeDecodeError) as exc:
            findings.append(f"workflow-read: {WORKFLOWS / workflow.name}: {exc}")
            continue
        findings.extend(structure_findings(workflow, text))

    for name in sorted(on_disk - in_workflows):
        findings.append(
            f"unrun-suite: {SCRIPTS / name} is never invoked by {WORKFLOWS}; "
            "a suite no workflow runs cannot fail"
        )
    for name in sorted(in_workflows - on_disk):
        findings.append(
            f"stale-reference: {WORKFLOWS} invokes {SCRIPTS / name}, which does not exist"
        )
    return findings


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--root", type=Path, default=ROOT, help="repository root")
    args = parser.parse_args()

    findings = check(root=args.root.resolve())
    for finding in findings:
        print(finding)
    if findings:
        print(f"script test check failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    print(f"script test check passed: {len(present(args.root.resolve()))} suite(s) run in CI")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

#!/usr/bin/env python3
"""Run the script test suites that a push file set selects.

This is the hook-side selector that `lefthook.yml` invokes as
`python3 scripts/run-script-tests.py -- {push_files}`. Preflight is not
full-suite proof: it stops an obviously broken push early, while CI stays the
verification owner for every suite.

The battery and suite universe are derived from `.github/workflows/ci.yml`
with the real YAML parser (PyYAML), never from a curated copy:

- The complete tooling battery is the `&&`-chained command list of the
  verify-tooling step named "Test release and installer tooling". Each of its
  segments must be exactly `python3 scripts/test-<name>.py`, or the battery
  is undecodable and this selector refuses to run (exit 2) rather than guess.
- A changed `scripts/test-*.py` file runs itself.
- Any other changed file runs the suites that bind it: a suite whose AST
  imports the changed module, or whose AST carries a string constant naming
  the changed file's basename or repository-relative path. Binding is an
  advisory candidate choice for partial preflight only; it is not an
  exhaustive transitive test mapping.
- A changed file with no bound suite falls back to the complete tooling
  battery. A changed battery-defining file (the CI workflow or the hook
  configuration) also runs the battery, because the selection mapping itself
  may have drifted. Nothing is ever silently skipped.

Push files arrive as whole `argv` elements: Lefthook single-quotes expanded
placeholders, and the forwarded `--` keeps a dash-prefixed path positional.

Suites are spawned with the hook-inherited Git namespace cleared
(scripts/git_environment.py): a suite building its own scratch
repository would otherwise have its Git operations redirected into the outer
repository the hook serves. If that namespace cannot be discovered, no suite
is spawned at all.
"""

from __future__ import annotations

import argparse
import ast
import re
import shlex
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path

import git_environment

try:
    import yaml
except ImportError:  # pragma: no cover - CI runner images carry PyYAML
    yaml = None

ROOT = Path(__file__).resolve().parents[1]
CI_WORKFLOW = ".github/workflows/ci.yml"
TOOLING_JOB = "verify-tooling"
TOOLING_STEP_NAME = "Test release and installer tooling"

# Changing either of these files can change how suites are selected, so both
# always route to the complete tooling battery.
MAPPING_FILES = frozenset({CI_WORKFLOW, "lefthook.yml"})

TEST_SUITE_RE = re.compile(r"^scripts/test-[a-z0-9-]+\.py$")
SUITE_INVOCATION_RE = re.compile(r"(?<![\w-])python3 (scripts/test-[a-z0-9-]+\.py)(?![\w-])")


class BatteryError(Exception):
    """The tooling battery cannot be derived from the CI workflow."""


def _ci_document(root: Path) -> dict:
    """The parsed CI workflow. Missing or malformed YAML is a BatteryError."""
    if yaml is None:
        raise BatteryError("PyYAML is required to derive suites from the CI workflow")
    try:
        document = yaml.safe_load((root / CI_WORKFLOW).read_text(encoding="utf-8"))
    except OSError as error:
        raise BatteryError(f"cannot read {CI_WORKFLOW}: {error}") from error
    except yaml.YAMLError as error:
        raise BatteryError(f"{CI_WORKFLOW} is not valid YAML: {error}") from error
    if not isinstance(document, dict):
        raise BatteryError(f"{CI_WORKFLOW} must parse to a YAML mapping")
    return document


def _ci_steps(root: Path) -> list[tuple[str | None, str]]:
    """(step name, run command) pairs for every step that runs a command."""
    jobs = _ci_document(root).get("jobs")
    if not isinstance(jobs, dict):
        raise BatteryError(f"{CI_WORKFLOW} declares no jobs")
    steps: list[tuple[str | None, str]] = []
    for job in jobs.values():
        if not isinstance(job, dict) or not isinstance(job.get("steps"), list):
            continue
        for step in job["steps"]:
            if isinstance(step, dict) and isinstance(step.get("run"), str) and step["run"].strip():
                steps.append((step.get("name"), step["run"]))
    return steps


def _battery_from_command(command: str) -> list[str]:
    battery: list[str] = []
    for segment in command.split("&&"):
        tokens = shlex.split(segment)
        if len(tokens) != 2 or tokens[0] != "python3" or not TEST_SUITE_RE.fullmatch(tokens[1]):
            raise BatteryError(
                f"the tooling battery step {TOOLING_STEP_NAME!r} must chain only "
                f"`python3 scripts/test-<name>.py` commands; got: {segment.strip()!r}"
            )
        battery.append(tokens[1])
    if not battery:
        raise BatteryError(f"the {TOOLING_STEP_NAME!r} step runs no script suites")
    return battery


def tooling_battery(root: Path = ROOT) -> list[str]:
    """The complete tooling battery, derived from the CI workflow."""
    jobs = _ci_document(root).get("jobs")
    job = jobs.get(TOOLING_JOB) if isinstance(jobs, dict) else None
    steps = job.get("steps") if isinstance(job, dict) else None
    if isinstance(steps, list):
        for step in steps:
            if isinstance(step, dict) and step.get("name") == TOOLING_STEP_NAME and isinstance(step.get("run"), str):
                return _battery_from_command(step["run"])
    raise BatteryError(f"{CI_WORKFLOW} has no step named {TOOLING_STEP_NAME!r}")


def ci_suites(root: Path = ROOT) -> list[str]:
    """Every script suite the CI workflow invokes, in first-reference order."""
    suites: list[str] = []
    for _, command in _ci_steps(root):
        for suite in SUITE_INVOCATION_RE.findall(command):
            if suite not in suites:
                suites.append(suite)
    return suites


def on_disk_suites(root: Path = ROOT) -> list[str]:
    """Every suite file present under scripts/, CI-referenced or not."""
    scripts = root / "scripts"
    if not scripts.is_dir():
        return []
    return sorted(
        f"scripts/{path.name}" for path in scripts.glob("test-*.py") if TEST_SUITE_RE.fullmatch(f"scripts/{path.name}")
    )


def _imported_modules(tree: ast.AST) -> set[str]:
    modules: set[str] = set()
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            modules.update(alias.name for alias in node.names)
        elif isinstance(node, ast.ImportFrom) and node.module:
            modules.add(node.module)
    return modules


def _string_constants(tree: ast.AST) -> set[str]:
    return {
        node.value
        for node in ast.walk(tree)
        if isinstance(node, ast.Constant) and isinstance(node.value, str)
    }


def suite_binds(suite: Path, changed: str) -> bool:
    """A suite binds a changed file when it imports it or names it by path."""
    try:
        tree = ast.parse(suite.read_text(encoding="utf-8"))
    except (OSError, SyntaxError, ValueError):
        return False
    if Path(changed).stem in _imported_modules(tree):
        return True
    references = _string_constants(tree)
    return Path(changed).name in references or changed in references


def _normalized(files: list[str]) -> list[str]:
    seen: set[str] = set()
    ordered: list[str] = []
    for candidate in files:
        path = candidate.strip()
        while path.startswith("./"):
            path = path[2:]
        if not path or path in seen:
            continue
        seen.add(path)
        ordered.append(path)
    return ordered


@dataclass(frozen=True)
class Selection:
    commands: list[list[str]]
    used_battery_fallback: bool


def select(files: list[str], root: Path = ROOT) -> Selection:
    """Choose the suite commands for a push file set. Never empty."""
    battery = tooling_battery(root)
    battery_commands = [["python3", suite] for suite in battery]
    changed_files = _normalized(files)

    if not changed_files or any(path in MAPPING_FILES for path in changed_files):
        return Selection(battery_commands, True)

    # CI-referenced suites first (in workflow order), then suites that exist
    # on disk but are not wired into CI yet.
    referenced = ci_suites(root)
    universe = referenced + [suite for suite in on_disk_suites(root) if suite not in referenced]
    order = {suite: index for index, suite in enumerate(universe)}
    selected: set[str] = set()
    for changed in changed_files:
        if TEST_SUITE_RE.fullmatch(changed):
            selected.add(changed)
            continue
        bound = {suite for suite in universe if suite_binds(root / suite, changed)}
        if not bound:
            return Selection(battery_commands, True)
        selected.update(bound)

    commands = [["python3", suite] for suite in sorted(selected, key=lambda s: (order.get(s, len(order)), s))]
    return Selection(commands, False)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--root", type=Path, default=ROOT, help="repository root")
    parser.add_argument("--dry-run", action="store_true", help="print the selected commands without running them")
    parser.add_argument("files", nargs="*", help="push files the selection is derived from")
    args = parser.parse_args(argv)
    root = args.root.resolve()

    try:
        selection = select(args.files, root=root)
    except BatteryError as error:
        print(f"run-script-tests: {error}", file=sys.stderr)
        print("run-script-tests: refusing to select anything from an unreadable battery", file=sys.stderr)
        return 2

    for command in selection.commands:
        print(f"script-suite {' '.join(command)}")
    if args.dry_run:
        return 0

    try:
        child_environment = git_environment.sanitized_environment()
    except git_environment.GitEnvironmentError as error:
        print(f"run-script-tests: {error}", file=sys.stderr)
        print("run-script-tests: refusing to spawn suites with an unknown Git environment", file=sys.stderr)
        return 2

    for command in selection.commands:
        completed = subprocess.run(command, cwd=root, check=False, env=child_environment)
        if completed.returncode:
            print(
                f"run-script-tests: suite failed (exit {completed.returncode}): {' '.join(command)}",
                file=sys.stderr,
            )
            return completed.returncode
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

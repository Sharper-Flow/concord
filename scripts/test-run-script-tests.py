#!/usr/bin/env python3
"""Tests for the changed-file script suite selector.

The selector spawns suites with the hook-inherited Git environment cleared;
scripts/git_environment.py owns that namespace and is tested
here alongside the selector boundary that uses it.
"""

from __future__ import annotations

import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

import git_environment

# This suite builds Git repositories itself, so a hook that
# launched it must not keep a redirecting Git namespace in place.
git_environment.scrub_inherited()


REPO = Path(__file__).resolve().parents[1]
SCRIPT = REPO / "scripts/run-script-tests.py"
HELPER = REPO / "scripts/git_environment.py"

# A minimal CI workflow that carries the same shape as the real one: a
# verify-tooling job with the chained tooling battery step, and another job
# whose steps reference suites line by line. The selector must derive every
# suite and command from this text, never from a curated list.
CI_FIXTURE = """name: CI
on:
  pull_request:
jobs:
  verify-tooling:
    name: verify-tooling
    runs-on: ubuntu-latest
    steps:
      - name: Test release and installer tooling
        run: python3 scripts/test-alpha.py && python3 scripts/test-beta.py && python3 scripts/test-gamma.py
  verify-contracts:
    name: verify-contracts
    runs-on: ubuntu-latest
    steps:
      - name: Check one
        run: |
          python3 scripts/test-alpha.py
          python3 scripts/test-delta.py
"""

FAKE_PYTHON3 = f"""#!{sys.executable}
import json
import os
from pathlib import Path
import sys

with open(os.environ["SUITE_RECORD"], "a") as record:
    record.write(json.dumps({{"argv": sys.argv, "cwd": os.getcwd()}}) + "\\n")
suite = sys.argv[1] if len(sys.argv) > 1 else ""
if os.environ.get("FAIL_SUITE") and os.environ["FAIL_SUITE"] in suite:
    sys.exit(7)
"""


def load_selector(root: Path):
    spec = importlib.util.spec_from_file_location("run_script_tests_under_test", root / "scripts/run-script-tests.py")
    assert spec and spec.loader
    module = importlib.util.module_from_spec(spec)
    sys.modules["run_script_tests_under_test"] = module
    try:
        spec.loader.exec_module(module)
    finally:
        sys.modules.pop("run_script_tests_under_test", None)
    return module


class SelectorRoot:
    """A synthetic repository carrying the fixture CI and chosen scripts."""

    def __init__(self, battery_text: str | None = None) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.root = Path(self.tempdir.name)
        (self.root / "scripts").mkdir()
        (self.root / ".github/workflows").mkdir(parents=True)
        ci = battery_text if battery_text is not None else CI_FIXTURE
        (self.root / ".github/workflows/ci.yml").write_text(ci, encoding="utf-8")
        shutil.copyfile(SCRIPT, self.root / "scripts/run-script-tests.py")
        # The copied runner imports its sibling helper; a fixture root that
        # carries one must carry both, or the copy cannot run standalone.
        shutil.copyfile(HELPER, self.root / "scripts/git_environment.py")

    def script(self, name: str, text: str = "#!/usr/bin/env python3\n") -> Path:
        path = self.root / "scripts" / name
        path.write_text(text, encoding="utf-8")
        return path

    def __enter__(self) -> "SelectorRoot":
        return self

    def __exit__(self, *unused) -> None:
        self.tempdir.cleanup()


class BatteryDerivationTest(unittest.TestCase):
    def test_battery_is_derived_from_the_ci_tooling_step(self) -> None:
        with SelectorRoot() as fixture:
            selector = load_selector(fixture.root)
            self.assertEqual(
                selector.tooling_battery(fixture.root),
                ["scripts/test-alpha.py", "scripts/test-beta.py", "scripts/test-gamma.py"],
            )

    def test_battery_derivation_uses_the_real_yaml_parser(self) -> None:
        # Four-space step indentation is valid YAML but not a fixed indent
        # grammar; a real parser must still read it.
        indented = (
            "jobs:\n"
            "    verify-tooling:\n"
            "        steps:\n"
            "            - name: Test release and installer tooling\n"
            "              run: python3 scripts/test-alpha.py && python3 scripts/test-beta.py\n"
        )
        with SelectorRoot(battery_text=indented) as fixture:
            selector = load_selector(fixture.root)
            self.assertEqual(
                selector.tooling_battery(fixture.root),
                ["scripts/test-alpha.py", "scripts/test-beta.py"],
            )

    def test_missing_tooling_step_fails_visibly(self) -> None:
        with SelectorRoot(battery_text="name: CI\non:\n  pull_request:\njobs: {}\n") as fixture:
            selector = load_selector(fixture.root)
            with self.assertRaises(selector.BatteryError):
                selector.tooling_battery(fixture.root)

    def test_malformed_ci_yaml_refuses_to_derive_anything(self) -> None:
        with SelectorRoot(battery_text="jobs: [unclosed\n") as fixture:
            selector = load_selector(fixture.root)
            with self.assertRaises(selector.BatteryError):
                selector.tooling_battery(fixture.root)
            with self.assertRaises(selector.BatteryError):
                selector.ci_suites(fixture.root)

    def test_missing_pyyaml_refuses_to_derive_anything(self) -> None:
        with SelectorRoot() as fixture:
            selector = load_selector(fixture.root)
            original = selector.yaml
            try:
                selector.yaml = None
                with self.assertRaises(selector.BatteryError):
                    selector.tooling_battery(fixture.root)
            finally:
                selector.yaml = original

    def test_ci_suites_cover_every_workflow_reference(self) -> None:
        with SelectorRoot() as fixture:
            selector = load_selector(fixture.root)
            self.assertEqual(
                selector.ci_suites(fixture.root),
                ["scripts/test-alpha.py", "scripts/test-beta.py", "scripts/test-gamma.py", "scripts/test-delta.py"],
            )


class SelectionTest(unittest.TestCase):
    def select(self, fixture: SelectorRoot, files: list[str]):
        return load_selector(fixture.root).select(files, root=fixture.root)

    def test_changed_test_file_runs_itself(self) -> None:
        with SelectorRoot() as fixture:
            selection = self.select(fixture, ["scripts/test-delta.py"])
            self.assertEqual(selection.commands, [["python3", "scripts/test-delta.py"]])
            self.assertFalse(selection.used_battery_fallback)

    def test_changed_script_binds_suites_that_reference_it(self) -> None:
        with SelectorRoot() as fixture:
            fixture.script("check-thing.py")
            fixture.script("test-thing.py", '#!/usr/bin/env python3\nPATH_REF = "scripts/check-thing.py"\n')
            selection = self.select(fixture, ["scripts/check-thing.py"])
            self.assertEqual(selection.commands, [["python3", "scripts/test-thing.py"]])
            self.assertFalse(selection.used_battery_fallback)

    def test_changed_script_binds_suites_that_import_it(self) -> None:
        with SelectorRoot() as fixture:
            fixture.script("widget.py")
            fixture.script("test-widget.py", "#!/usr/bin/env python3\nimport widget\n")
            selection = self.select(fixture, ["scripts/widget.py"])
            self.assertEqual(selection.commands, [["python3", "scripts/test-widget.py"]])
            self.assertFalse(selection.used_battery_fallback)

    def test_changed_script_binds_suites_by_basename_reference(self) -> None:
        with SelectorRoot() as fixture:
            fixture.script("check-named.py")
            fixture.script("test-named.py", '#!/usr/bin/env python3\nNAME = "check-named.py"\n')
            selection = self.select(fixture, ["scripts/check-named.py"])
            self.assertEqual(selection.commands, [["python3", "scripts/test-named.py"]])

    def test_unbound_changed_script_falls_back_to_the_battery(self) -> None:
        with SelectorRoot() as fixture:
            fixture.script("check-mystery.py")
            selection = self.select(fixture, ["scripts/check-mystery.py"])
            self.assertEqual(
                selection.commands,
                [["python3", suite] for suite in ("scripts/test-alpha.py", "scripts/test-beta.py", "scripts/test-gamma.py")],
            )
            self.assertTrue(selection.used_battery_fallback)

    def test_mapping_file_change_runs_the_battery(self) -> None:
        for mapping in ("lefthook.yml", ".github/workflows/ci.yml"):
            with self.subTest(mapping=mapping):
                with SelectorRoot() as fixture:
                    selection = self.select(fixture, [mapping])
                    self.assertTrue(selection.used_battery_fallback)
                    self.assertEqual(len(selection.commands), 3)

    def test_no_files_runs_the_battery(self) -> None:
        with SelectorRoot() as fixture:
            selection = self.select(fixture, [])
            self.assertTrue(selection.used_battery_fallback)
            self.assertEqual(len(selection.commands), 3)

    def test_paths_with_spaces_stay_whole_arguments(self) -> None:
        with SelectorRoot() as fixture:
            selection = self.select(fixture, ["scripts/weird name; probe.py"])
            # No suite binds the odd path, and odd paths never match the test
            # suite shape, so the complete battery runs rather than a guess.
            self.assertTrue(selection.used_battery_fallback)
            self.assertEqual(len(selection.commands), 3)

    def test_files_are_normalized_before_selection(self) -> None:
        with SelectorRoot() as fixture:
            selection = self.select(fixture, ["./scripts/test-delta.py", "scripts/test-delta.py", ""])
            self.assertEqual(selection.commands, [["python3", "scripts/test-delta.py"]])

    def test_selection_stays_ordered_and_deduplicated(self) -> None:
        with SelectorRoot() as fixture:
            fixture.script("check-multi.py")
            fixture.script(
                "test-alpha.py",
                '#!/usr/bin/env python3\nREF = "scripts/check-multi.py"\n',
            )
            selection = self.select(fixture, ["scripts/check-multi.py", "scripts/test-delta.py"])
            self.assertEqual(
                selection.commands,
                [["python3", "scripts/test-alpha.py"], ["python3", "scripts/test-delta.py"]],
            )

    def test_every_selected_invocation_is_a_ci_suite_command(self) -> None:
        ci_text = CI_FIXTURE
        ci_commands = {
            "python3 scripts/test-alpha.py",
            "python3 scripts/test-beta.py",
            "python3 scripts/test-gamma.py",
            "python3 scripts/test-delta.py",
        }
        with SelectorRoot() as fixture:
            fixture.script("check-a.py")
            fixture.script("check-b.py")
            selector = load_selector(fixture.root)
            for files in (
                [],
                ["lefthook.yml"],
                ["scripts/check-a.py"],
                ["scripts/test-delta.py"],
                ["scripts/check-a.py", "scripts/test-b.py"],
            ):
                with self.subTest(files=files):
                    selection = selector.select(files, root=fixture.root)
                    self.assertTrue(selection.commands)
                    for command in selection.commands:
                        self.assertIn(" ".join(command), ci_commands)


class RunnerExecutionTest(unittest.TestCase):
    def run_selector(self, fixture: SelectorRoot, args: list[str], fail_suite: str = ""):
        bin_dir = fixture.root / "fake-bin"
        bin_dir.mkdir()
        python3 = bin_dir / "python3"
        python3.write_text(FAKE_PYTHON3)
        python3.chmod(0o700)
        record = fixture.root / "suites.jsonl"
        env = dict(
            os.environ,
            PATH=str(bin_dir) + os.pathsep + "/usr/bin:/bin",
            SUITE_RECORD=str(record),
            FAIL_SUITE=fail_suite,
        )
        result = subprocess.run(
            [sys.executable, str(SCRIPT), "--root", str(fixture.root), *args],
            env=env,
            text=True,
            capture_output=True,
            check=False,
        )
        result.calls = [json.loads(line) for line in record.read_text().splitlines()] if record.exists() else []
        return result

    def test_runner_executes_selected_suites_without_a_shell(self) -> None:
        with SelectorRoot() as fixture:
            result = self.run_selector(fixture, ["scripts/test-delta.py"])
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(len(result.calls), 1)
            # The suite path arrives as one argv element: no shell ever
            # re-tokenized the command. argv[0] is how the runner's
            # "python3" resolved through PATH.
            self.assertEqual(result.calls[0]["argv"][1:], ["scripts/test-delta.py"])
            self.assertTrue(result.calls[0]["argv"][0].endswith("python3"))
            self.assertEqual(result.calls[0]["cwd"], str(fixture.root))

    def test_runner_failure_stops_the_sequence_and_propagates(self) -> None:
        with SelectorRoot() as fixture:
            result = self.run_selector(fixture, ["lefthook.yml"], fail_suite="scripts/test-beta.py")
            self.assertEqual(result.returncode, 7, result.stderr)
            self.assertEqual(
                [call["argv"][1] for call in result.calls],
                ["scripts/test-alpha.py", "scripts/test-beta.py"],
            )
            self.assertIn("scripts/test-beta.py", result.stderr)

    def test_dry_run_prints_the_plan_without_executing(self) -> None:
        with SelectorRoot() as fixture:
            result = self.run_selector(fixture, ["--dry-run", "scripts/test-delta.py"])
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.calls, [])
            self.assertIn("python3 scripts/test-delta.py", result.stdout)

    def test_separator_keeps_push_files_positional(self) -> None:
        # The hook forwards `--` before {push_files}; a path that starts with
        # a dash must arrive as a file, never as a flag.
        with SelectorRoot() as fixture:
            result = self.run_selector(fixture, ["--dry-run", "--", "scripts/test-delta.py"])
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("python3 scripts/test-delta.py", result.stdout)
            self.assertEqual(result.calls, [])

    def test_undecodable_battery_refuses_to_run_anything(self) -> None:
        broken = CI_FIXTURE.replace(
            "run: python3 scripts/test-alpha.py &&",
            "run: sh -c 'python3 scripts/test-alpha.py' &&",
        )
        with SelectorRoot(battery_text=broken) as fixture:
            result = self.run_selector(fixture, ["scripts/test-delta.py"])
            self.assertEqual(result.returncode, 2, result.stderr)
            self.assertEqual(result.calls, [])
            self.assertIn("tooling battery", result.stderr.lower())


# Fixture scenario data for the poisoned-environment cases below: which
# variables a simulated hook sets, and where each points inside the scratch
# outer repository. The sanitization authority is never this list; it is
# scripts/git_environment.py, which discovers the namespace from Git itself.
LOCAL_GIT_ENVIRONMENT_VARS = (
    "GIT_ALTERNATE_OBJECT_DIRECTORIES",
    "GIT_CONFIG",
    "GIT_CONFIG_PARAMETERS",
    "GIT_CONFIG_COUNT",
    "GIT_OBJECT_DIRECTORY",
    "GIT_DIR",
    "GIT_WORK_TREE",
    "GIT_IMPLICIT_WORK_TREE",
    "GIT_GRAFT_FILE",
    "GIT_INDEX_FILE",
    "GIT_NO_REPLACE_OBJECTS",
    "GIT_REPLACE_REF_BASE",
    "GIT_PREFIX",
    "GIT_SHALLOW_FILE",
    "GIT_COMMON_DIR",
    "GIT_QUARANTINE_PATH",
)

# The fixture CI battery selects exactly one synthetic suite.
SCRATCH_COMMIT_CI_FIXTURE = """name: CI
on:
  pull_request:
jobs:
  verify-tooling:
    name: verify-tooling
    runs-on: ubuntu-latest
    steps:
      - name: Test release and installer tooling
        run: python3 scripts/test-scratch-commit.py
"""

# The synthetic suite a hook environment must not reach. It models this
# repository's real tooling suites (scripts/test-release.py works the same
# way): it initializes, configures, and commits its own scratch Git
# repository under a fresh temporary directory, far outside any outer
# repository. It is self-contained on purpose: the testcase must observe the
# real runner-child behavior, never fail on a missing helper import.
SCRATCH_COMMIT_SUITE = r'''#!/usr/bin/env python3
"""Build and commit a scratch Git repository of this suite's own."""
import shutil
import os
import subprocess
import sys
import tempfile
from pathlib import Path


def main() -> int:
    scratch = Path(tempfile.mkdtemp(prefix="con-896-suite-"))
    try:
        repo = scratch / "project"
        repo.mkdir()
        (repo / "tool.txt").write_text("suite scratch content\n", encoding="utf-8")
        commands = (
            ("init", "--quiet"),
            ("config", "user.name", "Suite Operator"),
            ("config", "user.email", "suite-operator@example.invalid"),
            ("add", "tool.txt"),
            ("commit", "--quiet", "--message", "suite scratch commit"),
            ("tag", "suite-scratch-tag"),
            ("rev-parse", "HEAD"),
        )
        head = ""
        for args in commands:
            done = subprocess.run(
                ["git", "-C", str(repo), *args], capture_output=True, text=True
            )
            if done.returncode:
                print(f"scratch suite: git {args[0]} failed: {done.stderr.strip()}", file=sys.stderr)
                return 9
            if args[0] == "rev-parse":
                head = done.stdout.strip()
        if not head:
            print("scratch suite: Git returned an empty HEAD", file=sys.stderr)
            return 9
        Path(os.environ["SUITE_COMPLETION_MARKER"]).write_text(f"HEAD {head}\n", encoding="utf-8")
        return 0
    finally:
        shutil.rmtree(scratch, ignore_errors=True)


if __name__ == "__main__":
    raise SystemExit(main())
'''


class GitEnvironmentTest(unittest.TestCase):
    """The shared sanitization helper (scripts/git_environment.py)."""

    def _discovery_cross_check(self) -> set[str]:
        """An independent execution of Git's own namespace query."""
        env = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
        raw = subprocess.run(
            ["git", "rev-parse", "--local-env-vars"],
            env=env,
            capture_output=True,
            text=True,
            check=True,
        )
        return set(raw.stdout.split())

    def test_namespace_is_discovered_from_the_installed_git(self) -> None:
        discovered = git_environment.local_environment_vars()
        expected = self._discovery_cross_check() | {git_environment.QUARANTINE_VAR}
        self.assertEqual(set(discovered), expected)
        for known in ("GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_QUARANTINE_PATH"):
            self.assertIn(known, discovered)

    def test_sanitized_environment_keeps_unrelated_and_transport_keys(self) -> None:
        poisoned = {
            "PATH": os.environ.get("PATH", ""),
            "HOME": "/scratch-home",
            "LANG": "C",
            "GIT_SSH_COMMAND": "ssh -oProxyCommand=none",
            "GIT_TERMINAL_PROMPT": "0",
            "GIT_DIR": "/scratch/outer/.git",
            "GIT_WORK_TREE": "/scratch/outer",
            "GIT_INDEX_FILE": "/scratch/outer/.git/index",
            "GIT_QUARANTINE_PATH": "/scratch/outer/.git/objects/quarantine",
            "GIT_CONFIG_PARAMETERS": "'user.name=Hook'",
            "GIT_CONFIG_COUNT": "1",
            "GIT_CONFIG_KEY_0": "user.name",
            "GIT_CONFIG_VALUE_0": "Hook",
        }
        cleaned = git_environment.sanitized_environment(poisoned)
        for kept in ("PATH", "HOME", "LANG", "GIT_SSH_COMMAND", "GIT_TERMINAL_PROMPT"):
            self.assertEqual(cleaned.get(kept), poisoned[kept], f"{kept} is not repository-local and must survive")
        for removed in (
            "GIT_DIR",
            "GIT_WORK_TREE",
            "GIT_INDEX_FILE",
            "GIT_QUARANTINE_PATH",
            "GIT_CONFIG_PARAMETERS",
            "GIT_CONFIG_COUNT",
            "GIT_CONFIG_KEY_0",
            "GIT_CONFIG_VALUE_0",
        ):
            self.assertNotIn(removed, cleaned)

    def test_discovery_survives_a_poisoned_inherited_config(self) -> None:
        base = {"PATH": os.environ.get("PATH", "")}
        poisoned = dict(
            base,
            GIT_DIR="/scratch/not/a/repository",
            GIT_CONFIG_COUNT="not-a-number",
            GIT_CONFIG_PARAMETERS="malformed payload with no structure",
            GIT_CONFIG_KEY_9="user.name",
            GIT_CONFIG_VALUE_9="injected",
        )
        self.assertEqual(
            set(git_environment.local_environment_vars(poisoned)),
            set(git_environment.local_environment_vars(base)),
        )

    def test_fail_closed_when_git_cannot_be_found(self) -> None:
        with tempfile.TemporaryDirectory(prefix="con-896-empty-path-") as empty:
            with self.assertRaises(git_environment.GitEnvironmentError):
                git_environment.local_environment_vars({"PATH": empty})

    def test_fail_closed_on_an_empty_or_malformed_namespace(self) -> None:
        for name, body in (
            ("empty", "#!/bin/sh\nexit 0\n"),
            ("malformed", "#!/bin/sh\nprintf 'GIT_DIR\\nNOT_A_GIT_VARIABLE\\n'\n"),
        ):
            with self.subTest(stub=name):
                with tempfile.TemporaryDirectory(prefix=f"con-896-stub-{name}-") as stub_dir:
                    stub = Path(stub_dir) / "git"
                    stub.write_text(body, encoding="utf-8")
                    stub.chmod(0o700)
                    with self.assertRaises(git_environment.GitEnvironmentError):
                        git_environment.local_environment_vars({"PATH": stub_dir})


class HookEnvironmentLeakTest(unittest.TestCase):
    """Suite children must not inherit a hook's Git environment.

    A hook invokes the real selector runner with Git's local environment
    variables set for the outer repository. The runner executes selected
    suites as children, so an inherited variable redirects every git call a
    suite makes into the outer repository, or refuses it outright. The real
    runner runs here under hook-like inherited variables that point only at a
    scratch outer repository; the suite must still succeed and the outer
    repository must come back byte-for-byte unchanged.
    """

    HOOK_ENVIRONMENT_CASES = (
        "GIT_DIR",
        "GIT_WORK_TREE",
        "GIT_INDEX_FILE",
        "GIT_COMMON_DIR",
        "GIT_QUARANTINE_PATH",
        "local-env-vars",
    )

    def _clean_hook_env(self) -> dict[str, str]:
        # The production helper is the single sanitization authority; the
        # fixture builds its poisoned cases on top of a sanitized base.
        return dict(git_environment.sanitized_environment())

    def _outer_scratch_repository(self, parent: Path) -> Path:
        parent.mkdir(parents=True, exist_ok=True)
        outer = parent / "outer-repository"
        outer.mkdir()

        def git(*args: str) -> None:
            subprocess.run(
                ["git", "-C", str(outer), *args],
                check=True,
                capture_output=True,
                text=True,
                env=self._clean_hook_env(),
            )

        git("init", "--quiet")
        git("config", "user.name", "Outer Keeper")
        git("config", "user.email", "outer-keeper@example.invalid")
        (outer / "outer-file.txt").write_text("outer worktree content\n", encoding="utf-8")
        git("add", "outer-file.txt")
        git("commit", "--quiet", "--message", "outer baseline")
        git("tag", "outer-tag")
        return outer

    def _snapshot(self, root: Path) -> dict[str, bytes]:
        """Byte inventory: config, HEAD, refs, tags, index, objects, worktree."""
        inventory: dict[str, bytes] = {}
        for path in sorted(root.rglob("*")):
            rel = str(path.relative_to(root))
            if path.is_symlink():
                inventory[rel] = os.readlink(path).encode()
            elif path.is_dir():
                inventory[rel + os.sep] = b""
            else:
                inventory[rel] = path.read_bytes()
        return inventory

    def _hook_environment(self, name: str, outer: Path) -> dict[str, str]:
        """One hook-like inheritance case; every path stays inside `outer`."""
        git_dir = outer / ".git"
        objects = git_dir / "objects"
        if name == "GIT_DIR":
            return {"GIT_DIR": str(git_dir)}
        if name == "GIT_WORK_TREE":
            return {"GIT_WORK_TREE": str(outer)}
        if name == "GIT_INDEX_FILE":
            return {"GIT_INDEX_FILE": str(git_dir / "index")}
        if name == "GIT_COMMON_DIR":
            return {"GIT_COMMON_DIR": str(git_dir)}
        if name == "GIT_QUARANTINE_PATH":
            # A pre-receive hook inherits the quarantine alongside GIT_DIR.
            return {
                "GIT_DIR": str(git_dir),
                "GIT_QUARANTINE_PATH": str(objects / "quarantine-incoming"),
            }
        if name == "local-env-vars":
            return {
                "GIT_DIR": str(git_dir),
                "GIT_WORK_TREE": str(outer),
                "GIT_INDEX_FILE": str(git_dir / "index"),
                "GIT_COMMON_DIR": str(git_dir),
                "GIT_OBJECT_DIRECTORY": str(objects / "alt-object-store"),
                "GIT_ALTERNATE_OBJECT_DIRECTORIES": str(objects / "alternates-scratch"),
                "GIT_QUARANTINE_PATH": str(objects / "quarantine-incoming"),
                "GIT_CONFIG": str(outer / "scratch-git-config"),
                "GIT_CONFIG_COUNT": "2",
                "GIT_CONFIG_KEY_0": "user.name",
                "GIT_CONFIG_VALUE_0": "Hook Injected",
                "GIT_CONFIG_KEY_1": "user.email",
                "GIT_CONFIG_VALUE_1": "hook@example.invalid",
                "GIT_CONFIG_PARAMETERS": "'user.name=HookParameters'",
                "GIT_GRAFT_FILE": str(outer / "scratch-graft"),
                "GIT_SHALLOW_FILE": str(outer / "scratch-shallow"),
                "GIT_NO_REPLACE_OBJECTS": "1",
                "GIT_REPLACE_REF_BASE": "refs/replace/",
                "GIT_PREFIX": "",
                "GIT_IMPLICIT_WORK_TREE": str(outer),
            }
        raise AssertionError(f"unknown hook environment case: {name}")

    def _prepare_hook_environment_fixture(self, name: str, outer: Path) -> None:
        """Create, before the before-snapshot, every path a case points at.

        A redirected child would create or write these inside the outer
        repository; preparing them first means the comparison after the run
        proves the fix leaves them exactly as prepared, instead of trading on
        paths that never existed when the snapshot was taken.
        """
        objects = outer / ".git" / "objects"
        directories = []
        files = []
        if name == "GIT_QUARANTINE_PATH":
            directories = [objects / "quarantine-incoming"]
        elif name == "local-env-vars":
            directories = [
                objects / "quarantine-incoming",
                objects / "alt-object-store",
                objects / "alternates-scratch",
            ]
            files = [outer / "scratch-git-config", outer / "scratch-graft", outer / "scratch-shallow"]
        for directory in directories:
            directory.mkdir(parents=True, exist_ok=True)
        for file in files:
            file.write_bytes(b"")

    def _leak_report(self, name: str, before: dict[str, bytes], after: dict[str, bytes]) -> str:
        changed = sorted(
            {key for key in before.keys() & after.keys() if before[key] != after[key]}
            | (before.keys() ^ after.keys())
        )
        shown = ", ".join(changed[:10]) if changed else "none"
        return (
            f"the inherited {name} environment redirected suite Git operations "
            f"into the outer scratch repository ({len(changed)} paths changed: {shown})"
        )

    def _invoke_runner(self, fixture: SelectorRoot, env: dict[str, str], runner: Path = SCRIPT):
        return subprocess.run(
            [sys.executable, str(runner), "--root", str(fixture.root), "lefthook.yml"],
            env=env,
            text=True,
            capture_output=True,
            check=False,
        )

    def _assert_hook_case_result(
        self,
        name: str,
        result: subprocess.CompletedProcess[str],
        before: dict[str, bytes],
        after: dict[str, bytes],
        completion_marker: Path,
    ) -> None:
        # Compare snapshots before returncode so a failed child still reports writes.
        self.assertEqual(before, after, self._leak_report(name, before, after))
        self.assertIn(
            "script-suite python3 scripts/test-scratch-commit.py", result.stdout, result.stderr
        )
        self.assertEqual(
            result.returncode,
            0,
            f"the suite broke under the inherited {name} environment:\n{result.stderr}",
        )
        self.assertTrue(completion_marker.is_file(), "the selected suite did not write its completion marker")
        self.assertRegex(
            completion_marker.read_text(encoding="utf-8"),
            r"^HEAD (?:[0-9a-f]{40}|[0-9a-f]{64})\n$",
            "the marker must contain the scratch repository HEAD after all Git commands succeed",
        )

    def _exercise_hook_case(
        self,
        name: str,
        runner: Path = SCRIPT,
        suite_text: str = SCRATCH_COMMIT_SUITE,
    ) -> None:
        with tempfile.TemporaryDirectory(prefix="con-896-outer-") as outer_dir:
            outer = self._outer_scratch_repository(Path(outer_dir))
            self._prepare_hook_environment_fixture(name, outer)
            before = self._snapshot(outer)
            with SelectorRoot(battery_text=SCRATCH_COMMIT_CI_FIXTURE) as fixture:
                fixture.script("test-scratch-commit.py", suite_text)
                completion_marker = fixture.root / "suite-completed.txt"
                env = self._clean_hook_env()
                env.update(self._hook_environment(name, outer))
                env["SUITE_COMPLETION_MARKER"] = str(completion_marker)
                result = self._invoke_runner(fixture, env, runner)
                after = self._snapshot(outer)
                self._assert_hook_case_result(name, result, before, after, completion_marker)

    def test_runner_children_do_not_inherit_a_hook_git_environment(self) -> None:
        for name in self.HOOK_ENVIRONMENT_CASES:
            with self.subTest(hook_environment=name):
                # The real selector runner is invoked as a hook would invoke
                # it; the fixture CI battery selects one synthetic suite.
                self._exercise_hook_case(name)

    def test_negative_control_rejects_runner_that_only_prints_a_plan(self) -> None:
        with tempfile.TemporaryDirectory(prefix="con-896-skipped-runner-") as directory:
            fake_runner = Path(directory) / "skipped_runner.py"
            fake_runner.write_text(
                "print('script-suite python3 scripts/test-scratch-commit.py')\n",
                encoding="utf-8",
            )
            # Exercise the exact assertion path used by all six real cases.
            # This runner prints the plan and exits 0 without spawning the
            # synthetic child; the missing completion marker must reject it.
            with self.assertRaises(AssertionError):
                self._exercise_hook_case("GIT_DIR", runner=fake_runner)

    def _assert_namespace_discovery_refusal(
        self,
        runner: Path = SCRIPT,
        suite_text: str = SCRATCH_COMMIT_SUITE,
    ) -> None:
        # Fail closed: when Git's own namespace cannot be discovered, the
        # runner must refuse to spawn any suite rather than risk poisoned
        # children, however healthy the selection itself looks.
        with tempfile.TemporaryDirectory(prefix="con-896-stub-git-") as stub_dir:
            stub = Path(stub_dir) / "git"
            stub.write_text("#!/bin/sh\necho 'stub git refuses discovery' >&2\nexit 1\n", encoding="utf-8")
            stub.chmod(0o700)
            bin_dir = Path(stub_dir) / "fake-bin"
            bin_dir.mkdir()
            python3 = bin_dir / "python3"
            python3.write_text(FAKE_PYTHON3)
            python3.chmod(0o700)
            record = Path(stub_dir) / "suites.jsonl"
            recorded_suites = None
            with SelectorRoot(battery_text=SCRATCH_COMMIT_CI_FIXTURE) as fixture:
                fixture.script("test-scratch-commit.py", suite_text)
                env = dict(
                    os.environ,
                    PATH=str(bin_dir) + os.pathsep + str(stub_dir) + os.pathsep + "/usr/bin:/bin",
                    SUITE_RECORD=str(record),
                )
                result = self._invoke_runner(fixture, env, runner)
                if record.is_file():
                    recorded_suites = record.read_text(encoding="utf-8")
        self.assertEqual(result.returncode, 2, result.stderr)
        self.assertIsNone(
            recorded_suites,
            f"a refused runner spawned a suite before refusing: {recorded_suites!r}",
        )
        self.assertIn("refusing to spawn suites", result.stderr)

    def test_runner_refuses_to_spawn_suites_when_namespace_discovery_fails(self) -> None:
        self._assert_namespace_discovery_refusal()

    def test_negative_control_rejects_refusal_after_a_child_wrote_its_marker(self) -> None:
        with tempfile.TemporaryDirectory(prefix="con-896-erroneous-runner-") as directory:
            root = Path(directory)
            fake_runner = root / "erroneous_runner.py"
            spawn_proof = root / "child-spawn-proof.txt"
            child_text = (
                "import os\n"
                "from pathlib import Path\n"
                "Path(os.environ['SUITE_RECORD']).write_text('child completed\\n', encoding='utf-8')\n"
            )
            fake_runner.write_text(
                "import os, subprocess, sys\n"
                "from pathlib import Path\n"
                "root = Path(sys.argv[sys.argv.index('--root') + 1])\n"
                "subprocess.run([sys.executable, str(root / 'scripts/test-scratch-commit.py')], check=True, env=os.environ.copy())\n"
                "record = Path(os.environ['SUITE_RECORD'])\n"
                "Path(" + repr(str(spawn_proof)) + ").write_text(record.read_text(encoding='utf-8'), encoding='utf-8')\n"
                "print('run-script-tests: refusing to spawn suites with an undiscovered Git environment', file=sys.stderr)\n"
                "raise SystemExit(2)\n",
                encoding="utf-8",
            )
            try:
                # The fake runner really spawns the fixture child and copies
                # its marker outside the helper's cleaned tempdir. The shared
                # P2 assertions must reject its false refusal.
                with self.assertRaises(AssertionError):
                    self._assert_namespace_discovery_refusal(
                        runner=fake_runner,
                        suite_text=child_text,
                    )
            finally:
                self.assertEqual(spawn_proof.read_text(encoding="utf-8"), "child completed\n")


class RepositoryBatteryTest(unittest.TestCase):
    def test_real_repository_battery_starts_with_the_ci_battery(self) -> None:
        selector = load_selector(REPO)
        battery = selector.tooling_battery(REPO)
        self.assertTrue(battery, "the tooling battery must derive from ci.yml")
        self.assertEqual(battery[0], "scripts/test-release.py")
        self.assertIn("scripts/test-pre-push.py", battery)
        self.assertIn("scripts/test-run-script-tests.py", battery)

    def test_real_repository_selection_runs_a_changed_checker_suite(self) -> None:
        selector = load_selector(REPO)
        selection = selector.select(["scripts/check-json.py"], root=REPO)
        self.assertIn(["python3", "scripts/test-check-json.py"], selection.commands)
        self.assertFalse(selection.used_battery_fallback)


if __name__ == "__main__":
    unittest.main()

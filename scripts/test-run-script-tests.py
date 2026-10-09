#!/usr/bin/env python3
"""Tests for the changed-file script suite selector (CON-893 preflight)."""

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


REPO = Path(__file__).resolve().parents[1]
SCRIPT = REPO / "scripts/run-script-tests.py"

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
        # Four-space step indentation is valid YAML but not the historical
        # six/eight/ten indent grammar; a real parser must still read it.
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

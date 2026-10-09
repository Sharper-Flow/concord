#!/usr/bin/env python3
"""Tests for the pre-push preflight drift validator (CON-893).

Two conformance planes:

- Unit drift: `scripts/check-pre-push.py` fails when lefthook.yml loses a
  gate, pin, environment, or diverges from the CI commands that own the same
  command. No routing is modeled here; glob routing is proved by running the
  real pinned Lefthook below.
- Real tool: the required routing table runs the pinned Lefthook release in
  isolated temporary Git repositories, with the real lefthook.yml glob lists
  and marker-emitting commands, through actual `--file` paths. A glob edit in
  the real config changes what the real tool runs, so these fixtures fail CI
  when routing drifts.
"""

from __future__ import annotations

import importlib.util
import json
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

try:
    import yaml
except ImportError:  # pragma: no cover - runner images carry PyYAML
    yaml = None

import git_environment

# CON-896: this suite builds temporary Git repositories for the real pinned
# Lefthook runs, so a hook that launched it must not keep a redirecting Git
# namespace in place. The scrub runs before the in-process checker loads.
git_environment.scrub_inherited()


REPO = Path(__file__).resolve().parents[1]
CHECKER = REPO / "scripts/check-pre-push.py"
SPEC = importlib.util.spec_from_file_location("pre_push_checker", CHECKER)
assert SPEC and SPEC.loader
checker = importlib.util.module_from_spec(SPEC)
sys.modules.setdefault("pre_push_checker", checker)
SPEC.loader.exec_module(checker)

# The pinned upstream release: an external tool, never a go.mod dependency.
LEFTHOOK_MODULE = "github.com/evilmartians/lefthook/v2"
LEFTHOOK_PIN = "v2.1.14"
LEFTHOOK = ["go", "run", f"{LEFTHOOK_MODULE}@{LEFTHOOK_PIN}"]

GO_GATES = ("gofmt", "go-vet", "go-build", "go-compile-tests", "go-lint")
GO_PLANE_GATES = GO_GATES + ("check-reachability", "check-complexity")
ALL_GATES = GO_PLANE_GATES + ("check-json", "script-tests")

# Required routing: each representative push path must select exactly this
# gate set under the real lefthook.yml globs, executed by the real tool.
REQUIRED_ROUTES: tuple[tuple[str, tuple[str, ...]], ...] = (
    ("internal/store/store.go", GO_PLANE_GATES),
    ("cmd/concord/main.go", GO_PLANE_GATES),
    ("main.go", GO_PLANE_GATES),
    ("go.mod", GO_PLANE_GATES),
    ("go.sum", GO_PLANE_GATES),
    (".golangci.yml", GO_GATES),
    ("scripts/check-json.py", ("check-json", "script-tests")),
    ("scripts/generate-work-kinds.py", ("check-json", "script-tests")),
    ("scripts/knowledge_index.py", ("script-tests",)),
    ("scripts/test-alpha.py", ("script-tests",)),
    ("scripts/check-reachability.py", ("check-reachability", "check-json", "script-tests")),
    ("scripts/check-complexity.py", ("check-complexity", "check-json", "script-tests")),
    ("contracts/concord-knowledge-index.v1.schema.json", ("check-json",)),
    (".concord/tooling.v1.json", ("check-json", "script-tests")),
    (".concord/docs/reachability-exceptions.v1.json", ("check-json", "check-reachability")),
    ("adapter/opencode/session.test.ts", ("check-json",)),
    ("bin/oc-test", ("script-tests",)),
    (".github/workflows/ci.yml", ALL_GATES),
    ("lefthook.yml", ALL_GATES),
    ("README.md", ()),
)


class SyntheticRoot:
    """A throwaway repository carrying the real hook, CI, and scripts."""

    def __init__(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.root = Path(self.tempdir.name)
        shutil.copyfile(REPO / "lefthook.yml", self.root / "lefthook.yml")
        (self.root / ".github/workflows").mkdir(parents=True)
        shutil.copyfile(REPO / ".github/workflows/ci.yml", self.root / ".github/workflows/ci.yml")
        shutil.copytree(REPO / "scripts", self.root / "scripts", ignore=shutil.ignore_patterns("__pycache__"))

    def mutate_hook(self, mutate) -> None:
        document = yaml.safe_load((self.root / "lefthook.yml").read_text(encoding="utf-8"))
        mutate(document)
        (self.root / "lefthook.yml").write_text(yaml.safe_dump(document, sort_keys=False), encoding="utf-8")

    def replace_ci_text(self, old: str, new: str) -> None:
        path = self.root / ".github/workflows/ci.yml"
        text = path.read_text(encoding="utf-8")
        assert old in text, f"fixture lost its anchor: {old!r}"
        path.write_text(text.replace(old, new), encoding="utf-8")

    def __enter__(self) -> "SyntheticRoot":
        return self

    def __exit__(self, *unused) -> None:
        self.tempdir.cleanup()


class RepositoryDriftTest(unittest.TestCase):
    def test_repository_itself_passes(self) -> None:
        self.assertEqual(checker.check(), [])

    def test_pyyaml_absence_fails_visibly(self) -> None:
        original = checker.yaml
        try:
            checker.yaml = None
            findings = checker.check()
        finally:
            checker.yaml = original
        self.assertTrue(any(finding.startswith("pyyaml-required:") for finding in findings), findings)


class HookConfigDriftTest(unittest.TestCase):
    def test_missing_hook_config_is_a_finding(self) -> None:
        with SyntheticRoot() as fixture:
            (fixture.root / "lefthook.yml").unlink()
            findings = checker.check(root=fixture.root)
            self.assertTrue(any(finding.startswith("hook-config:") for finding in findings), findings)

    def test_malformed_hook_yaml_returns_structured_findings(self) -> None:
        with SyntheticRoot() as fixture:
            (fixture.root / "lefthook.yml").write_text("pre-push: [unclosed\n", encoding="utf-8")
            findings = checker.check(root=fixture.root)
            self.assertTrue(any(finding.startswith("hook-config:") for finding in findings), findings)

    def test_malformed_ci_yaml_returns_structured_findings(self) -> None:
        with SyntheticRoot() as fixture:
            (fixture.root / ".github/workflows/ci.yml").write_text("jobs: [unclosed\n", encoding="utf-8")
            findings = checker.check(root=fixture.root)
            self.assertTrue(
                any(finding.startswith("hook-config:") and "ci.yml" in finding for finding in findings),
                findings,
            )

    def test_missing_gate_is_a_finding(self) -> None:
        with SyntheticRoot() as fixture:
            fixture.mutate_hook(lambda doc: doc["pre-push"]["commands"].pop("go-lint"))
            findings = checker.check(root=fixture.root)
            self.assertTrue(any("gate-missing: go-lint" in finding for finding in findings), findings)

    def test_renamed_gate_is_a_finding(self) -> None:
        with SyntheticRoot() as fixture:
            def rename(doc) -> None:
                commands = doc["pre-push"]["commands"]
                commands["go-format"] = commands.pop("gofmt")

            fixture.mutate_hook(rename)
            findings = checker.check(root=fixture.root)
            self.assertTrue(any("gate-missing: gofmt" in finding for finding in findings), findings)
            self.assertTrue(any("gate-unknown: go-format" in finding for finding in findings), findings)

    def test_command_token_drift_is_a_finding(self) -> None:
        with SyntheticRoot() as fixture:
            def drift(doc) -> None:
                doc["pre-push"]["commands"]["go-vet"]["run"] = "go vet -shift ./..."

            fixture.mutate_hook(drift)
            findings = checker.check(root=fixture.root)
            self.assertTrue(any("gate-parity: go-vet" in finding for finding in findings), findings)

    def test_version_pin_drift_is_a_finding(self) -> None:
        with SyntheticRoot() as fixture:
            def drift(doc) -> None:
                run = doc["pre-push"]["commands"]["go-lint"]["run"]
                doc["pre-push"]["commands"]["go-lint"]["run"] = run.replace("v2.13.1", "v2.13.2")

            fixture.mutate_hook(drift)
            findings = checker.check(root=fixture.root)
            self.assertTrue(any("gate-parity: go-lint" in finding for finding in findings), findings)

    def test_flag_drift_is_a_finding(self) -> None:
        with SyntheticRoot() as fixture:
            def drift(doc) -> None:
                run = doc["pre-push"]["commands"]["check-json"]["run"]
                doc["pre-push"]["commands"]["check-json"]["run"] = run.replace(" --contract-tests-external", "")

            fixture.mutate_hook(drift)
            findings = checker.check(root=fixture.root)
            self.assertTrue(any("gate-parity: check-json" in finding for finding in findings), findings)

    def test_environment_value_drift_is_a_finding(self) -> None:
        with SyntheticRoot() as fixture:
            def drift(doc) -> None:
                doc["pre-push"]["commands"]["check-json"]["env"]["CONCORD_REQUIRE_BUN"] = "0"

            fixture.mutate_hook(drift)
            findings = checker.check(root=fixture.root)
            self.assertTrue(any("gate-env-parity: check-json" in finding for finding in findings), findings)

    def test_missing_environment_is_a_finding(self) -> None:
        with SyntheticRoot() as fixture:
            def drift(doc) -> None:
                doc["pre-push"]["commands"]["check-json"].pop("env")

            fixture.mutate_hook(drift)
            findings = checker.check(root=fixture.root)
            self.assertTrue(any("gate-env-parity: check-json" in finding for finding in findings), findings)

    def test_unexpected_gate_environment_is_a_finding(self) -> None:
        with SyntheticRoot() as fixture:
            def drift(doc) -> None:
                doc["pre-push"]["commands"]["gofmt"]["env"] = {"CONCORD_REQUIRE_BUN": "1"}

            fixture.mutate_hook(drift)
            findings = checker.check(root=fixture.root)
            self.assertTrue(any("gate-env-parity: gofmt" in finding for finding in findings), findings)

    def test_min_version_drift_is_a_finding(self) -> None:
        with SyntheticRoot() as fixture:
            fixture.mutate_hook(lambda doc: doc.update(min_version="2.1.13"))
            findings = checker.check(root=fixture.root)
            self.assertTrue(any(finding.startswith("hook-pin:") for finding in findings), findings)

    def test_assert_lefthook_installed_is_required(self) -> None:
        with SyntheticRoot() as fixture:
            fixture.mutate_hook(lambda doc: doc.update(assert_lefthook_installed=False))
            findings = checker.check(root=fixture.root)
            self.assertTrue(any("assert_lefthook_installed" in finding for finding in findings), findings)

    def test_no_auto_install_is_required(self) -> None:
        with SyntheticRoot() as fixture:
            fixture.mutate_hook(lambda doc: doc.update(no_auto_install=False))
            findings = checker.check(root=fixture.root)
            self.assertTrue(any("no_auto_install" in finding for finding in findings), findings)

    def test_parallel_pre_push_is_required(self) -> None:
        with SyntheticRoot() as fixture:
            fixture.mutate_hook(lambda doc: doc["pre-push"].update(parallel=False))
            findings = checker.check(root=fixture.root)
            self.assertTrue(any("parallel" in finding for finding in findings), findings)

    def test_selector_command_drift_is_a_finding(self) -> None:
        with SyntheticRoot() as fixture:
            def drift(doc) -> None:
                doc["pre-push"]["commands"]["script-tests"]["run"] += " --extra"

            fixture.mutate_hook(drift)
            findings = checker.check(root=fixture.root)
            self.assertTrue(any("selector-parity:" in finding for finding in findings), findings)

    def test_selector_must_forward_the_separator(self) -> None:
        with SyntheticRoot() as fixture:
            def drift(doc) -> None:
                doc["pre-push"]["commands"]["script-tests"]["run"] = (
                    "python3 scripts/run-script-tests.py {push_files}"
                )

            fixture.mutate_hook(drift)
            findings = checker.check(root=fixture.root)
            self.assertTrue(any("selector-parity:" in finding for finding in findings), findings)

    def test_placeholder_outside_the_selector_is_a_finding(self) -> None:
        with SyntheticRoot() as fixture:
            def drift(doc) -> None:
                doc["pre-push"]["commands"]["gofmt"]["run"] = 'test -z "$(gofmt -l {push_files})"'

            fixture.mutate_hook(drift)
            findings = checker.check(root=fixture.root)
            self.assertTrue(any(finding.startswith("placeholder:") for finding in findings), findings)


class CiDriftTest(unittest.TestCase):
    def test_changed_ci_command_drops_the_gate(self) -> None:
        with SyntheticRoot() as fixture:
            fixture.replace_ci_text("run: go vet ./...", "run: go vet -std ./...")
            findings = checker.check(root=fixture.root)
            self.assertTrue(any("gate-parity: go-vet" in finding for finding in findings), findings)

    def test_absent_ci_step_drops_the_gate(self) -> None:
        with SyntheticRoot() as fixture:
            fixture.replace_ci_text(
                "      - name: Lint Go\n        run: go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.1 run --output.text.colors=false ./...\n",
                "",
            )
            findings = checker.check(root=fixture.root)
            self.assertTrue(any("gate-parity: go-lint" in finding for finding in findings), findings)

    def test_changed_ci_environment_drops_the_gate(self) -> None:
        with SyntheticRoot() as fixture:
            fixture.replace_ci_text('CONCORD_REQUIRE_BUN: "1"', 'CONCORD_REQUIRE_BUN: "0"')
            findings = checker.check(root=fixture.root)
            self.assertTrue(any("gate-env-parity: check-json" in finding for finding in findings), findings)

    def test_changed_ci_battery_is_followed_not_flagged(self) -> None:
        # The battery is derived from CI with the same real parser the
        # selector uses, so editing CI moves both sides together.
        with SyntheticRoot() as fixture:
            fixture.replace_ci_text(" && python3 scripts/test-pre-push.py", "")
            findings = checker.check(root=fixture.root)
            self.assertEqual(findings, [])


class SelectorContractTest(unittest.TestCase):
    def test_selector_module_missing_is_a_finding(self) -> None:
        with SyntheticRoot() as fixture:
            (fixture.root / "scripts/run-script-tests.py").unlink()
            findings = checker.check(root=fixture.root)
            self.assertTrue(any(finding.startswith("selector-module:") for finding in findings), findings)

    def test_undecodable_battery_is_a_finding(self) -> None:
        with SyntheticRoot() as fixture:
            (fixture.root / "scripts/run-script-tests.py").write_text(
                "#!/usr/bin/env python3\n"
                "class BatteryError(Exception):\n    pass\n"
                "def tooling_battery(root):\n"
                "    raise BatteryError('undecodable')\n"
                "def ci_suites(root):\n"
                "    return []\n",
                encoding="utf-8",
            )
            findings = checker.check(root=fixture.root)
            self.assertTrue(any(finding.startswith("battery-derivation:") for finding in findings), findings)

    def test_battery_naming_missing_suites_is_a_finding(self) -> None:
        with SyntheticRoot() as fixture:
            (fixture.root / "scripts/run-script-tests.py").write_text(
                "#!/usr/bin/env python3\n"
                "def tooling_battery(root):\n"
                "    return ['scripts/test-alpha.py']\n"
                "def ci_suites(root):\n"
                "    return ['scripts/test-alpha.py']\n",
                encoding="utf-8",
            )
            findings = checker.check(root=fixture.root)
            self.assertTrue(any(finding.startswith("suite-missing:") for finding in findings), findings)

    def test_battery_suite_missing_on_disk_is_a_finding(self) -> None:
        with SyntheticRoot() as fixture:
            (fixture.root / "scripts/test-release.py").unlink()
            findings = checker.check(root=fixture.root)
            self.assertTrue(any(finding.startswith("suite-missing:") for finding in findings), findings)


class RealToolRepository:
    """An isolated Git repository running the real pinned Lefthook.

    The real lefthook.yml glob lists are kept; gate run commands are replaced
    by markers so a gate's execution is observable without running the real
    (heavy) commands. `no_auto_install` stays on, so nothing here writes a
    Git hook.
    """

    def __init__(self, document: dict, raw_config: str | None = None) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.root = Path(self.tempdir.name)
        (self.root / ".routing").mkdir()
        subprocess.run(["git", "init", "-q", "."], cwd=self.root, check=True, capture_output=True)
        subprocess.run(["git", "config", "user.email", "conformance@example.com"], cwd=self.root, check=True)
        subprocess.run(["git", "config", "user.name", "conformance"], cwd=self.root, check=True)
        config = raw_config if raw_config is not None else yaml.safe_dump(document, sort_keys=False)
        (self.root / "lefthook.yml").write_text(config, encoding="utf-8")

    def add_file(self, path: str, text: str = "") -> None:
        target = self.root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(text, encoding="utf-8")

    def commit(self) -> None:
        subprocess.run(["git", "add", "-A"], cwd=self.root, check=True, capture_output=True)
        subprocess.run(["git", "commit", "-qm", "fixture"], cwd=self.root, check=True, capture_output=True)

    def clear_markers(self) -> None:
        for marker in (self.root / ".routing").iterdir():
            marker.unlink()

    def markers(self) -> set[str]:
        return {marker.name for marker in (self.root / ".routing").iterdir()}

    def run_hook(self, *files: str) -> subprocess.CompletedProcess:
        argv = list(LEFTHOOK) + ["run", "pre-push"]
        for path in files:
            argv += ["--file", path]
        return subprocess.run(argv, cwd=self.root, text=True, capture_output=True, check=False)

    def __enter__(self) -> "RealToolRepository":
        return self

    def __exit__(self, *unused) -> None:
        self.tempdir.cleanup()


def marker_config(mutate=None) -> dict:
    """The real lefthook.yml with marker commands and untouched glob lists."""
    document = yaml.safe_load((REPO / "lefthook.yml").read_text(encoding="utf-8"))
    for name, command in document["pre-push"]["commands"].items():
        command.pop("env", None)
        command["run"] = f"touch .routing/{name}"
    if mutate:
        mutate(document)
    return document


@unittest.skipUnless(shutil.which("go"), "real-tool routing needs go on PATH")
class RealToolRoutingTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.repository = RealToolRepository(marker_config())
        for path, _ in REQUIRED_ROUTES:
            if path != "lefthook.yml":
                cls.repository.add_file(path)
        cls.repository.commit()

    @classmethod
    def tearDownClass(cls) -> None:
        cls.repository.tempdir.cleanup()

    def test_each_representative_path_selects_exactly_its_required_gates(self) -> None:
        for path, expected in REQUIRED_ROUTES:
            with self.subTest(path=path):
                self.repository.clear_markers()
                result = self.repository.run_hook(path)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertEqual(self.repository.markers(), set(expected), result.stdout + result.stderr)

    def test_a_real_glob_edit_changes_real_routing(self) -> None:
        # Proof the fixtures above detect routing drift: dropping the Go
        # pattern from one gate changes what the real tool runs for a Go
        # path. Rebinding the list (not mutating it) keeps the gates that
        # share the YAML anchor untouched.
        def drift(document) -> None:
            vet = document["pre-push"]["commands"]["go-vet"]
            vet["glob"] = [pattern for pattern in vet["glob"] if pattern != "*.go"]

        with RealToolRepository(marker_config(drift)) as repository:
            repository.add_file("internal/store/store.go")
            repository.commit()
            result = repository.run_hook("internal/store/store.go")
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertNotIn("go-vet", repository.markers())
            self.assertIn("gofmt", repository.markers())
            self.assertIn("go-build", repository.markers())

    def test_running_the_hook_installs_nothing(self) -> None:
        hooks = list((self.repository.root / ".git/hooks").iterdir())
        self.assertTrue(hooks, "a git init always ships sample hooks")
        self.assertTrue(all(hook.name.endswith(".sample") for hook in hooks), hooks)


@unittest.skipUnless(shutil.which("go"), "real-tool config smoke needs go on PATH")
class RealToolConfigSmokeTest(unittest.TestCase):
    def test_the_committed_config_parses_under_the_real_tool(self) -> None:
        # The routing fixtures re-dump a parsed copy, so the committed file
        # itself (YAML anchors included) is exercised verbatim here. A
        # documentation-only push selects no gate, so nothing heavy runs.
        with RealToolRepository({}, raw_config=(REPO / "lefthook.yml").read_text(encoding="utf-8")) as repository:
            repository.add_file("README.md")
            repository.commit()
            result = repository.run_hook("README.md")
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual(repository.markers(), set())


@unittest.skipUnless(shutil.which("go"), "real-tool quoting needs go on PATH")
class RealToolQuotingTest(unittest.TestCase):
    WEIRD_PATHS = (
        "scripts/plain.py",
        "scripts/weird name; touch PWNED.py",
        "scripts/$(touch PWNED2).py",
        "scripts/'quoted'.py",
    )

    def test_placeholders_quote_spaces_and_metacharacters(self) -> None:
        document = {
            "min_version": LEFTHOOK_PIN.lstrip("v"),
            "assert_lefthook_installed": True,
            "no_auto_install": True,
            "pre-push": {
                "parallel": True,
                "commands": {
                    "forward": {
                        "glob": "scripts/*.py",
                        "run": "python3 scripts/probe.py -- {push_files}",
                    }
                },
            },
        }
        with RealToolRepository(document) as repository:
            repository.add_file(
                "scripts/probe.py",
                "#!/usr/bin/env python3\n"
                "import json, sys\n"
                "open('.routing/argv.json', 'w').write(json.dumps(sys.argv[1:]))\n",
            )
            for path in self.WEIRD_PATHS:
                repository.add_file(path)
            repository.commit()
            result = repository.run_hook(*self.WEIRD_PATHS)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            argv = json.loads((repository.root / ".routing/argv.json").read_text(encoding="utf-8"))
            self.assertEqual(argv, ["--", *self.WEIRD_PATHS])
            self.assertFalse((repository.root / "PWNED").exists(), "shell injection through a placeholder")
            self.assertFalse((repository.root / "PWNED2").exists(), "shell injection through a placeholder")


if __name__ == "__main__":
    unittest.main()

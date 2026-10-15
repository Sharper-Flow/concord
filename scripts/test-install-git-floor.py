#!/usr/bin/env python3
"""The installer Git integration floor: require_git_version and preflight.

The accepted `git --version` report shape and the signed 64-bit bound on
every numeric component mirror the runtime parser in
internal/store/worker_integration_git_version.go. The installer
preflight must consult the same floor before any write, and a guard refusal
must block the aggregated preflight verdict.

The workflow battery: every checkout-bearing CI and release job runs the
same canonical `require_git_version` guard through runpy as its first
Git-consuming workload, no job without a checkout carries a guard, and no
step installs a replacement Git package. Each distinct guard command the
workflows declare is exercised as a subprocess against a fake `git` on
PATH; no packages are installed in any test here.
"""
from __future__ import annotations

import os
import re
import shlex
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import yaml

import install as installer

ROOT = Path(__file__).resolve().parents[1]
CI_WORKFLOW = ROOT / ".github" / "workflows" / "ci.yml"
RELEASE_WORKFLOW = ROOT / ".github" / "workflows" / "release.yml"
GUARD_STEP_NAME = "Check Git integration prerequisite"
GUARD_RUN = (
    'python3 -c "import runpy; '
    "print(runpy.run_path('scripts/install.py')['require_git_version']())\""
)
PACKAGE_INSTALL_RE = re.compile(r"\b(?:apt(?:-get)?|dnf|yum|apk|snap|zypper|pip3?)\s+install\b")


def load_workflow(path: Path) -> dict:
    document = yaml.safe_load(path.read_text(encoding="utf-8"))
    if not isinstance(document, dict):
        raise AssertionError(f"{path} must parse to a YAML mapping")
    return document


def workflow_jobs(document: dict) -> dict:
    block = document.get("jobs")
    if not isinstance(block, dict):
        raise AssertionError("workflow jobs must parse to a mapping")
    return block


def checkout_steps(steps: list[dict]) -> list[dict]:
    return [step for step in steps if str(step.get("uses", "")).startswith("actions/checkout@")]


def guard_steps(steps: list[dict]) -> list[dict]:
    return [step for step in steps if step.get("name") == GUARD_STEP_NAME]


def iter_job_steps():
    """(path, job_id, steps) for every job of both workflow files."""
    for path in (CI_WORKFLOW, RELEASE_WORKFLOW):
        for job_id, job in workflow_jobs(load_workflow(path)).items():
            steps = job.get("steps")
            # A reusable workflow call job (`uses:` at the job level) declares
            # no steps and needs no local guard.
            if steps is None:
                steps = []
            if not isinstance(steps, list):
                raise AssertionError(f"{path.name}:{job_id} must declare a step list")
            yield path, job_id, steps


class RequireGitVersionTests(unittest.TestCase):
    """Each test drives require_git_version against one fake git binary."""

    def setUp(self) -> None:
        tempdir = tempfile.TemporaryDirectory()
        self.addCleanup(tempdir.cleanup)
        self.root = Path(tempdir.name)
        self.commands = self.root / "bin"
        self.commands.mkdir()
        self.write_git_report("git version 2.53.0\n")

    def write_git(self, body: str) -> None:
        path = self.commands / "git"
        path.write_text(f"#!/bin/sh\n{body}\n", encoding="utf-8")
        path.chmod(0o755)

    def write_git_report(self, report: str) -> None:
        self.write_git(f"printf '%s' {shlex.quote(report)}")

    def require(self):
        with mock.patch.dict(os.environ, {"PATH": str(self.commands)}):
            return installer.require_git_version()

    def require_report(self, report: str) -> str:
        self.write_git_report(report)
        return self.require()

    def require_refusal(self, report: str) -> str:
        self.write_git_report(report)
        with self.assertRaises(installer.InstallerError) as raised:
            self.require()
        return str(raised.exception)

    def test_supported_versions_pass_with_complete_identity(self) -> None:
        cases = {
            "git version 2.38": "2.38",
            "git version 2.38.0\n": "2.38.0",
            "git version 2.53.0\n": "2.53.0",
            "git version 2.43.0.windows.1\n": "2.43.0.windows.1",
            "git version 2.39.5 (Apple Git-154)\n": "2.39.5 (Apple Git-154)",
            "git version 2.46.0.123.gabcdef1.dirty\n": "2.46.0.123.gabcdef1.dirty",
            "git version 3.0\n": "3.0",
        }
        for report, identity in cases.items():
            with self.subTest(report=report):
                self.assertEqual(self.require_report(report), identity)

    def test_git_below_the_floor_refuses(self) -> None:
        for report in ("git version 2.37\n", "git version 2.37.9", "git version 2.25.1\n", "git version 1.99\n"):
            with self.subTest(report=report):
                self.assertIn("2.38.0", self.require_refusal(report))

    def test_missing_git_refuses(self) -> None:
        empty = self.root / "empty"
        empty.mkdir()
        with mock.patch.dict(os.environ, {"PATH": str(empty)}):
            with self.assertRaises(installer.InstallerError) as raised:
                installer.require_git_version()
        self.assertIn("missing command git", str(raised.exception))

    def test_failing_git_command_refuses(self) -> None:
        self.write_git("echo probe exploded >&2\nexit 3")
        with mock.patch.dict(os.environ, {"PATH": str(self.commands)}):
            with self.assertRaises(installer.InstallerError) as raised:
                installer.require_git_version()
        message = str(raised.exception)
        self.assertIn("git --version", message)
        self.assertIn("probe exploded", message)

    def test_hanging_probe_refuses_as_a_deadline(self) -> None:
        self.write_git("exec /bin/sleep 30")
        with mock.patch.object(installer, "GIT_VERSION_PROBE_TIMEOUT", 0.2):
            with mock.patch.dict(os.environ, {"PATH": str(self.commands)}):
                with self.assertRaises(installer.InstallerError) as raised:
                    installer.require_git_version()
        self.assertIn("deadline", str(raised.exception))

    def test_probe_bound_is_five_seconds(self) -> None:
        self.assertEqual(installer.GIT_VERSION_PROBE_TIMEOUT, 5)

    def test_malformed_reports_refuse_like_the_runtime_parser(self) -> None:
        for report in (
            "",
            "   \n",
            "git",
            "git version",
            "git version \n",
            "VERSION 2.38",
            "git --version 2.38",
            "git version 2\n",
            "git version abc",
            "git version (Apple Git-154)",
            "git version 2.38junk",
            "git version 2.38.1junk",
            "git version 2.53.0\nnot a version report\n",
            "git version 2.53.0\nnot a version report",
            "git version 2.53.0 \n",
            "git version 2.38 junk\n",
            "git version 2.38.",
            "git version 2.38.garbage\n",
        ):
            with self.subTest(report=report):
                self.assertIn("not a Git version report", self.require_refusal(report))

    def test_unavailable_git_exec_refuses(self) -> None:
        with mock.patch.dict(os.environ, {"PATH": str(self.commands)}):
            with mock.patch.object(installer.subprocess, "run", side_effect=OSError("permission denied")):
                with self.assertRaises(installer.InstallerError) as raised:
                    installer.require_git_version()
        self.assertIn("cannot probe the host Git version", str(raised.exception))

    def test_undecodable_git_output_refuses(self) -> None:
        self.write_git(r"printf 'git version 2.53.\377\276\n'")
        with mock.patch.dict(os.environ, {"PATH": str(self.commands)}):
            with self.assertRaises(installer.InstallerError) as raised:
                installer.require_git_version()
        self.assertIn("cannot probe the host Git version", str(raised.exception))

    def test_huge_numeric_component_refuses(self) -> None:
        self.assertIn("not a Git version report", self.require_refusal("git version 2." + "9" * 5000 + "\n"))
        self.assertIn("not a Git version report", self.require_refusal("git version " + "9" * 5000 + ".38\n"))

    def test_numeric_components_keep_the_signed_64_bit_bound(self) -> None:
        self.assertEqual(
            self.require_report("git version 2.9223372036854775807\n"),
            "2.9223372036854775807",
        )
        self.assertEqual(
            self.require_report("git version 2." + "0" * 5000 + "38\n"),
            "2." + "0" * 5000 + "38",
        )
        self.assertIn(
            "not a Git version report",
            self.require_refusal("git version 2.9223372036854775808\n"),
        )
        self.assertIn(
            "not a Git version report",
            self.require_refusal("git version 2.99999999999999999999.0\n"),
        )
        self.assertIn(
            "not a Git version report",
            self.require_refusal("git version 2.38.99999999999999999999\n"),
        )


class PreflightGitFloorTests(unittest.TestCase):
    """Preflight consults require_git_version and cannot bypass a refusal."""

    def setUp(self) -> None:
        tempdir = tempfile.TemporaryDirectory()
        self.addCleanup(tempdir.cleanup)
        self.root = Path(tempdir.name)
        self.commands = self.root / "bin"
        self.commands.mkdir()
        self.write_command("git", self.report_stub("git version 2.37\n"))
        for name in ("opencode", "secret-tool", "gnome-keyring-daemon", "busctl", "dbus-run-session", "systemctl"):
            self.write_command(name, "exit 0")
        self.paths = installer.paths_for(self.root)

    @staticmethod
    def report_stub(report: str) -> str:
        return f"printf '%s' {shlex.quote(report)}"

    def write_command(self, name: str, body: str) -> None:
        path = self.commands / name
        path.write_text(f"#!/bin/sh\n{body}\n", encoding="utf-8")
        path.chmod(0o755)

    def preflight_with_green_host(self):
        """Run preflight with every non-Git precondition satisfied."""
        with mock.patch.dict(
            os.environ, {"PATH": str(self.commands) + os.pathsep + str(self.paths.bin_dir)}
        ), mock.patch.object(
            installer, "command_status", lambda name: str(self.commands / name)
        ), mock.patch.object(
            installer, "secret_service_status", lambda: (True, "org.freedesktop.secrets is available")
        ):
            return installer.preflight(self.paths, "v1.0.0", None)

    def test_preflight_refuses_old_git_reported_by_the_real_probe(self) -> None:
        with mock.patch.dict(
            os.environ, {"PATH": str(self.commands) + os.pathsep + str(self.paths.bin_dir)}
        ), mock.patch.object(
            installer, "secret_service_status", lambda: (True, "org.freedesktop.secrets is available")
        ):
            with self.assertRaises(installer.InstallerError) as raised:
                installer.preflight(self.paths, "v1.0.0", None)
        message = str(raised.exception)
        self.assertIn("2.37", message)
        self.assertIn("2.38.0", message)

    def test_preflight_consults_the_git_guard(self) -> None:
        with mock.patch.object(installer, "require_git_version", return_value="2.53.0") as guard:
            config_plan = self.preflight_with_green_host()
        self.assertEqual(guard.call_count, 1)
        self.assertIsInstance(config_plan, installer.ConfigPlan)

    def test_preflight_cannot_bypass_a_refusing_guard(self) -> None:
        refusal = installer.InstallerError("synthetic git floor refusal")
        with mock.patch.object(installer, "require_git_version", side_effect=refusal):
            with self.assertRaises(installer.InstallerError) as raised:
                self.preflight_with_green_host()
        self.assertIn("synthetic git floor refusal", str(raised.exception))


class WorkflowGitFloorTests(unittest.TestCase):
    """Every checkout-bearing job guards Git before any Git-consuming workload."""

    def test_every_checkout_job_runs_one_canonical_guard(self) -> None:
        for path, job_id, steps in iter_job_steps():
            guards = guard_steps(steps)
            with self.subTest(workflow=path.name, job=job_id):
                if checkout_steps(steps):
                    self.assertEqual(len(guards), 1, "a checkout-bearing job must carry exactly one guard")
                    self.assertEqual(guards[0].get("run"), GUARD_RUN, "the guard invocation must be canonical")
                else:
                    self.assertEqual(guards, [], "a job without a checkout needs no guard")

    def test_guard_precedes_every_other_run_step_after_checkout(self) -> None:
        for path, job_id, steps in iter_job_steps():
            guards = guard_steps(steps)
            if not guards:
                continue
            with self.subTest(workflow=path.name, job=job_id):
                guard_index = steps.index(guards[0])
                checkout_index = steps.index(checkout_steps(steps)[0])
                self.assertLess(checkout_index, guard_index, "the guard must run after checkout")
                earlier_runs = [step.get("name") for step in steps[checkout_index:guard_index] if "run" in step]
                self.assertEqual(earlier_runs, [], f"Git-consuming workload precedes the guard: {earlier_runs}")

    def test_no_workflow_installs_a_replacement_git_package(self) -> None:
        for path, job_id, steps in iter_job_steps():
            for step in steps:
                run = step.get("run")
                if isinstance(run, str):
                    with self.subTest(workflow=path.name, job=job_id, step=step.get("name")):
                        self.assertIsNone(PACKAGE_INSTALL_RE.search(run), "no step may install a Git replacement")

    def test_the_workflows_declare_exactly_one_canonical_guard_command(self) -> None:
        declared = {step["run"] for _, _, steps in iter_job_steps() for step in guard_steps(steps)}
        self.assertEqual(declared, {GUARD_RUN})

    def test_ci_tooling_battery_runs_this_suite(self) -> None:
        for step in workflow_jobs(load_workflow(CI_WORKFLOW))["verify-tooling"]["steps"]:
            if step.get("name") == "Test release and installer tooling":
                run = step["run"]
                self.assertTrue(
                    run.endswith(" && python3 scripts/test-install-git-floor.py"),
                    f"the tooling battery must chain this suite: ...{run[-120:]}",
                )
                break
        else:
            self.fail("the tooling battery step is missing")


class GuardCommandExecutionTests(unittest.TestCase):
    """The canonical workflow guard command, driven by a fake git on PATH."""

    def setUp(self) -> None:
        declared = {step["run"] for _, _, steps in iter_job_steps() for step in guard_steps(steps)}
        self.assertTrue(declared, "no workflow declares a guard command to exercise")

    def run_guard(self, git_body: str) -> subprocess.CompletedProcess:
        tempdir = tempfile.TemporaryDirectory()
        self.addCleanup(tempdir.cleanup)
        bin_dir = Path(tempdir.name)
        git = bin_dir / "git"
        git.write_text(f"#!/bin/sh\n{git_body}\n", encoding="utf-8")
        git.chmod(0o755)
        env = dict(os.environ)
        env["PATH"] = f"{bin_dir}{os.pathsep}{env.get('PATH', '')}"
        return subprocess.run(
            GUARD_RUN, shell=True, capture_output=True, text=True, env=env, cwd=ROOT, timeout=60
        )

    def test_guard_refuses_git_2_37(self) -> None:
        result = self.run_guard(r"printf 'git version 2.37\n'")
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn("2.38.0", result.stderr)

    def test_guard_accepts_git_2_38_and_2_53(self) -> None:
        for report, expected in ((r"git version 2.38\n", "2.38"), (r"git version 2.53.0\n", "2.53.0")):
            with self.subTest(report=report):
                result = self.run_guard(f"printf '{report}'")
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stdout.strip(), expected)

    def test_guard_refuses_a_malformed_report(self) -> None:
        result = self.run_guard(r"printf 'git version banana\n'")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("not a Git version report", result.stderr)

    def test_guard_refuses_a_failing_git_command(self) -> None:
        result = self.run_guard("echo probe exploded >&2\nexit 7")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("git --version", result.stderr)


if __name__ == "__main__":
    unittest.main()

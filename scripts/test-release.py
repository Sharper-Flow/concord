#!/usr/bin/env python3
"""Unit tests for scripts/release.py using temporary Git repositories."""
from __future__ import annotations

import importlib.util
import io
import os
import subprocess
import sys
import tarfile
import tempfile
import unittest
from pathlib import Path

import git_environment

# This suite builds temporary Git repositories, so a hook that
# launched it must not keep a redirecting Git namespace in place. The scrub
# runs before the in-process Git helpers below load.
git_environment.scrub_inherited(fixture=True)

import install
import release


CHECKOUT_ACTION = "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1"
TARGET_SHA_REF = "${{ github.sha }}"
WORKFLOW_PATH = Path(__file__).parents[1] / ".github" / "workflows" / "release.yml"

# The publication path checks out the admitted push SHA with tags for version
# computation; only the publishing checkout may persist credentials. Every
# admission job (evidence lookup, admission gate) checks out the same exact
# SHA with full history and no persisted credentials.
ADMISSION_CHECKOUTS = ("verify-lookup", "admit-verification")
PUBLICATION_CHECKOUTS = {
    "prepare": {
        "ref": TARGET_SHA_REF,
        "fetch-depth": 0,
        "fetch-tags": True,
        "persist-credentials": False,
    },
    "build-and-publish": {
        "ref": TARGET_SHA_REF,
        "fetch-depth": 0,
        "fetch-tags": True,
        "persist-credentials": True,
    },
}
ADMISSION_CHECKOUT_INPUTS = {
    "ref": TARGET_SHA_REF,
    "fetch-depth": 0,
    "persist-credentials": False,
}


def load_release_evidence():
    """Import scripts/release-evidence.py, whose dashed name is not a module."""
    spec = importlib.util.spec_from_file_location(
        "release_evidence_for_release_tests", Path(__file__).with_name("release-evidence.py")
    )
    if spec is None or spec.loader is None:
        raise AssertionError("scripts/release-evidence.py cannot be loaded")
    module = importlib.util.module_from_spec(spec)
    sys.modules["release_evidence_for_release_tests"] = module
    spec.loader.exec_module(module)
    return module


class WorkflowParseError(ValueError):
    """The release workflow is not acceptable strict YAML."""


def parse_release_workflow(workflow: str) -> dict[str, object]:
    """Parse the workflow with the real YAML parser (PyYAML), strictly.

    Duplicate keys and malformed YAML are refused, so a workflow GitHub would
    misparse cannot pass the structure checks below. The strict loader lives
    in scripts/release-evidence.py; one real parser serves every consumer.
    """
    release_evidence = load_release_evidence()
    try:
        document = release_evidence.strict_yaml_load(workflow)
    except release_evidence.WorkflowYamlError as error:
        raise WorkflowParseError(str(error)) from error
    if not isinstance(document, dict):
        raise WorkflowParseError("workflow must parse to a YAML mapping")
    return document


def assert_release_workflow_structure(workflow: str) -> None:
    document = parse_release_workflow(workflow)
    jobs = document.get("jobs")
    if not isinstance(jobs, dict):
        raise AssertionError("workflow jobs mapping is missing")
    expected_conditions = {
        "prepare": "${{ !cancelled() && needs.admit-verification.result == 'success' }}",
        "build-and-publish": "${{ !cancelled() && needs.prepare.result == 'success' && needs.prepare.outputs.should_release == 'true' }}",
    }
    for job_name, expected_condition in expected_conditions.items():
        job = jobs.get(job_name)
        if not isinstance(job, dict) or job.get("if") != expected_condition:
            raise AssertionError(f"{job_name} condition must be {expected_condition!r}")
    checkout_steps: list[tuple[str, dict[str, object]]] = []
    for job_name, job in jobs.items():
        if not isinstance(job, dict):
            continue
        steps = job.get("steps")
        if not isinstance(steps, list):
            continue
        checkout_steps.extend(
            (job_name, step)
            for step in steps
            if isinstance(step, dict) and step.get("uses") == CHECKOUT_ACTION
        )
    expected_checkouts = PUBLICATION_CHECKOUTS | {
        name: ADMISSION_CHECKOUT_INPUTS for name in ADMISSION_CHECKOUTS
    }
    expected_total = len(expected_checkouts)
    if len(checkout_steps) != expected_total:
        raise AssertionError(
            f"expected exactly {expected_total} pinned checkout steps, found {len(checkout_steps)}"
        )
    for job_name, expected_inputs in expected_checkouts.items():
        matches = [step for name, step in checkout_steps if name == job_name]
        if len(matches) != 1:
            raise AssertionError(f"expected one pinned checkout step in {job_name}")
        with_mapping = matches[0].get("with")
        if not isinstance(with_mapping, dict):
            raise AssertionError(f"checkout step in {job_name} has no with mapping")
        if with_mapping.keys() != expected_inputs.keys() or any(
            type(with_mapping[key]) is not type(value) or with_mapping[key] != value
            for key, value in expected_inputs.items()
        ):
            raise AssertionError(
                f"{job_name} checkout inputs {with_mapping!r} are not exactly {expected_inputs!r}"
            )


def replace_once(workflow: str, old: str, new: str) -> str:
    if old not in workflow:
        raise AssertionError(f"test mutation target is absent: {old!r}")
    return workflow.replace(old, new, 1)


def repository_snapshot(root: Path) -> dict[str, bytes]:
    """Byte inventory of everything a redirected Git child could mutate."""
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


class ReleaseTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.repo = Path(self.tempdir.name)
        self.git("init", "--quiet")
        self.git("config", "user.email", "test@example.com")
        self.git("config", "user.name", "Release Test")
        self.commit("chore: constitutional snapshot")
        self.git("tag", "constitutional-bootstrap")

    def tearDown(self) -> None:
        self.tempdir.cleanup()

    def git(self, *args: str) -> str:
        return subprocess.check_output(["git", "-C", str(self.repo), *args], text=True)

    def run_release(self, repo: Path) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [sys.executable, str(Path(__file__).with_name("release.py")), "--repo", str(repo)],
            text=True,
            capture_output=True,
            timeout=30,
        )

    def clone(self, *, depth: int | None = None, no_tags: bool = False) -> Path:
        destination = self.repo / "clone"
        command = ["git", "clone", "--quiet"]
        if depth is not None:
            command.extend(["--depth", str(depth)])
        if no_tags:
            command.append("--no-tags")
        command.extend([f"file://{self.repo}", str(destination)])
        subprocess.check_call(command)
        return destination

    def commit(self, subject: str, body: str = "") -> None:
        path = self.repo / "history.txt"
        path.write_text(path.read_text() + subject + "\n" if path.exists() else subject + "\n")
        self.git("add", "history.txt")
        command = ["git", "-C", str(self.repo), "commit", "--quiet", "-m", subject]
        if body:
            command.extend(["-m", body])
        subprocess.check_call(command)

    def test_breaking_change_wins_with_major_bump(self) -> None:
        self.commit("fix: patch first")
        self.commit("feat!: incompatible API")
        result = release.compute(self.repo)
        self.assertEqual(result["bump"], "major")
        self.assertEqual(result["version"], "v1.0.0")
        self.assertIn("## Breaking Changes", result["changelog"])

    def test_breaking_footer_is_major(self) -> None:
        self.commit("feat: new API", "BREAKING CHANGE: old API removed")
        self.assertEqual(release.compute(self.repo)["bump"], "major")

    def test_malformed_header_with_breaking_footer_is_not_major(self) -> None:
        self.commit("not conventional", "BREAKING CHANGE: this must be ignored")
        result = release.compute(self.repo)
        self.assertFalse(result["release"])
        self.assertIsNone(result["bump"])

    def test_feat_is_minor(self) -> None:
        self.commit("feat(cli): add status")
        result = release.compute(self.repo)
        self.assertEqual(result["version"], "v0.1.0")
        self.assertIn("**cli:** feat(cli): add status", result["changelog"])

    def test_first_real_release_is_v0_1_0(self) -> None:
        self.commit("feat: first released feature")
        result = release.compute(self.repo)
        self.assertEqual(result["base_tag"], None)
        self.assertEqual(result["commit_boundary"], "constitutional-bootstrap")
        self.assertEqual(result["version"], "v0.1.0")

    def test_fix_is_patch(self) -> None:
        self.commit("fix: repair output")
        result = release.compute(self.repo)
        self.assertEqual(result["bump"], "patch")
        self.assertEqual(result["version"], "v0.0.1")

    def test_other_and_nonconventional_commits_are_noop(self) -> None:
        self.commit("docs: explain release process")
        self.commit("update spelling")
        result = release.compute(self.repo)
        self.assertFalse(result["release"])
        self.assertIsNone(result["version"])

    def test_first_release_ignores_constitutional_bootstrap_tag(self) -> None:
        self.commit("fix: first released fix")
        result = release.compute(self.repo)
        self.assertIsNone(result["base_tag"])
        self.assertEqual(result["base_version"], "v0.0.0")
        self.assertEqual(result["version"], "v0.0.1")

    def test_shallow_tagless_clone_fails_closed_without_release_authority(self) -> None:
        self.commit("feat: first released feature")
        shallow = self.clone(depth=1, no_tags=True)
        self.assertEqual(self.git("rev-parse", "--is-shallow-repository").strip(), "false")
        self.assertEqual(
            subprocess.check_output(
                ["git", "-C", str(shallow), "rev-parse", "--is-shallow-repository"],
                text=True,
            ).strip(),
            "true",
        )
        self.assertEqual(subprocess.check_output(["git", "-C", str(shallow), "tag"], text=True), "")

        result = self.run_release(shallow)

        self.assertNotEqual(result.returncode, 0)
        self.assertIn(
            "release error: no semver release tag found and constitutional-bootstrap is missing or unreachable",
            result.stderr,
        )
        self.assertNotIn("should_release=false", result.stdout)

    def test_unreachable_constitutional_boundary_fails_closed(self) -> None:
        primary = self.git("branch", "--show-current").strip()
        self.git("tag", "--delete", "constitutional-bootstrap")
        self.git("checkout", "--quiet", "--orphan", "unrelated-bootstrap-history")
        self.git("rm", "--quiet", "--ignore-unmatch", "-rf", ".")
        self.commit("chore: unrelated bootstrap")
        self.git("tag", "constitutional-bootstrap")
        self.git("checkout", "--quiet", primary)
        self.commit("feat: release with unreachable authority")

        with self.assertRaisesRegex(
            release.ReleaseError,
            "no semver release tag found and constitutional-bootstrap is missing or unreachable",
        ):
            release.compute(self.repo)

    def test_missing_constitutional_boundary_fails_closed(self) -> None:
        self.git("tag", "--delete", "constitutional-bootstrap")
        self.commit("feat: release without authority")

        with self.assertRaisesRegex(
            release.ReleaseError,
            "no semver release tag found and constitutional-bootstrap is missing or unreachable",
        ):
            release.compute(self.repo)

    def test_unreachable_semver_tag_is_ignored(self) -> None:
        primary = self.git("branch", "--show-current").strip()
        self.git("checkout", "--quiet", "--orphan", "unrelated-release-history")
        self.git("rm", "--quiet", "--ignore-unmatch", "-rf", ".")
        self.commit("feat: unrelated release")
        self.git("tag", "v9.9.9")
        self.git("checkout", "--quiet", primary)
        self.commit("fix: actual release")

        result = release.compute(self.repo)

        self.assertIsNone(result["base_tag"])
        self.assertEqual(result["commit_boundary"], "constitutional-bootstrap")
        self.assertEqual(result["version"], "v0.0.1")

    def test_malformed_semver_tag_is_ignored(self) -> None:
        self.commit("feat: actual release")
        self.git("tag", "v1.2")

        result = release.compute(self.repo)

        self.assertIsNone(result["base_tag"])
        self.assertEqual(result["version"], "v0.1.0")

    def test_release_workflow_checkouts_have_mapping_aware_inputs(self) -> None:
        assert_release_workflow_structure(WORKFLOW_PATH.read_text(encoding="utf-8"))

    def test_release_workflow_binds_packaging_to_the_push_sha(self) -> None:
        """The build, tag, and error paths must all name the admitted push SHA."""
        workflow = WORKFLOW_PATH.read_text(encoding="utf-8")
        self.assertNotIn("workflow_run", workflow)
        self.assertIn('test "$(git rev-parse HEAD)" = "${{ github.sha }}"', workflow)
        self.assertIn('if [ "$tagged" != "${{ github.sha }}" ]; then', workflow)
        self.assertIn('git tag --annotate "$VERSION" "${{ github.sha }}"', workflow)

    def test_release_workflow_never_names_an_adapter_file_it_copies(self) -> None:
        """The packing step reads ADAPTER_FILES; it must not restate the set.

        A literal copy list is how v1.0.0 through v1.1.1 shipped without
        credentials.ts: install.py gained the requirement and the workflow's
        hardcoded cp did not follow. Only copy commands are inspected, so
        prose may still name a file while the packing step may not.
        """
        copy_lines = [
            line
            for line in WORKFLOW_PATH.read_text(encoding="utf-8").splitlines()
            if line.lstrip().startswith("cp ") or " cp " in line
        ]
        for name in install.ADAPTER_FILES:
            for line in copy_lines:
                self.assertNotIn(
                    f"adapter/opencode/{name}",
                    line,
                    msg=(
                        f"release.yml copies adapter/opencode/{name} by name. "
                        "Derive the set from install.ADAPTER_FILES instead, so "
                        "the archive cannot fall behind what the installer requires."
                    ),
                )

    def split_step_heredoc(self, run: str) -> tuple[str, str, str]:
        """Split a step's run block into its command, Python body, and trailing."""
        command, separator, remainder = run.partition(" <<'PY'\n")
        self.assertTrue(separator, "Python heredoc is missing")
        body, terminator, trailing = remainder.partition("\nPY\n")
        self.assertTrue(terminator, "heredoc terminator is missing")
        return command, body, trailing

    def run_archive_verification(self, omitted: str | None = None) -> subprocess.CompletedProcess[str]:
        document = parse_release_workflow(WORKFLOW_PATH.read_text(encoding="utf-8"))
        steps = document["jobs"]["build-and-publish"]["steps"]
        matches = [
            (index, step)
            for index, step in enumerate(steps)
            if step.get("name") == "Verify the archive carries every file the installer requires"
        ]
        self.assertEqual(len(matches), 1)
        index, step = matches[0]
        self.assertNotIn("if", step)
        self.assertFalse(step.get("continue-on-error", False))
        for name in ("Create annotated release tag", "Publish GitHub Release"):
            self.assertLess(index, next(i for i, item in enumerate(steps) if item.get("name") == name))
        command, body, trailing = self.split_step_heredoc(step["run"])
        self.assertTrue(command.splitlines()[-1].startswith('python3 - "release/upload/'))
        self.assertFalse(trailing.strip())

        root = WORKFLOW_PATH.parents[2]
        required = {"bin/concord"}
        required |= {f"adapter/opencode/{name}" for name in install.ADAPTER_FILES}
        required |= {f"instructions/{name}" for name in install.INSTRUCTION_FILES}
        required |= {f"agents/{name}" for name in install.AGENT_FILES}
        required |= {
            path.relative_to(root).as_posix()
            for path in (root / "skills").rglob("*")
            if path.is_file()
        }
        if omitted is not None:
            self.assertIn(omitted, required, "omission must remove a real fixture member")
            required.remove(omitted)
        archive = self.repo / "synthetic-release.tar.gz"
        with tarfile.open(archive, "w:gz") as bundle:
            for name in sorted(required):
                member = tarfile.TarInfo(name)
                content = b"synthetic release file\n"
                member.size = len(content)
                bundle.addfile(member, io.BytesIO(content))
        return subprocess.run(
            [sys.executable, "-", str(archive)],
            input=body,
            cwd=root,
            text=True,
            capture_output=True,
        )

    def test_release_workflow_accepts_complete_archive(self) -> None:
        result = self.run_archive_verification()
        self.assertEqual(result.returncode, 0, f"stdout:\n{result.stdout}\nstderr:\n{result.stderr}")
        self.assertIn("carries every installer-required file", result.stdout)

    def test_release_workflow_rejects_archive_missing_writing_skill_or_its_license(self) -> None:
        for omitted in (
            "skills/concord-law-writing/SKILL.md", "skills/concord-law-writing/LICENSE",
            "skills/concord-instruction-writing/SKILL.md", "skills/concord-instruction-writing/LICENSE",
        ):
            with self.subTest(omitted=omitted):
                result = self.run_archive_verification(omitted)
                self.assertNotEqual(result.returncode, 0, f"stdout:\n{result.stdout}\nstderr:\n{result.stderr}")
                self.assertIn(f"omits files the installer requires: {omitted}", result.stderr)

    def test_release_workflow_verifies_the_archive_against_installer_requirements(self) -> None:
        for omitted in (
            "bin/concord",
            f"adapter/opencode/{install.ADAPTER_FILES[0]}",
            f"instructions/{install.INSTRUCTION_FILES[0]}",
            f"agents/{install.AGENT_FILES[0]}",
        ):
            with self.subTest(omitted=omitted):
                result = self.run_archive_verification(omitted)
                self.assertNotEqual(result.returncode, 0, f"stdout:\n{result.stdout}\nstderr:\n{result.stderr}")
                self.assertIn(f"omits files the installer requires: {omitted}", result.stderr)

    def test_release_workflow_tag_step_tolerates_an_existing_tag_at_head(self) -> None:
        """A retried publish must not fail because the tag already exists."""
        workflow = WORKFLOW_PATH.read_text(encoding="utf-8")
        self.assertIn('git rev-parse --verify --quiet "refs/tags/$VERSION"', workflow)
        self.assertIn("already points at", workflow)

    def extract_python_heredoc(self, step_name: str) -> tuple[str, str, str]:
        """Return the run command, Python body, and trailing shell of a step."""
        document = parse_release_workflow(WORKFLOW_PATH.read_text(encoding="utf-8"))
        steps = document["jobs"]["build-and-publish"]["steps"]
        matches = [step for step in steps if step.get("name") == step_name]
        self.assertEqual(len(matches), 1, f"expected exactly one {step_name!r} step")
        return self.split_step_heredoc(matches[0]["run"])

    def run_inline_heredoc_step(
        self, step_name: str, arguments: list[str]
    ) -> subprocess.CompletedProcess[str]:
        """Execute a step whose heredoc feeds `python3 -` with synthetic inputs."""
        command, body, _trailing = self.extract_python_heredoc(step_name)
        self.assertTrue(
            command.splitlines()[-1].startswith("python3 - "),
            f"{step_name} must invoke its heredoc with python3 -",
        )
        return subprocess.run(
            [sys.executable, "-", *arguments],
            input=body,
            cwd=WORKFLOW_PATH.parents[2],
            text=True,
            capture_output=True,
        )

    def run_script_heredoc_step(
        self, step_name: str, arguments: list[str]
    ) -> subprocess.CompletedProcess[str]:
        """Execute a step's heredoc as the script file the step writes."""
        command, body, trailing = self.extract_python_heredoc(step_name)
        self.assertTrue(
            command.splitlines()[-1].startswith('cat > "$RUNNER_TEMP/'),
            f"{step_name} must write its heredoc to a script file",
        )
        script_name = command.splitlines()[-1].split('"')[1].rsplit("/", 1)[-1]
        self.assertIn(f'python3 "$RUNNER_TEMP/{script_name}"', trailing)
        with tempfile.TemporaryDirectory() as directory:
            script = Path(directory) / script_name
            script.write_text(body, encoding="utf-8")
            return subprocess.run(
                [sys.executable, str(script), *arguments],
                cwd=WORKFLOW_PATH.parents[2],
                text=True,
                capture_output=True,
            )

    def write_lines(self, lines: list[str]) -> Path:
        handle = tempfile.TemporaryDirectory()
        self.addCleanup(handle.cleanup)
        path = Path(handle.name) / "input.txt"
        path.write_text("".join(f"{line}\n" for line in lines), encoding="utf-8")
        return path

    def test_publish_step_passes_the_computed_make_latest(self) -> None:
        """The publish action must never fall back to the API default.

        Omitting make_latest lets GitHub force Latest onto every newly
        published release, which is how v8.0.2 held Latest over v8.1.0 on
        2026-09-09 while scripts/install.py follows /releases/latest.
        """
        document = parse_release_workflow(WORKFLOW_PATH.read_text(encoding="utf-8"))
        steps = document["jobs"]["build-and-publish"]["steps"]

        def index_of(name: str) -> int:
            matches = [i for i, step in enumerate(steps) if step.get("name") == name]
            self.assertEqual(len(matches), 1, f"expected exactly one {name!r} step")
            return matches[0]

        decide = steps[index_of("Decide whether this publish holds the Latest flag")]
        self.assertEqual(decide.get("id"), "latest")
        self.assertLess(
            index_of("Reconcile an existing release for this version"),
            index_of("Decide whether this publish holds the Latest flag"),
        )
        self.assertLess(
            index_of("Decide whether this publish holds the Latest flag"),
            index_of("Publish GitHub Release"),
        )
        publish = steps[index_of("Publish GitHub Release")]
        with_mapping = publish.get("with")
        self.assertIsInstance(with_mapping, dict)
        self.assertEqual(
            with_mapping.get("make_latest"), "${{ steps.latest.outputs.make_latest }}"
        )
        assertion = steps[index_of("Assert Latest names the highest published version")]
        self.assertIn("!cancelled()", assertion.get("if", ""))
        self.assertLess(index_of("Publish GitHub Release"), index_of(
            "Assert Latest names the highest published version"
        ))

    def test_latest_decision_step_refuses_the_incident_shape(self) -> None:
        """Publishing v8.0.2 while v8.1.0 is published must not hold Latest."""
        with tempfile.TemporaryDirectory() as directory:
            decision = Path(directory) / "make-latest.txt"
            result = self.run_inline_heredoc_step(
                "Decide whether this publish holds the Latest flag",
                ["v8.0.2", self.write_lines(["v8.0.1", "v8.1.0"]), decision],
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(decision.read_text(encoding="utf-8"), "false\n")

    def test_latest_decision_step_allows_a_new_highest_version(self) -> None:
        for version, published in (
            ("v8.1.0", ["v8.0.1", "v8.0.2"]),
            ("v8.1.0", ["v8.1.0"]),
        ):
            with tempfile.TemporaryDirectory() as directory, self.subTest(
                version=version, published=published
            ):
                decision = Path(directory) / "make-latest.txt"
                result = self.run_inline_heredoc_step(
                    "Decide whether this publish holds the Latest flag",
                    [version, self.write_lines(published), decision],
                )
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(decision.read_text(encoding="utf-8"), "true\n")

    def test_latest_assertion_step_accepts_the_highest_holder(self) -> None:
        result = self.run_script_heredoc_step(
            "Assert Latest names the highest published version",
            [self.write_lines(["v8.0.2", "v8.1.0"]), self.write_lines(["v8.1.0"])],
        )
        self.assertEqual(result.returncode, 0, f"stdout:\n{result.stdout}\nstderr:\n{result.stderr}")
        self.assertIn("Latest names the highest published version: v8.1.0", result.stdout)

    def test_latest_assertion_step_rejects_an_older_holder(self) -> None:
        result = self.run_script_heredoc_step(
            "Assert Latest names the highest published version",
            [self.write_lines(["v8.0.2", "v8.1.0"]), self.write_lines(["v8.0.2"])],
        )
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn("highest published version is v8.1.0", result.stderr)

    def test_workflow_validator_rejects_missing_input(self) -> None:
        workflow = WORKFLOW_PATH.read_text(encoding="utf-8")
        mutated = replace_once(workflow, "          fetch-depth: 0\n", "")
        with self.assertRaises((WorkflowParseError, AssertionError)):
            assert_release_workflow_structure(mutated)

    def test_workflow_validator_rejects_comment_only_input(self) -> None:
        workflow = WORKFLOW_PATH.read_text(encoding="utf-8")
        mutated = replace_once(workflow, "          fetch-tags: true\n", "          # fetch-tags: true\n")
        with self.assertRaises((WorkflowParseError, AssertionError)):
            assert_release_workflow_structure(mutated)

    def test_workflow_validator_rejects_input_hidden_in_run_block(self) -> None:
        workflow = WORKFLOW_PATH.read_text(encoding="utf-8")
        mutated = replace_once(
            workflow,
            "          fetch-depth: 0\n          fetch-tags: true\n          persist-credentials: false",
            "        run: |\n          fetch-depth: 0\n          fetch-tags: true\n          persist-credentials: false",
        )
        with self.assertRaises((WorkflowParseError, AssertionError)):
            assert_release_workflow_structure(mutated)

    def test_workflow_validator_rejects_duplicate_input(self) -> None:
        workflow = WORKFLOW_PATH.read_text(encoding="utf-8")
        mutated = replace_once(workflow, "          fetch-depth: 0\n", "          fetch-depth: 0\n          fetch-depth: 0\n")
        with self.assertRaises(WorkflowParseError):
            assert_release_workflow_structure(mutated)

    def test_workflow_validator_rejects_stringified_integer(self) -> None:
        workflow = WORKFLOW_PATH.read_text(encoding="utf-8")
        mutated = replace_once(workflow, "          fetch-depth: 0\n", '          fetch-depth: "0"\n')
        with self.assertRaises(AssertionError):
            assert_release_workflow_structure(mutated)

    def test_workflow_validator_preserves_exact_checkout_input_types(self) -> None:
        workflow = WORKFLOW_PATH.read_text(encoding="utf-8")
        for old, new in (
            ("fetch-depth: 0", "fetch-depth: false"),
            ("persist-credentials: false", "persist-credentials: 0"),
            ("fetch-tags: true", "fetch-tags: 1"),
            ("persist-credentials: true", "persist-credentials: 1"),
        ):
            with self.subTest(input=old):
                with self.assertRaises(AssertionError):
                    assert_release_workflow_structure(replace_once(workflow, old, new))

    def test_workflow_validator_rejects_wrong_checkout_action(self) -> None:
        workflow = WORKFLOW_PATH.read_text(encoding="utf-8")
        mutated = replace_once(workflow, CHECKOUT_ACTION, "actions/checkout@main")
        with self.assertRaises(AssertionError):
            assert_release_workflow_structure(mutated)

    def test_workflow_validator_rejects_misindented_input(self) -> None:
        workflow = WORKFLOW_PATH.read_text(encoding="utf-8")
        mutated = replace_once(workflow, "          fetch-depth: 0\n", "        fetch-depth: 0\n")
        with self.assertRaises((WorkflowParseError, AssertionError)):
            assert_release_workflow_structure(mutated)

    def test_workflow_validator_rejects_misnested_with_mapping(self) -> None:
        workflow = WORKFLOW_PATH.read_text(encoding="utf-8")
        mutated = replace_once(workflow, "        with:\n", "      with:\n")
        with self.assertRaises((WorkflowParseError, AssertionError)):
            assert_release_workflow_structure(mutated)

    def test_bootstrap_only_history_has_no_release(self) -> None:
        result = release.compute(self.repo)
        self.assertEqual(result["commit_boundary"], "constitutional-bootstrap")
        self.assertFalse(result["release"])
        self.assertEqual(result["commits"], [])

    def test_uses_latest_semver_tag_as_base(self) -> None:
        self.commit("fix: initial release")
        self.git("tag", "v1.2.3")
        self.commit("feat: follow-up feature")
        result = release.compute(self.repo)
        self.assertEqual(result["base_tag"], "v1.2.3")
        self.assertEqual(result["version"], "v1.3.0")

    def test_publish_holds_latest_when_version_is_highest(self) -> None:
        self.assertTrue(release.publish_holds_latest("v8.0.2", ["v8.0.1"]))
        self.assertTrue(release.publish_holds_latest("v0.0.1", []))

    def test_publish_of_an_older_version_after_a_newer_one_leaves_latest(self) -> None:
        """The 2026-09-09 shape: v8.0.2 publishing after v8.1.0."""
        self.assertFalse(release.publish_holds_latest("v8.0.2", ["v8.0.1", "v8.1.0"]))

    def test_equal_republish_of_the_highest_version_holds_latest(self) -> None:
        """A retry that re-publishes the current highest must restore Latest."""
        self.assertTrue(release.publish_holds_latest("v8.1.0", ["v8.1.0"]))

    def test_highest_release_tag_orders_by_semver_not_text(self) -> None:
        self.assertEqual(release.highest_release_tag(["v8.9.0", "v8.10.0"]), "v8.10.0")

    def test_highest_release_tag_ignores_non_semver_names(self) -> None:
        self.assertEqual(
            release.highest_release_tag(["v1.2", "nightly-2026", "v1.2.3"]), "v1.2.3"
        )
        self.assertIsNone(release.highest_release_tag(["v1.2", "nightly-2026"]))

    def annotate(self, tag: str) -> None:
        self.git("tag", "--annotate", tag, "--message", f"Release {tag}")

    def test_rerun_with_release_tag_at_head_reenters_the_release(self) -> None:
        """A retried publish re-emits the tagged release instead of refusing.

        Once the annotated release tag is pushed, a rerun finds no commits
        since it. The computation must re-emit that tag as the release, with
        its changelog against the boundary it was first computed from, so the
        asset upload can converge.
        """
        self.commit("fix: first released fix")
        self.annotate("v0.0.1")

        result = release.compute(self.repo)

        self.assertTrue(result["release"])
        self.assertEqual(result["version"], "v0.0.1")
        self.assertEqual(result["tag"], "v0.0.1")
        self.assertIsNone(result["base_tag"])
        self.assertEqual(result["commit_boundary"], "constitutional-bootstrap")
        self.assertIn("fix: first released fix", result["changelog"])

    def test_rerun_between_releases_uses_the_previous_release_boundary(self) -> None:
        self.commit("fix: first released fix")
        self.annotate("v0.0.1")
        self.commit("feat: follow-up feature")
        self.annotate("v0.1.0")

        result = release.compute(self.repo)

        self.assertTrue(result["release"])
        self.assertEqual(result["version"], "v0.1.0")
        self.assertEqual(result["base_tag"], "v0.0.1")
        self.assertIsNone(result["bump"])
        self.assertIn("feat: follow-up feature", result["changelog"])
        self.assertNotIn("fix: first released fix", result["changelog"])

    def test_releasable_commits_after_the_tag_still_bump_normally(self) -> None:
        self.commit("fix: first released fix")
        self.annotate("v0.0.1")
        self.commit("fix: follow-up fix")

        result = release.compute(self.repo)

        self.assertTrue(result["release"])
        self.assertEqual(result["version"], "v0.0.2")
        self.assertEqual(result["bump"], "patch")

    def test_nonconventional_commits_after_the_tag_have_no_release(self) -> None:
        self.commit("fix: first released fix")
        self.annotate("v0.0.1")
        self.commit("docs: explain the release")

        result = release.compute(self.repo)

        self.assertFalse(result["release"])
        self.assertIsNone(result["version"])

    def test_hook_inherited_git_dir_cannot_reach_an_outer_repository(self) -> None:
        """This real suite under a hook's inherited GIT_DIR.

        The suite runs again as a child process with GIT_DIR pointing at a
        scratch outer repository. Without startup sanitization every scratch
        repository this suite builds is redirected into the outer one while
        the suite still reports success; with it, the outer repository stays
        byte-for-byte untouched and the child passes.
        """
        with tempfile.TemporaryDirectory(prefix="con-896-outer-") as outer_dir:
            outer = Path(outer_dir) / "outer-repository"
            outer.mkdir()

            def outer_git(*args: str) -> None:
                subprocess.run(
                    ["git", "-C", str(outer), *args],
                    check=True,
                    capture_output=True,
                    text=True,
                    env=git_environment.sanitized_environment(fixture=True),
                )

            outer_git("init", "--quiet")
            outer_git("config", "user.name", "Outer Keeper")
            outer_git("config", "user.email", "outer-keeper@example.invalid")
            (outer / "outer-file.txt").write_text("outer worktree content\n", encoding="utf-8")
            outer_git("add", "outer-file.txt")
            outer_git("commit", "--quiet", "--message", "outer baseline")
            outer_git("tag", "outer-tag")
            before = repository_snapshot(outer)
            env = git_environment.sanitized_environment(fixture=True)
            env["GIT_DIR"] = str(outer / ".git")
            child = subprocess.run(
                [sys.executable, str(Path(__file__)), "ReleaseTests.test_fix_is_patch"],
                env=env,
                text=True,
                capture_output=True,
                check=False,
            )
            after = repository_snapshot(outer)
        changed = sorted(set(before) ^ set(after)) + sorted(
            key for key in before if before[key] != after.get(key)
        )
        # The snapshot comparison runs before the exit-code check so a broken
        # child still reports the writes it managed.
        self.assertEqual(
            before,
            after,
            "the inherited GIT_DIR redirected this suite's scratch repositories "
            f"into the outer repository ({len(changed)} paths changed: {', '.join(changed[:10])})",
        )
        self.assertEqual(child.returncode, 0, child.stderr)


if __name__ == "__main__":
    unittest.main()

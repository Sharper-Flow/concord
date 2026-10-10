#!/usr/bin/env python3
"""Unit tests for scripts/release.py using temporary Git repositories."""
from __future__ import annotations

import importlib.util
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import git_environment

# This suite builds temporary Git repositories, so a hook that
# launched it must not keep a redirecting Git namespace in place. The scrub
# runs before the in-process Git helpers below load.
git_environment.scrub_inherited()

import install
import release


CHECKOUT_ACTION = "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1"
TARGET_SHA_REF = "${{ github.sha }}"
WORKFLOW_PATH = Path(__file__).parents[1] / ".github" / "workflows" / "release.yml"

# The publication path checks out the admitted push SHA with tags for version
# computation; only the publishing checkout may persist credentials. Every
# admission job (evidence lookup, refresh checks, admission gate) checks out
# the same exact SHA with full history and no persisted credentials.
ADMISSION_CHECKOUTS = ("verify-lookup", "refresh-main-checks", "admit-verification")
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

    def test_release_workflow_verifies_the_archive_against_installer_requirements(self) -> None:
        workflow = WORKFLOW_PATH.read_text(encoding="utf-8")
        self.assertIn("install.ADAPTER_FILES", workflow)
        self.assertIn("omits files the installer requires", workflow)

    def test_release_workflow_tag_step_tolerates_an_existing_tag_at_head(self) -> None:
        """A retried publish must not fail because the tag already exists."""
        workflow = WORKFLOW_PATH.read_text(encoding="utf-8")
        self.assertIn('git rev-parse --verify --quiet "refs/tags/$VERSION"', workflow)
        self.assertIn("already points at", workflow)

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
                    env=git_environment.sanitized_environment(),
                )

            outer_git("init", "--quiet")
            outer_git("config", "user.name", "Outer Keeper")
            outer_git("config", "user.email", "outer-keeper@example.invalid")
            (outer / "outer-file.txt").write_text("outer worktree content\n", encoding="utf-8")
            outer_git("add", "outer-file.txt")
            outer_git("commit", "--quiet", "--message", "outer baseline")
            outer_git("tag", "outer-tag")
            before = repository_snapshot(outer)
            env = git_environment.sanitized_environment()
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

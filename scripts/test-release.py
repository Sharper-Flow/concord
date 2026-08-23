#!/usr/bin/env python3
"""Unit tests for scripts/release.py using temporary Git repositories."""
from __future__ import annotations

import dataclasses
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import install
import release


CHECKOUT_ACTION = "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1"
HEAD_SHA_REF = "${{ github.event.workflow_run.head_sha }}"
WORKFLOW_PATH = Path(__file__).parents[1] / ".github" / "workflows" / "release.yml"


class WorkflowParseError(ValueError):
    """The release workflow is outside the parser's supported YAML subset."""


@dataclasses.dataclass(frozen=True)
class WorkflowLine:
    indent: int
    text: str
    number: int


def _strip_yaml_comment(text: str) -> str:
    quote: str | None = None
    escaped = False
    for index, character in enumerate(text):
        if quote == '"':
            if escaped:
                escaped = False
            elif character == "\\":
                escaped = True
            elif character == '"':
                quote = None
            continue
        if quote == "'":
            if character == "'":
                if index + 1 < len(text) and text[index + 1] == "'":
                    continue
                quote = None
            continue
        if character in ('"', "'"):
            quote = character
        elif character == "#" and (index == 0 or text[index - 1].isspace()):
            return text[:index].rstrip()
    return text.rstrip()


def _mapping_entry(text: str, number: int) -> tuple[str, str]:
    quote: str | None = None
    for index, character in enumerate(text):
        if quote:
            if character == quote:
                quote = None
            continue
        if character in ('"', "'"):
            quote = character
        elif character == ":":
            key = text[:index].strip()
            if not key:
                break
            return key.strip('"\''), text[index + 1 :].strip()
    raise WorkflowParseError(f"line {number}: expected a mapping entry")


def _scalar(value: str, number: int) -> object:
    if value.startswith(("|", ">")):
        return ""
    if value in ("true", "false"):
        return value == "true"
    if value and value.lstrip("-").isdigit():
        return int(value)
    if len(value) >= 2 and value[0] == value[-1] and value[0] in ('"', "'"):
        return value[1:-1]
    return value


def _tokens(workflow: str) -> list[WorkflowLine]:
    result: list[WorkflowLine] = []
    block_scalar_indent: int | None = None
    for number, physical_line in enumerate(workflow.splitlines(), 1):
        if physical_line.lstrip(" ").startswith("\t") or "\t" in physical_line[: len(physical_line) - len(physical_line.lstrip(" "))]:
            raise WorkflowParseError(f"line {number}: tabs are not valid indentation")
        indent = len(physical_line) - len(physical_line.lstrip(" "))
        if block_scalar_indent is not None:
            if not physical_line.strip() or indent > block_scalar_indent:
                continue
            block_scalar_indent = None
        text = _strip_yaml_comment(physical_line[indent:])
        if not text:
            continue
        result.append(WorkflowLine(indent, text, number))
        if not text.startswith("-"):
            try:
                _, value = _mapping_entry(text, number)
            except WorkflowParseError:
                continue
            if value.startswith(("|", ">")):
                block_scalar_indent = indent
    return result


def _add_mapping_value(mapping: dict[str, object], key: str, value: object, number: int) -> None:
    if key in mapping:
        raise WorkflowParseError(f"line {number}: duplicate key: {key}")
    mapping[key] = value


def _parse_block(lines: list[WorkflowLine], index: int, indent: int) -> tuple[object, int]:
    if index >= len(lines) or lines[index].indent != indent:
        raise WorkflowParseError(f"line {lines[index].number if index < len(lines) else 'end'}: invalid indentation")
    if lines[index].text.startswith("-"):
        return _parse_sequence(lines, index, indent)
    return _parse_mapping(lines, index, indent)


def _parse_mapping(lines: list[WorkflowLine], index: int, indent: int) -> tuple[dict[str, object], int]:
    mapping: dict[str, object] = {}
    while index < len(lines) and lines[index].indent == indent:
        line = lines[index]
        if line.text.startswith("-"):
            raise WorkflowParseError(f"line {line.number}: sequence item in mapping")
        key, raw_value = _mapping_entry(line.text, line.number)
        index += 1
        if not raw_value:
            if index < len(lines) and lines[index].indent > indent:
                value, index = _parse_block(lines, index, lines[index].indent)
            else:
                value = {}
        else:
            value = _scalar(raw_value, line.number)
            if index < len(lines) and lines[index].indent > indent:
                raise WorkflowParseError(f"line {lines[index].number}: unexpected indentation")
        _add_mapping_value(mapping, key, value, line.number)
    return mapping, index


def _parse_sequence(lines: list[WorkflowLine], index: int, indent: int) -> tuple[list[object], int]:
    sequence: list[object] = []
    while index < len(lines) and lines[index].indent == indent:
        line = lines[index]
        if not line.text.startswith("-"):
            raise WorkflowParseError(f"line {line.number}: expected a sequence item")
        item = line.text[1:].strip()
        index += 1
        if not item:
            if index < len(lines) and lines[index].indent > indent:
                value, index = _parse_block(lines, index, lines[index].indent)
            else:
                value = None
        elif ":" in item:
            key, raw_value = _mapping_entry(item, line.number)
            mapping: dict[str, object] = {}
            if raw_value:
                value = _scalar(raw_value, line.number)
            elif index < len(lines) and lines[index].indent > indent:
                value, index = _parse_block(lines, index, lines[index].indent)
            else:
                value = {}
            _add_mapping_value(mapping, key, value, line.number)
            if index < len(lines) and lines[index].indent > indent:
                child, index = _parse_mapping(lines, index, lines[index].indent)
                for child_key, child_value in child.items():
                    _add_mapping_value(mapping, child_key, child_value, lines[index - 1].number)
            value = mapping
        else:
            value = _scalar(item, line.number)
            if index < len(lines) and lines[index].indent > indent:
                raise WorkflowParseError(f"line {lines[index].number}: unexpected indentation")
        sequence.append(value)
    return sequence, index


def parse_release_workflow(workflow: str) -> dict[str, object]:
    """Parse the workflow's supported YAML subset with structural safeguards.

    This is intentionally not a general YAML parser: anchors, aliases, flow
    collections, tags, and advanced scalar rules are unsupported. It does
    support the block mappings/sequences and scalars used by release.yml.
    """
    lines = _tokens(workflow)
    if not lines or lines[0].indent != 0:
        raise WorkflowParseError("workflow must start with a top-level mapping")
    document, index = _parse_mapping(lines, 0, 0)
    if index != len(lines):
        raise WorkflowParseError(f"line {lines[index].number}: unparsed workflow content")
    return document


def assert_release_workflow_structure(workflow: str) -> None:
    document = parse_release_workflow(workflow)
    jobs = document.get("jobs")
    if not isinstance(jobs, dict):
        raise AssertionError("workflow jobs mapping is missing")
    expected_persist = {"prepare": False, "build-and-publish": True}
    checkout_steps: list[tuple[str, dict[str, object]]] = []
    for job_name, job in jobs.items():
        if not isinstance(job, dict):
            continue
        steps = job.get("steps")
        if not isinstance(steps, list):
            continue
        for step in steps:
            if isinstance(step, dict) and step.get("uses") == CHECKOUT_ACTION:
                checkout_steps.append((job_name, step))
    if len(checkout_steps) != 2:
        raise AssertionError(f"expected exactly two pinned checkout steps, found {len(checkout_steps)}")
    for job_name, expected_credentials in expected_persist.items():
        matches = [step for name, step in checkout_steps if name == job_name]
        if len(matches) != 1:
            raise AssertionError(f"expected one pinned checkout step in {job_name}")
        with_mapping = matches[0].get("with")
        if not isinstance(with_mapping, dict):
            raise AssertionError(f"checkout step in {job_name} has no with mapping")
        expected = {
            "ref": HEAD_SHA_REF,
            "fetch-depth": 0,
            "fetch-tags": True,
            "persist-credentials": expected_credentials,
        }
        for key, expected_value in expected.items():
            actual = with_mapping.get(key)
            if type(actual) is not type(expected_value) or actual != expected_value:
                raise AssertionError(f"{job_name} checkout input {key} is incorrect")


def replace_once(workflow: str, old: str, new: str) -> str:
    if old not in workflow:
        raise AssertionError(f"test mutation target is absent: {old!r}")
    return workflow.replace(old, new, 1)


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


if __name__ == "__main__":
    unittest.main()

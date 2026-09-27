#!/usr/bin/env python3
"""Tests for scripts/check-primary-prompts.py.

Positive scenarios run the checker against the real repository examples.
Negative scenarios copy the examples into a temporary tree, break one invariant
on purpose, and prove the checker reports it: a host tool name in frontmatter, a
missing advisory handoff, duplicated conduct text, a diverged shared section,
a lost intake boundary, a missing cross-repository boundary phrase, and a
forbidden move-into-another-repository instruction.
"""
from __future__ import annotations

import importlib.util
import re
import shutil
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]

spec = importlib.util.spec_from_file_location("check_primary_prompts", ROOT / "scripts" / "check-primary-prompts.py")
check_primary_prompts = importlib.util.module_from_spec(spec)
spec.loader.exec_module(check_primary_prompts)


class PrimaryPromptCheckTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.root = Path(self.tempdir.name)
        prompts = self.root / check_primary_prompts.PROMPT_DIR
        prompts.mkdir(parents=True)
        instructions = self.root / "instructions"
        instructions.mkdir()
        shutil.copy2(ROOT / "instructions" / "evidence.md", instructions / "evidence.md")
        for name in check_primary_prompts.PRIMARY_PROMPT_FILES:
            shutil.copy2(ROOT / check_primary_prompts.PROMPT_DIR / name, prompts / name)

    def tearDown(self) -> None:
        self.tempdir.cleanup()

    def findings(self) -> list[str]:
        return check_primary_prompts.check_primary_prompts(self.root)

    def rewrite(self, name: str, transform) -> None:
        path = self.root / check_primary_prompts.PROMPT_DIR / name
        path.write_text(transform(path.read_text(encoding="utf-8")), encoding="utf-8")

    def test_repository_examples_pass(self) -> None:
        self.assertEqual(check_primary_prompts.check_primary_prompts(ROOT), [])

    def test_examples_are_outside_discovery_and_release_paths(self) -> None:
        self.assertEqual(check_primary_prompts.PROMPT_DIR.parts, ("examples", "opencode", "agents"))
        installer = (ROOT / "scripts" / "install.py").read_text(encoding="utf-8")
        release = (ROOT / ".github" / "workflows" / "release.yml").read_text(encoding="utf-8")
        self.assertNotIn("examples/opencode/agents", release)
        self.assertNotIn("prompts/concord-", release)
        for name in check_primary_prompts.PRIMARY_PROMPT_FILES:
            self.assertFalse((ROOT / ".opencode" / "agents" / name).exists())
            self.assertNotIn(name, installer)

    def test_missing_prompt_reports(self) -> None:
        (self.root / check_primary_prompts.PROMPT_DIR / "concord-2.md").unlink()
        findings = self.findings()
        self.assertTrue(any("missing primary agent example" in finding for finding in findings))

    def test_host_tool_name_in_frontmatter_reports(self) -> None:
        self.rewrite("concord-1.md", lambda text: text.replace("  webfetch: allow\n", "  webfetch: allow\n  github_*: allow\n"))
        findings = self.findings()
        self.assertTrue(any("github_'" in finding and "frontmatter" in finding for finding in findings))

    def test_host_mutation_tool_permission_in_coordinator_frontmatter_reports(self) -> None:
        self.rewrite("concord-1.md", lambda text: text.replace("  edit: allow\n", "  edit: allow\n  morph_edit: allow\n"))
        findings = self.findings()
        self.assertTrue(any("morph_edit" in finding and "frontmatter" in finding for finding in findings))

    def test_unclosed_frontmatter_reports_without_traceback(self) -> None:
        self.rewrite("concord-0.md", lambda text: text.replace("\n---\n", "\n", 1))
        findings = self.findings()
        self.assertTrue(any("frontmatter is not closed" in finding for finding in findings))

    def test_missing_advisory_handoff_reports(self) -> None:
        def strip_handoff(text: str) -> str:
            start = text.index("Advisory handoff.")
            end = text.index("\n\n", start)
            return text[:start] + text[end + 2:]
        self.rewrite("concord-1.md", strip_handoff)
        findings = self.findings()
        self.assertTrue(any("concord-1.md" in finding and "Advisory handoff" in finding for finding in findings))

    def test_advisory_handoff_without_decline_rule_reports(self) -> None:
        self.rewrite(
            "concord-2.md",
            lambda text: text.replace(
                "When the operator declines, do not repeat the unchanged\nrecommendation, and do not pause work the contract still permits.",
                "",
            ),
        )
        findings = self.findings()
        self.assertTrue(any("do not repeat" in finding for finding in findings))

    def test_duplicated_lookup_obligation_reports(self) -> None:
        self.rewrite("concord-0.md", lambda text: text.replace("## Dispatch boundary", "Recall is not evidence.\n\n## Dispatch boundary"))
        findings = self.findings()
        self.assertTrue(any("duplicates the technical-unknown" in finding for finding in findings))

    def test_permission_drift_reports(self) -> None:
        self.rewrite("concord-2.md", lambda text: text.replace('"sudo *": "deny"', '"sudo *": "allow"'))
        findings = self.findings()
        self.assertTrue(any("different permission frontmatter" in finding for finding in findings))

    def test_shared_section_drift_reports(self) -> None:
        self.rewrite(
            "concord-2.md",
            lambda text: text.replace("You decide, then you record.", "You decide, then you record it."),
        )
        findings = self.findings()
        self.assertTrue(any("differ outside the posture" in finding for finding in findings))

    def test_intake_boundary_loss_reports(self) -> None:
        self.rewrite("concord-0.md", lambda text: text.replace("  write: deny\n", ""))
        findings = self.findings()
        self.assertTrue(any("intake boundary" in finding for finding in findings))

    def test_coordinator_task_boundary_loss_reports(self) -> None:
        self.rewrite(
            "concord-1.md",
            lambda text: text.replace('task:\n    "*": deny\n    "concord-*": allow', 'task:\n    "*": deny'),
        )
        findings = self.findings()
        self.assertTrue(any("task boundary" in finding for finding in findings))

    def test_boundary_phrases_pass_on_real_examples(self) -> None:
        self.assertEqual(self.findings(), [])

    def test_missing_boundary_statement_reports(self) -> None:
        def drop_boundary(text: str) -> str:
            rewritten, count = re.subn(
                r"never claim or move across the\s+boundary",
                "follow the work into the other repository",
                text,
            )
            self.assertEqual(count, 1, "fixture no longer carries the boundary statement")
            return rewritten

        self.rewrite("concord-1.md", drop_boundary)
        findings = self.findings()
        self.assertTrue(any(
            "concord-1.md" in finding
            and "cross-repository boundary phrase 'never claim or move across the boundary'" in finding
            for finding in findings
        ))

    def test_forbidden_move_instruction_reports(self) -> None:
        self.rewrite(
            "concord-2.md",
            lambda text: text
            + "\nWhen the work reaches a second repository, claim a worktree in "
            "the other repository from this session.\n",
        )
        findings = self.findings()
        self.assertTrue(any(
            "concord-2.md" in finding
            and "move into another repository" in finding
            and "claim a worktree in the other repository" in finding
            for finding in findings
        ))

    def test_forbidden_move_instruction_reports_in_intake_example(self) -> None:
        self.rewrite(
            "concord-0.md",
            lambda text: text + "\nFor a second repository, move the session into the other repository.\n",
        )
        findings = self.findings()
        self.assertTrue(any(
            "concord-0.md" in finding and "move into another repository" in finding
            for finding in findings
        ))


if __name__ == "__main__":
    raise SystemExit(unittest.main())

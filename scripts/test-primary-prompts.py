#!/usr/bin/env python3
"""Tests for scripts/check-primary-prompts.py.

Positive scenarios run the checker against the real repository examples.
Negative scenarios copy the examples into a temporary tree, break one invariant
on purpose, and prove the checker reports it: a host tool name in frontmatter, a
missing advisory handoff, duplicated conduct text, a diverged shared section,
a lost intake boundary, a missing cross-repository boundary phrase, and a
forbidden move-into-another-repository instruction. The named profiles prove
the CON-1163 contract: the Concord-RTE profile carries Build-level grants with
no workflow denial and the required role sentences, and the retired
concord-coordinator profile is rejected on sight.
"""
from __future__ import annotations

import importlib.util
import re
import shutil
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]

spec = importlib.util.spec_from_file_location("check_primary_prompts", ROOT / "scripts" / "check-primary-prompts.py")
check_primary_prompts = importlib.util.module_from_spec(spec)
spec.loader.exec_module(check_primary_prompts)

# CON-1163: the sentences the RTE role body must state, matched case exact.
# The apostrophe in the first phrase is ASCII: the validator matches bytes,
# not typography.
RTE_REQUIRED_PHRASES = (
    "never decides inside another session's item",
    "Never answer a Concord approval challenge",
    "goes to the operator unchanged",
    "Each session owns its design, code, tests, review, law text, publication",
    "Keep Linear current",
    "Report ship status to the operator",
)
RTE_BUILD_TOOLS = ("edit", "write", "patch", "morph_edit")


class PrimaryPromptCheckTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.root = Path(self.tempdir.name)
        prompts = self.root / check_primary_prompts.PROMPT_DIR
        prompts.mkdir(parents=True)
        instructions = self.root / ".concord/instructions"
        instructions.mkdir(parents=True)
        shutil.copy2(ROOT / ".concord/instructions" / "evidence.md", instructions / "evidence.md")
        for name in check_primary_prompts.PRIMARY_PROMPT_FILES:
            shutil.copy2(ROOT / check_primary_prompts.PROMPT_DIR / name, prompts / name)
        profiles = self.root / check_primary_prompts.PROFILE_DIR
        profiles.mkdir(parents=True)
        for name in check_primary_prompts.NAMED_PROFILE_FILES:
            shutil.copy2(ROOT / check_primary_prompts.PROFILE_DIR / name, profiles / name)

    def tearDown(self) -> None:
        self.tempdir.cleanup()

    def findings(self) -> list[str]:
        return check_primary_prompts.check_primary_prompts(self.root)

    def rewrite(self, name: str, transform) -> None:
        directory = check_primary_prompts.PROFILE_DIR if name in check_primary_prompts.NAMED_PROFILE_FILES else check_primary_prompts.PROMPT_DIR
        path = self.root / directory / name
        before = path.read_text(encoding="utf-8")
        after = transform(before)
        self.assertNotEqual(before, after, f"fixture transform left {name} unchanged")
        path.write_text(after, encoding="utf-8")

    def test_named_profiles_ship_as_managed_agents(self) -> None:
        installer = (ROOT / "scripts" / "install.py").read_text(encoding="utf-8")
        for name in check_primary_prompts.NAMED_PROFILE_FILES:
            self.assertIn(f'"{name}"', installer)

    def test_missing_named_profile_reports(self) -> None:
        (self.root / check_primary_prompts.PROFILE_DIR / "Concord-RTE.md").unlink()
        findings = self.findings()
        self.assertTrue(any("missing named primary profile" in finding for finding in findings))

    def test_rte_lost_build_permission_grants_report(self) -> None:
        def strip_grants(text: str) -> str:
            for tool in RTE_BUILD_TOOLS:
                line = f"  {tool}: allow\n"
                self.assertIn(line, text, line)
                text = text.replace(line, "")
            return text

        self.rewrite("Concord-RTE.md", strip_grants)
        findings = self.findings()
        for tool in RTE_BUILD_TOOLS:
            self.assertTrue(
                any("Concord-RTE.md" in finding and tool in finding for finding in findings),
                tool,
            )

    def test_rte_workflow_denial_reports(self) -> None:
        # The RTE holds no profile ceiling: a workflow denial in its
        # frontmatter would strand the job the role exists for.
        self.rewrite(
            "Concord-RTE.md",
            lambda text: text.replace("  morph_edit: allow\n", "  morph_edit: allow\n  concord_work_start: deny\n"),
        )
        findings = self.findings()
        self.assertTrue(any("Concord-RTE.md" in finding and "concord_work_start" in finding for finding in findings))

    def test_rte_lost_required_role_sentence_reports(self) -> None:
        def strip_sentences(text: str) -> str:
            for phrase in RTE_REQUIRED_PHRASES:
                self.assertIn(phrase, text, phrase)
                text = text.replace(phrase, "")
            return text

        self.rewrite("Concord-RTE.md", strip_sentences)
        findings = self.findings()
        for phrase in RTE_REQUIRED_PHRASES:
            self.assertTrue(
                any("Concord-RTE.md" in finding and phrase in finding for finding in findings),
                phrase,
            )

    def test_retired_coordinator_profile_reports(self) -> None:
        # CON-1163 zero tolerance: the rename deleted the profile, so a
        # checkout that still ships it fails the validator.
        (self.root / check_primary_prompts.PROFILE_DIR / "concord-coordinator.md").write_text(
            "---\nmode: primary\n---\n\n# Concord coordinator\n", encoding="utf-8"
        )
        findings = self.findings()
        self.assertTrue(any("concord-coordinator" in finding for finding in findings))

    def test_operator_lost_boundary_phrase_reports(self) -> None:
        self.rewrite("concord-operator.md", lambda text: re.sub(r"this session stops driving the\s+other\s+repository", "both sessions drive it", text))
        findings = self.findings()
        self.assertTrue(any("concord-operator.md" in finding and "cross-repository boundary phrase" in finding for finding in findings))

    def test_operator_lost_delegation_anchor_reports(self) -> None:
        self.rewrite("concord-operator.md", lambda text: text.replace("Size is not an exemption", "Size decides"))
        findings = self.findings()
        self.assertTrue(any("concord-operator.md" in finding and "delegation anchor" in finding for finding in findings))

    def test_named_profile_body_routing_by_generated_id_reports(self) -> None:
        self.rewrite("concord-operator.md", lambda text: text.replace("the explore utility", "concord-explore", 1))
        findings = self.findings()
        self.assertTrue(any("concord-operator.md" in finding and "concord-explore" in finding for finding in findings))

    def test_named_profile_host_tool_in_frontmatter_reports(self) -> None:
        self.rewrite("concord-operator.md", lambda text: text.replace("  webfetch: allow\n", "  webfetch: allow\n  exa_*: allow\n", 1))
        findings = self.findings()
        self.assertTrue(any("concord-operator.md" in finding and "exa_" in finding for finding in findings))

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


if __name__ == "__main__":
    raise SystemExit(unittest.main())

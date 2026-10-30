#!/usr/bin/env python3
"""Tests for scripts/check-primary-prompts.py.

Positive scenarios run the checker against the real profiles and intake example.
Negative scenarios copy them into a temporary tree, break one invariant on
purpose, and prove the checker reports it: a host tool name in frontmatter,
duplicated conduct text, a lost intake boundary, or a retired example. The named
profiles prove their contract: the Concord-RTE and Concord profiles carry Build-level grants
with no workflow denial, Concord-Planner keeps
its path-scoped edits, ask-default shell, utility-only delegation, and
work-state denials, and the retired concord-coordinator and concord-operator
profiles are rejected on sight.
"""
from __future__ import annotations

import importlib.util
import shutil
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
        self.assertTrue(any("missing named primary profile" in finding and "Concord-RTE.md" in finding for finding in findings))
        (self.root / check_primary_prompts.PROFILE_DIR / "Concord.md").unlink()
        findings = self.findings()
        self.assertTrue(any("missing named primary profile" in finding and "Concord.md" in finding for finding in findings))

    def test_concord_lost_build_permission_grants_report(self) -> None:
        def strip_grants(text: str) -> str:
            for tool in check_primary_prompts.PRIMARY_BUILD_TOOLS:
                line = f"  {tool}: allow\n"
                self.assertIn(line, text, line)
                text = text.replace(line, "")
            return text

        self.rewrite("Concord.md", strip_grants)
        findings = self.findings()
        for tool in check_primary_prompts.PRIMARY_BUILD_TOOLS:
            self.assertTrue(
                any("Concord.md" in finding and tool in finding for finding in findings),
                tool,
            )

    def test_concord_workflow_denial_reports(self) -> None:
        # Concord holds no profile ceiling: a workflow denial in its
        # frontmatter would strand the end-to-end work the role exists for.
        self.rewrite(
            "Concord.md",
            lambda text: text.replace("  webfetch: allow\n", "  webfetch: allow\n  concord_work_start: deny\n"),
        )
        findings = self.findings()
        self.assertTrue(any("Concord.md" in finding and "concord_work_start" in finding for finding in findings))

    def test_rte_lost_build_permission_grants_report(self) -> None:
        def strip_grants(text: str) -> str:
            for tool in check_primary_prompts.PRIMARY_BUILD_TOOLS:
                line = f"  {tool}: allow\n"
                self.assertIn(line, text, line)
                text = text.replace(line, "")
            return text

        self.rewrite("Concord-RTE.md", strip_grants)
        findings = self.findings()
        for tool in check_primary_prompts.PRIMARY_BUILD_TOOLS:
            self.assertTrue(
                any("Concord-RTE.md" in finding and tool in finding for finding in findings),
                tool,
            )

    def test_rte_workflow_denial_reports(self) -> None:
        # The RTE holds no profile ceiling: a workflow denial in its
        # frontmatter would strand the job the role exists for.
        self.rewrite(
            "Concord-RTE.md",
            lambda text: text.replace("  webfetch: allow\n", "  webfetch: allow\n  concord_work_start: deny\n"),
        )
        findings = self.findings()
        self.assertTrue(any("Concord-RTE.md" in finding and "concord_work_start" in finding for finding in findings))

    def test_named_profiles_cannot_reallow_host_bash_denials(self) -> None:
        for name in check_primary_prompts.FULL_PROFILE_FILES:
            for rule in ('    "git *": allow\n', '    "git *": "ask"\n'):
                self.setUp()
                self.rewrite(name, lambda text: text.replace('  bash:\n', '  bash:\n' + rule, 1))
                self.assertTrue(any(name in finding and "bash rules must only deny named commands" in finding for finding in self.findings()), rule)

    def assert_planner_finding(self, transform, fragment: str) -> None:
        self.rewrite("Concord-Planner.md", transform)
        findings = self.findings()
        self.assertTrue(any("Concord-Planner.md" in finding and fragment in finding for finding in findings), findings)

    def test_wildcard_follows_opencode_matching(self) -> None:
        match = check_primary_prompts.wildcard_match
        self.assertTrue(match("git status", "git status *"))
        self.assertTrue(match("git status --short", "git status *"))
        self.assertFalse(match("git statusx", "git status *"))
        self.assertTrue(match(".planning/a/b.md", ".planning/*"))
        self.assertFalse(match("xplanning/a.md", ".planning/*"))
        self.assertTrue(match("ab", "a?"))
        self.assertFalse(match("a(b)", "a.b."))
        rules = [("*", "deny"), ("gh pr *", "deny"), ("gh pr view *", "allow")]
        self.assertEqual(check_primary_prompts.evaluate(rules, "gh pr view 1"), "allow")
        self.assertEqual(check_primary_prompts.evaluate(rules, "gh pr merge 1"), "deny")
        self.assertEqual(check_primary_prompts.evaluate([], "anything"), "ask")

    def test_planner_edit_outside_planning_reports(self) -> None:
        self.assert_planner_finding(lambda text: text.replace('  edit:\n    "*": deny\n', '  edit:\n    "*": allow\n', 1), "permission.edit")

    def test_planner_unscoped_editor_grant_reports(self) -> None:
        self.assert_planner_finding(lambda text: text.replace("  webfetch: allow\n", "  webfetch: allow\n  write: allow\n", 1), "unscoped 'write: allow'")

    def test_planner_reallowed_push_reports(self) -> None:
        self.assert_planner_finding(lambda text: text.replace('    "git push *": deny\n', "", 1), "'git push origin main' to ask, not deny")
        self.setUp()
        self.assert_planner_finding(lambda text: text.replace('    "git stash *": deny\n', '    "gh pr merge *": allow\n    "git stash *": deny\n', 1), "'gh pr merge 12 --auto --squash' to allow, not deny")

    def test_planner_allow_default_shell_reports(self) -> None:
        self.assert_planner_finding(lambda text: text.replace('  bash:\n    "*": ask\n', '  bash:\n    "*": allow\n', 1), 'permission.bash must open with "*": ask')

    def test_planner_lane_delegation_reports(self) -> None:
        self.assert_planner_finding(lambda text: text.replace('  task:\n    "*": deny\n', '  task:\n    "*": allow\n', 1), "'concord-implement' to allow, not deny")

    def test_planner_lost_work_state_denial_reports(self) -> None:
        self.assert_planner_finding(lambda text: text.replace("  concord_work_transition: deny\n", "", 1), "does not deny Concord tool concord_work_transition")

    def test_rte_coordination_issue_reports(self) -> None:
        path = self.root / check_primary_prompts.PROFILE_DIR / "Concord-RTE.md"
        original = path.read_text(encoding="utf-8")
        for inserted in (
            "Create a scoped coordination issue when the assignment needs one.",
            "Record the directive on the Coordination\n  Issue.",
            "Reuse the matching coordination issues.",
        ):
            with self.subTest(inserted=inserted):
                self.rewrite("Concord-RTE.md", lambda _: original.replace(
                    "## Run the coordination pass", f"{inserted}\n\n## Run the coordination pass", 1,
                ))
                findings = self.findings()
                self.assertEqual(len(findings), 1, findings)
                self.assertIn("Concord-RTE.md names", findings[0])
                self.assertIn("never an issue", findings[0])

    def test_retired_coordinator_profile_reports(self) -> None:
        # Zero tolerance: a checkout that still ships a retired profile
        # fails the validator.
        (self.root / check_primary_prompts.PROFILE_DIR / "concord-coordinator.md").write_text(
            "---\nmode: primary\n---\n\n# Concord coordinator\n", encoding="utf-8"
        )
        findings = self.findings()
        self.assertTrue(any("concord-coordinator" in finding for finding in findings))

    def test_retired_operator_profile_reports(self) -> None:
        # Zero tolerance, and the rule is not coordinator-specific: a
        # checkout that still ships the retired operator profile fails the
        # validator too.
        (self.root / check_primary_prompts.PROFILE_DIR / "concord-operator.md").write_text(
            "---\nmode: primary\n---\n\n# Concord operator\n", encoding="utf-8"
        )
        findings = self.findings()
        self.assertTrue(any("concord-operator" in finding for finding in findings))

    def test_concord_lost_delegation_anchor_reports(self) -> None:
        self.rewrite("Concord.md", lambda text: text.replace("Size is not an exemption", "Size decides"))
        findings = self.findings()
        self.assertTrue(any("Concord.md" in finding and "delegation anchor" in finding for finding in findings))

    def test_named_profile_body_routing_by_generated_id_reports(self) -> None:
        self.rewrite("Concord.md", lambda text: text.replace("the explore utility", "concord-explore", 1))
        findings = self.findings()
        self.assertTrue(any("Concord.md" in finding and "concord-explore" in finding for finding in findings))

    def test_named_profile_host_tool_in_frontmatter_reports(self) -> None:
        self.rewrite("Concord.md", lambda text: text.replace("  webfetch: allow\n", "  webfetch: allow\n  exa_*: allow\n", 1))
        findings = self.findings()
        self.assertTrue(any("Concord.md" in finding and "exa_" in finding for finding in findings))

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
        (self.root / check_primary_prompts.PROMPT_DIR / "concord-0.md").unlink()
        findings = self.findings()
        self.assertTrue(any("missing primary agent example" in finding for finding in findings))

    def test_host_tool_name_in_frontmatter_reports(self) -> None:
        self.rewrite("concord-0.md", lambda text: text.replace("permission:\n", "permission:\n  github_*: allow\n", 1))
        findings = self.findings()
        self.assertTrue(any("github_'" in finding and "frontmatter" in finding for finding in findings))

    def test_host_mutation_tool_permission_in_coordinator_frontmatter_reports(self) -> None:
        self.rewrite("Concord.md", lambda text: text.replace("  edit: allow\n", "  edit: allow\n  morph_edit: allow\n"))
        findings = self.findings()
        self.assertTrue(any("morph_edit" in finding and "frontmatter" in finding for finding in findings))

    def test_unclosed_frontmatter_reports_without_traceback(self) -> None:
        self.rewrite("concord-0.md", lambda text: text.replace("\n---\n", "\n", 1))
        findings = self.findings()
        self.assertTrue(any("frontmatter is not closed" in finding for finding in findings))

    def test_retired_examples_report(self) -> None:
        for name in check_primary_prompts.RETIRED_PRIMARY_EXAMPLES:
            with self.subTest(name=name):
                path = self.root / check_primary_prompts.PROMPT_DIR / name
                path.write_text("---\nmode: primary\n---\n", encoding="utf-8")
                self.assertTrue(any(
                    "retired primary agent example" in finding and name in finding
                    for finding in self.findings()
                ))
                path.unlink()

    def test_retired_examples_do_not_ship(self) -> None:
        for name in check_primary_prompts.RETIRED_PRIMARY_EXAMPLES:
            self.assertFalse((ROOT / check_primary_prompts.PROMPT_DIR / name).exists())

    def test_duplicated_lookup_obligation_reports(self) -> None:
        self.rewrite("concord-0.md", lambda text: text.replace("## Dispatch boundary", "Recall is not evidence.\n\n## Dispatch boundary"))
        findings = self.findings()
        self.assertTrue(any("duplicates the technical-unknown" in finding for finding in findings))

    def test_intake_boundary_loss_reports(self) -> None:
        self.rewrite("concord-0.md", lambda text: text.replace("  write: deny\n", ""))
        findings = self.findings()
        self.assertTrue(any("intake boundary" in finding for finding in findings))

    def test_intake_github_planning_write_grants_report(self) -> None:
        original = check_primary_prompts.read_prompt(self.root, "concord-0.md")
        for pattern in ("gh issue create *", "gh issue edit *", "gh issue comment *", "gh issue create*", "gh issue close *", "gh api *"):
            with self.subTest(pattern=pattern):
                self.rewrite("concord-0.md", lambda _: original.replace(
                    '    "gh issue view *": allow\n',
                    f'    "gh issue view *": allow\n    "{pattern}": allow\n',
                ))
                self.assertTrue(any("unapproved bash grant" in finding for finding in self.findings()))

    def test_coordinator_task_boundary_loss_reports(self) -> None:
        self.rewrite(
            "Concord.md",
            lambda text: text.replace('task:\n    "*": allow', 'task:\n    "*": deny'),
        )
        findings = self.findings()
        self.assertTrue(any("does not allow task by default" in finding for finding in findings))

    def test_copied_profiles_and_example_pass(self) -> None:
        self.assertEqual(self.findings(), [])


if __name__ == "__main__":
    raise SystemExit(unittest.main())

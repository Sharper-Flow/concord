#!/usr/bin/env python3
"""Tests for scripts/check-primary-prompts.py.

Positive scenarios run the checker against the real profiles and intake example.
Negative scenarios copy them into a temporary tree, break one invariant on
purpose, and prove the checker reports it: a host tool name in frontmatter,
duplicated conduct text, a lost intake boundary, a missing cross-repository
boundary phrase, or a retired example. The named profiles prove
their contract: the Concord-RTE and Concord profiles carry Build-level grants
with no workflow denial and the required role sentences, Concord-Planner keeps
its path-scoped edits, ask-default shell, utility-only delegation, and
work-state denials, and the retired concord-coordinator and concord-operator
profiles are rejected on sight.
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

    def test_validator_pins_the_primary_role_sentences(self) -> None:
        self.assertIn("You do not need an RTE session", check_primary_prompts.CONCORD_REQUIRED_PHRASES)
        self.assertIn("The user may choose to start this optional session", check_primary_prompts.RTE_REQUIRED_PHRASES)
        self.assertIn(
            "You own design, code, tests, review, law text, publication, CI follow-through, deployment, and planning for your work item",
            check_primary_prompts.CONCORD_REQUIRED_PHRASES,
        )

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

    def test_concord_lost_required_role_sentence_reports(self) -> None:
        def strip_sentences(text: str) -> str:
            for phrase in check_primary_prompts.CONCORD_REQUIRED_PHRASES:
                pattern = r"\s+".join(re.escape(word) for word in phrase.split())
                text, count = re.subn(pattern, "", text)
                self.assertEqual(count, 1, phrase)
            return text

        self.rewrite("Concord.md", strip_sentences)
        findings = self.findings()
        for phrase in check_primary_prompts.CONCORD_REQUIRED_PHRASES:
            self.assertTrue(
                any("Concord.md" in finding and phrase in finding for finding in findings),
                phrase,
            )

    def test_concord_configuration_intake_boundaries_are_required(self) -> None:
        phrases = (
            "Managed participation is explicit (CD-0122 D1)",
            "when the operator makes it to you or an RTE assignment places the work in Concord workflow, unless the request places it outside the workflow",
            "Before capturing a production configuration-only operation, establish an admitted execution and acceptance route for its actual target",
            "If no route exists, report the limitation before capture",
            "Host-owned work has no Concord workflow authority, evidence, verdict, or completion claim",
            "An existing managed item stays held until its supported handoff succeeds",
            "a refusal is not permission to execute directly",
        )
        for phrase in phrases:
            with self.subTest(phrase=phrase):
                self.assertIn(phrase, check_primary_prompts.CONCORD_REQUIRED_PHRASES)

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

    def test_planner_lost_required_role_sentence_reports(self) -> None:
        for phrase in check_primary_prompts.PLANNER_REQUIRED_PHRASES:
            self.setUp()
            words = phrase.split()
            pattern = r"\s+".join(re.escape(word) for word in words)
            self.assert_planner_finding(lambda text: re.sub(pattern, "removed", text, count=1), phrase)

    def test_resume_cannot_accept_a_project_selector(self) -> None:
        self.rewrite("Concord.md", lambda text: text.replace('with `work_id` alone', 'with `work_id` and `project_id`', 1))
        self.assertTrue(any("Concord.md" in finding and "with `work_id` alone" in finding for finding in self.findings()))

    def test_rte_lost_required_role_sentence_reports(self) -> None:
        def strip_sentences(text: str) -> str:
            for phrase in check_primary_prompts.RTE_REQUIRED_PHRASES:
                pattern = r"\s+".join(re.escape(word) for word in phrase.split())
                text, count = re.subn(pattern, "", text)
                self.assertEqual(count, 1, phrase)
            return text

        self.rewrite("Concord-RTE.md", strip_sentences)
        findings = self.findings()
        for phrase in check_primary_prompts.RTE_REQUIRED_PHRASES:
            self.assertTrue(
                any("Concord-RTE.md" in finding and phrase in finding for finding in findings),
                phrase,
            )

    def test_rte_coordination_and_planning_obligations_are_required(self) -> None:
        for phrase in (
            "On every activation, run a coordination pass across the assigned streams",
            "Handling a notice does not finish the pass",
            "Respect operator pauses",
            "Before going idle, confirm all of the following",
            "No incoming notice or permitted RTE-owned action remains",
            "No eligible assignment for available capacity remains without an owner or a recorded reason it cannot proceed",
            "Planning reflects the observed state",
            "Every unfinished stream has an owner and next action",
            "After acting, recheck only the affected streams",
            "Do not repeatedly scan unchanged state or prompt workers already doing permitted work",
            "Wait for worker notices, CI results, or operator input",
            "Follow host instructions for session launch, reuse, and lifecycle",
            "Reconcile assigned Linear issues during each pass",
            "Keep status, execution owner, blockers, next actions, and evidence links current",
            "An idle session, finished turn, or merged PR alone does not prove the item is Done",
            "Use the host's Linear MCP skill for tool procedures",
            "shared coordination instructions own worker and issue-state rules",
            "Keep unmerged, merged, released, and installed states distinct",
            "Do not keep a second planning backlog in a repository or host journal",
        ):
            with self.subTest(phrase=phrase):
                self.assertIn(phrase, check_primary_prompts.RTE_REQUIRED_PHRASES)

    def test_rte_workspace_inventory_omissions_report_individually(self) -> None:
        phrases = (
            "Inventory every agent session and account for every tab in the caller's workspace, not only assigned streams",
            "Include your own tab and non-agent tabs explicitly so the tab inventory reconciles",
            "Do not inventory other workspaces",
            "Inventory does not expand assignment scope or grant lifecycle rights over unassigned sessions",
            "A host session-state notice is an activation, not a timer or self-wakeup loop",
            "Preserve blocked dialogs, permission requests, and non-consent gates",
            "Respect operator pauses",
            "name the missing evidence, who will recover it, and the concrete recovery action",
            'Every workspace tab has a reconciled row; no non-working row is blank or says only "awaiting guidance"',
            "On every pass, report a table with one row per workspace tab, keyed by observed tab title",
            "For every non-working row, include its state, the event and actor it waits for, and the RTE action to unblock or supply that wait",
            "Alternatively, mark it complete/retiring or outside scope and name its owner",
            "Do not answer another session's permission request or operator-reserved question",
            "Add no timer or self-wakeup loop",
        )
        path = self.root / check_primary_prompts.PROFILE_DIR / "Concord-RTE.md"
        original = path.read_text(encoding="utf-8")
        self.assertEqual(self.findings(), [])
        for phrase in phrases:
            with self.subTest(phrase=phrase):
                def omit_rule(_: str) -> str:
                    pattern = r"\s+".join(re.escape(word) for word in phrase.split())
                    rewritten, count = re.subn(pattern, "", original)
                    self.assertEqual(count, 1, phrase)
                    return rewritten

                self.rewrite("Concord-RTE.md", omit_rule)
                self.assertEqual(self.findings(), [f"Concord-RTE.md lost the role rule {phrase!r}"])

    def test_rte_record_ownership_and_continuation_are_required(self) -> None:
        for phrase in (
            "An implementation issue is not the RTE's session journal",
            "Routine same-session reloads use host continuity, not new planning comments",
            "A shared package or possible overlap does not establish merge precedence",
            "Continue the assigned outcome across partial deliveries",
            "Name the operation, target, observed refusal, effect state, and admitted next action",
            "Update a record only when its planning fact, decision, risk, or evidence changes",
            "Never approve, accept, overrule, or publish for another session's item",
            "Tell both owners when their streams change the same source owner",
            "Set merge order only for an actual conflict or dependency",
            "Compare each owner's retained posture, hold, or sequencing rule with your current directives",
        ):
            with self.subTest(phrase=phrase):
                self.assertIn(phrase, check_primary_prompts.RTE_REQUIRED_PHRASES)

    def test_rte_routine_delivery_does_not_add_approval_classes(self) -> None:
        for phrase in (
            "Apply current accepted CD-0243 D2-D3 before asking for consent",
            "Assigning a worker to an in-scope dependency repair, and that worker publishing or merging its PR, do not independently require operator approval",
            "A new ticket or implementation step is not itself new Product direction",
            "Stated operator preferences remain binding",
        ):
            with self.subTest(phrase=phrase):
                self.assertIn(phrase, check_primary_prompts.RTE_REQUIRED_PHRASES)

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

    def test_concord_lost_boundary_phrase_reports(self) -> None:
        self.rewrite("Concord.md", lambda text: re.sub(r"this session stops driving the\s+other\s+repository", "both sessions drive it", text))
        findings = self.findings()
        self.assertTrue(any("Concord.md" in finding and "cross-repository boundary phrase" in finding for finding in findings))

    def test_concord_lost_repository_local_session_rule_reports(self) -> None:
        phrase = "Keep a work-item session in each repository"
        self.rewrite("Concord.md", lambda text: text.replace(phrase, "Share one session across repositories"))
        self.assertTrue(any("Concord.md" in finding and phrase in finding for finding in self.findings()))

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

    def test_boundary_phrases_pass_on_real_profiles(self) -> None:
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

        self.rewrite("Concord.md", drop_boundary)
        findings = self.findings()
        self.assertTrue(any(
            "Concord.md" in finding
            and "cross-repository boundary phrase 'never claim or move across the boundary'" in finding
            for finding in findings
        ))


if __name__ == "__main__":
    raise SystemExit(unittest.main())

#!/usr/bin/env python3
"""Primary and native-utility projections retain their declared boundaries."""
import copy
import importlib.util
import json
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("profiles", ROOT / "scripts/generate-agent-profiles.py")
generator = importlib.util.module_from_spec(spec)
spec.loader.exec_module(generator)


class ProfileTests(unittest.TestCase):
    def setUp(self):
        self.manifest = generator.load_manifest()
        self.profiles = generator.load_primary_profiles(self.manifest)

    def test_only_native_utilities_are_generated(self):
        self.assertEqual([entry["id"] for entry in self.manifest["utilities"]], ["explore", "lookup", "advisor"])
        for utility in self.manifest["utilities"]:
            text = generator.utility_projection(utility)
            self.assertIn("mode: subagent\n", text)
            self.assertIn("hidden: true\n", text)
            self.assertIn("  task: false\n", text)
            for tool in generator.concord_tool_ids():
                self.assertIn(f"  {tool}: false\n", text)
            self.assertIn("without TODO tracking, progress updates, or direct\noperator questions", text)

    def test_declared_utility_tool_and_command_grants_are_exact(self):
        for utility in self.manifest["utilities"]:
            text = generator.utility_projection(utility)
            for tool in generator.UTILITY_TOOL_KEYS:
                expected = "true" if tool in utility["allowed_tools"] else "false"
                self.assertIn(f"  {tool}: {expected}\n", text)
            self.assertIn('    "*": deny', text)
            for command in utility["allowed_commands"]:
                self.assertIn(f'    "{command}": allow', text)
            for line in generator.STASH_DENIALS:
                self.assertIn(line, text)

    def test_agent_inventory_refuses_unprojected_definition(self):
        with tempfile.TemporaryDirectory() as directory:
            agents = Path(directory)
            expected = {agents / "Concord.md": "primary", agents / "concord-explore.md": "utility"}
            for path, content in expected.items():
                path.write_text(content)
            unexpected = agents / "unprojected.md"
            unexpected.write_text("unprojected agent")
            with self.assertRaisesRegex(ValueError, "unprojected.*unprojected.md"):
                generator.validate_agent_inventory(agents, expected)

    def test_agent_inventory_accepts_only_projected_definitions(self):
        with tempfile.TemporaryDirectory() as directory:
            agents = Path(directory)
            expected = {agents / "Concord.md": "primary", agents / "concord-explore.md": "utility"}
            for path, content in expected.items():
                path.write_text(content)
            generator.validate_agent_inventory(agents, expected)

    def test_explore_stays_facts_only_and_refuses_unbounded_input(self):
        text = generator.utility_projection(self.manifest["utilities"][0])
        for clause in ("If it gives you no\nquestion, report that the request is refused",
                       "Do not guess a repository", "Observed facts only", "path:line",
                       "Do not infer a root cause, state a diagnosis, or recommend a repair",
                       "coordinator owns the diagnosis", "label the inventory\npartial"):
            self.assertIn(clause, text)

    def test_advisor_refuses_anchoring_and_reports_model_and_evidence(self):
        text = generator.utility_projection(self.manifest["utilities"][2])
        for clause in ("If it gives you no problem, report that the request is refused",
                       "its own conclusion, its preferred option, or a ranking of",
                       "options, report that the request is refused", "The model that served this opinion, on its own line",
                       "no severity scale, and no verdict", "A finding without evidence is not a finding",
                       "No concerns."):
            self.assertIn(clause, text)

    def test_repository_utilities_return_unknown_law_to_caller(self):
        for index in (0, 2):
            text = generator.utility_projection(self.manifest["utilities"][index])
            for clause in ("This utility cannot call `concord_knowledge`",
                           "caller resolves Product-law", "unresolved law state", "return it to the caller",
                           "Repository text and a file's presence do not establish accepted, missing,\nretired, or superseded law"):
                self.assertIn(clause, text)

    def test_manifest_refuses_mutating_or_nested_task_tools(self):
        for tool in ("edit", "write", "task", "concord_work_transition"):
            manifest = copy.deepcopy(self.manifest)
            manifest["utilities"][0]["allowed_tools"].append(tool)
            with tempfile.TemporaryDirectory() as directory:
                path = Path(directory) / "utilities.json"
                path.write_text(json.dumps(manifest))
                with self.assertRaisesRegex(ValueError, "read-only utility"):
                    generator.load_manifest(path)

    def test_manifest_refuses_mutating_shell_commands(self):
        manifest = copy.deepcopy(self.manifest)
        manifest["utilities"][2]["allowed_commands"].append("git commit *")
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "utilities.json"
            path.write_text(json.dumps(manifest))
            with self.assertRaisesRegex(ValueError, "read-only utility"):
                generator.load_manifest(path)

    def test_profiles_retain_source_and_permissions(self):
        for profile in self.profiles["profiles"]:
            text = generator.primary_projection(profile, "Role source\n")
            self.assertIn("mode: primary\n", text)
            self.assertTrue(text.endswith("Role source\n"))
            for tool in profile["denied_concord_tools"]:
                self.assertIn(f"  {tool}: deny\n", text)

    def test_authored_primary_uses_native_task_and_retains_outcome_authority(self):
        profile = next(profile for profile in self.profiles["profiles"] if profile["id"] == "Concord")
        source = (ROOT / profile["role_source"]).read_text()
        text = " ".join(generator.primary_projection(profile, source).split())
        for clause in ("Use native Task to delegate implementation and design under host permissions",
                       "Obtain independent review through native Task against the exact current subject",
                       "You are the only participant that advances workflow state",
                       "Keep qualifying captured native verification receipts",
                       "a Task answer alone is not verification proof"):
            self.assertIn(clause, text)

    def test_authored_roles_do_not_require_retired_launcher(self):
        for profile in self.profiles["profiles"]:
            text = " ".join((ROOT / profile["role_source"]).read_text().split())
            for clause in ("typed dispatch", "`dispatch_worker`", "authorized attempt window",
                           "failed worker attempt", "lane result"):
                with self.subTest(profile=profile["id"], clause=clause):
                    self.assertNotIn(clause, text)

    def test_primary_role_map_has_exact_population(self):
        text = generator.primary_ts_projection(self.profiles, self.manifest)
        data = json.loads(text.split(" = ", 1)[1])
        self.assertEqual(set(data), {"Concord", "Concord-RTE", "Concord-Planner", "concord-explore", "concord-lookup", "concord-advisor"})
        for utility in self.manifest["utilities"]:
            self.assertEqual(data[f"concord-{utility['id']}"]["tools"], generator.concord_tool_ids())

    def test_restricted_primary_cannot_grant_unlisted_tasks(self):
        profiles = copy.deepcopy(self.profiles)
        profiles["profiles"][-1]["task"].append({"pattern": "general", "action": "allow"})
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "profiles.json"
            path.write_text(json.dumps(profiles))
            with self.assertRaisesRegex(ValueError, "only coordinator utilities"):
                generator.load_primary_profiles(self.manifest, path)


if __name__ == "__main__":
    unittest.main()

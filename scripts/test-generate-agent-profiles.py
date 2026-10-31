#!/usr/bin/env python3
"""Primary and native-utility projections retain their declared boundaries."""
import copy
import importlib.util
import json
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("profiles", ROOT / "scripts/generate-agent-profiles.py")
generator = importlib.util.module_from_spec(spec)
spec.loader.exec_module(generator)
spec = importlib.util.spec_from_file_location("prompt_checks", ROOT / "scripts/check-primary-prompts.py")
checks = importlib.util.module_from_spec(spec)
spec.loader.exec_module(checks)


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
            self.assertIn(f"mode: {profile.get('mode', 'primary')}\n", text)
            self.assertTrue(text.endswith("Role source\n"))
            for tool in profile["denied_concord_tools"]:
                self.assertIn(f"  {tool}: deny\n", text)

    def test_authored_primary_uses_native_task_and_retains_outcome_authority(self):
        profile = next(profile for profile in self.profiles["profiles"] if profile["id"] == "Concord")
        source = (ROOT / profile["role_source"]).read_text()
        text = " ".join(generator.primary_projection(profile, source).split())
        for clause in (*checks.DELEGATION_ANCHOR_MARKERS,
                       "You are the only participant that advances workflow state"):
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
        self.assertEqual(set(data), {"Concord", "Concord-RTE", "Concord-Planner", "concord-explore", "concord-lookup", "concord-advisor", "concord-review", "concord-verify", "concord-implement", "concord-research"})
        for utility in self.manifest["utilities"]:
            self.assertEqual(data[f"concord-{utility['id']}"]["tools"], generator.concord_tool_ids())

    def test_restricted_primary_cannot_grant_unlisted_tasks(self):
        profiles = copy.deepcopy(self.profiles)
        planner = next(profile for profile in profiles["profiles"] if profile["id"] == "Concord-Planner")
        planner["task"].append({"pattern": "general", "action": "allow"})
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "profiles.json"
            path.write_text(json.dumps(profiles))
            with self.assertRaisesRegex(ValueError, "only coordinator utilities"):
                generator.load_primary_profiles(self.manifest, path)

    def test_reviewer_cannot_be_projected_as_a_primary(self):
        profiles = copy.deepcopy(self.profiles)
        next(profile for profile in profiles["profiles"] if profile["id"] == "concord-review")["mode"] = "primary"
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "profiles.json"
            path.write_text(json.dumps(profiles))
            with self.assertRaisesRegex(ValueError, "must declare mode subagent"):
                generator.load_primary_profiles(self.manifest, path)

    def reviewer(self):
        return next(profile for profile in self.profiles["profiles"] if profile["id"] == "concord-review")

    def verifier(self):
        return next(profile for profile in self.profiles["profiles"] if profile["id"] == "concord-verify")

    def test_researcher_method_and_effective_permissions(self):
        profile = next(profile for profile in self.profiles["profiles"] if profile["id"] == "concord-research")
        text = generator.primary_projection(profile, (ROOT / profile["role_source"]).read_text())
        self.assertIn("mode: subagent\n", text)
        self.assertNotIn("\nmodel:", text)
        normalized = " ".join(text.split())
        for clause in ("framed question", "known context", "explicit investigation budget", "needs-decision",
                       "bounded evidence plan", "through `concord_knowledge` before delegating",
                       "Choose follow-up questions from findings", "Verify decisive claims",
                       "assigned budget ends, whichever comes first", "Do not edit files",
                       "bounded read-only diagnostics", "citation at each claim", "conflicting evidence",
                       "remaining uncertainty, gaps and decisions", "driving session records the conclusion",
                       "Command-text restrictions are hygiene, not a sandbox"):
            self.assertIn(clause, normalized)
        frontmatter = checks.split_frontmatter(text)
        # Flat permissions append after the host grants, just like pattern rules.
        flat = [(line.strip().split(": ")[0], line.strip().split(": ")[1])
                for line in frontmatter.splitlines() if line.startswith("  ") and not line.startswith("    ") and line.endswith((": allow", ": deny", ": ask"))]
        for tool in ("edit", "write", "patch", "apply_patch", "morph_edit", "question", "todowrite", "ci_watch",
                     "opencode_mcp_connect", "opencode_mcp_disconnect", *checks.PLANNER_WRITERS):
            self.assertEqual(checks.evaluate([("*", "allow"), *flat], tool), "deny", tool)
        for tool in ("read", "glob", "grep", "skill", "execute", "webfetch", "concord_knowledge",
                     "concord_work_browse", "concord_work_trace", "concord_domain", "concord_product_view"):
            self.assertEqual(checks.evaluate([("*", "allow"), *flat], tool), "allow", tool)
        self.assertEqual(set(profile["denied_concord_tools"]), set(checks.PLANNER_WRITERS))
        bash = checks.permission_rules(frontmatter, "bash")
        self.assertNotIn("*", [pattern for pattern, _ in bash])
        host = [("*", "allow"), ("ps *", "deny"), ("kill *", "deny"), ("pkill *", "deny")]
        for command in ("ps aux", "kill 123", "pkill opencode", "sudo true", "rm -rf / *", "git commit -m fix",
                        "git -C repo commit -m fix", "git push origin HEAD", "git stash", "gh pr create",
                        "gh --repo org/repo pr create", "gh release create v1", "gh workflow run ci",
                        "gh api repos/org/repo", "herdr-notify --to peer", "concord invoke"):
            self.assertEqual(checks.evaluate(host + bash, command), "deny", command)
        for command in ("git status", "git diff", "git log -n 5", "gh pr view 1", "concord --help",
                        "concord --version", "concord continuity-block", "concord client show"):
            self.assertEqual(checks.evaluate(host + bash, command), "allow", command)
        tasks = checks.permission_rules(frontmatter, "task")
        for agent in ("concord-explore", "concord-lookup"):
            self.assertEqual(checks.evaluate(tasks, agent), "allow", agent)
        for agent in (*generator.PRIMARY_PROFILE_IDS, *generator.STEP_HELPER_IDS, "concord-advisor", "general", "build", "unknown"):
            self.assertEqual(checks.evaluate(tasks, agent), "deny", agent)
        driver = next(profile for profile in self.profiles["profiles"] if profile["id"] == "Concord")
        driver_tasks = checks.permission_rules(checks.split_frontmatter(generator.primary_projection(driver, "Role\n")), "task")
        self.assertEqual(checks.evaluate(driver_tasks, "concord-research"), "allow")
        for other in self.profiles["profiles"]:
            if other["id"] in (*generator.STEP_HELPER_IDS, "Concord-Planner"):
                rules = [(rule["pattern"], rule["action"]) for rule in other["task"]]
                self.assertEqual(checks.evaluate(rules, "concord-research"), "deny", other["id"])

    def test_implementer_boundary_and_effective_permissions(self):
        profile = next(profile for profile in self.profiles["profiles"] if profile["id"] == "concord-implement")
        text = generator.primary_projection(profile, (ROOT / profile["role_source"]).read_text())
        self.assertIn("mode: subagent\n", text)
        self.assertNotIn("\nmodel:", text)
        for tool in ("edit", "write", "patch", "read", "skill", "execute"):
            self.assertEqual(profile["tools"][tool], "allow", tool)
        self.assertEqual(profile["tools"]["question"], "deny")
        self.assertEqual(set(profile["denied_concord_tools"]), set(checks.PLANNER_WRITERS))
        normalized = " ".join(text.split())
        for clause in ("Change, Fix or Removal", "paths or subsystem", "outcome", "named check", "needs-decision", "failing before", "passing after", "uncommitted"):
            self.assertIn(clause, normalized)
        frontmatter = checks.split_frontmatter(text)
        bash = checks.permission_rules(frontmatter, "bash")
        self.assertNotIn("*", [pattern for pattern, _ in bash])
        host = [("*", "allow"), ("ps *", "deny"), ("kill *", "deny"), ("pkill *", "deny")]
        for command in ("ps aux", "kill 123", "sudo true", "git commit -m fix", "git push origin HEAD", "gh pr create", "gh release create v1", "herdr-notify --to peer", "concord invoke", "/synthetic/bin/concord invoke", "env concord invoke", "go run ./cmd/concord invoke", "bash -c 'concord invoke' gh pr view 1"):
            self.assertEqual(checks.evaluate(host + bash, command), "deny", command)
        for command in ("git diff", "bin/oc-test targeted -- python3 scripts/test-generate-agent-profiles.py", "concord --help", "concord --version", "concord continuity-block", "concord client show"):
            self.assertEqual(checks.evaluate(host + bash, command), "allow", command)
        task = checks.permission_rules(frontmatter, "task")
        for agent in ("concord-explore", "concord-lookup"):
            self.assertEqual(checks.evaluate(task, agent), "allow", agent)
        for agent in ("general", "concord-advisor", "concord-review", "concord-verify", "concord-implement"):
            self.assertEqual(checks.evaluate(task, agent), "deny", agent)

    def test_verifier_is_model_free_test_only_and_returns_uncertainty(self):
        profile = self.verifier()
        text = generator.primary_projection(profile, (ROOT / profile["role_source"]).read_text())
        self.assertIn("mode: subagent\n", text)
        self.assertNotIn("\nmodel:", text)
        for tool in ("edit", "write", "patch", "apply_patch", "morph_edit", "question", "ci_watch", "todowrite"):
            self.assertEqual(profile["tools"][tool], "deny", tool)
        for tool in ("read", "glob", "grep", "skill", "execute"):
            self.assertEqual(profile["tools"][tool], "allow", tool)
        self.assertEqual(set(profile["denied_concord_tools"]), set(checks.PLANNER_WRITERS))
        for clause in ("explicit execution budget", "`TESTING.md` and `bin/oc-test`", "Do not edit files",
                       "Read its status first", "if it is not completed", "Never\nwatch, poll or wait",
                       "matches a linked record", "explicitly unknown", "exit codes", "unexpected changed paths",
                       "A Task result is not a bound verification report\nor a Concord verdict"):
            self.assertIn(clause, text)

    def test_verifier_effective_permissions_preserve_host_denies_and_caller_owned_waits(self):
        frontmatter = checks.split_frontmatter(generator.primary_projection(self.verifier(), "Role\n"))
        bash = checks.permission_rules(frontmatter, "bash")
        self.assertNotIn("*", [pattern for pattern, _ in bash])
        host = [("*", "allow"), ("ps *", "deny"), ("kill *", "deny"), ("pkill *", "deny")]
        for command in ("ps aux", "kill 123", "pkill opencode", "sudo true", "git commit -m fix", "git push origin HEAD",
                        "git -C repo commit -m fix", "gh pr create", "gh run rerun 1", "gh run watch 1",
                        "gh pr checks 1 --watch", "gh release create v1", "gh api repos/org/repo/actions/runs/1/rerun",
                        "herdr-notify --to peer", "ci-watch 1", "ci_watch 1", "concord invoke", "/synthetic/bin/concord invoke",
                        "env concord invoke", "go run ./cmd/concord invoke", "bash -c 'concord invoke' gh run view 1"):
            self.assertEqual(checks.evaluate(host + bash, command), "deny", command)
        for command in ("bin/oc-test targeted -- python3 scripts/test-generate-agent-profiles.py", "go test ./cmd/concord",
                        "gh run view 1 --repo org/repo --json status,jobs", "gh run view 1 --job 2 --log-failed",
                        "gh run list --repo org/repo", "git status", "git diff", "concord --help", "concord --version",
                        "concord continuity-block", "concord client show"):
            self.assertEqual(checks.evaluate(host + bash, command), "allow", command)
        tasks = checks.permission_rules(frontmatter, "task")
        for agent in ("concord-explore", "concord-lookup"):
            self.assertEqual(checks.evaluate(tasks, agent), "allow", agent)
        for agent in ("general", "concord-advisor", "concord-review", "concord-verify", "concord-implement"):
            self.assertEqual(checks.evaluate(tasks, agent), "deny", agent)

    def test_reviewer_is_native_repair_capable_and_loads_the_method(self):
        profile = self.reviewer()
        text = generator.primary_projection(profile, (ROOT / profile["role_source"]).read_text())
        self.assertIn("mode: subagent\n", text)
        self.assertNotIn("\nmodel:", text)
        for tool in ("edit", "write", "patch", "read", "skill", "execute"):
            self.assertEqual(profile["tools"][tool], "allow")
        self.assertEqual(profile["tools"]["question"], "deny")
        for tool in ("concord_knowledge", "concord_work_browse", "concord_work_trace", "concord_domain", "concord_product_view"):
            self.assertNotIn(tool, profile["denied_concord_tools"])
        self.assertEqual(set(profile["denied_concord_tools"]), set(checks.PLANNER_WRITERS))
        self.assertIn("Load the `concord-review` skill", text)
        self.assertIn("uncommitted", text)
        self.assertIn("caller commits", text)
        self.assertIn("no read-only review mode", text)

    def test_reviewer_effective_permissions_preserve_host_safety_and_block_delivery(self):
        text = generator.primary_projection(self.reviewer(), "Role source\n")
        frontmatter = checks.split_frontmatter(text)
        bash = checks.permission_rules(frontmatter, "bash")
        self.assertNotIn("*", [pattern for pattern, _ in bash])
        host = [("*", "allow"), ("kill *", "deny"), ("pkill *", "deny"), ("ps *", "deny")]
        for command in ("kill 123", "pkill opencode", "ps aux", "sudo true", "git commit -m fix", "git -C repo commit -m fix", "git push origin HEAD", "git commit-tree abc", "git cherry-pick abc", "git revert abc", "git am patch", "gh pr create", "gh --repo org/repo pr create", "gh release create v1", "gh --repo org/repo release create v1", "herdr-notify --to peer", "herdr agent prompt peer hello", "bash -c 'concord invoke' gh pr view 1"):
            with self.subTest(command=command):
                self.assertEqual(checks.evaluate(host + bash, command), "deny")
        for command in ("go test ./internal/agent", "bin/oc-test targeted -- python3 scripts/test-generate-agent-profiles.py", "git diff", "git status",
                        "go test ./cmd/concord -run TestCommandSpecs", "git diff origin/main -- cmd/concord", "git -C /synthetic/concord status", "ls cmd/concord", "gh pr view 1", "gh pr diff 1", "gh pr checks 1"):
            self.assertEqual(checks.evaluate(host + bash, command), "allow", command)
        task = checks.permission_rules(frontmatter, "task")
        for agent in ("concord-explore", "concord-lookup"):
            self.assertEqual(checks.evaluate(task, agent), "allow")
        for agent in ("concord-advisor", "concord-review", "concord-implement", "general", "build"):
            self.assertEqual(checks.evaluate(task, agent), "deny")

    def test_step_helpers_cli_denies_self_asserted_workflow_identity(self):
        for profile in self.profiles["profiles"]:
            if profile["id"] not in generator.STEP_HELPER_IDS:
                continue
            with self.subTest(profile=profile["id"]):
                bash = checks.permission_rules(checks.split_frontmatter(generator.primary_projection(profile, "Role\n")), "bash")
                for binary in ("concord", "/synthetic/bin/concord", "bin/concord", "./concord", "env concord", "env CONCORD_DB=x concord", "xargs concord", "go run ./cmd/concord"):
                    for arguments in ("invoke --agent-ref Concord", "work-capture", "work-action", "client register", "client show-extra", "--db state.db invoke", "--help invoke"):
                        command = f"{binary} {arguments}"
                        self.assertEqual(checks.evaluate(bash, command), "deny", command)
                for command in ("bash -c 'concord invoke'", 'sh -c "cd repo && concord work-action"', "sh -c 'cd repo;concord invoke'", 'eval "concord invoke"',
                                 "bash -c 'concord invoke' /synthetic/bin/concord --help", "env herdr-notify --to peer", "bash -c 'herdr agent prompt peer hi'"):
                    self.assertEqual(checks.evaluate(bash, command), "deny", command)
                for arguments in ("--help", "--version", "continuity-block", "client show"):
                    self.assertEqual(checks.evaluate(bash, f"concord {arguments}"), "allow", arguments)

    @unittest.skipUnless(os.environ.get("CONCORD_HOST_MERGE_PROBE"), "set CONCORD_HOST_MERGE_PROBE for isolated real-host resolution")
    def test_staged_host_admits_native_review_and_preserves_effective_denials(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            home, repo = root / "home", root / "repo"
            for path in (home / ".config/opencode", home / ".local/share", home / ".cache", repo / ".opencode/agents"):
                path.mkdir(parents=True)
            (home / ".config/opencode/opencode.json").write_text(json.dumps({"permission": {"bash": {"*": "allow", "ps *": "deny", "kill *": "deny", "pkill *": "deny"}}}))
            for profile in self.profiles["profiles"]:
                shutil.copyfile(ROOT / f".opencode/agents/{profile['id']}.md", repo / f".opencode/agents/{profile['id']}.md")
            env = {"PATH": os.environ["PATH"], "HOME": str(home), "XDG_CONFIG_HOME": str(home / ".config"), "XDG_DATA_HOME": str(home / ".local/share"), "XDG_CACHE_HOME": str(home / ".cache")}

            def resolve(agent):
                result = subprocess.run([os.environ["CONCORD_HOST_MERGE_PROBE"], "--pure", "debug", "agent", agent], cwd=repo, env=env, capture_output=True, text=True, timeout=30)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                data = json.loads(result.stdout)
                self.assertEqual(data["name"], agent)
                return data

            def action(data, permission, value="*"):
                rules = [(rule["pattern"], rule["action"]) for rule in data["permission"] if checks.wildcard_match(permission, rule["permission"])]
                return checks.evaluate(rules, value)

            driver, reviewer = resolve("Concord"), resolve("concord-review")
            self.assertEqual(reviewer["mode"], "subagent")
            self.assertEqual(action(driver, "task", "concord-review"), "allow")
            for tool in checks.PLANNER_WRITERS:
                self.assertEqual(action(reviewer, tool), "deny", tool)
            for tool in ("concord_knowledge", "concord_domain", "concord_work_browse", "concord_work_trace", "concord_product_view", "edit", "write"):
                self.assertEqual(action(reviewer, tool), "allow", tool)
            for command in ("ps aux", "kill 123", "pkill opencode", "concord invoke", "/synthetic/bin/concord invoke", "env concord invoke", "concord client register", "git commit -m fix", "git push origin HEAD", "gh pr create", "herdr-notify --to peer"):
                self.assertEqual(action(reviewer, "bash", command), "deny", command)
            for command in ("go test ./internal/agent", "go test ./cmd/concord -run TestCommandSpecs"):
                self.assertEqual(action(reviewer, "bash", command), "allow", command)
            self.assertEqual(action(reviewer, "question"), "deny")
            for agent in ("concord-explore", "concord-lookup"):
                self.assertEqual(action(reviewer, "task", agent), "allow", agent)
            for agent in ("general", "concord-review", "concord-advisor"):
                self.assertEqual(action(reviewer, "task", agent), "deny", agent)
            verifier = resolve("concord-verify")
            self.assertEqual(verifier["mode"], "subagent")
            self.assertEqual(action(driver, "task", "concord-verify"), "allow")
            for tool in (*checks.PLANNER_WRITERS, "edit", "write", "patch", "apply_patch", "morph_edit", "question", "ci_watch"):
                self.assertEqual(action(verifier, tool), "deny", tool)
            for tool in ("concord_knowledge", "concord_domain", "concord_work_browse", "concord_work_trace", "concord_product_view"):
                self.assertEqual(action(verifier, tool), "allow", tool)
            for command in ("ps aux", "kill 123", "pkill opencode", "concord invoke", "git commit -m fix", "git push origin HEAD", "gh run watch 1", "gh pr create", "herdr-notify --to peer"):
                self.assertEqual(action(verifier, "bash", command), "deny", command)
            for command in ("bin/oc-test targeted -- python3 scripts/test-generate-agent-profiles.py", "gh run view 1 --log-failed"):
                self.assertEqual(action(verifier, "bash", command), "allow", command)
            for agent in ("concord-explore", "concord-lookup"):
                self.assertEqual(action(verifier, "task", agent), "allow", agent)
            for agent in ("general", "concord-review", "concord-verify", "concord-advisor"):
                self.assertEqual(action(verifier, "task", agent), "deny", agent)
            implementer = resolve("concord-implement")
            self.assertEqual(implementer["mode"], "subagent")
            self.assertEqual(action(driver, "task", "concord-implement"), "allow")
            for tool in (*checks.PLANNER_WRITERS, "question"):
                self.assertEqual(action(implementer, tool), "deny", tool)
            for tool in ("concord_knowledge", "concord_domain", "concord_work_browse", "concord_work_trace", "concord_product_view", "edit", "write"):
                self.assertEqual(action(implementer, tool), "allow", tool)
            for command in ("ps aux", "kill 123", "pkill opencode", "concord invoke", "/synthetic/bin/concord invoke", "env concord invoke", "git commit -m fix", "git push origin HEAD", "gh pr create", "herdr-notify --to peer"):
                self.assertEqual(action(implementer, "bash", command), "deny", command)
            for command in ("git diff", "bin/oc-test targeted -- python3 scripts/test-generate-agent-profiles.py", "concord --help"):
                self.assertEqual(action(implementer, "bash", command), "allow", command)
            for agent in ("concord-explore", "concord-lookup"):
                self.assertEqual(action(implementer, "task", agent), "allow", agent)
            for agent in ("general", "concord-review", "concord-verify", "concord-implement", "concord-advisor"):
                self.assertEqual(action(implementer, "task", agent), "deny", agent)
            researcher = resolve("concord-research")
            self.assertEqual(researcher["mode"], "subagent")
            self.assertEqual(action(driver, "task", "concord-research"), "allow")
            for tool in (*checks.PLANNER_WRITERS, "edit", "write", "patch", "apply_patch", "morph_edit", "question"):
                self.assertEqual(action(researcher, tool), "deny", tool)
            for tool in ("concord_knowledge", "concord_domain", "concord_work_browse", "concord_work_trace", "concord_product_view"):
                self.assertEqual(action(researcher, tool), "allow", tool)
            for command in ("ps aux", "kill 123", "pkill opencode", "concord invoke", "git commit -m fix", "git push origin HEAD", "gh pr create", "herdr-notify --to peer"):
                self.assertEqual(action(researcher, "bash", command), "deny", command)
            self.assertEqual(action(researcher, "bash", "git diff"), "allow")
            for agent in ("concord-explore", "concord-lookup"):
                self.assertEqual(action(researcher, "task", agent), "allow", agent)
            for agent in (*generator.STEP_HELPER_IDS, "concord-advisor", "general"):
                self.assertEqual(action(researcher, "task", agent), "deny", agent)


if __name__ == "__main__":
    unittest.main()

#!/usr/bin/env python3
"""Check real declaration data without executing native controls.

Native resolution remains owned by ResolveNativeRecipeCase in the store;
these assertions check adoption and independent corpus pins, not a second decoder.
"""
from __future__ import annotations

import importlib.util
import json
import re
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("tooling_checker", ROOT / "scripts/check-project-tooling.py")
assert SPEC and SPEC.loader
checker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(checker)


def load(path: str) -> dict:
    return json.loads((ROOT / path).read_text(encoding="utf-8"))


class DefaultToolingCoverageTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tools = load(".concord/tooling.v1.json")["tools"]
        self.bindings = {
            (entry["anchor"]["kind"], entry["anchor"]["value"]): entry
            for tool in self.tools for entry in tool.get("coverage", [])
        }

    def test_manifest_remains_valid_and_bounded(self) -> None:
        self.assertEqual(checker.check(root=ROOT), [])
        self.assertTrue(self.bindings, "default declarations must adopt coverage")

    def test_declared_cases_and_pinned_files_exist(self) -> None:
        for entry in self.bindings.values():
            recipe = load(entry["recipe_path"])
            self.assertTrue(recipe["cases"][entry["case_id"]], entry)
            for key in ("test_files", "fixture_files", "harness_files", "support_files"):
                for path in recipe.get(key, []):
                    findings: list[str] = []
                    checker.validate_file_path(ROOT, entry["recipe_path"], key, path, findings)
                    self.assertEqual(findings, [])

    def test_every_owned_package_with_tests_has_complete_package_declaration(self) -> None:
        owners = load(".concord/docs/knowledge/package-domains.json")["packages"]
        expected = {"./" + owner["path"] for owner in owners
                    if list((ROOT / owner["path"]).glob("*_test.go"))}
        actual = {value for kind, value in self.bindings if kind == "go_package"}
        self.assertEqual(actual, expected)
        for package in sorted(expected):
            entry = self.bindings[("go_package", package)]
            recipe = load(entry["recipe_path"])
            self.assertEqual(recipe["kind"], "go_package_v1")
            self.assertEqual(recipe["package_cwd"], package[2:])
            self.assertEqual(recipe["cases"][entry["case_id"]], ["go_package:" + package])
            self.assertFalse(recipe.get("test_files"))
            self.assertFalse(recipe.get("fixture_files"))

    def test_store_package_only_allows_named_git_prerequisite_skips(self) -> None:
        entry = self.bindings[("go_package", "./internal/store")]
        self.assertEqual(load(entry["recipe_path"])["expected_skips"], [
            "TestAdapterDispatchRecipeGenerated",
            "TestAdapterDispatchRecipeRecordsReady",
            "TestMergeEvidenceSchemaAlignsWithFoldRule",
            "TestOutsideRepairOutcomeKindParity",
            "TestOutsideRepairRecordVerdictGroupSubsets",
            "TestOutsideRepairUnicodeParity",
            "TestStoreOracleRecipeRecordsReady",
            "TestStoreTemplateOracleRecipeExecutes",
        ])

    def test_go_corpus_cases_keep_exact_independent_scenario_witnesses(self) -> None:
        corpus_bindings = 0
        for (kind, identity), entry in self.bindings.items():
            if kind != "scenario":
                continue
            recipe = load(entry["recipe_path"])
            if recipe["kind"] != "go_top_level_v1":
                continue
            corpus_bindings += 1
            witnesses = recipe["cases"][entry["case_id"]]
            self.assertTrue(any(name.endswith("/" + identity) for name in witnesses), identity)
            self.assertTrue(recipe["test_files"], identity)
            corpora = [name for name in recipe["fixture_files"] if name.startswith(".concord/scenarios/")]
            self.assertTrue(any('"' + identity + '"' in (ROOT / name).read_text(encoding="utf-8")
                                for name in corpora), identity)
        self.assertGreaterEqual(corpus_bindings, 35)

    def test_named_go_tests_use_independent_selected_harnesses(self) -> None:
        count = 0
        for (kind, identity), entry in self.bindings.items():
            if kind != "go_test":
                continue
            count += 1
            package, test = identity.rsplit(".", 1)
            recipe = load(entry["recipe_path"])
            self.assertEqual(recipe["kind"], "go_top_level_v1")
            self.assertEqual(recipe["package_cwd"], package)
            self.assertIn(test, recipe["cases"][entry["case_id"]])
            self.assertTrue(recipe["test_files"])
        self.assertGreaterEqual(count, 7)

    def test_all_continuity_obligations_have_exact_recipe_witnesses(self) -> None:
        corpus = load(".concord/scenarios/adapter-continuity.v1.json")
        for scenario in corpus["scenarios"]:
            identity = scenario["id"]
            entry = self.bindings[("scenario", identity)]
            recipe = load(entry["recipe_path"])
            witnesses = recipe["cases"][entry["case_id"]]
            if recipe["kind"] == "go_top_level_v1":
                self.assertTrue(any(name.endswith("/" + identity) for name in witnesses))
                continue
            self.assertIn(identity, witnesses)
            check = next(check for check in recipe["checks"] if check["witness"] == identity)
            self.assertEqual(check["argv"][-2:], ["--expect", identity])
            self.assertIn(".concord/scenarios/adapter-continuity.v1.json", recipe["support_files"])

    def test_generated_lane_symbols_require_freshness_and_tests(self) -> None:
        for anchor in ("internal/store/generated_agent_lanes.go#LaneRegistryManifestDigest",
                       "adapter/opencode/generated-agent-lanes.ts#agentUtilities"):
            entry = self.bindings[("generated", anchor)]
            recipe = load(entry["recipe_path"])
            witnesses = recipe["cases"][entry["case_id"]]
            commands = [check["argv"] for check in recipe["checks"] if check["witness"] in witnesses]
            self.assertIn(["python3", "scripts/generate-agent-lanes.py", "--check"], commands)
            self.assertIn(["python3", "scripts/test-generate-agent-lanes.py"], commands)
            self.assertIn("scripts/generate-agent-contracts.py", recipe["harness_files"])

    def test_wrappers_and_unrelated_checks_acquire_no_coverage(self) -> None:
        for tool in self.tools:
            if tool.get("coverage"):
                self.assertTrue(checker.direct_command(tool["invocation"]), tool["id"])
            if tool["invocation"].startswith("bin/") or tool["id"] in {"repo-validators", "go-vet", "gofmt", "govulncheck"}:
                self.assertFalse(tool.get("coverage"), tool["id"])
        self.assertNotIn(("validator", "scripts/check-json.py"), self.bindings)

    def test_new_python_tests_are_executed_by_required_ci(self) -> None:
        ci = (ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        for script in ("scripts/test-adapter-acceptance.py", "scripts/test-default-tooling-coverage.py"):
            self.assertTrue(re.search(r"(?m)^        run:.*python3 " + re.escape(script) + r"(?:\s|$)", ci), script)


if __name__ == "__main__":
    unittest.main(verbosity=2)

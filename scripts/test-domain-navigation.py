#!/usr/bin/env python3
"""Navigation failures must stop at the repository boundary, not invent owners."""

from __future__ import annotations

import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from unittest.mock import patch

SCRIPTS = Path(__file__).resolve().parent
sys.path.insert(0, str(SCRIPTS))
SPEC = importlib.util.spec_from_file_location("navigation_registry_checker", SCRIPTS / "check-domain-registry.py")
checker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(checker)
import domain_navigation as nav


class NavigationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.registry = {
            "schema_version": "1.0", "product_key": "fixture", "root_domain_id": "product-root:fixture",
            "domains": [
                {"domain_id": "product-root:fixture", "name": "Fixture", "purpose": "Product-wide constraints.", "status": "current", "architecture_relations": []},
                {"domain_id": "core", "name": "Core", "purpose": "Fixture implementation.", "status": "current", "parent_domain_id": "product-root:fixture", "architecture_relations": []},
            ],
        }
        self.manifest = {"domain_registry": self.registry, "records": [
            {"id": "root-law", "status": "accepted", "home_domain_id": "product-root:fixture"},
            {"id": "core-law", "status": "accepted", "home_domain_id": "core"},
        ]}
        self.navigation = {"schema_version": "1.0", "rules": [
            {"domain_id": "product-root:fixture", "include": ["ROOT.md"], "exclude": []},
            {"domain_id": "core", "include": ["src/**", ".concord/navigation/**"], "exclude": []},
        ], "unresolved": [], "test_coverage": []}
        self.write("ROOT.md", "Fixture\n")
        self.write("src/a.go", "package fixture\n")
        self.save()
        self.git("init", "-q", "-b", "main")
        self.git("add", ".")
        self.git("commit", "-q", "-m", "fixture")

    def git(self, *args):
        env = dict(os.environ, GIT_AUTHOR_NAME="Fixture", GIT_AUTHOR_EMAIL="fixture@example.invalid",
                   GIT_COMMITTER_NAME="Fixture", GIT_COMMITTER_EMAIL="fixture@example.invalid")
        return subprocess.run(["git", "-C", str(self.root), *args], env=env, check=True, capture_output=True, text=True).stdout

    def write(self, path, text):
        target = self.root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(text)

    def save(self):
        # The companion owns itself through the implementation Domain in fixtures.
        if ".concord/domain-navigation.v1.json" not in self.navigation["rules"][1]["include"]:
            self.navigation["rules"][1]["include"].append(".concord/domain-navigation.v1.json")
        self.write(".concord/domain-navigation.v1.json", json.dumps(self.navigation))

    def findings(self):
        with patch.object(checker.knowledge_index, "compose_manifest", return_value=self.manifest):
            return checker.validate(self.root)[0]

    def test_unknown_domain_refuses(self):
        self.navigation["rules"][1]["domain_id"] = "unknown"
        self.save()
        self.assertTrue(self.findings())

    def test_overlap_refuses_without_precedence(self):
        self.navigation["rules"][0]["include"].append("src/**")
        self.save()
        self.assertTrue(self.findings())

    def test_new_unmapped_path_refuses(self):
        self.write("new.go", "package fixture\n")
        self.assertTrue(self.findings())

    def test_new_unresolved_path_refuses(self):
        self.write("new.go", "package fixture\n")
        self.navigation["unresolved"].append({"path": "new.go", "candidate_domain_ids": ["core"], "reason": "New uncertainty."})
        self.save()
        self.assertTrue(self.findings())

    def test_changed_legacy_unresolved_path_is_advisory(self):
        self.navigation["rules"][1]["exclude"].append("src/a.go")
        self.navigation["unresolved"].append({"path": "src/a.go", "candidate_domain_ids": ["core"], "reason": "Legacy uncertainty."})
        self.save()
        self.git("add", ".")
        self.git("commit", "-q", "-m", "legacy unresolved")
        self.write("src/a.go", "package fixture\n// Changed.\n")
        self.assertEqual(self.findings(), [])
        state = nav.partition(self.root, self.registry)
        self.assertIn("changed legacy unresolved path: src/a.go", " ".join(state["advisories"]))

    def test_legacy_unresolved_list_cannot_grow(self):
        self.navigation["rules"][1]["exclude"].append("src/a.go")
        self.navigation["unresolved"].append({"path": "src/a.go", "candidate_domain_ids": ["core"], "reason": "Unchanged but newly unresolved."})
        self.save()
        self.assertTrue(self.findings())

    def default_src(self, domain="core"):
        self.navigation["rules"][1]["include"] = ["src/a.go", ".concord/navigation/**"]
        self.navigation["default_rules"] = [{"domain_id": domain, "include": ["src/**"], "exclude": []}]
        self.save()

    def test_new_file_in_directory_uses_authored_default(self):
        self.default_src()
        self.write("src/new_test.go", "package fixture\n")
        self.assertEqual(nav.partition(self.root, self.registry)["owners"]["src/new_test.go"], "core")

    def test_explicit_rule_wins_over_directory_default(self):
        self.default_src("product-root:fixture")
        self.assertEqual(nav.partition(self.root, self.registry)["owners"]["src/a.go"], "core")

    def test_default_rule_order_never_decides_an_owner(self):
        self.default_src()
        self.navigation["default_rules"].append({"domain_id": "product-root:fixture", "include": ["src/**"], "exclude": []})
        self.write("src/new.go", "package fixture\n")
        self.save()
        with self.assertRaisesRegex(nav.NavigationError, "overlapping default navigation owners"):
            nav.partition(self.root, self.registry)

    def test_default_never_resolves_a_named_legacy_gap(self):
        self.default_src()
        self.navigation["rules"][1]["exclude"].append("src/a.go")
        self.navigation["unresolved"].append({"path": "src/a.go", "candidate_domain_ids": ["core"], "reason": "Legacy uncertainty."})
        self.save()
        self.git("add", ".")
        self.git("commit", "-q", "-m", "legacy unresolved")
        state = nav.partition(self.root, self.registry)
        self.assertNotIn("src/a.go", state["owners"])
        self.assertIn("src/a.go", state["unresolved"])

    def test_unmapped_new_path_names_navmap_style_remediation(self):
        self.write("new.go", "package fixture\n")
        with self.assertRaisesRegex(nav.NavigationError, "new.go.*navmap-style remediation"):
            nav.partition(self.root, self.registry)

    def test_default_requires_a_known_domain_and_existing_directory(self):
        for domain, pattern in (("unknown", "src/**"), ("core", "missing/**")):
            with self.subTest(domain=domain, pattern=pattern):
                self.navigation["default_rules"] = [{"domain_id": domain, "include": [pattern], "exclude": []}]
                self.save()
                self.assertTrue(self.findings())

    def test_future_test_pattern_uses_existing_directory_default(self):
        self.navigation["default_rules"] = [{"domain_id": "core", "include": ["src/**/*_test.go"], "exclude": []}]
        self.save()
        self.assertEqual(self.findings(), [])
        self.write("src/future_test.go", "package fixture\n")
        self.assertEqual(nav.partition(self.root, self.registry)["owners"]["src/future_test.go"], "core")

    def test_changed_gap_prints_advisory_even_in_strict_mode(self):
        self.registry["domains"][0]["architecture_relations"] = [
            {"kind": "depends_on", "target_domain_id": "core", "governing_law_ids": ["root-law"]}
        ]
        self.navigation["rules"][1]["exclude"].append("src/a.go")
        self.navigation["unresolved"].append({"path": "src/a.go", "candidate_domain_ids": ["core"], "reason": "Legacy uncertainty."})
        self.save()
        self.git("add", ".")
        self.git("commit", "-q", "-m", "legacy unresolved")
        self.write("src/a.go", "package fixture\n// Changed.\n")
        with patch.object(checker.knowledge_index, "compose_manifest", return_value=self.manifest):
            code, out, _ = self.invoke("check-domain-registry.py", "--strict")
        self.assertEqual(code, 0)
        self.assertIn("domain navigation advisory: changed legacy unresolved path: src/a.go", out)

    def test_dead_glob_refuses(self):
        self.navigation["rules"][1]["include"].append("missing/**")
        self.save()
        self.assertTrue(self.findings())

    def test_parent_selector_refuses(self):
        self.navigation["rules"][1]["include"].append("../outside/**")
        self.save()
        self.assertTrue(self.findings())

    def test_symlink_escape_refuses(self):
        (self.root / "src/escape.go").symlink_to(self.root.parent / "outside.go")
        self.assertTrue(self.findings())

    def test_valid_partition_passes(self):
        self.assertEqual(self.findings(), [])

    def test_foreign_product_without_companion_stays_optional(self):
        self.git("rm", "-q", ".concord/domain-navigation.v1.json")
        self.git("commit", "-q", "-m", "foreign product has no companion")
        self.assertEqual(self.findings(), [])

    def test_adopted_companion_cannot_be_removed(self):
        (self.root / ".concord/domain-navigation.v1.json").unlink()
        self.assertTrue(self.findings())

    def test_initial_adoption_may_name_unchanged_legacy_paths(self):
        self.git("rm", "--cached", "-q", nav.COMPANION)
        self.git("commit", "-q", "-m", "pre-adoption baseline")
        self.navigation["rules"][1]["exclude"].append("src/a.go")
        self.navigation["unresolved"].append({"path": "src/a.go", "candidate_domain_ids": ["core"], "reason": "Legacy uncertainty."})
        self.save()
        self.assertEqual(self.findings(), [])

    def test_unresolved_list_can_shrink(self):
        self.navigation["rules"][1]["exclude"].append("src/a.go")
        self.navigation["unresolved"].append({"path": "src/a.go", "candidate_domain_ids": ["core"], "reason": "Legacy uncertainty."})
        self.save()
        self.git("add", ".")
        self.git("commit", "-q", "-m", "legacy unresolved")
        self.navigation["rules"][1]["exclude"] = []
        self.navigation["unresolved"] = []
        self.save()
        self.assertEqual(self.findings(), [])

    def test_unresolved_file_mode_change_is_advisory(self):
        self.navigation["rules"][1]["exclude"].append("src/a.go")
        self.navigation["unresolved"].append({"path": "src/a.go", "candidate_domain_ids": ["core"], "reason": "Legacy uncertainty."})
        self.save()
        self.git("add", ".")
        self.git("commit", "-q", "-m", "legacy unresolved")
        (self.root / "src/a.go").chmod(0o755)
        self.assertEqual(self.findings(), [])
        self.assertTrue(nav.partition(self.root, self.registry)["advisories"])

    def test_rule_order_does_not_choose_an_owner(self):
        self.save()
        before = nav.partition(self.root, self.registry)["owners"]
        self.navigation["rules"].reverse()
        self.write(nav.COMPANION, json.dumps(self.navigation))
        self.assertEqual(nav.partition(self.root, self.registry)["owners"], before)

    def test_root_cannot_be_a_catch_all(self):
        self.navigation["rules"][0]["include"].append("**")
        self.save()
        self.assertTrue(self.findings())

    def test_duplicate_json_keys_refuse(self):
        self.write(nav.COMPANION, '{"schema_version":"1.0","schema_version":"1.0"}')
        self.assertIn("duplicate JSON key", " ".join(self.findings()))

    def test_unknown_shared_test_owner_refuses(self):
        self.write("src/a_test.go", "package fixture\n")
        self.navigation["test_coverage"] = [{"path": "src/a_test.go", "covers_owner_ids": ["domain:unknown"]}]
        self.save()
        self.assertTrue(self.findings())

    def test_shared_test_keeps_one_file_home(self):
        self.write("src/a_test.go", "package fixture\n")
        self.navigation["test_coverage"] = [{"path": "src/a_test.go", "covers_owner_ids": ["domain:core", "domain:product-root:fixture"]}]
        self.save()
        state = nav.partition(self.root, self.registry)
        self.assertEqual(state["owners"]["src/a_test.go"], "core")
        self.assertEqual(len(state["coverage"]["src/a_test.go"]), 2)

    def prepare_catalogs(self):
        self.navigation["rules"][1]["include"].extend([".concord/docs/**", ".concord/tooling.v1.json", "contracts/**", "cmd/**", "go.mod", "scripts/**"])
        self.save()
        self.write(nav.REGISTRY, json.dumps(self.registry))
        self.write(".concord/tooling.v1.json", json.dumps({"tools": [{"id": "fixture-check", "tier": "fast", "invocation": "python3 check.py"}]}))
        self.write("contracts/agent-tool-surface.v1.json", json.dumps({"tools": [{"id": "fixture", "operations": ["fixture.inspect"]}]}))
        self.write("cmd/concord/main.go", 'package fixture; var commandSpecs = []commandSpec{{Canonical: "inspect"}}')
        self.write("go.mod", "module example.invalid/fixture\n")
        for path in ("scripts/domain_navigation.py", "scripts/domain-navigation-cli/main.go"):
            self.write(path, "Fixture generator source\n")
        original = subprocess.run

        def run(argv, **kwargs):
            if argv[0] == "go":
                return subprocess.CompletedProcess(argv, 0, '{"verbs":[{"canonical":"inspect","two_word":""}],"early_dispatch":["launcher"]}', "")
            return original(argv, **kwargs)

        patched = patch.object(nav.subprocess, "run", side_effect=run)
        patched.start()
        self.addCleanup(patched.stop)

    def invoke(self, script, *args):
        spec = importlib.util.spec_from_file_location("navigation_command", SCRIPTS / script)
        command = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(command)
        out, err = io.StringIO(), io.StringIO()
        with patch.object(sys, "argv", [script, "--root", str(self.root), *args]), redirect_stdout(out), redirect_stderr(err):
            code = command.main()
        return code, out.getvalue(), err.getvalue()

    def test_generator_is_deterministic_and_cards_are_bounded(self):
        self.prepare_catalogs()
        first = nav.artifacts(self.root)
        self.assertEqual(first, nav.artifacts(self.root))
        for path, content in first.items():
            if path.endswith(".md"):
                self.assertIn(b"DO NOT EDIT", content)
                self.assertLessEqual(len(content), 8192)
                self.assertLessEqual(len(content.splitlines()), 120)
        data = json.loads(first[f"{nav.OUTPUT}/inventory.json"])
        self.assertEqual(len(data["files"]), data["mapped_count"])
        self.assertEqual(len(data["domains"]), 2)
        core = next(d for d in data["domains"] if d["domain_id"] == "core")
        self.assertEqual(core["agent_operations"], ["fixture.inspect"])
        self.assertEqual(core["cli_verbs"], [{"canonical": "inspect", "two_word": ""}])
        self.assertEqual(core["cli_early_dispatch"], ["launcher"])
        self.assertNotIn("mechanisms", data)
        self.assertNotIn("advisory_notes", data)
        self.assertEqual(data["schema_version"], "1.0")

    def test_mapping_change_leaves_other_domain_card_unchanged(self):
        self.prepare_catalogs()
        before = nav.artifacts(self.root)
        self.write("OTHER.md", "Product-wide fixture\n")
        self.navigation["rules"][0]["include"].append("OTHER.md")
        self.save()
        after = nav.artifacts(self.root)
        self.assertEqual(before[nav.card_path("core")], after[nav.card_path("core")])
        self.assertNotEqual(before[nav.card_path("product-root:fixture")], after[nav.card_path("product-root:fixture")])

    def test_committed_artifacts_have_no_per_change_fingerprints(self):
        self.prepare_catalogs()
        generated = nav.artifacts(self.root)
        data = json.loads(generated[f"{nav.OUTPUT}/inventory.json"])
        self.assertNotIn("source_fingerprints", data)
        self.assertNotIn("path_set_fingerprint", data)
        self.assertTrue(all(b"fingerprint" not in content for path, content in generated.items() if path.endswith(".md")))

    def test_check_rederives_content_without_rejecting_irrelevant_source_bytes(self):
        self.prepare_catalogs()
        self.assertEqual(self.invoke("generate-domain-navigation.py")[0], 0)
        self.write("cmd/concord/main.go", 'package fixture; var commandSpecs = []commandSpec{{Canonical: "inspect"}}\n// Comment only.\n')
        self.assertEqual(self.invoke("generate-domain-navigation.py", "--check")[0], 0)

    def test_missing_stale_extra_and_changed_source_artifacts_refuse(self):
        self.prepare_catalogs()
        self.assertEqual(self.invoke("generate-domain-navigation.py", "--check")[0], 1)
        self.assertEqual(self.invoke("generate-domain-navigation.py")[0], 0)
        self.assertEqual(self.invoke("generate-domain-navigation.py", "--check")[0], 0)
        self.write(nav.card_path("core"), "Tampered card\n")
        self.assertEqual(self.invoke("generate-domain-navigation.py", "--check")[0], 1)
        self.assertEqual(self.invoke("generate-domain-navigation.py")[0], 0)
        self.write(f"{nav.OUTPUT}/extra.md", "Unexpected artifact\n")
        self.assertEqual(self.invoke("generate-domain-navigation.py", "--check")[0], 1)
        (self.root / nav.OUTPUT / "extra.md").unlink()
        self.write("contracts/agent-tool-surface.v1.json", json.dumps({"tools": [{"id": "fixture", "operations": ["fixture.inspect", "fixture.update"]}]}))
        self.assertEqual(self.invoke("generate-domain-navigation.py", "--check")[0], 1)

    def test_card_filename_slug_preserves_domain_identity(self):
        self.prepare_catalogs()
        root_path = ".concord/navigation/domains/product-root--fixture.md"
        self.assertEqual(nav.card_path("product-root:fixture"), root_path)
        self.assertEqual(nav.card_path("core"), ".concord/navigation/domains/core.md")
        generated = nav.artifacts(self.root)
        self.assertTrue(all(":" not in Path(path).name for path in generated))
        self.assertIn(b"Domain: `product-root:fixture`", generated[root_path])

    def test_card_slug_collision_refuses(self):
        self.registry["domains"].append({
            "domain_id": "product-root--fixture", "name": "Collision", "purpose": "Fixture collision.",
            "status": "current", "architecture_relations": [],
        })
        with self.assertRaisesRegex(nav.NavigationError, "card filename collision"):
            nav.expected_paths(self.registry)

    def test_generator_reconciles_obsolete_owned_cards(self):
        self.prepare_catalogs()
        self.assertEqual(self.invoke("generate-domain-navigation.py")[0], 0)
        obsolete = f"{nav.OUTPUT}/domains/product-root:fixture.md"
        self.write(obsolete, f"<!-- {nav.GENERATED} -->\nDomain: `product-root:fixture`\n")
        self.assertEqual(self.invoke("generate-domain-navigation.py", "--check")[0], 1)
        self.assertEqual(self.invoke("generate-domain-navigation.py")[0], 0)
        self.assertFalse((self.root / obsolete).exists())
        self.assertTrue((self.root / nav.OUTPUT / "domains/product-root--fixture.md").is_file())
        inventory = json.loads((self.root / nav.OUTPUT / "inventory.json").read_text())
        self.assertNotIn(obsolete, inventory["files"])
        self.assertEqual(self.invoke("generate-domain-navigation.py", "--check")[0], 0)

    def test_generator_never_deletes_unowned_extras(self):
        self.prepare_catalogs()
        self.assertEqual(self.invoke("generate-domain-navigation.py")[0], 0)
        orphan = f"{nav.OUTPUT}/domains/orphan.md"
        self.write(orphan, f"<!-- {nav.GENERATED} -->\nObsolete generated card\n")
        unowned = f"{nav.OUTPUT}/domains/notes.md"
        self.write(unowned, "Authored notes must not be deleted.\n")
        self.assertEqual(self.invoke("generate-domain-navigation.py")[0], 1)
        self.assertTrue((self.root / orphan).exists())
        self.assertEqual((self.root / unowned).read_text(), "Authored notes must not be deleted.\n")

    def test_lookup_root_uses_slug_without_changing_domain_id(self):
        self.prepare_catalogs()
        for flags in (("--domain", "product-root:fixture"), ("--path", "ROOT.md")):
            with self.subTest(flags=flags):
                code, out, _ = self.invoke("domain-navigation.py", *flags)
                self.assertEqual(code, 0)
                self.assertEqual(json.loads(out)["domain_id"], "product-root:fixture")
                self.assertEqual(json.loads(out)["card_path"], ".concord/navigation/domains/product-root--fixture.md")

    def test_oversize_card_refuses_without_truncation(self):
        self.prepare_catalogs()
        self.registry["domains"][1]["purpose"] = "\u754c" * 3000
        self.write(nav.REGISTRY, json.dumps(self.registry))
        with self.assertRaisesRegex(nav.NavigationError, "card exceeds"):
            nav.artifacts(self.root)

    def test_lookup_returns_domain_card_and_explicit_unresolved(self):
        self.prepare_catalogs()
        code, out, _ = self.invoke("domain-navigation.py", "--path", "src/a.go")
        self.assertEqual(code, 0)
        self.assertEqual(json.loads(out)["domain_id"], "core")
        code, out, _ = self.invoke("domain-navigation.py", "--domain", "core")
        self.assertEqual(code, 0)
        self.assertEqual(json.loads(out)["card_path"], nav.card_path("core"))
        self.git("rm", "--cached", "-q", nav.COMPANION)
        self.git("commit", "-q", "-m", "pre-adoption baseline")
        self.navigation["rules"][1]["exclude"].append("src/a.go")
        self.navigation["unresolved"].append({"path": "src/a.go", "candidate_domain_ids": ["core"], "reason": "Legacy uncertainty."})
        self.save()
        code, out, _ = self.invoke("domain-navigation.py", "--path", "src/a.go")
        self.assertEqual(code, 0)
        self.assertEqual(json.loads(out)["status"], "unresolved")

    def test_lookup_rejects_escape_unknown_path_and_unknown_domain(self):
        for flags in (("--path", "../outside.go"), ("--path", "missing.go"), ("--domain", "unknown")):
            with self.subTest(flags=flags):
                self.assertEqual(self.invoke("domain-navigation.py", *flags)[0], 1)


class NavigationSemanticsTests(unittest.TestCase):
    """schema_version 1.1 adds closed semantic machinery over the 1.0 partition."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.registry = {
            "schema_version": "1.0", "product_key": "fixture", "root_domain_id": "product-root:fixture",
            "domains": [
                {"domain_id": "product-root:fixture", "name": "Fixture", "purpose": "Product-wide constraints.", "status": "current", "architecture_relations": []},
                {"domain_id": "core", "name": "Core", "purpose": "Fixture implementation.", "status": "current", "parent_domain_id": "product-root:fixture",
                 "architecture_relations": [{"kind": "depends_on", "target_domain_id": "product-root:fixture", "governing_law_ids": ["root-law"]}]},
            ],
        }
        self.manifest = {"domain_registry": self.registry, "records": [
            {"id": "root-law", "status": "accepted", "home_domain_id": "product-root:fixture", "kind": "decision",
             "path": ".concord/docs/decisions/root-law.md"},
            {"id": "core-law", "status": "accepted", "home_domain_id": "core", "kind": "spec",
             "path": ".concord/docs/specs/core-law.md"},
            {"id": "core-note", "status": "published", "home_domain_id": "core", "kind": "lesson",
             "path": ".concord/docs/lessons/core-note.md"},
        ]}
        self.mechanism = {
            "owner_id": "mechanism:fixture-engine", "domain_id": "core",
            "responsibility": "Owns fixture navigation semantics.",
            "source_paths": ["src/a.go"], "contract_refs": ["src/contract.go"],
            "law_refs": [{"law_id": "core-law", "clause_id": "Decision"}],
            "control_law_ids": ["core-law"], "check_ids": ["fixture-check"],
        }
        self.navigation = {
            "schema_version": "1.1",
            "rules": [
                {"domain_id": "product-root:fixture", "include": ["ROOT.md"], "exclude": []},
                {"domain_id": "core", "include": ["src/**", ".concord/**", "contracts/**", "cmd/**", "go.mod", "scripts/**"], "exclude": []},
            ],
            "unresolved": [], "test_coverage": [],
            "mechanisms": [self.mechanism],
            "catalog_bindings": [
                {"catalog": "cli", "entry_id": "inspect", "owner_id": "mechanism:fixture-engine"},
                {"catalog": "cli", "entry_id": "launcher", "owner_id": "mechanism:fixture-engine"},
                {"catalog": "agent", "entry_id": "fixture.inspect", "owner_id": "mechanism:fixture-engine"},
                {"catalog": "workflow", "entry_id": "fixture.action", "owner_id": "mechanism:fixture-engine"},
            ],
            "dependency_interpretations": [{
                "source_domain_id": "core", "kind": "depends_on", "target_domain_id": "product-root:fixture",
                "law_refs": [{"law_id": "root-law", "clause_id": "D1. Root holds"}], "contract_refs": [],
            }],
        }
        self.write("ROOT.md", "Fixture\n")
        self.write("src/a.go", "package fixture\n")
        self.write("src/contract.go", "package fixture\n")
        self.write(".concord/docs/decisions/root-law.md",
                   "# Root law\n\n## Decision\n\nText.\n\n### D1. Root holds\n\nText.\n\n## Acceptance Criteria\n\n```gherkin\nScenario: root begins an item\n  Given a current release\n  When the agent begins work\n  Then the item opens\n```\n")
        self.write(".concord/docs/specs/core-law.md", "# Core law\n\n## Decision\n\nText.\n\n## Acceptance Criteria\n\n```gherkin\nScenario: core holds a boundary\n  Given a fixture\n  When the boundary is read\n  Then the clause resolves\n```\n")
        self.write(".concord/docs/lessons/core-note.md", "# Core note\n\nA lesson is not law.\n")
        self.write(".concord/docs/knowledge/coverage/root-law.json",
                   json.dumps({"id": "root-law", "state": "outstanding", "issue": "CON-1"}))
        self.write(".concord/docs/knowledge/coverage/core-law.json",
                   json.dumps({"id": "core-law", "state": "satisfied",
                               "evidence": [{"kind": "validator", "value": "scripts/check-fixture.py"}]}))
        self.save()
        self.write(nav.REGISTRY, json.dumps(self.registry))
        self.write(".concord/tooling.v1.json", json.dumps({"tools": [{"id": "fixture-check", "tier": "fast", "invocation": "python3 check.py"}]}))
        self.write("contracts/agent-tool-surface.v1.json", json.dumps({"tools": [{"id": "fixture", "operations": ["fixture.inspect"]}]}))
        self.write("cmd/concord/main.go", 'package fixture; var commandSpecs = []commandSpec{{Canonical: "inspect"}}')
        self.write("go.mod", "module example.invalid/fixture\n")
        for path in ("scripts/domain_navigation.py", "scripts/domain-navigation-cli/main.go",
                     "scripts/knowledge_index.py", "scripts/workflow-action-contracts/main.go"):
            self.write(path, "Fixture generator source\n")
        self.git("init", "-q", "-b", "main")
        self.git("add", ".")
        self.git("commit", "-q", "-m", "fixture")
        original = subprocess.run

        def run(argv, **kwargs):
            if argv[0] == "go":
                if any("workflow-action-contracts" in part for part in argv):
                    return subprocess.CompletedProcess(argv, 0, '{"schema_version":"1.0","actions":[{"id":"fixture.action"}],"workflows":[]}', "")
                return subprocess.CompletedProcess(argv, 0, '{"verbs":[{"canonical":"inspect","two_word":""}],"early_dispatch":["launcher"]}', "")
            return original(argv, **kwargs)

        patched = patch.object(nav.subprocess, "run", side_effect=run)
        patched.start()
        self.addCleanup(patched.stop)

    def git(self, *args):
        env = dict(os.environ, GIT_AUTHOR_NAME="Fixture", GIT_AUTHOR_EMAIL="fixture@example.invalid",
                   GIT_COMMITTER_NAME="Fixture", GIT_COMMITTER_EMAIL="fixture@example.invalid")
        return subprocess.run(["git", "-C", str(self.root), *args], env=env, check=True, capture_output=True, text=True).stdout

    def write(self, path, text):
        target = self.root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(text)

    def save(self):
        if ".concord/domain-navigation.v1.json" not in self.navigation["rules"][1]["include"]:
            self.navigation["rules"][1]["include"].append(".concord/domain-navigation.v1.json")
        self.write(".concord/domain-navigation.v1.json", json.dumps(self.navigation))

    def findings(self):
        with patch.object(checker.knowledge_index, "compose_manifest", return_value=self.manifest):
            return checker.validate(self.root)[0]

    def state(self):
        with patch.object(nav.knowledge_index, "compose_manifest", return_value=self.manifest):
            return nav.partition(self.root, self.registry)

    def inventory(self):
        with patch.object(nav.knowledge_index, "compose_manifest", return_value=self.manifest):
            return nav.inventory(self.root, self.registry)

    def artifacts(self):
        with patch.object(nav.knowledge_index, "compose_manifest", return_value=self.manifest):
            return nav.artifacts(self.root)

    def test_v11_companion_validates(self):
        self.assertEqual(self.findings(), [])

    def test_v11_unknown_version_refuses(self):
        self.navigation["schema_version"] = "2.0"
        self.save()
        self.assertTrue(self.findings())

    def test_v11_missing_semantic_field_refuses(self):
        del self.navigation["mechanisms"]
        self.save()
        self.assertTrue(self.findings())

    def test_v11_extra_field_refuses(self):
        self.navigation["mechanisms"][0]["weight"] = 1
        self.save()
        self.assertTrue(self.findings())

    def test_v11_unresolved_must_be_empty(self):
        self.navigation["unresolved"].append({"path": "src/a.go", "candidate_domain_ids": ["core"], "reason": "Not allowed at 1.1."})
        self.save()
        self.assertTrue(self.findings())

    def test_v11_unknown_law_ref_refuses(self):
        self.mechanism["law_refs"] = [{"law_id": "missing-law", "clause_id": "Decision"}]
        self.save()
        self.assertTrue(self.findings())

    def test_v11_non_law_record_refuses(self):
        self.mechanism["law_refs"] = [{"law_id": "core-note", "clause_id": "Decision"}]
        self.save()
        self.assertTrue(self.findings())

    def test_v11_absent_clause_refuses(self):
        self.mechanism["law_refs"] = [{"law_id": "core-law", "clause_id": "No Such Heading"}]
        self.save()
        self.assertTrue(self.findings())

    def test_v11_scenario_clause_resolves(self):
        self.mechanism["law_refs"] = [{"law_id": "core-law", "clause_id": "core holds a boundary"}]
        self.save()
        self.assertEqual(self.findings(), [])

    def test_v11_scenario_outside_a_fence_is_not_a_criterion(self):
        self.write(".concord/docs/specs/core-law.md", "# Core law\n\nScenario: unwritten outside a fence\n")
        self.mechanism["law_refs"] = [{"law_id": "core-law", "clause_id": "unwritten outside a fence"}]
        self.save()
        self.assertTrue(self.findings())

    def test_v11_heading_inside_a_fence_is_not_an_anchor(self):
        self.write(".concord/docs/specs/core-law.md", "# Core law\n\n```\n## Fenced Heading\n```\n")
        self.mechanism["law_refs"] = [{"law_id": "core-law", "clause_id": "Fenced Heading"}]
        self.save()
        self.assertTrue(self.findings())

    def test_v11_shared_contract_interpretation_requires_contract_refs(self):
        self.registry["domains"][1]["architecture_relations"].append(
            {"kind": "shares_contract_with", "target_domain_id": "product-root:fixture", "governing_law_ids": ["root-law"]})
        self.write(nav.REGISTRY, json.dumps(self.registry))
        self.navigation["dependency_interpretations"].append({
            "source_domain_id": "core", "kind": "shares_contract_with", "target_domain_id": "product-root:fixture",
            "law_refs": [{"law_id": "root-law", "clause_id": "D1. Root holds"}], "contract_refs": [],
        })
        self.save()
        self.assertTrue(self.findings())
        self.navigation["dependency_interpretations"][1]["contract_refs"] = ["src/contract.go"]
        self.save()
        self.assertEqual(self.findings(), [])

    def test_v11_unknown_control_law_refuses(self):
        self.mechanism["control_law_ids"] = ["missing-law"]
        self.save()
        self.assertTrue(self.findings())

    def test_v11_unknown_check_refuses(self):
        self.mechanism["check_ids"] = ["missing-check"]
        self.save()
        self.assertTrue(self.findings())

    def test_v11_wildcard_source_path_refuses(self):
        self.mechanism["source_paths"] = ["src/**"]
        self.save()
        self.assertTrue(self.findings())

    def test_v11_missing_source_path_refuses(self):
        self.mechanism["contract_refs"] = ["src/missing.go"]
        self.save()
        self.assertTrue(self.findings())

    def test_v11_oversized_responsibility_refuses(self):
        self.mechanism["responsibility"] = "x" * 513
        self.save()
        self.assertTrue(self.findings())

    def test_v11_malformed_owner_id_refuses(self):
        self.mechanism["owner_id"] = "fixture-engine"
        self.save()
        self.assertTrue(self.findings())

    def test_v11_duplicate_owner_id_refuses(self):
        self.navigation["mechanisms"].append(dict(self.mechanism))
        self.save()
        self.assertTrue(self.findings())

    def test_v11_unknown_domain_refuses(self):
        self.mechanism["domain_id"] = "unknown"
        self.save()
        self.assertTrue(self.findings())

    def test_v11_binding_unknown_mechanism_refuses(self):
        self.navigation["catalog_bindings"][0]["owner_id"] = "mechanism:missing"
        self.save()
        self.assertTrue(self.findings())

    def test_v11_binding_bad_catalog_refuses(self):
        self.navigation["catalog_bindings"][0]["catalog"] = "tui"
        self.save()
        self.assertTrue(self.findings())

    def test_v11_binding_duplicate_entry_refuses(self):
        self.navigation["catalog_bindings"].append(dict(self.navigation["catalog_bindings"][0]))
        self.save()
        self.assertTrue(self.findings())

    def test_v11_binding_unknown_entry_refuses(self):
        self.navigation["catalog_bindings"][0]["entry_id"] = "nope"
        self.save()
        with self.assertRaisesRegex(nav.NavigationError, "unknown catalog entry"):
            self.inventory()

    def test_v11_binding_uncovered_entries_refuse(self):
        self.navigation["catalog_bindings"] = [b for b in self.navigation["catalog_bindings"] if b["entry_id"] != "launcher"]
        self.save()
        with self.assertRaisesRegex(nav.NavigationError, "uncovered catalog entries"):
            self.inventory()

    def test_v11_interpretation_tuple_must_exist(self):
        self.navigation["dependency_interpretations"][0]["kind"] = "shares_contract_with"
        self.save()
        self.assertTrue(self.findings())

    def test_v11_interpretation_governing_set_must_match(self):
        self.navigation["dependency_interpretations"][0]["law_refs"] = [
            {"law_id": "root-law", "clause_id": "D1. Root holds"}, {"law_id": "core-law", "clause_id": "Decision"}]
        self.save()
        self.assertTrue(self.findings())

    def test_v11_interpretation_absent_clause_refuses(self):
        self.navigation["dependency_interpretations"][0]["law_refs"] = [{"law_id": "root-law", "clause_id": "Missing"}]
        self.save()
        self.assertTrue(self.findings())

    def test_v11_inventory_emits_resolved_semantics(self):
        data = self.inventory()
        self.assertEqual(data["schema_version"], "1.1")
        mechanism = data["mechanisms"][0]
        self.assertEqual(mechanism["owner_id"], "mechanism:fixture-engine")
        law = mechanism["law_refs"][0]
        self.assertEqual(law["locator"], ".concord/docs/specs/core-law.md")
        self.assertEqual(law["content_hash"], nav.sha((self.root / ".concord/docs/specs/core-law.md").read_bytes()))
        self.assertNotIn("text", law)
        control = mechanism["controls"][0]
        self.assertEqual(control["state"], "satisfied")
        self.assertEqual(control["evidence"], [{"kind": "validator", "value": "scripts/check-fixture.py"}])
        self.assertEqual(data["catalog_sizes"], {"agent": 1, "cli": 2, "workflow": 1})
        core = next(d for d in data["domains"] if d["domain_id"] == "core")
        self.assertEqual(core["mechanism_owner_ids"], ["mechanism:fixture-engine"])
        self.assertEqual(sorted(core["bound_entries"]["cli"]), ["inspect", "launcher"])
        interpretation = data["dependency_interpretations"][0]
        self.assertEqual(interpretation["law_refs"][0]["locator"], ".concord/docs/decisions/root-law.md")
        self.assertTrue(any("dispatch admission" in note for note in data["advisory_notes"]))
        self.assertNotIn("source_fingerprints", data)
        self.assertNotIn("path_set_fingerprint", data)

    def test_v11_outstanding_control_retains_issue(self):
        self.mechanism["control_law_ids"] = ["root-law"]
        self.save()
        control = self.inventory()["mechanisms"][0]["controls"][0]
        self.assertEqual(control["state"], "outstanding")
        self.assertEqual(control["issue"], "CON-1")
        self.assertNotIn("evidence", control)

    def test_v11_cards_stay_bounded_and_link_inventory(self):
        generated = self.artifacts()
        for path, content in generated.items():
            if path.endswith(".md"):
                self.assertLessEqual(len(content), 8192)
                self.assertLessEqual(len(content.splitlines()), 120)
                self.assertIn(b"inventory.json", content)
        core_card = generated[nav.card_path("core")].decode()
        self.assertIn("`mechanism:fixture-engine`", core_card)
        self.assertIn("dispatch admission", core_card)
        root_card = generated[nav.card_path("product-root:fixture")].decode()
        self.assertIn("depends_on", root_card)

    def test_v11_semantic_state_is_deterministic(self):
        self.assertEqual(self.state()["semantics"]["mechanisms"], self.inventory()["mechanisms"])


class SegmentPatternTests(unittest.TestCase):
    def test_single_segment_wildcards_never_cross_a_slash(self):
        # Exhaust a finite domain rather than sample only the successful paths.
        from itertools import product
        for name in ("a", "ab", ".hidden"):
            for parts in product(("a", "b", ".hidden"), repeat=2):
                path = "/".join(parts) + "/" + name
                self.assertFalse(nav.matches(path, "*"))
                self.assertTrue(nav.matches(path, "**/*"))
                self.assertEqual(nav.matches(path, "a/*/*"), parts[0] == "a")

    def test_recursive_segment_matches_zero_or_more_segments(self):
        for path in ("a.go", "src/a.go", "src/deep/a.go", ".hidden/a.go"):
            self.assertTrue(nav.matches(path, "**/a.go"))
        self.assertFalse(nav.matches("src/b.go", "**/a.go"))
        self.assertFalse(nav.matches("SRC/a.go", "src/**"))
        self.assertTrue(nav.matches("src/a.go", "src/?.go"))


if __name__ == "__main__":
    unittest.main()

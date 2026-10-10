#!/usr/bin/env python3
"""Focused tests for Domain registry referential validation."""

from __future__ import annotations

import importlib.util
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = Path(__file__).with_name("check-domain-registry.py")
SPEC = importlib.util.spec_from_file_location("check_domain_registry", SCRIPT)
assert SPEC and SPEC.loader
checker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(checker)


class DomainRegistryReferentialTests(unittest.TestCase):
    def setUp(self) -> None:
        scratch = tempfile.TemporaryDirectory()
        self.addCleanup(scratch.cleanup)
        self.root = Path(scratch.name)

    @staticmethod
    def manifest(
        domain_ids: list[str],
        relations: dict[str, list[str]] | None = None,
        laws: dict[str, list[str]] | None = None,
    ) -> dict:
        relations = relations or {}
        laws = laws or {}
        domains = []
        records = []
        for domain_id in domain_ids:
            domains.append(
                {
                    "domain_id": domain_id,
                    "parent_domain_id": domain_ids[0] if domain_id != domain_ids[0] else None,
                    "architecture_relations": [
                        {"target_domain_id": target, "governing_law_ids": laws.get(f"{domain_id}->{target}", [])}
                        for target in relations.get(domain_id, [])
                    ],
                }
            )
            records.append({"id": f"law-{domain_id}", "status": "accepted", "home_domain_id": domain_id})
        return {"domain_registry": {"root_domain_id": domain_ids[0], "domains": domains}, "records": records}

    def check(self, manifest: dict) -> list[str]:
        with patch.object(checker.knowledge_index, "compose_manifest", return_value=manifest):
            return checker.validate(self.root)

    def run_main(self, manifest: dict, *extra_arguments: str) -> int:
        with (
            patch.object(checker.knowledge_index, "compose_manifest", return_value=manifest),
            patch.object(sys, "argv", [str(SCRIPT), "--root", str(self.root), *extra_arguments]),
        ):
            return checker.main()

    def test_a_clean_multi_domain_registry_passes(self) -> None:
        findings = self.check(self.manifest(["root", "child"], {"root": ["child"]}))

        self.assertEqual(findings, [])
        self.assertEqual(self.run_main(self.manifest(["root", "child"], {"root": ["child"]})), 0)

    def test_a_domain_that_owns_no_law_record_fails(self) -> None:
        manifest = self.manifest(["root", "child"])
        manifest["records"] = [record for record in manifest["records"] if record["id"] != "law-child"]

        findings = self.check(manifest)

        self.assertEqual(len(findings), 1, findings)
        self.assertIn("domain child: declared but owns no law record", findings[0])
        self.assertEqual(self.run_main(manifest), 1)

    def test_an_unknown_home_or_applicability_domain_fails(self) -> None:
        manifest = self.manifest(["root"])
        manifest["records"].append(
            {
                "id": "law-root",
                "status": "accepted",
                "home_domain_id": "ghost",
                "applies_to_domain_ids": ["phantom"],
            }
        )

        findings = self.check(manifest)

        self.assertEqual(len(findings), 2, findings)
        self.assertIn("home_domain_id 'ghost' is not a declared Domain", findings[0])
        self.assertIn("applies_to_domain_ids names undeclared Domain 'phantom'", findings[1])

    def test_applicability_restatating_the_home_fails(self) -> None:
        manifest = self.manifest(["root"])
        manifest["records"][0]["applies_to_domain_ids"] = ["root"]

        findings = self.check(manifest)

        self.assertEqual(len(findings), 1, findings)
        self.assertIn("restates its home 'root'", findings[0])
        self.assertIn("CD-0041 D3", findings[0])

    def test_a_relation_to_an_undeclared_domain_fails(self) -> None:
        findings = self.check(self.manifest(["root"], {"root": ["ghost"]}))

        self.assertEqual(len(findings), 1, findings)
        self.assertIn("relation 0: target 'ghost' is not a declared Domain", findings[0])

    def test_a_domain_cannot_relate_to_itself(self) -> None:
        findings = self.check(self.manifest(["root"], {"root": ["root"]}))

        self.assertEqual(len(findings), 1, findings)
        self.assertIn("relation 0: a Domain cannot declare a relation to itself", findings[0])

    def test_a_relation_law_must_be_current(self) -> None:
        laws = {"root->child": ["law-retired"]}
        manifest = self.manifest(["root", "child"], {"root": ["child"]}, laws)

        findings = self.check(manifest)

        self.assertEqual(len(findings), 1, findings)
        self.assertIn("governing_law_ids names 'law-retired', which is not a current law record", findings[0])

    def test_the_root_has_no_parent_and_children_parent_the_root(self) -> None:
        manifest = self.manifest(["root", "child"], {"root": ["child"]})
        manifest["domain_registry"]["domains"][0]["parent_domain_id"] = "child"
        manifest["domain_registry"]["domains"][1]["parent_domain_id"] = "ghost"

        findings = self.check(manifest)

        self.assertEqual(len(findings), 2, findings)
        self.assertIn("the root Domain must not declare a parent", findings[0])
        self.assertIn("parent_domain_id must be the root 'root', found 'ghost'", findings[1])


if __name__ == "__main__":
    unittest.main()

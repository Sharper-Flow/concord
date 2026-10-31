#!/usr/bin/env python3
"""Focused tests for Domain registry referential validation."""

from __future__ import annotations

import importlib.util
import json
import re
import shutil
import sys
import tempfile
import unittest
from pathlib import Path


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

    def write_registry(self, registry: dict) -> None:
        knowledge = self.root / ".concord/docs/knowledge"
        knowledge.mkdir(parents=True, exist_ok=True)
        (knowledge / "domain-registry.json").write_text(
            json.dumps(registry, indent=2) + "\n", encoding="utf-8"
        )

    def write_law(self, relative: Path, record: dict) -> None:
        path = self.root / ".concord/law" / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        front = json.dumps(record, indent=2)
        path.write_text(f"---\n{front}\n---\n\n# body\n", encoding="utf-8")

    def tree(
        self,
        domain_ids: list[str],
        relations: dict[str, list[str]] | None = None,
        laws: dict[str, list[str]] | None = None,
        records: list[dict] | None = None,
    ) -> dict:
        relations = relations or {}
        laws = laws or {}
        domains = []
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
        self.write_registry({"root_domain_id": domain_ids[0], "domains": domains})
        law_records = records if records is not None else [
            {"id": f"law-{domain_id}", "home_domain_id": domain_id} for domain_id in domain_ids
        ]
        for position, record in enumerate(law_records):
            self.write_law(Path(f"decisions/CD-{position:04d}-record.md"), record)
        return {"root_domain_id": domain_ids[0], "domains": domains, "records": law_records}

    def check(self) -> list[str]:
        return checker.validate(self.root)

    def test_a_clean_multi_domain_registry_passes(self) -> None:
        self.tree(["root", "child"], {"root": ["child"]})

        self.assertEqual(self.check(), [])

    def test_domains_need_no_relation_for_navigation(self) -> None:
        self.tree(["root", "child"])

        self.assertEqual(self.check(), [])

    def test_a_subdomain_can_have_a_non_root_parent(self) -> None:
        self.tree(["root", "parent", "leaf"])
        registry = json.loads(
            (self.root / ".concord/docs/knowledge/domain-registry.json").read_text()
        )
        registry["domains"][2]["parent_domain_id"] = "parent"
        self.write_registry(registry)

        self.assertEqual(self.check(), [])

    def test_subdomain_cycles_fail(self) -> None:
        self.tree(["root", "parent", "leaf"])
        registry = json.loads(
            (self.root / ".concord/docs/knowledge/domain-registry.json").read_text()
        )
        registry["domains"][1]["parent_domain_id"] = "leaf"
        registry["domains"][2]["parent_domain_id"] = "parent"
        self.write_registry(registry)

        findings = self.check()

        self.assertEqual(len(findings), 2, findings)
        self.assertTrue(all("parent hierarchy contains a cycle" in finding for finding in findings))

    def test_package_guidance_reaches_an_entry_invariant_and_tests(self) -> None:
        instructions = (ROOT / "AGENTS.md").read_text()
        pointer = next(line for line in instructions.splitlines() if "Package and seam ownership" in line)
        self.assertIn(".concord/docs/core-architecture.md", pointer)
        architecture = (ROOT / ".concord/docs/core-architecture.md").read_text()
        section = architecture.split("## 4. Entry points by responsibility\n", 1)[1].split("\n## 5.", 1)[0]
        rows = [line for line in section.splitlines() if line.startswith("| ")]
        self.assertLessEqual(len(rows) - 1, 12)
        task = "SQLite current-state and effect-receipt authority"
        owner = next(line for line in rows if line.startswith(f"| {task} |"))
        columns = owner.split("|")
        for entry in re.findall(r"`([^`]+)`", columns[2]):
            self.assertTrue((ROOT / entry).is_file(), entry)
        note = (ROOT / "internal/store/doc.go").read_text()
        self.assertIn("SetMaxOpenConns(1)", note)
        self.assertIn("tx-scoped core", note)
        self.assertLessEqual(len(note.splitlines()), 60)
        for test_file in re.findall(r"`([^`]+)`", columns[3]):
            self.assertTrue((ROOT / "internal/store" / test_file).is_file(), test_file)

    def test_a_new_package_file_needs_no_global_catalog_update(self) -> None:
        shutil.copytree(ROOT / ".concord/docs/knowledge", self.root / ".concord/docs/knowledge")
        shutil.copytree(ROOT / ".concord/law", self.root / ".concord/law")
        before = (self.root / ".concord/docs/knowledge/domain-registry.json").read_text()
        self.assertEqual(self.check(), [])
        package = self.root / "internal/store"
        package.mkdir(parents=True)
        (package / "synthetic_owner.go").write_text("package store\n")
        self.assertEqual(
            (self.root / ".concord/docs/knowledge/domain-registry.json").read_text(), before
        )
        self.assertEqual(self.check(), [])
        self.assertFalse((self.root / ".concord/domain-navigation.v1.json").exists())
        self.assertFalse((self.root / ".concord/navigation").exists())

    def test_a_domain_that_owns_no_law_record_fails(self) -> None:
        records = [
            {"id": "law-root", "home_domain_id": "root"},
            {"id": "law-child", "home_domain_id": "child"},
        ]
        self.tree(["root", "child"], records=records)
        # Retire the child's law: folder membership supplies law status, so
        # deleting the file retires the record.
        (self.root / ".concord/law/decisions/CD-0001-record.md").unlink()

        findings = self.check()

        self.assertEqual(len(findings), 1, findings)
        self.assertIn("domain child: declared but owns no law record", findings[0])

    def test_an_unknown_home_or_applicability_domain_fails(self) -> None:
        self.tree(
            ["root"],
            records=[
                {"id": "law-root", "home_domain_id": "root"},
                {
                    "id": "law-root-ghost",
                    "home_domain_id": "ghost",
                    "applies_to_domain_ids": ["phantom"],
                },
            ],
        )

        findings = self.check()

        self.assertEqual(len(findings), 2, findings)
        self.assertIn("home_domain_id 'ghost' is not a declared Domain", findings[0])
        self.assertIn("applies_to_domain_ids names undeclared Domain 'phantom'", findings[1])

    def test_applicability_restatating_the_home_fails(self) -> None:
        self.tree(
            ["root"],
            records=[
                {
                    "id": "law-root",
                    "home_domain_id": "root",
                    "applies_to_domain_ids": ["root"],
                }
            ],
        )

        findings = self.check()

        self.assertEqual(len(findings), 1, findings)
        self.assertIn("restates its home 'root'", findings[0])
        self.assertIn("CD-0222 D3", findings[0])

    def test_a_relation_to_an_undeclared_domain_fails(self) -> None:
        self.tree(["root"], {"root": ["ghost"]})

        findings = self.check()

        self.assertEqual(len(findings), 1, findings)
        self.assertIn("relation 0: target 'ghost' is not a declared Domain", findings[0])

    def test_a_domain_cannot_relate_to_itself(self) -> None:
        self.tree(["root"], {"root": ["root"]})

        findings = self.check()

        self.assertEqual(len(findings), 1, findings)
        self.assertIn("relation 0: a Domain cannot declare a relation to itself", findings[0])

    def test_a_relation_law_must_be_current(self) -> None:
        self.tree(["root", "child"], {"root": ["child"]}, {"root->child": ["law-retired"]})

        findings = self.check()

        self.assertEqual(len(findings), 1, findings)
        self.assertIn("governing_law_ids names 'law-retired', which is not a current law record", findings[0])

    def test_the_root_has_no_parent_and_child_parents_must_resolve(self) -> None:
        self.tree(["root", "child"], {"root": ["child"]})
        registry = json.loads(
            (self.root / ".concord/docs/knowledge/domain-registry.json").read_text()
        )
        registry["domains"][0]["parent_domain_id"] = "child"
        registry["domains"][1]["parent_domain_id"] = "ghost"
        self.write_registry(registry)

        findings = self.check()

        self.assertEqual(len(findings), 2, findings)
        self.assertIn("the root Domain must not declare a parent", findings[0])
        self.assertIn("parent_domain_id 'ghost' is not a declared Domain", findings[1])

    def test_a_law_file_without_frontmatter_is_a_named_finding(self) -> None:
        self.tree(["root", "child"], {"root": ["child"]})
        (self.root / ".concord/law/decisions/CD-0000-record.md").write_text(
            "# no frontmatter\n", encoding="utf-8"
        )

        findings = self.check()

        self.assertEqual(
            [finding for finding in findings if "frontmatter" in finding],
            [".concord/law/decisions/CD-0000-record.md: no closed --- JSON frontmatter block"],
            findings,
        )


if __name__ == "__main__":
    unittest.main()

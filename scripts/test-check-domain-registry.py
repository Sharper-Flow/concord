#!/usr/bin/env python3
"""Focused tests for Domain registry participation validation."""

from __future__ import annotations

import importlib.util
import sys
import unittest
from pathlib import Path
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = Path(__file__).with_name("check-domain-registry.py")
SPEC = importlib.util.spec_from_file_location("check_domain_registry", SCRIPT)
assert SPEC and SPEC.loader
checker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(checker)


class DomainRegistryParticipationTests(unittest.TestCase):
    @staticmethod
    def manifest(domain_ids: list[str], relations: dict[str, list[str]] | None = None) -> dict:
        relations = relations or {}
        domains = []
        records = []
        for domain_id in domain_ids:
            domains.append(
                {
                    "domain_id": domain_id,
                    "parent_domain_id": domain_ids[0] if domain_id != domain_ids[0] else None,
                    "architecture_relations": [
                        {"target_domain_id": target, "governing_law_ids": []}
                        for target in relations.get(domain_id, [])
                    ],
                }
            )
            records.append({"id": f"law-{domain_id}", "status": "accepted", "home_domain_id": domain_id})
        return {"domain_registry": {"root_domain_id": domain_ids[0], "domains": domains}, "records": records}

    def check(self, manifest: dict) -> tuple[list[str], list[str]]:
        with patch.object(checker.knowledge_index, "compose_manifest", return_value=manifest):
            return checker.validate(ROOT)

    def test_orphan_domain_is_advisory_without_strict(self) -> None:
        findings, participation = self.check(self.manifest(["root", "child"]))

        self.assertEqual(findings, [])
        self.assertEqual(len(participation), 2)
        with patch.object(checker.knowledge_index, "compose_manifest", return_value=self.manifest(["root", "child"])), patch.object(
            sys, "argv", [str(SCRIPT)]
        ):
            self.assertEqual(checker.main(), 0)
        with patch.object(checker.knowledge_index, "compose_manifest", return_value=self.manifest(["root", "child"])), patch.object(
            sys, "argv", [str(SCRIPT), "--strict"]
        ):
            self.assertEqual(checker.main(), 1)

    def test_target_only_domain_participates(self) -> None:
        findings, participation = self.check(
            self.manifest(["root", "sink"], {"root": ["sink"]})
        )

        self.assertEqual(findings, [])
        self.assertEqual(participation, [])

    def test_single_domain_needs_no_relation(self) -> None:
        findings, participation = self.check(self.manifest(["root"]))

        self.assertEqual(findings, [])
        self.assertEqual(participation, [])


if __name__ == "__main__":
    unittest.main()

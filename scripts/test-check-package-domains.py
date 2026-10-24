#!/usr/bin/env python3
"""Focused tests for the package-to-Domain map validator."""

from __future__ import annotations

import importlib.util
import json
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

SCRIPT = Path(__file__).with_name("check-package-domains.py")
SPEC = importlib.util.spec_from_file_location("check_package_domains", SCRIPT)
assert SPEC and SPEC.loader
checker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(checker)

ROOT_DOMAIN = "product-root:demo"
FILES = [
    "cmd/demo/main.go",
    "internal/store/store.go",
    "internal/store/storetest/fixture.go",
    "scripts/check.py",
    "README.md",
]
PACKAGES = ["cmd/demo", "internal/store", "internal/store/storetest"]


def entry(path: str, *domain_ids: str) -> dict:
    return {"domain_ids": sorted(domain_ids), "path": path}


def clean_entries() -> list[dict]:
    return [
        entry("cmd/demo", "surface"),
        entry("internal/store", "storage"),
        entry("internal/store/storetest", "storage"),
        entry("scripts", "verification"),
    ]


def manifest(records: list[dict] | None = None, deprecated: tuple[str, ...] = ()) -> dict:
    domains = [{"domain_id": ROOT_DOMAIN, "status": "current"}]
    for domain_id in ("storage", "surface", "verification", *deprecated):
        domains.append({"domain_id": domain_id, "status": "deprecated" if domain_id in deprecated else "current"})
    return {"domain_registry": {"root_domain_id": ROOT_DOMAIN, "domains": domains}, "records": records or []}


def law(law_id: str, home: str, *evidence: str, kind: str = "decision", status: str = "accepted") -> dict:
    return {"id": law_id, "kind": kind, "status": status, "home_domain_id": home, "evidence": list(evidence)}


class PackageDomainMapTests(unittest.TestCase):
    def setUp(self) -> None:
        scratch = tempfile.TemporaryDirectory()
        self.addCleanup(scratch.cleanup)
        self.root = Path(scratch.name)
        (self.root / checker.MAP_PATH).parent.mkdir(parents=True)

    def write(self, packages: list[dict], raw: bytes | None = None) -> None:
        document = {"packages": packages, "schema_version": "1.0"}
        (self.root / checker.MAP_PATH).write_bytes(raw if raw is not None else checker.shard_format.canonical_bytes(document))

    def check(
        self,
        packages: list[dict] | None = None,
        records: list[dict] | None = None,
        files: list[str] | None = None,
        go_packages: list[str] | None = None,
        deprecated: tuple[str, ...] = (),
    ) -> list[str]:
        self.write(clean_entries() if packages is None else packages)
        with patch.object(checker.knowledge_index, "compose_manifest", return_value=manifest(records, deprecated)):
            return checker.validate(self.root, FILES if files is None else files, PACKAGES if go_packages is None else go_packages)

    def assertFinding(self, findings: list[str], fragment: str) -> None:
        self.assertTrue(any(fragment in finding for finding in findings), f"no finding contains {fragment!r}: {findings}")

    def test_clean_map_passes(self) -> None:
        self.assertEqual(self.check(), [])

    def test_missing_map_refuses(self) -> None:
        with patch.object(checker.knowledge_index, "compose_manifest", return_value=manifest()):
            self.assertEqual(checker.validate(self.root, FILES, PACKAGES), [f"{checker.MAP_PATH}: missing"])

    def test_go_package_without_entry_refuses(self) -> None:
        findings = self.check(go_packages=[*PACKAGES, "internal/version"], files=[*FILES, "internal/version/version.go"])
        self.assertFinding(findings, "Go package internal/version: has no package-map entry")

    def test_unknown_and_deprecated_domains_refuse(self) -> None:
        entries = clean_entries()
        entries[0] = entry("cmd/demo", "retired", "surface")
        entries[1] = entry("internal/store", "nowhere")
        findings = self.check(packages=entries, deprecated=("retired",))
        self.assertFinding(findings, "package cmd/demo: 'retired' is not a current registry Domain")
        self.assertFinding(findings, "package internal/store: 'nowhere' is not a current registry Domain")

    def test_code_file_without_entry_refuses(self) -> None:
        findings = self.check(files=[*FILES, "tools/gen.sh", "bin/runner"])
        self.assertFinding(findings, "code file tools/gen.sh: resolves to no package-map entry")
        self.assertFinding(findings, "code file bin/runner: resolves to no package-map entry")
        self.assertFalse(any("README.md" in finding for finding in findings))

    def test_entry_without_code_refuses(self) -> None:
        findings = self.check(packages=[*clean_entries()[:3], entry("docs", "surface"), clean_entries()[3]])
        self.assertFinding(findings, "package docs: resolves no tracked code file")

    def test_file_entry_resolves_nothing(self) -> None:
        entries = [*clean_entries()[:2], entry("internal/store/store.go", "storage"), *clean_entries()[2:]]
        self.assertFinding(self.check(packages=entries), "package internal/store/store.go: resolves no tracked code file")

    def test_nested_package_resolves_to_longest_prefix(self) -> None:
        records = [law("CD-1", "verification", "internal/store/storetest/fixture.go")]
        findings = self.check(records=records)
        self.assertEqual(findings, ["package internal/store/storetest: law CD-1 homed in 'verification' names internal/store/storetest/fixture.go, so the entry must list 'verification'"])

    def test_law_home_missing_from_entry_refuses(self) -> None:
        findings = self.check(records=[law("CD-2", "surface", "internal/store/store.go")])
        self.assertFinding(findings, "package internal/store: law CD-2 homed in 'surface' names internal/store/store.go")

    def test_law_check_exempts_root_noncode_and_nonlaw_records(self) -> None:
        records = [
            law("CD-3", ROOT_DOMAIN, "internal/store/store.go"),
            law("CD-4", "surface", "README.md"),
            law("L-1", "surface", "internal/store/store.go", kind="lesson", status="published"),
        ]
        self.assertEqual(self.check(records=records), [])

    def test_spec_and_constitution_are_law(self) -> None:
        records = [law("SPEC", "surface", "scripts/check.py", kind="spec"), law("C1", "storage", "cmd/demo/main.go", kind="constitution")]
        findings = self.check(records=records)
        self.assertFinding(findings, "law SPEC homed in 'surface'")
        self.assertFinding(findings, "law C1 homed in 'storage'")

    def test_shape_refusals(self) -> None:
        cases = {
            "glob": [entry("internal/*", "storage")],
            "absolute": [entry("/internal/store", "storage")],
            "traversal": [entry("internal/../store", "storage")],
            "dot": [entry(".", "storage")],
            "trailing slash": [entry("internal/store/", "storage")],
        }
        for label, packages in cases.items():
            with self.subTest(label):
                self.assertFinding(self.check(packages=packages), "is not a normalized relative directory without globs")

    def test_duplicate_and_unsorted_paths_refuse(self) -> None:
        entries = clean_entries()
        self.assertFinding(self.check(packages=[*entries, entries[0]]), "package paths must be unique")
        self.assertFinding(self.check(packages=list(reversed(entries))), "packages must be sorted by path")

    def test_domain_ids_bounds_and_order_refuse(self) -> None:
        self.assertFinding(self.check(packages=[{"domain_ids": [], "path": "cmd/demo"}]), "domain_ids must list 1-16 Domain IDs")
        self.assertFinding(
            self.check(packages=[{"domain_ids": ["surface", "storage"], "path": "cmd/demo"}]),
            "domain_ids must be sorted and unique",
        )

    def test_document_shape_and_canonical_bytes_refuse(self) -> None:
        self.write(clean_entries(), raw=b"{")
        with patch.object(checker.knowledge_index, "compose_manifest", return_value=manifest()):
            self.assertFinding(checker.validate(self.root, FILES, PACKAGES), "not valid JSON")
        self.write(clean_entries(), raw=json.dumps({"packages": clean_entries(), "schema_version": "1.0", "extra": 1}).encode())
        with patch.object(checker.knowledge_index, "compose_manifest", return_value=manifest()):
            self.assertFinding(checker.validate(self.root, FILES, PACKAGES), "exactly schema_version and packages")
        self.write(clean_entries(), raw=json.dumps({"packages": clean_entries(), "schema_version": "1.0"}).encode())
        with patch.object(checker.knowledge_index, "compose_manifest", return_value=manifest()):
            self.assertFinding(checker.validate(self.root, FILES, PACKAGES), "bytes are not canonical")

    def test_oversized_map_refuses(self) -> None:
        self.write(clean_entries(), raw=b" " * (checker.MAX_MAP_BYTES + 1))
        with patch.object(checker.knowledge_index, "compose_manifest", return_value=manifest()):
            self.assertFinding(checker.validate(self.root, FILES, PACKAGES), "exceeds the 16384-byte knowledge shard bound")

    def test_repository_map_passes(self) -> None:
        root = checker.ROOT
        self.assertEqual(checker.validate(root, checker.tracked_files(root), checker.go_packages(root)), [])


if __name__ == "__main__":
    sys.exit(unittest.main())

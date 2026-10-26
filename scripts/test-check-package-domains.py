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
        for name in FILES:
            target = self.root / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.touch()

    def write(self, packages: list[dict], raw: bytes | None = None, version: str = "1.0") -> None:
        document = {"packages": packages, "schema_version": version}
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
        findings = self.check(packages=sorted([*clean_entries(), entry("docs", "surface")], key=lambda value: value["path"]))
        self.assertFinding(findings, "package docs: resolves no tracked code file")

    def test_file_entry_resolves_nothing(self) -> None:
        entries = sorted([*clean_entries(), entry("internal/store/store.go", "storage")], key=lambda value: value["path"])
        # CD-0244 D2 refuses files using actual filesystem type, not suffixes.
        self.assertFinding(self.check(packages=entries), "repository-contained directory/module root")

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

    def test_explicit_root_roles_and_empty_product(self) -> None:
        entries = [
            {"path": "cmd/demo", "role": "composition"},
            {"path": "internal/store", "role": "domain", "owner_domain_id": "storage"},
            {"path": "internal/store/storetest", "role": "test_support"},
            {"path": "scripts", "role": "mixed", "domain_ids": ["storage", "verification"]},
        ]
        for name in FILES:
            if checker.is_code_file(name):
                target = self.root / name
                target.parent.mkdir(parents=True, exist_ok=True)
                target.touch()
        self.write(entries, version="1.1")
        # CD-0244 D2 retires evidence-to-multi-Domain ownership inference for 1.1.
        with patch.object(checker.knowledge_index, "compose_manifest", return_value=manifest([law("CD-1", "surface", "internal/store/store.go")])):
            self.assertEqual(checker.validate(self.root, FILES, PACKAGES), [])
        self.write([], version="1.1")
        with patch.object(checker.knowledge_index, "compose_manifest", return_value=manifest()):
            self.assertEqual(checker.validate(self.root, [], []), [])
            self.assertFinding(checker.validate(self.root, FILES, PACKAGES), "has no package-map entry")

    def test_root_role_cardinalities(self) -> None:
        invalid = [
            {"role": "domain"},
            {"role": "domain", "owner_domain_id": ""},
            {"role": "domain", "owner_domain_id": ["storage", "surface"]},
            {"role": "domain", "owner_domain_id": "storage", "domain_ids": []},
            {"role": "mixed", "domain_ids": []},
            {"role": "mixed", "domain_ids": ["storage"]},
            {"role": "mixed", "domain_ids": ["storage", "storage"]},
            {"role": "mixed", "domain_ids": [f"domain-{i:02d}" for i in range(17)]},
            {"role": "mixed", "domain_ids": ["storage", "surface"], "owner_domain_id": "storage"},
            {"role": "composition", "domain_ids": []},
            {"role": "test_support", "owner_domain_id": "storage"},
            {"role": "unknown"},
            {"domain_ids": ["storage"]},
        ]
        for fields in invalid:
            with self.subTest(fields=fields):
                raw = checker.shard_format.canonical_bytes({"schema_version": "1.1", "packages": [{"path": "src", **fields}]})
                self.assertTrue(checker.validate_shape(raw)[1])

    def test_root_filesystem_and_current_domains(self) -> None:
        (self.root / "src").mkdir()
        (self.root / "src" / "source.go").touch()
        for role_fields in [
            {"role": "domain", "owner_domain_id": "missing"},
            {"role": "domain", "owner_domain_id": "retired"},
            {"role": "mixed", "domain_ids": ["retired", "storage"]},
        ]:
            self.write([{"path": "src", **role_fields}], version="1.1")
            with patch.object(checker.knowledge_index, "compose_manifest", return_value=manifest(deprecated=("retired",))):
                self.assertFinding(checker.validate(self.root, ["src/source.go"], []), "not a current registry Domain")
        for version in ("1.0", "1.1"):
            for name in ["src/source.go", "missing", "src/source"]:
                if name == "src/source":
                    (self.root / name).touch()
                fields = {"domain_ids": ["storage"]} if version == "1.0" else {"role": "composition"}
                self.write([{"path": name, **fields}], version=version)
                with patch.object(checker.knowledge_index, "compose_manifest", return_value=manifest()):
                    self.assertFinding(checker.validate(self.root, [], []), "directory")

    def test_both_versions_refuse_glob_and_noncanonical_json(self) -> None:
        for version in ("1.0", "1.1"):
            for name in ("src/*.go", "src\\file", "src/../other"):
                fields = {"domain_ids": ["storage"]} if version == "1.0" else {"role": "composition"}
                raw = checker.shard_format.canonical_bytes({"schema_version": version, "packages": [{"path": name, **fields}]})
                self.assertTrue(checker.validate_shape(raw)[1], (version, name))
        for raw in (b'{"schema_version":"1.1","packages":[],"packages":[]}', b'{"schema_version":"1.1","packages":null}'):
            self.assertTrue(checker.validate_shape(raw)[1])

    def test_root_symlink_cannot_escape_repository(self) -> None:
        with tempfile.TemporaryDirectory() as outside:
            (self.root / "escape").symlink_to(outside, target_is_directory=True)
            self.write([{"path": "escape", "role": "composition"}], version="1.1")
            with patch.object(checker.knowledge_index, "compose_manifest", return_value=manifest()):
                self.assertFinding(checker.validate(self.root, ["escape/main.go"], []), "repository-contained directory/module root")

    def test_module_directory_is_not_inferred_from_filename_suffix(self) -> None:
        (self.root / "module.go").mkdir()
        self.write([{"path": "module.go", "role": "composition"}], version="1.1")
        with patch.object(checker.knowledge_index, "compose_manifest", return_value=manifest()):
            self.assertEqual(checker.validate(self.root, ["module.go/entry.py"], []), [])

    def test_schema_path_pattern_matches_root_shape(self) -> None:
        schema = json.loads((checker.ROOT / "contracts/package-domains.v1.schema.json").read_text())
        pattern = checker.re.compile(schema["$defs"]["path"]["pattern"])
        for name in ("src", "src/module.v1", "src/nested", "src/file.go", "src/file.TSX", "src/*.go", "src\\file", "../src", "src/../other", "src/", "src//nested", ".", "/src"):
            with self.subTest(name=name):
                self.assertEqual(bool(pattern.fullmatch(name)), checker.normalized_path(name))

    def test_repository_map_passes(self) -> None:
        root = checker.ROOT
        self.assertEqual(checker.validate(root, checker.tracked_files(root), checker.go_packages(root)), [])


if __name__ == "__main__":
    sys.exit(unittest.main())

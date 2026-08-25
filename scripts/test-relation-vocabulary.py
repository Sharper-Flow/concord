#!/usr/bin/env python3
"""Negative tests for the relation vocabulary closure checker."""
from __future__ import annotations

import importlib.util
import json
import shutil
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
SCRIPT = Path(__file__).with_name("check-relation-vocabulary.py")
SPEC = importlib.util.spec_from_file_location("relation_vocabulary_checker", SCRIPT)
assert SPEC and SPEC.loader
checker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(checker)


class RelationVocabularyFixture(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory(prefix="relation-vocabulary-")
        self.root = Path(self.tempdir.name)
        for relative in (
            "contracts/relation-vocabulary.v1.json",
            "contracts/relation-vocabulary.schema.json",
            "contracts/agent-tool-surface-payloads.schema.json",
            "internal/store/generated_relation_vocabulary.go",
            "contracts/relation-vocabulary.digest",
            "scripts/generate-relation-vocabulary.py",
        ):
            destination = self.root / relative
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(REPO_ROOT / relative, destination)

    def tearDown(self) -> None:
        self.tempdir.cleanup()

    def read_json(self, relative: str) -> dict:
        return json.loads((self.root / relative).read_text(encoding="utf-8"))

    def write_json(self, relative: str, value: object) -> None:
        (self.root / relative).write_text(
            json.dumps(value, indent=2) + "\n", encoding="utf-8"
        )

    def surface(self) -> dict:
        return self.read_json("contracts/agent-tool-surface-payloads.schema.json")

    def vocabulary(self) -> dict:
        return self.read_json("contracts/relation-vocabulary.v1.json")

    def findings(self) -> list[str]:
        return checker.check(self.root)

    def assert_finding(self, prefix: str) -> None:
        findings = self.findings()
        self.assertTrue(any(finding.startswith(prefix) for finding in findings), findings)

    def test_clean_fixture_passes(self) -> None:
        self.assertEqual(self.findings(), [])

    def test_generator_freshness_finds_changed_projection(self) -> None:
        path = self.root / "internal/store/generated_relation_vocabulary.go"
        path.write_text(path.read_text(encoding="utf-8") + "\n", encoding="utf-8")
        self.assert_finding("generator-freshness:")

    def test_link_kind_closure_finds_missing_member(self) -> None:
        surface = self.surface()
        surface["$defs"]["relation_link_kind"]["enum"].remove("blocks")
        self.write_json("contracts/agent-tool-surface-payloads.schema.json", surface)
        self.assert_finding("link-kind-closure: missing member(s):")

    def test_link_kind_closure_finds_extra_member(self) -> None:
        surface = self.surface()
        surface["$defs"]["relation_link_kind"]["enum"].append("ghost")
        self.write_json("contracts/agent-tool-surface-payloads.schema.json", surface)
        self.assert_finding("link-kind-closure: extra member(s):")

    def test_query_label_closure_finds_missing_member(self) -> None:
        surface = self.surface()
        surface["$defs"]["relation_query_label"]["enum"].remove("blocks")
        self.write_json("contracts/agent-tool-surface-payloads.schema.json", surface)
        self.assert_finding("query-label-closure: missing member(s):")

    def test_query_label_closure_finds_extra_member(self) -> None:
        surface = self.surface()
        surface["$defs"]["relation_query_label"]["enum"].append("ghost")
        self.write_json("contracts/agent-tool-surface-payloads.schema.json", surface)
        self.assert_finding("query-label-closure: extra member(s):")

    def test_query_label_orphan_finds_unresolved_label(self) -> None:
        surface = self.surface()
        surface["$defs"]["relation_query_label"]["enum"].append("ghost")
        self.write_json("contracts/agent-tool-surface-payloads.schema.json", surface)
        self.assert_finding("query-label-orphan:")

    def test_overlap_resolution_subset_finds_unwritten_kind(self) -> None:
        surface = self.surface()
        surface["$defs"]["work_relate_resolve_overlap_input"]["properties"]["resolution_kind"]["enum"].append("implements")
        self.write_json("contracts/agent-tool-surface-payloads.schema.json", surface)
        self.assert_finding("overlap-resolution-subset:")

    def test_stale_reference_finds_old_shared_definition(self) -> None:
        path = self.root / "contracts/agent-tool-surface-payloads.schema.json"
        text = path.read_text(encoding="utf-8")
        text = text.replace(
            '"relation_link_kind": {',
            '"stale_marker": "#/$defs/relation_kind",\n    "relation_link_kind": {',
            1,
        )
        path.write_text(text, encoding="utf-8")
        self.assert_finding("stale-ref:")

    def test_inverse_label_uniqueness_finds_duplicate(self) -> None:
        vocabulary = self.vocabulary()
        vocabulary["kinds"][0]["inverse_label"] = "implemented_by"
        self.write_json("contracts/relation-vocabulary.v1.json", vocabulary)
        self.assert_finding("inverse-label-uniqueness:")

    def test_inverse_label_uniqueness_finds_kind_collision(self) -> None:
        vocabulary = self.vocabulary()
        vocabulary["kinds"][0]["inverse_label"] = "depends_on"
        self.write_json("contracts/relation-vocabulary.v1.json", vocabulary)
        self.assert_finding("inverse-label-uniqueness:")


if __name__ == "__main__":
    unittest.main()

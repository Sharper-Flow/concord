#!/usr/bin/env python3
"""Tests for the project tooling manifest checker."""

from __future__ import annotations

import importlib.util
import json
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("check-project-tooling.py")
SPEC = importlib.util.spec_from_file_location("project_tooling_checker", SCRIPT)
assert SPEC and SPEC.loader
checker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(checker)


def tool(identifier: str = "go-vet", **overrides: object) -> dict[str, object]:
    entry: dict[str, object] = {
        "id": identifier,
        "purpose": "Standard static analysis.",
        "invocation": "go vet ./...",
        "tier": "fast",
        "in_ci": True,
        "ci_reference": "go vet ./...",
    }
    entry.update(overrides)
    return entry


class ProjectToolingTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.root = Path(self.tempdir.name)
        (self.root / ".concord").mkdir()
        (self.root / "contracts").mkdir()
        (self.root / ".github/workflows").mkdir(parents=True)
        (self.root / "contracts" / "project-tooling.v1.schema.json").write_text("{}\n", encoding="utf-8")
        (self.root / ".github/workflows" / "ci.yml").write_text(
            "      - run: go vet ./...\n", encoding="utf-8"
        )

    def tearDown(self) -> None:
        self.tempdir.cleanup()

    def manifest(self, tools: list[dict[str, object]]) -> None:
        document = {"schema_version": "1.0", "project": "demo", "tools": tools}
        (self.root / ".concord" / "tooling.v1.json").write_text(
            json.dumps(document, indent=2) + "\n", encoding="utf-8"
        )

    def test_valid_manifest_passes(self) -> None:
        on_demand = tool("slow-scan", invocation="slow-scan ./...", tier="slow", in_ci=False)
        del on_demand["ci_reference"]
        self.manifest([tool(), on_demand])

        self.assertEqual(checker.check(root=self.root), [])

    def test_missing_manifest_is_a_finding(self) -> None:
        findings = checker.check(root=self.root)

        self.assertEqual(len(findings), 1)
        self.assertIn("missing manifest", findings[0])

    def test_missing_schema_is_a_finding(self) -> None:
        (self.root / "contracts" / "project-tooling.v1.schema.json").unlink()
        self.manifest([tool()])

        findings = checker.check(root=self.root)

        self.assertTrue(any("missing schema" in finding for finding in findings))

    def test_unknown_tool_field_is_reported(self) -> None:
        self.manifest([tool(extra="nope")])

        findings = checker.check(root=self.root)

        self.assertTrue(any("unknown field" in finding for finding in findings))

    def test_missing_required_field_is_reported(self) -> None:
        entry = tool()
        del entry["tier"]
        self.manifest([entry])

        findings = checker.check(root=self.root)

        self.assertTrue(any("missing field" in finding for finding in findings))

    def test_bad_tier_is_reported(self) -> None:
        self.manifest([tool(tier="instant")])

        findings = checker.check(root=self.root)

        self.assertTrue(any("tier must be one of" in finding for finding in findings))

    def test_duplicate_tool_id_is_reported(self) -> None:
        self.manifest([tool(), tool()])

        findings = checker.check(root=self.root)

        self.assertTrue(any("duplicate tool id" in finding for finding in findings))

    def test_unresolvable_config_path_is_reported(self) -> None:
        self.manifest([tool(config_path=".golangci.yml")])

        findings = checker.check(root=self.root)

        self.assertTrue(any("config_path does not resolve" in finding for finding in findings))

    def test_resolvable_config_path_passes(self) -> None:
        (self.root / "ruff.toml").write_text("[lint]\n", encoding="utf-8")
        self.manifest([tool(config_path="ruff.toml")])

        self.assertEqual(checker.check(root=self.root), [])

    def test_config_path_traversal_is_reported(self) -> None:
        self.manifest([tool(config_path="../outside.toml")])

        findings = checker.check(root=self.root)

        self.assertTrue(any("repository-relative path without traversal" in finding for finding in findings))

    def test_in_ci_true_requires_reference(self) -> None:
        entry = tool()
        del entry["ci_reference"]
        self.manifest([entry])

        findings = checker.check(root=self.root)

        self.assertTrue(any("ci_reference is required" in finding for finding in findings))

    def test_reference_missing_from_workflows_is_reported(self) -> None:
        self.manifest([tool(ci_reference="gosec ./...")])

        findings = checker.check(root=self.root)

        self.assertTrue(any("ci_reference not found in any workflow" in finding for finding in findings))

    def test_in_ci_false_forbids_reference(self) -> None:
        self.manifest([tool(in_ci=False)])

        findings = checker.check(root=self.root)

        self.assertTrue(any("ci_reference is forbidden" in finding for finding in findings))

    def test_duplicate_json_keys_are_rejected(self) -> None:
        raw = '{"schema_version": "1.0", "schema_version": "1.0", "project": "demo", "tools": []}'
        (self.root / ".concord" / "tooling.v1.json").write_text(raw, encoding="utf-8")

        findings = checker.check(root=self.root)

        self.assertTrue(any("duplicate JSON key" in finding for finding in findings))

    def test_multiline_invocation_is_reported(self) -> None:
        self.manifest([tool(invocation="go vet ./...\n&& echo done")])

        findings = checker.check(root=self.root)

        self.assertTrue(any("single line" in finding for finding in findings))

    def test_repository_itself_passes(self) -> None:
        self.assertEqual(checker.check(root=Path(__file__).resolve().parents[1]), [])


if __name__ == "__main__":
    unittest.main()

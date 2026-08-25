#!/usr/bin/env python3
"""Tests for the project tooling manifest checker."""

from __future__ import annotations

import importlib.util
import json
import tempfile
import unittest
from pathlib import Path

try:
    import jsonschema
except ImportError:
    jsonschema = None


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
        "cadence": "routine",
        "automation_path": ".github/workflows/ci.yml",
    }
    entry.update(overrides)
    return entry


class ProjectToolingTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.root = Path(self.tempdir.name)
        (self.root / ".concord").mkdir()
        (self.root / ".github/workflows").mkdir(parents=True)
        (self.root / ".github/workflows/ci.yml").write_text("name: CI\n", encoding="utf-8")

    def tearDown(self) -> None:
        self.tempdir.cleanup()

    def document(self, tools: list[dict[str, object]]) -> dict[str, object]:
        return {"schema_version": "1.0", "project": "demo", "tools": tools}

    def manifest(self, tools: list[dict[str, object]]) -> None:
        (self.root / ".concord/tooling.v1.json").write_text(
            json.dumps(self.document(tools), indent=2) + "\n", encoding="utf-8"
        )

    def test_schema_free_valid_adoption_passes(self) -> None:
        on_demand = tool("slow-scan", invocation="slow-scan ./...", tier="slow", cadence="on_demand")
        del on_demand["automation_path"]
        self.manifest([tool(), on_demand])

        self.assertFalse((self.root / "contracts/project-tooling.v1.schema.json").exists())
        self.assertEqual(checker.check(root=self.root), [])

    def test_missing_manifest_is_a_finding(self) -> None:
        findings = checker.check(root=self.root)

        self.assertEqual(len(findings), 1)
        self.assertIn("missing manifest", findings[0])

    def test_manifest_symlink_escape_is_reported(self) -> None:
        with tempfile.TemporaryDirectory() as outside:
            target = Path(outside) / "tooling.v1.json"
            target.write_text(json.dumps(self.document([tool()])), encoding="utf-8")
            (self.root / ".concord/tooling.v1.json").symlink_to(target)

            findings = checker.check(root=self.root)

        self.assertTrue(any("manifest resolves outside" in finding for finding in findings))

    def test_unknown_tool_field_is_reported(self) -> None:
        self.manifest([tool(extra="nope")])

        findings = checker.check(root=self.root)

        self.assertTrue(any("unknown field" in finding for finding in findings))

    def test_missing_required_field_is_reported(self) -> None:
        entry = tool()
        del entry["cadence"]
        self.manifest([entry])

        findings = checker.check(root=self.root)

        self.assertTrue(any("missing field" in finding for finding in findings))

    def test_bad_tier_and_cadence_are_reported(self) -> None:
        self.manifest([tool(tier="instant", cadence="sometimes")])

        findings = checker.check(root=self.root)

        self.assertTrue(any("tier must be one of" in finding for finding in findings))
        self.assertTrue(any("cadence must be one of" in finding for finding in findings))

    def test_unhashable_tier_and_cadence_are_reported_without_crashing(self) -> None:
        for key, value in (("tier", []), ("cadence", {})):
            with self.subTest(key=key):
                self.manifest([tool(**{key: value})])
                findings = checker.check(root=self.root)
                self.assertTrue(any(f"{key} must be one of" in finding for finding in findings))

    def test_duplicate_tool_id_is_reported(self) -> None:
        self.manifest([tool(), tool()])

        findings = checker.check(root=self.root)

        self.assertTrue(any("duplicate tool id" in finding for finding in findings))

    def test_unresolvable_file_paths_are_reported(self) -> None:
        for key in ("config_path", "automation_path"):
            with self.subTest(key=key):
                self.manifest([tool(**{key: "missing/file.toml"})])
                findings = checker.check(root=self.root)
                self.assertTrue(any(f"{key} does not resolve" in finding for finding in findings))

    def test_resolvable_file_paths_pass(self) -> None:
        (self.root / "ruff.toml").write_text("[lint]\n", encoding="utf-8")
        self.manifest([tool(config_path="ruff.toml")])

        self.assertEqual(checker.check(root=self.root), [])

    def test_file_paths_reject_directories(self) -> None:
        (self.root / "config-dir").mkdir()
        self.manifest([tool(config_path="config-dir")])

        findings = checker.check(root=self.root)

        self.assertTrue(any("config_path must resolve to a regular file" in finding for finding in findings))

    def test_config_path_symlink_escape_is_reported(self) -> None:
        with tempfile.TemporaryDirectory() as outside:
            target = Path(outside) / "ruff.toml"
            target.write_text("[lint]\n", encoding="utf-8")
            (self.root / "ruff.toml").symlink_to(target)
            self.manifest([tool(config_path="ruff.toml")])

            findings = checker.check(root=self.root)

        self.assertTrue(any("config_path resolves outside" in finding for finding in findings))

    def test_automation_path_symlink_escape_is_reported(self) -> None:
        with tempfile.TemporaryDirectory() as outside:
            target = Path(outside) / "automation.yml"
            target.write_text("name: outside\n", encoding="utf-8")
            (self.root / "automation.yml").symlink_to(target)
            self.manifest([tool(automation_path="automation.yml")])

            findings = checker.check(root=self.root)

        self.assertTrue(any("automation_path resolves outside" in finding for finding in findings))

    def test_path_lexical_constraints_are_reported(self) -> None:
        invalid = ("/absolute.toml", "../outside.toml", "dir//file", "dir/./file", "dir\\file")
        for key in ("config_path", "automation_path"):
            for value in invalid:
                with self.subTest(key=key, value=value):
                    self.manifest([tool(**{key: value})])
                    findings = checker.check(root=self.root)
                    self.assertTrue(any(f"{key} must be a safe repository-relative path" in finding for finding in findings))

    def test_duplicate_json_keys_are_rejected(self) -> None:
        raw = '{"schema_version": "1.0", "schema_version": "1.0", "project": "demo", "tools": []}'
        (self.root / ".concord/tooling.v1.json").write_text(raw, encoding="utf-8")

        findings = checker.check(root=self.root)

        self.assertTrue(any("duplicate JSON key" in finding for finding in findings))

    def test_project_with_trailing_newline_is_rejected(self) -> None:
        document = self.document([tool()])
        document["project"] = "demo\n"
        (self.root / ".concord/tooling.v1.json").write_text(json.dumps(document), encoding="utf-8")

        findings = checker.check(root=self.root)

        self.assertTrue(any("project must match" in finding for finding in findings))

    def test_invocation_must_be_a_single_line_without_controls(self) -> None:
        for invocation in ("go vet ./...\nnext", "go vet\rnext", "go\tvet", "go vet\x7f"):
            with self.subTest(invocation=repr(invocation)):
                self.manifest([tool(invocation=invocation)])
                findings = checker.check(root=self.root)
                self.assertTrue(any("invocation must be a single line" in finding for finding in findings))

    def test_cost_hint_is_bounded_and_single_line(self) -> None:
        for cost_hint in ("abc", "x" * 129, "four\nlines"):
            with self.subTest(cost_hint=repr(cost_hint)):
                self.manifest([tool(cost_hint=cost_hint)])
                findings = checker.check(root=self.root)
                self.assertTrue(any("cost_hint" in finding for finding in findings))

    def test_text_lengths_count_raw_characters(self) -> None:
        boundary_entries = (
            tool(purpose=" a  ", invocation="x", cost_hint=" a  ", notes=" a  "),
            tool(
                purpose=" " + "p" * 254 + " ",
                invocation=" " + "i" * 510 + " ",
                cost_hint=" " + "c" * 126 + " ",
                notes=" " + "n" * 510 + " ",
            ),
        )
        for entry in boundary_entries:
            with self.subTest(lengths={key: len(value) for key, value in entry.items() if isinstance(value, str)}):
                self.manifest([entry])
                self.assertEqual(checker.check(root=self.root), [])

    def test_text_fields_reject_whitespace_only_values(self) -> None:
        for key in ("purpose", "invocation", "cost_hint", "notes"):
            with self.subTest(key=key):
                self.manifest([tool(**{key: " \t\r\n"})])
                findings = checker.check(root=self.root)
                self.assertTrue(any(f"{key} must contain a character outside JSON whitespace" in finding for finding in findings))

    @unittest.skipIf(jsonschema is None, "jsonschema is not installed")
    def test_checker_matches_schema_on_text_lexical_fixtures(self) -> None:
        schema = json.loads(
            (Path(__file__).resolve().parents[1] / "contracts/project-tooling.v1.schema.json").read_text(encoding="utf-8")
        )
        validator = jsonschema.Draft202012Validator(schema)
        fixtures = [
            ("minimum raw lengths", self.document([tool(purpose=" a  ", invocation="x", cost_hint=" a  ", notes=" a  ")]), True),
            (
                "maximum padded lengths",
                self.document([
                    tool(
                        purpose=" " + "p" * 254 + " ",
                        invocation=" " + "i" * 510 + " ",
                        cost_hint=" " + "c" * 126 + " ",
                        notes=" " + "n" * 510 + " ",
                    )
                ]),
                True,
            ),
            ("multiline invocation", self.document([tool(invocation="go vet\nnext")]), False),
            ("control cost hint", self.document([tool(cost_hint="cost\tseconds")]), False),
        ]
        for key in ("purpose", "invocation", "cost_hint", "notes"):
            fixtures.append((f"JSON whitespace {key}", self.document([tool(**{key: " \t\r\n"})]), False))
        for name, character, control_allowed in (
            ("U+001C", "\u001c", False),
            ("U+001F", "\u001f", False),
            ("U+0085", "\u0085", True),
            ("NBSP", "\u00a0", True),
        ):
            fixtures.extend(
                [
                    (f"{name} purpose", self.document([tool(purpose=character * 4)]), True),
                    (f"{name} invocation", self.document([tool(invocation=character)]), control_allowed),
                    (f"{name} cost hint", self.document([tool(cost_hint=character * 4)]), control_allowed),
                    (f"{name} notes", self.document([tool(notes=character * 4)]), True),
                ]
            )
        trailing_project = self.document([tool()])
        trailing_project["project"] = "demo\n"
        fixtures.append(("trailing-newline project", trailing_project, False))

        for name, document, expected_valid in fixtures:
            with self.subTest(name=name):
                (self.root / ".concord/tooling.v1.json").write_text(json.dumps(document), encoding="utf-8")
                checker_valid = not checker.check(root=self.root)
                schema_valid = not list(validator.iter_errors(document))
                self.assertEqual(checker_valid, expected_valid)
                self.assertEqual(schema_valid, expected_valid)

    def test_schema_uses_checker_lexical_constraints(self) -> None:
        schema = json.loads(
            (Path(__file__).resolve().parents[1] / "contracts/project-tooling.v1.schema.json").read_text(encoding="utf-8")
        )
        properties = schema["$defs"]["tool"]["properties"]

        self.assertEqual(properties["config_path"]["pattern"], checker.SAFE_PATH_PATTERN)
        self.assertEqual(properties["automation_path"]["pattern"], checker.SAFE_PATH_PATTERN)
        self.assertEqual(properties["tier"]["type"], "string")
        self.assertEqual(properties["cadence"]["type"], "string")
        self.assertEqual(properties["cadence"]["enum"], ["routine", "on_demand"])
        text_pattern = r"[^ \t\r\n]"
        self.assertEqual(properties["purpose"]["pattern"], text_pattern)
        self.assertEqual(properties["notes"]["pattern"], text_pattern)
        self.assertIn({"pattern": text_pattern}, properties["invocation"]["allOf"])
        self.assertIn({"pattern": text_pattern}, properties["cost_hint"]["allOf"])
        self.assertIn("cadence", schema["$defs"]["tool"]["required"])

    def test_dogfood_keeps_bin_oc_test_on_demand(self) -> None:
        repository = Path(__file__).resolve().parents[1]
        document = json.loads((repository / ".concord/tooling.v1.json").read_text(encoding="utf-8"))
        entries = {entry["id"]: entry for entry in document["tools"]}

        self.assertEqual(entries["bin-oc-test"]["cadence"], "on_demand")
        self.assertTrue(any(entry["cadence"] == "routine" for entry in document["tools"]))

    def test_repository_itself_passes(self) -> None:
        self.assertEqual(checker.check(root=Path(__file__).resolve().parents[1]), [])


if __name__ == "__main__":
    unittest.main()

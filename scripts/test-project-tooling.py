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
        on_demand = tool("slow-scan", invocation="slow-scan ./...", tier="slow")
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
        del entry["tier"]
        self.manifest([entry])

        findings = checker.check(root=self.root)

        self.assertTrue(any("missing field" in finding for finding in findings))

    def test_bad_tier_is_reported(self) -> None:
        self.manifest([tool(tier="instant")])

        findings = checker.check(root=self.root)

        self.assertTrue(any("tier must be one of" in finding for finding in findings))

    def test_unhashable_tier_is_reported_without_crashing(self) -> None:
        self.manifest([tool(tier=[])])

        findings = checker.check(root=self.root)

        self.assertTrue(any("tier must be one of" in finding for finding in findings))

    def test_cadence_is_rejected_as_methodology(self) -> None:
        self.manifest([tool(cadence="on_demand")])

        findings = checker.check(root=self.root)

        self.assertTrue(any("unknown field(s): cadence" in finding for finding in findings))

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

    def test_invocation_must_be_plain_argv(self) -> None:
        for invocation in (
            "go vet ./... && gofmt -l .",
            "go test ./...; echo done",
            "make 'lint'",
            'sh -c "go vet ./..."',
            "echo `date`",
            "go run $(go env GOPATH)/bin/tool",
            "a | b",
            "a > out",
            "a < in",
            "sub(a)",
            "path\\ with\\ spaces",
        ):
            with self.subTest(invocation=repr(invocation)):
                self.manifest([tool(invocation=invocation)])
                findings = checker.check(root=self.root)
                self.assertTrue(any("invocation must be plain argv" in finding for finding in findings))

    def test_plain_argv_invocations_pass(self) -> None:
        self.manifest([tool("flagged", invocation="go test -race -timeout=20m ./..."), tool("dotted", invocation="bin/oc-test conformance")])

        self.assertEqual(checker.check(root=self.root), [])

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

    def test_schema_matches_checker_contract(self) -> None:
        schema = json.loads(
            (Path(__file__).resolve().parents[1] / "contracts/project-tooling.v1.schema.json").read_text(encoding="utf-8")
        )
        root_properties = schema["properties"]
        tool_schema = schema["$defs"]["tool"]
        properties = schema["$defs"]["tool"]["properties"]

        self.assertEqual(set(schema["required"]), checker.ROOT_FIELDS)
        self.assertEqual(set(root_properties), checker.ROOT_FIELDS | checker.OPTIONAL_ROOT_FIELDS)
        self.assertEqual(root_properties["schema_version"]["const"], checker.SCHEMA_VERSION)
        self.assertEqual(root_properties["project"]["pattern"], checker.IDENTIFIER_PATTERN)
        self.assertEqual(root_properties["tools"]["maxItems"], checker.MAX_TOOLS)
        self.assertEqual(root_properties["tools"]["minItems"], 0)
        shape = schema["$defs"]["domain_shape"]
        self.assertEqual(set(shape["properties"]), checker.DOMAIN_SHAPE_FIELDS)
        self.assertEqual(set(properties), checker.TOOL_FIELDS)
        self.assertEqual(set(tool_schema["required"]), checker.REQUIRED_TOOL_FIELDS)
        self.assertEqual(properties["id"]["pattern"], checker.IDENTIFIER_PATTERN)
        self.assertEqual(set(properties["tier"]["enum"]), checker.TIERS)
        self.assertEqual(properties["config_path"]["pattern"], checker.SAFE_PATH_PATTERN)
        self.assertEqual(properties["automation_path"]["pattern"], checker.SAFE_PATH_PATTERN)
        for key in ("config_path", "automation_path"):
            self.assertEqual(properties[key]["minLength"], checker.PATH_MIN_LENGTH)
            self.assertEqual(properties[key]["maxLength"], checker.PATH_MAX_LENGTH)
        for key, (minimum, maximum, single_line) in checker.TEXT_CONSTRAINTS.items():
            self.assertEqual(properties[key]["minLength"], minimum)
            self.assertEqual(properties[key]["maxLength"], maximum)
            patterns = properties[key]["allOf"] if "allOf" in properties[key] else [{"pattern": properties[key]["pattern"]}]
            self.assertIn({"pattern": checker.NON_WHITESPACE_PATTERN}, patterns)
            self.assertEqual({"pattern": checker.SINGLE_LINE_PATTERN} in patterns, single_line)
        invocation_patterns = properties["invocation"]["allOf"]
        self.assertIn({"pattern": checker.PLAIN_ARGV_PATTERN}, invocation_patterns)
        coverage = schema["$defs"]["coverage"]
        self.assertEqual(properties["coverage"]["maxItems"], checker.MAX_COVERAGE)
        self.assertTrue(properties["coverage"]["uniqueItems"])
        self.assertFalse(coverage["additionalProperties"])
        self.assertEqual(set(coverage["required"]), {"anchor", "recipe_path", "case_id"})
        self.assertEqual(set(coverage["properties"]["anchor"]["properties"]["kind"]["enum"]), checker.ANCHOR_KINDS)
        self.assertEqual(coverage["properties"]["anchor"]["properties"]["value"]["pattern"], checker.ANCHOR_VALUE_PATTERN)
        self.assertEqual(coverage["properties"]["recipe_path"]["pattern"], checker.RECIPE_PATH_PATTERN)
        self.assertEqual(coverage["properties"]["case_id"]["pattern"], checker.CASE_PATTERN)

    def coverage(self, **overrides: object) -> dict[str, object]:
        entry = {"anchor": {"kind": "go_test", "value": "internal/demo.TestDemo"},
                 "recipe_path": ".concord/oracles/demo.json", "case_id": "case:demo"}
        entry.update(overrides)
        recipe = self.root / ".concord/oracles/demo.json"
        recipe.parent.mkdir(exist_ok=True)
        recipe.write_text(json.dumps({"kind": "go_top_level_v1", "package_cwd": "internal/demo",
                                     "test_files": ["internal/demo/demo_test.go"], "fixture_files": [],
                                     "cases": {"case:demo": ["TestDemo"], "case:other": ["TestOther"]}}), encoding="utf-8")
        source = self.root / "internal/demo/demo_test.go"
        source.parent.mkdir(parents=True, exist_ok=True)
        source.write_text('package demo\nimport "testing"\nfunc TestDemo(t *testing.T) { t.Fatal("bad") }\n', encoding="utf-8")
        return entry

    def test_explicit_coverage_and_shared_binding_pass(self) -> None:
        coverage = [self.coverage()]
        self.manifest([tool("first", invocation="go test -count=1 ./internal/demo", coverage=coverage),
                       tool("second", invocation="go test -race ./internal/demo", coverage=coverage)])
        self.assertEqual(checker.check(root=self.root), [])

    def test_unrelated_recipe_case_cannot_cover_named_test(self) -> None:
        entry = self.coverage()
        (self.root / entry["recipe_path"]).write_text(json.dumps({
            "kind": "go_top_level_v1", "package_cwd": "internal/demo",
            "test_files": ["internal/demo/demo_test.go"], "fixture_files": [],
            "cases": {"case:demo": ["TestUnrelated"]},
        }), encoding="utf-8")
        self.manifest([tool(invocation="go test ./internal/demo", coverage=[entry])])
        self.assertTrue(any("anchor" in f and "case" in f for f in checker.check(root=self.root)))

    def test_coverage_closed_bounds_and_conflicts(self) -> None:
        for coverage in (None, {}, [self.coverage(extra=True)], [self.coverage(anchor={"kind": "other", "value": "x"})],
                         [self.coverage(recipe_path="../out.json")], [self.coverage(case_id="case:bad space")],
                         [self.coverage(), self.coverage()], [self.coverage()] * 65):
            with self.subTest(coverage=coverage):
                self.manifest([tool(invocation="go test ./internal/demo", coverage=coverage)])
                self.assertTrue(checker.check(root=self.root))
        self.manifest([tool("first", invocation="go test ./internal/demo", coverage=[self.coverage()]),
                       tool("second", invocation="go test ./internal/demo", coverage=[self.coverage(case_id="case:other")])])
        self.assertTrue(any("conflicting" in f for f in checker.check(root=self.root)))

    def test_coverage_requires_direct_allowlisted_command(self) -> None:
        for invocation in ("go test -exec=helper ./internal/demo", "go test -run TestDemo ./internal/demo",
                           "go test -short ./internal/demo", "go test -tags=x ./internal/demo", "go test -unknown ./internal/demo",
                           "bun test --todo adapter/opencode/demo.test.ts", "python3 scripts/check-demo.py --skip",
                           "bin/oc-test targeted -- go test ./internal/demo"):
            with self.subTest(invocation=invocation):
                self.manifest([tool(invocation=invocation, coverage=[self.coverage()])])
                self.assertTrue(any("direct" in f for f in checker.check(root=self.root)))

    def test_coverage_path_requires_contained_file(self) -> None:
        entry = self.coverage()
        (self.root / entry["recipe_path"]).unlink()
        self.manifest([tool(invocation="go test ./internal/demo", coverage=[entry])])
        self.assertTrue(any("recipe_path does not resolve" in f for f in checker.check(root=self.root)))

    def test_manifest_byte_bound(self) -> None:
        self.manifest([tool(notes="x" * 65536)])
        self.assertTrue(any("65536 bytes" in f for f in checker.check(root=self.root)))

    def test_direct_grammar_positive_and_flag_bounds(self) -> None:
        for invocation in ("go test ./...", "go test -race -v -json -count 1 -timeout=20m -p=2 -parallel 4 ./internal/...",
                           "bun test --timeout=1000 ./adapter/opencode/demo.test.ts", "python3 scripts/check-demo.py"):
            with self.subTest(invocation=invocation):
                self.assertTrue(checker.direct_command(invocation))
        for invocation in ("go test -count=0 ./...", "go test -timeout=0s ./...", "go test -v=false ./...",
                           "go test -v -v ./...", "go test -coverpkg=./other ./...", "go test ../other",
                           "go test ./../other", "go test ./tmp/...", "go\ttest ./...", "go test ./internal//demo", "bun test --timeout=0 adapter/opencode/demo.test.ts"):
            with self.subTest(invocation=invocation):
                self.assertFalse(checker.direct_command(invocation))

    def test_domain_shape_empty_product_and_declared_tools(self) -> None:
        document = self.document([])
        document["domain_shape"] = {}
        manifest = self.root / ".concord/tooling.v1.json"
        manifest.write_text(json.dumps(document), encoding="utf-8")
        self.assertEqual(checker.check(root=self.root), [])
        document["tools"] = [tool()]
        document["domain_shape"] = {
            "boundary_check": "go-vet", "data_object_extractor": "go-vet",
            "config_extractor": "go-vet", "reference_check": "go-vet",
            "required_checks": ["go-vet"],
        }
        manifest.write_text(json.dumps(document), encoding="utf-8")
        self.assertEqual(checker.check(root=self.root), [])

    def test_domain_shape_rejects_missing_tools_and_malformed_declarations(self) -> None:
        shapes = [None, [], {"unknown": True}, {"boundary_check": "missing"},
                  {"boundary_check": ""}, {"reference_check": None},
                  {"required_checks": ["missing"]}, {"required_checks": ["go-vet", "go-vet"]},
                  {"required_checks": None}, {"required_checks": [[]]},
                  {"required_checks": ["go-vet"] * 65}]
        for shape in shapes:
            with self.subTest(shape=shape):
                document = self.document([tool()])
                document["domain_shape"] = shape
                (self.root / ".concord/tooling.v1.json").write_text(json.dumps(document), encoding="utf-8")
                self.assertTrue(checker.check(root=self.root))
        self.manifest([])
        self.assertTrue(checker.check(root=self.root))

    def test_dogfood_carries_no_selection_policy(self) -> None:
        repository = Path(__file__).resolve().parents[1]
        document = json.loads((repository / ".concord/tooling.v1.json").read_text(encoding="utf-8"))
        entries = {entry["id"]: entry for entry in document["tools"]}

        self.assertEqual(entries["bin-oc-test"]["config_path"], "bin/oc-test")
        self.assertTrue(all("cadence" not in entry for entry in document["tools"]))

    def test_repository_itself_passes(self) -> None:
        self.assertEqual(checker.check(root=Path(__file__).resolve().parents[1]), [])


if __name__ == "__main__":
    unittest.main()

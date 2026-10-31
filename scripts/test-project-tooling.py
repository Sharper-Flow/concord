#!/usr/bin/env python3
"""Tests for the project tooling manifest checker."""

from __future__ import annotations

import importlib.util
import json
import tempfile
import unittest
from unittest import mock
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
        self.assertEqual(set(root_properties), checker.ROOT_FIELDS)
        self.assertEqual(root_properties["schema_version"]["const"], checker.SCHEMA_VERSION)
        self.assertEqual(root_properties["project"]["pattern"], checker.IDENTIFIER_PATTERN)
        self.assertEqual(root_properties["tools"]["maxItems"], checker.MAX_TOOLS)
        self.assertEqual(root_properties["tools"]["minItems"], 0)
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

    def test_empty_declared_tools_are_valid(self) -> None:
        self.manifest([])
        self.assertEqual(checker.check(root=self.root), [])

    def test_dogfood_carries_no_selection_policy(self) -> None:
        repository = Path(__file__).resolve().parents[1]
        document = json.loads((repository / ".concord/tooling.v1.json").read_text(encoding="utf-8"))
        entries = {entry["id"]: entry for entry in document["tools"]}

        self.assertEqual(entries["bin-oc-test"]["config_path"], "bin/oc-test")
        self.assertTrue(all("cadence" not in entry for entry in document["tools"]))

    def test_repository_itself_passes(self) -> None:
        self.assertEqual(checker.check(root=Path(__file__).resolve().parents[1]), [])

    def coverage_fixture(self, invocation: str = "bun test adapter/opencode/") -> dict:
        (self.root / ".concord/scenarios").mkdir(exist_ok=True)
        (self.root / ".concord/scenarios/demo.json").write_text(json.dumps({
            "scenarios": [{"id": "scenario-proof"}],
        }))
        (self.root / "adapter/opencode").mkdir(parents=True, exist_ok=True)
        (self.root / "adapter/opencode/demo.test.ts").write_text(
            'test("scenario-proof", () => { expect(true).toBe(true); });\n'
        )
        (self.root / ".github/workflows/ci.yml").write_text(f"        run: {invocation}\n")
        return tool("scenario-check", invocation=invocation, coverage=[{
            "anchor": {"kind": "scenario", "value": "scenario-proof"},
            "owner": {"kind": "adapter_test", "value": "adapter/opencode/demo.test.ts#scenario-proof"},
        }])

    def test_explicit_scenario_binding_reaches_an_existing_consumer(self) -> None:
        self.manifest([self.coverage_fixture()])
        self.assertEqual(checker.check(root=self.root), [])

    def test_coverage_rejects_unrelated_or_missing_owner(self) -> None:
        entry = self.coverage_fixture()
        for owner in ("adapter/opencode/absent.test.ts#scenario-proof", "adapter/opencode/demo.test.ts#other-proof"):
            with self.subTest(owner=owner):
                entry["coverage"][0]["owner"]["value"] = owner
                self.manifest([entry])
                self.assertTrue(any("owner" in f for f in checker.check(root=self.root)))

    def test_coverage_rejects_unrelated_invocation_and_ci_mentions(self) -> None:
        entry = self.coverage_fixture()
        entry["invocation"] = "bun test adapter/opencode/other.test.ts"
        self.manifest([entry])
        self.assertTrue(any("reachable" in f for f in checker.check(root=self.root)))
        entry["invocation"] = "bun test adapter/opencode/"
        self.manifest([entry])
        (self.root / ".github/workflows/ci.yml").write_text(
            "        run: echo bun test adapter/opencode/\n"
        )
        self.assertTrue(any("workflow" in f for f in checker.check(root=self.root)))

    def test_coverage_rejects_duplicate_and_conflicting_bindings(self) -> None:
        entry = self.coverage_fixture()
        entry["coverage"].append(entry["coverage"][0].copy())
        self.manifest([entry])
        self.assertTrue(any("duplicate" in f for f in checker.check(root=self.root)))
        other = self.coverage_fixture()
        other["id"] = "other-check"
        other["coverage"][0]["owner"]["value"] = "adapter/opencode/demo.test.ts#different-test"
        self.manifest([entry, other])
        self.assertTrue(any("conflicting" in f for f in checker.check(root=self.root)))

    def test_coverage_shape_is_closed_and_bounded(self) -> None:
        entry = self.coverage_fixture()
        for coverage in ([], None, [{}], [{"anchor": [], "owner": {}}], entry["coverage"] * 65):
            with self.subTest(coverage=coverage):
                entry["coverage"] = coverage
                self.manifest([entry])
                self.assertTrue(checker.check(root=self.root))

    def test_go_package_derivation_does_not_promote_selected_tests(self) -> None:
        import evidence_anchors as anchors

        package = {"kind": "go_package", "value": "internal/demo"}
        test = {"kind": "go_test", "value": "internal/demo.TestProof"}
        for invocation in ("go test ./...", "bin/oc-test broad -- go test -race ./..."):
            self.assertTrue(anchors.invocation_reaches(self.root, invocation, package))
            self.assertTrue(anchors.invocation_reaches(self.root, invocation, test))
        selected = "go test -run ^TestProof$ ./internal/demo"
        self.assertFalse(anchors.invocation_reaches(self.root, selected, package))
        self.assertTrue(anchors.invocation_reaches(self.root, selected, test))
        self.assertFalse(anchors.invocation_reaches(self.root, selected, {**test, "value": "internal/demo.TestOther"}))
        for invocation in ("bin/oc-test plan broad -- go test ./...", "go test -run=TestProof -skip=TestProof ./internal/demo", "go test ./internal/other"):
            self.assertFalse(anchors.invocation_reaches(self.root, invocation, test))

    def test_validator_binding_derives_transitive_calls_not_strings(self) -> None:
        import evidence_anchors as anchors

        (self.root / "scripts").mkdir()
        (self.root / "scripts/check-outer.py").write_text(
            "import subprocess\nsubprocess.run(['python3', 'scripts/check-inner.py'])\n"
        )
        (self.root / "scripts/check-inner.py").write_text("raise SystemExit(0)\n")
        owner = {"kind": "validator", "value": "scripts/check-inner.py"}
        self.assertTrue(anchors.invocation_reaches(self.root, "python3 scripts/check-outer.py", owner))
        self.assertFalse(anchors.invocation_reaches(self.root, "echo scripts/check-inner.py", owner))

    def test_continuity_inventory_cannot_drop_an_unmentioned_scenario(self) -> None:
        entry = self.coverage_fixture()
        (self.root / ".concord/scenarios/adapter-continuity.v1.json").write_text(json.dumps({
            "scenarios": [{"id": "unclaimed-proof"}],
        }))
        self.manifest([entry])
        self.assertTrue(any("no consuming-test owner: unclaimed-proof" in f for f in checker.check(root=self.root)))

    def test_binding_rejects_nested_duplicate_and_deferred_scenarios(self) -> None:
        entry = self.coverage_fixture()
        self.manifest([entry])
        corpus = self.root / ".concord/scenarios/demo.json"
        for scenarios in ([{"id": "other", "then": [{"id": "scenario-proof"}]}],
                          [{"id": "scenario-proof"}, {"id": "scenario-proof"}]):
            corpus.write_text(json.dumps({"scenarios": scenarios}))
            self.assertTrue(any("unique non-deferred" in f for f in checker.check(root=self.root)))
        corpus.write_text(json.dumps({"scenarios": [{"id": "scenario-proof"}]}))
        (self.root / "internal").mkdir()
        (self.root / "internal/deferred_test.go").write_text('Deferrals["scenario-proof"] = "not executed"')
        self.assertTrue(any("unique non-deferred" in f for f in checker.check(root=self.root)))

    def test_generated_parity_requires_reachable_artifact_reader(self) -> None:
        (self.root / "scripts").mkdir()
        (self.root / "generated.ts").write_text("// Code generated by generator; DO NOT EDIT.\nexport const parity = 1;\n")
        script = self.root / "scripts/check-parity.py"
        script.write_text("print('unrelated')\n")
        (self.root / ".github/workflows/ci.yml").write_text("        run: python3 scripts/check-parity.py\n")
        self.manifest([tool(invocation="python3 scripts/check-parity.py", coverage=[{
            "anchor": {"kind": "generated", "value": "generated.ts#parity"},
            "owner": {"kind": "validator", "value": "scripts/check-parity.py"},
        }])])
        self.assertTrue(any("does not reference" in f for f in checker.check(root=self.root)))
        script.write_text("from pathlib import Path\nassert 'parity' in Path('generated.ts').read_text()\n")
        self.assertEqual(checker.check(root=self.root), [])

    def test_go_scenario_requires_asserting_named_consumer_and_ci_selection(self) -> None:
        import evidence_anchors as anchors

        entry = self.coverage_fixture("go test ./internal/demo")
        entry["coverage"][0]["owner"] = {"kind": "go_test", "value": "internal/demo.TestProof"}
        package = self.root / "internal/demo"
        package.mkdir(parents=True)
        source = package / "proof_test.go"
        source.write_text('package demo\nfunc TestProof(t *testing.T) { t.Run("scenario-proof", func(t *testing.T) { t.Fatal("proof") }) }\n')
        self.manifest([entry])
        with mock.patch.object(anchors, "_load_go_test_facts", return_value={"internal/demo": {"TestProof": True}}):
            self.assertEqual(checker.check(root=self.root), [])
            source.write_text('package demo\nconst proofID = "scenario-proof"\nfunc TestProof(t *testing.T) { t.Run(proofID, func(t *testing.T) { t.Fatal("proof") }) }\n')
            self.assertEqual(checker.check(root=self.root), [])
            source.write_text('package demo\nfunc TestProof(t *testing.T) {\n// t.Run("scenario-proof", func(t *testing.T) {})\nt.Fatal("unrelated")\n}\n')
            self.assertTrue(any("consuming test" in f for f in checker.check(root=self.root)))
            source.write_text('package demo\nfunc TestProof(t *testing.T) { t.Fatal("unrelated") }\n')
            self.assertTrue(any("consuming test" in f for f in checker.check(root=self.root)))
        with mock.patch.object(anchors, "_load_go_test_facts", return_value={"internal/demo": {"TestProof": False}}):
            self.assertTrue(any("owner does not resolve" in f for f in checker.check(root=self.root)))

    def test_bun_report_flags_preserve_selection_but_skips_do_not(self) -> None:
        import evidence_anchors as anchors

        owner = {"kind": "adapter_test", "value": "adapter/opencode/demo.test.ts#scenario-proof"}
        base = "bun test adapter/opencode/ --reporter=junit --reporter-outfile=report.xml --timings=timings.json --update-timings"
        self.assertTrue(anchors.invocation_reaches(self.root, base, owner))
        self.assertTrue(anchors.invocation_reaches(self.root, base + " --path-ignore-patterns=adapter/opencode/other.test.ts", owner))
        self.assertFalse(anchors.invocation_reaches(self.root, base + " --path-ignore-patterns=adapter/opencode/*.test.ts", owner))
        self.assertFalse(anchors.invocation_reaches(self.root, base + " --test-name-pattern=unrelated", owner))

    def test_coverage_schema_and_validator_enumerations_match(self) -> None:
        import evidence_anchors as anchors

        definitions = json.loads((SCRIPT.parents[1] / "contracts/project-tooling.v1.schema.json").read_text())["$defs"]
        self.assertEqual(definitions["tool"]["properties"]["coverage"]["maxItems"], anchors.MAX_COVERAGE)
        self.assertEqual(definitions["coverage_anchor"]["properties"]["kind"]["enum"], list(anchors.COVERAGE_KINDS))
        self.assertEqual(definitions["coverage_owner"]["properties"]["kind"]["enum"], list(anchors.OWNER_KINDS))


if __name__ == "__main__":
    unittest.main()

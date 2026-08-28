#!/usr/bin/env python3
import copy, importlib.util, json, re, unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("generator", ROOT / "scripts/generate-agent-contracts.py")
generator = importlib.util.module_from_spec(spec); spec.loader.exec_module(generator)
manifest = json.loads((ROOT / "contracts/agent-tool-surface.v1.json").read_text())
ir = json.loads((ROOT / "contracts/agent-tool-surface.schema.json").read_text())
checker_spec = importlib.util.spec_from_file_location("lane_checker", ROOT / "scripts/check-agent-contracts.py")
lane_checker = importlib.util.module_from_spec(checker_spec); checker_spec.loader.exec_module(lane_checker)
lanes_schema = json.loads((ROOT / "contracts/agent-lanes.schema.json").read_text())
report_schema = json.loads((ROOT / "contracts/agent-lane-report.schema.json").read_text())
lane_registry = json.loads((ROOT / "contracts/agent-lanes.v1.json").read_text())
packet_schema = json.loads((ROOT / "contracts/agent-lane-packet.schema.json").read_text())
envelope_schema = json.loads((ROOT / "contracts/agent-tool-envelope.schema.json").read_text())

class ManifestTamperTests(unittest.TestCase):
    def assert_rejected(self, value):
        with self.assertRaises(ValueError):
            generator.validate_persisted_manifest(value, ir)

    def test_unknown_field(self):
        value=copy.deepcopy(manifest); value["unexpected"]=True; self.assert_rejected(value)
    def test_invalid_enum(self):
        value=copy.deepcopy(manifest); value["operations"][0]["capability"]="not-a-capability"; self.assert_rejected(value)
    def test_missing_operation_pairing(self):
        value=copy.deepcopy(manifest); value["tools"][0]["operations"].pop()
        with self.assertRaises(ValueError): generator.validate(value)
    def test_changed_schema_ref(self):
        value=copy.deepcopy(manifest); value["schemas"]["product_context"]["ref"]="contracts/missing.json#/$defs/product_context"
        with self.assertRaises(ValueError): generator.validate(value)
    def test_duplicate_id(self):
        value=copy.deepcopy(manifest); value["operations"][1]["id"]=value["operations"][0]["id"]
        with self.assertRaises(ValueError): generator.validate(value)
    def test_missing_coverage(self):
        value=copy.deepcopy(manifest); value["tools"][0]["operations"].append("concord_product_view.missing")
        with self.assertRaises(ValueError): generator.validate(value)

class Ts2BudgetTests(unittest.TestCase):
    """TS2's tool budget is law in the document and in the manifest; CI joins them."""

    def test_document_budget_matches_manifest(self):
        manifest = json.loads((ROOT / "contracts/agent-tool-surface.v1.json").read_text())
        document = (ROOT / "docs/agent-tool-surface-budget.md").read_text()
        matches = re.findall(r"always_visible_tools: (\d+)", document)
        self.assertEqual(len(matches), 1, "TS2 must state the budget exactly once")
        declared = int(matches[0])
        self.assertEqual(manifest["surface"]["tool_count"], declared,
                         "manifest surface.tool_count disagrees with the TS2 budget")
        self.assertEqual(len(manifest["tools"]), declared,
                         "manifest tool list disagrees with the TS2 budget")

    def test_a_shrunk_document_budget_is_rejected(self):
        manifest = json.loads((ROOT / "contracts/agent-tool-surface.v1.json").read_text())
        document = (ROOT / "docs/agent-tool-surface-budget.md").read_text().replace(
            "always_visible_tools: 10", "always_visible_tools: 9")
        with self.assertRaises(AssertionError):
            matches = re.findall(r"always_visible_tools: (\d+)", document)
            declared = int(matches[0])
            self.assertEqual(manifest["surface"]["tool_count"], declared)

    def test_a_manifest_tool_count_drift_is_rejected(self):
        manifest = json.loads((ROOT / "contracts/agent-tool-surface.v1.json").read_text())
        drifted = copy.deepcopy(manifest)
        drifted["surface"]["tool_count"] = drifted["surface"]["tool_count"] - 1
        document = (ROOT / "docs/agent-tool-surface-budget.md").read_text()
        matches = re.findall(r"always_visible_tools: (\d+)", document)
        declared = int(matches[0])
        self.assertNotEqual(drifted["surface"]["tool_count"], declared)

class EvidenceObligationVocabularyTests(unittest.TestCase):
    """CD-0056 D2: the obligation vocabulary is one closed set across two contracts."""

    def findings(self, lanes=None, report=None, registry=None):
        return lane_checker.evidence_obligation_findings(
            copy.deepcopy(lanes if lanes is not None else lanes_schema),
            copy.deepcopy(report if report is not None else report_schema),
            copy.deepcopy(registry if registry is not None else lane_registry),
        )

    def test_shipped_contracts_agree(self):
        self.assertEqual(self.findings(), [])

    def test_divergent_enums_are_rejected(self):
        value = copy.deepcopy(report_schema); value["$defs"]["evidence_obligation"]["enum"].remove("severity")
        self.assertTrue(any("vocabulary differs" in f for f in self.findings(report=value)))

    def test_reordered_enums_are_rejected(self):
        value = copy.deepcopy(report_schema); value["$defs"]["evidence_obligation"]["enum"].reverse()
        self.assertTrue(any("ordered differently" in f for f in self.findings(report=value)))

    def test_lane_obligation_outside_the_vocabulary_is_rejected(self):
        value = copy.deepcopy(lane_registry); value["lanes"][0]["evidence_obligations"].append("vibes")
        self.assertTrue(any("outside the closed vocabulary" in f for f in self.findings(registry=value)))

    def test_missing_definition_is_rejected(self):
        value = copy.deepcopy(lanes_schema); del value["$defs"]["evidence_obligation"]
        self.assertTrue(any("missing $defs/evidence_obligation/enum" in f for f in self.findings(lanes=value)))

    def test_duplicated_token_is_rejected(self):
        value = copy.deepcopy(lanes_schema); value["$defs"]["evidence_obligation"]["enum"].append("severity")
        self.assertTrue(any("duplicate-free" in f for f in self.findings(lanes=value)))

class CD0043LaneMethodologyTests(unittest.TestCase):
    """Issue #461: CD-0043's verification facts are asserted, not narrated."""

    def findings(self, packet=None, registry=None, skills=None):
        return lane_checker.cd0043_lane_methodology_findings(
            copy.deepcopy(packet if packet is not None else packet_schema),
            copy.deepcopy(registry if registry is not None else lane_registry),
            list(skills if skills is not None else ["README.md"]),
        )

    def test_shipped_contracts_satisfy(self):
        self.assertEqual(self.findings(), [])

    def test_methodology_property_is_rejected(self):
        value = copy.deepcopy(packet_schema)
        value["properties"]["methodology"] = {"type": "string", "minLength": 1, "maxLength": 64}
        self.assertTrue(any("'methodology' property is rejected" in f for f in self.findings(packet=value)))

    def test_registry_version_bump_is_rejected(self):
        value = copy.deepcopy(lane_registry); value["version"] = 2
        self.assertTrue(any("pinned version 1" in f for f in self.findings(registry=value)))

    def test_extra_skill_entry_is_rejected(self):
        findings = self.findings(skills=["README.md", "review-rubric.md"])
        self.assertTrue(any("README.md only" in f for f in findings))
        self.assertTrue(any("review-rubric.md" in f for f in findings))

    def test_empty_skills_boundary_is_rejected(self):
        self.assertTrue(any("README.md only" in f for f in self.findings(skills=[])))

class EnvelopeOperationVocabularyTests(unittest.TestCase):
    """Issue #352: every tool/operation pair the envelope declares must be satisfiable."""

    def findings(self, envelope=None):
        return lane_checker.envelope_operation_findings(
            copy.deepcopy(envelope if envelope is not None else envelope_schema),
        )

    def test_shipped_contracts_agree(self):
        self.assertEqual(self.findings(), [])

    def test_operation_removed_from_the_base_enum_is_rejected(self):
        value = copy.deepcopy(envelope_schema); value["$defs"]["base"]["properties"]["operation"]["enum"].remove("continuity")
        found = self.findings(value)
        self.assertTrue(any("unsatisfiable" in f and "concord_work_trace.continuity" in f for f in found), found)

    def test_operation_removed_from_the_next_intent_enum_is_rejected(self):
        value = copy.deepcopy(envelope_schema); value["$defs"]["nextIntent"]["properties"]["operation"]["enum"].remove("messages")
        found = self.findings(value)
        self.assertTrue(any("nextIntent" in f and "concord_work_browse.messages" in f for f in found), found)

    def test_new_tool_operation_pair_without_an_enum_entry_is_rejected(self):
        value = copy.deepcopy(envelope_schema)
        value["$defs"]["toolOperation"]["oneOf"].append({
            "required": ["tool", "operation"], "not": {"required": ["query_id"]},
            "properties": {"tool": {"const": "concord_work_browse"}, "operation": {"const": "forecast"}},
        })
        found = self.findings(value)
        self.assertTrue(any("concord_work_browse.forecast" in f for f in found), found)
        self.assertEqual(len([f for f in found if "concord_work_browse.forecast" in f]), 2, found)

    def test_new_pair_whose_query_id_the_pattern_rejects_is_rejected(self):
        value = copy.deepcopy(envelope_schema)
        value["$defs"]["toolOperation"]["oneOf"].append({
            "required": ["tool", "operation", "query_id"],
            "properties": {"tool": {"const": "concord_work_browse"}, "operation": {"const": "scope"}, "query_id": {"const": "PM1.Q99"}},
        })
        found = self.findings(value)
        self.assertTrue(any("query_id" in f and "PM1.Q99" in f for f in found), found)

    def test_write_operations_absent_from_tool_operation_are_not_findings(self):
        # Containment is one-way: the enum may name operations no read branch pairs.
        value = copy.deepcopy(envelope_schema)
        for definition in ("base", "nextIntent"):
            value["$defs"][definition]["properties"]["operation"]["enum"].append("unpaired_write")
        self.assertEqual(self.findings(value), [])

    def test_missing_tool_operation_definition_is_rejected(self):
        value = copy.deepcopy(envelope_schema); del value["$defs"]["toolOperation"]["oneOf"]
        self.assertTrue(any("missing $defs/toolOperation/oneOf" in f for f in self.findings(value)))

    def test_duplicated_enum_token_is_rejected(self):
        value = copy.deepcopy(envelope_schema); value["$defs"]["base"]["properties"]["operation"]["enum"].append("scope")
        self.assertTrue(any("duplicate-free" in f for f in self.findings(value)))

    def test_uncompilable_query_id_pattern_is_rejected(self):
        value = copy.deepcopy(envelope_schema); value["$defs"]["base"]["properties"]["query_id"]["pattern"] = "^(PM1"
        self.assertTrue(any("not a valid regular expression" in f for f in self.findings(value)))

class AdapterHostPinTests(unittest.TestCase):
    """The pin replaced a hand-written mirror of the host surface.

    The mirror could only be wrong in the direction nothing checked, so these
    tests hold the two properties that keep the replacement honest: the pin
    names a reviewed set of declarations, and the diagnostics it allows are a
    budget rather than an exemption.
    """

    def setUp(self):
        self.pin = json.loads((ROOT / "docs/adapter-host-pin.v1.json").read_text())
        self.schema = json.loads((ROOT / "contracts/adapter-host-pin.schema.json").read_text())
        # Tamper cases run against a synthetic pin, not the shipped one. A test
        # that mutates the real manifest's allowances asserts the repository
        # still carries that debt, and would fail when the debt is paid off.
        self.fixture = {
            "schema_version": "1.0",
            "sources": ["adapter/opencode"],
            "packages": [
                {"name": "@example/host", "version": "1.0.0", "declares": "the host tool surface"},
                {"name": "runtime-types", "version": "2.0.0", "declares": "the ambient runtime globals"},
            ],
            "typescript": "5.9.3",
            "compiler_options": {
                "target": "es2022", "module": "esnext", "moduleResolution": "bundler",
                "lib": ["es2022"], "types": ["runtime-types"], "strict": True, "noEmit": True,
                "skipLibCheck": True, "resolveJsonModule": True, "forceConsistentCasingInFileNames": True,
            },
            "allowances": [{"file": "example.ts", "code": "TS2322", "count": 1, "state": "outstanding", "issue": 1, "reason": "a recorded divergence"}],
        }

    def findings(self, mutate=None, pin=None):
        value = copy.deepcopy(self.fixture if pin is None else pin)
        if mutate:
            mutate(value)
        found = []
        lane_checker.validate_host_pin(value, self.schema, found)
        return found

    def test_shipped_pin_satisfies_its_contract(self):
        self.assertEqual(self.findings(pin=self.pin), [])

    def test_fixture_is_valid_so_tamper_cases_isolate_one_defect(self):
        self.assertEqual(self.findings(), [])

    def test_ambient_type_package_that_is_not_pinned_is_rejected(self):
        def mutate(value):
            value["compiler_options"]["types"].append("node")
        self.assertTrue(any("names no pinned package" in f for f in self.findings(mutate)))

    def test_outstanding_allowance_without_an_issue_is_rejected(self):
        def mutate(value):
            del value["allowances"][0]["issue"]
        self.assertTrue(any("carries no tracking issue" in f for f in self.findings(mutate)))

    def test_settled_allowance_carrying_an_issue_is_rejected(self):
        def mutate(value):
            value["allowances"][0]["state"] = "out_of_scope"
        self.assertTrue(any("must not carry an issue" in f for f in self.findings(mutate)))

    def test_version_range_is_rejected(self):
        def mutate(value):
            value["packages"][0]["version"] = "^1.18.23"
        self.assertTrue(any("does not satisfy its contract" in f for f in self.findings(mutate)))

    def test_non_strict_typecheck_is_rejected(self):
        # A non-strict run against real declarations proves less than the
        # mirror it replaced, so the contract pins strict rather than defaulting it.
        def mutate(value):
            value["compiler_options"]["strict"] = False
        self.assertTrue(any("does not satisfy its contract" in f for f in self.findings(mutate)))

    def test_allowance_states_match_the_contract_vocabulary(self):
        declared = set(self.schema["properties"]["allowances"]["items"]["properties"]["state"]["enum"])
        self.assertEqual(declared, {"outstanding", "unmeasured", "out_of_scope"})
        for allowance in self.pin["allowances"]:
            self.assertIn(allowance["state"], declared)

    def test_every_shipped_outstanding_allowance_names_a_tracking_issue(self):
        for allowance in self.pin["allowances"]:
            if allowance["state"] == "outstanding":
                self.assertIsInstance(allowance.get("issue"), int)

    def test_pinned_bun_types_match_the_bun_the_workflow_installs(self):
        # The ambient runtime surface the typecheck asserts and the runtime CI
        # actually runs are two claims about one thing. Pinning them separately
        # is how they would come apart without anything noticing.
        workflow = (ROOT / ".github/workflows/ci.yml").read_text()
        installed = re.search(r"bun-version:\s*(\S+)", workflow)
        self.assertIsNotNone(installed, "ci.yml declares no bun-version")
        pinned = [package["version"] for package in self.pin["packages"] if package["name"] == "bun-types"]
        self.assertEqual(pinned, [installed.group(1)])


class AdapterHostDiagnosticTests(unittest.TestCase):
    OUTPUT = "src/concord.ts(244,124): error TS2322: Type 'x' is not assignable to type 'y'.\n  Type 'a' is not assignable to type 'b'.\n"

    def reconcile(self, output, allowances, exit_code=0):
        found = []
        lane_checker.reconcile_diagnostics(output, allowances, found, exit_code)
        return found

    def allowance(self, **overrides):
        base = {"file": "concord.ts", "code": "TS2322", "count": 1, "state": "outstanding", "issue": 560, "reason": "recorded divergence"}
        base.update(overrides)
        return base

    def test_recorded_divergence_at_its_recorded_size_passes(self):
        self.assertEqual(self.reconcile(self.OUTPUT, [self.allowance()]), [])

    def test_continuation_lines_are_not_counted_as_diagnostics(self):
        # tsc indents the explanation under the diagnostic. Counting those lines
        # would inflate every allowance by however much detail tsc chose to print.
        self.assertEqual(self.reconcile(self.OUTPUT, [self.allowance(count=2)]), ["allowance drift: concord.ts emits 1 TS2322 diagnostic(s), the manifest records 2"])

    def test_unrecorded_diagnostic_is_a_finding(self):
        found = self.reconcile(self.OUTPUT, [])
        self.assertTrue(any("does not satisfy the pinned host declarations" in f for f in found), found)

    def test_spent_allowance_is_a_finding(self):
        found = self.reconcile("", [self.allowance()])
        self.assertTrue(any("stale allowance" in f for f in found), found)

    def test_allowance_matches_by_file_and_code_not_position(self):
        moved = self.OUTPUT.replace("(244,124)", "(999,1)")
        self.assertEqual(self.reconcile(moved, [self.allowance()]), [])

    def test_allowance_does_not_cover_a_different_code_in_the_same_file(self):
        other = "src/concord.ts(12,1): error TS2345: Argument of type 'x'.\n"
        found = self.reconcile(self.OUTPUT + other, [self.allowance()])
        self.assertTrue(any("TS2345" in f for f in found), found)

    def test_positionless_diagnostic_is_a_finding(self):
        # tsc reports "no inputs were found" without a source position. Silently
        # skipping it would let the check pass over a typecheck of nothing.
        found = self.reconcile("error TS18003: No inputs were found in config file.\n", [], 1)
        self.assertTrue(any("no source position" in f for f in found), found)

    def test_nonzero_exit_without_any_diagnostic_is_a_finding(self):
        found = self.reconcile("bun: command failed\n", [], 1)
        self.assertTrue(any("without reporting a diagnostic" in f for f in found), found)

    def test_clean_run_reports_nothing(self):
        self.assertEqual(self.reconcile("", [], 0), [])


if __name__ == "__main__": unittest.main()

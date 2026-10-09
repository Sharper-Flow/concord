#!/usr/bin/env python3
import copy, importlib.util, io, json, os, re, tempfile, unittest, unittest.mock
from contextlib import ExitStack, redirect_stderr, redirect_stdout
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
payload_schema = json.loads((ROOT / "contracts/agent-tool-surface-payloads.schema.json").read_text())

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

class EvidenceFixedBudgetCeilingTests(unittest.TestCase):
    """CD-0038 D2: the surface ceiling is uniform except where accepted
    evidence fixes a different value. The generator admits only the
    declared exceptions (workflow_action TS1 30; worktree_verify CON-317
    1800); any other non-uniform value, including one that mimics an
    evidence-fixed value on a different operation, must fail closed so the
    rule cannot erode by accretion.
    """

    def _replace_supported_budget(self, operation_id, value):
        value_dict = copy.deepcopy(manifest)
        for op in value_dict["operations"]:
            if op["id"] == operation_id:
                op["supported_budget_seconds"] = value
        return value_dict

    def test_shipped_manifest_admits_only_declared_evidence_fixed_ceilings(self):
        # The shipped manifest declares exactly two non-uniform ceilings
        # (workflow_action 30 and worktree_verify 1800); validate() enforces
        # that, and the rest of the surface must stay at the uniform 300.
        try:
            generator.validate(manifest)
        except ValueError as err:
            self.fail(f"shipped manifest violates its own uniformity rule: {err}")
        non_uniform = {op["id"]: op["supported_budget_seconds"] for op in manifest["operations"]
                       if op["supported_budget_seconds"] != 300}
        self.assertEqual(non_uniform, {
            "concord_work_transition.workflow_action": 30,
            "concord_work_transition.worktree_verify": 1800,
        })

    def test_a_uniform_seven_minute_ceiling_is_rejected(self):
        # A budget value no evidence pins is still over the uniform 300 and
        # the generator refuses it before the rule reach can widen.
        drifted = self._replace_supported_budget("concord_work_transition.lifecycle", 420)
        with self.assertRaises(ValueError):
            generator.validate(drifted)

    def test_an_undeclared_non_uniform_ceiling_is_rejected(self):
        # A ceiling the evidence list does not admit (one operation, a value
        # no rule fixes) breaks the uniformity discipline even when the
        # value would otherwise lie in range.
        drifted = self._replace_supported_budget("concord_work_transition.lifecycle", 600)
        with self.assertRaises(ValueError):
            generator.validate(drifted)

    def test_an_evidence_value_on_an_undeclared_operation_is_rejected(self):
        # 1800 is a declared ceiling for worktree_verify only. Putting it on
        # another operation copies the value without the evidence that
        # names the operation; the rule's job is to refuse that copy.
        drifted = self._replace_supported_budget("concord_work_transition.lifecycle", 1800)
        with self.assertRaises(ValueError):
            generator.validate(drifted)


class EnvelopeOperationCoverageTests(unittest.TestCase):
    """The manifest is the source of truth for what `toolOperation` must pair.

    The sibling check proves a declared pair stays satisfiable. This proves the
    other direction, which nothing held: an operation the surface declares but
    the envelope never enumerates has no satisfiable branch, so the adapter
    answers malformed_core_response for a well-formed core result.
    """
    def test_the_shipped_contracts_cover_every_declared_operation(self):
        self.assertEqual(lane_checker.envelope_operation_coverage_findings(envelope_schema, manifest), [])

    def test_an_unenumerated_operation_is_rejected(self):
        value = copy.deepcopy(envelope_schema)
        removed = None
        for index, branch in enumerate(value["$defs"]["toolOperation"]["oneOf"]):
            properties = branch.get("properties", {})
            if properties.get("query_id", {}).get("const") == "PM1.Q1":
                removed = value["$defs"]["toolOperation"]["oneOf"].pop(index)
                break
        self.assertIsNotNone(removed, "fixture lost the branch this test removes")
        findings = lane_checker.envelope_operation_coverage_findings(value, manifest)
        self.assertTrue(findings)
        self.assertIn("concord_product_view.resolve", findings[0])

    def test_a_query_id_that_disagrees_with_the_manifest_is_rejected(self):
        value = copy.deepcopy(envelope_schema)
        for branch in value["$defs"]["toolOperation"]["oneOf"]:
            if branch.get("properties", {}).get("query_id", {}).get("const") == "PM1.Q1":
                branch["properties"]["query_id"]["const"] = "PM1.Q99"
                break
        findings = lane_checker.envelope_operation_coverage_findings(value, manifest)
        self.assertTrue(findings)
        self.assertIn("PM1.Q99", " ".join(findings))

class Ts2BudgetTests(unittest.TestCase):
    """TS2's tool budget is law in the document and in the manifest; CI joins them."""

    def test_document_budget_matches_manifest(self):
        manifest = json.loads((ROOT / "contracts/agent-tool-surface.v1.json").read_text())
        document = (ROOT / ".concord/docs/agent-tool-surface-budget.md").read_text()
        matches = re.findall(r"always_visible_tools: (\d+)", document)
        self.assertEqual(len(matches), 1, "TS2 must state the budget exactly once")
        declared = int(matches[0])
        self.assertEqual(manifest["surface"]["tool_count"], declared,
                         "manifest surface.tool_count disagrees with the TS2 budget")
        self.assertEqual(len(manifest["tools"]), declared,
                         "manifest tool list disagrees with the TS2 budget")

    def test_a_shrunk_document_budget_is_rejected(self):
        manifest = json.loads((ROOT / "contracts/agent-tool-surface.v1.json").read_text())
        document = (ROOT / ".concord/docs/agent-tool-surface-budget.md").read_text().replace(
            "always_visible_tools: 10", "always_visible_tools: 9")
        with self.assertRaises(AssertionError):
            matches = re.findall(r"always_visible_tools: (\d+)", document)
            declared = int(matches[0])
            self.assertEqual(manifest["surface"]["tool_count"], declared)

    def test_a_manifest_tool_count_drift_is_rejected(self):
        manifest = json.loads((ROOT / "contracts/agent-tool-surface.v1.json").read_text())
        drifted = copy.deepcopy(manifest)
        drifted["surface"]["tool_count"] = drifted["surface"]["tool_count"] - 1
        document = (ROOT / ".concord/docs/agent-tool-surface-budget.md").read_text()
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

class EvidenceBoundParityTests(unittest.TestCase):
    """The request evidence declaration owns every evidence field bound.

    The result schema $defs/evidenceRef must carry the same bounds: authority
    and locator_kind 1..256, digest hex pattern 8..128, version <=256, locator
    1..2048. A mismatch admits evidence values the request would refuse, or
    refuses values the request admitted; both ways the parity check at
    generation time is the only thing that holds them in lockstep.
    """

    def check(self, payload=None, envelope=None):
        generator.check_evidence_bound_parity(
            copy.deepcopy(payload if payload is not None else payload_schema),
            copy.deepcopy(envelope if envelope is not None else envelope_schema),
        )

    def test_shipped_schemas_agree(self):
        # No exception means the bounds agree across both declarations.
        self.check()

    def test_a_shrunken_envelope_authority_bound_is_rejected(self):
        value = copy.deepcopy(envelope_schema)
        value["$defs"]["evidenceRef"]["properties"]["authority"]["maxLength"] = 128
        with self.assertRaises(ValueError) as ctx:
            self.check(envelope=value)
        self.assertIn("authority", str(ctx.exception))

    def test_an_envelope_locator_kind_bound_divergence_is_rejected(self):
        value = copy.deepcopy(envelope_schema)
        value["$defs"]["evidenceRef"]["properties"]["locator_kind"]["maxLength"] = 64
        with self.assertRaises(ValueError) as ctx:
            self.check(envelope=value)
        self.assertIn("locator_kind", str(ctx.exception))

    def test_a_payload_authority_bound_drift_is_rejected(self):
        # A request-side drift is also a mismatch. The request evidence
        # reaches authority through $defs/short, while the envelope inlines
        # its bound, so narrowing `short` alone must fire the parity check.
        value = copy.deepcopy(payload_schema)
        value["$defs"]["short"]["maxLength"] = 128
        with self.assertRaises(ValueError) as ctx:
            self.check(payload=value)
        self.assertIn("authority", str(ctx.exception))

    def test_an_envelope_digest_pattern_drift_is_rejected(self):
        # The digest bound includes its pattern. A different pattern string,
        # here one that drops the optional sha256: prefix and uppercase hex,
        # is a mismatch even when the length bounds agree.
        value = copy.deepcopy(envelope_schema)
        value["$defs"]["evidenceRef"]["properties"]["digest"]["pattern"] = r"^[a-f0-9]+$"
        with self.assertRaises(ValueError) as ctx:
            self.check(envelope=value)
        self.assertIn("digest", str(ctx.exception))

    def test_a_payload_locator_maxlength_drift_is_rejected(self):
        value = copy.deepcopy(payload_schema)
        value["$defs"]["evidence"]["properties"]["locator"]["maxLength"] = 1024
        with self.assertRaises(ValueError) as ctx:
            self.check(payload=value)
        self.assertIn("locator", str(ctx.exception))

    def test_a_payload_version_maxlength_drift_is_rejected(self):
        value = copy.deepcopy(payload_schema)
        value["$defs"]["evidence"]["properties"]["version"]["maxLength"] = 128
        with self.assertRaises(ValueError) as ctx:
            self.check(payload=value)
        self.assertIn("version", str(ctx.exception))

    def test_missing_envelope_evidence_ref_is_rejected(self):
        value = copy.deepcopy(envelope_schema)
        del value["$defs"]["evidenceRef"]
        with self.assertRaises(ValueError):
            self.check(envelope=value)

    def test_missing_payload_evidence_is_rejected(self):
        value = copy.deepcopy(payload_schema)
        del value["$defs"]["evidence"]
        with self.assertRaises(ValueError):
            self.check(payload=value)


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
        self.pin = json.loads((ROOT / ".concord/docs/adapter-host-pin.v1.json").read_text())
        self.schema = json.loads((ROOT / "contracts/adapter-host-pin.schema.json").read_text())
        # Tamper cases run against a synthetic pin, not the shipped one. A test
        # that mutates the real manifest's allowances asserts the repository
        # still carries that debt, and would fail when the debt is paid off.
        self.fixture = {
            "schema_version": "1.0",
            "sources": ["adapter/opencode"],
            "packages": [
                {"name": "@opencode-ai/plugin", "version": "1.0.0", "declares": "the host tool surface"},
                {"name": "runtime-types", "version": "2.0.0", "declares": "the ambient runtime globals"},
            ],
            "typescript": "5.9.3",
            "compiler_options": {
                "target": "es2022", "module": "esnext", "moduleResolution": "bundler",
                "lib": ["es2022"], "types": ["runtime-types"], "strict": True, "noEmit": True,
                "skipLibCheck": True, "resolveJsonModule": True, "forceConsistentCasingInFileNames": True,
                "allowJs": True,
            },
            "allowances": [{"file": "example.ts", "code": "TS2322", "count": 1, "state": "outstanding", "issue": 1, "reason": "a recorded divergence"}],
            "runtime_probe": {
                "probe_identity": "tool-result-contract-v1", "runner": "python3 scripts/probe-adapter-host-result.py", "observed_on": "2026-08-29", "host_package": "opencode-ai", "host_version": "1.0.0",
                "source_urls": ["https://example.com/registry", "https://example.com/truncate"],
                "commands": {"bare": "bunx opencode-ai@1.0.0 --pure debug agent build --tool result-probe_bare --params '{}'", "conforming": "bunx opencode-ai@1.0.0 --pure debug agent build --tool result-probe_conforming --params '{}'"},
                "bare_object_failure": {"status": "failed", "exit_code": 1, "error_contains": "undefined is not an object (evaluating 'c.split')"},
                "conforming_result_success": {"status": "passed", "exit_code": 0, "output": "{}", "metadata_truncated": False},
                "limits": {"max_bytes": 51200, "max_lines": 2000},
            },
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

    def test_missing_runtime_probe_evidence_is_rejected(self):
        def mutate(value):
            del value["runtime_probe"]
        self.assertTrue(any("does not satisfy its contract" in f for f in self.findings(mutate)))

    def test_stale_runtime_probe_version_is_rejected(self):
        def mutate(value):
            value["runtime_probe"]["host_version"] = "2.0.0"
        self.assertTrue(any("version does not match" in f for f in self.findings(mutate)))

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


class DeliveryDecidableTeachingTests(unittest.TestCase):
    """CD-0184: the authoring schema teaches that acceptance is decidable at
    delivery. The outcome_predicates array shared by approve_contract and
    supersede_contract and the add_condition expected_within_seconds field
    carry the rule, and the generator refuses a projection that drops it.
    """

    def _add_condition_branches(self, defs):
        for condition in defs["work_transition_action_shared_input"]["allOf"]:
            trigger = condition.get("if", {}).get("properties", {}).get("action_id", {}).get("const")
            if trigger != "add_condition":
                continue
            then = condition["then"]
            return then.get("anyOf") or [then]
        return None

    def test_shipped_predicate_array_teaches_the_rule(self):
        items = payload_schema["$defs"]["workflow_action_outcome_predicates"]
        self.assertEqual(generator.DELIVERY_RULE_PREDICATE_DESCRIPTION, items.get("description"))

    def test_shipped_add_condition_wait_teaches_the_rule(self):
        branches = self._add_condition_branches(payload_schema["$defs"])
        self.assertIsNotNone(branches)
        for branch in branches:
            wait = branch["properties"]["fields"]["properties"]["expected_within_seconds"]
            self.assertEqual(generator.DELIVERY_RULE_WAIT_DESCRIPTION, wait.get("description"))

    def test_generator_refuses_a_projection_that_drops_the_rule(self):
        defs = copy.deepcopy(payload_schema["$defs"])
        del defs["workflow_action_outcome_predicates"]["description"]
        with self.assertRaises(ValueError):
            generator.require_delivery_rule_teaching(defs)

    def test_generator_refuses_a_projection_that_drops_the_wait_rule(self):
        defs = copy.deepcopy(payload_schema["$defs"])
        for branch in self._add_condition_branches(defs):
            branch["properties"]["fields"]["properties"]["expected_within_seconds"].pop("description")
        with self.assertRaises(ValueError):
            generator.require_delivery_rule_teaching(defs)

    def test_generator_refuses_a_projection_without_the_add_condition_condition(self):
        defs = copy.deepcopy(payload_schema["$defs"])
        conditions = defs["work_transition_action_shared_input"]["allOf"]
        defs["work_transition_action_shared_input"]["allOf"] = [
            condition for condition in conditions
            if condition.get("if", {}).get("properties", {}).get("action_id", {}).get("const") != "add_condition"
        ]
        with self.assertRaises(ValueError):
            generator.require_delivery_rule_teaching(defs)

    def test_generator_refuses_a_predicate_description_reduced_to_markers(self):
        defs = copy.deepcopy(payload_schema["$defs"])
        defs["workflow_action_outcome_predicates"]["description"] = "decidable at delivery; raised_from"
        with self.assertRaises(ValueError):
            generator.require_delivery_rule_teaching(defs)

    def test_generator_refuses_a_wait_description_reduced_to_markers(self):
        defs = copy.deepcopy(payload_schema["$defs"])
        for branch in self._add_condition_branches(defs):
            branch["properties"]["fields"]["properties"]["expected_within_seconds"]["description"] = "raised_from; time window"
        with self.assertRaises(ValueError):
            generator.require_delivery_rule_teaching(defs)


class MutationApprovalPropertyTests(unittest.TestCase):
    """Every mutation can reach the cross-Product approval escalation: the
    runtime forces requiresApproval when the derived Product scope crosses the
    selected Product, and the approved resubmission carries input.approval.
    Each mutation input must declare the optional typed approval property, and
    the generator refuses a mutation input without it, so the declared surface
    and the runtime rule cannot disagree.
    """

    def mutation_approval_refusals(self, defs):
        refusals = []
        for operation in manifest["operations"]:
            if operation["kind"] != "mutation":
                continue
            schema = defs[operation["input_schema"].split("/")[-1]]
            try:
                generator.require_mutation_approval_property(operation, schema)
            except ValueError as err:
                refusals.append(str(err))
        return refusals

    def test_shipped_mutation_inputs_declare_optional_approval(self):
        self.assertEqual(self.mutation_approval_refusals(payload_schema["$defs"]), [])

    def test_a_mutation_input_without_the_approval_property_is_rejected(self):
        defs = copy.deepcopy(payload_schema["$defs"])
        del defs["work_define_observation_record_input"]["properties"]["approval"]
        refusals = self.mutation_approval_refusals(defs)
        self.assertTrue(any("concord_work_define.observation_record" in f for f in refusals), refusals)

    def test_a_required_approval_property_is_rejected(self):
        defs = copy.deepcopy(payload_schema["$defs"])
        schema = defs["work_define_observation_record_input"]
        schema["required"] = list(schema["required"]) + ["approval"]
        refusals = self.mutation_approval_refusals(defs)
        self.assertTrue(any("concord_work_define.observation_record" in f for f in refusals), refusals)

    def test_an_untyped_approval_property_is_rejected(self):
        defs = copy.deepcopy(payload_schema["$defs"])
        defs["work_define_observation_record_input"]["properties"]["approval"] = {"type": "string"}
        refusals = self.mutation_approval_refusals(defs)
        self.assertTrue(any("concord_work_define.observation_record" in f for f in refusals), refusals)


def verify_job_steps() -> list[dict]:
    """The verify job's steps as name/run/env records.

    Pinned to the indents scripts/check-script-tests.py enforces (step names
    at six spaces, `run:` and `env:` at eight, their bodies at ten), so the
    read stays structural and needs no YAML parser on a contributor laptop.
    """
    text = (ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8")
    steps: list[dict] = []
    current: dict | None = None
    section: str | None = None
    in_job = False
    for line in text.splitlines():
        stripped = line.strip()
        if (
            in_job
            and line.startswith("  ")
            and not line.startswith("   ")
            and not stripped.startswith("#")
            and stripped.endswith(":")
        ):
            break  # the next job's key ends the verify job
        if not in_job:
            if line == "  verify:":
                in_job = True
            continue
        if line.startswith("      - name:"):
            current = {"name": line.split(":", 1)[1].strip(), "run": "", "env": {}}
            steps.append(current)
            section = None
            continue
        if current is None:
            continue
        if line.startswith("        run:"):
            inline = line.split(":", 1)[1].strip()
            current["run"] = "" if inline in {"|", ">"} else inline
            section = "run"
        elif line.startswith("        env:"):
            section = "env"
        elif stripped and line.startswith("          "):
            if section == "run":
                current["run"] += "\n" + stripped
            elif section == "env" and ":" in stripped:
                key, _, value = stripped.partition(":")
                current["env"][key.strip()] = value.strip().strip('"')
        elif stripped:
            section = None
    return steps


class CIContractRoutingTests(unittest.TestCase):
    """The complete contract check executes once, through the JSON umbrella.

    The verify job used to run scripts/check-agent-contracts.py twice: once
    directly under CONCORD_REQUIRE_BUN=1 and once nested through
    scripts/check-json.py without it, so the nested copy degraded to the
    contributor-laptop fallbacks whenever Bun was missing. The duplicate
    direct step is gone; the strict environment lives on the umbrella step
    and reaches the nested checker because subprocess.run without `env=`
    inherits the caller's environment. These assertions fail on the old
    wiring (a direct step exists, the umbrella carries no env) and pass on
    the new one.
    """

    def test_the_complete_contract_check_runs_only_through_the_umbrella(self):
        steps = verify_job_steps()
        direct = [step for step in steps if "scripts/check-agent-contracts.py" in step["run"]]
        self.assertEqual(
            direct,
            [],
            f"standalone complete-contract steps must not exist: {[step['name'] for step in direct]}",
        )
        umbrella = [step for step in steps if "scripts/check-json.py" in step["run"]]
        self.assertEqual(len(umbrella), 1, "expected exactly one JSON umbrella step")
        self.assertEqual(
            umbrella[0]["env"].get("CONCORD_REQUIRE_BUN"),
            "1",
            "the umbrella must run under CONCORD_REQUIRE_BUN=1 so the nested check fails closed",
        )

    def test_the_direct_adapter_suite_step_is_retained(self):
        # The nested contract check also runs `bun test adapter/opencode`,
        # but the adapter_test evidence anchors resolve only against a direct
        # workflow invocation, so deleting the direct step would strand them.
        direct = [step for step in verify_job_steps() if "bun test adapter/opencode" in step["run"]]
        self.assertTrue(direct, "the direct bun test adapter/opencode/ step is required coverage")


class _Completed:
    def __init__(self, returncode=0, stdout="", stderr=""):
        self.returncode = returncode
        self.stdout = stdout
        self.stderr = stderr


class _FakeSubprocess:
    """Answers the checker's subprocess orchestration without spawning it.

    Each control below targets one failure branch. The checker's own
    subprocesses (the tamper suite, the generators, the Bun invocations) are
    routed through this fake so the control reaches its branch regardless of
    the host; every validator that runs before those subprocesses is real.
    """

    def __init__(self, results=()):
        self.results = list(results)  # (predicate over the argument words, result)
        self.calls: list[list[str]] = []

    def run(self, args, **kwargs):
        words = [str(arg) for arg in args]
        self.calls.append(words)
        for predicate, result in self.results:
            if predicate(words):
                return result
        return _Completed()


class StrictFailureControls(unittest.TestCase):
    """The umbrella is exactly as strict as its failure branches.

    CONCORD_REQUIRE_BUN=1 turns two contributor-laptop tolerances (Bun not
    installed; the pinned host declarations not installable) into required-
    check failures. Each control exercises the real main() up to its branch,
    faking only the subprocess layer, and each tolerant twin proves the
    environment variable is what fails the check rather than the condition
    alone.
    """

    def _main(self, *, fake, staging, require_bun, which="/fake/bun"):
        environment = {"CONCORD_REQUIRE_BUN": "1"} if require_bun else {}
        patches = [
            unittest.mock.patch.object(lane_checker, "subprocess", fake),
            unittest.mock.patch.object(lane_checker, "stage_host_workspace", staging),
            unittest.mock.patch("shutil.which", return_value=which),
        ]
        if require_bun:
            patches.append(unittest.mock.patch.dict(os.environ, environment))
        else:
            patches.append(unittest.mock.patch.dict(os.environ))
        out, err = io.StringIO(), io.StringIO()
        with ExitStack() as stack:
            for patch in patches:
                stack.enter_context(patch)
            with redirect_stdout(out), redirect_stderr(err):
                if not require_bun:
                    os.environ.pop("CONCORD_REQUIRE_BUN", None)
                code = lane_checker.main()
        return code, out.getvalue(), err.getvalue()

    def test_missing_bun_fails_closed_under_the_strict_environment(self):
        code, _, err = self._main(fake=_FakeSubprocess(), staging=lambda *a: None, require_bun=True, which=None)
        self.assertEqual(code, 1)
        self.assertIn("Bun is not installed", err)

    def test_missing_bun_stays_tolerable_on_a_contributor_laptop(self):
        code, out, _ = self._main(fake=_FakeSubprocess(), staging=lambda *a: None, require_bun=False, which=None)
        self.assertEqual(code, 0)
        self.assertIn("generated-marker fallback only", out)

    def test_an_unreachable_registry_fails_closed_under_the_strict_environment(self):
        error = "host declarations could not be installed: simulated registry unreachable"
        code, _, err = self._main(fake=_FakeSubprocess(), staging=lambda *a: error, require_bun=True)
        self.assertEqual(code, 1)
        self.assertIn(error, err)

    def test_an_unreachable_registry_stays_tolerable_on_a_contributor_laptop(self):
        code, out, _ = self._main(
            fake=_FakeSubprocess(),
            staging=lambda *a: "host declarations could not be installed: simulated",
            require_bun=False,
        )
        self.assertEqual(code, 0)
        self.assertIn("host typecheck NOT run", out)

    def test_a_host_typecheck_failure_fails_the_check(self):
        # TS9999 is not a code the TypeScript compiler emits, so no recorded
        # allowance can absorb the simulated diagnostic; reconcile_diagnostics
        # must report it and main() must propagate the failure.
        typecheck = _Completed(1, "src/concord.ts(1,1): error TS9999: simulated host type failure\n", "")
        fake = _FakeSubprocess(results=[(lambda words: len(words) > 1 and words[1] == "x", typecheck)])
        code, _, err = self._main(fake=fake, staging=lambda *a: None, require_bun=True)
        self.assertEqual(code, 1)
        self.assertIn("TS9999", err)
        self.assertIn("does not satisfy the pinned host declarations", err)

    def test_a_failed_registry_install_reports_a_staging_error(self):
        # The staging mechanism itself, not main()'s handling of it: a Bun
        # binary that cannot reach the registry must surface as the staging
        # error string the strict branch above prints.
        with tempfile.TemporaryDirectory() as directory:
            fake_bun = Path(directory) / "bun"
            fake_bun.write_text("#!/bin/sh\necho 'npm ERR! network unreachable' >&2\nexit 1\n", encoding="utf-8")
            os.chmod(fake_bun, 0o755)
            pin = json.loads((ROOT / ".concord/docs/adapter-host-pin.v1.json").read_text())
            error = lane_checker.stage_host_workspace(str(fake_bun), pin, Path(directory) / "workspace")
        self.assertIsNotNone(error)
        self.assertTrue(error.startswith("host declarations could not be installed"), error)
        self.assertIn("network unreachable", error)

    def test_host_sources_refresh_without_reinstalling_dependencies(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "repository"
            origin = root / "adapter"
            origin.mkdir(parents=True)
            (origin / "current.ts").write_text("export const value = 1;\n")
            (origin / "removed.ts").write_text("export const old = true;\n")
            workspace = Path(directory) / "workspace"
            dependencies = workspace / "node_modules"
            dependencies.mkdir(parents=True)
            marker = dependencies / "installed"
            marker.write_text("pinned dependencies\n")
            package = workspace / "package.json"
            package.write_text('{"private":true}\n')
            lockfile = workspace / "bun.lock"
            lockfile.write_text("pinned lockfile\n")
            pin = {"sources": ["adapter"], "compiler_options": {"strict": True, "noEmit": True}}
            with unittest.mock.patch.object(lane_checker, "ROOT", root), unittest.mock.patch.object(lane_checker.subprocess, "run") as run:
                self.assertIsNone(lane_checker.stage_host_sources(pin, workspace))
                (origin / "current.ts").write_text("export const value = 2;\n")
                (origin / "removed.ts").unlink()
                self.assertIsNone(lane_checker.stage_host_sources(pin, workspace))
                run.assert_not_called()
            self.assertEqual((workspace / "src/current.ts").read_text(), "export const value = 2;\n")
            self.assertFalse((workspace / "src/removed.ts").exists())
            self.assertEqual(marker.read_text(), "pinned dependencies\n")
            self.assertEqual(package.read_text(), '{"private":true}\n')
            self.assertEqual(lockfile.read_text(), "pinned lockfile\n")
            self.assertEqual(json.loads((workspace / "tsconfig.json").read_text()), {"compilerOptions": pin["compiler_options"], "include": ["src/*.ts"]})

    def test_a_plain_js_source_module_stages_beside_the_typescript(self):
        # The adapter ships plain JavaScript source modules that the
        # TypeScript imports directly (worker-report-protocol.js); a staging
        # glob that drops them breaks the import graph the pinned typecheck
        # walks. The .js enters through allowJs when a .ts root imports it,
        # so the include list stays TypeScript-only.
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "repository"
            origin = root / "adapter"
            origin.mkdir(parents=True)
            (origin / "entry.ts").write_text('import { a } from "./plain.js"\n', encoding="utf-8")
            (origin / "plain.js").write_text("export const a = 1\n", encoding="utf-8")
            (origin / "notes.json").write_text("{}\n", encoding="utf-8")
            workspace = Path(directory) / "workspace"
            pin = {"sources": ["adapter"], "compiler_options": {"strict": True, "noEmit": True, "allowJs": True}}
            with unittest.mock.patch.object(lane_checker, "ROOT", root):
                self.assertIsNone(lane_checker.stage_host_sources(pin, workspace))
            self.assertTrue((workspace / "src/entry.ts").is_file())
            self.assertTrue((workspace / "src/plain.js").is_file())
            self.assertTrue((workspace / "src/notes.json").is_file())
            self.assertEqual(json.loads((workspace / "tsconfig.json").read_text()), {"compilerOptions": pin["compiler_options"], "include": ["src/*.ts"]})

    def test_a_broken_contract_fixture_fails_the_check(self):
        # A digest that no longer matches its definition makes a valid-marked
        # fixture invalid; the corpus validators are real, so main() must
        # fail before any subprocess orchestration is reached.
        real_load = lane_checker._load_json

        def broken_fixture(path):
            value = real_load(path)
            if getattr(path, "name", str(path)) == "workflow-engine.fixtures.json":
                for case in value["cases"]:
                    if case["id"] == "definition-implementation-valid":
                        case["instance"]["digest"] = "sha256:" + "0" * 64
            return value

        out, err = io.StringIO(), io.StringIO()
        with unittest.mock.patch.object(lane_checker, "_load_json", broken_fixture):
            with redirect_stdout(out), redirect_stderr(err):
                code = lane_checker.main()
        self.assertEqual(code, 1)
        self.assertIn("expected valid instance", err.getvalue())


if __name__ == "__main__": unittest.main()

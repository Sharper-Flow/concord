#!/usr/bin/env python3
import copy, importlib.util, io, json, os, re, subprocess, tempfile, unittest, unittest.mock
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
            "always_visible_tools: 9", "always_visible_tools: 8")
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


class WorkerPacketMirrorProjectionTests(unittest.TestCase):
    """The TS8 worker_packet inputs mirror is derived from the canonical
    lane packet contract, not handwritten.

    A canonical inline input field must reach the published surface on
    regeneration. A missing projection fails --check before the core can
    refuse the dispatcher's packet as an unknown property.
    """

    def project(self, payload=None, packet=None):
        value = copy.deepcopy(payload_schema if payload is None else payload)
        canonical = copy.deepcopy(packet_schema if packet is None else packet)
        generator.project_worker_packet_inputs(value, canonical)
        return value["$defs"]["worker_packet"]["properties"]["inputs"]

    def test_shipped_mirror_carries_every_canonical_input_field(self):
        canonical = packet_schema["properties"]["inputs"]
        mirror = payload_schema["$defs"]["worker_packet"]["properties"]["inputs"]
        self.assertEqual(sorted(mirror["properties"]), sorted(canonical["properties"]))
        self.assertEqual(mirror.get("required"), canonical.get("required"))

    def test_projection_is_idempotent_on_the_shipped_mirror(self):
        self.assertEqual(self.project(), payload_schema["$defs"]["worker_packet"]["properties"]["inputs"])

    def test_record_fields_keep_shared_aliases_and_canonical_teaching(self):
        projected = self.project()["properties"]
        canonical = packet_schema["properties"]["inputs"]["properties"]
        aliases = {
            "law_context": "workflow_law_context",
            "design_record": "workflow_design_record",
            "proposal_record": "workflow_proposal_record",
            "work_record": "worker_packet_work_record",
        }
        for field, alias in aliases.items():
            with self.subTest(field=field):
                self.assertIn(alias, payload_schema["$defs"])
                expected = {"$ref": f"#/$defs/{alias}"}
                if "description" in canonical[field]:
                    expected["description"] = canonical[field]["description"]
                self.assertEqual(projected[field], expected)

    def test_an_omitted_canonical_field_is_restored(self):
        # Regeneration restores a missing canonical field verbatim.
        payload = copy.deepcopy(payload_schema)
        del payload["$defs"]["worker_packet"]["properties"]["inputs"]["properties"]["report_protocol"]
        restored = self.project(payload=payload)
        self.assertEqual(
            restored["properties"]["report_protocol"],
            packet_schema["properties"]["inputs"]["properties"]["report_protocol"],
        )

    def test_a_new_canonical_inline_field_lands_verbatim(self):
        packet = copy.deepcopy(packet_schema)
        packet["properties"]["inputs"]["properties"]["focus"] = {"type": "string", "minLength": 1, "maxLength": 64}
        projected = self.project(packet=packet)
        self.assertEqual(projected["properties"]["focus"], {"type": "string", "minLength": 1, "maxLength": 64})

    def test_a_removed_canonical_field_leaves_the_mirror(self):
        packet = copy.deepcopy(packet_schema)
        del packet["properties"]["inputs"]["properties"]["report_protocol"]
        projected = self.project(packet=packet)
        self.assertNotIn("report_protocol", projected["properties"])

    def test_the_required_set_follows_canonical(self):
        packet = copy.deepcopy(packet_schema)
        packet["properties"]["inputs"]["required"].append("context")
        projected = self.project(packet=packet)
        self.assertEqual(projected["required"], ["task", "binding", "context"])

    def test_a_canonical_ref_field_without_an_alias_is_rejected(self):
        # A field the canonical schema binds through a lane-local $ref cannot
        # be copied verbatim: the ref target does not exist in the payloads
        # document. Generation must fail naming the field, so anchoring a new
        # alias is a deliberate act.
        packet = copy.deepcopy(packet_schema)
        packet["properties"]["inputs"]["properties"]["plan"] = {"$ref": "#/$defs/lane_plan"}
        with self.assertRaises(ValueError) as ctx:
            self.project(packet=packet)
        self.assertIn("plan", str(ctx.exception))

    def test_an_aliased_field_ignores_the_canonical_node_shape(self):
        # binding is aliased: the payloads schema's shared def owns the
        # published bound. A canonical change to the aliased node must not
        # leak into the mirror through a verbatim copy.
        packet = copy.deepcopy(packet_schema)
        packet["properties"]["inputs"]["properties"]["binding"] = {"$ref": "#/$defs/lane_other"}
        projected = self.project(packet=packet)
        self.assertEqual(projected["properties"]["binding"], {"$ref": "#/$defs/worker_packet_binding"})

    def test_context_fields_keep_their_aliases_and_published_text(self):
        projected = self.project()
        shipped = payload_schema["$defs"]["worker_packet"]["properties"]["inputs"]["properties"]
        self.assertEqual(projected["properties"]["work_context"], shipped["work_context"])
        self.assertEqual(projected["properties"]["checkpoint"], shipped["checkpoint"])
        self.assertEqual(projected["properties"]["work_context"]["$ref"], "#/$defs/work_context_view")
        self.assertEqual(projected["properties"]["checkpoint"]["$ref"], "#/$defs/continuity_checkpoint")
        self.assertEqual(projected["properties"]["work_context"]["description"],
                         generator.WORKER_PACKET_INPUT_PUBLISHED_TEXT["work_context"])
        self.assertEqual(projected["properties"]["checkpoint"]["description"],
                         generator.WORKER_PACKET_INPUT_PUBLISHED_TEXT["checkpoint"])

    def test_an_alias_naming_an_unknown_def_is_rejected(self):
        payload = copy.deepcopy(payload_schema)
        del payload["$defs"]["continuity_checkpoint"]
        with self.assertRaises(ValueError) as ctx:
            self.project(payload=payload)
        self.assertIn("continuity_checkpoint", str(ctx.exception))

    def test_the_packets_top_level_fields_stay_declared(self):
        # The projection derives only the inputs object; the packet's
        # top-level fields remain the payloads schema's own declaration.
        payload = copy.deepcopy(payload_schema)
        payload["$defs"]["worker_packet"]["properties"]["lane_digest"] = {"$ref": "#/$defs/id"}
        self.project(payload=payload)
        self.assertEqual(payload["$defs"]["worker_packet"]["properties"]["lane_digest"], {"$ref": "#/$defs/id"})


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
        branches = [defs[name] for name in generator.ADD_CONDITION_VARIANTS if name in defs]
        return branches or None

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
        for name in generator.ADD_CONDITION_VARIANTS:
            defs.pop(name, None)
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


def workflow_job_steps(job_name: str) -> list[dict]:
    """One job's steps as name/run/env records.

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
            break  # the next job's key ends this job
        if not in_job:
            if line == f"  {job_name}:":
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


def workflow_job_block(job_name: str) -> str:
    """The job's YAML text, from its key line to the next job-level key."""
    text = (ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8")
    body: list[str] = []
    collecting = False
    for line in text.splitlines():
        if not collecting:
            collecting = line == f"  {job_name}:"
            if collecting:
                body.append(line)
            continue
        if (
            line.startswith("  ")
            and not line.startswith("   ")
            and not line.strip().startswith("#")
            and line.strip().endswith(":")
        ):
            break  # the next job's key ends this job
        body.append(line)
    return "\n".join(body)


def job_needs(block: str) -> list[str]:
    """The job's `needs` dependencies, inline or as a block list."""
    inline = re.search(r"^ {4}needs: (.+)$", block, re.MULTILINE)
    if inline:
        return re.findall(r"[a-z][a-z0-9-]*", inline.group(1))
    listed = re.search(r"^ {4}needs:\n((?: {6}- .+\n?)+)", block, re.MULTILINE)
    return re.findall(r"- ([a-z][a-z0-9-]*)", listed.group(1)) if listed else []


AGGREGATE_DEPENDENCIES = ("verify-history", "verify-contracts", "verify-adapter", "verify-tooling")


class CIContractRoutingTests(unittest.TestCase):
    """Each suite has one owner, and the required gate demands all owners pass."""

    def test_verify_contracts_runs_the_umbrella_with_both_external_suite_options(self):
        steps = workflow_job_steps("verify-contracts")
        direct = [step for step in steps if "scripts/check-agent-contracts.py" in step["run"]]
        self.assertEqual(
            direct,
            [],
            f"standalone complete-contract steps must not exist: {[step['name'] for step in direct]}",
        )
        for owner, command in (
            ("verify-adapter", "bun test adapter/opencode"),
            ("verify-tooling", "scripts/test-agent-contracts.py"),
        ):
            duplicated = [step for step in steps if command in step["run"]]
            self.assertEqual(
                duplicated,
                [],
                f"{command} belongs to {owner}; verify-contracts must pass the matching external-suite option instead",
            )
        umbrella = [step for step in steps if "scripts/check-json.py" in step["run"]]
        self.assertEqual(len(umbrella), 1, "expected exactly one JSON umbrella step in verify-contracts")
        for flag in ("--adapter-tests-external", "--contract-tests-external"):
            self.assertIn(
                flag,
                umbrella[0]["run"],
                f"the umbrella step must pass {flag}: that suite runs as its own CI step",
            )
        self.assertEqual(
            umbrella[0]["env"].get("CONCORD_REQUIRE_BUN"),
            "1",
            "the umbrella must run under CONCORD_REQUIRE_BUN=1 so the nested check fails closed",
        )

    def test_verify_adapter_runs_the_adapter_suite_directly(self):
        # The adapter_test evidence anchors resolve only against a direct
        # workflow invocation, so deleting the direct step would strand them.
        direct = [step for step in workflow_job_steps("verify-adapter") if "bun test adapter/opencode" in step["run"]]
        self.assertTrue(direct, "verify-adapter must run the adapter suite directly")

    def test_verify_tooling_runs_the_contract_selftest_directly(self):
        direct = [step for step in workflow_job_steps("verify-tooling") if "scripts/test-agent-contracts.py" in step["run"]]
        self.assertTrue(direct, "verify-tooling must run scripts/test-agent-contracts.py directly")

    def test_the_aggregate_verify_job_gates_every_dependency_on_success(self):
        block = workflow_job_block("verify")
        needs = job_needs(block)
        for dependency in AGGREGATE_DEPENDENCIES:
            self.assertIn(dependency, needs, f"the aggregate verify job must depend on {dependency}")
        self.assertRegex(
            block,
            r"(?m)^ {4}if: always\(\)$",
            "the aggregate must run its gates even when a dependency failed",
        )
        gated: set[str] = set()
        for step in workflow_job_steps("verify"):
            text = " ".join(step["env"].values()) + " " + step["run"]
            referenced = {dependency for dependency in AGGREGATE_DEPENDENCIES if f"needs.{dependency}.result" in text}
            if not referenced:
                continue
            self.assertIn(
                "success",
                step["run"],
                f"step {step['name']!r} reads a dependency result but does not refuse non-success results",
            )
            gated |= referenced
        self.assertEqual(
            gated,
            set(AGGREGATE_DEPENDENCIES),
            f"dependencies without a success gate: {sorted(set(AGGREGATE_DEPENDENCIES) - gated)}",
        )

    def test_the_real_gate_refuses_each_non_success_owner(self):
        steps = workflow_job_steps("verify")
        bindings = {
            key: dependency
            for step in steps
            for key, value in step["env"].items()
            for dependency in AGGREGATE_DEPENDENCIES
            if value == f"${{{{ needs.{dependency}.result }}}}"
        }
        self.assertEqual(set(bindings.values()), set(AGGREGATE_DEPENDENCIES))
        command = "\n".join(step["run"] for step in steps)
        environment = dict(os.environ, **{key: "success" for key in bindings})
        passed = subprocess.run(["bash", "-e", "-c", command], env=environment, capture_output=True)
        self.assertEqual(passed.returncode, 0, passed.stderr)
        for key, dependency in bindings.items():
            for result in ("failure", "cancelled", "skipped"):
                with self.subTest(owner=dependency, result=result):
                    failed = subprocess.run(
                        ["bash", "-e", "-c", command],
                        env=dict(environment, **{key: result}), capture_output=True,
                    )
                    self.assertNotEqual(failed.returncode, 0, "a non-success owner must fail verify")

    def test_each_direct_suite_has_exactly_one_owner(self):
        text = (ROOT / ".github/workflows/ci.yml").read_text()
        jobs = re.findall(r"^  ([a-z][a-z0-9-]*):$", text, re.MULTILINE)
        runs = [step["run"] for job in jobs for step in workflow_job_steps(job)]
        for command in ("bun test adapter/opencode", "scripts/test-agent-contracts.py", "scripts/check-json.py"):
            self.assertEqual(sum(run.count(command) for run in runs), 1, command)
        self.assertFalse(any("scripts/check-project-tooling.py" in run for run in runs),
                         "the JSON umbrella owns project tooling validation")


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
                code = lane_checker.main([])
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
                code = lane_checker.main([])
        self.assertEqual(code, 1)
        self.assertIn("expected valid instance", err.getvalue())


class ExternalSuiteRoutingOptionsTests(unittest.TestCase):
    """CON-893: the external-suite options omit exactly one subprocess each.

    CI splits the old verify job so each suite runs as its own step, and the
    nested complete check must not duplicate them. Each option removes only
    its suite subprocess: every other obligation — the selftest, the
    generated checks, the lane validators, the Bun builds, the fixture probe,
    the expected-suite presence check, the host typecheck, and both strict
    failures — still runs and still propagates failure. A default invocation
    runs the complete check with both suites.
    """

    MINIMAL_PIN = {
        "sources": ["adapter/opencode"],
        "packages": [],
        "typescript": "5.9.3",
        "compiler_options": {},
        "allowances": [],
        "runtime_probe": {},
    }

    def _main(self, argv, fake=None, which="/fake/bun", staging=lambda *args: None, require_bun=False):
        fake = fake if fake is not None else _FakeSubprocess()
        patches = [
            unittest.mock.patch.object(lane_checker, "subprocess", fake),
            unittest.mock.patch.object(lane_checker, "stage_host_workspace", staging),
            unittest.mock.patch.object(lane_checker, "load_host_pin", lambda findings: copy.deepcopy(self.MINIMAL_PIN)),
            unittest.mock.patch("shutil.which", return_value=which),
        ]
        patches.append(unittest.mock.patch.dict(os.environ, {"CONCORD_REQUIRE_BUN": "1"} if require_bun else {}))
        out, err = io.StringIO(), io.StringIO()
        with ExitStack() as stack:
            for patch in patches:
                stack.enter_context(patch)
            if not require_bun:
                os.environ.pop("CONCORD_REQUIRE_BUN", None)
            with redirect_stdout(out), redirect_stderr(err):
                code = lane_checker.main(list(argv))
        return code, fake.calls, out.getvalue(), err.getvalue()

    @staticmethod
    def _selftest_calls(calls):
        return [call for call in calls if any("test-agent-contracts.py" in word for word in call)]

    @staticmethod
    def _adapter_suite_calls(calls):
        return [call for call in calls if call[1:3] == ["test", "adapter/opencode"]]

    def test_a_default_invocation_runs_both_suites(self):
        code, calls, out, _ = self._main([])
        self.assertEqual(code, 0)
        self.assertTrue(self._selftest_calls(calls), "a default invocation must run the contract selftest")
        self.assertTrue(self._adapter_suite_calls(calls), "a default invocation must run the adapter suite")
        self.assertIn("agent contract check passed", out)

    def test_adapter_tests_external_omits_only_the_adapter_suite(self):
        code, calls, out, _ = self._main(["--adapter-tests-external"])
        self.assertEqual(code, 0)
        self.assertFalse(self._adapter_suite_calls(calls), "the adapter suite subprocess must be omitted")
        self.assertTrue(self._selftest_calls(calls), "the contract selftest must still run")
        self.assertTrue(any(call[1] == "build" for call in calls), "the Bun builds must still run")

    def test_contract_tests_external_omits_only_the_selftest(self):
        code, calls, _, _ = self._main(["--contract-tests-external"])
        self.assertEqual(code, 0)
        self.assertFalse(self._selftest_calls(calls), "the contract selftest subprocess must be omitted")
        self.assertTrue(self._adapter_suite_calls(calls), "the adapter suite must still run")

    def test_both_options_retain_every_other_subprocess(self):
        code, calls, out, _ = self._main(["--adapter-tests-external", "--contract-tests-external"])
        self.assertEqual(code, 0)
        self.assertFalse(self._selftest_calls(calls))
        self.assertFalse(self._adapter_suite_calls(calls))
        self.assertEqual(len([call for call in calls if call[1] == "build"]), 2, "both Bun builds must still run")
        self.assertTrue(any(call[1] == "run" for call in calls), "the fixture probe and host schema probe must still run")
        self.assertTrue(any(call[1] == "x" for call in calls), "the host typecheck must still run")
        self.assertTrue(any(any("generate-agent-contracts.py" in word for word in call) for call in calls))
        self.assertTrue(any(any("generate-agent-lanes.py" in word for word in call) for call in calls))

    def test_the_expected_suite_presence_check_survives_adapter_tests_external(self):
        # The option skips the suite subprocess, never the obligation that
        # the suite files exist: a job running the suite externally still has
        # to carry it.
        real_glob = Path.glob

        def hiding_glob(self, pattern):
            return iter(()) if pattern == "*.test.ts" else real_glob(self, pattern)

        with unittest.mock.patch.object(Path, "glob", hiding_glob):
            code, _, _, err = self._main(["--adapter-tests-external"])
        self.assertEqual(code, 1)
        self.assertIn("adapter test suite missing", err)

    def test_a_selftest_failure_propagates_under_adapter_tests_external(self):
        fake = _FakeSubprocess(results=[(
            lambda words: any("test-agent-contracts.py" in word for word in words),
            _Completed(1, "", "simulated selftest failure"),
        )])
        code, _, _, _ = self._main(["--adapter-tests-external"], fake=fake)
        self.assertEqual(code, 1)

    def test_an_adapter_suite_failure_propagates_under_contract_tests_external(self):
        fake = _FakeSubprocess(results=[(
            lambda words: words[1:3] == ["test", "adapter/opencode"],
            _Completed(1),
        )])
        code, _, _, _ = self._main(["--contract-tests-external"], fake=fake)
        self.assertEqual(code, 1)

    def test_a_generated_contract_check_failure_propagates_under_both_options(self):
        fake = _FakeSubprocess(results=[(
            lambda words: any("generate-agent-contracts.py" in word for word in words),
            _Completed(1),
        )])
        code, _, _, _ = self._main(["--adapter-tests-external", "--contract-tests-external"], fake=fake)
        self.assertEqual(code, 1)

    def test_a_bun_build_failure_propagates_under_both_options(self):
        fake = _FakeSubprocess(results=[(lambda words: len(words) > 1 and words[1] == "build", _Completed(1))])
        code, _, _, _ = self._main(["--adapter-tests-external", "--contract-tests-external"], fake=fake)
        self.assertEqual(code, 1)

    def test_a_host_typecheck_failure_propagates_under_both_options(self):
        # TS9999 is not a code the TypeScript compiler emits, so no recorded
        # allowance can absorb the simulated diagnostic.
        typecheck = _Completed(1, "src/concord.ts(1,1): error TS9999: simulated host type failure\n", "")
        fake = _FakeSubprocess(results=[(lambda words: len(words) > 1 and words[1] == "x", typecheck)])
        code, _, _, err = self._main(["--adapter-tests-external", "--contract-tests-external"], fake=fake)
        self.assertEqual(code, 1)
        self.assertIn("TS9999", err)
        self.assertIn("does not satisfy the pinned host declarations", err)

    def test_missing_bun_fails_closed_under_both_options(self):
        code, _, _, err = self._main(["--adapter-tests-external", "--contract-tests-external"], which=None, require_bun=True)
        self.assertEqual(code, 1)
        self.assertIn("Bun is not installed", err)

    def test_an_unreachable_registry_fails_closed_under_both_options(self):
        error = "host declarations could not be installed: simulated registry unreachable"
        code, _, _, err = self._main(
            ["--adapter-tests-external", "--contract-tests-external"],
            staging=lambda *args: error,
            require_bun=True,
        )
        self.assertEqual(code, 1)
        self.assertIn(error, err)


class WorkflowActionVariantCorpusTests(unittest.TestCase):
    """Registry-derived deterministic corpus over every published workflow
    action variant (CON-412).

    The corpus derives its cases from the live registry projection (the same
    `go run ./scripts/workflow-action-contracts` output the generator reads),
    not from the frozen JSON, so an action the registry adds without a
    generated closed variant fails here. For each action it proves:

    - a valid sample built from the variant's own required/properties admits
      against the variant schema (published shape), the core input union, and
      the public variant when one exists;
    - missing-required, unknown-field, cross-variant-field, bad-enum,
      bad-type, and bad-range mutants are refused by the variant schema AND
      by the core input union AND by the public variant, so the published
      closed branch and the core cannot admit differently;
    - the legal input combinations the store's guards enforce (the registry
      projection's combinations, authored beside the guards) are exact: each
      discriminator value's required fields are required and its forbidden
      fields are refused, in every published and core shape.
    """

    @classmethod
    def setUpClass(cls):
        cls.actions, cls.workflows, cls.teaching = generator.load_workflow_action_contracts()
        cls.defs = payload_schema["$defs"]

    def _variants(self):
        for action in self.actions:
            name = f"work_transition_action_variant_{action['id']}"
            self.assertIn(name, self.defs, f"registry action {action['id']} has no closed variant")
            yield action, self.defs[name]

    def _schema_names(self, action, shape="core"):
        if shape == "core":
            names = [f"work_transition_action_variant_{action['id']}", "work_transition_action_input"]
        else:
            names = [f"work_transition_action_public_variant_{action['id']}", "work_transition_action_public_input"]
        return names

    def _shapes(self, action):
        # The core payload answers to the core union; a divergent public
        # payload (dispatch_worker, whose core attempt identity the adapter
        # authors) answers to the public union the agent boundary validates.
        shapes = [("core", f"work_transition_action_variant_{action['id']}", self._schema_names(action, "core"))]
        public = f"work_transition_action_public_variant_{action['id']}"
        if public in self.defs:
            shapes.append(("public", public, self._schema_names(action, "public")))
        return shapes

    def _legacy_layout(self, action, value):
        # The core input keeps admitting recorded historical payload layouts
        # (CON-412: historic execution preserved, never advertised). A mutant
        # whose fields object is absent or empty is exactly that layout for
        # an action that records legacy payloads, and only that.
        if not action.get("legacy_payloads"):
            return False
        fields = value.get("fields", {})
        return fields == {} or fields is None

    def _assert_verdict(self, value, schema_name, should_admit, label):
        try:
            generator.schema_validate(value, self.defs[schema_name], payload_schema, schema_name)
        except ValueError as err:
            if should_admit:
                self.fail(f"{label}: {schema_name} refused a payload its contract admits ({err})")
            return
        if not should_admit:
            self.fail(f"{label}: {schema_name} admitted a payload its contract refuses")

    # Node-derived mutant walks. Each returns (label, value, path) mutants
    # for one schema node against the sample value that reaches it.
    def _enum_mutants(self, schema, value, path=()):
        if isinstance(schema, dict) and "$ref" in schema:
            schema = self.defs.get(schema["$ref"].removeprefix("#/$defs/"), schema)
        if not isinstance(schema, dict):
            return
        if "enum" in schema and path and isinstance(value, (str, int)) and not isinstance(value, bool):
            yield "; ".join(path) + " bad enum", "__corpus_not_an_enum_value__", path
        kind = schema.get("type")
        kind = next((candidate for candidate in (kind if isinstance(kind, list) else [kind]) if candidate != "null"), None) if kind else None
        if isinstance(value, dict):
            for name, child in (schema.get("properties") or {}).items():
                if name not in value:
                    continue
                yield from self._enum_mutants(child, value[name], path + (name,))
        elif isinstance(value, list) and value:
            yield from self._enum_mutants(schema.get("items", {}), value[0], path + ("0",))

    def _type_mutants(self, schema, value, path=()):
        if isinstance(schema, dict) and "$ref" in schema:
            schema = self.defs.get(schema["$ref"].removeprefix("#/$defs/"), schema)
        if not isinstance(schema, dict):
            return
        kind = schema.get("type")
        if kind and path:
            primary = next((candidate for candidate in (kind if isinstance(kind, list) else [kind]) if candidate != "null"), None)
            if primary == "string":
                yield "; ".join(path) + " bad type", 17, path
            elif primary is not None:
                yield "; ".join(path) + " bad type", "corpus-wrong-type", path
        if isinstance(value, dict):
            for name, child in (schema.get("properties") or {}).items():
                if name not in value:
                    continue
                yield from self._type_mutants(child, value[name], path + (name,))
        elif isinstance(value, list) and value:
            yield from self._type_mutants(schema.get("items", {}), value[0], path + ("0",))

    def _range_mutants(self, schema, value, path=()):
        if isinstance(schema, dict) and "$ref" in schema:
            schema = self.defs.get(schema["$ref"].removeprefix("#/$defs/"), schema)
        if not isinstance(schema, dict):
            return
        if isinstance(value, str):
            if schema.get("minLength", 0) > 0 and len(value) >= schema["minLength"]:
                yield "; ".join(path) + " under minLength", "v" * (schema["minLength"] - 1), path
            if "maxLength" in schema and len(value) <= schema["maxLength"]:
                yield "; ".join(path) + " over maxLength", "v" * (schema["maxLength"] + 1), path
            return
        if isinstance(value, list):
            items = schema.get("items", {})
            if schema.get("minItems", 0) > 0 and len(value) >= schema["minItems"]:
                under = [self._sample(items)] * (schema["minItems"] - 1)
                yield "; ".join(path) + " under minItems", under, path
            if "maxItems" in schema and len(value) <= schema["maxItems"]:
                over = [self._sample(items)] * (schema["maxItems"] + 1)
                yield "; ".join(path) + " over maxItems", over, path
            if value:
                yield from self._range_mutants(items, value[0], path + ("0",))
            return
        if isinstance(value, dict):
            for name, child in (schema.get("properties") or {}).items():
                if name not in value:
                    continue
                yield from self._range_mutants(child, value[name], path + (name,))
            return
        if isinstance(value, int) and not isinstance(value, bool):
            if "minimum" in schema and value >= schema["minimum"]:
                yield "; ".join(path) + " under minimum", schema["minimum"] - 1, path
            if "maximum" in schema and value <= schema["maximum"]:
                yield "; ".join(path) + " over maximum", schema["maximum"] + 1, path

    def test_valid_samples_admit_and_mutants_refuse(self):
        own_fields = {action["id"]: {field["name"] for field in action["payload"]["fields"]} for action in self.actions}
        for action, variant in self._variants():
            for kind, shape_name, names in self._shapes(action):
                valid = self._sample(self.defs[shape_name])
                for name in names:
                    self._assert_verdict(valid, name, True, f"{action['id']} {kind} valid sample")
                required = [key for key in self.defs[shape_name].get("required", []) if key != "action_id"]
                if required:
                    mutant = copy.deepcopy(valid); del mutant[required[0]]
                    for name in names:
                        if name.endswith("_input") and self._legacy_layout(action, mutant):
                            self._assert_verdict(mutant, name, True, f"{action['id']} {kind} recorded legacy layout")
                            continue
                        self._assert_verdict(mutant, name, False, f"{action['id']} {kind} missing required {required[0]}")
                mutant = copy.deepcopy(valid); mutant["unknown_field"] = True
                for name in names:
                    self._assert_verdict(mutant, name, False, f"{action['id']} {kind} unknown field")
                fields = valid.get("fields")
                if isinstance(fields, dict) and fields:
                    mutant = copy.deepcopy(valid); del mutant["fields"][next(iter(fields))]
                    cross = next((name for other in self.actions if other["id"] != action["id"]
                                  for name in {field["name"] for field in other["payload"]["fields"]}
                                  if name not in own_fields[action["id"]]), None)
                    if cross is not None:
                        mutant["fields"][cross] = "cross-variant"
                    for name in names:
                        if name.endswith("_input") and self._legacy_layout(action, mutant):
                            self._assert_verdict(mutant, name, True, f"{action['id']} {kind} recorded legacy layout")
                            continue
                        self._assert_verdict(mutant, name, False, f"{action['id']} {kind} cross-variant or unknown fields field")
                # Node-derived enum, type, and range mutants: every one must
                # be refused by the published shape and the core union alike.
                for walker in (self._enum_mutants, self._type_mutants, self._range_mutants):
                    for label, replacement, path in walker(self.defs[shape_name], valid):
                        path = tuple(int(step) if isinstance(step, str) and step.isdigit() else step for step in path)
                        mutant = copy.deepcopy(valid)
                        node = mutant
                        for step in path[:-1]:
                            node = node[int(step)] if isinstance(node, list) else node[step]
                        if path:
                            final = path[-1]
                            if isinstance(node, list): node[int(final)] = replacement
                            else: node[final] = replacement
                        for name in names:
                            if name.endswith("_input") and self._legacy_layout(action, mutant):
                                self._assert_verdict(mutant, name, True, f"{action['id']} {kind} recorded legacy layout")
                                continue
                            self._assert_verdict(mutant, name, False, f"{action['id']} {kind} {label}")

    def test_cross_field_rule_mutants_refuse_everywhere(self):
        # The registry payload declarations state the cross-field rules core
        # admission enforces: legal input combinations (one branch per enum
        # value) and alternative field groups (exactly one supplied). The
        # published variant must refuse every rule's own invalid mutant
        # exactly — a forbidden field under one discriminator value, a
        # missing required field under another, a withheld alternative group,
        # and two groups supplied together — and admit each rule's legal
        # payload.
        for action, variant in self._variants():
            cross = action.get("cross_field") or {}
            combinations = cross.get("combinations", [])
            alternatives = cross.get("alternatives", [])
            if not combinations and not alternatives:
                continue
            fields_schema = variant["properties"]["fields"]
            self.assertIn("oneOf", fields_schema, f"{action['id']} declares cross-field rules but its variant publishes none")

            def branch_property(name):
                # A combination field lives in the branch that declares it;
                # a forbidden-under-this-value field lives in a sibling branch.
                for branch in fields_schema["oneOf"]:
                    property = branch.get("properties", {}).get(name)
                    if property is not None:
                        return property
                return {"type": "string"}

            def envelope_with(fields):
                base = {key: self._sample(variant["properties"][key]) for key in variant.get("required", []) if key != "fields"}
                base["fields"] = fields
                return base

            by_value = {combination["value"]: combination for combination in combinations}
            # A kind-match declaration expands one alternative group into
            # per-kind branches (outcome_kind consts on supersede_contract):
            # each is registry-declared through the match, not through a
            # combination, so it carries no requires/forbids to expand.
            kind_match_fields = {match["field"] for match in cross.get("kind_matches", [])}
            for branch in fields_schema["oneOf"]:
                discriminator = next((name for name, property in branch.get("properties", {}).items() if "const" in property), None)
                if discriminator is None:
                    continue
                value = branch["properties"][discriminator]["const"]
                if discriminator in kind_match_fields:
                    continue
                combination = by_value.get(value)
                self.assertIsNotNone(combination, f"{action['id']} publishes a branch for {discriminator}={value} the registry does not declare")
                base = envelope_with(self._sample(branch, fallback_properties=branch.get("properties")))
                base["fields"][discriminator] = value
                for _kind, _shape_name, names in self._shapes(action):
                    for forbidden in combination.get("forbids", []):
                        if forbidden == discriminator:
                            continue
                        mutant = copy.deepcopy(base)
                        mutant["fields"][forbidden] = self._sample(branch_property(forbidden))
                        for name in names:
                            self._assert_verdict(mutant, name, False, f"{action['id']} {discriminator}={value} carries forbidden {forbidden}")
                        # Stripping the forbidden field leaves a legal payload.
                        legal = copy.deepcopy(mutant); legal["fields"].pop(forbidden)
                        for name in names:
                            self._assert_verdict(legal, name, True, f"{action['id']} {discriminator}={value} without {forbidden}")
                    for required_under_value in combination.get("requires", []):
                        if required_under_value == discriminator:
                            continue
                        mutant = copy.deepcopy(base)
                        mutant["fields"].pop(required_under_value, None)
                        for name in names:
                            self._assert_verdict(mutant, name, False, f"{action['id']} {discriminator}={value} drops required {required_under_value}")
                        legal = copy.deepcopy(mutant)
                        legal["fields"][required_under_value] = self._sample(branch_property(required_under_value))
                        for name in names:
                            self._assert_verdict(legal, name, True, f"{action['id']} {discriminator}={value} with {required_under_value}")

            # Alternative groups: each declared group names the fields its
            # closed branch requires; a branch answers to the group whose
            # names it carries. A kind-matched group expands into one closed
            # branch per declared kind, so only the plain groups map 1:1 to
            # const-free branches (CON-412).
            group_branches = [branch for branch in fields_schema["oneOf"] if not any("const" in property for property in branch.get("properties", {}).values())]

            def branch_for(group):
                for branch in fields_schema["oneOf"]:
                    required = set(branch.get("required", []))
                    if all(name in required for name in group["fields"]):
                        return branch
                self.fail(f"{action['id']} declares alternative group {group['fields']} with no published branch")

            if alternatives:
                plain_groups = [group for group in alternatives if not (kind_match_fields & set(group["fields"]))]
                kind_groups = [group for group in alternatives if kind_match_fields & set(group["fields"])]
                self.assertEqual(len(group_branches), len(plain_groups), f"{action['id']} alternative branches and declaration groups disagree")
                for group in kind_groups:
                    match = next(match for match in cross.get("kind_matches", []) if match["field"] in group["fields"])
                    branches = [branch for branch in fields_schema["oneOf"] if match["field"] in branch.get("properties", {}) and "const" in branch["properties"][match["field"]]]
                    self.assertGreater(len(branches), 0, f"{action['id']} kind-matched group {group['fields']} publishes no per-kind branch")
                    for branch in branches:
                        self.assertEqual(
                            branch["properties"][match["field"]]["const"],
                            branch["properties"][match["object"]]["properties"][match["discriminator"]]["const"],
                            f"{action['id']} per-kind branch does not bind {match['field']} to {match['object']}.{match['discriminator']}",
                        )
                for group in alternatives:
                    branch = branch_for(group)
                    for _kind, _shape_name, names in self._shapes(action):
                        legal = envelope_with(self._sample(branch, fallback_properties=branch.get("properties")))
                        for name in names:
                            self._assert_verdict(copy.deepcopy(legal), name, True, f"{action['id']} alternative group {group['fields']} supplied")
                        withheld = {key: value for key, value in legal["fields"].items()}
                        for name in group["fields"]:
                            withheld.pop(name, None)
                        for name in names:
                            self._assert_verdict(envelope_with(withheld), name, False, f"{action['id']} alternative group {group['fields']} withheld")
                    # A field the group forbids beside it is omitted from the
                    # branch entirely and refused when carried — one negative
                    # mutant for each newly owned cross-field rule.
                    for forbidden in group.get("forbids", []):
                        self.assertNotIn(forbidden, branch.get("properties", {}), f"{action['id']} branch for {group['fields']} publishes forbidden {forbidden}")
                        mutant = envelope_with(self._sample(branch, fallback_properties=branch.get("properties")))
                        mutant["fields"][forbidden] = self._sample(branch_property(forbidden))
                        for _kind, _shape_name, names in self._shapes(action):
                            for name in names:
                                self._assert_verdict(copy.deepcopy(mutant), name, False, f"{action['id']} group {group['fields']} carries forbidden {forbidden}")
                    if len(alternatives) > 1:
                        for other_group in alternatives:
                            if other_group is group:
                                continue
                            other = branch_for(other_group)
                            for exclusive in other_group["fields"]:
                                if exclusive in branch.get("properties", {}):
                                    continue
                                mutant = envelope_with(self._sample(branch, fallback_properties=branch.get("properties")))
                                mutant["fields"][exclusive] = self._sample(other["properties"][exclusive])
                                for _kind, _shape_name, names in self._shapes(action):
                                    for name in names:
                                        self._assert_verdict(copy.deepcopy(mutant), name, False, f"{action['id']} alternative groups carry {exclusive} together")

    def test_guard_owned_teaching_is_published(self):
        # CON-412: the items close per kind; every branch teaches the ordinal
        # position rule and the outcome_kind/outcome_payload.kind equality
        # rule from the guard-owned projection.
        items = self.defs["workflow_action_outcome_predicates"]["items"]["oneOf"]
        self.assertEqual(len(items), 4)
        for branch in items:
            self.assertEqual(branch["properties"]["ordinal"]["description"], self.teaching["predicate_ordinal_rule"])
            self.assertEqual(branch["properties"]["outcome_kind"]["description"], self.teaching["outcome_kind_equality_rule"])
            self.assertEqual(branch["properties"]["outcome_kind"]["const"], branch["properties"]["outcome_payload"]["properties"]["kind"]["const"])
        approve = self.defs["work_transition_action_variant_approve_contract"]["properties"]["fields"]["properties"]
        if "required_evidence" in approve:
            self.assertEqual(approve["required_evidence"].get("description"), self.teaching["obligation_membership_rule"])

    def test_every_registry_action_has_one_closed_variant(self):
        names = {f"work_transition_action_variant_{action['id']}" for action in self.actions}
        published = {name for name in self.defs if name.startswith("work_transition_action_variant_") or name.startswith("work_transition_action_public_variant_")}
        self.assertEqual(published - {f"work_transition_action_public_variant_{a['id']}" for a in self.actions if a["payload"] != a["public_payload"]}, names)

    def _sample(self, schema, path="$", fallback_properties=None):
        schema = self.defs[schema["$ref"].removeprefix("#/$defs/")] if "$ref" in schema else schema
        if "const" in schema: return schema["const"]
        if "enum" in schema: return schema["enum"][0]
        if "oneOf" in schema:
            base = {key: self._sample(self._property(schema, fallback_properties, key), f"{path}.{key}") for key in schema.get("required", [])}
            branch_value = self._sample(schema["oneOf"][0], f"{path}.oneOf[0]", schema.get("properties"))
            if isinstance(branch_value, dict):
                for key, value in branch_value.items():
                    base.setdefault(key, value)
            return base
        if "allOf" in schema:
            base = self._sample({k: v for k, v in schema.items() if k != "allOf"}, path, fallback_properties)
            for member in schema["allOf"]:
                merged = self._sample(member, path, fallback_properties)
                if isinstance(base, dict) and isinstance(merged, dict): base.update(merged)
            return base
        kind = schema.get("type")
        if isinstance(kind, list): kind = next((candidate for candidate in kind if candidate != "null"), kind[0])
        if kind is None and ("properties" in schema or "required" in schema): kind = "object"
        if kind == "object":
            result = {}
            for key in schema.get("required", []):
                result[key] = self._sample(self._property(schema, fallback_properties, key), f"{path}.{key}")
            return result
        if kind == "array": return [] if not schema.get("minItems") else [self._sample(schema.get("items", {"type": "string"}), path)]
        if kind == "integer": return schema.get("minimum", 0)
        if kind == "boolean": return True
        if schema.get("format") == "date-time": return "2026-08-08T00:00:00Z"
        pattern = schema.get("pattern", "")
        if "sha256:" in pattern: return "sha256:" + "0" * 64
        if "[0-9a-f]{40}" in pattern: return "0" * 40
        if pattern.startswith("^msg:"): return "msg:" + "0" * 32
        if pattern.startswith("^https://"): return "https://example.test/pull/1"
        if schema.get("minLength"): return "v" * schema["minLength"]
        return "id-1"

    def _property(self, schema, fallback_properties, key):
        properties = schema.get("properties") or fallback_properties or {}
        return properties.get(key, {"type": "string"})


if __name__ == "__main__": unittest.main()

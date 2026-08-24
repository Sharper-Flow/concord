#!/usr/bin/env python3
import copy, importlib.util, json, unittest
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

if __name__ == "__main__": unittest.main()

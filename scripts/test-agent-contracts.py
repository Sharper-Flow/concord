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

if __name__ == "__main__": unittest.main()

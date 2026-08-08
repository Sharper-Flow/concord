#!/usr/bin/env python3
import copy, importlib.util, json, unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("generator", ROOT / "scripts/generate-agent-contracts.py")
generator = importlib.util.module_from_spec(spec); spec.loader.exec_module(generator)
manifest = json.loads((ROOT / "contracts/agent-tool-surface.v1.json").read_text())
ir = json.loads((ROOT / "contracts/agent-tool-surface.schema.json").read_text())

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

if __name__ == "__main__": unittest.main()

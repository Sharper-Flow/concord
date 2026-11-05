#!/usr/bin/env python3
import json
import subprocess
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


class WorkflowPlanProjectionTest(unittest.TestCase):
    def test_action_route_is_available_at_intake(self):
        manifest = json.loads((ROOT / "contracts/agent-tool-surface.v1.json").read_text())
        operation = next(item for item in manifest["operations"] if item["id"] == "concord_work_transition.workflow_action")
        self.assertEqual(operation.get("availability", "always"), "always")
        schema = json.loads((ROOT / "contracts/agent-tool-surface.schema.json").read_text())
        self.assertEqual(schema["properties"]["operations"]["items"]["properties"]["availability"]["enum"], ["always"])

    def test_catalog_projection_tracks_templates_and_prelude(self):
        templates = json.loads((ROOT / "contracts/workflow-step-templates.v1.json").read_text())
        result = subprocess.run(["go", "run", "./scripts/workflow-action-contracts"], cwd=ROOT, text=True, capture_output=True, check=True)
        projection = json.loads(result.stdout)
        actions = {action["id"]: action for action in projection["actions"]}
        expected = {action for template in templates["types"] for step in template["steps"] for action in step["actions"]}
        expected.update({"record_intake", "record_clarify", "pick_type", "insert_step"})
        self.assertEqual(set(actions), expected)
        self.assertEqual({item["id"] for item in templates["types"]}, {"change", "fix", "research", "removal"})
        fields = lambda action: {field["name"]: field for field in actions[action]["variants"][0]["payload"]["fields"]}
        self.assertEqual(set(fields("pick_type")["type"]["enum"]), {item["id"] for item in templates["types"]})
        self.assertFalse(fields("record_plan")["reason"]["required"])
        self.assertIn("decision_domains", fields("approve_contract"))
        update = {field["name"] for field in projection["contract_updates"][0]["fields"]}
        self.assertIn("decision_domains", update)
        self.assertTrue({"workflow_ref", "workflow_digest", "definition_digest"}.isdisjoint(update))

    def test_resume_schema_follows_templates(self):
        templates = json.loads((ROOT / "contracts/workflow-step-templates.v1.json").read_text())
        document = json.loads((ROOT / "contracts/agent-tool-surface-payloads.schema.json").read_text())
        defs = document["$defs"]
        resume = defs["resume_read_document"]
        source = templates["plan_schema"]
        self.assertEqual(resume["properties"]["type"]["enum"], source["properties"]["type"]["enum"])
        self.assertEqual(resume["properties"]["steps"]["items"]["$ref"], "#/$defs/workflow_plan_step")
        self.assertEqual(defs["workflow_plan_step"]["properties"]["attempts"]["items"]["$ref"], "#/$defs/workflow_plan_attempt")
        self.assertEqual(defs["workflow_plan_attempt"]["required"], source["$defs"]["attempt"]["required"])


if __name__ == "__main__":
    unittest.main()

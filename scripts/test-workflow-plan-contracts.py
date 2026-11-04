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

    def test_read_schemas_follow_templates_and_action_fields(self):
        templates = json.loads((ROOT / "contracts/workflow-step-templates.v1.json").read_text())
        document = json.loads((ROOT / "contracts/agent-tool-surface-payloads.schema.json").read_text())
        defs = document["$defs"]
        plan = defs["workflow_step_plan"]
        source = templates["plan_schema"]
        self.assertEqual(plan["properties"]["type"]["enum"], source["properties"]["type"]["enum"])
        self.assertEqual(plan["properties"]["steps"]["items"]["$ref"], "#/$defs/workflow_plan_step")
        self.assertEqual(defs["workflow_plan_step"]["properties"]["attempts"]["items"]["$ref"], "#/$defs/workflow_plan_attempt")
        self.assertEqual(defs["workflow_plan_attempt"]["required"], source["$defs"]["attempt"]["required"])
        for name, action in (("workflow_plan_read_document", "record_plan"), ("workflow_removal_read_document", "record_removal_scope")):
            wrapper = defs[name]
            self.assertEqual(set(wrapper["required"]), {"work_version", "document", "recorded_at", "actor", "event_ref"})
            fields = defs["work_transition_action_variant_" + action]["properties"]["fields"]
            self.assertEqual(set(wrapper["properties"]["document"]["properties"]), set(fields["properties"]))
            self.assertEqual(set(wrapper["properties"]["document"]["required"]), set(fields["required"]))
        alignment = defs["workflow_backlog_alignment"]
        self.assertIn("all_matches_removed", alignment["required"])
        self.assertEqual(alignment["properties"]["all_matches_removed"], {"type": "boolean"})
        guidance = defs["workflow_outcome_outcome"]["properties"]["allowed"]["description"]
        for template in templates["types"]:
            self.assertIn(template["id"] + " admits ", guidance)
        self.assertIn("report_recorded", guidance)


if __name__ == "__main__":
    unittest.main()

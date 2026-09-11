package agent

import "testing"

func TestWorkflowActionSchemaAdmitsHistoricalRecordDesignAndCurrentTypedRecordDesign(t *testing.T) {
	historical := []byte(`{"work_id":"work-1","expected_version":7,"action_id":"record_design","idempotency_key":"design-v5"}`)
	if err := ValidateOperationPayload("concord_work_transition", "workflow_action", historical, false); err != nil {
		t.Fatalf("historical record_design request was refused at the agent boundary: %v", err)
	}
	current := []byte(`{"work_id":"work-1","expected_version":7,"action_id":"record_design","fields":{"approach":"keep the pinned contract","decisions":[{"id":"decision:one","question":"Which route?","choice":"native","rationale":"The adapter owns the route","rejected":[]}],"touched_refs":["work:item"]},"idempotency_key":"design-v6"}`)
	if err := ValidateOperationPayload("concord_work_transition", "workflow_action", current, false); err != nil {
		t.Fatalf("current typed record_design request was refused at the agent boundary: %v", err)
	}
}

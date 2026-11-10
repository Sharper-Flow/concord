package agent

import (
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
)

// workflow-engine-contract.md §§5.1 and 11 retire record_design; the six-part
// record_plan holds the current design content.
func TestWorkflowActionSchemaRefusesLegacyRecordDesignAndAdmitsTheTypedShape(t *testing.T) {
	historical := []byte(`{"work_id":"work-1","expected_version":7,"action_id":"record_design","idempotency_key":"design-v5"}`)
	if err := agentcontract.ValidateOperationPayload("concord_work_transition", "workflow_action", historical, false); err == nil {
		t.Fatal("legacy fields-less record_design was admitted at the agent boundary; the closed union must refuse it")
	}
	current := []byte(`{"work_id":"work-1","expected_version":7,"action_id":"record_design","fields":{"approach":"keep the pinned contract","decisions":[{"id":"decision:one","question":"Which route?","choice":"native","rationale":"The adapter owns the route","rejected":[]}],"touched_refs":["work:item"]},"idempotency_key":"design-v6"}`)
	if err := agentcontract.ValidateOperationPayload("concord_work_transition", "workflow_action", current, false); err == nil {
		t.Fatal("retired typed record_design was admitted at the agent boundary")
	}
}

func TestWorkflowActionSchemaRefusesRetiredProposal(t *testing.T) {
	for _, payload := range []string{
		`{"work_id":"work-1","expected_version":7,"action_id":"record_proposal","idempotency_key":"proposal-v1"}`,
		`{"work_id":"work-1","expected_version":7,"action_id":"record_proposal","fields":{"problem":"Problem","affected":["People"],"stakes":"Stakes","user_outcomes":["Outcome"]},"idempotency_key":"proposal-v2"}`,
	} {
		if err := agentcontract.ValidateOperationPayload("concord_work_transition", "workflow_action", []byte(payload), false); err == nil {
			t.Fatal("retired record_proposal was admitted at the agent boundary")
		}
	}
}

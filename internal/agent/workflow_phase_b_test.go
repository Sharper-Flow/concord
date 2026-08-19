package agent

import (
	"context"
	"encoding/json"
	"testing"
)

func TestWorkflowActionDispatchBoundaryRemainsDisabled(t *testing.T) {
	r := runtime{}
	base := NewBase("workflow-disabled", "concord_work_transition", "workflow_action")
	response, err := r.mutate(context.Background(), base, json.RawMessage(`{"work_id":"work-alpha","expected_version":2,"action_id":"start_execution","fields":{"unexpected":true},"idempotency_key":"wf-disabled"}`), Grant{}, ContractOperation{ID: "concord_work_transition.workflow_action"})
	if err != nil || response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "invalid_input" {
		t.Fatalf("disabled workflow_action response=%+v err=%v", response, err)
	}
	if response.Error.RecoveryAction.Kind != "contact_operator" {
		t.Fatalf("disabled workflow_action recovery=%+v", response.Error.RecoveryAction)
	}
}

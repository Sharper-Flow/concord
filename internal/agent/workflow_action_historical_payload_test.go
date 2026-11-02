package agent

import (
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
)

// The live authoring union is closed: the fields-less record_design layout
// the legacy input branch used to admit refuses at the boundary, and the
// current typed shape is the only authoring route (CD-0218 D2/D4). The
// typed design fields remain the authoring contract.
func TestWorkflowActionSchemaRefusesLegacyRecordDesignAndAdmitsTheTypedShape(t *testing.T) {
	historical := []byte(`{"work_id":"work-1","expected_version":7,"action_id":"record_design","idempotency_key":"design-v5"}`)
	if err := agentcontract.ValidateOperationPayload("concord_work_transition", "workflow_action", historical, false); err == nil {
		t.Fatal("legacy fields-less record_design was admitted at the agent boundary; the closed union must refuse it")
	}
	current := []byte(`{"work_id":"work-1","expected_version":7,"action_id":"record_design","fields":{"approach":"keep the pinned contract","decisions":[{"id":"decision:one","question":"Which route?","choice":"native","rationale":"The adapter owns the route","rejected":[]}],"touched_refs":["work:item"]},"idempotency_key":"design-v6"}`)
	if err := agentcontract.ValidateOperationPayload("concord_work_transition", "workflow_action", current, false); err != nil {
		t.Fatalf("current typed record_design request was refused at the agent boundary: %v", err)
	}
}

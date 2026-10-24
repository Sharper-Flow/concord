package agent

import (
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

// The live authoring union is closed: the fields-less record_design layout
// the legacy input branch used to admit refuses at the boundary, and the
// current typed shape is the only authoring route (CD-0218 D2/D4). The
// frozen historical decoder keeps admitting the recorded layout, so a
// stored payload replays while no live call can author it.
func TestWorkflowActionSchemaRefusesLegacyRecordDesignAndAdmitsTheTypedShape(t *testing.T) {
	historical := []byte(`{"work_id":"work-1","expected_version":7,"action_id":"record_design","idempotency_key":"design-v5"}`)
	if err := ValidateOperationPayload("concord_work_transition", "workflow_action", historical, false); err == nil {
		t.Fatal("legacy fields-less record_design was admitted at the agent boundary; the closed union must refuse it")
	}
	current := []byte(`{"work_id":"work-1","expected_version":7,"action_id":"record_design","fields":{"approach":"keep the pinned contract","decisions":[{"id":"decision:one","question":"Which route?","choice":"native","rationale":"The adapter owns the route","rejected":[]}],"touched_refs":["work:item"]},"idempotency_key":"design-v6"}`)
	if err := ValidateOperationPayload("concord_work_transition", "workflow_action", current, false); err != nil {
		t.Fatalf("current typed record_design request was refused at the agent boundary: %v", err)
	}
	// The frozen reader keeps the recorded payload layouts valid: the
	// retired authoring shape stays readable at the exact pinned versions
	// through the historical decoder's design-content closure, without
	// regaining a live authoring permission.
	if err := payloadschema.ValidateInDocument(payloadschema.Document(), "workflow_design_content", historical); err == nil {
		t.Fatal("frozen design-content closure admitted a fields-less record_design payload; the frozen reader must still bind the typed content")
	}
	typedContent := []byte(`{"approach":"keep the pinned contract","decisions":[{"id":"decision:one","question":"Which route?","choice":"native","rationale":"The adapter owns the route","rejected":[]}],"touched_refs":["work:item"]}`)
	if err := payloadschema.ValidateInDocument(payloadschema.Document(), "workflow_design_content", typedContent); err != nil {
		t.Fatalf("frozen historical decoder refused the typed design content: %v", err)
	}
}

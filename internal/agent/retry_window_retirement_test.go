package agent

import (
	"encoding/json"
	"testing"
)

func TestPublishedWorkflowReadOmitsRetryWindow(t *testing.T) {
	projection := fullyPopulatedWorkflowReadProjection()
	shaped, err := publishedWorkflowRead(&projection)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := shaped["retry_window"]; exists {
		t.Error("the published workflow read still includes retry_window")
	}
	if _, exists := workflowReadPublishedFields()["retry_window"]; exists {
		t.Error("the generated workflow read field set still declares retry_window")
	}
	raw, err := json.Marshal(shaped)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePayloadSchema("workflow_read", raw); err != nil {
		t.Fatalf("the published workflow read violates its closed contract: %v", err)
	}
}

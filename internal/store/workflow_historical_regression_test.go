package store

import (
	"encoding/json"
	"testing"
)

// This corpus uses interfaces present in v8.13.3 so an immutable source
// snapshot and the repaired tree can execute the same assertions.
func TestHistoricalReproDeclaredSchema(t *testing.T) {
	d := WorkflowDefinition{ActionDefinitions: []WorkflowActionDefinition{{ID: "probe", Payload: WorkflowPayloadDefinition{Closed: true, Fields: []WorkflowPayloadField{{Name: "value", ValueType: PayloadObject, Required: true, SchemaRef: "workflow_design_decision"}}}}}}
	if err := validateWorkflowActionPayload(d, "probe", json.RawMessage(`{"value":{}}`)); err == nil {
		t.Error("REPRO: preflight accepted an object that violates its declared schema")
	}
}

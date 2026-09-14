package store

import (
	"encoding/json"
	"testing"
)

func TestHistoricalReproArrayItemContract(t *testing.T) {
	d, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"approach":"shared validation","decisions":[{"id":"choice-one","question":"which approach","choice":"shared validator","rationale":"one declared contract","rejected":[]}],"touched_refs":["internal/store/workflow.go"]}`)
	if err := validateWorkflowActionPayload(d.Definition, "record_design", raw); err != nil {
		t.Errorf("REPRO: a valid decision array was checked as one element: %v", err)
	}
}

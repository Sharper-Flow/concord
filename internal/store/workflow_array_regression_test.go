package store

import (
	"encoding/json"
	"testing"
)

func TestHistoricalReproArrayItemContract(t *testing.T) {
	raw := json.RawMessage(`{"outcomes":"deliver change","approach":"shared validation","decisions":[{"id":"choice-one","question":"which approach","choice":"shared validator","rationale":"one declared contract","rejected":[]},{"id":"choice-two","question":"which boundary","choice":"typed payload","rationale":"preserve validation","rejected":[]}],"touches":["internal/store/workflow.go"],"merge_split":"one change","risks":"check boundaries"}`)
	if _, err := validateWorkflowPlanActionPayload("record_plan", raw); err != nil {
		t.Errorf("REPRO: a valid decision array was checked as one element: %v", err)
	}
}

package store

import (
	"encoding/json"
	"testing"
)

// The fixture compiles at the pinned baseline and at HEAD through the
// historicalWorkflowPayload adapter each revision provides.
func TestHistoricalReproArrayItemContract(t *testing.T) {
	raw := json.RawMessage(`{"outcomes":[{"id":"outcome:one","statement":"deliver change","done_condition":"works","evidence":"tests","depends_on":[]}],"approach":"shared validation","decisions":[{"id":"choice-one","question":"which approach","choice":"shared validator","rationale":"one declared contract","rejected":[],"status":"settled"},{"id":"choice-two","question":"which boundary","choice":"typed payload","rationale":"preserve validation","rejected":[],"status":"settled"}],"touches":[{"path":"internal/store/workflow.go","role":"change"}],"merge_split":"one change","risks":"check boundaries"}`)
	if err := historicalWorkflowPayload("record_plan", raw); err != nil {
		t.Errorf("REPRO: a valid decision array was checked as one element: %v", err)
	}
}

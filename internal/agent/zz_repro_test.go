package agent

import (
	"encoding/json"
	"testing"
)

func TestReproRecoveryBindFields(t *testing.T) {
	raw := json.RawMessage(`{"work_id":"work-1","expected_version":51,"action_id":"bind_evidence","idempotency_key":"k","fields":{"evidence_kind":"artifact","evidence_ref":"CD-0096"},"evidence":[{"kind":"artifact","authority":"docs","locator":"CD-0096","locator_kind":"law_amendment"}]}`)
	if err := ValidatePayloadSchema("work_transition_action_input", raw); err != nil {
		t.Logf("REPRO: %v", err)
	} else {
		t.Logf("REPRO: accepted")
	}
}

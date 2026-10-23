package store

import (
	"context"
	"encoding/json"
	"testing"
)

// pOverlapContractFacts derives the current consent facts for the raw fold
// witnesses. These cases change at most one child Domain and require no
// operator consent; a serious change must use a consumed approval instead.
func pOverlapContractFacts(t *testing.T, s *Store, event Event) Event {
	t.Helper()
	definition := pinnedWorkflowDefinitionForTest(t, s, event.SubjectID)
	event = withPinnedDecisionFact(t, definition, event)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(event.Payload, &fields); err != nil {
		t.Fatal(err)
	}
	contract := fields
	if event.Kind == WorkflowContractSuperseded {
		contract = map[string]json.RawMessage{}
		if err := json.Unmarshal(fields["successor_contract"], &contract); err != nil {
			t.Fatal(err)
		}
	}
	var modifies []string
	var binding *WorkflowArchitectureBinding
	if err := json.Unmarshal(contract["law_modifies"], &modifies); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(contract["architecture_binding"], &binding); err != nil {
		t.Fatal(err)
	}
	declarations := map[string]json.RawMessage{}
	if workflowContractChangesLaw(modifies, binding) {
		declarations = lawChangeFields(t, map[string]any{
			"law_change_reason":                          "exercise the declared overlap footprint",
			"law_change_removes_user_visible_behavior":   false,
			"law_change_touches_security_or_data_safety": false,
		})
	}
	change, err := deriveWorkflowLawChangeTx(context.Background(), s.DatabaseForTesting(), definition, event.SubjectID, declarations, modifies, binding, []string{})
	if err != nil {
		t.Fatal(err)
	}
	if change != nil {
		if change.Serious {
			t.Fatal("overlap fixture unexpectedly requires exact-text operator consent")
		}
		contract["law_change"] = mustJSONValue(change)
	}
	if event.Kind == WorkflowContractSuperseded {
		fields["successor_contract"] = mustJSONValue(contract)
		event.PayloadVersion = workflowContractSupersededLawChangePayloadVersion
	} else {
		event.PayloadVersion = workflowContractLawChangePayloadVersion
	}
	event.Payload = mustJSONValue(fields)
	return event
}

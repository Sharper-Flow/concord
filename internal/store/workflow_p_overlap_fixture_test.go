package store

import (
	"encoding/json"
	"testing"
)

// pOverlapContractFacts derives the current decision facts for the raw fold
// witnesses. These cases change at most one child Domain and require no
// operator consent; a serious change takes the operator's conversational
// decision under CD-0243.
func pOverlapContractFacts(t *testing.T, s *Store, event Event) Event {
	t.Helper()
	definition := pinnedWorkflowDefinitionForTest(t, s, event.SubjectID)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(event.Payload, &fields); err != nil {
		t.Fatal(err)
	}
	if event.Kind == "work.intent_revised" {
		var contract map[string]json.RawMessage
		if err := json.Unmarshal(fields["contract"], &contract); err != nil {
			t.Fatal(err)
		}
		if workflowDeclaresDecisionDomains(definition) {
			if _, declared := contract["decision_domains"]; !declared {
				contract["decision_domains"] = mustJSONValue([]string{})
			}
		}
		fields["contract"] = mustJSONValue(contract)
		event.Payload = mustJSONValue(fields)
		event.PayloadVersion = CurrentEventPayloadVersion(event.Kind)
		return event
	}
	event = withPinnedDecisionFact(t, definition, event)
	if err := json.Unmarshal(event.Payload, &fields); err != nil {
		t.Fatal(err)
	}
	event.PayloadVersion = workflowContractPayloadVersion
	event.Payload = mustJSONValue(fields)
	return event
}

package store

import (
	"encoding/json"
	"strings"
	"testing"
)

// The predicate set carries seven independent rules. A refusal that names only
// "an invalid outcome predicate set" leaves the caller to read the store source
// to learn which rule applied. Each refusal must name its rule and its value.
func TestWorkflowContractPredicateRefusalNamesItsRule(t *testing.T) {
	payload := json.RawMessage(`{"kind":"exists"}`)
	for _, tc := range []struct {
		name      string
		ordinal   int
		predicate workflowContractPredicatePayload
		seen      map[string]bool
		wants     []string
	}{
		{
			name:      "missing prefix",
			predicate: workflowContractPredicatePayload{PredicateID: "readme-states-problem", OutcomeKind: "exists", OutcomePayload: payload},
			wants:     []string{"readme-states-problem", "predicate:"},
		},
		{
			name:      "empty id",
			predicate: workflowContractPredicatePayload{OutcomeKind: "exists", OutcomePayload: payload},
			wants:     []string{"ordinal 0", "predicate_id"},
		},
		{
			name:      "too short",
			predicate: workflowContractPredicatePayload{PredicateID: "predicate:", OutcomeKind: "exists", OutcomePayload: payload},
			wants:     []string{"predicate:", "11-128"},
		},
		{
			name:      "duplicate",
			predicate: workflowContractPredicatePayload{PredicateID: "predicate:alpha", OutcomeKind: "exists", OutcomePayload: payload},
			seen:      map[string]bool{"predicate:alpha": true},
			wants:     []string{"predicate:alpha", "more than once"},
		},
		{
			name:      "ordinal mismatch",
			ordinal:   2,
			predicate: workflowContractPredicatePayload{PredicateID: "predicate:alpha", Ordinal: 0, OutcomeKind: "exists", OutcomePayload: payload},
			wants:     []string{"predicate:alpha", "ordinal 0", "position 2"},
		},
		{
			name:      "no outcome kind",
			predicate: workflowContractPredicatePayload{PredicateID: "predicate:alpha", OutcomePayload: payload},
			wants:     []string{"predicate:alpha", "outcome_kind"},
		},
		{
			name:      "no outcome payload",
			predicate: workflowContractPredicatePayload{PredicateID: "predicate:alpha", OutcomeKind: "exists"},
			wants:     []string{"predicate:alpha", "outcome_payload"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen := tc.seen
			if seen == nil {
				seen = map[string]bool{}
			}
			err := validateWorkflowContractPredicate(tc.ordinal, tc.predicate, seen)
			if err == nil {
				t.Fatal("predicate was admitted, want refusal")
			}
			assertFailureKind(t, err, KindInvalidPayload)
			for _, want := range tc.wants {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("refusal %q does not name %q", err.Error(), want)
				}
			}
		})
	}
}

func TestWorkflowContractPredicateAdmitsAValidPredicate(t *testing.T) {
	err := validateWorkflowContractPredicate(1, workflowContractPredicatePayload{
		PredicateID: "predicate:readme-states-problem", Ordinal: 1,
		OutcomeKind: "exists", OutcomePayload: json.RawMessage(`{"kind":"exists"}`),
	}, map[string]bool{"predicate:other": true})
	if err != nil {
		t.Fatalf("valid predicate refused: %v", err)
	}
}

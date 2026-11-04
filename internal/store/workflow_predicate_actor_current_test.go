package store

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWorkflowPredicateDecoderIsClosedAndRejectsTrailingData(t *testing.T) {
	valid := []byte(`{"kind":"exists","surface":"git:tree","subjects":["path:cmd/concord"]}`)
	predicate, err := DecodeWorkflowPredicate(valid)
	if err != nil || predicate.Kind != PredicateExists {
		t.Fatalf("predicate=%+v err=%v", predicate, err)
	}
	for _, input := range []string{
		`{"kind":"exists","surface":"git:tree","subjects":["path:x"],"op":"not"}`,
		`{"kind":"expression","surface":"git:tree","subjects":["path:x"]}`,
		`{"kind":"exists","surface":"git:tree","subjects":["path:x"]} {}`,
		`{"kind":"exists","surface":"git:tree","subjects":["path:x"],"predicate":{"kind":"absent"}}`,
		`{"kind":"exists","surface":"git:tree","subjects":"path:x"}`,
		`{"kind":1,"surface":"git:tree","subjects":["path:x"]}`,
		`{"kind":"exists","surface":"git:tree","subjects":["path:x"],"kind":"absent"}`,
		`{"kind":"outcome","allowed":["no_change"],"decision_record":{"question":"q","question":"q","options_considered":["a"]}}`,
		`{"kind":"outcome","allowed":["accepted_decision"],"decision_record":{"question":"q","options_considered":["a"],"decision":"accepted_decision","rationale":"r","consequences":["c"],"inputs":["i"],"poc_findings":"none","supersedes":null,"superseded_by":null,"unknowns":[],"required_to_decide":[],"reviewer_actor_ref":"actor:` + strings.Repeat("a", 64) + `","extra":true}}`,
	} {
		if _, err := DecodeWorkflowPredicate([]byte(input)); err == nil {
			t.Fatalf("invalid predicate accepted: %s", input)
		}
	}
	if _, err := DecodeWorkflowPredicate([]byte(`{"kind":"exists","surface":"git:tree","subjects":["` + strings.Repeat("a", maxWorkflowPredicateBytes) + `"]}`)); err == nil {
		t.Fatal("oversized predicate was accepted")
	}
}

func TestWorkflowActorValidation(t *testing.T) {
	if err := ValidateWorkflowActor(WorkflowActor{PrincipalRef: "p1", ClientRef: "c1", AgentRef: "a1", SessionRef: "s1", ActorClass: ActorAgent}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowPredicateRoundTripUsesStrictShape(t *testing.T) {
	predicate := OutcomePredicate{Kind: PredicateOutcome, Allowed: []string{"no_change"}}
	raw, err := json.Marshal(predicate)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeWorkflowPredicate(raw)
	if err != nil || decoded.Kind != PredicateOutcome || len(decoded.Allowed) != 1 || decoded.Allowed[0] != "no_change" {
		t.Fatalf("round trip=%+v err=%v", decoded, err)
	}
}

func TestWorkflowDecisionRecordKeepsReviewerAndOutcomeGuards(t *testing.T) {
	valid := `{"kind":"outcome","allowed":["accepted_decision"],"decision_record":{"question":"q","options_considered":["a"],"decision":"accepted_decision","rationale":"r","consequences":["c"],"inputs":["i"],"poc_findings":"none","supersedes":null,"superseded_by":null,"unknowns":[],"required_to_decide":[],"reviewer_actor_ref":"actor:` + strings.Repeat("a", 64) + `"}}`
	predicate, err := DecodeWorkflowPredicate([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(predicate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeWorkflowPredicate(raw); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		strings.Replace(valid, `"reviewer_actor_ref":"actor:`+strings.Repeat("a", 64)+`"`, `"reviewer_actor_ref":"unverified"`, 1),
		strings.Replace(valid, `"allowed":["accepted_decision"]`, `"allowed":["insufficient_evidence"]`, 1),
		strings.Replace(valid, `"poc_findings":"none"`, `"poc_findings":""`, 1),
		strings.Replace(valid, `"options_considered":["a"]`, `"options_considered":["a","a"]`, 1),
	} {
		if _, err := DecodeWorkflowPredicate([]byte(invalid)); err == nil {
			t.Fatalf("invalid decision record accepted: %s", invalid)
		}
	}
}

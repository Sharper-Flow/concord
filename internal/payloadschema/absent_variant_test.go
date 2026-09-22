package payloadschema

import (
	"strings"
	"testing"
)

// An absent-kind outcome predicate must be writable through the tool-surface
// schema exactly as its oneOf variant describes it: kind, surface, subjects,
// and distinguish_from. A live session on core v11.13.4 recorded both
// refusals for the two shapes this payload takes — "unknown property
// ...outcome_payload.distinguish_from" when distinguish_from was supplied and
// "oneOf mismatch ... kind=\"absent\" requires [kind, surface, subjects,
// distinguish_from]" when it was omitted (work-086bbbe). This test pins the
// source-level contract: the composed action-input schema admits the absent
// variant with distinguish_from, and still refuses the shape that omits it.

func approveInputWithAbsentPredicate(payload string) string {
	return `{"work_id":"work-1","expected_version":11,"action_id":"approve_contract","idempotency_key":"absent-variant-1",
		"fields":{"contract_version":1,
			"premise":"The premise states the bounded change.",
			"required_evidence":["verification"],
			"rigor_class":"prototype_internal",
			"outcome_predicates":[{"predicate_id":"predicate:absent-thing","ordinal":0,"outcome_kind":"absent","outcome_payload":` + payload + `}]}}`
}

func TestAbsentOutcomePredicateValidatesWithDistinguishFrom(t *testing.T) {
	data := []byte(approveInputWithAbsentPredicate(`{"kind":"absent","surface":"go_source","subjects":["internal/store/workpin.go"],"distinguish_from":["renamed","archived"]}`))
	if err := Validate("work_transition_action_input", data); err != nil {
		t.Fatalf("the absent variant with distinguish_from was refused by the source schema: %v", err)
	}
}

func TestAbsentOutcomePredicateWithoutDistinguishFromStillRefused(t *testing.T) {
	data := []byte(approveInputWithAbsentPredicate(`{"kind":"absent","surface":"go_source","subjects":["internal/store/workpin.go"]}`))
	err := Validate("work_transition_action_input", data)
	if err == nil {
		t.Fatal("the absent variant without distinguish_from was accepted, but the oneOf requires it")
	}
	if !strings.Contains(err.Error(), "distinguish_from") && !strings.Contains(err.Error(), "absent") {
		t.Fatalf("refusal does not name the absent variant or its missing field: %v", err)
	}
}

package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// issue1013SuccessorContract is a complete recovery payload: the correction
// replaces the wrong subject the pinned contract named with the delivered one.
func issue1013SuccessorContract() json.RawMessage {
	return json.RawMessage(`{"contract_version":2,"premise":"corrected predicate subject","outcome_predicates":[{"predicate_id":"predicate:primary","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:workflow","immutable_subject_ref":"commit:e3d7c6e6","expected_result":"pass"}}],"required_evidence":["verification"],"route_conventions":[],"spec_mandate":[],"law_modifies":[],"rigor_class":"prototype_internal","supersede_reason":"the pinned subject named an unrelated commit","audit_evidence":["evidence:issue1013"]}`)
}

func issue1013Preflight(t *testing.T, s *Store, workID, action string, payload json.RawMessage, actor WorkflowActor) error {
	t.Helper()
	return WorkflowActionPreflightWithRegistry(context.Background(), s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
		WorkID: workID, ActionID: action, Payload: payload, Actor: actor,
	})
}

// The late verdict recovery admitted by the owning transaction must also pass
// the read-only preflight, and the admission closes again once the predicate
// holds a healthy verdict (#1013). The same journey shows the supersede
// refusal outside a recovery state names the stale-contract recovery route.
func TestIssue1013LateVerdictRecoveryPassesReadOnlyPreflight(t *testing.T) {
	const workID = "issue1013-late-verdict-preflight"
	s, owner := seedItemAtAcceptance(t, workID, false)
	reviewer := verdictReviewer(t, workID)
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:primary","verdict_kind":"outcome_mismatch","incomparable_with_approved":true}`), 0, reviewer); err != nil {
		t.Fatalf("record mismatch verdict: %v", err)
	}
	operator := operatorVerdictActor(t, workID)
	if err := runIssue933OperatorAction(t, s, workID, "confirm_premise", json.RawMessage(`{"contract_version":1}`), owner, operator); err != nil {
		t.Fatalf("confirm premise: %v", err)
	}
	healthy := json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:primary","verdict_kind":"ok"}`)
	if err := issue1013Preflight(t, s, workID, "record_verdict", healthy, owner); err != nil {
		t.Fatalf("read-only preflight refused the late verdict recovery: %v", err)
	}
	if err := runVerdictActionAs(t, s, workID, "record_verdict", healthy, 0, reviewer); err != nil {
		t.Fatalf("late healthy verdict recovery: %v", err)
	}
	// The predicate now holds a healthy verdict, so the recovery admission
	// must close and the read-only preflight return to the step refusal.
	err := issue1013Preflight(t, s, workID, "record_verdict", healthy, owner)
	if err == nil {
		t.Fatal("healthy comparable verdict replacement passed the read-only preflight")
	}
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindIllegalLifecycleTransition || failure.Detail != "workflow action is not declared on the current step" {
		t.Fatalf("post-recovery verdict failure=%v, want the current-step refusal", err)
	}
	// At release, away from the premise checkpoint and with the active
	// contract current, contract correction keeps its recovery refusal.
	err = issue1013Preflight(t, s, workID, "supersede_contract", issue1013SuccessorContract(), owner)
	if err == nil {
		t.Fatal("supersede_contract passed the read-only preflight outside a recovery state")
	}
	if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || failure.Detail != "contract recovery is available only for a stale workflow contract" {
		t.Fatalf("outside-recovery supersede failure=%v, want the stale-contract recovery refusal", err)
	}
}

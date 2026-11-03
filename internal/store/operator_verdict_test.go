package store

import (
	"context"
	"encoding/json"
	"testing"
)

func setWorkflowStepForOperatorVerdictTest(t *testing.T, s *Store, workID, stepID string) {
	t.Helper()
	moveWorkflowPlanFixtureFrontier(t, s, workID, stepID)
}

// #909: a host restart mints a new session identity, so the completing
// action can be the first place that tuple appears. The complete path lands
// the guard's actor-recording events instead of dropping them.
func TestCompleteRecordsAFirstSeenActorTuple(t *testing.T) {
	t.Parallel()
	const workID = "complete-first-seen-actor"
	f := qCurrentAcceptance(t, workID)
	ctx := t.Context()
	if err := f.run(ctx, "confirm_premise", "confirm", f.version(ctx, t), nil, nil); err != nil {
		t.Fatalf("confirm the premise: %v", err)
	}
	completeWorkflowPlanFixture(t, f.store, workID)
	// The restarted session: same principal and client, new agent and
	// session identity, never recorded on this work item before.
	restarted := WorkflowActor{PrincipalRef: f.owner.PrincipalRef, ClientRef: f.owner.ClientRef, AgentRef: "agent/owner-restarted", SessionRef: "session/" + workID + "-restarted", ActorClass: ActorAgent}
	restartedFixture := f
	restartedFixture.owner = restarted
	if err := restartedFixture.run(ctx, "complete", "complete", f.version(ctx, t), json.RawMessage(`{"impact_verdict":"non-breaking"}`), nil); err != nil {
		t.Fatalf("complete from a first-seen actor tuple refused: %v", err)
	}
	var state string
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "completed" {
		t.Fatalf("instance_state=%s, want completed", state)
	}
	restartedRef, _ := WorkflowActorRef(restarted)
	var recorded int
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_actors WHERE actor_ref=?`, restartedRef).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 1 {
		t.Fatalf("restarted actor recordings=%d, want 1", recorded)
	}
}

// #911: the premise confirmation is the last step where record_verdict is
// declared, so it refuses while any approved criterion lacks an associated
// passing receipt instead of letting the item wedge at complete.
func TestConfirmPremiseRequiresEveryPredicateVerdict(t *testing.T) {
	t.Parallel()
	const workID = "confirm-all-predicates"
	present := map[string]any{"predicate_id": "predicate:present", "ordinal": 0, "outcome_kind": "exists", "outcome_payload": map[string]any{"kind": "exists", "surface": "surface:one", "subjects": []string{"subject:one"}}}
	absent := map[string]any{"predicate_id": "predicate:absent", "ordinal": 1, "outcome_kind": "absent", "outcome_payload": map[string]any{"kind": "absent", "surface": "surface:two", "subjects": []string{"subject:two"}, "distinguish_from": []string{"archived"}}}
	f := qBuildAcceptancePredicates(t, workID, "", false, nil, false, present, absent)
	s := f.store
	if err := runVerdictActionAs(t, s, workID, "record_verdict", criterionAssociationPayload(f.receipt, "predicate:present"), 0, f.owner); err != nil {
		t.Fatalf("associate one criterion: %v", err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	missing, err := missingPredicateVerdicts(context.Background(), tx, workID)
	_ = tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || missing[0] != "predicate:absent" {
		t.Fatalf("missing predicates=%v, want [predicate:absent]", missing)
	}
	version := readWorkVersion(t, s, workID)
	err = f.run(t.Context(), "confirm_premise", "confirm", version, nil, nil)
	var failure *Failure
	if err == nil || !failureAs(err, &failure) || failure.Kind != KindMissingEvidence {
		t.Fatalf("confirm_premise with an unassociated criterion = %v, want the missing-verdict refusal", err)
	}
	if readWorkVersion(t, s, workID) != version {
		t.Fatal("the refused confirmation moved the item")
	}
}

package store

import (
	"context"
	"encoding/json"
	"testing"
)

func TestConfirmationAvailabilityTracksFrontier(t *testing.T) {
	ctx := context.Background()
	workID := "hidden-confirmation-work"
	f := rAcceptance(t, workID, nil, false)
	s := f.store

	pinIntents := func() (pin WorkPin) {
		pin, err := ReadWorkPin(ctx, s, workID)
		if err != nil {
			t.Fatalf("pin read failed: %v", err)
		}
		return pin
	}
	advertised := func(pin WorkPin) bool {
		return workPinContainsAction(pin.NextValidIntents, "confirm_premise")
	}

	if !advertised(pinIntents()) {
		t.Fatal("pin hid confirm_premise at its declared frontier")
	}
	if err := runVerdictActionAs(t, s, workID, "confirm_premise", json.RawMessage(`{}`), 0, f.owner); err != nil {
		t.Fatalf("confirm at review: %v", err)
	}
	completeWorkflowPlanFixture(t, s, workID)
	version := readWorkVersion(t, s, workID)
	request := planAdmissionRequest(workID, "confirm_premise", version, f.owner, json.RawMessage(`{}`))
	if advertised(pinIntents()) {
		t.Fatal("pin advertised confirm_premise with no frontier")
	}
	if err := planAdmissionPreflight(t, s, request); !hasFailureKind(err, KindIllegalLifecycleTransition) {
		t.Fatalf("no-frontier preflight = %v", err)
	}
	if err := planAdmissionDispatch(t, s, request); !hasFailureKind(err, KindIllegalLifecycleTransition) {
		t.Fatalf("no-frontier dispatch = %v", err)
	}
	if readWorkVersion(t, s, workID) != version {
		t.Fatal("refused confirmation changed version")
	}
}

// seedContractlessVerifyWorkflow puts a fix instance at its review step
// with no active contract. The instance row is the least state the
// confirmation and pin reads need; callers add a contract when they need one.
func seedContractlessVerifyWorkflow(t *testing.T, s *Store, workID string) {
	t.Helper()
	db := s.DatabaseForTesting()
	actorRef := DeriveWorkflowActorRef("principal/withheld", "client/withheld", "agent/withheld", "session/withheld")
	// These projections are write-seam-only. The guard row admits the direct seed.
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,?,?,?,?,?,?)`,
		actorRef, "principal/withheld", "client/withheld", "agent/withheld", "session/withheld", "agent", "2026-08-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_instances(work_id,instance_state,execution_actor_ref) VALUES(?,'intake',?)`,
		workID, actorRef); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	seedWorkflowPlanFrontier(t, s, workID, WorkflowStepPlanFix, "review")
}

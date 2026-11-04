package store

import (
	"context"
	"testing"
)

// TestUnavailableConfirmationIsNotAdvertised holds the intent-projection side
// of the confirmation gate. The pin states what the caller may do, and a
// confirm_premise whose deliverables are missing would refuse, so the pin must
// not advertise it. Once the deliverables stand, the action returns.
func TestUnavailableConfirmationIsNotAdvertised(t *testing.T) {
	ctx := context.Background()
	workID := "hidden-confirmation-work"
	otherWorkID := "hidden-confirmation-other"
	f, _, refs := pReceiptFixture(t, workID, 1, 1)
	s := f.store
	seedWork(t, s, otherWorkID)

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

	// The confirmation gate closes the action while deliverables are missing.
	hidden := pinIntents()
	if advertised(hidden) {
		t.Fatal("pin advertised confirm_premise while its deliverables were missing")
	}

	// With the verdict recorded, the deliverables stand and the
	// confirmation returns.
	if err := runVerdictActionAs(t, s, workID, "record_verdict", pReceiptBatch(refs, 0), 0, f.owner); err != nil {
		t.Fatal(err)
	}
	if !advertised(pinIntents()) {
		t.Fatal("pin hid confirm_premise while its deliverables stood")
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

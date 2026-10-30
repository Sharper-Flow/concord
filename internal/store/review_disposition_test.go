package store

import "testing"

func TestReviewDispositionMainMayAssociateProofAfterExecution(t *testing.T) {
	f, _, refs := pReceiptFixture(t, "review-self-association", 1, 1)
	var executor string
	if err := f.store.db.QueryRow(`SELECT execution_actor_ref FROM workflow_instances WHERE work_id=?`, f.workID).Scan(&executor); err != nil {
		t.Fatal(err)
	}
	if executor != mustActorRef(t, f.owner) {
		t.Fatalf("fixture executor=%s, want the associating coordinator", executor)
	}
	if err := runVerdictActionAs(t, f.store, f.workID, "record_verdict", pReceiptBatch(refs, 0), 0, f.owner); err != nil {
		t.Fatalf("coordinator cannot associate producer proof after executing: %v", err)
	}
}

func TestReviewDispositionReplacesShipGate(t *testing.T) {
	for _, disposition := range []string{"continue", "correction_required", "revision_required", "ready_for_final_proof"} {
		t.Run(disposition, func(t *testing.T) {
			report := reviewBlockFixture()
			report.Disposition = disposition
			if err := validateReviewBlock(report); err != nil {
				t.Fatalf("explicit disposition is rejected by the retired ship gate: %v", err)
			}
		})
	}
}

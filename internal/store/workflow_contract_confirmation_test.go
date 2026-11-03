package store

import (
	"encoding/json"
	"testing"
)

// Confirmation binds to its contract version; completion does not re-confirm it.
func TestContractRevisionInvalidatesPriorPremiseConfirmation(t *testing.T) {
	t.Parallel()
	const workID = "cd31-work"
	f := qCurrentAcceptance(t, workID)
	s := f.store
	if err := f.run(t.Context(), "confirm_premise", "confirm-first", f.version(t.Context(), t), nil, nil); err != nil {
		t.Fatal(err)
	}
	var confirmed int
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_premise_confirmations WHERE work_id=? AND contract_version=1`, workID).Scan(&confirmed); err != nil || confirmed != 1 {
		t.Fatalf("first confirmation count = %d, want 1: %v", confirmed, err)
	}
	contract := pContractSuccessor(t, s, workID, "Align the five sibling provider services.")
	if err := reviseContractRecord(t, s, workID, f.owner, contract, readWorkVersion(t, s, workID), nil); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_premise_confirmations c WHERE c.work_id=? AND c.contract_version=(SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL)`, workID, workID).Scan(&confirmed); err != nil || confirmed != 0 {
		t.Fatalf("revised contract borrowed the old confirmation: %d, %v", confirmed, err)
	}
	if step := currentStep(t, s, workID); step != "release" {
		t.Fatalf("revision reset release to %s", step)
	}
	pin := issue1013Pin(t, s, workID)
	if issue1013HasIntent(pin, "confirm_premise") || !issue1013HasIntent(pin, "complete") {
		t.Fatal("terminal update advertised late confirmation or hid completion")
	}
	if err := f.run(t.Context(), "confirm_premise", "late-confirm", f.version(t.Context(), t), nil, nil); !hasFailureKind(err, KindIllegalLifecycleTransition) {
		t.Fatalf("off-step confirmation = %v", err)
	}
	if err := f.run(t.Context(), "complete", "complete", f.version(t.Context(), t), json.RawMessage(`{"impact_verdict":"non-breaking"}`), nil); err != nil {
		t.Fatalf("completion after ordinary revision = %v", err)
	}
}

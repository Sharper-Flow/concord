package store

import (
	"strings"
	"testing"
)

// Confirmation binds to the current contract version. A changed premise needs
// a new confirmation even when the criterion content and its proof are intact.
func TestContractRevisionInvalidatesPriorPremiseConfirmation(t *testing.T) {
	t.Parallel()
	const workID = "cd31-work"
	f := qCurrentAcceptance(t, workID)
	s := f.store
	actor, err := WorkflowActorRef(f.owner)
	if err != nil {
		t.Fatal(err)
	}
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
	completion := workflowCompletedFixtureEvent("cd31-terminal", workID, actor, actor, readWorkVersion(t, s, workID))
	err = CompleteWorkflow(t.Context(), s, completion)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindMissingEvidence || failure.Clause != 5 || !strings.Contains(failure.Detail, "premise") {
		t.Fatalf("completion after revision = %v, want the premise refusal at clause 5", err)
	}
}

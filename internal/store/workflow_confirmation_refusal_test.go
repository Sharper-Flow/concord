package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestConfirmPremiseRefusalNamesTheMissingArtifact pins the confirmation
// prerequisite's refusal: it names the missing investigation artifact and the
// observation that satisfies it, so the caller is not sent to refresh context.
func TestConfirmPremiseRefusalNamesTheMissingArtifact(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	workID := "confirmation-refusal-artifact"
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	seedIssue31DomainRegistry(t, s)
	seedContractlessVerifyWorkflow(t, s, workID)
	seedActiveBreakFixContract(t, s, workID)

	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	err = requireRecordedInvestigationArtifact(ctx, tx, workID)

	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("confirm_premise without an investigation artifact must refuse, got %v", err)
	}
	if failure.Kind != KindMissingEvidence {
		t.Fatalf("kind %q, want %q: %s", failure.Kind, KindMissingEvidence, failure.Detail)
	}
	if !strings.Contains(failure.Detail, "investigation artifact") {
		t.Errorf("detail %q does not name the missing artifact", failure.Detail)
	}
	wantRemedy := "call concord_work_define.observation_record with this work_id and refs containing the bare domain_id of a current Product Domain"
	if failure.RecoveryAction != wantRemedy {
		t.Errorf("refusal remedy = %q, want %q", failure.RecoveryAction, wantRemedy)
	}
}

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

	// The confirmation gate closes the action while the deliverables stand
	// missing; the investigation artifact alone does not open it.
	hidden := pinIntents()
	if advertised(hidden) {
		t.Fatal("pin advertised confirm_premise while its deliverables were missing")
	}
	insertInvestigationGateObservation(t, s, workID, "obs:"+strings.Repeat("6", 16), []string{"root", otherWorkID})
	if still := pinIntents(); advertised(still) {
		t.Fatalf("artifact read advertised confirmation before its verdict: %v", still.NextValidIntents)
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

// seedContractlessVerifyWorkflow puts a break-fix instance at its verify step
// with no active contract. The instance row is the least state the
// confirmation and pin reads need; callers add a contract when they need one.
func seedContractlessVerifyWorkflow(t *testing.T, s *Store, workID string) WorkflowReadDefinition {
	t.Helper()
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	digest, err := WorkflowDefinitionDigest(registered.Definition)
	if err != nil {
		t.Fatal(err)
	}
	db := s.DatabaseForTesting()
	actorRef := DeriveWorkflowActorRef("principal/withheld", "client/withheld", "agent/withheld", "session/withheld")
	// These projections are write-seam-only. The guard row admits the direct seed.
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
			t.Fatal(err)
		}
	}()
	if _, err := db.Exec(`INSERT OR IGNORE INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,?,?,?,?,?,?)`,
		actorRef, "principal/withheld", "client/withheld", "agent/withheld", "session/withheld", "agent", "2026-08-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_instances(work_id,definition_ref,definition_digest,current_step,instance_state,execution_actor_ref) VALUES(?,?,?,?,'running',?)`,
		workID, registered.Definition.Ref, digest, "verify", actorRef); err != nil {
		t.Fatal(err)
	}
	return WorkflowReadDefinition{Ref: registered.Definition.Ref, Digest: digest}
}

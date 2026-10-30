package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestWithheldOperatorQuestionStatesItsReason holds the distinction between a
// step that has no operator question and a step whose question is withheld
// pending an investigation artifact. Both once read as a bare null on the
// pin, so a caller could not tell "nothing to answer here" from "answerable
// once you record an observation", and the only way to learn which was to
// call confirm_premise and read the refusal.
func TestWithheldOperatorQuestionStatesItsReason(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	workID := "withheld-question-work"
	otherWorkID := "withheld-question-other"
	seedWork(t, s, workID)
	seedWork(t, s, otherWorkID)
	seedWorkflowLaw(t, s)
	seedIssue31DomainRegistry(t, s)
	definition := seedWithheldQuestionWorkflow(t, s, workID)

	// A step with no question-bearing action withholds nothing. The absent
	// question there is the shape of the step, not a gate the caller can open.
	question, withheld, err := workflowOperatorQuestionTx(ctx, s.DatabaseForTesting(), workID, "repair", 1, definition, WorkflowReadContract{})
	if err != nil {
		t.Fatalf("question read failed: %v", err)
	}
	if question != nil || withheld != nil {
		t.Fatalf("step repair declares no approval action; got question=%v withheld=%v", question, withheld)
	}

	// A human checkpoint with no investigation artifact withholds its question
	// and must say so, naming the action and the remedy.
	question, withheld, err = workflowOperatorQuestionTx(ctx, s.DatabaseForTesting(), workID, "verify", 1, definition, WorkflowReadContract{})
	if err != nil {
		t.Fatalf("question read failed: %v", err)
	}
	if question != nil {
		t.Fatal("confirm_premise question admitted without an investigation artifact")
	}
	if withheld == nil {
		t.Fatal("a withheld question read as a bare absence; the caller cannot tell it from a step with no checkpoint")
	}
	if withheld.ActionID != "confirm_premise" {
		t.Errorf("withheld action %q, want confirm_premise", withheld.ActionID)
	}
	if !strings.Contains(withheld.Reason, "investigation artifact") {
		t.Errorf("reason %q does not state what is missing", withheld.Reason)
	}
	wantRemedy := "call concord_work_define.observation_record with this work_id and refs containing the bare domain_id of a current Product Domain and the id of another work item"
	if withheld.Remedy != wantRemedy {
		t.Errorf("withheld remedy = %q, want %q", withheld.Remedy, wantRemedy)
	}

	// The pin of a contract-less legacy pin reads no contract block, so it
	// carries neither a pending nor a withheld question; the projection
	// above is the selection-time answer.
	pin, err := ReadWorkPin(ctx, s, workID)
	if err != nil {
		t.Fatalf("pin read failed: %v", err)
	}
	if pin.PendingOperatorDecision != nil || pin.WithheldOperatorDecision != nil {
		t.Fatalf("pin published question=%+v withheld=%+v for a contract-less pin", pin.PendingOperatorDecision, pin.WithheldOperatorDecision)
	}

	// Once the artifact resolves, the question opens and nothing is withheld.
	insertInvestigationGateObservation(t, s, workID, "obs:"+strings.Repeat("5", 16), []string{"root", otherWorkID})
	question, withheld, err = workflowOperatorQuestionTx(ctx, s.DatabaseForTesting(), workID, "verify", 1, definition, WorkflowReadContract{})
	if err != nil {
		t.Fatalf("question refused after investigation: %v", err)
	}
	if question == nil {
		t.Fatal("question stayed closed after a resolvable investigation artifact")
	}
	if withheld != nil {
		t.Errorf("withheld=%+v alongside an open question", withheld)
	}
}

// TestConfirmPremiseRefusalNamesTheMissingArtifact pins the call-path refusal.
// Without it the selection guard falls through to a message that reports a
// question which expired, and the caller is sent to refresh context instead of
// to the observation that opens the confirmation.
func TestConfirmPremiseRefusalNamesTheMissingArtifact(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	workID := "withheld-question-refusal"
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	seedIssue31DomainRegistry(t, s)
	seedWithheldQuestionWorkflow(t, s, workID)
	seedActiveBreakFixContract(t, s, workID)

	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	err = validateWorkflowOperatorSelectionTx(ctx, tx, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
		WorkID: workID, ActionID: "confirm_premise", ExpectedVersion: version,
		SelectedChoice: "confirm",
	})

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
// confirm_premise whose deliverables are missing would refuse at the
// selection guard, so the pin must not advertise it. An active contract
// projects neither a question nor a withholding; once the deliverables
// stand, the action returns.
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
	// missing, and the active contract projects neither question nor
	// withholding: the investigation artifact changes nothing there.
	hidden := pinIntents()
	if hidden.PendingOperatorDecision != nil || hidden.WithheldOperatorDecision != nil {
		t.Fatalf("active-contract pin = %+v / %+v, want neither", hidden.PendingOperatorDecision, hidden.WithheldOperatorDecision)
	}
	if advertised(hidden) {
		t.Fatal("pin advertised confirm_premise while its deliverables were missing")
	}
	insertInvestigationGateObservation(t, s, workID, "obs:"+strings.Repeat("6", 16), []string{"root", otherWorkID})
	if still := pinIntents(); still.PendingOperatorDecision != nil || still.WithheldOperatorDecision != nil || advertised(still) {
		t.Fatalf("artifact read changed the active-contract pin: %+v / %+v advertised=%v", still.PendingOperatorDecision, still.WithheldOperatorDecision, advertised(still))
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

// seedWithheldQuestionWorkflow puts a break-fix instance at one step with no
// active contract: the legacy pin whose confirmation folds the human
// question path. The instance row is the least state the projection and the
// selection guard both need.
func seedWithheldQuestionWorkflow(t *testing.T, s *Store, workID string) WorkflowReadDefinition {
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

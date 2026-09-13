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
	definition := seedWithheldQuestionWorkflow(t, s, workID, "verify")

	// A step with no approval-required action withholds nothing. The absent
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
	if withheld.Remedy == "" {
		t.Error("withheld question states no remedy, so the caller learns what is wrong but not what to do")
	}

	// The pin carries the same reason, so a caller that reads before it acts
	// never has to provoke a refusal to learn why the question is closed.
	pin, err := ReadWorkPin(ctx, s, workID)
	if err != nil {
		t.Fatalf("pin read failed: %v", err)
	}
	if pin.PendingOperatorDecision != nil {
		t.Fatal("pin published a question the gate withholds")
	}
	if pin.WithheldOperatorDecision == nil || pin.WithheldOperatorDecision.ActionID != "confirm_premise" {
		t.Fatalf("pin withheld decision = %+v, want confirm_premise with a reason", pin.WithheldOperatorDecision)
	}
	if pin.WithheldOperatorDecision.Reason != withheld.Reason {
		t.Errorf("pin reason %q differs from the gate's %q", pin.WithheldOperatorDecision.Reason, withheld.Reason)
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
// to the observation that opens the question.
func TestConfirmPremiseRefusalNamesTheMissingArtifact(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	workID := "withheld-question-refusal"
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	seedIssue31DomainRegistry(t, s)
	seedWithheldQuestionWorkflow(t, s, workID, "verify")

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
		SelectedChoice: "confirm", DecisionContextDigest: "sha256:" + strings.Repeat("a", 64),
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
}

// seedWithheldQuestionWorkflow puts a break-fix instance at one step with an
// approved contract, which is the least state a pin read and the selection
// guard both need.
func seedWithheldQuestionWorkflow(t *testing.T, s *Store, workID, step string) WorkflowReadDefinition {
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
	// These projections are fold-only. The guard row admits the direct seed.
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.Exec(`DELETE FROM fold_guard`); err != nil {
			t.Fatal(err)
		}
	}()
	if _, err := db.Exec(`INSERT OR IGNORE INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,?,?,?,?,?,?)`,
		actorRef, "principal/withheld", "client/withheld", "agent/withheld", "session/withheld", "agent", "2026-08-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_instances(work_id,definition_ref,definition_version,definition_digest,current_step,instance_state,execution_actor_ref) VALUES(?,?,?,?,?,'running',?)`,
		workID, registered.Definition.Ref, registered.Definition.Version, digest, step, actorRef); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,1,'withheld question premise','internal_sqlite','[]','[]','2026-08-01T00:00:00Z',?,'[]','[]',1,'prototype_internal')`,
		workID, actorRef); err != nil {
		t.Fatal(err)
	}
	return WorkflowReadDefinition{Ref: registered.Definition.Ref, Version: registered.Definition.Version, Digest: digest}
}

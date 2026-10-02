package store

// The refinement conformance test binds the abstract liveness model to the
// real engine: it replays a witness path through the real store — repair
// accept, refinement rejection, fresh settling review, the settling accept —
// and after each action compares the state the tx-scoped loader folds from
// the event history with the abstract successor the model computed for the
// same action. The model cannot drift from the fold without this test
// failing.

import (
	"context"
	"testing"
)

// conformanceCheckpoint maps the loaded admission state onto the abstract
// model state and compares the two.
func conformanceCheckpoint(t *testing.T, s *Store, definition WorkflowDefinition, step string, want admissionModelState, label string) {
	t.Helper()
	ctx := context.Background()
	tx, txErr := s.db.BeginTx(ctx, nil)
	if txErr != nil {
		t.Fatalf("%s: begin: %v", label, txErr)
	}
	defer func() { _ = tx.Rollback() }()
	loaded, err := loadWorkflowAdmissionStateTx(ctx, tx, "admission-conformance", definition, step, "workflow_admission_conformance_test")
	if err != nil {
		t.Fatalf("%s: load: %v", label, err)
	}
	got := admissionModelState{step: loaded.Step, debt: loaded.ReviewDebt, ready: loaded.ReadyReviewVerdict}
	if loaded.ReadyReviewAttemptID != "" && loaded.ReadyReviewVerdict == "" {
		// The loader folds a ready pre-CD-0197 review with an absent verdict;
		// the model names that state ready "absent".
		got.ready = "absent"
	}
	if got != want {
		t.Fatalf("%s: loaded state %+v (step %q debt %q ready %q) != abstract successor %+v", label, loaded, got.step, got.debt, got.ready, want)
	}
}

// TestAdmissionConformanceReplayRejectionReviewAndSettlingAccept replays the
// CON-796 witness path on the current break-fix definition and asserts, at
// every checkpoint, that the loader's folded state equals the abstract
// successor of the previous state under the action just performed.
func TestAdmissionConformanceReplayRejectionReviewAndSettlingAccept(t *testing.T) {
	const workID = "admission-conformance"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	definition, defErr := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if defErr != nil {
		t.Fatal(defErr)
	}
	def := definition.Definition
	ownerRef, err := WorkflowActorRef(fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	acceptor := reviewGateAcceptor(workID)
	at := int64(100)

	model := admissionModelState{step: "repair", debt: ReviewDebtNone}

	// start_repair: the step-entry advance moves repair's start, and the
	// model's successor for an advance-mode action follows the same forward
	// edge. The model state keeps its step "repair" because the engine's
	// start action opens the epoch without leaving the step; the checkpoint
	// asserts that equality, not an assumption.
	reviewGateStartStep(t, s, workID, "repair", "start_repair", fixture.owner)
	conformanceCheckpoint(t, s, def, "repair", model, "after start_repair")

	// The repair attempt dispatches and completes; no review debt exists, so
	// the model state is unchanged.
	reviewGateRunAttempt(t, s, workID, "attempt:"+workID+":repair", "repair", 1, reviewGateLane(t, "implementation"), ownerRef, at)
	at += 2
	conformanceCheckpoint(t, s, def, "repair", model, "after repair dispatch")

	// The repair accept advances to refine.
	if err := reviewGateAcceptResult(t, s, workID, "attempt:"+workID+":repair", 1, acceptor); err != nil {
		t.Fatalf("accept repair: %v", err)
	}
	model = admissionSuccessor(def, model, "accept_worker_result")
	conformanceCheckpoint(t, s, def, "refine", model, "after repair accept")

	// start_refine opens the refinement epoch; the step stays refine.
	reviewGateStartStep(t, s, workID, "refine", "start_refine", fixture.owner)
	conformanceCheckpoint(t, s, def, "refine", model, "after start_refine")

	// The first review attempt completes and the acceptor rejects it: the
	// rejection opens the post-rejection review debt.
	reviewGateRunAttempt(t, s, workID, "attempt:"+workID+":review-1", "refine", 1, reviewGateLane(t, "review"), ownerRef, at)
	at += 2
	conformanceCheckpoint(t, s, def, "refine", model, "after review dispatch")
	reviewGateRejectResult(t, s, workID, "attempt:"+workID+":review-1", 1, acceptor)
	model = admissionSuccessor(def, model, "reject_worker_result")
	conformanceCheckpoint(t, s, def, "refine", model, "after rejection")

	// A fresh review of the repaired result completes with a ship verdict:
	// the model's dispatch move names the settling review ready.
	reviewGateRunAttemptWithVerdict(t, s, workID, "attempt:"+workID+":review-2", 1, reviewGateLane(t, "review"), ownerRef, "ship", at)
	model = admissionSuccessor(def, model, "dispatch_worker")
	conformanceCheckpoint(t, s, def, "refine", model, "after settling review completion")

	// The ready review's acceptance is the combined accept: it names the
	// ready attempt's identity and asserts delivery, so it settles the debt
	// and advances to the delivery gate in one action. The pure model's
	// accept move stands for that identity-satisfying route.
	if err := acceptRefineResult(t, s, workID, "attempt:"+workID+":review-2", 1, acceptor); err != nil {
		t.Fatalf("accept the settling review: %v", err)
	}
	model = admissionSuccessor(def, model, "accept_worker_result")
	conformanceCheckpoint(t, s, def, "delivery", model, "after settling accept")

	// The settled debt leaves the fold debt-free: the model and the loader
	// agree the repaired result may advance to delivery.
	if model.debt != ReviewDebtNone {
		t.Fatalf("settling accept left model debt %q", model.debt)
	}
}

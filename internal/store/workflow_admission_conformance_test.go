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

// admissionConformanceView is the part of the folded admission state that
// workflowAdmit reads and the abstract model folds. The ready review's
// identity is compared by presence, because the model names a synthetic
// attempt, and its settling flag only beside a ready review. Three read
// fields stay out: the breaking notices and open external conditions, which
// the model holds at zero, and the evidence-binding recovery, an exit the
// model does not count. The raw attempt lifecycle and latest disposition are
// loaded for callers but never decided on; the hold and recovery flags they
// feed are compared instead.
type admissionConformanceView struct {
	Step                        string
	Lifecycle                   string
	InstanceState               string
	CorrectionWorkflow          bool
	ReviewStep                  bool
	ActiveContracts             int64
	LawPinStale                 bool
	DesignStale                 bool
	ReviewDebt                  WorkflowReviewDebt
	ReadyReview                 bool
	ReadyReviewSettles          bool
	LateVerdictRoute            bool
	WorkerFailureRecovery       bool
	CorrectionRecovery          bool
	CorrectionRequestRecovery   bool
	CorrectionEscalated         bool
	SameStepFailedAttempts      int64
	FailedWorkerRetry           bool
	DispatchHold                bool
	PendingOperatorDecision     bool
	CompleteStepCorrection      bool
	ContractCorrectionAvailable bool
	DeliveryStarted             bool
	DeliveryProofRequired       bool
	DeliveryProofReady          bool
	DeliveryFailure             bool
	DeliveryProofFailure        bool
	MandatePresent              bool
	MandateMissing              bool
	MandateBindingStep          string
	MandateAddedByContract      bool
	MandateCorrectionAction     string
	MandateFailure              bool
}

func admissionConformanceViewOf(state WorkflowAdmissionState) admissionConformanceView {
	view := admissionConformanceView{
		Step: state.Step, Lifecycle: state.Lifecycle, InstanceState: state.InstanceState,
		CorrectionWorkflow: state.CorrectionWorkflow, ReviewStep: state.ReviewStep,
		ActiveContracts: state.ActiveContracts, LawPinStale: state.LawPinStale, DesignStale: state.DesignStale,
		ReviewDebt: state.ReviewDebt, ReadyReview: state.ReadyReviewAttemptID != "",
		LateVerdictRoute: state.LateVerdictRoute, WorkerFailureRecovery: state.WorkerFailureRecovery,
		CorrectionRecovery: state.CorrectionRecovery, CorrectionRequestRecovery: state.CorrectionRequestRecovery,
		CorrectionEscalated: state.CorrectionEscalated, SameStepFailedAttempts: state.SameStepFailedAttempts,
		FailedWorkerRetry: state.FailedWorkerRetry != nil,
		DispatchHold:      state.DispatchHold, PendingOperatorDecision: state.PendingOperatorDecision,
		CompleteStepCorrection: state.CompleteStepCorrection, ContractCorrectionAvailable: state.ContractCorrectionAvailable,
		DeliveryStarted: state.Delivery.Started, DeliveryProofRequired: state.Delivery.ProofRequired,
		DeliveryFailure: state.Delivery.Failure != nil, MandateFailure: state.Mandate.Failure != nil,
		MandatePresent: state.Mandate.Present, MandateMissing: state.Mandate.LawID != "", MandateBindingStep: state.Mandate.BindingStep,
		MandateAddedByContract: state.Mandate.AddedByContract, MandateCorrectionAction: state.Mandate.CorrectionAction,
	}
	if state.Delivery.ProofRequired {
		view.DeliveryProofReady = state.Delivery.ProofReady
		view.DeliveryProofFailure = state.Delivery.ProofFailure != nil
	}
	if view.ReadyReview {
		view.ReadyReviewSettles = state.ReadyReviewSettles
	}
	return view
}

// conformanceCheckpoint folds the work item's admission state through the
// real tx-scoped loader and compares it with the model state lifted through
// the same rules the liveness checks decide over.
func conformanceCheckpoint(t *testing.T, s *Store, workID string, definition WorkflowDefinition, want admissionModelState, label string) {
	t.Helper()
	ctx := context.Background()
	tx, txErr := s.db.BeginTx(ctx, nil)
	if txErr != nil {
		t.Fatalf("%s: begin: %v", label, txErr)
	}
	defer func() { _ = tx.Rollback() }()
	var step string
	if err := tx.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		t.Fatalf("%s: read step: %v", label, err)
	}
	loaded, _, err := loadWorkflowAdmissionStateTx(ctx, tx, workID, definition, step, "workflow_admission_conformance_test")
	if err != nil {
		t.Fatalf("%s: load: %v", label, err)
	}
	got, model := admissionConformanceViewOf(loaded), admissionConformanceViewOf(admissionWorkflowState(definition, want))
	if got != model {
		t.Fatalf("%s: loaded fold %+v != abstract successor %s lifted to %+v", label, got, want, model)
	}
}

// TestAdmissionConformanceReplayRejectionReviewAndSettlingAccept replays the
// CON-796 witness path on the current break-fix definition and asserts, at
// every checkpoint, that the loader's fold equals the abstract successor of
// the previous state under the action just performed, lifted through the
// rules the liveness checks decide over.
func TestAdmissionConformanceReplayRejectionReviewAndSettlingAccept(t *testing.T) {
	const workID = "admission-conformance"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	def := mustBuiltinDefinition(t, "workflow.break_fix").Definition
	ownerRef, err := WorkflowActorRef(fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	acceptor := reviewGateAcceptor(workID)
	at := int64(100)

	// The fixture parks the item at repair under one approved contract.
	model := admissionModelState{step: "repair", debt: ReviewDebtNone, contracts: 1}
	conformanceCheckpoint(t, s, workID, def, model, "seeded at repair")

	reviewGateStartStep(t, s, workID, "repair", "start_repair", fixture.owner)
	model = admissionSuccessor(def, model, "start_repair")
	conformanceCheckpoint(t, s, workID, def, model, "after start_repair")

	// The repair attempt dispatches and completes: the completed attempt
	// holds the step until its accept.
	reviewGateRunAttempt(t, s, workID, "attempt:"+workID+":repair", "repair", 1, reviewGateLane(t, "implementation"), ownerRef, at)
	at += 2
	model = admissionSuccessor(def, model, "dispatch_worker")
	conformanceCheckpoint(t, s, workID, def, model, "after repair dispatch")

	if err := reviewGateAcceptResult(t, s, workID, "attempt:"+workID+":repair", 1, acceptor); err != nil {
		t.Fatalf("accept repair: %v", err)
	}
	model = admissionSuccessor(def, model, "accept_worker_result")
	conformanceCheckpoint(t, s, workID, def, model, "after repair accept")

	reviewGateStartStep(t, s, workID, "refine", "start_refine", fixture.owner)
	model = admissionSuccessor(def, model, "start_refine")
	conformanceCheckpoint(t, s, workID, def, model, "after start_refine")

	// The first review completes and the acceptor rejects it: the rejection
	// opens the post-rejection review debt.
	reviewGateRunAttempt(t, s, workID, "attempt:"+workID+":review-1", "refine", 1, reviewGateLane(t, "review"), ownerRef, at)
	at += 2
	model = admissionSuccessor(def, model, "dispatch_worker")
	conformanceCheckpoint(t, s, workID, def, model, "after review dispatch")
	reviewGateRejectResult(t, s, workID, "attempt:"+workID+":review-1", 1, acceptor)
	model = admissionSuccessor(def, model, "reject_worker_result")
	conformanceCheckpoint(t, s, workID, def, model, "after rejection")

	// A fresh review of the repaired result completes with a ship verdict:
	// the model's dispatch move names the settling review ready.
	reviewGateRunAttemptWithVerdict(t, s, workID, "attempt:"+workID+":review-2", 1, reviewGateLane(t, "review"), ownerRef, "ship", at)
	model = admissionSuccessor(def, model, "dispatch_worker")
	conformanceCheckpoint(t, s, workID, def, model, "after settling review completion")

	// The ready review's acceptance names the ready attempt and asserts
	// delivery, so it settles the debt and advances to the delivery gate.
	refineProofSeedGreenRun(t, s, workID, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
	model.proof = true
	conformanceCheckpoint(t, s, workID, def, model, "after current-epoch verification")
	if err := acceptRefineResult(t, s, workID, "attempt:"+workID+":review-2", 1, acceptor); err != nil {
		t.Fatalf("accept the settling review: %v", err)
	}
	model = admissionSuccessor(def, model, "accept_worker_result")
	conformanceCheckpoint(t, s, workID, def, model, "after settling accept")
	if model.debt != ReviewDebtNone || model.step != "delivery" {
		t.Fatalf("settling accept left model step %q debt %q", model.step, model.debt)
	}
}

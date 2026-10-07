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
	"encoding/json"
	"strings"
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
	RefinementWorkflow          bool
	ReviewStep                  bool
	ActiveContracts             int64
	LawPinStale                 bool
	DesignStale                 bool
	ArtifactStale               bool
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
	DeliveryJobsRecorded        bool
	DeliveryJobsUnsatisfiedKeys string
	DeliveryJobsScopeKeys       string
	DeliveryJobsIntegrated      bool
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
		RefinementWorkflow: state.RefinementWorkflow, ReviewStep: state.ReviewStep,
		ActiveContracts: state.ActiveContracts, LawPinStale: state.LawPinStale, DesignStale: state.DesignStale,
		ArtifactStale: state.ArtifactStale,
		ReviewDebt:    state.ReviewDebt, ReadyReview: state.ReadyReviewAttemptID != "",
		LateVerdictRoute: state.LateVerdictRoute, WorkerFailureRecovery: state.WorkerFailureRecovery,
		CorrectionRecovery: state.CorrectionRecovery, CorrectionRequestRecovery: state.CorrectionRequestRecovery,
		CorrectionEscalated: state.CorrectionEscalated, SameStepFailedAttempts: state.SameStepFailedAttempts,
		FailedWorkerRetry: state.FailedWorkerRetry != nil,
		DispatchHold:      state.DispatchHold, PendingOperatorDecision: state.PendingOperatorDecision,
		CompleteStepCorrection: state.CompleteStepCorrection, ContractCorrectionAvailable: state.ContractCorrectionAvailable,
		DeliveryStarted: state.Delivery.Started, DeliveryProofRequired: state.Delivery.ProofRequired,
		DeliveryFailure: state.Delivery.Failure != nil, MandateFailure: state.Mandate.Failure != nil,
		DeliveryJobsRecorded: state.Delivery.JobsRecorded, DeliveryJobsUnsatisfiedKeys: state.Delivery.JobsUnsatisfiedKeys,
		DeliveryJobsScopeKeys: state.Delivery.JobsScopeKeys, DeliveryJobsIntegrated: state.Delivery.JobsIntegrated,
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
	loaded := conformanceFold(t, s, workID, definition, label)
	got, model := admissionConformanceViewOf(loaded), admissionConformanceViewOf(admissionWorkflowState(definition, want))
	if got != model {
		t.Fatalf("%s: loaded fold %+v != abstract successor %s lifted to %+v", label, got, want, model)
	}
}

func conformanceFold(t *testing.T, s *Store, workID string, definition WorkflowDefinition, label string) WorkflowAdmissionState {
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
	loaded, _, _, err := loadWorkflowAdmissionStateTx(ctx, tx, workID, definition, step, "workflow_admission_conformance_test")
	if err != nil {
		t.Fatalf("%s: load: %v", label, err)
	}
	return loaded
}

// The historical fence and dispatch hold survive re-entry until a fresh
// start. Artifact truth must also match before that start, without masking
// the loader's bit or changing those independently governed fence rules.
func conformanceArtifactCheckpoint(t *testing.T, s *Store, workID string, definition WorkflowDefinition, want admissionModelState, label string) {
	t.Helper()
	got := conformanceFold(t, s, workID, definition, label)
	if got.Step != want.step || got.ArtifactStale != want.artifactStale {
		t.Fatalf("%s: loaded step %s artifact stale %v, want step %s artifact stale %v", label, got.Step, got.ArtifactStale, want.step, want.artifactStale)
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
	// The fixture appends this repair attempt without a worker-job binding,
	// so its accept is the advancing successor; the job-bound accept that
	// holds is replayed by TestAdmissionConformanceLocalJobAcceptHolds.
	repairAccepts := admissionSuccessors(def, model, "accept_worker_result")
	model = repairAccepts[len(repairAccepts)-1]
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
	// The settling review carries no worker job and asserts delivery, so
	// its accept is the advancing successor.
	settlingAccepts := admissionSuccessors(def, model, "accept_worker_result")
	model = settlingAccepts[len(settlingAccepts)-1]
	conformanceCheckpoint(t, s, workID, def, model, "after settling accept")
	if model.debt != ReviewDebtNone || model.step != "delivery" {
		t.Fatalf("settling accept left model step %q debt %q", model.step, model.debt)
	}

	// The staleness cycle (CD-0201 D5/D6), replayed through the same fold:
	// the gate's delivery reaches verify, an unhealthy verdict stales the
	// artifact, the declared route returns the instance to repair, and only
	// the accepted delivery at the route target — the producer every
	// unhealthy route of the definition names — re-produces it. Every
	// checkpoint compares the folded ArtifactStale bit with the abstract
	// successor's, so a model that clears staleness anywhere else, or a
	// fold that classifies a non-producing completion as production,
	// fails here.
	if err := reviewGateRecordDelivery(t, s, workID, acceptor); err != nil {
		t.Fatalf("record delivery at the gate: %v", err)
	}
	model = admissionSuccessor(def, model, "record_delivery")
	conformanceCheckpoint(t, s, workID, def, model, "delivery recorded at the gate")

	reviewer := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/conformance-reviewer", SessionRef: "session/" + workID + "-reviewer", ActorClass: ActorAgent}
	badVerdict := json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:return-route","verdict_kind":"outcome_mismatch","evaluation_evidence":["evidence:return-route-verification"],"incomparable_with_approved":true}`)
	if err := runVerdictActionAs(t, s, workID, "record_verdict", badVerdict, 0, reviewer); err != nil {
		t.Fatalf("record the unhealthy verdict: %v", err)
	}
	for _, successor := range admissionSuccessors(def, model, "record_verdict") {
		if successor.verdict == "bad" {
			model = successor
		}
	}
	if !model.artifactStale {
		t.Fatal("the model's bad-verdict successor did not stale the artifact")
	}
	conformanceCheckpoint(t, s, workID, def, model, "unhealthy verdict recorded")

	correction := json.RawMessage(`{"diagnosis":"the verification verdict is not healthy","strategy":"return to the producer step and re-produce the artifact","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:return-route-verification"]}`)
	if err := runIssue933OperatorAction(t, s, workID, "request_correction", correction, fixture.owner, fixture.operator); err != nil {
		t.Fatalf("request correction through the declared route: %v", err)
	}
	model = admissionSuccessor(def, model, "request_correction")
	if model.step != "repair" || !model.artifactStale {
		t.Fatalf("correction left model step %q stale %v, want repair still stale", model.step, model.artifactStale)
	}
	conformanceArtifactCheckpoint(t, s, workID, def, model, "returned producer remains stale")
	// Fence and hold state compare after the fresh start. The artifact
	// snapshot above also checks the historical stale bit at the return.

	// Re-production at the route target: the fenced start, the dispatch,
	// and the accepted delivery the fold counts as production.
	retryEpoch := reviewGateStartStep(t, s, workID, "repair", "start_repair", fixture.owner)
	model = admissionSuccessor(def, model, "start_repair")
	conformanceCheckpoint(t, s, workID, def, model, "producer restarted")
	reviewGateRunAttempt(t, s, workID, "attempt:"+workID+":repair-2", "repair", retryEpoch, reviewGateLane(t, "implementation"), ownerRef, at)
	model = admissionSuccessor(def, model, "dispatch_worker")
	conformanceCheckpoint(t, s, workID, def, model, "producer redispatched")
	if err := reviewGateAcceptResult(t, s, workID, "attempt:"+workID+":repair-2", retryEpoch, acceptor); err != nil {
		t.Fatalf("accept the fresh production: %v", err)
	}
	model = admissionSuccessor(def, model, "accept_worker_result")
	if model.artifactStale {
		t.Fatal("the model kept the artifact stale past fresh production at the route target")
	}
	conformanceArtifactCheckpoint(t, s, workID, def, model, "accepted fresh production")
	// Acceptance refreshes the artifact before refine's fresh fence. The
	// following full-state checkpoint also compares that fence and hold.
	refineEpoch := reviewGateStartStep(t, s, workID, "refine", "start_refine", fixture.owner)
	model = admissionSuccessor(def, model, "start_refine")
	conformanceCheckpoint(t, s, workID, def, model, "refine restarted")
	if refineEpoch == 0 {
		t.Fatal("refine restart returned no epoch")
	}

	// The walk back to the evaluator re-reads the same artifact fresh: the
	// fold's production frontier is the accepted delivery at repair, which
	// postdates the unhealthy verdict. The second refine epoch demands its
	// own green verification before its delivery.
	refineProofSeedGreenRun(t, s, workID, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee1")
	model.proof = true
	conformanceCheckpoint(t, s, workID, def, model, "second-epoch verification")
	if err := reviewGateRecordDelivery(t, s, workID, acceptor); err != nil {
		t.Fatalf("record delivery out of refine: %v", err)
	}
	model = admissionSuccessor(def, model, "record_delivery")
	conformanceCheckpoint(t, s, workID, def, model, "refine delivery recorded")
	if err := reviewGateRecordDelivery(t, s, workID, acceptor); err != nil {
		t.Fatalf("record delivery at the gate again: %v", err)
	}
	model = admissionSuccessor(def, model, "record_delivery")
	conformanceCheckpoint(t, s, workID, def, model, "back at verify, artifact fresh")
}

// TestAdmissionConformanceLocalJobAcceptHolds replays the CD-0205 local
// acceptance on the current break-fix definition through the real store: a
// recorded job, its job-bound dispatch and report, and the plain accept that
// satisfies the job. The loader's fold must equal the model's holding
// successor at every checkpoint, and the held state must still exit through
// the real delivery admission the step declares: record_delivery refuses
// without qualifying integration evidence and admits behind it.
func TestAdmissionConformanceLocalJobAcceptHolds(t *testing.T) {
	const workID = "admission-conformance-local-job"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	def := mustBuiltinDefinition(t, "workflow.break_fix").Definition
	if !admissionLocalJobAcceptStep(def, "repair") {
		t.Fatalf("%s v%d repair does not carry the worker-job lifecycle", def.Ref, def.Version)
	}
	acceptor := reviewGateAcceptor(workID)

	model := admissionModelState{step: "repair", debt: ReviewDebtNone, contracts: 1}
	conformanceCheckpoint(t, s, workID, def, model, "seeded at repair")
	reviewGateStartStep(t, s, workID, "repair", "start_repair", fixture.owner)
	model = admissionSuccessor(def, model, "start_repair")
	conformanceCheckpoint(t, s, workID, def, model, "after start_repair")

	job := &WorkerJobBinding{JobID: "job:conformance-repair", Revision: 1}
	recordWorkerJobRevisionForTest(t, s, workID, fixture.owner, job)
	model = admissionSuccessor(def, model, "record_worker_job")
	// The conformance lift compares the folded delivery state verbatim, so
	// the model carries the recorded revision's real deterministic keys.
	views, err := s.WorkerJobRevisions(context.Background(), workID)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 {
		t.Fatalf("recorded revisions = %d, want the one required revision", len(views))
	}
	model.jobsUnsatisfied = workerJobKey(views[0].Binding.JobID, views[0].Binding.Revision)
	model.jobsScope = views[0].ProjectScope
	conformanceCheckpoint(t, s, workID, def, model, "after record_worker_job")

	attempt := "attempt:" + workID + ":repair"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt, nil, fixture.owner, 0, "conformance-repair", job)
	if err := completeJobBoundAttemptForTest(s, workID, attempt, BuiltinLaneDefinitions()[0], job); err != nil {
		t.Fatal(err)
	}
	model = admissionSuccessor(def, model, "dispatch_worker")
	conformanceCheckpoint(t, s, workID, def, model, "after job-bound repair completion")

	if err := runVerdictActionAs(t, s, workID, "accept_worker_result", mustJSONValue(map[string]any{"attempt_id": attempt, "attempt_epoch": latestStepStartEpoch(t, s, workID, "repair")}), 0, acceptor); err != nil {
		t.Fatalf("local accept: %v", err)
	}
	accepts := admissionSuccessors(def, model, "accept_worker_result")
	var heldSatisfied *admissionModelState
	for i, successor := range accepts {
		if successor.step == "repair" && successor.jobs == "satisfied" && successor.jobsUnsatisfied == "" {
			heldSatisfied = &accepts[i]
		}
	}
	if heldSatisfied == nil {
		t.Fatalf("model accept successors = %v, want the held satisfied local acceptance", accepts)
	}
	if advanced := accepts[len(accepts)-1]; advanced.step == "repair" {
		t.Fatalf("model accept successors = %v, want the unbound advance beside the holds", accepts)
	}
	model = *heldSatisfied
	conformanceCheckpoint(t, s, workID, def, model, "after local job accept")

	// Re-recording the job onto the satisfied population reopens it
	// (CD-0205 D1/D2): the fold reads each job's latest revision only, so
	// revision 2 is required and unsatisfied, and the delivery facet keeps
	// no integration from the earlier acceptance epoch. The model successor
	// must hold exactly the fold the real engine derives.
	reRecorded := &WorkerJobBinding{JobID: job.JobID, Revision: 2}
	recordWorkerJobRevisionForTest(t, s, workID, fixture.owner, reRecorded)
	model = admissionSuccessor(def, model, "record_worker_job")
	views, err = s.WorkerJobRevisions(context.Background(), workID)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 || views[1].Binding.Revision != 2 || views[1].State != "recorded" || views[0].State != "satisfied" {
		t.Fatalf("re-recorded revisions = %#v, want revision 1 satisfied beside revision 2 required and unsatisfied", views)
	}
	model.jobsUnsatisfied = workerJobKey(views[1].Binding.JobID, views[1].Binding.Revision)
	model.jobsScope = views[1].ProjectScope
	conformanceCheckpoint(t, s, workID, def, model, "after re-recorded worker job")

	// The reopened revision dispatches, completes, and is accepted locally
	// again: the held state returns to the satisfied population with no
	// integration coverage, exactly as the first pass held.
	reAttempt := "attempt:" + workID + ":reopened"
	dispatchJobBoundAttempt(t, s, workID, "repair", reAttempt, nil, fixture.owner, 0, "conformance-reopened", reRecorded)
	if err := completeJobBoundAttemptForTest(s, workID, reAttempt, BuiltinLaneDefinitions()[0], reRecorded); err != nil {
		t.Fatal(err)
	}
	model = admissionSuccessor(def, model, "dispatch_worker")
	conformanceCheckpoint(t, s, workID, def, model, "after reopened job-bound completion")
	if err := runVerdictActionAs(t, s, workID, "accept_worker_result", mustJSONValue(map[string]any{"attempt_id": reAttempt, "attempt_epoch": latestStepStartEpoch(t, s, workID, "repair")}), 0, acceptor); err != nil {
		t.Fatalf("local accept of the reopened revision: %v", err)
	}
	accepts = admissionSuccessors(def, model, "accept_worker_result")
	heldSatisfied = nil
	for i, successor := range accepts {
		if successor.step == "repair" && successor.jobs == "satisfied" && successor.jobsUnsatisfied == "" {
			heldSatisfied = &accepts[i]
		}
	}
	if heldSatisfied == nil {
		t.Fatalf("model accept successors = %v, want the held satisfied local acceptance of the reopened revision", accepts)
	}
	model = *heldSatisfied
	conformanceCheckpoint(t, s, workID, def, model, "after local accept of the reopened revision")

	// The held state's exit is the real delivery admission, not the abstract
	// successor: record_delivery refuses behind the satisfied population
	// without qualifying integration evidence (CD-0205 D3).
	err = runVerdictActionAs(t, s, workID, "record_delivery", mustJSONValue(map[string]any{"delivery_artifact": "artifact:conformance-held", "delivery_state": "asserted"}), 0, acceptor)
	if err == nil {
		t.Fatal("record_delivery from the held state admitted without integration evidence")
	}
	if !strings.Contains(err.Error(), "verification evidence") {
		t.Fatalf("held record_delivery refusal = %v, want the integration remedy", err)
	}
	// The qualifying integration run integrates the required Project after
	// the recorded acceptance and result population, and the real exit
	// advances the step.
	workerJobIntegrationGreenRun(t, s, workID, "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")
	model.integration = true
	conformanceCheckpoint(t, s, workID, def, model, "after integration evidence")
	before := issue1013Pin(t, s, workID).Step
	if err := runVerdictActionAs(t, s, workID, "record_delivery", mustJSONValue(map[string]any{"delivery_artifact": "artifact:conformance-held", "delivery_state": "asserted"}), 0, acceptor); err != nil {
		t.Fatalf("record_delivery behind integration evidence: %v", err)
	}
	if after := issue1013Pin(t, s, workID).Step; after == before || after == "" {
		t.Fatalf("record_delivery behind integration evidence left the step at %q", after)
	}
}

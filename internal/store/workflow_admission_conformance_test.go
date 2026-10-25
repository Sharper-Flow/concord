package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Identity is compared exactly by the typed Review conformance test. This view
// compares every semantic Review dimension carried by the global model.
type admissionReviewView struct {
	Present, Candidate, CandidateMatches, Dependencies, Current, Progressed bool
	Blocked, ContinueConsumed, PublicationOutstanding                       bool
	Disposition                                                             string
	FirstGroups, ContinuedGroups, CorrectiveGroups                          int
}

func admissionReviewViewOf(state *WorkflowReviewState) admissionReviewView {
	if state == nil {
		return admissionReviewView{}
	}
	view := admissionReviewView{Present: true, Candidate: state.Candidate != nil, Dependencies: state.Dependencies != nil,
		Current: state.DependenciesCurrent, Progressed: state.ExecutionProgressed, Blocked: state.blocker() != "",
		ContinueConsumed: state.ContinueConsumed, PublicationOutstanding: state.PublicationOutstanding}
	if state.Accepted != nil {
		view.Disposition = state.Accepted.Report.Disposition
		view.CandidateMatches = state.Candidate != nil && *state.Candidate == state.Accepted.Report.OutputCandidate
	}
	for _, group := range state.Groups {
		switch {
		case group.CorrectionRef != "":
			view.CorrectiveGroups++
		case group.Permission == "first_entry":
			view.FirstGroups++
		case group.Permission == "continue":
			view.ContinuedGroups++
		}
	}
	return view
}

type admissionConformanceView struct {
	Step, Lifecycle, InstanceState                                               string
	RefinementWorkflow                                                           bool
	ActiveContracts                                                              int64
	LateCriterionAssociation, LawPinStale, DesignStale, ArtifactStale            bool
	Review                                                                       admissionReviewView
	WorkerFailureRecovery, CorrectionRecovery, CorrectionRequestRecovery         bool
	UnconsumedUnboundRejection, DispatchHold, PendingOperatorDecision            bool
	AcceptanceDeliverablesMissing                                                bool
	ApprovalPolicy, ConfirmationAuthority                                        string
	ConfirmationFactFailure, CompleteStepCorrection, ContractCorrectionAvailable bool
	DeliveryStarted, DeliveryProofRequired, DeliveryProofReady                   bool
	DeliveryFailure, DeliveryProofFailure, DeliveryJobsRecorded                  bool
	DeliveryJobsUnsatisfiedKeys, DeliveryJobsScopeKeys                           string
	DeliveryJobsIntegrated                                                       bool
	DeliveryJobsUndischargedSplit                                                string
	MandatePresent, MandateMissing                                               bool
	MandateBindingStep                                                           string
	MandateAddedByContract                                                       bool
	MandateCorrectionAction                                                      string
	MandateFailure                                                               bool
}

func admissionConformanceViewOf(state WorkflowAdmissionState) admissionConformanceView {
	view := admissionConformanceView{
		Step: state.Step, Lifecycle: state.Lifecycle, InstanceState: state.InstanceState,
		RefinementWorkflow: state.RefinementWorkflow,
		ActiveContracts:    state.ActiveContracts, LawPinStale: state.LawPinStale, DesignStale: state.DesignStale,
		LateCriterionAssociation: state.LateCriterionAssociation, ArtifactStale: state.ArtifactStale,
		Review:                admissionReviewViewOf(state.Review),
		WorkerFailureRecovery: state.WorkerFailureRecovery,
		CorrectionRecovery:    state.CorrectionRecovery, CorrectionRequestRecovery: state.CorrectionRequestRecovery,
		UnconsumedUnboundRejection: state.UnconsumedUnboundRejection,
		DispatchHold:               state.DispatchHold, PendingOperatorDecision: state.PendingOperatorDecision,
		AcceptanceDeliverablesMissing: state.AcceptanceDeliverablesMissing != nil,
		ApprovalPolicy:                state.ApprovalPolicy, ConfirmationAuthority: string(state.ConfirmationAuthority),
		ConfirmationFactFailure: state.ConfirmationFactFailure != nil,
		CompleteStepCorrection:  state.CompleteStepCorrection, ContractCorrectionAvailable: state.ContractCorrectionAvailable,
		DeliveryStarted: state.Delivery.Started, DeliveryProofRequired: state.Delivery.ProofRequired,
		DeliveryFailure: state.Delivery.Failure != nil, MandateFailure: state.Mandate.Failure != nil,
		DeliveryJobsRecorded: state.Delivery.JobsRecorded, DeliveryJobsUnsatisfiedKeys: state.Delivery.JobsUnsatisfiedKeys,
		DeliveryJobsScopeKeys: state.Delivery.JobsScopeKeys, DeliveryJobsIntegrated: state.Delivery.JobsIntegrated,
		MandatePresent: state.Mandate.Present, MandateMissing: state.Mandate.LawID != "", MandateBindingStep: state.Mandate.BindingStep,
		MandateAddedByContract: state.Mandate.AddedByContract, MandateCorrectionAction: state.Mandate.CorrectionAction,
	}
	if state.Delivery.ProofRequired {
		view.DeliveryProofReady, view.DeliveryProofFailure = state.Delivery.ProofReady, state.Delivery.ProofFailure != nil
	}
	return view
}

func conformanceCheckpoint(t *testing.T, s *Store, registry DefinitionRegistry, workID string, definition WorkflowDefinition, want admissionModelState, label string) {
	t.Helper()
	loaded := conformanceFold(t, s, registry, workID, definition, label)
	got, model := admissionConformanceViewOf(loaded), admissionConformanceViewOf(admissionWorkflowState(definition, want))
	if got != model {
		t.Fatalf("%s: loaded fold %+v != abstract successor %s lifted to %+v", label, got, want, model)
	}
}

func conformanceFold(t *testing.T, s *Store, registry DefinitionRegistry, workID string, definition WorkflowDefinition, label string) WorkflowAdmissionState {
	t.Helper()
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("%s: begin: %v", label, err)
	}
	defer func() { _ = tx.Rollback() }()
	var step string
	if err := tx.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	loaded, _, _, err := loadWorkflowAdmissionStateTxForRegistry(ctx, tx, workID, definition, step, "workflow_admission_conformance_test", registry)
	if err != nil {
		t.Fatalf("%s: load: %v", label, err)
	}
	return loaded
}

func TestAdmissionConformanceLocalJobAcceptHolds(t *testing.T) {
	f := seedWorkContextFixture(t, "admission-conformance-local-job")
	t.Cleanup(func() { _ = f.store.Close() })
	repo, _ := reviewSubjectClaim(t, f.store, f.workID)
	var branch string
	if err := f.store.db.QueryRow(`SELECT branch FROM worktree_entries WHERE set_id=? AND state='active'`, WorktreeSetID(f.workID)).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, repo, "branch", "-m", branch)
	journey := &candidateJourney{fixture: f, s: f.store, workID: f.workID, repo: repo}
	s, workID := f.store, f.workID
	definition := pinnedWorkflowDefinitionForTest(t, s, workID)
	model := admissionModelState{step: "repair", contracts: 1, engineering: true}
	conformanceCheckpoint(t, s, BuiltinWorkflowRegistry(), workID, definition, model, "before worker job")
	job := WorkerJobBinding{JobID: "job:conformance"}
	for round := 1; round <= 2; round++ {
		attempt := "attempt:conformance:" + itoa(round)
		conformanceImplementComplete(t, journey, &job, attempt)
		model = admissionSuccessors(definition, model, "record_worker_job")[0]
		model.jobsScope = "project"
		model.jobsUnsatisfied = workerJobKey(job.JobID, job.Revision)
		found := false
		for _, next := range admissionSuccessors(definition, model, "dispatch_worker") {
			if next.attempt == "completed" && next.attemptProduces {
				model, found = next, true
				break
			}
		}
		if !found {
			t.Fatal("model has no admitted producing completion")
		}
		// The first intake's observed producer proof still covers the
		// integrated candidate while the next changed result is pending.
		model.integration = round > 1
		conformanceCheckpoint(t, s, BuiltinWorkflowRegistry(), workID, definition, model, "job-bound terminal before local acceptance")
		window := reviewSubjectWindow(t, s, workID, attempt)
		if err := journey.action(t, "accept_worker_result", map[string]any{"attempt_id": attempt, "attempt_epoch": window.AttemptEpoch}, f.owner); err != nil {
			t.Fatal(err)
		}
		found = false
		for _, next := range admissionSuccessors(definition, model, "accept_worker_result") {
			if next.step == "repair" && next.jobs == "satisfied" && next.jobsUnsatisfied == "" {
				model, found = next, true
				break
			}
		}
		if !found {
			t.Fatal("model has no held, satisfied local intake")
		}
		model.integration = false
		conformanceCheckpoint(t, s, BuiltinWorkflowRegistry(), workID, definition, model, "local acceptance holds with no Review authority")
		journey.observe(t, attempt)
	}
	before := readWorkVersion(t, s, workID)
	err := journey.action(t, "record_delivery", map[string]any{"delivery_artifact": "artifact:conformance", "delivery_state": "asserted"}, f.owner)
	if !hasFailureKind(err, KindMissingEvidence) || readWorkVersion(t, s, workID) != before || currentStep(t, s, workID) != "repair" {
		t.Fatalf("delivery without accepted Review = %v; local intake cannot authorize exit", err)
	}
}

// The current intake witness produces a real changed delivery after dispatch.
// No recorded-history accept or fixed-input no-op stands in for Main's action.
func conformanceImplementComplete(t *testing.T, j *candidateJourney, job *WorkerJobBinding, attempt string) {
	t.Helper()
	f := j.fixture
	recordWorkerJobRevisionForTest(t, j.s, j.workID, f.owner, job)
	view, err := readWorkContextView(t.Context(), j.s.db, j.workID)
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatchWorkContextAttempt(t, f, workContextDispatchPacket(t, f, attempt, *job, view)); err != nil {
		t.Fatal(err)
	}
	lane := laneForID(t)
	if err := ApplyOperation(t.Context(), j.s, Operation{Events: []Event{implementDispatchEvidenceEvent(j.workID, attempt, lane, *job)}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(j.repo, "internal", "store", job.JobID, "value.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package fixture\n// "+attempt+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	j.commit(t, "worker@example.invalid", "produce bounded implementation")
	eventID := attempt + ":completed"
	prepared, err := PrepareWorkerTerminalDelivery(t.Context(), j.s, PrepareWorkerTerminalDeliveryRequest{WorkID: j.workID, AttemptID: attempt, EventID: eventID})
	if err != nil {
		t.Fatal(err)
	}
	event := Event{EventID: eventID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: j.workID, Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion(WorkerCompleted), Payload: mustJSONValue(WorkerCompletedPayload{
		AttemptID: attempt, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion,
		EvidenceOrigin: WorkerEvidenceReported, Evidence: laneCoveringEvidence(lane), WorkerJob: job,
	})}
	workerTerminalEventThroughSeamCtx(t, WithWorkerTerminalPreparation(t.Context(), prepared), j.s, event)
}

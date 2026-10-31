package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Exact identities are compared by the typed Review conformance test.
type admissionReviewView struct {
	Present, Candidate, CandidateMatches, Dependencies, Current bool
	Blocked, PublicationOutstanding, Crucial                    bool
	Disposition                                                 string
}

func admissionReviewViewOf(state *WorkflowReviewState) admissionReviewView {
	if state == nil {
		return admissionReviewView{}
	}
	view := admissionReviewView{Present: true, Candidate: state.Candidate != nil, Dependencies: state.Dependencies != nil,
		Current: state.DependenciesCurrent, Blocked: state.blocker() != "", PublicationOutstanding: state.PublicationOutstanding, Crucial: state.Crucial}
	if state.Accepted != nil {
		view.Disposition = state.Accepted.Report.Disposition
		view.CandidateMatches = state.Candidate != nil && *state.Candidate == state.Accepted.Report.OutputCandidate
	}
	return view
}

type admissionConformanceView struct {
	Step, Lifecycle, InstanceState                                    string
	RefinementWorkflow                                                bool
	ActiveContracts                                                   int64
	LateCriterionAssociation, LawPinStale, DesignStale, ArtifactStale bool
	Review                                                            admissionReviewView
	CorrectionRequestRecovery                                         bool
	AcceptanceDeliverablesMissing                                     bool
	CompleteStepCorrection, ContractCorrectionAvailable               bool
	DeliveryStarted, DeliveryProofRequired, DeliveryProofReady        bool
	DeliveryFailure, DeliveryProofFailure                             bool
	MandatePresent, MandateMissing                                    bool
	MandateBindingStep                                                string
	MandateAddedByContract                                            bool
	MandateCorrectionAction                                           string
	MandateFailure                                                    bool
}

func admissionConformanceViewOf(state WorkflowAdmissionState) admissionConformanceView {
	view := admissionConformanceView{
		Step: state.Step, Lifecycle: state.Lifecycle, InstanceState: state.InstanceState,
		RefinementWorkflow: state.RefinementWorkflow, ActiveContracts: state.ActiveContracts,
		LawPinStale: state.LawPinStale, DesignStale: state.DesignStale,
		LateCriterionAssociation: state.LateCriterionAssociation, ArtifactStale: state.ArtifactStale,
		Review: admissionReviewViewOf(state.Review), CorrectionRequestRecovery: state.CorrectionRequestRecovery,
		AcceptanceDeliverablesMissing: state.AcceptanceDeliverablesMissing != nil,
		CompleteStepCorrection:        state.CompleteStepCorrection,
		ContractCorrectionAvailable:   state.ContractCorrectionAvailable,
		DeliveryStarted:               state.Delivery.Started, DeliveryProofRequired: state.Delivery.ProofRequired,
		DeliveryFailure: state.Delivery.Failure != nil, MandateFailure: state.Mandate.Failure != nil,
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
	defer tx.Rollback()
	var step string
	if err := tx.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := loadWorkflowAdmissionStateTxForRegistry(ctx, tx, workID, definition, step, "workflow_admission_conformance_test", registry)
	if err != nil {
		t.Fatalf("%s: load: %v", label, err)
	}
	return loaded
}

func TestAdmissionConformanceQualifiedEditsNeedReview(t *testing.T) {
	f := seedWorkContextFixture(t, "admission-conformance-edits")
	t.Cleanup(func() { f.store.Close() })
	repo, _ := reviewSubjectClaim(t, f.store, f.workID)
	j := &candidateJourney{fixture: f, s: f.store, workID: f.workID, repo: repo}
	definition := pinnedWorkflowDefinitionForTest(t, f.store, f.workID)
	for round := 1; round <= 2; round++ {
		conformanceImplementChange(t, j, "change-"+itoa(round))
		loaded := conformanceFold(t, f.store, BuiltinWorkflowRegistry(), f.workID, definition, "qualified ordinary edit")
		if loaded.Step != "repair" || loaded.Review == nil || loaded.Review.Accepted != nil {
			t.Fatalf("ordinary edit advanced or manufactured Review: %+v", loaded)
		}
		before := readWorkVersion(t, f.store, f.workID)
		err := j.action(t, "record_delivery", map[string]any{"delivery_artifact": "artifact:conformance", "delivery_state": "asserted"}, f.owner)
		if !hasFailureKind(err, KindMissingEvidence) || readWorkVersion(t, f.store, f.workID) != before || currentStep(t, f.store, f.workID) != "repair" {
			t.Fatalf("delivery without recorded Review = %v; ordinary edits cannot authorize exit", err)
		}
	}
}

func conformanceImplementChange(t *testing.T, j *candidateJourney, label string) {
	t.Helper()
	path := filepath.Join(j.repo, "internal", "store", "value.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package fixture\nvar Value = "+itoa(len(label))+" // "+label+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	j.commit(t, "author@example.invalid", "produce bounded implementation")
	j.observe(t, label)
}

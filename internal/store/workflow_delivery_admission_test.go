package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func loadedAdmissionForTest(t *testing.T, s *Store, workID string, pin WorkPin) (WorkflowDefinition, WorkflowAdmissionState) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	entry, err := VerifyWorkflowInstanceDefinitionTx(ctx, tx, BuiltinWorkflowRegistry(), workID)
	if err != nil {
		t.Fatal(err)
	}
	state, _, err := loadWorkflowAdmissionStateTxForRegistry(ctx, tx, workID, entry.Definition, pin.Step, "admission_test", BuiltinWorkflowRegistry())
	if err != nil {
		t.Fatal(err)
	}
	return entry.Definition, state
}

func TestDeliveryPrerequisiteAdmissionAgreement(t *testing.T) {
	for _, stage := range []string{"before-start", "missing-epoch-proof", "green-proof"} {
		t.Run(stage, func(t *testing.T) {
			const workID = "delivery-admission-agreement"
			// Before the start the reviewed candidate parks at refine with no
			// refine action recorded; otherwise the refine start is fenced.
			step := "refine"
			if stage == "before-start" {
				step = "execution"
			}
			var c acceptDeliveryCandidate
			if stage == "missing-epoch-proof" {
				c = acceptDeliveryUnobservedCandidateAt(t, workID, "workflow.implementation")
			} else {
				c = acceptDeliveryCandidateAt(t, workID, "workflow.implementation", step, false)
			}
			if stage == "before-start" {
				setWorkflowStepForOperatorVerdictTest(t, c.f.store, workID, "refine")
			}
			if stage != "missing-epoch-proof" {
				acceptDeliveryBindFresh(t, c, "agreement")
			}
			fixture := struct{ store *Store }{c.f.store}
			pin := issue1013Pin(t, fixture.store, workID)
			definition, state := loadedAdmissionForTest(t, fixture.store, workID, pin)
			model := admissionModelState{
				step: pin.Step, contracts: 1,
				review:  admissionModelReview{accepted: "ready_for_final_proof", candidate: true, current: true},
				started: stage != "before-start", proof: stage == "green-proof",
			}
			want := admissionConformanceViewOf(admissionWorkflowState(definition, model))
			// The unobserved fixture has no criterion assessment or association.
			want.AcceptanceDeliverablesMissing = stage == "missing-epoch-proof"
			if got := admissionConformanceViewOf(state); got != want {
				t.Fatalf("%s: loaded admission %+v != expected %+v", stage, got, want)
			}
			decision := workflowAdmit(definition, state, "record_delivery")
			preflightErr := WorkflowActionPreflightWithRegistry(context.Background(), fixture.store, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
				WorkID: workID, ExpectedVersion: pin.Version, ActionID: "record_delivery", Actor: reviewGateAcceptor(workID),
				Payload: json.RawMessage(`{"delivery_artifact":"artifact:agreement","delivery_state":"asserted"}`),
			})
			executionErr := refineProofDelivery(t, fixture.store, workID, nil)
			advertised := workPinContainsAction(pin.NextValidIntents, "record_delivery")
			if stage == "green-proof" {
				if !decision.Admitted || !advertised || preflightErr != nil || executionErr != nil {
					t.Fatalf("green proof: admitted=%v, advertised=%v, preflight=%v, execution=%v", decision.Admitted, advertised, preflightErr, executionErr)
				}
				return
			}
			if decision.Admitted || advertised || preflightErr == nil || executionErr == nil {
				t.Fatalf("missing prerequisite: admitted=%v, advertised=%v, preflight=%v, execution=%v", decision.Admitted, advertised, preflightErr, executionErr)
			}
			var preflightFailure, executionFailure *Failure
			if !failureAs(preflightErr, &preflightFailure) || !failureAs(executionErr, &executionFailure) ||
				decision.Failure.Kind != preflightFailure.Kind || decision.Failure.Kind != executionFailure.Kind ||
				!strings.Contains(executionFailure.Detail, decision.Failure.Detail) || preflightFailure.Detail != decision.Failure.Detail {
				t.Fatalf("refusals differ: admission=%v, preflight=%v, execution=%v", decision.Failure, preflightErr, executionErr)
			}
		})
	}
}

func TestDeliveryProofPreservesBindingOrderAndTooling(t *testing.T) {
	malformed := newFailure(KindInvariantViolation, "workflow_action", "malformed verify operation", false, "rebuild projections")
	good := workflowVerificationRun{Command: []string{"go", "vet", "./..."}}
	other := workflowVerificationRun{Command: []string{"go", "test", "./..."}}
	bad := workflowVerificationRun{Disqualifier: "the bound verify lease did not record exit code 0"}
	declared := refineProofTooling(ProjectDeclaredTool{ID: "go-vet", Invocation: "go vet ./..."})
	for _, test := range []struct {
		name    string
		runs    []workflowVerificationRun
		tooling *ProjectToolingManifest
		ready   bool
		failure bool
	}{
		{"green-before-malformed", []workflowVerificationRun{good, {Failure: malformed}}, nil, true, false},
		{"malformed-before-green", []workflowVerificationRun{{Failure: malformed}, good}, nil, false, true},
		{"non-green-before-green", []workflowVerificationRun{bad, good}, nil, true, false},
		{"later-declared-command", []workflowVerificationRun{other, good}, declared, true, false},
		{"undeclared-before-malformed", []workflowVerificationRun{other, {Failure: malformed}}, declared, false, true},
		{"only-undeclared-command", []workflowVerificationRun{other}, declared, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ready, why, err := workflowVerificationProof(test.runs, test.tooling)
			if ready != test.ready || (err != nil) != test.failure || (!ready && err == nil && why == "") {
				t.Fatalf("proof = %v, %q, %v", ready, why, err)
			}
		})
	}
}

func TestMalformedDeliveryProofDoesNotHideRecovery(t *testing.T) {
	definition := mustBuiltinDefinition(t, "workflow.implementation").Definition
	state := WorkflowAdmissionState{Step: "refine", ActiveContracts: 1, Delivery: workflowDeliveryAdmission{
		Started: true, ProofRequired: true,
		ProofFailure: newFailure(KindInvariantViolation, "workflow_action", "malformed verify operation", false, "rebuild projections"),
	}}
	if decision := workflowAdmit(definition, state, "record_delivery"); decision.Admitted || decision.Failure.Kind != KindInvariantViolation {
		t.Fatalf("malformed proof admission = %+v", decision)
	}
	for _, action := range []string{"bind_evidence", "checkpoint_context", "checkpoint_refine"} {
		if decision := workflowAdmit(definition, state, action); !decision.Admitted {
			t.Fatalf("malformed proof hides %s: %+v", action, decision)
		}
	}
}

func TestDeliveryGuardConsumesTheSharedMandateDecision(t *testing.T) {
	definition := WorkflowDefinition{
		ActionDefinitions: []WorkflowActionDefinition{{ID: "record_delivery", ExecutionMode: ActionAdvance}},
		StepGraph:         WorkflowStepGraph{Steps: []WorkflowStep{{ID: "execution", Actions: []string{"record_delivery", "bind_evidence"}}}},
	}
	state := WorkflowAdmissionState{
		Step: "execution", ActiveContracts: 1, Delivery: workflowDeliveryAdmission{Started: true},
		Mandate: workflowMandateAdmission{Present: true, LawID: "spec:one", BindingStep: "execution"},
	}
	decision := workflowAdmit(definition, state, "record_delivery")
	if decision.Admitted || decision.Failure == nil {
		t.Fatalf("unbound mandate admission = %+v", decision)
	}
	g := workflowActionGuardContext{
		request:        WorkflowActionExecutionRequest{ActionID: "record_delivery"},
		admissionState: &state, admissionDecision: &decision,
	}
	if err := guardDeliveryAdmission(&g); err != decision.Failure {
		t.Fatalf("delivery guard = %v, want the shared mandate refusal %v", err, decision.Failure)
	}
}

func TestDeliveryGuardKeepsProofEvidenceOutsideAbstractState(t *testing.T) {
	const workID = "delivery-proof-snapshot"
	c := acceptDeliveryCandidateAt(t, workID, "workflow.implementation", "refine", false)
	acceptDeliveryBindFresh(t, c, "snapshot")
	fixture := struct{ store *Store }{c.f.store}
	ctx := context.Background()
	tx, err := fixture.store.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	entry, err := VerifyWorkflowInstanceDefinitionTx(ctx, tx, BuiltinWorkflowRegistry(), workID)
	if err != nil {
		t.Fatal(err)
	}
	state, runs, err := loadWorkflowAdmissionStateTxForRegistry(ctx, tx, workID, entry.Definition, "refine", "workflow_action", BuiltinWorkflowRegistry())
	if err != nil {
		t.Fatal(err)
	}
	decision := workflowAdmit(entry.Definition, state, "record_delivery")
	// Binding, claim, early verification, and Review qualification cover the candidate.
	if !state.Delivery.ProofReady || len(runs) != 4 || !decision.Admitted {
		t.Fatalf("proof snapshot: state=%+v, runs=%+v, decision=%+v", state.Delivery, runs, decision)
	}
	for _, cached := range []bool{false, true} {
		g := workflowActionGuardContext{
			ctx: ctx, tx: tx, entry: entry, currentStep: "refine",
			request: WorkflowActionExecutionRequest{WorkID: workID, ActionID: "record_delivery"},
		}
		if cached {
			g.admissionState, g.admissionDecision, g.deliveryProofRuns = &state, &decision, runs
		}
		g.request.ProjectTooling = refineProofTooling(ProjectDeclaredTool{ID: "head", Invocation: "git rev-parse --verify HEAD^{commit}"})
		if err := guardDeliveryAdmission(&g); err != nil {
			t.Fatalf("cached=%v: declared proof refused: %v", cached, err)
		}
		g.request.ProjectTooling = refineProofTooling(ProjectDeclaredTool{ID: "go-test", Invocation: "go test ./..."})
		var failure *Failure
		if err := guardDeliveryAdmission(&g); !failureAs(err, &failure) || failure.Kind != KindMissingEvidence || !strings.Contains(failure.Detail, "not a tool the Project declares") {
			t.Fatalf("cached=%v: undeclared proof = %v", cached, err)
		}
		if g.admissionState.Delivery != state.Delivery {
			t.Fatalf("cached=%v: tooling check changed the abstract proof classification", cached)
		}
	}
}

func TestDeliveryProofKeepsDuplicateContractRecoveryReachable(t *testing.T) {
	const workID = "delivery-proof-duplicate-contract"
	c := acceptDeliveryCandidateAt(t, workID, "workflow.implementation", "refine", false)
	duplicateActiveWorkflowContract(context.Background(), t, c.f.store, workID)
	_, action, err := WorkflowActionDefinitionFor(context.Background(), c.f.store, BuiltinWorkflowRegistry(), workID, "supersede_contract")
	if err != nil || action.ID != "supersede_contract" {
		t.Fatalf("supersede_contract discovery at refine with duplicate contracts = %+v, %v", action, err)
	}
}

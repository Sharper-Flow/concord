package store

import (
	"context"
	"slices"
	"testing"
)

func TestConfirmationCorrectionDoesNotDependOnStepKind(t *testing.T) {
	definition := WorkflowDefinition{StepGraph: WorkflowStepGraph{
		Steps: []WorkflowStep{{ID: "confirmation", Kind: WorkflowStepInternalSQLite, Actions: []string{"confirm_premise"}}},
	}}
	if !workflowContractCorrectionCheckpoint(definition, "confirmation") {
		t.Fatal("confirmation step must admit contract correction")
	}
	if !workPinContainsAction(workPinIntents(definition, "confirmation", 1), "supersede_contract") {
		t.Fatal("confirmation step must advertise contract correction")
	}
}

func TestReviewCheckpointCorrectionAcrossKinds(t *testing.T) {
	count := 0
	for _, definition := range BuiltinWorkflowDefinitions() {
		if workflowFamilyArchived(definition.Ref) {
			continue
		}
		if workflowContractCorrectionCheckpoint(definition, "no-such-step") {
			t.Fatalf("%s: nonexistent step is a correction checkpoint", definition.Ref)
		}
		for _, step := range definition.StepGraph.Steps {
			if stepDeclaresAction(definition, step.ID, "complete") {
				continue // Complete-step admission has its own contradiction gate.
			}
			terminal := slices.Contains(definition.StepGraph.TerminalSteps, step.ID)
			expected := !terminal && slices.Contains([]string{"proposal", "design", "investigate", "findings", "execution", "repair", "refine", "acceptance", "verify", "conclude"}, step.ID)
			if got := workflowContractCorrectionCheckpoint(definition, step.ID); got != expected {
				t.Fatalf("%s step %s (%s): correction checkpoint=%v, want %v", definition.Ref, step.ID, step.Kind, got, expected)
			}
			if got := admissionContractCorrection(definition, admissionModelState{step: step.ID, contracts: 1}); got != expected {
				t.Fatalf("%s step %s (%s): model correction checkpoint=%v, want %v", definition.Ref, step.ID, step.Kind, got, expected)
			}
			if expected {
				count++
			}
		}
	}
	if count == 0 {
		t.Fatal("no registered ordinary correction checkpoint was checked")
	}
	for _, kind := range []WorkflowStepKind{WorkflowStepCrossAuthority, WorkflowStepInternalSQLite, WorkflowStepExternalEffect} {
		t.Run(string(kind), func(t *testing.T) {
			for _, action := range []string{"record_proposal", "record_design", "record_finding", "record_report", "start_execution", "checkpoint_repair", "checkpoint_refine"} {
				t.Run(action, func(t *testing.T) {
					definition := WorkflowDefinition{Ref: "workflow.correction_probe", StepGraph: WorkflowStepGraph{
						StartStep: "producing", TerminalSteps: []string{"sealed", "complete"}, Steps: []WorkflowStep{
							{ID: "producing", Kind: kind, Actions: []string{action}},
							{ID: "quiet", Kind: WorkflowStepInternalSQLite, Actions: []string{action, "record_review"}},
							{ID: "confirm", Kind: WorkflowStepInternalSQLite, Actions: []string{"confirm_premise"}},
							{ID: "review_only", Kind: kind, Actions: []string{"record_review"}},
							{ID: "plain", Kind: WorkflowStepInternalSQLite, Actions: []string{"record_note"}},
							{ID: "sealed", Kind: kind, Actions: []string{action}},
							{ID: "complete", Kind: WorkflowStepInternalSQLite, Actions: []string{"complete"}},
						},
					}}
					for _, step := range []string{"producing", "quiet", "confirm", "review_only", "plain", "sealed", "complete", "no-such-step"} {
						want := step == "producing" || step == "quiet" || step == "confirm" || step == "complete"
						if got := workflowContractCorrectionCheckpoint(definition, step); got != want {
							t.Fatalf("%s: correction checkpoint=%v, want %v", step, got, want)
						}
						if got := admissionContractCorrection(definition, admissionModelState{step: step, contracts: 1}); got != (want && step != "complete") {
							t.Fatalf("%s: model correction checkpoint=%v, want %v", step, got, want && step != "complete")
						}
					}
				})
			}
		})
	}
}

func TestReviewCheckpointCorrectionAvailability(t *testing.T) {
	for _, family := range []string{"workflow.break_fix", "workflow.implementation"} {
		t.Run(family, func(t *testing.T) {
			workID := "review-checkpoint-" + family
			j := pReturnReviewFixture(t, workID, family, "")
			definition := pinnedWorkflowDefinitionForTest(t, j.s, workID)
			step := currentStep(t, j.s, workID)
			check := func(stage string, available bool, contracts int64) {
				t.Helper()
				before := contractCorrectionEffectSnapshot(t, j.s, workID)
				got, err := workflowContractCorrectionAvailable(context.Background(), j.s.db, workID, definition, step, "test")
				if err != nil || got != available {
					t.Fatalf("%s: availability=%v err=%v, want %v", stage, got, err, available)
				}
				_, _, err = WorkflowActionDefinitionFor(t.Context(), j.s, BuiltinWorkflowRegistry(), workID, "supersede_contract")
				if (err == nil) != available {
					t.Fatalf("%s: discovery err=%v, want available=%v", stage, err, available)
				}
				pin, pinErr := ReadWorkPin(t.Context(), j.s, workID)
				if contracts == 0 {
					if !hasFailureKind(pinErr, KindInvariantViolation) {
						t.Fatalf("%s: pin error=%v, want typed invariant_violation for inactive contract projection", stage, pinErr)
					}
				} else if pinErr != nil {
					t.Fatalf("%s: read work pin: %v", stage, pinErr)
				}
				intent := false
				for _, candidate := range pin.NextValidIntents {
					if candidate.ActionID == "supersede_contract" && candidate.ReasonCode == "operator_contract_correction" {
						intent = true
					}
				}
				if pinErr == nil && intent != available {
					t.Fatalf("%s: pin correction intent=%v, want %v", stage, intent, available)
				}
				if got := admissionContractCorrection(definition, admissionModelState{step: step, contracts: contracts}); got != available {
					t.Fatalf("%s: model availability=%v, want %v", stage, got, available)
				}
				if !available {
					err := WorkflowActionPreflightWithRegistry(t.Context(), j.s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
						WorkID: workID, ExpectedVersion: readWorkVersion(t, j.s, workID), ActionID: "supersede_contract", Actor: j.fixture.owner,
					})
					if !hasFailureKind(err, KindInvariantViolation) {
						t.Fatalf("%s: preflight=%v, want typed invariant_violation for inactive contract projection", stage, err)
					}
				}
				if after := contractCorrectionEffectSnapshot(t, j.s, workID); after != before {
					t.Fatalf("%s: read-only correction surfaces changed durable state", stage)
				}
			}
			check("ordinary producer before Review", true, 1)
			acceptCurrentReviewForTest(t, j.fixture, j.repo, "correction_required")
			check("recorded corrective Review", true, 1)
			// This malformed inactive projection retains its referenced row;
			// availability must not invent an active contract to recover it.
			if _, err := j.s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_contracts SET superseded_by=contract_version WHERE work_id=?; DELETE FROM write_guard`, workID); err != nil {
				t.Fatal(err)
			}
			check("no active contract", false, 0)
		})
	}
}

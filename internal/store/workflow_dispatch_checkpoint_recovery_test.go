package store

import (
	"context"
	"slices"
	"testing"
)

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
			expected := !terminal && workflowUnhealthyVerdictRouteTarget(definition, step.ID) != ""
			if !terminal {
				if step.Kind == WorkflowStepHumanCheckpoint {
					expected = expected || slices.Contains(step.Actions, "confirm_premise")
				} else {
					expected = expected || slices.Contains(step.Actions, "record_review")
				}
			}
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
	for _, kind := range []WorkflowStepKind{WorkflowStepCrossAuthority, WorkflowStepInternalSQLite, WorkflowStepExternalEffect, WorkflowStepHumanCheckpoint} {
		t.Run(string(kind), func(t *testing.T) {
			actions := []string{"record_review"}
			if kind == WorkflowStepHumanCheckpoint {
				actions = append(actions, "confirm_premise")
			}
			definition := WorkflowDefinition{Ref: "workflow.correction_probe", StepGraph: WorkflowStepGraph{
				StartStep: "producing", TerminalSteps: []string{"sealed"}, Steps: []WorkflowStep{
					{ID: "producing", Kind: kind, Actions: actions},
					{ID: "quiet", Kind: WorkflowStepHumanCheckpoint, Actions: []string{"record_review"}},
					{ID: "plain", Kind: WorkflowStepInternalSQLite, Actions: []string{"record_note"}},
					{ID: "sealed", Kind: kind, Actions: actions},
				},
			}}
			for _, step := range []string{"producing", "quiet", "plain", "sealed", "no-such-step"} {
				want := step == "producing"
				if got := workflowContractCorrectionCheckpoint(definition, step); got != want {
					t.Fatalf("%s: correction checkpoint=%v, want %v", step, got, want)
				}
				if got := admissionContractCorrection(definition, admissionModelState{step: step, contracts: 1}); got != want {
					t.Fatalf("%s: model correction checkpoint=%v, want %v", step, got, want)
				}
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
				pin := issue1013Pin(t, j.s, workID)
				intent := false
				for _, candidate := range pin.NextValidIntents {
					if candidate.ActionID == "supersede_contract" && candidate.ReasonCode == "operator_contract_correction" {
						intent = true
					}
				}
				if intent != available {
					t.Fatalf("%s: pin correction intent=%v, want %v", stage, intent, available)
				}
				if got := admissionContractCorrection(definition, admissionModelState{step: step, contracts: contracts}); got != available {
					t.Fatalf("%s: model availability=%v, want %v", stage, got, available)
				}
				if !available {
					err := WorkflowActionPreflightWithRegistry(t.Context(), j.s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
						WorkID: workID, ExpectedVersion: pin.Version, ActionID: "supersede_contract", Actor: j.fixture.owner,
					})
					if !hasFailureKind(err, KindInvalidOperation) {
						t.Fatalf("%s: preflight=%v, want stale-contract recovery refusal", stage, err)
					}
				}
				if after := contractCorrectionEffectSnapshot(t, j.s, workID); after != before {
					t.Fatalf("%s: read-only correction surfaces changed durable state", stage)
				}
			}
			check("ordinary producer before Review", true, 1)
			acceptCurrentReviewForTest(t, j.fixture, j.repo, "correction_required")
			check("recorded corrective Review", true, 1)
			if _, err := j.s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_contracts SET superseded_by=contract_version+1 WHERE work_id=?; DELETE FROM write_guard`, workID); err != nil {
				t.Fatal(err)
			}
			check("no active contract", false, 0)
		})
	}
}

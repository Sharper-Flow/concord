package store

import (
	"slices"
	"testing"
)

func TestWorkflowCorrectionAndReviewStepMatrix(t *testing.T) {
	type expectation struct{ correction, review, readyAdvances bool }
	want := map[string]map[string]expectation{
		"workflow.implementation": {
			"proposal": {true, false, false}, "alignment": {false, false, false},
			"planning": {false, false, false}, "design": {true, false, false},
			"execution": {true, true, true}, "refine": {true, true, true},
			"delivery": {false, false, false}, "acceptance": {true, true, false},
			"release": {true, false, false},
		},
		"workflow.break_fix": {
			"diagnosis": {false, false, false}, "planning": {false, false, false},
			"repair": {true, true, true}, "refine": {true, true, true},
			"delivery": {false, false, false}, "verify": {true, true, false},
			"complete": {true, false, false},
		},
		"workflow.research": {
			"frame": {false, false, false}, "investigate": {true, false, false},
			"findings": {true, false, false}, "conclude": {true, true, false},
			"complete": {true, false, false},
		},
	}
	families, steps := 0, 0
	for _, definition := range BuiltinWorkflowDefinitions() {
		if workflowFamilyArchived(definition.Ref) {
			continue
		}
		expected, exists := want[definition.Ref]
		if !exists || len(expected) != len(definition.StepGraph.Steps) {
			t.Fatalf("%s: step matrix does not cover the exact current definition", definition.Ref)
		}
		families++
		for _, step := range definition.StepGraph.Steps {
			steps++
			t.Run(definition.Ref+"/"+step.ID, func(t *testing.T) {
				e, exists := expected[step.ID]
				if !exists {
					t.Fatal("current step is absent from the preservation matrix")
				}
				if got := workflowContractCorrectionCheckpoint(definition, step.ID); got != e.correction {
					t.Fatalf("correction checkpoint=%v, want %v", got, e.correction)
				}
				if !slices.Contains(step.Actions, "complete") {
					if got := admissionContractCorrection(definition, admissionModelState{step: step.ID, contracts: 1}); got != e.correction {
						t.Fatalf("ordinary correction model=%v, want %v", got, e.correction)
					}
				}
				if got := slices.Contains(step.Actions, "record_review"); got != e.review {
					t.Fatalf("declared Review=%v, want %v", got, e.review)
				}
				if e.review {
					for _, successor := range admissionSuccessors(definition, admissionModelState{step: step.ID}, "record_review") {
						wantStep := step.ID
						if e.readyAdvances && successor.review.accepted == "ready_for_final_proof" {
							wantStep = workflowNextStep(definition, step.ID)
						}
						if successor.step != wantStep {
							t.Errorf("%s model successor=%s, want %s", successor.review.accepted, successor.step, wantStep)
						}
					}
				}
				for _, disposition := range []string{"continue", "correction_required", "revision_required", "ready_for_final_proof"} {
					completion := workflowActionCompletedPayload{StepID: step.ID, ActionID: "record_review", Review: &ReviewBlock{Disposition: disposition}}
					if got, want := workflowCompletedActionAdvances(definition, completion), e.readyAdvances && disposition == "ready_for_final_proof"; got != want {
						t.Errorf("%s: advances=%v, want %v", disposition, got, want)
					}
				}
				if workflowCompletedActionAdvances(definition, workflowActionCompletedPayload{StepID: step.ID, ActionID: "record_review"}) {
					t.Fatal("missing Review advanced the step")
				}
				work := "correction-matrix-" + step.ID
				seed := seedWorkflowReturnRouteFixtureWithDefinition(t, work, mustBuiltinDefinition(t, definition.Ref), step.ID, []string{}, []string{})
				before := contractCorrectionEffectSnapshot(t, seed.store, work)
				wantAvailable := e.correction && !slices.Contains(step.Actions, "complete")
				available, err := workflowContractCorrectionAvailable(t.Context(), seed.store.db, work, definition, step.ID, "test")
				if err != nil || available != wantAvailable {
					t.Fatalf("active-contract correction availability=%v, want %v: %v", available, wantAvailable, err)
				}
				_, _, resolutionErr := WorkflowActionDefinitionFor(t.Context(), seed.store, BuiltinWorkflowRegistry(), work, "supersede_contract")
				if (resolutionErr == nil) != wantAvailable {
					t.Fatalf("correction resolution=%v, want available=%v", resolutionErr, wantAvailable)
				}
				pin, err := ReadWorkPin(t.Context(), seed.store, work)
				if err != nil {
					t.Fatal(err)
				}
				advertised := slices.ContainsFunc(pin.NextValidIntents, func(intent WorkPinIntent) bool {
					return intent.ActionID == "supersede_contract" && intent.ReasonCode == "operator_contract_correction"
				})
				if advertised != wantAvailable {
					t.Fatalf("correction intent=%v, want %v", advertised, wantAvailable)
				}
				if after := contractCorrectionEffectSnapshot(t, seed.store, work); after != before {
					t.Fatal("correction discovery changed durable state")
				}
			})
		}
		if workflowContractCorrectionCheckpoint(definition, "missing") || workflowCompletedActionAdvances(definition, workflowActionCompletedPayload{StepID: "missing", ActionID: "record_review", Review: &ReviewBlock{Disposition: "ready_for_final_proof"}}) {
			t.Fatalf("%s: nonexistent step acquired correction or advancement authority", definition.Ref)
		}
	}
	if families != len(want) || steps != 21 {
		t.Fatalf("checked %d families and %d steps, want 3 families and 21 steps", families, steps)
	}
}

func TestCoordinatorReviewAtEveryVerdictStepHolds(t *testing.T) {
	for _, tc := range []struct{ family, step string }{
		{"workflow.implementation", "acceptance"},
		{"workflow.break_fix", "verify"},
		{"workflow.research", "conclude"},
	} {
		t.Run(tc.family, func(t *testing.T) {
			work := "verdict-review-" + tc.step
			seed := seedWorkflowReturnRouteFixtureWithDefinition(t, work, mustBuiltinDefinition(t, tc.family), tc.step, []string{}, []string{})
			f := workContextFixture{store: seed.store, workID: work, owner: seed.owner, operator: seed.operator}
			repo, _ := reviewSubjectClaim(t, f.store, work)
			for _, disposition := range []string{"continue", "correction_required", "revision_required", "ready_for_final_proof"} {
				t.Run(disposition, func(t *testing.T) {
					before := readWorkVersion(t, f.store, work)
					ref, _ := completeCurrentReviewForTest(t, f, repo, disposition)
					if got := currentStep(t, f.store, work); got != tc.step {
						t.Fatalf("recording %s Review advanced %s to %s", disposition, tc.step, got)
					}
					pin, err := ReadWorkPin(t.Context(), f.store, work)
					if err != nil || pin.Lifecycle != "in_progress" || pin.Version <= before || pin.Review == nil || pin.Review.Accepted == nil || pin.Review.Accepted.ReportRef != ref || pin.Review.Accepted.Report.Disposition != disposition || !pin.Review.DependenciesCurrent {
						t.Fatalf("holding Review did not retain the current report: %+v, %v", pin, err)
					}
				})
			}
		})
	}
}

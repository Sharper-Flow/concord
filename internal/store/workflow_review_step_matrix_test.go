package store

import (
	"slices"
	"testing"
)

func TestWorkflowReviewStepMatrix(t *testing.T) {
	want := map[string][]string{
		"workflow.implementation": {"execution", "refine", "acceptance"},
		"workflow.break_fix":      {"repair", "refine", "verify"},
		"workflow.research":       {"conclude"},
	}
	families, steps := 0, 0
	for _, definition := range BuiltinWorkflowDefinitions() {
		if workflowFamilyArchived(definition.Ref) {
			continue
		}
		expected, exists := want[definition.Ref]
		if !exists {
			t.Fatalf("%s: missing Review matrix", definition.Ref)
		}
		families++
		for _, step := range definition.StepGraph.Steps {
			steps++
			t.Run(definition.Ref+"/"+step.ID, func(t *testing.T) {
				review := slices.Contains(expected, step.ID)
				if got := slices.Contains(step.Actions, "record_review"); got != review {
					t.Fatalf("declared Review=%v, want %v", got, review)
				}
				if review {
					for _, successor := range admissionSuccessors(definition, admissionModelState{step: step.ID}, "record_review") {
						if successor.step != step.ID {
							t.Errorf("%s model successor=%s, want held at %s", successor.review.accepted, successor.step, step.ID)
						}
					}
				}
				for _, disposition := range []string{"continue", "correction_required", "revision_required", "ready_for_final_proof"} {
					completion := workflowActionCompletedPayload{StepID: step.ID, ActionID: "record_review", Review: &ReviewBlock{Disposition: disposition}}
					if workflowCompletedActionAdvances(definition, completion) {
						t.Errorf("%s: recorded Review advanced the step; record_delivery owns the producing-step exit", disposition)
					}
				}
				if workflowCompletedActionAdvances(definition, workflowActionCompletedPayload{StepID: step.ID, ActionID: "record_review"}) {
					t.Fatal("missing Review advanced the step")
				}
			})
		}
		if workflowCompletedActionAdvances(definition, workflowActionCompletedPayload{StepID: "missing", ActionID: "record_review", Review: &ReviewBlock{Disposition: "ready_for_final_proof"}}) {
			t.Fatalf("%s: nonexistent step acquired advancement authority", definition.Ref)
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
			reviewSubjectClaim(t, f.store, work)
			for _, disposition := range []string{"continue", "correction_required", "revision_required", "ready_for_final_proof"} {
				t.Run(disposition, func(t *testing.T) {
					before := readWorkVersion(t, f.store, work)
					ref, _ := completeCurrentReviewForTest(t, f, disposition)
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

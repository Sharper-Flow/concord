package store

import "testing"

func TestCoordinatorReviewAtPlanFrontiersHolds(t *testing.T) {
	for _, tc := range []struct {
		planType WorkflowStepPlanType
		step     string
	}{
		{WorkflowStepPlanChange, "review"},
		{WorkflowStepPlanFix, "review"},
		{WorkflowStepPlanResearch, "conclude"},
	} {
		t.Run(string(tc.planType), func(t *testing.T) {
			work := "verdict-review-" + string(tc.planType)
			seed := seedWorkflowReturnRouteFixtureWithPlan(t, work, tc.planType, tc.step, []string{}, []string{})
			f := workContextFixture{store: seed.store, workID: work, owner: seed.owner, operator: seed.operator}
			reviewSubjectClaim(t, f.store, work)
			for _, disposition := range []string{"continue", "correction_required", "revision_required", "ready_for_final_proof"} {
				t.Run(disposition, func(t *testing.T) {
					before := readWorkVersion(t, f.store, work)
					ref, _ := completeCurrentReviewForTest(t, f, disposition)
					if got := workflowPlanFixtureFrontier(t, f.store, work); got != tc.step {
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

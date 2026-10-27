package store

import "testing"

func TestReviewAcceptancePreservesContinueJobReadiness(t *testing.T) {
	for _, disposition := range []string{"continue", "correction_required", "revision_required", "ready_for_final_proof"} {
		t.Run(disposition, func(t *testing.T) {
			j := pReturnReviewFixture(t, "continue-job-"+disposition, "workflow.break_fix", disposition)
			state, err := readWorkflowReviewFacts(t.Context(), j.s.db, j.workID)
			if err != nil || state.Accepted == nil {
				t.Fatalf("accepted Review = %+v, err = %v", state.Accepted, err)
			}
			auth, result, err := readReviewAttempt(t.Context(), j.s.db, j.workID, state.Accepted.AttemptID)
			if err != nil || auth.WorkerJob == nil {
				t.Fatalf("Review job = %+v, err = %v", auth.WorkerJob, err)
			}
			views, err := readWorkerJobRevisions(t.Context(), j.s.db, j.workID)
			if err != nil {
				t.Fatal(err)
			}
			for _, view := range views {
				if view.Binding != *auth.WorkerJob {
					continue
				}
				if disposition == "continue" {
					if view.State != "recorded" || !view.Ready || view.SatisfiedResultRef != "" || !workerReviewContinuesJob(auth, result) {
						t.Fatalf("continue Review consumed its job: %+v", view)
					}
				} else if view.State != "satisfied" || view.Ready || view.SatisfiedResultRef == "" || workerReviewContinuesJob(auth, result) {
					t.Fatalf("%s Review changed existing satisfaction: %+v", disposition, view)
				}
				return
			}
			t.Fatal("accepted Review's exact job revision is missing")
		})
	}
}

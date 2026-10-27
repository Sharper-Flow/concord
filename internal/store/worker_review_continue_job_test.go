package store

import (
	"strings"
	"testing"
)

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

func TestContinueReviewCannotAssertWholeWorkDelivery(t *testing.T) {
	c := acceptDeliveryCandidateAt(t, "continue-review-delivery", "workflow.break_fix", "refine", false)
	j := c.j
	job := WorkerJobBinding{JobID: "job:continue-delivery", Revision: 1}
	recordWorkerJobRevisionForTest(t, j.s, j.workID, j.fixture.owner, &job)
	attempt, epoch := completeCurrentReviewForBoundJobForTest(t, j.fixture, j.repo, "continue", job, func(report *WorkerReviewBlock) {
		for _, criterion := range report.Dependencies.Criteria {
			report.Assessments = append(report.Assessments, WorkerReviewAssessment{PredicateID: criterion.PredicateID, Assessment: "supported", ReceiptRefs: []string{c.early.OperationRef}})
		}
	})
	before := j.effects(t)
	err := j.action(t, "accept_worker_result", map[string]any{
		"attempt_id": attempt, "attempt_epoch": epoch,
		"delivery_artifact": "artifact:permission-only", "delivery_state": "asserted",
	}, j.fixture.owner)
	if !hasFailureKind(err, KindMissingEvidence) || !strings.Contains(err.Error(), "leaving execution requires accepted ready_for_final_proof, not continue") || j.effects(t) != before {
		t.Fatalf("permission-only Review asserted delivery or escaped its semantic gate: %v", err)
	}
	if err := j.action(t, "accept_worker_result", map[string]any{"attempt_id": attempt, "attempt_epoch": epoch}, j.fixture.owner); err != nil {
		t.Fatal(err)
	}
	views, err := readWorkerJobRevisions(t.Context(), j.s.db, j.workID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, view := range views {
		if view.Binding == job {
			found = true
			if view.State != "recorded" || !view.Ready || view.SatisfiedResultRef != "" {
				t.Fatalf("local permission-only acceptance satisfied its job: %+v", view)
			}
		}
	}
	if !found {
		t.Fatal("permission-only Review's bound job disappeared")
	}
	before = j.effects(t)
	err = j.action(t, "record_delivery", map[string]any{"delivery_artifact": "artifact:permission-only", "delivery_state": "asserted"}, j.fixture.owner)
	if !hasFailureKind(err, KindMissingEvidence) || !strings.Contains(err.Error(), "ready_for_final_proof, not continue") || j.effects(t) != before {
		t.Fatalf("standalone delivery accepted permission-only Review or changed state: %v", err)
	}
}

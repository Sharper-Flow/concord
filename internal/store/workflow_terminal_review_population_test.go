package store

import (
	"path/filepath"
	"strings"
	"testing"
)

// CD-0242 D5 requires an accepted Review disposition bound to the current
// candidate; D9 holds progression on open in-scope blocking findings. The
// candidate moves only through a recorded integration, here a Review's
// corrective pass that Main accepts.

// Job-bound reviews run at refinement, where a ready revision can be recorded.
func terminalRefineReview(t *testing.T, j *candidateAtVerify, attemptID, jobID, observeID, wantSubject string) {
	t.Helper()
	job := terminalDispatchReview(t, j, attemptID, jobID, observeID, wantSubject)
	j.completeLane(t, attemptID, "review", &job, nil)
	terminalAcceptReview(t, j, attemptID)
}

// terminalCorrectiveReview dispatches a job-bound Review of the current
// candidate, commits its corrective change B inside the job's path scope, and
// has Main accept the Review, which integrates B as the candidate. It returns
// the Review's qualified run of B.
func terminalCorrectiveReview(t *testing.T, j *candidateAtVerify, attemptID, jobID, observeID string) WorktreeVerifyResult {
	t.Helper()
	job := terminalDispatchReview(t, j, attemptID, jobID, observeID, j.a)
	var run WorktreeVerifyResult
	j.b, run = j.completeCorrectiveReview(t, attemptID, job, filepath.Join("pkg", "value.go"), "package oracle\n\n// Value returns the approved value.\nfunc Value() int { return 7 }\n", observeID+"-output")
	terminalAcceptReview(t, j, attemptID)
	j.requireAcceptedReview(t, attemptID, j.b)
	return run
}

func terminalDispatchReview(t *testing.T, j *candidateAtVerify, attemptID, jobID, observeID, wantSubject string) WorkerJobBinding {
	t.Helper()
	preparation := j.prepare(t, observeID)
	job := j.recordReadyJob(t, jobID, preparation)
	j.dispatchJob(t, attemptID, "review", "review", job)
	if got := j.dispatchedSubject(t, attemptID); got != wantSubject {
		t.Fatalf("review dispatch subject = %q, want %q", got, wantSubject)
	}
	return job
}

func terminalAcceptReview(t *testing.T, j *candidateAtVerify, attemptID string) {
	t.Helper()
	if err := j.acceptEvidence(t, attemptID); err != nil {
		t.Fatalf("accept %s: %v", attemptID, err)
	}
}

// terminalRecordCurrentVerdict binds one qualified run of the current subject
// and associates it with the criterion.
func terminalRecordCurrentVerdict(t *testing.T, j *candidateAtVerify, run WorktreeVerifyResult) {
	t.Helper()
	j.bindVerification(t, run)
	if err := j.action(t, "record_verdict", criterionAssociationPayload(run.OperationRef, "predicate:work-context"), j.fixture.owner); err != nil {
		t.Fatalf("record verdict over %s: %v", run.SubjectRef, err)
	}
}

// terminalRequireCompleted asserts the ordinary completion finished the work.
func terminalRequireCompleted(t *testing.T, j *candidateAtVerify) {
	t.Helper()
	var state string
	if err := j.s.db.QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, j.workID).Scan(&state); err != nil || state != "completed" {
		t.Fatalf("instance state = %q err=%v, want completed", state, err)
	}
}

func terminalJourneyThroughRefineReviews(t *testing.T, workID string) *candidateAtVerify {
	t.Helper()
	j := journeyToRefineA(t, workID)
	terminalRefineReview(t, j, "attempt-review-refine-one", "job:terminal-review-refine-one", "review-refine-one", j.a)
	terminalRefineReview(t, j, "attempt-review-refine-two", "job:terminal-review-refine-two", "review-refine-two", j.a)
	j.runA = j.observeRun(t, "refine-a")
	j.snapshotA = j.snapshotSubjectRecords(t, j.a)
	return j
}

// terminalDeliverB records the refine and delivery exits over the integrated
// B, each proved by the named qualified B run, and returns the work to verify.
func terminalDeliverB(t *testing.T, j *candidateAtVerify, runB WorktreeVerifyResult) {
	t.Helper()
	j.bindVerification(t, runB)
	for _, gate := range []string{"refine", "delivery"} {
		if err := j.action(t, "record_delivery", map[string]any{"delivery_artifact": "artifact:candidate-b", "delivery_state": "asserted"}, j.acceptor); err != nil {
			t.Fatalf("%s delivery over B: %v", gate, err)
		}
	}
	if got := currentStep(t, j.s, j.workID); got != "verify" {
		t.Fatalf("step after delivery over B = %q, want verify", got)
	}
}

func TestManagedCandidateTerminalReviewPopulation(t *testing.T) {
	t.Run("accepted_b_review_binds_integrated_candidate", func(t *testing.T) {
		j := journeyThroughA(t, "candidate-terminal-review-g01", true)

		// A second pass-0 review of A, accepted at the verify step beside
		// the journey's own review.
		j.dispatchReview(t, "attempt-review-pass0")
		j.completeLane(t, "attempt-review-pass0", "review", nil, nil)
		if err := j.acceptEvidence(t, "attempt-review-pass0"); err != nil {
			t.Fatalf("accept the second pass-0 review: %v", err)
		}
		j.snapshotA = j.snapshotSubjectRecords(t, j.a)

		// Main accepts the corrective Review and integrates B; the reviewer's
		// verdict cites a qualified B run.
		terminalRecordCurrentVerdict(t, j, terminalCorrectiveReview(t, j, "attempt-review-b", "job:terminal-review-b", "review-b"))

		// The accepted Review and criterion proof both bind integrated B.
		if err := j.confirmPremise(t); err != nil {
			t.Fatalf("confirm premise over B with an accepted Review bound to B: %v", err)
		}
		bindCompletionClosure(t, j.s, j.workID, j.fixture.owner)
		if err := j.completeOrdinary(t); err != nil {
			t.Fatalf("ordinary complete over B with an accepted Review bound to B: %v", err)
		}
		terminalRequireCompleted(t, j)
		j.requireRecordsUnchanged(t, j.snapshotA)
	})

	t.Run("accepted_a_review_refuses_unrecorded_clean_b", func(t *testing.T) {
		j := journeyToVerify(t, "candidate-terminal-review-a", true)
		j.observeB(t)
		// B is unrecorded, so its run is no proof of the integrated A.
		runB := j.observeRun(t, "b-verdict")
		j.bindVerification(t, runB)
		before := j.effects(t)
		var failure *Failure
		if err := j.action(t, "record_verdict", criterionAssociationPayload(runB.OperationRef, "predicate:work-context"), j.fixture.owner); !failureAs(err, &failure) || failure.Kind != KindMissingEvidence || !strings.Contains(failure.Detail, "requires a passing producer receipt on the final candidate") {
			t.Fatalf("criterion association over unrecorded B = %v, want the final-candidate receipt refusal", err)
		}
		if after := j.effects(t); after != before {
			t.Fatalf("refused association changed durable state:\nbefore=%s\nafter=%s", before, after)
		}
		if err := j.confirmPremise(t); failureKind(err) != KindStaleRequiresReview {
			t.Fatalf("confirm with the current accepted A review and clean B: %v, want stale_requires_review", err)
		}
		if after := j.effects(t); after != before {
			t.Fatalf("stale review refusal changed durable state:\nbefore=%s\nafter=%s", before, after)
		}
		j.requireRecordsUnchanged(t, j.snapshotA)
	})

	t.Run("distinct_job_reviews_admit_on_integrated_b", func(t *testing.T) {
		j := terminalJourneyThroughRefineReviews(t, "candidate-terminal-review-jobs")
		runB := terminalCorrectiveReview(t, j, "attempt-review-b", "job:terminal-review-refine-one", "review-b")
		terminalDeliverB(t, j, runB)
		terminalRecordCurrentVerdict(t, j, runB)
		if err := j.confirmPremise(t); err != nil {
			t.Fatalf("confirm premise over B with an accepted Review bound to B: %v", err)
		}
		bindCompletionClosure(t, j.s, j.workID, j.fixture.owner)
		if err := j.completeOrdinary(t); err != nil {
			t.Fatalf("ordinary complete over B with an accepted Review bound to B: %v", err)
		}
		terminalRequireCompleted(t, j)
		j.requireRecordsUnchanged(t, j.snapshotA)
	})
}

package store

import (
	"path/filepath"
	"testing"
)

// CD-0206 D3 retires superseded completed reviews. Terminal subject checks
// consume only accepted reviews that remain current under that rule. The
// candidate moves only through a recorded integration, here a Review's
// corrective pass that Main accepts (CD-0242 D5).

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

// terminalCharacterize runs the declared acceptance routes and reports their
// actual typed outcomes. No expected disposition is asserted: the case pins
// only that a refusal leaves the durable state unchanged and that retained
// records stay byte-identical. An admission is the review-obligation owner's
// question and is reported, never repaired here.
func terminalCharacterize(t *testing.T, j *candidateAtVerify, label string) {
	t.Helper()
	t.Logf("case4[%s] live_HEAD=%s native_pin=%s", label, j.head(t), j.pinned(t))
	before := j.effects(t)
	confirmErr := j.confirmPremise(t)
	if confirmErr != nil {
		t.Logf("case4[%s] confirm_premise -> refused kind=%s: %v", label, failureKind(confirmErr), confirmErr)
		if after := j.effects(t); after != before {
			t.Errorf("case4[%s] confirm_premise refusal changed durable state:\nbefore=%s\nafter=%s", label, before, after)
		}
		return
	}
	t.Logf("case4[%s] confirm_premise -> admitted", label)
	before = j.effects(t)
	completeErr := j.completeOrdinary(t)
	if completeErr != nil {
		t.Logf("case4[%s] complete -> refused kind=%s: %v", label, failureKind(completeErr), completeErr)
		if after := j.effects(t); after != before {
			t.Errorf("case4[%s] complete refusal changed durable state:\nbefore=%s\nafter=%s", label, before, after)
		}
		return
	}
	t.Logf("case4[%s] complete -> admitted", label)
	var state string
	if err := j.s.db.QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, j.workID).Scan(&state); err != nil {
		t.Fatalf("read instance state: %v", err)
	}
	t.Logf("case4[%s] instance_state=%s", label, state)
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
	t.Run("superseded_reviews_admit_on_fresh_b_review", func(t *testing.T) {
		j := journeyThroughA(t, "candidate-terminal-review-g01", true)

		// A second pass-0 review of A, accepted at the verify step beside
		// the journey's own review.
		j.dispatchReview(t, "attempt-review-pass0")
		j.completeLane(t, "attempt-review-pass0", "review", nil, nil)
		if err := j.acceptEvidence(t, "attempt-review-pass0"); err != nil {
			t.Fatalf("accept the second pass-0 review: %v", err)
		}
		j.snapshotA = j.snapshotSubjectRecords(t, j.a)

		// A corrective Review supersedes the pass-0 reviews and integrates
		// B; the reviewer's verdict cites a fresh qualified B run.
		terminalRecordCurrentVerdict(t, j, terminalCorrectiveReview(t, j, "attempt-review-b", "job:terminal-review-b", "review-b"))

		// The reviews the corrective pass superseded must not block the
		// accepted B review: terminal admission admits.
		if err := j.confirmPremise(t); err != nil {
			t.Fatalf("confirm premise over B with the newest review accepted on B: %v", err)
		}
		if err := j.completeOrdinary(t); err != nil {
			t.Fatalf("ordinary complete over B with the newest review accepted on B: %v", err)
		}
		terminalRequireCompleted(t, j)
		j.requireRecordsUnchanged(t, j.snapshotA)
	})

	t.Run("consumed_review_on_a_refuses_clean_b", func(t *testing.T) {
		j := journeyToVerify(t, "candidate-terminal-review-a", true)
		j.observeB(t)
		// B is unrecorded, so its run is no proof of the integrated A.
		runB := j.observeRun(t, "b-verdict")
		j.bindVerification(t, runB)
		before := j.effects(t)
		if err := j.action(t, "record_verdict", criterionAssociationPayload(runB.OperationRef, "predicate:work-context"), j.fixture.owner); err == nil {
			t.Fatal("criterion association over unrecorded B admitted")
		} else {
			t.Logf("criterion association over unrecorded B refused: %v", err)
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

	t.Run("distinct_job_reviews_admit_on_fresh_b_review", func(t *testing.T) {
		j := terminalJourneyThroughRefineReviews(t, "candidate-terminal-review-jobs")
		runB := terminalCorrectiveReview(t, j, "attempt-review-b", "job:terminal-review-refine-one", "review-b")
		terminalDeliverB(t, j, runB)
		terminalRecordCurrentVerdict(t, j, runB)
		if err := j.confirmPremise(t); err != nil {
			t.Fatalf("confirm premise over B with the newest review accepted on B: %v", err)
		}
		if err := j.completeOrdinary(t); err != nil {
			t.Fatalf("ordinary complete over B with the newest review accepted on B: %v", err)
		}
		terminalRequireCompleted(t, j)
		j.requireRecordsUnchanged(t, j.snapshotA)
	})

	t.Run("undispositioned_newest_review_characterized", func(t *testing.T) {
		j := journeyThroughA(t, "candidate-terminal-review-open", true)

		// The newest completed review is a second Review of A, completed
		// with no disposition.
		j.dispatchReview(t, "attempt-review-newest")
		j.completeLane(t, "attempt-review-newest", "review", nil, nil)

		terminalCharacterize(t, j, "undispositioned")
		j.requireRecordsUnchanged(t, j.snapshotA)
	})

	t.Run("rejected_newest_review_characterized", func(t *testing.T) {
		j := journeyThroughA(t, "candidate-terminal-review-rejected", true)

		j.dispatchReview(t, "attempt-review-newest")
		j.completeLane(t, "attempt-review-newest", "review", nil, nil)

		// The newest completed review is the second Review of A, completed
		// and then rejected. A refusal of the rejection itself is
		// characterized and reported, never routed around.
		rejector := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/terminal-review-rejector", SessionRef: "session/" + j.workID + "-rejector", ActorClass: ActorAgent}
		rejectErr := j.action(t, "reject_worker_result", map[string]any{
			"attempt_id": "attempt-review-newest", "attempt_epoch": j.dispatchEpoch(t, "attempt-review-newest"),
			"diagnosis": "the newest review result does not satisfy the review obligation", "strategy": "obtain a fresh independent review of the current subject",
			"predicate_ids": []string{"predicate:work-context"}, "evidence_refs": []string{"candidate-completed-attempt-review-newest"},
		}, rejector)
		if rejectErr != nil {
			t.Fatalf("reject the newest completed review: %v", rejectErr)
		}
		t.Logf("case4[rejected] reject_worker_result -> admitted")
		terminalCharacterize(t, j, "rejected")
		j.requireRecordsUnchanged(t, j.snapshotA)
	})
}

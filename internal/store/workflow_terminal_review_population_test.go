package store

import (
	"context"
	"testing"
)

// CD-0206 D3 retires superseded completed reviews. Terminal subject checks
// consume only accepted reviews that remain current under that rule.

// Job-bound reviews run at refinement, where a ready revision can be recorded.
func terminalRefineReview(t *testing.T, j *candidateAtVerify, attemptID, jobID, observeID, wantSubject string) {
	t.Helper()
	preparation := j.prepare(t, observeID)
	job := j.recordReadyJob(t, jobID, preparation)
	j.dispatchJob(t, attemptID, "review", "review", job)
	if got := j.dispatchedSubject(t, attemptID); got != wantSubject {
		t.Fatalf("review dispatch subject = %q, want %q", got, wantSubject)
	}
	j.completeLane(t, attemptID, "review", &job, nil)
	if err := j.action(t, "accept_worker_result", map[string]any{"attempt_id": attemptID, "attempt_epoch": j.dispatchEpoch(t, attemptID)}, j.acceptor); err != nil {
		t.Fatalf("accept %s: %v", attemptID, err)
	}
}

// terminalRecordCurrentVerdict binds one qualified run of the current subject
// and records the reviewer's healthy verdict over it.
func terminalRecordCurrentVerdict(t *testing.T, j *candidateAtVerify, run WorktreeVerifyResult) {
	t.Helper()
	j.bindVerification(t, run)
	if err := j.action(t, "record_verdict", map[string]any{"contract_version": 1, "verdicts": []map[string]any{{"predicate_id": "predicate:work-context", "verdict_kind": "ok", "evaluation_evidence": []string{run.OperationRef}}}}, j.reviewer); err != nil {
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

		// The correction retires the pass-0 reviews while the live subject
		// is still A, and the corrected pass produces B through the managed
		// route and executes the pinned harness on it.
		j.correctToRepair(t, j.observeRun(t, "pass0-correction"))
		j.commitB(t)
		j.observeB(t)
		preparationB := j.prepare(t, "b")
		jobB := j.recordReadyJob(t, "job:candidate", preparationB)
		j.dispatchJob(t, "attempt-verify-b", "verify", "verification", jobB)
		if got := j.dispatchedSubject(t, "attempt-verify-b"); got != j.b {
			t.Fatalf("B verification dispatch subject = %q, want B", got)
		}
		launches := j.launches
		o := j.executeRequest(t, "attempt-verify-b", jobB, preparationB)
		executed, err := j.s.VerifyWorktree(context.Background(), j.request("execute-b", &o))
		if err != nil || executed.Oracle == nil || executed.Oracle.Qualification != "pass" || j.launches != launches+1 {
			t.Fatalf("execute control on B: %+v err=%v", executed.Oracle, err)
		}
		exit := 0
		control := j.bundle.Control
		j.completeLane(t, "attempt-verify-b", "verification", &jobB, &WorkerOracleReceipt{
			ControlIDs: []string{control.ControlID}, CaseIDs: control.CaseIDs, SubjectCommit: j.b,
			RecipeSource: WorkerOracleRecipeSource{Kind: WorkContextSourceRepositoryFile, ProjectID: control.RecipeSource.ProjectID, Path: control.RecipeSource.Path, CommitOID: control.RecipeSource.CommitOID},
			Result:       OracleReceiptResultPass, ExitCode: &exit, RunRef: executed.OperationRef, EvidenceRefs: []string{executed.OperationRef},
		})
		if err := j.action(t, "accept_worker_result", map[string]any{"attempt_id": "attempt-verify-b", "attempt_epoch": j.dispatchEpoch(t, "attempt-verify-b")}, j.acceptor); err != nil {
			t.Fatalf("accept B verification: %v", err)
		}

		// The fresh consumed review of B runs at the refine visit of the
		// corrected pass, bound to its own job.
		j.bindVerification(t, j.observeRun(t, "repair-b"))
		if err := j.action(t, "record_delivery", map[string]any{"delivery_artifact": "artifact:candidate-b", "delivery_state": "asserted"}, j.acceptor); err != nil {
			t.Fatalf("repair delivery over B: %v", err)
		}
		if err := j.action(t, "checkpoint_refine", map[string]any{}, j.acceptor); err != nil {
			t.Fatalf("checkpoint_refine over B: %v", err)
		}
		terminalRefineReview(t, j, "attempt-review-b", "job:terminal-review-b", "review-b", j.b)

		// Deliveries over B return the work to verify, and the reviewer's
		// verdict cites the B execution.
		j.bindVerification(t, j.observeRun(t, "refine-b"))
		for _, gate := range []string{"refine", "delivery"} {
			if err := j.action(t, "record_delivery", map[string]any{"delivery_artifact": "artifact:candidate-b", "delivery_state": "asserted"}, j.acceptor); err != nil {
				t.Fatalf("%s delivery over B: %v", gate, err)
			}
		}
		if got := currentStep(t, j.s, j.workID); got != "verify" {
			t.Fatalf("step after delivery over B = %q, want verify", got)
		}
		terminalRecordCurrentVerdict(t, j, executed)

		// The reviews the correction retired must not block the fresh B
		// review: terminal admission admits.
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
		terminalRecordCurrentVerdict(t, j, j.observeRun(t, "b-verdict"))
		before := j.effects(t)
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
		j.commitB(t)
		j.observeB(t)
		terminalRefineReview(t, j, "attempt-review-b", "job:terminal-review-refine-one", "review-b", j.b)
		runB := j.observeRun(t, "refine-b")
		j.bindVerification(t, runB)
		for _, gate := range []string{"refine", "delivery"} {
			if err := j.action(t, "record_delivery", map[string]any{"delivery_artifact": "artifact:candidate-b", "delivery_state": "asserted"}, j.acceptor); err != nil {
				t.Fatalf("%s delivery over B: %v", gate, err)
			}
		}
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
		j.commitB(t)
		j.observeB(t)

		// The current B verification verdict lands while the newest
		// completed review is still the accepted A review.
		terminalRecordCurrentVerdict(t, j, j.observeRun(t, "b-verdict"))

		// The newest completed review is the B review, completed with no
		// disposition.
		j.dispatchReview(t, "attempt-review-b")
		j.completeLane(t, "attempt-review-b", "review", nil, nil)

		terminalCharacterize(t, j, "undispositioned")
		j.requireRecordsUnchanged(t, j.snapshotA)
	})

	t.Run("rejected_newest_review_characterized", func(t *testing.T) {
		j := journeyThroughA(t, "candidate-terminal-review-rejected", true)
		j.commitB(t)
		j.observeB(t)
		terminalRecordCurrentVerdict(t, j, j.observeRun(t, "b-verdict"))

		j.dispatchReview(t, "attempt-review-b")
		j.completeLane(t, "attempt-review-b", "review", nil, nil)

		// The newest completed review is the B review, completed and then
		// rejected. A refusal of the rejection itself is characterized and
		// reported, never routed around.
		rejector := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/terminal-review-rejector", SessionRef: "session/" + j.workID + "-rejector", ActorClass: ActorAgent}
		rejectErr := j.action(t, "reject_worker_result", map[string]any{
			"attempt_id": "attempt-review-b", "attempt_epoch": j.dispatchEpoch(t, "attempt-review-b"),
			"diagnosis": "the newest review result does not satisfy the review obligation", "strategy": "obtain a fresh independent review of the current subject",
			"predicate_ids": []string{"predicate:work-context"}, "evidence_refs": []string{"candidate-completed-attempt-review-b"},
		}, rejector)
		if rejectErr != nil {
			t.Fatalf("reject the newest completed review: %v", rejectErr)
		}
		t.Logf("case4[rejected] reject_worker_result -> admitted")
		terminalCharacterize(t, j, "rejected")
		j.requireRecordsUnchanged(t, j.snapshotA)
	})
}

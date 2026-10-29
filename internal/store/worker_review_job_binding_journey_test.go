package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFinalReviewBindsAcceptedRepairingReviewCandidate(t *testing.T) {
	j := journeyWithIntegratedB(t, "final-review-repaired-candidate")
	job, err := workflowDispatchedJobForAttempt(t.Context(), j.s.db, j.workID, "attempt-integrate-b")
	if err != nil || job == nil {
		t.Fatalf("repairing Review job = %+v, %v", job, err)
	}
	requireFinalReviewJourneyBinding(t, j.candidateJourney, *job)
	// This job uses the journey's ledger-fixture oracle, not its native pkg
	// harness. Qualify the unchanged bundle on the repaired candidate.
	oracle, err := readWorkerJobOracle(t.Context(), j.s.db, j.workID, *job)
	if err != nil || oracle == nil {
		t.Fatalf("repairing Review oracle = %+v, %v", oracle, err)
	}
	seedOraclePreparationLedgerFixture(t, j.s, j.workID, oracle)
	const attempt = "attempt-final-review-repaired"
	if err := j.dispatchReviewJob(t, attempt, *job); err != nil {
		t.Fatalf("dispatch final Review of the accepted repairing Review's candidate: %v", err)
	}
	if got := j.dispatchedSubject(t, attempt); got != j.b {
		t.Fatalf("final Review subject = %q, want repaired candidate %q", got, j.b)
	}
	j.completeLane(t, attempt, "review", job, nil)
	if err := j.acceptEvidence(t, attempt); err != nil {
		t.Fatalf("accept final Review of the repaired candidate: %v", err)
	}
	j.requireAcceptedReview(t, attempt, j.b)
}

func TestFinalReviewRepairKeepsOriginalImplementationSatisfaction(t *testing.T) {
	j := newCandidateJourney(t, "final-review-original-satisfaction")
	job := j.recordReadyJob(t, "job:original", j.prepare(t, "original-base"))
	const implementation = "attempt-original-implementation"
	j.dispatchJob(t, implementation, "implement", "implementation", job)
	if err := os.WriteFile(filepath.Join(j.repo, "pkg", "value.go"), []byte("package oracle\nfunc Value() int { return 7 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := j.commit(t, "worker@example.invalid", "worker returns the approved value")
	j.completeLane(t, implementation, "implementation", &job, nil)
	if err := j.acceptEvidence(t, implementation); err != nil {
		t.Fatalf("accept original implementation: %v", err)
	}
	j.observe(t, "original-output")
	j.prepare(t, "original-review-input")
	satisfaction := func() string {
		t.Helper()
		var ref string
		if err := j.s.db.QueryRow(`SELECT satisfied_result_ref FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=? AND digest=?`, j.workID, job.JobID, job.Revision, job.Digest).Scan(&ref); err != nil {
			t.Fatal(err)
		}
		return ref
	}
	original := satisfaction()
	const repair = "attempt-review-original-job"
	if err := j.dispatchReviewJob(t, repair, job); err != nil {
		t.Fatalf("dispatch repairing Review on the satisfied implementation revision: %v", err)
	}
	c, _ := j.completeCorrectiveReview(t, repair, job, filepath.Join("pkg", "review.go"), "package oracle\n\nconst reviewedCorrection = true\n", "original-job-repair")
	// A completed but unaccepted repairing Review cannot replace A's anchor.
	requireFinalReviewJourneyBinding(t, j, job)
	if err := j.acceptEvidence(t, repair); err != nil {
		t.Fatalf("accept repairing Review on the original satisfied revision: %v", err)
	}
	j.requireAcceptedReview(t, repair, c)
	if c == a || satisfaction() != original {
		t.Fatalf("repair candidate = %s, satisfaction = %s; want a changed candidate with original satisfaction %s", c, satisfaction(), original)
	}
	requireFinalReviewJourneyBinding(t, j, job)
	const final = "attempt-final-review-original-job"
	j.prepare(t, "original-final-review-input")
	if err := j.dispatchReviewJob(t, final, job); err != nil {
		t.Fatalf("dispatch final Review without replacing implementation satisfaction: %v", err)
	}
	j.completeLane(t, final, "review", &job, nil)
	if err := j.acceptEvidence(t, final); err != nil {
		t.Fatalf("accept final Review without replacing implementation satisfaction: %v", err)
	}
	j.requireAcceptedReview(t, final, c)
	if got := satisfaction(); got != original {
		t.Fatalf("final Review replaced satisfaction %s with %s", original, got)
	}
}

func TestFinalReviewBindsCurrentRevisionSatisfiedByVerifyAfterRepairingReview(t *testing.T) {
	// Stop at refine so record_delivery exercises the execution-exit guard,
	// rather than a step where that action is unavailable.
	j := journeyToRefineA(t, "final-review-verify-satisfaction")
	preparationA := j.prepare(t, "repair-input")
	repairJob := j.recordReadyJob(t, j.jobA.JobID, preparationA)
	const repair = "attempt-repairing-review"
	if err := j.dispatchReviewJob(t, repair, repairJob); err != nil {
		t.Fatalf("dispatch repairing Review: %v", err)
	}
	c, _ := j.completeCorrectiveReview(t, repair, repairJob, filepath.Join("pkg", "review.go"), "package oracle\n\nconst reviewedCorrection = true\n", "repair-output")
	if c == j.a {
		t.Fatal("repairing Review did not advance the candidate")
	}
	if err := j.acceptEvidence(t, repair); err != nil {
		t.Fatalf("accept repairing Review: %v", err)
	}
	j.requireAcceptedReview(t, repair, c)

	preparationC := j.prepare(t, "verify-input")
	current := j.recordReadyJob(t, repairJob.JobID, preparationC)
	if current.Revision != repairJob.Revision+1 {
		t.Fatalf("ordinary revision = %+v, want the successor of %+v", current, repairJob)
	}
	const verify = "attempt-verify-repaired"
	// dispatchJob's packet projection is implementation-specific. Build the
	// verification packet and start the actual verification lane instead.
	lane := reviewGateLane(t, "verification")
	packet := joinPacketFor(t, j.s, j.workID, currentStep(t, j.s, j.workID), verify, lane.ID, lane.Digest)
	packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, j.s, j.workID, current, lane.ID)
	if err := j.dispatchPacket(t, packet); err != nil {
		t.Fatalf("dispatch Verify of the current ordinary revision: %v", err)
	}
	j.laneDispatch(t, verify, "verification", &current)
	if got := j.dispatchedSubject(t, verify); got != c {
		t.Fatalf("Verify input = %q, want repaired candidate %q", got, c)
	}
	oracle := j.executeRequest(t, verify, current, preparationC)
	run, err := j.s.VerifyWorktree(t.Context(), j.request("execute-current-verify", &oracle))
	if err != nil || run.Oracle == nil || run.Oracle.Qualification != "pass" || run.Oracle.SubjectCommit != c {
		t.Fatalf("Verify oracle on repaired candidate = %+v, %v", run.Oracle, err)
	}
	j.bindVerification(t, run)
	control := j.bundle.Control
	exit := 0
	j.completeLane(t, verify, "verification", &current, &WorkerOracleReceipt{
		ControlIDs: []string{control.ControlID}, CaseIDs: control.CaseIDs, SubjectCommit: c,
		RecipeSource: WorkerOracleRecipeSource{Kind: WorkContextSourceRepositoryFile, ProjectID: control.RecipeSource.ProjectID, Path: control.RecipeSource.Path, CommitOID: control.RecipeSource.CommitOID},
		Result:       OracleReceiptResultPass, ExitCode: &exit, RunRef: run.OperationRef, EvidenceRefs: []string{run.OperationRef},
	})
	if err := j.acceptEvidence(t, verify); err != nil {
		t.Fatalf("accept Verify satisfying the current ordinary revision: %v", err)
	}
	j.requireAcceptedReview(t, repair, c)
	var state, satisfiedRef, satisfiedAttempt, satisfiedClass, delivered string
	if err := j.s.db.QueryRow(`SELECT j.state, j.satisfied_result_ref, e.attempt_id,
		json_extract(a.payload,'$.worker_capability_class'), json_extract(w.terminal_payload,'$.observed_delivery.commit')
		FROM worker_job_revisions j
		JOIN worker_attempt_dispositions e ON e.work_id=j.work_id AND e.event_id=j.satisfied_result_ref
		JOIN worker_authorizations a ON a.work_id=e.work_id AND a.attempt_id=e.attempt_id
		JOIN worker_attempts w ON w.work_id=e.work_id AND w.attempt_id=e.attempt_id
		WHERE j.work_id=? AND j.job_id=? AND j.revision=? AND j.digest=?`, j.workID, current.JobID, current.Revision, current.Digest).
		Scan(&state, &satisfiedRef, &satisfiedAttempt, &satisfiedClass, &delivered); err != nil {
		t.Fatal(err)
	}
	if state != "satisfied" || satisfiedAttempt != verify || satisfiedClass != "verification" || delivered != c || j.head(t) != c {
		t.Fatalf("current revision satisfaction = %s by %s (%s), output %s HEAD %s; want Verify on unchanged %s", state, satisfiedAttempt, satisfiedClass, delivered, j.head(t), c)
	}
	review, err := loadWorkflowReviewState(t.Context(), j.s.db, j.workID)
	if err != nil || !review.StaleJobs[current.JobID] {
		t.Fatalf("revised job's stale Review state = %+v, %v", review.StaleJobs, err)
	}
	if err := workflowAdmitReview(review, ""); !hasFailureKind(err, KindMissingEvidence) || !strings.Contains(err.Error(), "changed worker-job revision") {
		t.Fatalf("execution exit before fresh Review = %v, want changed-revision refusal", err)
	}
	delivery := map[string]any{"delivery_artifact": "artifact:repaired-candidate", "delivery_state": "asserted"}
	before := j.effects(t)
	if err := j.action(t, "record_delivery", delivery, j.acceptor); !hasFailureKind(err, KindMissingEvidence) || !strings.Contains(err.Error(), "changed worker-job revision") {
		t.Fatalf("record_delivery before fresh Review = %v, want changed-revision refusal", err)
	}
	if after := j.effects(t); after != before {
		t.Fatal("refused delivery changed durable state")
	}
	t.Logf("accepted repairing Review integrated %s; %s revision %d is satisfied by Verify; record_delivery requires a fresh Review", c, current.JobID, current.Revision)
	requireFinalReviewJourneyBinding(t, j.candidateJourney, current)
	const final = "attempt-final-review-current"
	if err := j.dispatchReviewJob(t, final, current); err != nil {
		t.Fatalf("dispatch final Review bound to the current Verify-satisfied revision: %v", err)
	}
	if got := j.dispatchedSubject(t, final); got != c {
		t.Fatalf("final Review input = %q, want repaired candidate %q", got, c)
	}
	j.reviewReceipts = []string{run.OperationRef}
	j.completeLane(t, final, "review", &current, nil)
	if err := j.acceptEvidence(t, final); err != nil {
		t.Fatalf("accept final Review bound to the current Verify-satisfied revision: %v", err)
	}
	j.requireAcceptedReview(t, final, c)
	var afterReview string
	if err := j.s.db.QueryRow(`SELECT satisfied_result_ref FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=? AND digest=?`, j.workID, current.JobID, current.Revision, current.Digest).Scan(&afterReview); err != nil || afterReview != satisfiedRef {
		t.Fatalf("final Review satisfaction = %q, %v; want Verify's acceptance %q", afterReview, err, satisfiedRef)
	}
	if err := j.action(t, "record_delivery", delivery, j.acceptor); err != nil {
		t.Fatalf("record_delivery after the fresh current Review: %v", err)
	}
}

func requireFinalReviewJourneyBinding(t *testing.T, j *candidateJourney, want WorkerJobBinding) {
	t.Helper()
	ready, err := readyWorkerJobRevisions(t.Context(), j.s.db, j.workID)
	if err != nil || len(ready) != 0 {
		t.Fatalf("ready jobs = %+v, %v; want none", ready, err)
	}
	final, err := finalReviewWorkerJobTx(t.Context(), j.s.db, j.workID)
	if err != nil || final == nil || *final != want {
		t.Errorf("final Review binding = %+v, %v; want current binding %+v", final, err, want)
	}
	snapshot, err := ReadWorkflowContinuity(t.Context(), j.s, ContinuityRequest{Work: j.workID})
	if err != nil {
		t.Fatal(err)
	}
	var bindings []WorkerJobBinding
	for _, job := range snapshot.ReviewWorkerJobs {
		bindings = append(bindings, WorkerJobBinding{JobID: job.JobID, Revision: job.Revision, Digest: job.Digest})
	}
	if len(bindings) != 1 || bindings[0] != want {
		t.Errorf("continuity final Review jobs = %+v; want only %+v", bindings, want)
	}
}

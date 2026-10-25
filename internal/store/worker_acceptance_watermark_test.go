package store

import (
	"reflect"
	"testing"
)

func TestWorkerRepeatedAcceptancePreservesFrozenPopulation(t *testing.T) {
	const work = "repeated-acceptance-watermark"
	f := oSeedReviewRoute(t, work, "workflow.break_fix", "repair")
	s := f.store
	defer s.Close()
	lane := reviewGateLane(t, "implementation")
	older := &WorkerJobBinding{JobID: "job:older-input", Revision: 1}
	latest := &WorkerJobBinding{JobID: "job:latest-input", Revision: 1}
	recordWorkerJobRevisionForTest(t, s, work, f.owner, older)
	recordWorkerJobRevisionForTest(t, s, work, f.owner, latest)
	dispatchJobBoundAttempt(t, s, work, "repair", "attempt:older", nil, f.owner, 0, "older-dispatch", older)
	if err := completeJobBoundAttemptForTest(s, work, "attempt:older", lane, older); err != nil {
		t.Fatal(err)
	}
	oDispatchJobBoundAttempt(t, s, work, "attempt:latest", nil, f.owner, 0, "latest-dispatch", latest)
	completeAndAcceptAttempt(t, s, work, "attempt:latest", lane, reviewGateAcceptor(work))
	git := integrationWorkflowWorktree(t, s, work)
	failed := integrationFailedVerifyFor(t, s, git, work, "project", "lease-repeat-acceptance")
	before, err := deriveWorkerFailureIntegration(t.Context(), s.db, work, integrationAttributionObservation(failed.LeaseID))
	if err != nil {
		t.Fatal(err)
	}
	if before == nil || before.Integration == nil {
		t.Fatalf("initial frozen identity=%+v; want an integration identity", before)
	}
	found := false
	for _, input := range before.Integration.InputResults {
		found = found || input.Job != nil && *input.Job == *latest
	}
	if !found {
		t.Fatalf("initial frozen population=%+v; want the latest accepted input", before.Integration.InputResults)
	}
	var firstRef, firstPayload string
	var firstSeq int64
	if err := s.db.QueryRow(`SELECT event_id,payload,seq FROM worker_attempt_dispositions WHERE work_id=? AND attempt_id='attempt:latest' AND action_id='accept_worker_result'`, work).Scan(&firstRef, &firstPayload, &firstSeq); err != nil {
		t.Fatal(err)
	}
	err = runVerdictActionAs(t, s, work, "accept_worker_result", mustJSONValue(map[string]any{
		"attempt_id": "attempt:latest", "attempt_epoch": latestStepStartEpoch(t, s, work, "repair"),
	}), 0, reviewGateAcceptor(work))
	if err != nil {
		t.Fatalf("repeated acceptance refused: %v", err)
	}
	after, err := deriveWorkerFailureIntegration(t.Context(), s.db, work, integrationAttributionObservation(failed.LeaseID))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("repeated acceptance changed the frozen input population: before=%+v after=%+v", before.Integration, after.Integration)
	}
	var retainedFirst, latestSeq int64
	if err := s.db.QueryRow(`SELECT first_acceptance_seq,seq FROM worker_attempt_dispositions WHERE work_id=? AND attempt_id='attempt:latest' AND action_id='accept_worker_result'`, work).Scan(&retainedFirst, &latestSeq); err != nil {
		t.Fatal(err)
	}
	if retainedFirst != firstSeq || latestSeq <= firstSeq {
		t.Fatalf("acceptance coordinates first=%d latest=%d; want first=%d and an advanced latest", retainedFirst, latestSeq, firstSeq)
	}
	var satisfiedSeq int64
	if err := s.db.QueryRow(`SELECT satisfied_seq FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=?`, work, latest.JobID, latest.Revision).Scan(&satisfiedSeq); err != nil {
		t.Fatal(err)
	}
	if satisfiedSeq != latestSeq {
		t.Fatalf("current job satisfaction=%d, want latest acceptance=%d", satisfiedSeq, latestSeq)
	}
	assertWorkerWindowsMigration143Parity(t, s)
	if _, err := s.db.Exec(`ALTER TABLE domain_events RENAME TO hidden_acceptance_events`); err != nil {
		t.Fatal(err)
	}
	withoutLog, err := deriveWorkerFailureIntegration(t.Context(), s.db, work, integrationAttributionObservation(failed.LeaseID))
	if err != nil || !reflect.DeepEqual(before, withoutLog) {
		t.Fatalf("log-blind frozen population=%+v err=%v; want %+v", withoutLog, err, before)
	}
	source := WorkerFailureSource{Kind: WorkerFailureSourceResult, Locator: firstRef, Digest: workerFailurePayloadDigest(firstPayload)}
	if err := verifyWorkerFailureIntegrationResultSourceTx(t.Context(), s.db, work, source, map[string]bool{firstRef: true}, firstSeq+1); err != nil {
		t.Fatalf("original acceptance source no longer verifies after repeated acceptance: %v", err)
	}
	if err := s.Transact(t.Context(), func(tx *Transaction) error {
		if err := enterWrite(t.Context(), tx.tx); err != nil {
			return err
		}
		if _, err := tx.tx.ExecContext(t.Context(), `DELETE FROM effect_receipts WHERE effect_seq=?`, firstSeq); err != nil {
			return err
		}
		return leaveWrite(t.Context(), tx.tx)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := deriveWorkerFailureIntegration(t.Context(), s.db, work, integrationAttributionObservation(failed.LeaseID)); failureKind(err) != KindInvariantViolation {
		t.Fatalf("missing first-acceptance receipt returned %v, want invariant violation", err)
	}
}

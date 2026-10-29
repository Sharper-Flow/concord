package store

import (
	"reflect"
	"testing"
)

func TestWorkerAcceptanceFreshKeyDisposition(t *testing.T) {
	const work = "acceptance-fresh-key"
	f := oSeedReviewRoute(t, work, "workflow.break_fix", "repair")
	s := f.store
	defer s.Close()
	lane := reviewGateLane(t, "implementation")
	older := &WorkerJobBinding{JobID: "job:older", Revision: 1}
	latest := &WorkerJobBinding{JobID: "job:latest", Revision: 1}
	recordWorkerJobRevisionForTest(t, s, work, f.owner, older)
	recordWorkerJobRevisionForTest(t, s, work, f.owner, latest)
	dispatchJobBoundAttempt(t, s, work, "repair", "attempt:older", nil, f.owner, 0, "older-dispatch", older)
	if err := completeJobBoundAttemptForTest(s, work, "attempt:older", lane, older); err != nil {
		t.Fatal(err)
	}
	oDispatchJobBoundAttempt(t, s, work, "attempt:latest", nil, f.owner, 0, "latest-dispatch", latest)
	if err := completeJobBoundAttemptForTest(s, work, "attempt:latest", lane, latest); err != nil {
		t.Fatal(err)
	}
	acceptor := reviewGateAcceptor(work)
	requestFixture := crossAttemptFixture{fixture: workflowReturnRouteFixture{owner: acceptor}, s: s, workID: work}
	request := requestFixture.acceptAttemptRequest(t, readWorkVersion(t, s, work), "attempt:latest", latestStepStartEpoch(t, s, work, "repair"), "first", 30)
	_, err := invokeWorkflowActionForCD0059(t.Context(), t, s, request)
	if err != nil {
		t.Fatalf("first acceptance: %v", err)
	}
	snapshot := outsideRepairDatabaseSnapshot(t, s)
	request = requestFixture.acceptAttemptRequest(t, readWorkVersion(t, s, work), "attempt:latest", latestStepStartEpoch(t, s, work, "repair"), "duplicate", 31)
	if _, err := invokeWorkflowActionForCD0059(t.Context(), t, s, request); failureKind(err) != KindIllegalLifecycleTransition {
		t.Fatalf("fresh-key second acceptance: %v; want illegal lifecycle transition", err)
	}
	if after := outsideRepairDatabaseSnapshot(t, s); after != snapshot {
		t.Fatal("fresh-key second acceptance changed durable state")
	}
}

func TestWorkerRepeatedAcceptancePreservesDependentJobReadiness(t *testing.T) {
	const work = "repeated-acceptance-dependent"
	f := oSeedReviewRoute(t, work, "workflow.break_fix", "repair")
	s := f.store
	defer s.Close()
	lane := reviewGateLane(t, "implementation")
	older := &WorkerJobBinding{JobID: "job:older-input", Revision: 1}
	input := &WorkerJobBinding{JobID: "job:input", Revision: 1}
	recordWorkerJobRevisionForTest(t, s, work, f.owner, older)
	recordWorkerJobRevisionForTest(t, s, work, f.owner, input)
	dispatchJobBoundAttempt(t, s, work, "repair", "attempt:older", nil, f.owner, 0, "older-dispatch", older)
	if err := completeJobBoundAttemptForTest(s, work, "attempt:older", lane, older); err != nil {
		t.Fatal(err)
	}
	oDispatchJobBoundAttempt(t, s, work, "attempt:input", nil, f.owner, 0, "input-dispatch", input)
	completeAndAcceptAttempt(t, s, work, "attempt:input", lane, reviewGateAcceptor(work))
	var firstRef string
	if err := s.db.QueryRow(`SELECT satisfied_result_ref FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=?`, work, input.JobID, input.Revision).Scan(&firstRef); err != nil {
		t.Fatal(err)
	}
	if firstRef == "" {
		t.Fatal("accepted input has no satisfaction reference")
	}
	if err := recordWorkerJobFieldsForTest(t, s, work, f.owner, map[string]any{
		"job_id": "job:dependent", "objective": "Consume the exact accepted input",
		"stopping_condition": "The dependent check passes", "checks": []string{"check dependent"},
		"path_scope":    []string{"internal/store/dependent"},
		"prerequisites": []WorkerJobPrerequisite{{JobID: input.JobID, Revision: input.Revision, ResultRef: firstRef}},
		"ready":         true, "readiness_evidence": []string{"evidence:coordinator-ready"},
	}); err != nil {
		t.Fatal(err)
	}
	views, err := readWorkerJobRevisions(t.Context(), s.db, work)
	if err != nil {
		t.Fatal(err)
	}
	var dependent WorkerJobBinding
	for _, view := range views {
		if view.Binding.JobID == "job:dependent" {
			if !view.Ready {
				t.Fatal("dependent is not ready before repeated acceptance")
			}
			dependent = view.Binding
		}
	}
	if dependent.Digest == "" {
		t.Fatal("dependent job was not recorded")
	}
	if err := runVerdictActionAs(t, s, work, "accept_worker_result", mustJSONValue(map[string]any{
		"attempt_id": "attempt:input", "attempt_epoch": latestStepStartEpoch(t, s, work, "repair"),
	}), 0, reviewGateAcceptor(work)); failureKind(err) != KindIllegalLifecycleTransition {
		t.Fatalf("repeated acceptance: %v; want illegal lifecycle transition", err)
	}
	var currentRef string
	if err := s.db.QueryRow(`SELECT satisfied_result_ref FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=?`, work, input.JobID, input.Revision).Scan(&currentRef); err != nil {
		t.Fatal(err)
	}
	if currentRef != firstRef {
		t.Errorf("repeated acceptance replaced the input satisfaction reference %q with %q", firstRef, currentRef)
	}
	acceptCurrentReviewForBoundJobForTest(t, workContextFixture{store: s, workID: work, owner: f.owner}, workerFixtureDirectory(t, s, work), "continue", dependent)
	packet := dispatchWorkerPacket(t, s, work, "repair", "attempt:dependent")
	packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, work, dependent, "implement")
	if err := dispatchJobPacketForTest(t, s, work, "attempt:dependent", f.owner, "dependent-dispatch", packet); err != nil {
		t.Fatalf("dependent dispatch after repeated acceptance: %v", err)
	}
	recordJobBoundWorkerDispatch(t, s, work, "attempt:dependent", &dependent)
}

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
	snapshot := outsideRepairDatabaseSnapshot(t, s)
	err = runVerdictActionAs(t, s, work, "accept_worker_result", mustJSONValue(map[string]any{
		"attempt_id": "attempt:latest", "attempt_epoch": latestStepStartEpoch(t, s, work, "repair"),
	}), 0, reviewGateAcceptor(work))
	if failureKind(err) != KindIllegalLifecycleTransition {
		t.Fatalf("fresh-key second acceptance: %v; want illegal lifecycle transition", err)
	}
	if after := outsideRepairDatabaseSnapshot(t, s); after != snapshot {
		t.Fatal("refused second acceptance changed durable state")
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
	if retainedFirst != firstSeq || latestSeq != firstSeq {
		t.Fatalf("acceptance coordinates first=%d latest=%d; want both=%d", retainedFirst, latestSeq, firstSeq)
	}
	var satisfiedSeq int64
	if err := s.db.QueryRow(`SELECT satisfied_seq FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=?`, work, latest.JobID, latest.Revision).Scan(&satisfiedSeq); err != nil {
		t.Fatal(err)
	}
	if satisfiedSeq != firstSeq {
		t.Fatalf("current job satisfaction=%d, want first acceptance=%d", satisfiedSeq, firstSeq)
	}
	assertWorkerWindowsMigration143Parity(t, s)
	assertEventLogAbsent(t, s)
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

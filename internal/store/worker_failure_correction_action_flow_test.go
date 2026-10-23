package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Full coordinator-boundary acceptance witnesses for the correction actions'
// failure attribution. The wiring witnesses beside this file
// derive the block and append the completion raw; these witnesses drive the
// actual admission route — WorkflowActionPreflight,
// AuthorizeWorkflowActionAtBoundaryTx, and ApplyWorkflowActionTx — for
// reject_worker_result and request_correction, citing an existing failed
// clean combined verify run beside accepted local jobs, and pin the
// informational semantics of the recorded block on rebuild.

// correctionActionWitness composes one work item with an acceptance recorded
// before the combined run, the failed clean run itself, an acceptance
// recorded after the run started, and finally the completed unaccepted
// result the correction dispositions — the newest dispatch at the step, so
// the rejection route admits it. The cited run's watermark partitions the
// accepted population: the pre-run acceptance is inside it, the post-run
// acceptance is excluded.
func correctionActionWitness(t *testing.T, workID string) (workflowReturnRouteFixture, *Store, *WorkerJobBinding, WorktreeVerifyResult) {
	t.Helper()
	fixture := tSeedReviewRoute(t, workID)
	s := fixture.store
	lane := reviewGateLane(t, "implementation")
	acceptor := reviewGateAcceptor(workID)

	jobBefore := &WorkerJobBinding{JobID: "job:" + workID + "-before", Revision: 1}
	recordWorkerJobRevisionForTest(t, s, workID, fixture.owner, jobBefore)
	attemptBefore := "attempt:" + workID + ":before"
	tDispatchJobBoundAttempt(t, s, workID, attemptBefore, nil, fixture.owner, "correction-flow-before-"+workID, jobBefore)
	completeAndAcceptAttempt(t, s, workID, attemptBefore, lane, acceptor)

	git := integrationWorkflowWorktree(t, s, workID)
	failed := integrationFailedVerifyFor(t, s, git, workID, "project", "lease-"+workID)

	jobAfter := &WorkerJobBinding{JobID: "job:" + workID + "-after", Revision: 1}
	recordWorkerJobRevisionForTest(t, s, workID, fixture.owner, jobAfter)
	attemptAfter := "attempt:" + workID + ":after"
	tDispatchJobBoundAttempt(t, s, workID, attemptAfter, nil, fixture.owner, "correction-flow-after-"+workID, jobAfter)
	completeAndAcceptAttempt(t, s, workID, attemptAfter, lane, acceptor)

	jobResult := &WorkerJobBinding{JobID: "job:" + workID + "-result", Revision: 1}
	recordWorkerJobRevisionForTest(t, s, workID, fixture.owner, jobResult)
	attemptResult := "attempt:" + workID + ":result"
	// Each implementation group opens on its own continue Review (CD-0242 D5).
	acceptCurrentReviewForTest(t, workContextFixture{store: s, workID: workID, owner: fixture.owner}, workerFixtureDirectory(t, s, workID), "continue")
	tDispatchJobBoundAttempt(t, s, workID, attemptResult, nil, fixture.owner, "correction-flow-result-"+workID, jobResult)
	if err := completeJobBoundAttemptForTest(s, workID, attemptResult, lane, jobResult); err != nil {
		t.Fatalf("complete the rejected attempt: %v", err)
	}

	return fixture, s, jobBefore, failed
}

// runCorrectionActionFullBoundary drives one correction action through the
// actual admission route and returns the execution result.
func runCorrectionActionFullBoundary(t *testing.T, s *Store, fixture workflowReturnRouteFixture, workID, actionID string, fields map[string]any) (WorkflowActionExecutionResult, error) {
	t.Helper()
	payload := mustJSONValue(fields)
	return invokeWorkflowActionForCD0059(context.Background(), t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: readWorkVersion(t, s, workID), ActionID: actionID, Payload: payload,
		SessionWorktree: dispatchSessionWorktree(t, s, workID), Actor: fixture.operator,
		AcceptedInputsDigest: cd0059TestDigest(t, actionID+"-attribution-inputs"), IdempotencyIdentity: actionID + "-attribution-op",
		OperationID: "op-" + actionID + "-attribution", PrincipalRef: fixture.operator.PrincipalRef,
		Tool: "concord_work_transition", IdempotencyKey: actionID + "-attribution-key", RequestID: "req-" + actionID + "-attribution",
		AcceptedScope: `{}`, ContractDigest: testManifestDigest, Now: time.Unix(45, 0).UTC(),
	})
}

// readStoredCorrectionCompletion reads the newest stored action_completed
// payload of one correction action as its raw JSON members.
func readStoredCorrectionCompletion(t *testing.T, s *Store, workID, actionID string) map[string]json.RawMessage {
	t.Helper()
	var raw string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')=? ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionCompleted, actionID).Scan(&raw); err != nil {
		t.Fatalf("read the stored %s completion: %v", actionID, err)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &members); err != nil {
		t.Fatal(err)
	}
	return members
}

// storedCorrectionPayload re-encodes the newest stored completion payload.
func storedCorrectionPayload(t *testing.T, s *Store, workID, actionID string) string {
	t.Helper()
	var raw string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')=? ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionCompleted, actionID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

// attributionVerdictAndEvidenceCounts counts the verdict and evidence events
// one work carries, so the informational assertion compares before and after.
func attributionVerdictAndEvidenceCounts(t *testing.T, s *Store, workID string) (int64, int64) {
	t.Helper()
	var verdicts, evidence int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkflowVerdictRecorded).Scan(&verdicts); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkflowEvidenceBound).Scan(&evidence); err != nil {
		t.Fatal(err)
	}
	return verdicts, evidence
}

// assertCorrectionAttributionInformational pins the informational semantics:
// the recorded block minted no verdict or evidence authority, and the
// rejected attempt's job revision stays unsatisfied.
func assertCorrectionAttributionInformational(t *testing.T, s *Store, workID, jobID string, verdictsBefore, evidenceBefore int64) {
	t.Helper()
	var satisfied string
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(satisfied_result_ref,'') FROM worker_job_revisions WHERE work_id=? AND job_id=?`, workID, jobID).Scan(&satisfied); err != nil {
		t.Fatal(err)
	}
	if satisfied != "" {
		t.Fatalf("job %s satisfied_result_ref = %q, want the correction attribution to satisfy nothing", jobID, satisfied)
	}
	verdictsAfter, evidenceAfter := attributionVerdictAndEvidenceCounts(t, s, workID)
	if verdictsAfter != verdictsBefore || evidenceAfter != evidenceBefore {
		t.Fatalf("the correction attribution moved verdicts %d->%d or evidence %d->%d; the block is informational and mints no acceptance authority", verdictsBefore, verdictsAfter, evidenceBefore, evidenceAfter)
	}
}

// assertIntegrationBlockMatchesTheCitedRun reads the stored block and proves
// it against the cited run: the canonical operation reference, the retained
// result digest, and exactly the pre-watermark accepted population.
func assertIntegrationBlockMatchesTheCitedRun(t *testing.T, s *Store, workID, actionID, leaseID string, jobBefore *WorkerJobBinding) WorkerFailureAttribution {
	t.Helper()
	members := readStoredCorrectionCompletion(t, s, workID, actionID)
	raw, present := members["failure_attribution"]
	if !present {
		t.Fatalf("the stored %s completion records no failure_attribution", actionID)
	}
	var block WorkerFailureAttribution
	if err := json.Unmarshal(raw, &block); err != nil {
		t.Fatal(err)
	}
	if block.Origin != WorkerFailureAttributionRecorded || block.Identity == nil || block.Identity.Kind != WorkerFailureIdentityIntegration || block.Identity.Integration == nil {
		t.Fatalf("stored attribution = %+v, want the recorded integration identity", block)
	}
	integration := block.Identity.Integration
	if integration.NativeRun.Locator != worktreeVerifyOperationRef(leaseID) || integration.NativeRun.Digest != integrationRetainedResultDigest(t, s, leaseID) {
		t.Fatalf("native run = %+v, want the cited run's canonical reference and retained digest", integration.NativeRun)
	}
	if len(integration.InputResults) != 1 || integration.InputResults[0].Job == nil || *integration.InputResults[0].Job != *jobBefore {
		t.Fatalf("input population = %+v, want exactly the acceptance below the watermark (%s@%d)", integration.InputResults, jobBefore.JobID, jobBefore.Revision)
	}
	return block
}

// TestCorrectionAttributionFullActionRejectWitness drives the exact full
// reject_worker_result action with the integration observation: the stored
// completion records the core-derived block whose population is exactly the
// acceptance below the cited run's acquire watermark — the acceptance
// recorded after the run started is excluded — the block mints no acceptance
// authority, and rebuild reproduces it exactly while a corrupted population
// refuses at the owning join.
func TestCorrectionAttributionFullActionRejectWitness(t *testing.T) {
	t.Parallel()
	const workID = "correction-full-reject"
	fixture, s, jobBefore, failed := correctionActionWitness(t, workID)
	defer s.Close()
	verdictsBefore, evidenceBefore := attributionVerdictAndEvidenceCounts(t, s, workID)

	fields := map[string]any{
		"attempt_id": "attempt:" + workID + ":result", "attempt_epoch": latestStepStartEpoch(t, s, workID, "repair"),
		"diagnosis":           "the combined run failed its compatibility check",
		"strategy":            "repair the contributing job and rerun the combined verification",
		"predicate_ids":       []string{"predicate:return-route"},
		"evidence_refs":       []string{"evidence:return-route-verification"},
		"failure_attribution": json.RawMessage(mustJSONValue(attributionIntegrationObservation(t, s, failed.LeaseID))),
	}
	if _, err := runCorrectionActionFullBoundary(t, s, fixture, workID, "reject_worker_result", fields); err != nil {
		t.Fatalf("the full reject_worker_result action refused: %v", err)
	}

	block := assertIntegrationBlockMatchesTheCitedRun(t, s, workID, "reject_worker_result", failed.LeaseID, jobBefore)
	if block.Identity.Integration.InputsUnavailable {
		t.Fatal("a recorded watermark names its population available")
	}
	assertCorrectionAttributionInformational(t, s, workID, "job:"+workID+"-result", verdictsBefore, evidenceBefore)

	// Rebuild folds the recorded block against the same retained lease from
	// its staged operational snapshot and reproduces the exact completion.
	stored := storedCorrectionPayload(t, s, workID, "reject_worker_result")
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("rebuild with the recorded correction attribution: %v", err)
	}
	if rebuilt := storedCorrectionPayload(t, s, workID, "reject_worker_result"); rebuilt != stored {
		t.Fatal("rebuild changed the recorded correction attribution payload")
	}

	// A corrupted input population refuses the rebuild at the owning join.
	members := readStoredCorrectionCompletion(t, s, workID, "reject_worker_result")
	block.Identity.Integration.InputResults[0].Job.Digest = testDigestC
	members["failure_attribution"] = mustJSONValue(block)
	corruptLoggedWorkflowPayload(t, s, workID, "reject_worker_result", members)
	if err := RebuildFromLog(context.Background(), s); err == nil || !strings.Contains(err.Error(), "integration identity does not equal") {
		t.Fatalf("rebuild did not refuse the corrupted correction population: %v", err)
	}
}

// TestCorrectionAttributionFullActionRequestCorrectionWitness drives the
// exact full request_correction action on the live execution-Review route:
// the break-fix item at repair holds one accepted implementation delivery,
// the failed combined run, and an accepted Review that requires correction
// (CD-0242 D5). The request crosses the actual admission boundary —
// preflight, AuthorizeWorkflowActionAtBoundaryTx, ApplyWorkflowActionTx under
// the coordinator's own authority — the stored completion records the derived
// integration block, and rebuild reproduces it exactly while a corrupted
// population refuses.
func TestCorrectionAttributionFullActionRequestCorrectionWitness(t *testing.T) {
	t.Parallel()
	const workID = "correction-full-correction"
	fixture := tSeedReviewRoute(t, workID)
	s := fixture.store
	defer s.Close()
	lane := reviewGateLane(t, "implementation")

	job := &WorkerJobBinding{JobID: "job:" + workID + "-delivery", Revision: 1}
	recordWorkerJobRevisionForTest(t, s, workID, fixture.owner, job)
	attemptID := "attempt:" + workID + ":delivery"
	tDispatchJobBoundAttempt(t, s, workID, attemptID, nil, fixture.owner, "correction-flow-delivery-"+workID, job)
	completeAndAcceptAttempt(t, s, workID, attemptID, lane, reviewGateAcceptor(workID))

	// The failed clean combined run the correction cites, recorded on the
	// work's own claimed worktree, then the Review that requires correction.
	git := integrationWorkflowWorktree(t, s, workID)
	failed := integrationFailedVerifyFor(t, s, git, workID, "project", "lease-"+workID)
	acceptCurrentReviewForTest(t, workContextFixture{store: s, workID: workID, owner: fixture.owner}, workerFixtureDirectory(t, s, workID), "correction_required")

	fields := map[string]any{
		"diagnosis":           "the combined run failed its compatibility check",
		"strategy":            "repair the contributing job and rerun the combined verification",
		"predicate_ids":       []string{"predicate:return-route"},
		"evidence_refs":       []string{"evidence:return-route-verification"},
		"failure_attribution": json.RawMessage(mustJSONValue(attributionIntegrationObservation(t, s, failed.LeaseID))),
	}
	payload := mustJSONValue(fields)
	preflight := WorkflowActionPreflightRequest{
		WorkID: workID, ExpectedVersion: readWorkVersion(t, s, workID), ActionID: "request_correction",
		Payload: payload, Actor: fixture.owner,
	}
	execution := WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: readWorkVersion(t, s, workID), ActionID: "request_correction", Payload: payload,
		Actor:                fixture.owner,
		AcceptedInputsDigest: cd0059TestDigest(t, "request-correction-attribution-inputs"), IdempotencyIdentity: "request-correction-attribution-op",
		OperationID: "op-request-correction-attribution", PrincipalRef: fixture.owner.PrincipalRef,
		Tool: "concord_work_transition", IdempotencyKey: "request-correction-attribution-key", RequestID: "req-request-correction-attribution",
		AcceptedScope: `{}`, ContractDigest: testManifestDigest, Now: time.Unix(45, 0).UTC(),
	}
	err := AuthorizeWorkflowActionAtBoundaryTx(context.Background(), s, BuiltinWorkflowRegistry(), preflight, nil, func(tx *Transaction) error {
		_, inner := ApplyWorkflowActionTx(context.Background(), tx, BuiltinWorkflowRegistry(), execution)
		return inner
	})
	if err != nil {
		t.Fatalf("the full request_correction action refused: %v", err)
	}

	members := readStoredCorrectionCompletion(t, s, workID, "request_correction")
	raw, present := members["failure_attribution"]
	if !present {
		t.Fatal("the stored request_correction completion records no failure_attribution")
	}
	var block WorkerFailureAttribution
	if err := json.Unmarshal(raw, &block); err != nil {
		t.Fatal(err)
	}
	if block.Origin != WorkerFailureAttributionRecorded || block.Identity == nil || block.Identity.Kind != WorkerFailureIdentityIntegration || block.Identity.Integration == nil {
		t.Fatalf("stored attribution = %+v, want the recorded integration identity", block)
	}
	integration := block.Identity.Integration
	if integration.NativeRun.Locator != worktreeVerifyOperationRef(failed.LeaseID) || integration.NativeRun.Digest != integrationRetainedResultDigest(t, s, failed.LeaseID) {
		t.Fatalf("native run = %+v, want the cited run's canonical reference and retained digest", integration.NativeRun)
	}
	// The implementation delivery is the one acceptance below the run's
	// watermark; the Review accepted after the run is outside it.
	if len(integration.InputResults) != 1 || integration.InputsUnavailable {
		t.Fatalf("input population = (unavailable=%v results=%+v), want exactly the accepted delivery below the watermark", integration.InputsUnavailable, integration.InputResults)
	}
	var acceptedRef string
	if err := s.DatabaseForTesting().QueryRow(`SELECT event_id FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='accept_worker_result' ORDER BY seq LIMIT 1`, workID, WorkflowActionCompleted).Scan(&acceptedRef); err != nil {
		t.Fatal(err)
	}
	if integration.InputResults[0].ResultRef != acceptedRef {
		t.Fatalf("input result ref = %q, want the work's accepted delivery %q", integration.InputResults[0].ResultRef, acceptedRef)
	}

	stored := storedCorrectionPayload(t, s, workID, "request_correction")
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("rebuild with the recorded correction attribution: %v", err)
	}
	if rebuilt := storedCorrectionPayload(t, s, workID, "request_correction"); rebuilt != stored {
		t.Fatal("rebuild changed the recorded correction attribution payload")
	}

	block.Identity.Integration.InputResults = []WorkerFailureInputResult{{Job: &WorkerJobBinding{JobID: "job:smuggled", Revision: 1, Digest: testDigestC}, ResultRef: "evt:smuggled-acceptance"}}
	members["failure_attribution"] = mustJSONValue(block)
	corruptLoggedWorkflowPayload(t, s, workID, "request_correction", members)
	if err := RebuildFromLog(context.Background(), s); err == nil || !strings.Contains(err.Error(), "integration identity does not equal") {
		t.Fatalf("rebuild did not refuse the corrupted request_correction population: %v", err)
	}
}

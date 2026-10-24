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
	err = runVerdictActionAs(t, s, work, "accept_worker_result", mustJSONValue(map[string]any{
		"attempt_id": "attempt:latest", "attempt_epoch": latestStepStartEpoch(t, s, work, "repair"),
	}), 0, reviewGateAcceptor(work))
	t.Logf("repeated acceptance returned %v", err)
	after, err := deriveWorkerFailureIntegration(t.Context(), s.db, work, integrationAttributionObservation(failed.LeaseID))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("repeated acceptance changed the frozen input population: before=%+v after=%+v", before.Integration, after.Integration)
	}
}

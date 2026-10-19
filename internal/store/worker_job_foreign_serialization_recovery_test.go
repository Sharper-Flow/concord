package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The foreign serialization breach (CD-0225 D1) refuses both dispositions of
// the completed attempt: the acceptance and the split replacement. Its
// recovery rejects the completed predecessor attempt first — the already
// accepted foreign job's disposition is immutable and the recovery must
// never name it — then splits without replaced_attempt.
func TestWorkerJobForeignSerializationRecovery(t *testing.T) {
	const workID = "size-foreign-serial-recovery"
	fixture := sizeDispatchFixture(t, workID)
	t.Cleanup(func() { fixture.store.Close() })
	s := workerJobSizeStore(fixture)
	lane := reviewGateLane(t, "implementation")
	const breachPath = "internal/store/worker_jobs.go"
	base := testJobSizeBaseCommit()
	fields := workerJobSizeEditingFields(t, workID, fixture)
	fields["production_size_max"] = 10
	job := &WorkerJobBinding{JobID: "job:serial-correction", Revision: 1}
	recordEditingJobForTest(t, fixture, workID, fields, job)
	recordSizeDispatchAttempt(t, fixture, workID, "attempt:first", *job)

	// The foreign job inventories this job's own measured production path;
	// its acceptance after this job's first implementation dispatch is the
	// serialization breach.
	other := workerJobSizeEditingFields(t, workID, fixture)
	other["measurement_basis"].(map[string]any)["paths"] = []map[string]any{{"path": breachPath, "class": "production"}}
	otherJob := &WorkerJobBinding{JobID: "job:serial-foreign", Revision: 1}
	recordEditingJobForTest(t, fixture, workID, other, otherJob)
	complete := func(attemptID string, binding *WorkerJobBinding, tree string, count int64, paths ...WorkerObservedPath) {
		t.Helper()
		recordSizeDispatchAuthorization(t, s, workID, attemptID, *binding)
		if err := appendJobBoundWorkerDispatch(t, s, workID, attemptID, binding); err != nil {
			t.Fatalf("record implementation dispatch %s: %v", attemptID, err)
		}
		completed := reportBindingCompletion(t, s, workID, attemptID, lane, binding, func(payload *WorkerCompletedPayload) {
			payload.ObservedDelivery = &WorkerObservedDelivery{Commit: tree, State: "clean", Base: base, Paths: paths}
			payload.ProductionSize = &WorkerProductionSize{Base: base, Tree: tree, Count: count}
		})
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{completed}}); err != nil {
			t.Fatalf("record the completed report of %s: %v", attemptID, err)
		}
	}
	complete("attempt:serial-foreign", otherJob, strings.Repeat("4", 40), 90, WorkerObservedPath{Path: breachPath, Added: 90})
	if err := runVerdictActionAs(t, s, workID, "accept_worker_result", mustJSONValue(map[string]any{"attempt_id": "attempt:serial-foreign", "attempt_epoch": 1}), 0, reviewGateAcceptor(workID)); err != nil {
		t.Fatalf("accept the foreign job's work: %v", err)
	}
	tree := strings.Repeat("5", 40)
	complete("attempt:serial-correction", job, tree, 92, WorkerObservedPath{Path: breachPath, Added: 92})
	a := sizeAcceptance{fixture: fixture, s: s, workID: workID, attemptID: "attempt:serial-correction", job: *job, tree: tree}

	// The acceptance of the completed attempt refuses at the breach, and the
	// recovery names the reachable sequence without touching the accepted
	// foreign job.
	err := a.accept(t, nil)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || !strings.Contains(failure.Detail, "which a correction serializes") {
		t.Fatalf("the foreign serialization breach = %v; want the breach refusal", err)
	}
	if !strings.Contains(failure.RecoveryAction, "reject_worker_result") || !strings.Contains(failure.RecoveryAction, "without replaced_attempt") {
		t.Fatalf("breach recovery %q does not reject the completed attempt and split without replaced_attempt", failure.RecoveryAction)
	}
	if strings.Contains(failure.RecoveryAction, otherJob.JobID) {
		t.Fatalf("breach recovery %q directs action at accepted foreign job %s, whose disposition is immutable", failure.RecoveryAction, otherJob.JobID)
	}

	// The split replacement of the same completed attempt refuses at the
	// same breach: one owner, both callers.
	if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, replacementSplitFields(t, a, nil)); err == nil || !strings.Contains(err.Error(), "which a correction serializes") {
		t.Fatalf("the split replacement through the breach = %v; want the same refusal", err)
	}

	// The recovery is reachable: reject the completed predecessor attempt,
	// then split without replaced_attempt.
	if err := runVerdictActionAs(t, s, workID, "reject_worker_result", mustJSONValue(map[string]any{
		"attempt_id": a.attemptID, "attempt_epoch": 1,
		"diagnosis":     "the accepted foreign work entered the measured paths",
		"strategy":      "split the job on a base that separates the accepted change",
		"predicate_ids": []string{"predicate:return-route"}, "evidence_refs": []string{"evidence:serial-breach"},
	}), 0, reviewGateAcceptor(workID)); err != nil {
		t.Fatalf("reject the completed predecessor attempt: %v", err)
	}
	if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, splitFields(t, workID, fixture, *job, func(successors []map[string]any, allocation []map[string]any) ([]map[string]any, []map[string]any) {
		for _, successor := range successors {
			successor["production_size_max"] = 5
		}
		return successors, allocation
	})); err != nil {
		t.Fatalf("the recovery split without replaced_attempt: %v", err)
	}
	successor := latestSplitBinding(t, fixture, workID, "job:successor-a")
	if successor.JobID != "job:successor-a" || successor.Revision < 1 {
		t.Fatalf("recovery split successor = %s revision %d; want job:successor-a at a recorded revision", successor.JobID, successor.Revision)
	}
}

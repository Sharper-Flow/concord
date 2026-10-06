package store

import (
	"context"
	"regexp"
	"testing"
)

// The five deterministic witnesses the independent review preserved as
// probes (/tmp/opencode/con836-review-probe_test.go): each names one approved
// obligation the passing branch tests did not establish. They stay as
// repository tests so the obligations they prove cannot regress silently.

// A rejected completed result leaves its dispatched job unresolved exactly as
// a recorded failure does (CD-0205 refining CD-0164 D1): the rejection joins
// the all-dispatch window, and a later acceptance of an unrelated job neither
// closes it nor restores the failed job's budget.
func TestReviewRejectedJobWindow(t *testing.T) {
	const id = "review-rejected-job"
	f := seedWorkflowReturnRouteFixture(t, id, "workflow.break_fix", "repair")
	s := f.store
	defer s.Close()
	lane := BuiltinLaneDefinitions()[0]
	acceptor := reviewGateAcceptor(id)
	job := &WorkerJobBinding{JobID: "job:corrective-rejected", Revision: 1}
	attempt := "attempt:" + id + ":1"
	dispatchJobBoundAttempt(t, s, id, "repair", attempt, nil, f.owner, 0, "review-reject-1", job)
	if err := completeJobBoundAttemptForTest(s, id, attempt, lane, job); err != nil {
		t.Fatal(err)
	}
	epoch := latestStepStartEpoch(t, s, id, "repair")
	if err := runVerdictActionAs(t, s, id, "reject_worker_result", mustJSONValue(map[string]any{
		"attempt_id": attempt, "attempt_epoch": epoch, "diagnosis": "the corrective job remains unsatisfied", "strategy": "repair this job",
		"predicate_ids": []string{"predicate:return-route"}, "evidence_refs": []string{"evidence:review-reject"},
	}), 0, acceptor); err != nil {
		t.Fatal(err)
	}
	pin := issue1013Pin(t, s, id)
	unrelated := &WorkerJobBinding{JobID: "job:unrelated-success", Revision: 1}
	issue1013StartRepair(t, s, id, f.owner, pin.Version, epoch+1)
	pin = issue1013Pin(t, s, id)
	dispatchJobBoundAttempt(t, s, id, "repair", "attempt:"+id+":2", pin.Correction, f.owner, pin.Version, "review-reject-2", unrelated)
	completeAndAcceptAttempt(t, s, id, "attempt:"+id+":2", lane, acceptor)
	if count := jobWindowCorrectionCount(t, s, id); count != 2 {
		t.Fatalf("unrelated acceptance reset rejected job window: count=%d want 2", count)
	}
}

// A recorded worker-job revision admits the same predicate-id shape the lane
// report schema admits: one-or-more characters after the predicate: prefix,
// not an eleven-character minimum.
func TestReviewWorkerJobPredicateShape(t *testing.T) {
	id := "predicate:primary"
	if !regexp.MustCompile(`^predicate:[A-Za-z0-9][A-Za-z0-9._:-]*$`).MatchString(id) {
		t.Fatal("probe ID is invalid")
	}
	if !workerJobPredicatePattern.MatchString(id) {
		t.Fatalf("valid contract/report predicate %s is refused by worker-job predicate validator", id)
	}
}

// A delivery assertion may not exit a phase while a recorded required job is
// unsatisfied and no evidence binds the jobs' integration (CD-0205 D3):
// record_delivery refuses instead of asserting whole-work delivery.
func TestReviewUnsatisfiedJobPhaseExit(t *testing.T) {
	const id = "review-unsatisfied-job-exit"
	f := seedWorkflowReturnRouteFixture(t, id, "workflow.break_fix", "repair")
	s := f.store
	defer s.Close()
	reviewGateStartStep(t, s, id, "repair", "start_repair", f.owner)
	job := &WorkerJobBinding{JobID: "job:required-but-unfinished", Revision: 1}
	recordWorkerJobRevisionForTest(t, s, id, f.owner, job)
	err := runVerdictActionAs(t, s, id, "record_delivery", mustJSONValue(map[string]any{"delivery_artifact": "artifact:partial", "delivery_state": "asserted"}), 0, f.owner)
	if err == nil {
		t.Fatalf("record_delivery advanced to %s with an unsatisfied recorded job and no integration binding", issue1013Pin(t, s, id).Step)
	}
}

// A recorded revision keeps the parent contract authority it was recorded
// under (CD-0205 D1): after the operator supersedes the contract, the stale
// revision is no longer ready and the core refuses to authorize its dispatch
// until a new revision is recorded under the active contract.
func TestReviewJobAuthoritySurvivesSupersession(t *testing.T) {
	const id = "review-stale-job-authority"
	f := seedWorkflowReturnRouteFixture(t, id, "workflow.break_fix", "repair")
	s := f.store
	defer s.Close()
	job := &WorkerJobBinding{JobID: "job:old-authority", Revision: 1}
	recordWorkerJobRevisionForTest(t, s, id, f.owner, job)
	rescanned := driftStaleRegistryFixture(t, s)
	if err := runIssue933OperatorAction(t, s, id, "supersede_contract", registryRepinSuccessorPayload(2, rescanned, id, 1, "root"), f.owner, f.operator); err != nil {
		t.Fatal(err)
	}
	ready, err := s.ReadyWorkerJobRevisions(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 0 {
		t.Fatalf("superseded-authority revisions stayed ready: %+v", ready[0].Binding)
	}
	// The dispatch boundary refuses the stale revision even when a caller
	// builds the packet from the recorded revision directly.
	packet := dispatchWorkerPacket(t, s, id, "repair", "attempt:"+id+":stale")
	packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, id, *job)
	if err := dispatchJobPacketForTest(t, s, id, "attempt:"+id+":stale", f.owner, "review-stale-dispatch", packet); err == nil {
		t.Fatalf("core authorized an old-contract job after contract supersession")
	} else {
		t.Logf("old job dispatch refused: %v", err)
	}
}

// An acceptance discharges a failed job's corrective obligation only when the
// accepted revision carries the same recorded obligation (CD-0205 D4): a
// rewritten revision under the same job identity with an unrelated objective
// cannot close the original corrective window or reset its budget.
func TestReviewRewrittenRevisionResetsFailedJob(t *testing.T) {
	const id = "review-rewritten-correction"
	f := seedWorkflowReturnRouteFixture(t, id, "workflow.break_fix", "repair")
	s := f.store
	defer s.Close()
	acceptor := reviewGateAcceptor(id)
	job := &WorkerJobBinding{JobID: "job:original-corrective", Revision: 1}
	dispatchJobBoundAttempt(t, s, id, "repair", "attempt:"+id+":1", nil, f.owner, 0, "review-rewritten-1", job)
	failWorkerAttemptWithKind(t, s, id, "attempt:"+id+":1", "corrective objective remains unsatisfied")
	applyRecordWorkerFailureForTest(t, s, id, f.owner, "attempt:"+id+":1", latestStepStartEpoch(t, s, id, "repair"), readWorkVersion(t, s, id), "review-rewritten-failure")
	pin := issue1013Pin(t, s, id)
	issue1013StartRepair(t, s, id, f.owner, pin.Version, latestStepStartEpoch(t, s, id, "repair")+1)
	if err := recordWorkerJobActionForTest(t, s, id, f.owner, map[string]any{
		"job_id": job.JobID, "objective": "Perform an unrelated documentation task, not the failed repair",
		"stopping_condition": "Report the document title", "checks": []string{}, "ready": true, "readiness_evidence": []string{"evidence:new-document-task"},
	}); err != nil {
		t.Fatal(err)
	}
	revisions, err := s.WorkerJobRevisions(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	replacement := revisions[len(revisions)-1].Binding
	pin = issue1013Pin(t, s, id)
	dispatchJobBoundAttempt(t, s, id, "repair", "attempt:"+id+":2", pin.Correction, f.owner, pin.Version, "review-rewritten-2", &replacement)
	completeAndAcceptAttempt(t, s, id, "attempt:"+id+":2", BuiltinLaneDefinitions()[0], acceptor)
	if count := jobWindowCorrectionCount(t, s, id); count != 2 {
		t.Fatalf("acceptance of unrelated revised objective erased failed corrective job budget: count=%d want 2", count)
	}
}

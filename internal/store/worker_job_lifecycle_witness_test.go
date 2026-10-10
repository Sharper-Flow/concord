package store

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Deterministic witnesses of the approved worker-job obligations (CD-0205),
// each derived from an independent review probe: one bounded objective per
// dispatch, one disposition per local acceptance, and an unresolved failed
// job whose window, count, and exact retry approval survive unrelated
// success. They stay as repository tests so the obligations they prove
// cannot regress silently.

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
	// The rewritten revision keeps its unrelated objective; the oracle the
	// pin requires travels with it as fixture bookkeeping, not as the
	// obligation the acceptance discharges.
	if err := recordWorkerJobFieldsForTest(t, s, id, f.owner, map[string]any{
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

// A held local acceptance of an unrelated job resets no same-step wall
// (CD-0205 D4 refining CD-0164 D2): the failed job's obligation stays
// unresolved, so the wall the failed dispatch opened keeps its count and its
// exact retry approval.
func TestWitnessUnrelatedLocalAcceptHoldsSameStepWall(t *testing.T) {
	const id = "witness-unrelated-local-accept-wall"
	f := seedWorkflowReturnRouteFixture(t, id, "workflow.break_fix", "repair")
	s := f.store
	defer s.Close()
	job := &WorkerJobBinding{JobID: "job:witness-failed", Revision: 1}
	dispatchJobBoundAttempt(t, s, id, "repair", "attempt:"+id+":1", nil, f.owner, 0, "witness-wall-1", job)
	failWorkerAttemptWithKind(t, s, id, "attempt:"+id+":1", "fallback blocked")
	applyRecordWorkerFailureForTest(t, s, id, f.owner, "attempt:"+id+":1", latestStepStartEpoch(t, s, id, "repair"), readWorkVersion(t, s, id), "witness-wall-fail")
	before, err := workflowNonProgressAttemptCount(context.Background(), s.DatabaseForTesting(), id, "witness")
	if err != nil || before != 1 {
		t.Fatalf("before=%d err=%v, want the one failed dispatch counted", before, err)
	}
	pin := issue1013Pin(t, s, id)
	issue1013StartRepair(t, s, id, f.owner, pin.Version, latestStepStartEpoch(t, s, id, "repair")+1)
	pin = issue1013Pin(t, s, id)
	unrelated := &WorkerJobBinding{JobID: "job:witness-unrelated", Revision: 1}
	dispatchJobBoundAttempt(t, s, id, "repair", "attempt:"+id+":2", pin.Correction, f.owner, pin.Version, "witness-wall-2", unrelated)
	completeAndAcceptAttempt(t, s, id, "attempt:"+id+":2", BuiltinLaneDefinitions()[0], reviewGateAcceptor(id))
	after, err := workflowNonProgressAttemptCount(context.Background(), s.DatabaseForTesting(), id, "witness")
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("unrelated local accept reset the same-step failed count: before=%d after=%d", before, after)
	}
}

// A held local acceptance that discharges the last unresolved corrective
// obligation resets the same-step wall (CD-0164 D2 through CD-0205 D4): the
// correction window and the wall the failed dispatch opened share one reset
// derivation, so the satisfying acceptance that closes the window also frees
// the step's dispatch budget. The wall no longer counts the failed dispatch
// the satisfied job owes.
func TestWitnessSatisfyingLocalAcceptResetsSameStepWall(t *testing.T) {
	const id = "witness-satisfying-local-accept-wall"
	f := seedWorkflowReturnRouteFixture(t, id, "workflow.break_fix", "repair")
	s := f.store
	defer s.Close()
	job := &WorkerJobBinding{JobID: "job:witness-satisfying", Revision: 1}
	dispatchJobBoundAttempt(t, s, id, "repair", "attempt:"+id+":1", nil, f.owner, 0, "witness-satisfy-1", job)
	failWorkerAttemptWithKind(t, s, id, "attempt:"+id+":1", "fallback blocked")
	applyRecordWorkerFailureForTest(t, s, id, f.owner, "attempt:"+id+":1", latestStepStartEpoch(t, s, id, "repair"), readWorkVersion(t, s, id), "witness-satisfy-fail")
	before, err := workflowNonProgressAttemptCount(context.Background(), s.DatabaseForTesting(), id, "witness")
	if err != nil || before != 1 {
		t.Fatalf("before=%d err=%v, want the one failed dispatch counted", before, err)
	}
	pin := issue1013Pin(t, s, id)
	issue1013StartRepair(t, s, id, f.owner, pin.Version, latestStepStartEpoch(t, s, id, "repair")+1)
	pin = issue1013Pin(t, s, id)
	// A later revision of the same job identity carries the same recorded
	// obligation, so its acceptance discharges the unresolved one.
	satisfying := &WorkerJobBinding{JobID: job.JobID, Revision: 2}
	dispatchJobBoundAttempt(t, s, id, "repair", "attempt:"+id+":2", pin.Correction, f.owner, pin.Version, "witness-satisfy-2", satisfying)
	completeAndAcceptAttempt(t, s, id, "attempt:"+id+":2", BuiltinLaneDefinitions()[0], reviewGateAcceptor(id))
	if pin := issue1013Pin(t, s, id); pin.Correction != nil {
		t.Fatalf("satisfying acceptance left the correction window open: %#v", pin.Correction)
	}
	after, err := workflowNonProgressAttemptCount(context.Background(), s.DatabaseForTesting(), id, "witness")
	if err != nil {
		t.Fatal(err)
	}
	if after != 0 {
		t.Fatalf("satisfying local accept kept the same-step failed count: before=%d after=%d, want 0", before, after)
	}
}

// A rewritten revision of a failed job's identity consumes no correction
// authority (CD-0205 D4): the acceptance of an unrelated obligation leaves
// the window open and the correction record bound to the exact failed
// attempt, so the retry approval stays exact and readable.
func TestWitnessRewrittenJobKeepsExactRetryBinding(t *testing.T) {
	const id = "witness-rewritten-job-binding"
	f := seedWorkflowReturnRouteFixture(t, id, "workflow.break_fix", "repair")
	s := f.store
	defer s.Close()
	job := &WorkerJobBinding{JobID: "job:witness-corrective", Revision: 1}
	dispatchJobBoundAttempt(t, s, id, "repair", "attempt:"+id+":1", nil, f.owner, 0, "witness-binding-1", job)
	failWorkerAttemptWithKind(t, s, id, "attempt:"+id+":1", "fallback blocked")
	applyRecordWorkerFailureForTest(t, s, id, f.owner, "attempt:"+id+":1", latestStepStartEpoch(t, s, id, "repair"), readWorkVersion(t, s, id), "witness-binding-fail")
	pin := issue1013Pin(t, s, id)
	issue1013StartRepair(t, s, id, f.owner, pin.Version, latestStepStartEpoch(t, s, id, "repair")+1)
	// The rewritten revision keeps its unrelated objective; the oracle the
	// pin requires travels with it as fixture bookkeeping, not as the
	// obligation the acceptance discharges.
	if err := recordWorkerJobFieldsForTest(t, s, id, f.owner, map[string]any{
		"job_id": job.JobID, "objective": "An unrelated rewritten task", "stopping_condition": "An unrelated outcome",
		"ready": true, "readiness_evidence": []string{"evidence:witness-ready"},
	}); err != nil {
		t.Fatal(err)
	}
	views, err := s.WorkerJobRevisions(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	replacement := views[len(views)-1].Binding
	pin = issue1013Pin(t, s, id)
	dispatchJobBoundAttempt(t, s, id, "repair", "attempt:"+id+":2", pin.Correction, f.owner, pin.Version, "witness-binding-2", &replacement)
	completeAndAcceptAttempt(t, s, id, "attempt:"+id+":2", BuiltinLaneDefinitions()[0], reviewGateAcceptor(id))
	if n := jobWindowCorrectionCount(t, s, id); n != 2 {
		t.Fatalf("window count %d, want both dispatches preserved", n)
	}
	correction, err := workflowCorrectionContextForDispatch(context.Background(), s.DatabaseForTesting(), id, "repair", "")
	if err != nil {
		t.Fatal(err)
	}
	if correction == nil || correction.FailedAttemptID != "attempt:"+id+":1" {
		t.Fatalf("rewritten job acceptance lost the exact failed-attempt binding: %#v", correction)
	}
}

// The combined final acceptance may not exit refine on integration evidence
// acquired before the final pending job completed (CD-0205 D3): the verify
// run must have observed the whole integrated result population, so a run
// acquired before the final job's completion refuses the assertion.
func TestWitnessCombinedAcceptRejectsPreResultIntegration(t *testing.T) {
	const id = "witness-pre-result-integration"
	f := seedWorkflowReturnRouteFixture(t, id, "workflow.break_fix", "refine")
	s := f.store
	defer s.Close()
	reviewGateStartStep(t, s, id, "refine", "start_refine", f.owner)
	job := &WorkerJobBinding{JobID: "job:witness-final", Revision: 1}
	dispatchJobBoundAttempt(t, s, id, "refine", "attempt:"+id+":1", nil, f.owner, 0, "witness-integration", job)
	var occurred string
	if err := s.DatabaseForTesting().QueryRow(`SELECT occurred_at FROM domain_events WHERE subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, id, WorkflowActionStarted).Scan(&occurred); err != nil {
		t.Fatal(err)
	}
	at, err := time.Parse(time.RFC3339Nano, occurred)
	if err != nil {
		t.Fatal(err)
	}
	views, err := s.WorkerJobRevisions(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	ref := refineProofSeedVerifyRunForProject(t, s, id, strings.Repeat("a", 64), []string{"go", "vet", "./..."}, at.Add(time.Second), views[0].ProjectScope)
	refineProofBindVerification(t, s, id, ref, ref)
	if err := completeJobBoundAttemptForTest(s, id, "attempt:"+id+":1", BuiltinLaneDefinitions()[0], job); err != nil {
		t.Fatal(err)
	}
	err = runVerdictActionAs(t, s, id, "accept_worker_result", mustJSONValue(map[string]any{
		"attempt_id": "attempt:" + id + ":1", "attempt_epoch": latestStepStartEpoch(t, s, id, "refine"),
		"delivery_artifact": "artifact:witness-final", "delivery_state": "asserted",
	}), 0, reviewGateAcceptor(id))
	if err == nil {
		t.Fatal("combined acceptance admitted integration evidence acquired and bound before the final job completed")
	}
	if !strings.Contains(err.Error(), "verification evidence") {
		t.Fatalf("combined acceptance refusal = %v, want the integration remedy", err)
	}
}

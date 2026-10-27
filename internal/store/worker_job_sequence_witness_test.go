package store

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// Composed witnesses for the sequence and readiness obligations A3/A5/A17
// and A21: they drive the existing job lifecycle (CD-0205) and combined
// rejection/retry owner (CD-0198 D5) and are expected to pass on baseline.

// sequenceWitnessEventCount reads the whole event count of one work item.
func sequenceWitnessEventCount(t *testing.T, s *Store, workID string) int64 {
	t.Helper()
	var count int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_type=? AND subject_id=?`, string(SubjectWorkItem), workID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// sequenceWitnessAttemptCount reads the durable attempt bindings of one work
// item.
func sequenceWitnessAttemptCount(t *testing.T, s *Store, workID string) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_attempts WHERE work_id=?`, workID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// sequenceWitnessAcceptEventID returns the event id of the one accepted
// result completion for the attempt.
func sequenceWitnessAcceptEventID(t *testing.T, s *Store, workID, attemptID string) string {
	t.Helper()
	var eventID string
	if err := s.DatabaseForTesting().QueryRow(`SELECT r.event_id FROM worker_attempt_dispositions d JOIN effect_receipts r ON r.event_id=d.event_id AND r.effect_seq=d.seq WHERE d.work_id=? AND d.attempt_id=? AND d.action_id='accept_worker_result' AND r.kind=? AND r.subject_type=? AND r.subject_id=d.work_id`, workID, attemptID, WorkflowActionCompleted, string(SubjectWorkItem)).Scan(&eventID); err != nil {
		t.Fatalf("the accepted result completion for %s: %v", attemptID, err)
	}
	return eventID
}

// sequenceWitnessRecordJob records one worker-job revision from authored
// fields and fills the binding from the recorded revision.
func sequenceWitnessRecordJob(t *testing.T, s *Store, workID string, actor WorkflowActor, fields map[string]any, job *WorkerJobBinding) {
	t.Helper()
	if err := recordWorkerJobFieldsForTest(t, s, workID, actor, fields); err != nil {
		t.Fatalf("record worker-job %s: %v", fields["job_id"], err)
	}
	views, err := readWorkerJobRevisions(context.Background(), s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range views {
		if view.Binding.JobID == job.JobID {
			*job = view.Binding
		}
	}
	if job.Digest == "" {
		t.Fatalf("worker-job %s recorded without a derived digest", job.JobID)
	}
}

// TestWorkerJobPrerequisiteChainSequencesDispatchAcceptanceAndRebuild drives
// the exact-revision dependency chain of A3/A5/A17 through the real owners:
// the blocked successor's dispatch refuses atomically, each plain local
// acceptance satisfies exactly its recorded revision and holds the step,
// zero delivery and zero approval surface moves, and the rebuild folds back
// to identical bindings, result references, and readiness.
func TestWorkerJobPrerequisiteChainSequencesDispatchAcceptance(t *testing.T) {
	t.Parallel()
	const workID = "job-sequence-witness-prereq-chain"
	fixture := oSeedReviewRoute(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	ctx := context.Background()
	acceptor := reviewGateAcceptor(workID)
	lane := reviewGateLane(t, "implementation")
	approvalCounts := func() (int, int) {
		t.Helper()
		var approvals, challenges int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM agent_approvals`).Scan(&approvals); err != nil {
			t.Fatal(err)
		}
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM agent_approval_challenges`).Scan(&challenges); err != nil {
			t.Fatal(err)
		}
		return approvals, challenges
	}
	approvalsBefore, challengesBefore := approvalCounts()

	jobA := &WorkerJobBinding{JobID: "job:sequence-witness-a", Revision: 1}
	recordWorkerJobRevisionForTest(t, s, workID, fixture.owner, jobA)
	jobB := &WorkerJobBinding{JobID: "job:sequence-witness-b", Revision: 1}
	sequenceWitnessRecordJob(t, s, workID, fixture.owner, map[string]any{
		"job_id": jobB.JobID, "objective": "bounded successor job behind an exact prerequisite revision",
		"stopping_condition": "the recorded checks pass with no unresolved reference",
		"path_scope":         []string{"internal/store"}, "checks": []string{"go test ./internal/store/"},
		"reserved_integration": "integration evidence binds at the parent effect step",
		"ready":                true, "readiness_evidence": []string{"evidence:coordinator-ready"},
		"prerequisites": []map[string]any{{"job_id": jobA.JobID, "revision": jobA.Revision}},
	}, jobB)

	// Only the dependency-free job is selectable now; the successor is
	// recorded but waits on its exact prerequisite revision.
	ready, err := readyWorkerJobRevisions(ctx, s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 1 || ready[0].Binding != *jobA {
		t.Fatalf("ready revisions before the dependency = %#v, want only %s@%d", ready, jobA.JobID, jobA.Revision)
	}

	// The blocked successor's actual dispatch refuses through the one
	// dispatch admission, and the refusal is atomic: no event of any kind,
	// the version unchanged, and no attempt window opened.
	blockedAttempt := "attempt:" + workID + ":blocked-successor"
	blockedPacket := dispatchWorkerPacket(t, s, workID, "repair", blockedAttempt)
	blockedPacket["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, *jobB, "implement")
	versionBefore := readWorkVersion(t, s, workID)
	eventsBefore := sequenceWitnessEventCount(t, s, workID)
	if err := dispatchJobPacketForTest(t, s, workID, blockedAttempt, fixture.owner, "sequence-blocked-"+workID, blockedPacket); err == nil || !strings.Contains(err.Error(), "is not ready") {
		t.Fatalf("the blocked successor dispatch error = %v, want the readiness refusal", err)
	}
	if got := readWorkVersion(t, s, workID); got != versionBefore {
		t.Fatalf("the blocked dispatch moved the version %d -> %d", versionBefore, got)
	}
	if got := sequenceWitnessEventCount(t, s, workID); got != eventsBefore {
		t.Fatalf("the blocked dispatch left %d events (before %d); the refusal keeps no partial writes", got, eventsBefore)
	}
	if start := workflowCurrentStartAtStep(t, s, workID, "repair"); start != nil || len(workflowStartReceiptIDs(t, s, workID)) != 0 {
		t.Fatalf("the blocked dispatch opened a current step window or start receipt: %+v", start)
	}
	if got := sequenceWitnessAttemptCount(t, s, workID); got != 0 {
		t.Fatalf("the blocked dispatch left %d attempt bindings, want none", got)
	}

	// The dependency dispatches, completes, and its plain local acceptance
	// satisfies exactly its recorded revision and holds the parent step.
	attemptA := "attempt:" + workID + ":first"
	acceptExit := func() int64 {
		t.Helper()
		var seq int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(MAX(exit_seq),0) FROM workflow_phase_exits WHERE work_id=? AND action_id='accept_worker_result'`, workID).Scan(&seq); err != nil {
			t.Fatal(err)
		}
		return seq
	}
	// A held local job acceptance keeps the worker-result interval open, so
	// the real fold must not record an accept_worker_result phase exit.
	exitBefore := acceptExit()
	dispatchJobBoundAttempt(t, s, workID, "repair", attemptA, nil, fixture.owner, 0, "sequence-a-"+workID, jobA)
	completeAndAcceptAttempt(t, s, workID, attemptA, lane, acceptor)
	views, err := readWorkerJobRevisions(ctx, s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 || views[0].Binding != *jobA || views[1].Binding != *jobB || views[0].State != "satisfied" || views[1].State != "recorded" {
		t.Fatalf("revisions after the first local accept = %#v, want %s satisfied beside %s recorded", views, jobA.JobID, jobB.JobID)
	}
	if views[0].SatisfiedResultRef == "" || views[0].SatisfiedResultRef != sequenceWitnessAcceptEventID(t, s, workID, attemptA) {
		t.Fatalf("satisfied result ref = %q, want the first acceptance's own event id", views[0].SatisfiedResultRef)
	}
	if pin := issue1013Pin(t, s, workID); pin.Step != "repair" {
		t.Fatalf("step after the first local accept = %q, want repair held", pin.Step)
	}
	// The exact prerequisite revision's acceptance released the successor and
	// nothing else.
	ready, err = readyWorkerJobRevisions(ctx, s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 1 || ready[0].Binding != *jobB {
		t.Fatalf("ready revisions after the release = %#v, want exactly %s@%d", ready, jobB.JobID, jobB.Revision)
	}

	// The released successor dispatches through the same owner, and its own
	// plain local acceptance satisfies its revision, keeps the effect step
	// held, and writes no delivery assertion and no correction disposition.
	attemptB := "attempt:" + workID + ":second"
	oDispatchJobBoundAttempt(t, s, workID, attemptB, nil, fixture.owner, 0, "sequence-b-"+workID, jobB)
	completeAndAcceptAttempt(t, s, workID, attemptB, lane, acceptor)
	views, err = readWorkerJobRevisions(ctx, s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 || views[0].Binding != *jobA || views[1].Binding != *jobB || views[0].State != "satisfied" || views[1].State != "satisfied" {
		t.Fatalf("revisions after the second local accept = %#v, want exactly both implementation revisions satisfied with no separate continuation Review job", views)
	}
	if views[1].SatisfiedResultRef == "" || views[1].SatisfiedResultRef == views[0].SatisfiedResultRef || views[1].SatisfiedResultRef != sequenceWitnessAcceptEventID(t, s, workID, attemptB) {
		t.Fatalf("second satisfied result ref = %q, want the second acceptance's own distinct event id", views[1].SatisfiedResultRef)
	}
	if pin := issue1013Pin(t, s, workID); pin.Step != "repair" {
		t.Fatalf("step after the second local accept = %q, want the effect step held", pin.Step)
	}
	if got := acceptExit(); got != exitBefore {
		t.Fatalf("local job acceptances moved the accept_worker_result phase exit %d -> %d", exitBefore, got)
	}
	if seq, err := workflowLatestDeliveryAssertionSeq(ctx, s, workID); err != nil || seq != 0 {
		t.Fatalf("delivery assertion after two local accepts = %d, %v; want none", seq, err)
	}
	for _, actionID := range []string{"record_delivery", "reject_worker_result", "request_correction", "record_worker_failure"} {
		var count int
		if err := s.DatabaseForTesting().QueryRow(`SELECT (SELECT count(*) FROM workflow_phase_exits WHERE work_id=? AND action_id=?)+(SELECT count(*) FROM worker_attempt_dispositions WHERE work_id=? AND action_id=?)`, workID, actionID, workID, actionID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("local acceptance chain recorded %d %q completions, want zero", count, actionID)
		}
	}
	if correction, err := workflowCorrectionContext(ctx, s.db, workID, "repair"); err != nil || correction != nil {
		t.Fatalf("correction context after two local accepts = (%+#v, %v), want no correction window at all", correction, err)
	}
	if approvalsAfter, challengesAfter := approvalCounts(); approvalsAfter != approvalsBefore || challengesAfter != challengesBefore {
		t.Fatalf("the acceptance chain moved the approval tables from (%d,%d) to (%d,%d), want zero approval delta", approvalsBefore, challengesBefore, approvalsAfter, challengesAfter)
	}

}

// TestCombinedRejectRetryWithUnreadyLatestJobRevisionRefusesWhole drives the
// A21 obligation on the existing combined rejection owner: with the latest
// revision of the bound job unready, the retry re-binding the recorded
// revision refuses whole — rejection, dispatch, and attempt window all stay
// unwritten — and the rejection-only escape behind the refusal still records
// and opens no dispatch.
func TestCombinedRejectRetryWithUnreadyLatestJobRevisionRefusesWhole(t *testing.T) {
	t.Parallel()
	const workID = "job-sequence-witness-unready-retry"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.implementation", "execution")
	s := fixture.store
	defer s.Close()
	ctx := context.Background()
	dispatcher := diagnosisS1Actor(workID, "dispatcher")
	attempt := "attempt:" + workID + ":1"
	job := &WorkerJobBinding{JobID: "job:" + workID}
	dispatchJobBoundAttempt(t, s, workID, "execution", attempt, nil, dispatcher, 0, "sequence-unready-dispatch-"+workID, job)
	epoch := latestStepStartEpoch(t, s, workID, "execution")
	s2CompletedJobBoundAttempt(t, s, workID, attempt)

	// Recording an unready latest revision of the same job strands the
	// dispatched revision behind a supersession, exactly as the readiness
	// rule reads it.
	if err := recordWorkerJobFieldsForTest(t, s, workID, dispatcher, map[string]any{
		"job_id": job.JobID, "objective": "await the coordinator's readiness evidence",
		"stopping_condition": "the recorded checks pass",
		"path_scope":         []string{"internal/store"}, "checks": []string{"go test ./internal/store/"},
		"reserved_integration": "integration evidence binds at the parent effect step",
		"ready":                false,
	}); err != nil {
		t.Fatalf("record the unready latest revision: %v", err)
	}

	versionBefore := readWorkVersion(t, s, workID)
	eventsBefore := sequenceWitnessEventCount(t, s, workID)
	startBefore := workflowCurrentStartAtStep(t, s, workID, "execution")
	startsBefore := workflowStartReceiptIDs(t, s, workID)
	attemptsBefore := sequenceWitnessAttemptCount(t, s, workID)
	const combinedOperation = "sequence-unready-combined-" + workID
	if _, err := s2CombinedReject(t, s, workID, fixture.owner, s2CombinedRejectPayload(attempt, epoch, json.RawMessage(`{"lane_id":"implement"}`)), combinedOperation); err == nil {
		t.Fatal("the combined rejection and retry was admitted behind an unready latest job revision")
	} else {
		var failure *Failure
		if !failureAs(err, &failure) || failure.Kind != KindInvalidOperation || !strings.Contains(failure.Detail, "is not ready") {
			t.Fatalf("the combined refusal error = %v, want the job readiness refusal", err)
		}
	}
	if got := readWorkVersion(t, s, workID); got != versionBefore {
		t.Fatalf("the refused combined call moved the version %d -> %d", versionBefore, got)
	}
	if got := sequenceWitnessEventCount(t, s, workID); got != eventsBefore {
		t.Fatalf("the refused combined call left %d events (before %d); the refusal keeps no partial writes", got, eventsBefore)
	}
	if got := workflowCurrentStartAtStep(t, s, workID, "execution"); got == nil || startBefore == nil || *got != *startBefore || !slices.Equal(workflowStartReceiptIDs(t, s, workID), startsBefore) {
		t.Fatalf("the refused combined call changed the current start or exact receipts: %+v -> %+v", startBefore, got)
	}
	if got := sequenceWitnessAttemptCount(t, s, workID); got != attemptsBefore {
		t.Fatalf("the refused combined call left %d attempt bindings (before %d)", got, attemptsBefore)
	}
	var rejectionCompletions, retryAttemptRows int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_attempt_dispositions WHERE work_id=? AND attempt_id=? AND action_id='reject_worker_result'`, workID, attempt).Scan(&rejectionCompletions); err != nil {
		t.Fatal(err)
	}
	if rejectionCompletions != 0 {
		t.Fatalf("the refused combined call recorded %d rejection completions, want zero", rejectionCompletions)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, combinedOperation+":retry").Scan(&retryAttemptRows); err != nil {
		t.Fatal(err)
	}
	if retryAttemptRows != 0 {
		t.Fatalf("the refused combined call minted %d retry attempt bindings, want zero", retryAttemptRows)
	}

	// The rejection-only escape behind the refusal records through the same
	// owner, dispositions the result rejected, and still opens no dispatch.
	if _, err := s2CombinedReject(t, s, workID, fixture.owner, s2CombinedRejectPayload(attempt, epoch, nil), "sequence-unready-reject-only-"+workID); err != nil {
		t.Fatalf("the rejection-only escape behind the refusal: %v", err)
	}
	if got := readWorkVersion(t, s, workID); got <= versionBefore {
		t.Fatalf("the rejection-only escape left the version at %d, want the rejection recorded past %d", got, versionBefore)
	}
	correction, err := workflowCorrectionContext(ctx, s.db, workID, "execution")
	if err != nil || correction == nil || correction.Disposition != "rejected" || correction.SourceEventID == "" {
		t.Fatalf("correction after the rejection-only escape = (%+#v, %v), want the open rejected record", correction, err)
	}
	if got := sequenceWitnessAttemptCount(t, s, workID); got != attemptsBefore {
		t.Fatalf("the rejection-only escape left %d attempt bindings (before %d), want no retry attempt", got, attemptsBefore)
	}
	if got := workflowCurrentStartAtStep(t, s, workID, "execution"); got == nil || startBefore == nil || *got != *startBefore || !slices.Equal(workflowStartReceiptIDs(t, s, workID), startsBefore) {
		t.Fatalf("the rejection-only escape changed the current start or exact receipts: %+v -> %+v", startBefore, got)
	}
}

// readyWorkerJobRevisions reads the revisions a dispatcher may select now,
// through the readiness the projection read derives.
func readyWorkerJobRevisions(ctx context.Context, q queryer, workID string) ([]WorkerJobRevisionView, error) {
	all, err := readWorkerJobRevisions(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	ready := []WorkerJobRevisionView{}
	for _, view := range all {
		if view.Ready {
			ready = append(ready, view)
		}
	}
	return ready, nil
}

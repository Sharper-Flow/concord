package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// A failed job's correction stays open until an acceptance satisfies that
// recorded obligation. An unrelated accepted job cannot close it.
func TestUnrelatedAcceptedJobPreservesCorrectionWindow(t *testing.T) {
	const workID = "correction-window-unrelated-job"
	fixture := oSeedReviewRoute(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	lane := reviewGateLane(t, "implementation")
	worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	workerRef, err := WorkflowActorRef(worker)
	if err != nil {
		t.Fatal(err)
	}
	workerVersion := readWorkVersion(t, s, workID)
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{workflowEventWithActor("window-worker-"+workID, WorkflowActorRecorded, workID, workerRef, map[string]any{
		"work_id": workID, "expected_version": workerVersion, "resulting_version": workerVersion + 1,
		"actor_ref": workerRef, "principal_ref": worker.PrincipalRef, "client_ref": worker.ClientRef,
		"agent_ref": worker.AgentRef, "session_ref": worker.SessionRef, "actor_class": "agent",
	})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): workerVersion}}); err != nil {
		t.Fatal(err)
	}
	acceptor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/window-acceptor", SessionRef: "session/" + workID + "-acceptor", ActorClass: ActorAgent}
	correctiveJob := &WorkerJobBinding{JobID: "job:window-corrective", Revision: 1}
	unrelatedJob := &WorkerJobBinding{JobID: "job:window-unrelated", Revision: 1}

	attempt1 := "attempt:" + workID + ":1"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt1, nil, worker, issue1013Pin(t, s, workID).Version, "job-window-1", correctiveJob)
	// The counting journeys' terminal fact goes through the live ingest with
	// the recorded observation the promoted pin requires; the pinned
	// definition derives the failure record in the same commit, so the
	// explicit record disposition the fixture used to apply beside it is
	// replaced, never duplicated.
	s2WorkerFailedAttributedLive(t, s, workID, attempt1, "lane fallback blocked before the model ran")
	pin := issue1013Pin(t, s, workID)
	if pin.Correction == nil || pin.Correction.FailedAttemptID != attempt1 {
		t.Fatalf("correction after failed job-bound attempt = %#v, want %s", pin.Correction, attempt1)
	}

	// An unrelated job's acceptance leaves the failed obligation unresolved.
	attempt2 := "attempt:" + workID + ":2"
	issue1013StartRepair(t, s, workID, worker, pin.Version, latestStepStartEpoch(t, s, workID, "repair")+1)
	pin = issue1013Pin(t, s, workID)
	oDispatchJobBoundAttempt(t, s, workID, attempt2, pin.Correction, worker, pin.Version, "job-window-2", unrelatedJob)
	completeAndAcceptAttempt(t, s, workID, attempt2, lane, acceptor)
	if boundary := jobWindowAcceptanceBoundary(t, s, workID); boundary != 0 {
		t.Fatalf("unrelated acceptance closed the unresolved obligation at %d", boundary)
	}

	// The pin-level anchor keeps the failed job's window open: the unrelated
	// job's dispatch does not consume the correction record, so the owning
	// derivation the pin reads still returns the corrective retry binding.
	// The pin's step-scoped view may have advanced with the accept,
	// so the witness reads the anchor at the record's own step.
	anchor, anchorErr := workflowCorrectionContextForDispatch(context.Background(), s.DatabaseForTesting(), workID, "repair", "")
	if anchorErr != nil || anchor == nil || anchor.FailedAttemptID != attempt1 {
		t.Fatalf("anchor correction after the unrelated acceptance = (%#v, %v), want the open window on %s", anchor, anchorErr, attempt1)
	}

}

// Only an accepted result under the recorded corrective job identity closes
// the window (CD-0205): a later revision carrying the same obligation satisfies it.
func TestSatisfyingAcceptedJobResetsCorrectionWindow(t *testing.T) {
	const workID = "correction-window-satisfying-job"
	fixture := oSeedReviewRoute(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	lane := reviewGateLane(t, "implementation")
	worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	workerRef, err := WorkflowActorRef(worker)
	if err != nil {
		t.Fatal(err)
	}
	workerVersion := readWorkVersion(t, s, workID)
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{workflowEventWithActor("satisfy-worker-"+workID, WorkflowActorRecorded, workID, workerRef, map[string]any{
		"work_id": workID, "expected_version": workerVersion, "resulting_version": workerVersion + 1,
		"actor_ref": workerRef, "principal_ref": worker.PrincipalRef, "client_ref": worker.ClientRef,
		"agent_ref": worker.AgentRef, "session_ref": worker.SessionRef, "actor_class": "agent",
	})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): workerVersion}}); err != nil {
		t.Fatal(err)
	}
	acceptor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/satisfy-acceptor", SessionRef: "session/" + workID + "-acceptor", ActorClass: ActorAgent}
	correctiveJob := &WorkerJobBinding{JobID: "job:satisfy-corrective", Revision: 1}
	correctiveJobRevision2 := &WorkerJobBinding{JobID: "job:satisfy-corrective", Revision: 2}
	// The third dispatch must not reuse the satisfied revision 2: a satisfied
	// revision cannot redispatch (CD-0205), so the next leg records and
	// dispatches revision 3 of the same job identity.
	correctiveJobRevision3 := &WorkerJobBinding{JobID: "job:satisfy-corrective", Revision: 3}

	attempt1 := "attempt:" + workID + ":1"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt1, nil, worker, issue1013Pin(t, s, workID).Version, "job-satisfy-1", correctiveJob)
	// The live ingest carries the recorded observation and derives the
	// failure record in the same commit, replacing the explicit record
	// disposition.
	s2WorkerFailedAttributedLive(t, s, workID, attempt1, "lane fallback blocked before the model ran")
	pin := issue1013Pin(t, s, workID)
	if pin.Correction == nil || pin.Correction.FailedAttemptID != attempt1 {
		t.Fatalf("correction after failed job-bound attempt = %#v, want %s", pin.Correction, attempt1)
	}
	prefixAfterFirstFailure := maxEventSeq(t, s, workID)
	if boundary := jobWindowAcceptanceBoundary(t, s, workID); boundary != 0 {
		t.Fatalf("unsatisfied corrective job borrowed a satisfying acceptance at %d", boundary)
	}

	// A later revision of the same job identity satisfies the corrective
	// job, so the satisfying acceptance opens a fresh window.
	attempt2 := "attempt:" + workID + ":2"
	issue1013StartRepair(t, s, workID, worker, pin.Version, latestStepStartEpoch(t, s, workID, "repair")+1)
	pin = issue1013Pin(t, s, workID)
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt2, pin.Correction, worker, pin.Version, "job-satisfy-2", correctiveJobRevision2)
	completeAndAcceptAttempt(t, s, workID, attempt2, lane, acceptor)
	pin = issue1013Pin(t, s, workID)
	if pin.Correction != nil {
		t.Fatalf("satisfying acceptance left the correction context: %#v", pin.Correction)
	}
	if boundary := jobWindowAcceptanceBoundary(t, s, workID); boundary <= prefixAfterFirstFailure {
		t.Fatalf("satisfying acceptance boundary = %d, want after the failed obligation at %d", boundary, prefixAfterFirstFailure)
	}

	attempt3 := "attempt:" + workID + ":3"
	issue1013StartRepair(t, s, workID, worker, pin.Version, latestStepStartEpoch(t, s, workID, "repair")+1)
	pin = issue1013Pin(t, s, workID)
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt3, nil, worker, pin.Version, "job-satisfy-3", correctiveJobRevision3)
	s2WorkerFailedAttributedLive(t, s, workID, attempt3, "lane fallback blocked before the model ran")
	pin = issue1013Pin(t, s, workID)
	if pin.Correction == nil || pin.Correction.FailedAttemptID != attempt3 {
		t.Fatalf("correction after the satisfying acceptance = %#v, want the new failed attempt", pin.Correction)
	}
}

// Several job-bound failures each stay unresolved until their own recorded
// disposition names them (CD-0205): satisfying one of two unresolved jobs
// leaves the other's correction source intact.
func TestPartialSatisfactionPreservesOtherUnresolvedJobs(t *testing.T) {
	const workID = "correction-window-partial-satisfaction"
	fixture := oSeedReviewRoute(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	workerRef, err := WorkflowActorRef(worker)
	if err != nil {
		t.Fatal(err)
	}
	workerVersion := readWorkVersion(t, s, workID)
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{workflowEventWithActor("partial-worker-"+workID, WorkflowActorRecorded, workID, workerRef, map[string]any{
		"work_id": workID, "expected_version": workerVersion, "resulting_version": workerVersion + 1,
		"actor_ref": workerRef, "principal_ref": worker.PrincipalRef, "client_ref": worker.ClientRef,
		"agent_ref": worker.AgentRef, "session_ref": worker.SessionRef, "actor_class": "agent",
	})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): workerVersion}}); err != nil {
		t.Fatal(err)
	}
	acceptor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/partial-acceptor", SessionRef: "session/" + workID + "-acceptor", ActorClass: ActorAgent}
	jobA := &WorkerJobBinding{JobID: "job:partial-a", Revision: 1}
	jobB := &WorkerJobBinding{JobID: "job:partial-b", Revision: 1}

	attempt1 := "attempt:" + workID + ":1"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt1, nil, worker, issue1013Pin(t, s, workID).Version, "job-partial-1", jobA)
	s2WorkerFailedAttributedLive(t, s, workID, attempt1, "lane fallback blocked before the model ran")
	pin := issue1013Pin(t, s, workID)
	issue1013StartRepair(t, s, workID, worker, pin.Version, latestStepStartEpoch(t, s, workID, "repair")+1)
	pin = issue1013Pin(t, s, workID)

	attempt2 := "attempt:" + workID + ":2"
	oDispatchJobBoundAttempt(t, s, workID, attempt2, pin.Correction, worker, pin.Version, "job-partial-2", jobB)
	s2WorkerFailedAttributedLive(t, s, workID, attempt2, "lane fallback blocked before the model ran")

	// Satisfying job A leaves job B unresolved.
	attempt3 := "attempt:" + workID + ":3"
	pin = issue1013Pin(t, s, workID)
	issue1013StartRepair(t, s, workID, worker, pin.Version, latestStepStartEpoch(t, s, workID, "repair")+1)
	pin = issue1013Pin(t, s, workID)
	oDispatchJobBoundAttempt(t, s, workID, attempt3, pin.Correction, worker, pin.Version, "job-partial-3", jobA)
	completeAndAcceptAttempt(t, s, workID, attempt3, reviewGateLane(t, "implementation"), acceptor)
	if boundary := jobWindowAcceptanceBoundary(t, s, workID); boundary != 0 {
		t.Fatalf("partial satisfaction closed another job's obligation at %d", boundary)
	}
	// The pin-level anchor carries the window by unresolved job identity:
	// job B's failure record is not consumed by job A's dispatch or its
	// acceptance, so the owning derivation still returns the open correction
	// source. The pin's step-scoped view advanced past repair with the
	// accept, so the witness reads the anchor the pin reads at its step.
	correction, corrErr := workflowCorrectionContextForDispatch(context.Background(), s.DatabaseForTesting(), workID, "repair", "")
	if corrErr != nil || correction == nil || correction.FailedAttemptID != attempt2 {
		t.Fatalf("anchor correction after partially satisfying acceptance = (%#v, %v), want the open source on %s", correction, corrErr, attempt2)
	}
}

// The unresolved job's source survives live terminal failures, unrelated local acceptance,
// duplicate terminal reports, and rebuild (CD-0205 D4, CD-0198 D5).
func TestUnrelatedAcceptedJobPreservesDerivedFailureSource(t *testing.T) {
	const workID = "correction-window-derived-budget"
	// The scenario pins the current registered retained head through the same
	// lookup the fixture resolves, and refuses to run when that head no longer
	// derives the failure record: a head without the derivation would leave
	// the live ingests recordless and every later assertion meaningless.
	head, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	if !workflowDerivedWorkerFailureActive(head.Definition) {
		t.Fatalf("current head %s %s does not derive the failure record from the terminal event", head.Definition.Ref, head.Digest)
	}
	fixture := oSeedReviewRoute(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	lane := reviewGateLane(t, "implementation")
	dispatcher := diagnosisS1Actor(workID, "dispatcher")
	dispatcherRef, err := WorkflowActorRef(dispatcher)
	if err != nil {
		t.Fatal(err)
	}
	dispatcherVersion := readWorkVersion(t, s, workID)
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{workflowEventWithActor("derived-budget-worker-"+workID, WorkflowActorRecorded, workID, dispatcherRef, map[string]any{
		"work_id": workID, "expected_version": dispatcherVersion, "resulting_version": dispatcherVersion + 1,
		"actor_ref": dispatcherRef, "principal_ref": dispatcher.PrincipalRef, "client_ref": dispatcher.ClientRef,
		"agent_ref": dispatcher.AgentRef, "session_ref": dispatcher.SessionRef, "actor_class": "agent",
	})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): dispatcherVersion}}); err != nil {
		t.Fatal(err)
	}
	acceptor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/derived-budget-acceptor", SessionRef: "session/" + workID + "-acceptor", ActorClass: ActorAgent}
	jobA := &WorkerJobBinding{JobID: "job:derived-budget-a", Revision: 1}
	unrelatedJob := &WorkerJobBinding{JobID: "job:derived-budget-b", Revision: 1}

	// Job A's first attempt fails through the live terminal ingest: the
	// derivation pairs exactly one record with the original event.
	attempt1 := "attempt:" + workID + ":1"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt1, nil, dispatcher, issue1013Pin(t, s, workID).Version, "derived-budget-1", jobA)
	detailA := "the lane failed before a judgeable result existed"
	if err := s2WorkerFailedLive(t, s, s2WorkerFailedAttributedEvent(workID, attempt1, "failed-"+workID+"-1", preferredModelForLane(lane), WorkerFailureWorkerError, detailA, time.Unix(30, 0).UTC())); err != nil {
		t.Fatalf("the live terminal worker.failed ingest for %s: %v", attempt1, err)
	}
	records1 := s2DerivedFailureRecords(t, s, workID, attempt1)
	if len(records1) != 1 || !strings.HasSuffix(records1[0]["event_id"].(string), ":workflow-failure") {
		t.Fatalf("derived records for %s = %+v, want exactly one derived pair from the original terminal event", attempt1, records1)
	}
	pin := issue1013Pin(t, s, workID)
	if pin.Correction == nil || pin.Correction.Disposition != "failed" || pin.Correction.FailedAttemptID != attempt1 || pin.Correction.Diagnosis != detailA {
		t.Fatalf("correction after the derived failure = %#v, want the failed source on %s", pin.Correction, attempt1)
	}

	// An accepted result under a different job identity cannot close this obligation.
	attempt2 := "attempt:" + workID + ":2"
	issue1013StartRepair(t, s, workID, dispatcher, pin.Version, latestStepStartEpoch(t, s, workID, "repair")+1)
	pin = issue1013Pin(t, s, workID)
	oDispatchJobBoundAttempt(t, s, workID, attempt2, pin.Correction, dispatcher, pin.Version, "derived-budget-2", unrelatedJob)
	completeAndAcceptAttempt(t, s, workID, attempt2, lane, acceptor)
	if boundary := jobWindowAcceptanceBoundary(t, s, workID); boundary != 0 {
		t.Fatalf("unrelated acceptance closed the unresolved obligation at %d", boundary)
	}
	anchor, anchorErr := workflowCorrectionContextForDispatch(context.Background(), s.DatabaseForTesting(), workID, "repair", "")
	if anchorErr != nil || anchor == nil || anchor.FailedAttemptID != attempt1 {
		t.Fatalf("anchor correction after the unrelated acceptance = (%#v, %v), want the open source on %s", anchor, anchorErr, attempt1)
	}

	// A's retry is dispatched under A's original recorded revision and fails
	// through its own terminal ingest again.
	attempt3 := "attempt:" + workID + ":3"
	issue1013StartRepair(t, s, workID, dispatcher, pin.Version, latestStepStartEpoch(t, s, workID, "repair")+1)
	pin = issue1013Pin(t, s, workID)
	oDispatchJobBoundAttempt(t, s, workID, attempt3, pin.Correction, dispatcher, pin.Version, "derived-budget-3", jobA)
	retryJob := s2DispatchJobBinding(t, s, workID, attempt3)
	if retryJob == nil || retryJob.JobID != jobA.JobID || retryJob.Revision != jobA.Revision || retryJob.Digest != jobA.Digest {
		t.Fatalf("retry dispatch binding = %#v, want A's original recorded revision %s@%d", retryJob, jobA.JobID, jobA.Revision)
	}
	detailRetry := "the retry failed before a judgeable result existed"
	if err := s2WorkerFailedLive(t, s, s2WorkerFailedAttributedEvent(workID, attempt3, "failed-"+workID+"-3", preferredModelForLane(lane), WorkerFailureWorkerError, detailRetry, time.Unix(30, 0).UTC())); err != nil {
		t.Fatalf("the live terminal worker.failed ingest for %s: %v", attempt3, err)
	}
	records3 := s2DerivedFailureRecords(t, s, workID, attempt3)
	if len(records3) != 1 || records3[0]["step"] != "repair" || records3[0]["epoch"] != latestStepStartEpoch(t, s, workID, "repair") {
		t.Fatalf("derived records for %s = %+v, want exactly one at the retry's own epoch", attempt3, records3)
	}

	// The failed anchor is the retry's own record under A's original revision.
	pin = issue1013Pin(t, s, workID)
	if pin.Correction == nil || pin.Correction.FailedAttemptID != attempt3 || pin.Correction.Diagnosis != detailRetry {
		t.Fatalf("correction after the retry failure = %#v, want the latest failed source on %s", pin.Correction, attempt3)
	}
	if boundary := jobWindowAcceptanceBoundary(t, s, workID); boundary != 0 {
		t.Fatalf("retry failure closed the unresolved obligation at %d", boundary)
	}

	// A duplicate terminal report derives nothing: the retry attempt's
	// terminal fact is already recorded, and neither the records nor the anchor move.
	duplicate := s2WorkerFailedAttributedEvent(workID, attempt3, "failed-"+workID+"-3-duplicate", preferredModelForLane(lane), WorkerFailureWorkerError, detailRetry, time.Unix(30, 0).UTC())
	if err := s2WorkerFailedLive(t, s, duplicate); !hasFailureKind(err, KindProjectionConflict) {
		t.Fatalf("duplicate terminal worker.failed report error = %v, want %s", err, KindProjectionConflict)
	}
	if got := s2DerivedFailureRecords(t, s, workID, attempt3); len(got) != 1 {
		t.Fatalf("derived records after the duplicate terminal report = %+v, want the one original", got)
	}
	if boundary := jobWindowAcceptanceBoundary(t, s, workID); boundary != 0 {
		t.Fatalf("duplicate terminal report closed the unresolved obligation at %d", boundary)
	}
	if pin := issue1013Pin(t, s, workID); pin.Correction == nil || pin.Correction.FailedAttemptID != attempt3 {
		t.Fatalf("correction after the duplicate terminal report = %#v, want the unchanged window on %s", pin.Correction, attempt3)
	}

}

// jobWindowAcceptanceBoundary reads the current satisfied-obligation cutoff.
func jobWindowAcceptanceBoundary(t *testing.T, s *Store, workID string) int64 {
	t.Helper()
	boundary, err := workflowCorrectionAcceptanceBefore(context.Background(), s.DatabaseForTesting(), workID, 0, "workflow_correction")
	if err != nil {
		t.Fatalf("read satisfying acceptance for %s: %v", workID, err)
	}
	return boundary
}

// maxEventSeq returns the current last sequence of the work item's event
// log, so a test can pin the prefix a later derivation walk replays.
func maxEventSeq(t *testing.T, s *Store, workID string) int64 {
	t.Helper()
	var seq int64
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT COALESCE(MAX(effect_seq),0) FROM effect_receipts WHERE subject_type=? AND subject_id=?`, string(SubjectWorkItem), workID).Scan(&seq); err != nil {
		t.Fatalf("read max event seq for %s: %v", workID, err)
	}
	return seq
}

// dispatchJobBoundAttempt dispatches one worker attempt on the named step
// with the correction context when one is open. When the job carries no
// digest yet, it is first recorded through record_worker_job; the packet then
// carries the recorded revision as inputs.worker_job, so the dispatch
// authorization binds it, and the worker.dispatched evidence lands with the
// binding the authorization recorded.
func dispatchJobBoundAttempt(t *testing.T, s *Store, workID, stepID, attemptID string, correction *WorkflowCorrectionContext, actor WorkflowActor, _ int64, key string, job *WorkerJobBinding) {
	t.Helper()
	if job.Digest == "" {
		recordWorkerJobRevisionForTest(t, s, workID, actor, job)
	}
	packet := dispatchWorkerPacket(t, s, workID, stepID, attemptID)
	packet["schema_version"] = WorkerPacketSchemaVersion
	if correction != nil {
		packet["inputs"].(map[string]any)["correction"] = correction
	}
	packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, *job, "implement")
	if err := dispatchJobPacketForTest(t, s, workID, attemptID, actor, key, packet); err != nil {
		t.Fatalf("dispatch job-bound attempt %s: %v", attemptID, err)
	}
	recordJobBoundWorkerDispatch(t, s, workID, attemptID, job)
}

// dispatchJobPacketForTest runs the dispatch_worker authorization with the
// given packet at the work's current version. A bound job uses the current
// packet schema so these fixtures reach job admission, not a legacy-shape
// refusal before admission.
func dispatchJobPacketForTest(t *testing.T, s *Store, workID, attemptID string, actor WorkflowActor, key string, packet map[string]any) error {
	t.Helper()
	if packet["inputs"].(map[string]any)["worker_job"] != nil {
		packet["schema_version"] = WorkerPacketSchemaVersion
	}
	packet["inputs"].(map[string]any)["binding"].(map[string]any)["work_version"] = readWorkVersion(t, s, workID)
	packetPayload, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	payload := mustJSONValue(map[string]any{"attempt_id": attemptID, "worker_packet": json.RawMessage(packetPayload)})
	_, err = invokeWorkflowActionForCD0059(context.Background(), t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: readWorkVersion(t, s, workID), ActionID: "dispatch_worker", Payload: payload, SessionWorktree: dispatchSessionWorktree(t, s, workID),
		Actor: actor, AcceptedInputsDigest: "sha256:" + strings.Repeat("d", 64), IdempotencyIdentity: key, OperationID: key,
		PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: key, RequestID: "request:" + key, ContractDigest: testManifestDigest, Now: time.Unix(10, 0).UTC(),
	})
	return err
}

// recordedPacketJobForTest projects the recorded revision onto the packet's
// inputs.worker_job shape.
func recordedPacketJobForTest(t *testing.T, s *Store, workID string, job WorkerJobBinding, laneID string) map[string]any {
	t.Helper()
	views, err := readWorkerJobRevisions(context.Background(), s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range views {
		if view.Binding == job {
			var out map[string]any
			if err := json.Unmarshal(mustJSONValue(packetJobFromView(view)), &out); err != nil {
				t.Fatal(err)
			}
			// Mirror the dispatch admission's own projection: an
			// implementation lane of a size-capable pin carries the
			// computed caller exclusions; every other lane carries none.
			if testLaneCapabilityClass(laneID) == "implementation" && testWorkPinCallerCapable(t, s, workID) {
				exclusions, exclusionsErr := workerJobCallerExclusionsTx(context.Background(), s.db, workID, job.JobID)
				if exclusionsErr != nil {
					t.Fatal(exclusionsErr)
				}
				if len(exclusions) > 0 {
					out["caller_exclusions"] = exclusions
				}
			}
			return out
		}
	}
	t.Fatalf("worker job %#v is not recorded", job)
	return nil
}

// testLaneCapabilityClass resolves one registered lane's capability class.
func testLaneCapabilityClass(laneID string) string {
	for _, lane := range BuiltinLaneDefinitions() {
		if lane.ID == laneID {
			return lane.CapabilityClass
		}
	}
	return ""
}

// recordJobBoundWorkerDispatch records the worker.dispatched event through
// the authoritative lane-actor dispatch route (PrepareLaneActorDispatch +
// AppendLaneActorDispatchTx), carrying the packet digest and the worker-job
// binding the dispatch_worker authorization recorded.
func recordJobBoundWorkerDispatch(t *testing.T, s *Store, workID, attemptID string, job *WorkerJobBinding) {
	t.Helper()
	if err := appendJobBoundWorkerDispatch(t, s, workID, attemptID, job); err != nil {
		t.Fatalf("record job-bound worker dispatch %s: %v", attemptID, err)
	}
}

func appendJobBoundWorkerDispatch(t *testing.T, s *Store, workID, attemptID string, job *WorkerJobBinding) error {
	t.Helper()
	return appendLaneBoundWorkerDispatch(t, s, workID, attemptID, reviewGateLane(t, "implementation"), job)
}

// appendLaneBoundWorkerDispatch records the worker.dispatched evidence of the
// named lane through the authoritative lane-actor dispatch route, carrying
// the packet digest and the worker-job binding the dispatch_worker
// authorization recorded.
func appendLaneBoundWorkerDispatch(t *testing.T, s *Store, workID, attemptID string, lane LaneDefinition, job *WorkerJobBinding) error {
	t.Helper()
	var packetDigest string
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT json_extract(payload,'$.worker_packet_digest') FROM worker_authorizations WHERE work_id=? AND attempt_id=?`, workID, attemptID).Scan(&packetDigest); err != nil {
		packetDigest = "sha256:" + strings.Repeat("d", 64)
	}
	dispatch := Event{EventID: "job-window-dispatch-" + workID + "-" + attemptID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(20, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion(WorkerDispatched), Payload: mustJSONValue(WorkerDispatchedPayload{
		AttemptID: attemptID, LaneID: lane.ID, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, PacketDigest: packetDigest,
		ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion, WorkerJob: job,
	})}
	return s.Transact(context.Background(), func(transaction *Transaction) error {
		prepared, err := PrepareLaneActorDispatch(context.Background(), transaction, dispatch, "principal/operator", "client/concord-1")
		if err != nil {
			return err
		}
		_, err = AppendLaneActorDispatchTx(context.Background(), transaction, prepared)
		return err
	})
}

// recordWorkerJobFieldsForTest records one worker-job revision whose authored
// fields the caller owns — an unrelated rewritten objective, for example. On
// an oracle-capable pin the recording carries the fixture oracle the
// definition requires; the oracle-bearing revision seeds trusted preparations
// against the work's qualified subject, and the bootstrap is idempotent.
func recordWorkerJobFieldsForTest(t *testing.T, s *Store, workID string, actor WorkflowActor, fields map[string]any) error {
	t.Helper()
	if oracleCapablePinForWork(t, s, workID) {
		bootstrapOracleFixtureSubject(t, s, workID)
		fields["acceptance_oracle"] = acceptanceOracleFieldsForTest(t, s, workID)
	}
	return recordWorkerJobActionForTest(t, s, workID, actor, fields)
}

// recordWorkerJobRevisionForTest records one worker-job revision through the
// record_worker_job action, the authoring route a coordinator uses. The core
// derives the revision, the parent authority, and the digest; the binding is
// filled from the recorded revision. On an oracle-capable pin the recording
// carries the fixture oracle the definition requires.
func recordWorkerJobRevisionForTest(t *testing.T, s *Store, workID string, actor WorkflowActor, job *WorkerJobBinding) {
	t.Helper()
	fields := map[string]any{
		"job_id": job.JobID, "objective": "bounded job objective for " + job.JobID,
		"stopping_condition":   "the recorded checks pass with no unresolved reference",
		"checks":               []string{"go test ./internal/store/"},
		"reserved_integration": "integration evidence binds at the parent effect step",
		"ready":                true, "readiness_evidence": []string{"evidence:coordinator-ready"},
	}
	// A size-capable pin takes the per-job scope the size members author,
	// so coexisting jobs never share measured paths; earlier pins keep the
	// shared store scope.
	fields["path_scope"] = []string{"internal/store/" + job.JobID}
	if err := recordWorkerJobFieldsForTest(t, s, workID, actor, fields); err != nil {
		t.Fatalf("record worker-job %s: %v", job.JobID, err)
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
}

// readyWorkerJobForTest records one ready worker-job revision when the
// pinned definition runs the worker-job lifecycle (CD-0205) and returns the
// packet binding a dispatch of that revision carries as inputs.worker_job. An
// older pin returns nil and dispatches unbound. Record before building the
// packet: the recording advances the work version the packet binds.
func readyWorkerJobForTest(t *testing.T, s *Store, workID string, definition WorkflowDefinition, actor WorkflowActor, jobID string) map[string]any {
	t.Helper()
	if !workflowWorkerJobsActive(definition) {
		return nil
	}
	job := &WorkerJobBinding{JobID: jobID, Revision: 1}
	recordWorkerJobRevisionForTest(t, s, workID, actor, job)
	return recordedPacketJobForTest(t, s, workID, *job, "implement")
}

func recordWorkerJobActionForTest(t *testing.T, s *Store, workID string, actor WorkflowActor, fields map[string]any) error {
	t.Helper()
	return runVerdictActionAs(t, s, workID, "record_worker_job", mustJSONValue(fields), 0, actor)
}

// applyWorkerJobEventForTest appends one raw worker.job_recorded event
// through the workflow-authority test route at the work's current version, so
// a test can reach the fold's own refusals beneath the action's derivation —
// the boundary a replayed log meets.
func applyWorkerJobEventForTest(t *testing.T, s *Store, workID string, payload WorkerJobRecordedPayload) error {
	t.Helper()
	version := readWorkVersion(t, s, workID)
	resulting := version + 1
	payload.WorkflowVersionFields = WorkflowVersionFields{WorkID: workID, ExpectedVersion: &version, ResultingVersion: &resulting}
	event := Event{EventID: "job-record-" + payload.JobID + "-" + fmt.Sprint(payload.Revision) + "-" + fmt.Sprint(version), Kind: WorkerJobRecorded, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(19, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(payload)}
	return applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}})
}

// completeAndAcceptAttempt moves the attempt to completed and accepts its
// result at the named step.
func completeAndAcceptAttempt(t *testing.T, s *Store, workID, attemptID string, lane LaneDefinition, acceptor WorkflowActor) {
	t.Helper()
	dispatched, err := workflowDispatchedJobForAttempt(context.Background(), s.DatabaseForTesting(), workID, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if err := completeJobBoundAttemptForTest(s, workID, attemptID, lane, dispatched); err != nil {
		t.Fatalf("complete job-bound worker attempt %s: %v", attemptID, err)
	}
	if err := runVerdictActionAs(t, s, workID, "accept_worker_result", mustJSONValue(map[string]any{"attempt_id": attemptID, "attempt_epoch": latestStepStartEpoch(t, s, workID, "repair")}), 0, acceptor); err != nil {
		t.Fatalf("accept job-bound worker result %s: %v", attemptID, err)
	}
}

// completeJobBoundAttemptForTest appends the worker.completed report claiming
// the named worker-job revision. The recorded attempt owns the lane identity
// and the evidence obligations its report must discharge.
func completeJobBoundAttemptForTest(s *Store, workID, attemptID string, _ LaneDefinition, job *WorkerJobBinding) error {
	var laneID, laneDigest string
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT lane_id,lane_digest FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, attemptID).Scan(&laneID, &laneDigest); err != nil {
		return err
	}
	lane, err := LookupLane(laneID, laneDigest)
	if err != nil {
		return err
	}
	var directory string
	if err := s.DatabaseForTesting().QueryRow(`SELECT path FROM worktree_entries WHERE set_id=? AND state='active' ORDER BY project_id LIMIT 1`, WorktreeSetID(workID)).Scan(&directory); err != nil {
		return err
	}
	completed := jobBoundCompletionEvent(workID, "job-window-completed-"+attemptID, attemptID, lane, time.Unix(16, 0).UTC(), job, directory)
	return ApplyOperation(context.Background(), s, Operation{Events: []Event{completed}})
}

// attachReadyWorkerJobIfJobCapable records one ready worker-job revision on
// the named work when the pinned definition declares CD-0205 at the dispatch
// step, rewrites the packet's task and binding from the now-current recorded
// state, and attaches the recorded binding to the packet's inputs.worker_job.
// On every legacy pin the function leaves the packet untouched, so historic
// instance journeys dispatch with the byte-for-byte shape they were authored
// for. A job-capable pin with no recorded revision records one before the
// dispatch reads the work version the binding carries. The returned binding
// (nil on a legacy pin) names the worker.dispatched evidence the dispatch
// follows, so the lane-actor event can carry the same job the authorization
// recorded.
//
// The recorded job uses one job_id per (workID, stepID) so a sequence of
// dispatches at the same step accumulates revisions under one job, which is
// the CD-0205 D2 delivery admission the fold reads — the latest revision per
// job_id must be satisfied, so a sequence of dispatches cannot strand
// independent failed revisions alongside the satisfying accept.
func attachReadyWorkerJobIfJobCapable(t *testing.T, s *Store, workID, stepID string, actor WorkflowActor, packet map[string]any) *WorkerJobBinding {
	t.Helper()
	registered, err := VerifyWorkflowInstanceDefinition(context.Background(), s, BuiltinWorkflowRegistry(), workID)
	if err != nil {
		t.Fatal(err)
	}
	if !workflowWorkerJobsActive(registered.Definition) {
		return nil
	}
	if !stepDeclaresAction(registered.Definition, stepID, "record_worker_job") {
		return nil
	}
	jobID := "job:" + workID + ":" + stepID
	job := &WorkerJobBinding{JobID: jobID, Revision: 1}
	recordWorkerJobRevisionForTest(t, s, workID, actor, job)
	bindPacketToRecordedState(t, s, packet)
	packet["schema_version"] = WorkerPacketSchemaVersion
	packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, *job, "implement")
	return job
}

// The dispatch chain binds one recorded revision end to end (CD-0205): the
// packet must carry the selected ready revision with its recorded content,
// the worker.dispatched evidence must carry exactly the binding the
// authorization recorded, the report must claim that revision and no other,
// the job-bound accept holds the step as local acceptance, and a satisfied
// revision refuses a redispatch.
func TestWorkerJobDispatchRequiresRecordedRevision(t *testing.T) {
	const workID = "worker-job-recorded-revision-required"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	lane := reviewGateLane(t, "implementation")
	worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	job := &WorkerJobBinding{JobID: "job:record-core", Revision: 1}
	recordWorkerJobRevisionForTest(t, s, workID, worker, job)
	ready, err := readyWorkerJobRevisions(context.Background(), s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 1 || ready[0].Binding != *job || job.Digest == "" || ready[0].State != "recorded" || ready[0].ProjectScope == "" {
		t.Fatalf("ready revisions = %#v, want the one recorded revision with its derived digest and Project scope", ready)
	}

	packetWith := func(attemptID string, mutate func(map[string]any)) map[string]any {
		packet := dispatchWorkerPacket(t, s, workID, "repair", attemptID)
		packet["schema_version"] = WorkerPacketSchemaVersion
		if mutate != nil {
			mutate(packet["inputs"].(map[string]any))
		}
		return packet
	}
	refused := func(attemptID, want string, mutate func(map[string]any)) {
		t.Helper()
		err := dispatchJobPacketForTest(t, s, workID, attemptID, worker, "refused-"+attemptID, packetWith(attemptID, mutate))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("dispatch %s error = %v, want %q", attemptID, err, want)
		}
	}
	recorded := recordedPacketJobForTest(t, s, workID, *job, "implement")
	refused("attempt:"+workID+":unbound", "carries no inputs.worker_job", nil)
	refused("attempt:"+workID+":fabricated", "does not name a recorded worker-job revision", func(inputs map[string]any) {
		fabricated := map[string]any{}
		for k, v := range recorded {
			fabricated[k] = v
		}
		fabricated["job_id"] = "job:never-recorded"
		inputs["worker_job"] = fabricated
	})
	refused("attempt:"+workID+":swapped", "digest does not match the recorded revision", func(inputs map[string]any) {
		swapped := map[string]any{}
		for k, v := range recorded {
			swapped[k] = v
		}
		swapped["digest"] = "sha256:" + strings.Repeat("e", 64)
		inputs["worker_job"] = swapped
	})
	refused("attempt:"+workID+":rewritten", "content does not match the recorded revision", func(inputs map[string]any) {
		rewritten := map[string]any{}
		for k, v := range recorded {
			rewritten[k] = v
		}
		rewritten["objective"] = "a broader objective than the recorded job"
		inputs["worker_job"] = rewritten
	})

	// Dispatch evidence with no authorizing completion cannot attach a job.
	if err := appendJobBoundWorkerDispatch(t, s, workID, "attempt:"+workID+":unauthorized", job); err == nil || !strings.Contains(err.Error(), "does not match the worker job its dispatch_worker authorization bound") {
		t.Fatalf("unauthorized job-bound dispatch evidence error = %v, want the authorization-binding refusal", err)
	}

	attempt1 := "attempt:" + workID + ":1"
	if err := dispatchJobPacketForTest(t, s, workID, attempt1, worker, "record-real-1", packetWith(attempt1, func(inputs map[string]any) { inputs["worker_job"] = recorded })); err != nil {
		t.Fatalf("dispatch the recorded revision: %v", err)
	}
	// The evidence must carry the authorized binding, not another one.
	other := &WorkerJobBinding{JobID: job.JobID, Revision: job.Revision + 1, Digest: job.Digest}
	if err := appendJobBoundWorkerDispatch(t, s, workID, attempt1, other); err == nil || !strings.Contains(err.Error(), "does not match the worker job its dispatch_worker authorization bound") {
		t.Fatalf("substituted dispatch evidence error = %v, want the authorization-binding refusal", err)
	}
	recordJobBoundWorkerDispatch(t, s, workID, attempt1, job)

	// A re-recording that changes revision 1 in place refuses.
	tampered := WorkerJobRecordedPayload{JobID: job.JobID, Revision: job.Revision, ContractVersion: 1, Objective: "rewritten objective", StoppingCondition: "rewritten", PathScope: []string{}, PredicateIDs: []string{}, Checks: []string{}, Prerequisites: []WorkerJobPrerequisite{}, UnresolvedRefs: []string{}}
	tampered.Digest = DeriveWorkerJobDigest(tampered)
	if err := applyWorkerJobEventForTest(t, s, workID, tampered); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("tampered re-recording error = %v, want the immutability refusal", err)
	}

	// The report must claim exactly the dispatched revision.
	if err := completeJobBoundAttemptForTest(s, workID, attempt1, lane, nil); err == nil || !strings.Contains(err.Error(), "does not name the worker-job revision the attempt was dispatched under") {
		t.Fatalf("report without its job error = %v, want the report-binding refusal", err)
	}
	if err := completeJobBoundAttemptForTest(s, workID, attempt1, lane, other); err == nil || !strings.Contains(err.Error(), "does not name the worker-job revision the attempt was dispatched under") {
		t.Fatalf("report claiming a later revision error = %v, want the report-binding refusal", err)
	}

	// The job-bound accept is local acceptance: it satisfies the revision
	// and holds the parent step.
	acceptor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/record-acceptor", SessionRef: "session/" + workID + "-acceptor", ActorClass: ActorAgent}
	completeAndAcceptAttempt(t, s, workID, attempt1, lane, acceptor)
	if pin := issue1013Pin(t, s, workID); pin.Step != "repair" {
		t.Fatalf("step after the job-bound accept = %q, want repair held", pin.Step)
	}
	ready, err = readyWorkerJobRevisions(context.Background(), s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 0 {
		t.Fatalf("ready revisions after satisfaction = %#v, want none", ready)
	}
	attempt2 := "attempt:" + workID + ":redispatch"
	issue1013StartRepair(t, s, workID, worker, readWorkVersion(t, s, workID), latestStepStartEpoch(t, s, workID, "repair")+1)
	refused(attempt2, "is not ready", func(inputs map[string]any) { inputs["worker_job"] = recorded })
}

// A recorded job's relevant predicates derive their authority from the
// parent contract (CD-0205): the recording may name only predicates the
// active approved contract approved. Contract-version equality alone does
// not establish predicate authority — an undeclared predicate refuses the
// recording, so a job cannot claim relevance the core state does not hold.
func TestWorkerJobRecordingBindsApprovedContractPredicates(t *testing.T) {
	const workID = "worker-job-approved-predicates"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	payload := func(jobID string, predicates []string) WorkerJobRecordedPayload {
		p := WorkerJobRecordedPayload{
			JobID: jobID, Revision: 1, ContractVersion: 1, Objective: "bounded objective for " + jobID,
			StoppingCondition: "the recorded checks pass", PathScope: []string{"internal/store"},
			PredicateIDs: predicates, Checks: []string{"go test ./internal/store/"},
			Prerequisites: []WorkerJobPrerequisite{}, UnresolvedRefs: []string{},
			Readiness: &WorkerJobReadiness{Ready: true, Evidence: []string{"evidence:coordinator-ready"}},
		}
		p.Digest = DeriveWorkerJobDigest(p)
		return p
	}
	record := func(p WorkerJobRecordedPayload) error {
		t.Helper()
		return applyWorkerJobEventForTest(t, s, workID, p)
	}
	undeclared := payload("job:predicate-undeclared", []string{"predicate:never-approved"})
	if err := record(undeclared); err == nil || !strings.Contains(err.Error(), "did not approve") {
		t.Fatalf("undeclared predicate error = %v, want the predicate-authority refusal", err)
	}
	if err := record(payload("job:predicate-approved", []string{"predicate:return-route"})); err != nil {
		t.Fatalf("record revision under an approved predicate: %v", err)
	}
}

// Recording derives its parent authority from core state and its dispatch
// admissibility from the recorded revision alone (CD-0205): a contract
// version the work item does not hold refuses the recording, a path scope
// that escapes the repository refuses at the shape boundary, and a revision
// without asserted coordinator readiness, with open unresolved references,
// or behind an unsatisfied prerequisite is recorded but never admissible.
func TestWorkerJobRecordingAuthorityAndReadinessGates(t *testing.T) {
	const workID = "worker-job-authority-readiness-gates"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	basePayload := func(jobID string, mutate func(*WorkerJobRecordedPayload)) WorkerJobRecordedPayload {
		payload := WorkerJobRecordedPayload{
			JobID: jobID, Revision: 1, ContractVersion: 1, Objective: "bounded objective for " + jobID,
			StoppingCondition: "the recorded checks pass", PathScope: []string{"internal/store"},
			PredicateIDs: []string{}, Checks: []string{"go test ./internal/store/"},
			Prerequisites: []WorkerJobPrerequisite{}, UnresolvedRefs: []string{},
			Readiness: &WorkerJobReadiness{Ready: true, Evidence: []string{"evidence:coordinator-ready"}},
		}
		if mutate != nil {
			mutate(&payload)
		}
		payload.Digest = DeriveWorkerJobDigest(payload)
		return payload
	}
	record := func(t *testing.T, payload WorkerJobRecordedPayload) error {
		t.Helper()
		return applyWorkerJobEventForTest(t, s, workID, payload)
	}
	wrongContract := basePayload("job:gate-contract", func(p *WorkerJobRecordedPayload) { p.ContractVersion = 2 })
	if err := record(t, wrongContract); err == nil || !strings.Contains(err.Error(), "does not match the active parent contract") {
		t.Fatalf("wrong contract version error = %v, want the parent-contract refusal", err)
	}
	escapingPath := basePayload("job:gate-escape", func(p *WorkerJobRecordedPayload) { p.PathScope = []string{"internal/store", "../adapter"} })
	if err := record(t, escapingPath); err == nil || !strings.Contains(err.Error(), "normalized relative paths") {
		t.Fatalf("escaping path scope error = %v, want the containment refusal", err)
	}
	absolutePath := basePayload("job:gate-absolute", func(p *WorkerJobRecordedPayload) { p.PathScope = []string{"/etc"} })
	if err := record(t, absolutePath); err == nil || !strings.Contains(err.Error(), "normalized relative paths") {
		t.Fatalf("absolute path scope error = %v, want the containment refusal", err)
	}
	if err := record(t, basePayload("job:gate-ready", nil)); err != nil {
		t.Fatalf("record ready revision: %v", err)
	}
	if err := record(t, basePayload("job:gate-noreadiness", func(p *WorkerJobRecordedPayload) { p.Readiness = nil })); err != nil {
		t.Fatalf("record revision without readiness: %v", err)
	}
	if err := record(t, basePayload("job:gate-unreadiness", func(p *WorkerJobRecordedPayload) {
		p.Readiness = &WorkerJobReadiness{Ready: false, Evidence: []string{"evidence:blocked"}}
	})); err != nil {
		t.Fatalf("record not-ready revision: %v", err)
	}
	if err := record(t, basePayload("job:gate-unresolved", func(p *WorkerJobRecordedPayload) { p.UnresolvedRefs = []string{"contract:other:9"} })); err != nil {
		t.Fatalf("record revision with unresolved references: %v", err)
	}
	if err := record(t, basePayload("job:gate-prereq", func(p *WorkerJobRecordedPayload) {
		p.Prerequisites = []WorkerJobPrerequisite{{JobID: "job:gate-noreadiness", Revision: 1}}
	})); err != nil {
		t.Fatalf("record revision behind an unsatisfied prerequisite: %v", err)
	}
	ready, err := readyWorkerJobRevisions(context.Background(), s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	readyIDs := make(map[string]bool)
	for _, view := range ready {
		readyIDs[view.Binding.JobID] = true
	}
	if !readyIDs["job:gate-ready"] || len(readyIDs) != 1 {
		t.Fatalf("ready revisions = %v, want only job:gate-ready", readyIDs)
	}
}

// Local acceptance at the delivery-admitting refinement step (CD-0205): on
// the job-capable definitions a plain accept of a job-bound attempt is
// admitted without the delivery proof, satisfies only its recorded revision,
// and holds refine. A plain accept of an attempt dispatched without a job
// binding still refuses, because refine exits only through an admitted
// delivery assertion, and the delivery-asserting accept keeps the complete
// delivery admission and advances with applicable producer proof.
func TestWorkerJobLocalAcceptAtRefineHoldsWithoutDelivery(t *testing.T) {
	for _, family := range []string{"workflow.implementation", "workflow.break_fix"} {
		t.Run(family, func(t *testing.T) {
			const workID = "worker-job-refine-local-accept"
			f := acceptDeliveryFixtureFor(t, workID, family, "refine", "start_refine")
			s := f.fixture.store
			defer s.Close()
			owner := f.fixture.owner
			acceptor := reviewGateAcceptor(workID)
			lane := reviewGateLane(t, "implementation")
			reviewGateStartStep(t, s, workID, "refine", "start_refine", owner)
			epoch := func() int64 { return latestStepStartEpoch(t, s, workID, "refine") }

			local := &WorkerJobBinding{JobID: "job:refine-local", Revision: 1}
			localAttempt := "attempt:" + workID + ":local"
			dispatchJobBoundAttempt(t, s, workID, "refine", localAttempt, nil, owner, 0, "refine-local", local)
			if err := completeJobBoundAttemptForTest(s, workID, localAttempt, lane, local); err != nil {
				t.Fatal(err)
			}
			if err := runVerdictActionAs(t, s, workID, "accept_worker_result", mustJSONValue(map[string]any{"attempt_id": localAttempt, "attempt_epoch": epoch()}), 0, acceptor); err != nil {
				t.Fatalf("local accept of the job-bound attempt at refine: %v", err)
			}
			reviewGateRequireStep(t, s, workID, "refine")
			views, err := readWorkerJobRevisions(context.Background(), s.db, workID)
			if err != nil {
				t.Fatal(err)
			}
			if len(views) != 1 || views[0].State != "satisfied" || views[0].Binding != *local {
				t.Fatalf("revisions after the local accept = %#v, want the one satisfied revision", views)
			}
			if seq, err := workflowLatestDeliveryAssertionSeq(context.Background(), s, workID); err != nil || seq != 0 {
				t.Fatalf("delivery assertion after the local accept = %d, %v; want none", seq, err)
			}

			ownerRef, err := WorkflowActorRef(owner)
			if err != nil {
				t.Fatal(err)
			}
			unbound := "attempt:" + workID + ":unbound-implementation"
			reviewGateRunAttempt(t, s, workID, unbound, "refine", lane, ownerRef, 200)
			if err := runVerdictActionAs(t, s, workID, "accept_worker_result", mustJSONValue(map[string]any{"attempt_id": unbound, "attempt_epoch": epoch()}), 0, acceptor); err == nil || !strings.Contains(err.Error(), "exits only through an admitted delivery assertion") {
				t.Fatalf("plain accept of an unbound attempt at refine error = %v, want the delivery-assertion refusal", err)
			}

			oracleFixture := oracleRepairFixtureFrom(t, workID, f.fixture)
			oAcceptOracleFixtureReview(t, oracleFixture, workID, "continue")
			finalAttempt := oCompleteOracleFixtureReview(t, oracleFixture, workID, "ready_for_final_proof")
			finalEpoch := epoch()
			asserted := mustJSONValue(map[string]any{"attempt_id": finalAttempt, "attempt_epoch": finalEpoch, "delivery_artifact": "artifact:accept-delivery-" + workID, "delivery_state": "asserted"})
			refineProofCandidateRun(t, s, workID, "refine-final", refineProofCandidate(t, s, workID), []string{"true"})
			if err := runVerdictActionAs(t, s, workID, "accept_worker_result", asserted, 0, acceptor); err != nil {
				t.Fatalf("delivery-asserting accept with the green refine run: %v", err)
			}
			reviewGateRequireStep(t, s, workID, "delivery")
		})
	}
}

// testWorkPinCallerCapable reports whether the work's pinned definition
// declares the direct_caller_allowance member, so the packet fixture carries
// the caller exclusions exactly where the pin's declared dispatch packet
// schema admits them.
func testWorkPinCallerCapable(t *testing.T, s *Store, workID string) bool {
	t.Helper()
	var ref string
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref FROM workflow_instances WHERE work_id=?`, workID).Scan(&ref); err != nil {
		t.Fatalf("read pinned definition for %s: %v", workID, err)
	}
	registered, ok := BuiltinWorkflowRegistry().Current(ref)
	if !ok {
		t.Fatalf("current definition %s is not registered", ref)
	}
	return workflowCallerScopeActive(registered.Definition)
}

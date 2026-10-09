package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The correction window keeps its anchor when an unrelated accepted job
// arrives (CD-0205 refining CD-0164 D2): a job-bound failed attempt leaves
// the window open until an acceptance whose own attempt was dispatched under
// the same job identity. An acceptance under a different job_id never opens
// a new window, so the counted dispatches and the retry budget stay intact.
func TestUnrelatedAcceptedJobPreservesCorrectionWindow(t *testing.T) {
	const workID = "correction-window-unrelated-job"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s, owner := fixture.store, fixture.owner
	defer s.Close()
	lane := reviewGateLane(t, "implementation")
	worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	workerRef, err := WorkflowActorRef(worker)
	if err != nil {
		t.Fatal(err)
	}
	workerVersion := verdictItemVersion(t, s, workID)
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
	failWorkerAttemptWithKind(t, s, workID, attempt1, "lane fallback blocked before the model ran")
	applyRecordWorkerFailureForTest(t, s, workID, owner, attempt1, latestStepStartEpoch(t, s, workID, "repair"), readWorkVersion(t, s, workID), "job-window-f1")
	pin := issue1013Pin(t, s, workID)
	if pin.Correction == nil || pin.Correction.AttemptCount != 1 || pin.Correction.Escalated {
		t.Fatalf("correction after one failed job-bound attempt = %#v, want one counted attempt", pin.Correction)
	}

	// An accepted result under a different job identity must not open a new
	// counting window: the failed job stays unresolved, so the boundary and
	// the counted dispatches stay. Both dispatches sit in the preserved
	// window (CD-0164 D1 counts every dispatch), and the unrelated
	// acceptance resets nothing.
	attempt2 := "attempt:" + workID + ":2"
	issue1013StartRepair(t, s, workID, worker, pin.Version, latestStepStartEpoch(t, s, workID, "repair")+1)
	pin = issue1013Pin(t, s, workID)
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt2, pin.Correction, worker, pin.Version, "job-window-2", unrelatedJob)
	completeAndAcceptAttempt(t, s, workID, attempt2, lane, acceptor)
	if count := jobWindowCorrectionCount(t, s, workID); count != 2 {
		t.Fatalf("correction count after the unrelated acceptance = %d, want 2 dispatches in the preserved window", count)
	}

	// The pin-level anchor keeps the failed job's window open: the unrelated
	// job's dispatch does not consume the correction record, so the owning
	// derivation the pin reads still returns the corrective retry binding,
	// its counted dispatches, and the escalation state the boundary walk
	// derived. The pin's step-scoped view may have advanced with the accept,
	// so the witness reads the anchor at the record's own step.
	anchor, anchorErr := workflowCorrectionContextForDispatch(context.Background(), s.DatabaseForTesting(), workID, "repair", "")
	if anchorErr != nil || anchor == nil || anchor.Escalated || anchor.FailedAttemptID != attempt1 {
		t.Fatalf("anchor correction after the unrelated acceptance = (%#v, %v), want the open window on %s", anchor, anchorErr, attempt1)
	}

	// The preserved budget is real: the third dispatch in the same window
	// would reach the unchanged three-attempt escalation comparator with
	// the count this window carries, not a reset one. The comparator itself
	// is CD-0164 law this change does not touch, so the count is the
	// witness the wall reads.
}

// Only an accepted result under the recorded corrective job identity closes
// the window (CD-0205): a later revision of the same job_id satisfies it,
// and the next failure counts from one in a fresh window.
func TestSatisfyingAcceptedJobResetsCorrectionWindow(t *testing.T) {
	const workID = "correction-window-satisfying-job"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s, owner := fixture.store, fixture.owner
	defer s.Close()
	lane := reviewGateLane(t, "implementation")
	worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	workerRef, err := WorkflowActorRef(worker)
	if err != nil {
		t.Fatal(err)
	}
	workerVersion := verdictItemVersion(t, s, workID)
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
	failWorkerAttemptWithKind(t, s, workID, attempt1, "lane fallback blocked before the model ran")
	applyRecordWorkerFailureForTest(t, s, workID, owner, attempt1, latestStepStartEpoch(t, s, workID, "repair"), readWorkVersion(t, s, workID), "job-satisfy-f1")
	pin := issue1013Pin(t, s, workID)
	if pin.Correction == nil || pin.Correction.AttemptCount != 1 {
		t.Fatalf("correction after one failed job-bound attempt = %#v, want one counted attempt", pin.Correction)
	}
	prefixAfterFirstFailure := maxEventSeq(t, s, workID)

	// A later revision of the same job identity satisfies the corrective
	// job, so the acceptance opens a fresh window exactly as CD-0164 D2
	// requires a satisfying acceptance to.
	attempt2 := "attempt:" + workID + ":2"
	issue1013StartRepair(t, s, workID, worker, pin.Version, latestStepStartEpoch(t, s, workID, "repair")+1)
	pin = issue1013Pin(t, s, workID)
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt2, pin.Correction, worker, pin.Version, "job-satisfy-2", correctiveJobRevision2)
	completeAndAcceptAttempt(t, s, workID, attempt2, lane, acceptor)
	pin = issue1013Pin(t, s, workID)
	if pin.Correction != nil {
		t.Fatalf("satisfying acceptance left the correction context: %#v", pin.Correction)
	}
	// CD-0201 parity: the boundary derivation reads only the walked prefix,
	// so recounting at the historical prefix captured after the first
	// failure must return the count the live derivation returned then, not
	// a value contaminated by the later failed dispatch.
	if count := jobWindowCorrectionCount(t, s, workID, prefixAfterFirstFailure); count != 1 {
		t.Fatalf("prefix recount after later failures = %d, want the 1 dispatch the prefix holds", count)
	}

	attempt3 := "attempt:" + workID + ":3"
	issue1013StartRepair(t, s, workID, worker, pin.Version, latestStepStartEpoch(t, s, workID, "repair")+1)
	pin = issue1013Pin(t, s, workID)
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt3, nil, worker, pin.Version, "job-satisfy-3", correctiveJobRevision3)
	failWorkerAttemptWithKind(t, s, workID, attempt3, "lane fallback blocked before the model ran")
	applyRecordWorkerFailureForTest(t, s, workID, owner, attempt3, latestStepStartEpoch(t, s, workID, "repair"), readWorkVersion(t, s, workID), "job-satisfy-f3")
	pin = issue1013Pin(t, s, workID)
	if pin.Correction == nil || pin.Correction.AttemptCount != 1 || pin.Correction.Escalated {
		t.Fatalf("correction after the satisfying acceptance = %#v, want one counted attempt in a fresh window", pin.Correction)
	}
}

// Several job-bound failures each stay unresolved until their own recorded
// disposition names them (CD-0205): satisfying one of two unresolved jobs
// leaves the other's window — and the counted dispatches — exactly intact.
func TestPartialSatisfactionPreservesOtherUnresolvedJobs(t *testing.T) {
	const workID = "correction-window-partial-satisfaction"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s, owner := fixture.store, fixture.owner
	defer s.Close()
	worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	workerRef, err := WorkflowActorRef(worker)
	if err != nil {
		t.Fatal(err)
	}
	workerVersion := verdictItemVersion(t, s, workID)
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
	failWorkerAttemptWithKind(t, s, workID, attempt1, "lane fallback blocked before the model ran")
	applyRecordWorkerFailureForTest(t, s, workID, owner, attempt1, latestStepStartEpoch(t, s, workID, "repair"), readWorkVersion(t, s, workID), "job-partial-f1")
	pin := issue1013Pin(t, s, workID)
	issue1013StartRepair(t, s, workID, worker, pin.Version, latestStepStartEpoch(t, s, workID, "repair")+1)
	pin = issue1013Pin(t, s, workID)

	attempt2 := "attempt:" + workID + ":2"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt2, pin.Correction, worker, pin.Version, "job-partial-2", jobB)
	failWorkerAttemptWithKind(t, s, workID, attempt2, "lane fallback blocked before the model ran")
	applyRecordWorkerFailureForTest(t, s, workID, owner, attempt2, latestStepStartEpoch(t, s, workID, "repair"), readWorkVersion(t, s, workID), "job-partial-f2")

	// Satisfying job A leaves job B unresolved, so the window keeps every
	// counted dispatch: the two failures and the satisfying dispatch.
	attempt3 := "attempt:" + workID + ":3"
	pin = issue1013Pin(t, s, workID)
	issue1013StartRepair(t, s, workID, worker, pin.Version, latestStepStartEpoch(t, s, workID, "repair")+1)
	pin = issue1013Pin(t, s, workID)
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt3, pin.Correction, worker, pin.Version, "job-partial-3", jobA)
	completeAndAcceptAttempt(t, s, workID, attempt3, reviewGateLane(t, "implementation"), acceptor)
	if count := jobWindowCorrectionCount(t, s, workID); count != 3 {
		t.Fatalf("correction count after partially satisfying acceptance = %d, want 3 dispatches in the preserved window", count)
	}
	// The pin-level anchor carries the window by unresolved job identity:
	// job B's failure record is not consumed by job A's dispatch, so the pin
	// still holds the open correction with every counted dispatch — the two
	// failures and the satisfying dispatch.
	// The pin-level anchor carries the window by unresolved job identity:
	// job B's failure record is not consumed by job A's dispatch or its
	// acceptance, so the owning derivation still returns the open correction
	// with every counted dispatch — the two failures and the satisfying
	// dispatch. The pin's step-scoped view advanced past repair with the
	// accept, so the witness reads the anchor the pin reads at its step.
	correction, corrErr := workflowCorrectionContextForDispatch(context.Background(), s.DatabaseForTesting(), workID, "repair", "")
	if corrErr != nil || correction == nil || correction.AttemptCount != 3 || correction.FailedAttemptID != attempt2 {
		t.Fatalf("anchor correction after partially satisfying acceptance = (%#v, %v), want the open window on %s with 3 counted dispatches", correction, corrErr, attempt2)
	}
}

// jobWindowCorrectionCount reads the correction counting window through the
// real derivation, so a test can assert the boundary itself and not only the
// pin view built on it. The optional seq pins the walked prefix: deriving the
// count at a historical prefix must match what the live derivation returned
// at that point in the log (CD-0201 parity).
func jobWindowCorrectionCount(t *testing.T, s *Store, workID string, prefix ...int64) int64 {
	t.Helper()
	seq := int64(1) << 62
	if len(prefix) > 0 {
		seq = prefix[0]
	}
	count, err := workflowCorrectionAttemptCount(context.Background(), s.DatabaseForTesting(), workID, seq, "workflow_correction")
	if err != nil {
		t.Fatalf("count correction attempts for %s: %v", workID, err)
	}
	return count
}

// maxEventSeq returns the current last sequence of the work item's event
// log, so a test can pin the prefix a later derivation walk replays.
func maxEventSeq(t *testing.T, s *Store, workID string) int64 {
	t.Helper()
	var seq int64
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT COALESCE(MAX(seq),0) FROM domain_events WHERE subject_type=? AND subject_id=?`, string(SubjectWorkItem), workID).Scan(&seq); err != nil {
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
	packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, *job)
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
func recordedPacketJobForTest(t *testing.T, s *Store, workID string, job WorkerJobBinding) map[string]any {
	t.Helper()
	views, err := s.WorkerJobRevisions(context.Background(), workID)
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range views {
		if view.Binding == job {
			var out map[string]any
			if err := json.Unmarshal(mustJSONValue(packetJobFromView(view)), &out); err != nil {
				t.Fatal(err)
			}
			return out
		}
	}
	t.Fatalf("worker job %#v is not recorded", job)
	return nil
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
	lane := reviewGateLane(t, "implementation")
	var packetDigest string
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT json_extract(payload,'$.worker_packet_digest') FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=? ORDER BY seq DESC LIMIT 1`, string(SubjectWorkItem), workID, WorkflowActionCompleted, attemptID).Scan(&packetDigest); err != nil {
		packetDigest = "sha256:" + strings.Repeat("d", 64)
	}
	dispatch := Event{EventID: "job-window-dispatch-" + workID + "-" + attemptID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(20, 0).UTC(), PayloadVersion: 2, Payload: mustJSONValue(WorkerDispatchedPayload{
		AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, PacketDigest: packetDigest,
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

// recordWorkerJobRevisionForTest records one worker-job revision through the
// record_worker_job action, the authoring route a coordinator uses. The core
// derives the revision, the parent authority, and the digest; the binding is
// filled from the recorded revision. On an oracle-capable pin the recording
// carries the fixture oracle the definition requires.
func recordWorkerJobRevisionForTest(t *testing.T, s *Store, workID string, actor WorkflowActor, job *WorkerJobBinding) {
	t.Helper()
	fields := map[string]any{
		"job_id": job.JobID, "objective": "bounded job objective for " + job.JobID,
		"stopping_condition": "the recorded checks pass with no unresolved reference",
		"path_scope":         []string{"internal/store"}, "checks": []string{"go test ./internal/store/"},
		"reserved_integration": "integration evidence binds at the parent effect step",
		"ready":                true, "readiness_evidence": []string{"evidence:coordinator-ready"},
	}
	if oracleCapablePinForWork(t, s, workID) {
		// The oracle-bearing revision seeds trusted preparations against the
		// work's qualified subject; the bootstrap is idempotent and leaves an
		// already-qualified subject untouched.
		bootstrapOracleFixtureSubject(t, s, workID)
		fields["acceptance_oracle"] = acceptanceOracleFieldsForTest(t, s, workID)
	}
	if err := recordWorkerJobActionForTest(t, s, workID, actor, fields); err != nil {
		t.Fatalf("record worker-job %s: %v", job.JobID, err)
	}
	views, err := s.WorkerJobRevisions(context.Background(), workID)
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
	return recordedPacketJobForTest(t, s, workID, *job)
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
	var laneVersion int64
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT lane_id,lane_version,lane_digest FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, attemptID).Scan(&laneID, &laneVersion, &laneDigest); err != nil {
		return err
	}
	lane, err := LookupLane(laneID, laneVersion, laneDigest)
	if err != nil {
		return err
	}
	completed := jobBoundCompletionEvent(workID, "job-window-completed-"+attemptID, attemptID, lane, time.Unix(16, 0).UTC(), job)
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
	packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, *job)
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
	ready, err := s.ReadyWorkerJobRevisions(context.Background(), workID)
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
	recorded := recordedPacketJobForTest(t, s, workID, *job)
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
	ready, err = s.ReadyWorkerJobRevisions(context.Background(), workID)
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
	ready, err := s.ReadyWorkerJobRevisions(context.Background(), workID)
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
// delivery admission: it refuses without the green refine run and advances
// to the delivery gate with it.
func TestWorkerJobLocalAcceptAtRefineHoldsWithoutDelivery(t *testing.T) {
	for _, family := range []struct {
		ref     string
		version int64
	}{
		{"workflow.implementation", 24},
		{"workflow.break_fix", 21},
	} {
		t.Run(family.ref, func(t *testing.T) {
			const workID = "worker-job-refine-local-accept"
			f := acceptDeliveryFixtureFor(t, workID, family.ref, family.version, "refine", "start_refine")
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
			views, err := s.WorkerJobRevisions(context.Background(), workID)
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
			unbound := "attempt:" + workID + ":unbound-review"
			reviewGateRunAttempt(t, s, workID, unbound, "refine", epoch(), reviewGateLane(t, "review"), ownerRef, 200)
			if err := runVerdictActionAs(t, s, workID, "accept_worker_result", mustJSONValue(map[string]any{"attempt_id": unbound, "attempt_epoch": epoch()}), 0, acceptor); err == nil || !strings.Contains(err.Error(), "exits only through an admitted delivery assertion") {
				t.Fatalf("plain accept of an unbound attempt at refine error = %v, want the delivery-assertion refusal", err)
			}

			final := &WorkerJobBinding{JobID: "job:refine-final", Revision: 1}
			finalAttempt := "attempt:" + workID + ":final"
			dispatchJobBoundAttempt(t, s, workID, "refine", finalAttempt, nil, owner, 0, "refine-final", final)
			if err := completeJobBoundAttemptForTest(s, workID, finalAttempt, lane, final); err != nil {
				t.Fatal(err)
			}
			asserted := mustJSONValue(map[string]any{"attempt_id": finalAttempt, "attempt_epoch": epoch(), "delivery_artifact": "artifact:accept-delivery-" + workID, "delivery_state": "asserted"})
			if err := runVerdictActionAs(t, s, workID, "accept_worker_result", asserted, 0, acceptor); err == nil {
				t.Fatal("delivery-asserting accept admitted without the green refine run")
			}
			reviewGateRequireStep(t, s, workID, "refine")
			if err := acceptRefineResult(t, s, workID, finalAttempt, epoch(), acceptor); err != nil {
				t.Fatalf("delivery-asserting accept with the green refine run: %v", err)
			}
			reviewGateRequireStep(t, s, workID, "delivery")
		})
	}
}

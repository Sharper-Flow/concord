package store

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

// The workflow failure record derives from the attempt's own terminal failed
// event exactly once, and a rejected result can carry its retry dispatch
// through the one dispatch owner (CD-0198 D5). Every expectation that names
// the derived behavior is the suite's acceptance clause; every released-pin
// expectation is the frozen boundary the suite must not move.

// s2WorkerFailedLive ingests one live worker.failed event through the
// store-owned worker-evidence seam — the boundary the worker-fail and
// worker-abandon CLI verbs share — so the terminal fact and whatever the
// engine derives from it commit in one transaction.
func s2WorkerFailedLive(t *testing.T, s *Store, event Event) error {
	t.Helper()
	return s.Transact(context.Background(), func(transaction *Transaction) error {
		_, err := ApplyWorkerEvidenceOperationTx(context.Background(), transaction, event)
		return err
	})
}

func s2WorkerFailedEvent(workID, attemptID, eventID, detail, model string) Event {
	return Event{EventID: eventID, Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(30, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(WorkerFailedPayload{AttemptID: attemptID, ReadbackModel: model, FailureKind: WorkerFailureWorkerError, Detail: detail})}
}

// s2WorkerFailedAttributedEvent builds one current-version worker.failed
// event carrying the recorded producer observation exactly as the
// authenticated CLI assembles it: origin and observation only, never an
// identity — the live ingest derives the attribution identity from the
// attempt's own authorized dispatch window. The observation is the suite's
// truthful unknown synthetic: the host boundary observed the failure, cause
// and basis unknown, citing the one host event it saw.
func s2WorkerFailedAttributedEvent(workID, attemptID, eventID, model string, failureKind, detail string, occurredAt time.Time) Event {
	payload := WorkerFailedPayload{AttemptID: attemptID, ReadbackModel: model, FailureKind: failureKind, Detail: detail}
	payload.FailureAttribution = &WorkerFailureAttribution{Origin: WorkerFailureAttributionRecorded, Observation: workerFailureObservationFixture()}
	return Event{EventID: eventID, Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: occurredAt, PayloadVersion: WorkerEvidenceEventPayloadVersion(WorkerFailed), Payload: mustJSONValue(payload)}
}

// s2WorkerFailedAttributedLive records the counting journeys' terminal fact
// through the live worker-evidence seam with the recorded observation the
// promoted pin requires. The pinned definition derives the workflow failure
// record from this terminal event in the same commit, exactly once, so the
// explicit record disposition the counting helpers used to apply beside it
// is replaced, never duplicated.
func s2WorkerFailedAttributedLive(t *testing.T, s *Store, workID, attemptID, detail string) {
	t.Helper()
	event := s2WorkerFailedAttributedEvent(workID, attemptID, "failed-"+workID+"-"+attemptID, preferredModelForLane(BuiltinLaneDefinitions()[0]), WorkerFailureFallbackBlocked, detail, time.Unix(3, 0).UTC())
	if err := s2WorkerFailedLive(t, s, event); err != nil {
		t.Fatal(err)
	}
}

// s2DerivedFailureRecords counts the recorded workflow failure records for
// one attempt and returns their step, epoch, actor, and event id.
func s2DerivedFailureRecords(t *testing.T, s *Store, workID, attemptID string) []map[string]any {
	t.Helper()
	rows, err := s.DatabaseForTesting().Query(`SELECT json_extract(payload,'$.step_id'),json_extract(payload,'$.attempt_epoch'),json_extract(payload,'$.actor_ref'),event_id FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='record_worker_failure' AND json_extract(payload,'$.worker_attempt_id')=? ORDER BY seq`, workID, WorkflowActionCompleted, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var found []map[string]any
	for rows.Next() {
		var step, actor, eventID string
		var epoch int64
		if err := rows.Scan(&step, &epoch, &actor, &eventID); err != nil {
			t.Fatal(err)
		}
		found = append(found, map[string]any{"step": step, "epoch": epoch, "actor": actor, "event_id": eventID})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return found
}

func mustActorRef(t *testing.T, actor WorkflowActor) string {
	t.Helper()
	ref, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

// TestDerivedWorkerFailureRecordsOnceFromTheOriginalTerminalEvent proves the
// CD-0198 D5 derivation: one terminal worker.failed event on a derived-failure
// pin lands with exactly one workflow failure record derived from the
// attempt's own terminal event — the correction reads the failure detail and
// the standing default strategy — and neither a duplicate terminal event nor
// an exact replay of the original one produces a second record. Rebuild from
// the log reaches the same record.
func TestDerivedWorkerFailureRecordsOnceFromTheOriginalTerminalEvent(t *testing.T) {
	t.Parallel()
	const workID = "s2-derived-failure-once"
	// The derived-failure head that predates the attribution requirement:
	// this suite pins the released derivation semantics, and the terminal
	// events it records carry no producer observation.
	fixture := seedHistoricalWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", 27, "repair")
	s := fixture.store
	dispatcher := diagnosisS1Actor(workID, "dispatcher")
	lane := reviewGateLane(t, "implementation")
	attempt := "attempt:" + workID + ":1"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt, nil, dispatcher, 0, "s2-derive-dispatch-"+workID, &WorkerJobBinding{JobID: "job:" + workID})
	if starts := workflowStartedEventsAtStep(t, s, workID, "repair"); len(starts) != 1 || starts[0].Epoch != 1 {
		t.Fatalf("repair starts after the dispatch = %+v, want one dispatch start at epoch 1", starts)
	}
	detail := "the lane failed before a judgeable result existed"
	if err := s2WorkerFailedLive(t, s, s2WorkerFailedEvent(workID, attempt, "failed-"+workID+"-1", detail, preferredModelForLane(lane))); err != nil {
		t.Fatalf("the live terminal worker.failed ingest: %v", err)
	}
	records := s2DerivedFailureRecords(t, s, workID, attempt)
	if len(records) != 1 {
		t.Fatalf("derived failure records = %d, want exactly one from the original terminal event", len(records))
	}
	record := records[0]
	if record["step"] != "repair" || record["epoch"] != int64(1) {
		t.Fatalf("derived record = %+v, want the repair step at the window's epoch 1", record)
	}
	if record["actor"] != mustActorRef(t, dispatcher) {
		t.Fatalf("derived record actor = %v, want the dispatch authorization's actor", record["actor"])
	}
	if !strings.HasSuffix(record["event_id"].(string), ":workflow-failure") {
		t.Fatalf("derived record event id = %v, want the terminal event's derivation pair", record["event_id"])
	}
	// The correction context reads the derived record exactly as it read the
	// recorded one: failed disposition, the failure detail as diagnosis, the
	// standing default strategy.
	correction, err := workflowCorrectionContext(context.Background(), s.db, workID, "repair")
	if err != nil {
		t.Fatal(err)
	}
	if correction == nil || correction.Disposition != "failed" || correction.Diagnosis != detail || correction.Strategy != "retry with a fresh fenced attempt" || correction.FailedAttemptID != attempt {
		t.Fatalf("correction after the derived record = %+v, want the failed disposition carrying the original failure detail and the standing default strategy", correction)
	}
	// A duplicate terminal event for the same attempt refuses and leaves the
	// single record intact.
	if err := s2WorkerFailedLive(t, s, s2WorkerFailedEvent(workID, attempt, "failed-"+workID+"-1-duplicate", detail, preferredModelForLane(lane))); err == nil {
		t.Fatal("a duplicate terminal worker.failed event for a failed attempt was admitted")
	}
	if got := s2DerivedFailureRecords(t, s, workID, attempt); len(got) != 1 {
		t.Fatalf("derived failure records after the duplicate = %d, want one", len(got))
	}
	// An exact replay of the original terminal event cannot append a second
	// derivation either.
	if err := s2WorkerFailedLive(t, s, s2WorkerFailedEvent(workID, attempt, "failed-"+workID+"-1", detail, preferredModelForLane(lane))); err == nil {
		t.Fatal("an exact-replay terminal event appended twice")
	}
	if got := s2DerivedFailureRecords(t, s, workID, attempt); len(got) != 1 {
		t.Fatalf("derived failure records after the exact replay = %d, want one", len(got))
	}
	// Rebuild from the log folds the stored derivation to the same record.
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("rebuild from the log: %v", err)
	}
	rebuilt := s2DerivedFailureRecords(t, s, workID, attempt)
	if len(rebuilt) != 1 || rebuilt[0]["step"] != "repair" || rebuilt[0]["actor"] != record["actor"] {
		t.Fatalf("rebuilt failure records = %+v, want the one derived record rebuilt identically", rebuilt)
	}
	rebuiltCorrection, err := workflowCorrectionContext(context.Background(), s.db, workID, "repair")
	if err != nil {
		t.Fatal(err)
	}
	if rebuiltCorrection == nil || rebuiltCorrection.Disposition != "failed" || rebuiltCorrection.Diagnosis != detail {
		t.Fatalf("rebuilt correction = %+v, want the same failed disposition and diagnosis", rebuiltCorrection)
	}
}

func TestDerivedWorkerFailureMatchesManualCorrectionProjection(t *testing.T) {
	for _, family := range []struct {
		ref            string
		manualVersion  int64
		derivedVersion int64
		step           string
	}{
		{ref: "workflow.implementation", manualVersion: 28, derivedVersion: 30, step: "execution"},
		{ref: "workflow.break_fix", manualVersion: 25, derivedVersion: 27, step: "repair"},
	} {
		t.Run(family.ref, func(t *testing.T) {
			workID := "failure-parity-" + strings.TrimPrefix(family.ref, "workflow.")
			manual := seedHistoricalWorkflowReturnRouteFixture(t, workID, family.ref, family.manualVersion, family.step)
			derived := seedHistoricalWorkflowReturnRouteFixture(t, workID, family.ref, family.derivedVersion, family.step)
			fixtures := []workflowReturnRouteFixture{manual, derived}
			jobs := []*WorkerJobBinding{{JobID: "job:" + workID}, {JobID: "job:" + workID}}
			corrections := make([]*WorkflowCorrectionContext, len(fixtures))
			lane := reviewGateLane(t, "implementation")

			for round := int64(1); round <= 3; round++ {
				attempt := fmt.Sprintf("attempt:%s:%d", workID, round)
				terminalID := fmt.Sprintf("failed-%s-%d", workID, round)
				detail := fmt.Sprintf("failure detail for fenced attempt %d", round)
				for index, fixture := range fixtures {
					s := fixture.store
					dispatchJobBoundAttempt(t, s, workID, family.step, attempt, corrections[index], fixture.owner, 0, fmt.Sprintf("parity-dispatch-%d", round), jobs[index])
					epoch := latestStepStartEpoch(t, s, workID, family.step)
					if err := s2WorkerFailedLive(t, s, s2WorkerFailedEvent(workID, attempt, terminalID, detail, preferredModelForLane(lane))); err != nil {
						t.Fatalf("path %d terminal failure %d: %v", index, round, err)
					}
					if index == 0 {
						if records := s2DerivedFailureRecords(t, s, workID, attempt); len(records) != 0 {
							t.Fatalf("released pin derived failure records: %+v", records)
						}
						if err := runVerdictActionAs(t, s, workID, "record_worker_failure", mustJSONValue(map[string]any{"attempt_id": attempt, "attempt_epoch": epoch}), 0, fixture.owner); err != nil {
							t.Fatalf("manual failure record %d: %v", round, err)
						}
					}
					correction, err := workflowCorrectionContext(context.Background(), s.db, workID, family.step)
					if err != nil || correction == nil {
						t.Fatalf("path %d correction %d = (%+v, %v)", index, round, correction, err)
					}
					// Source and evidence locators are route-local. Qualify them
					// against the actual completion before comparing the context.
					var sourceID, actor string
					var sourceSeq int64
					var raw []byte
					if err := s.db.QueryRow(`SELECT event_id,seq,json_extract(payload,'$.actor_ref'),payload FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='record_worker_failure' AND json_extract(payload,'$.worker_attempt_id')=?`, workID, WorkflowActionCompleted, attempt).Scan(&sourceID, &sourceSeq, &actor, &raw); err != nil {
						t.Fatal(err)
					}
					var fields workflowCorrectionCompletionFields
					if err := json.Unmarshal(raw, &fields); err != nil {
						t.Fatal(err)
					}
					if fields.CorrectionDiagnosis != "" || fields.CorrectionStrategy != "" || len(fields.CorrectionEvidence) != 0 || len(fields.ResultEvidence) != 1 || actor != mustActorRef(t, fixture.owner) {
						t.Fatalf("path %d completion does not carry the default-only failure record: %+v, actor %q", index, fields, actor)
					}
					if isDerived := strings.HasSuffix(sourceID, workflowDerivedWorkerFailureEventIDSuffix); isDerived != (index == 1) {
						t.Fatalf("path %d source = %q, derived = %t", index, sourceID, isDerived)
					}
					if index == 1 && sourceID != terminalID+workflowDerivedWorkerFailureEventIDSuffix {
						t.Fatalf("derived source = %q, want the original terminal pair", sourceID)
					}
					want := &WorkflowCorrectionContext{
						Disposition: "failed", AttemptCount: round, AttemptLimit: 3, Escalated: round == 3,
						PredicateIDs: []string{}, EvidenceRefs: fields.ResultEvidence,
						Diagnosis: detail, Strategy: "retry with a fresh fenced attempt",
						FailureKind: string(WorkerFailureWorkerError), FailureDetail: detail,
						FailedAttemptID: attempt, FailedAttemptEpoch: round,
						SourceEventID: sourceID, SourceEventSeq: sourceSeq,
					}
					if !reflect.DeepEqual(correction, want) {
						t.Fatalf("path %d correction %d = %+v, want %+v", index, round, correction, want)
					}
					corrections[index] = correction
					if err := RebuildFromLog(context.Background(), s); err != nil {
						t.Fatalf("path %d rebuild %d: %v", index, round, err)
					}
					if rebuilt, err := workflowCorrectionContext(context.Background(), s.db, workID, family.step); err != nil || !reflect.DeepEqual(rebuilt, correction) {
						t.Fatalf("path %d rebuilt correction %d = (%+v, %v), want %+v", index, round, rebuilt, err, correction)
					}
				}
				manualContext, derivedContext := *corrections[0], *corrections[1]
				// All remaining fields must match. The distinct locators above
				// must keep naming each route's own immutable completion.
				derivedContext.SourceEventID = manualContext.SourceEventID
				derivedContext.SourceEventSeq = manualContext.SourceEventSeq
				derivedContext.EvidenceRefs = manualContext.EvidenceRefs
				if !reflect.DeepEqual(manualContext, derivedContext) {
					t.Fatalf("round %d derived correction = %+v, manual = %+v", round, derivedContext, manualContext)
				}
			}
		})
	}
}

// TestTerminalFailureWithoutADerivedRecordSkipsStructurally proves the
// derivation is structural, never prose: when a newer fenced window has
// superseded the failing attempt's epoch at the step, the terminal event
// lands unchanged with no derived record, and the live attempt's own
// terminal event still derives exactly its own record.
func TestTerminalFailureWithoutADerivedRecordSkipsStructurally(t *testing.T) {
	t.Parallel()
	const workID = "s2-derived-failure-structural-skip"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	dispatcher := diagnosisS1Actor(workID, "dispatcher")
	lane := reviewGateLane(t, "implementation")
	first, second := "attempt:"+workID+":1", "attempt:"+workID+":2"
	dispatchJobBoundAttempt(t, s, workID, "repair", first, nil, dispatcher, 0, "s2-skip-dispatch-1-"+workID, &WorkerJobBinding{JobID: "job:" + workID + ":1"})
	dispatchJobBoundAttempt(t, s, workID, "repair", second, nil, dispatcher, 0, "s2-skip-dispatch-2-"+workID, &WorkerJobBinding{JobID: "job:" + workID + ":2"})
	starts := workflowStartedEventsAtStep(t, s, workID, "repair")
	if len(starts) != 2 || starts[1].Epoch != 2 {
		t.Fatalf("repair starts = %+v, want two dispatch starts with the second at epoch 2", starts)
	}
	// Attempt 1 fails after attempt 2's window superseded its epoch. The
	// terminal carries the recorded observation the promoted pin requires;
	// the structural skip it proves is the derivation's, not the
	// attribution's.
	if err := s2WorkerFailedLive(t, s, s2WorkerFailedAttributedEvent(workID, first, "failed-"+workID+"-1", preferredModelForLane(lane), WorkerFailureWorkerError, "the superseded attempt failed", time.Unix(30, 0).UTC())); err != nil {
		t.Fatalf("the superseded attempt's terminal ingest: %v", err)
	}
	if got := s2DerivedFailureRecords(t, s, workID, first); len(got) != 0 {
		t.Fatalf("the superseded attempt derived %d records, want none outside its epoch", len(got))
	}
	// Attempt 2's own terminal event derives exactly its own record.
	if err := s2WorkerFailedLive(t, s, s2WorkerFailedAttributedEvent(workID, second, "failed-"+workID+"-2", preferredModelForLane(lane), WorkerFailureWorkerError, "the live attempt failed", time.Unix(30, 0).UTC())); err != nil {
		t.Fatalf("the live attempt's terminal ingest: %v", err)
	}
	records := s2DerivedFailureRecords(t, s, workID, second)
	if len(records) != 1 || records[0]["epoch"] != int64(2) {
		t.Fatalf("derived records for attempt 2 = %+v, want one at epoch 2", records)
	}
	if got := s2DerivedFailureRecords(t, s, workID, first); len(got) != 0 {
		t.Fatalf("the superseded attempt derived records late: %+v", got)
	}
}

// s2CombinedRejectPayload builds the rejection payload with the optional
// retry dispatch intent.
func s2CombinedRejectPayload(attemptID string, epoch int64, retry json.RawMessage) json.RawMessage {
	payload := map[string]any{
		"attempt_id": attemptID, "attempt_epoch": epoch,
		"diagnosis": "the review rejects the result", "strategy": "reproduce the result with the review findings addressed",
		"predicate_ids": []string{"predicate:return-route"}, "evidence_refs": []string{"evidence:return-route-verification"},
	}
	if retry != nil {
		payload["retry"] = retry
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return encoded
}

// s2CombinedReject runs the combined reject_worker_result call as one actor
// with the caller's claimed session worktree, so the retry dispatch binds the
// same claimed worktree a dispatch binds.
func s2CombinedReject(t *testing.T, s *Store, workID string, owner WorkflowActor, payload json.RawMessage, operationID string) (WorkflowActionExecutionResult, error) {
	t.Helper()
	return invokeWorkflowActionForCD0059(context.Background(), t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: readWorkVersion(t, s, workID), ActionID: "reject_worker_result",
		Payload: payload, SessionWorktree: dispatchSessionWorktree(t, s, workID),
		Actor: owner, AcceptedInputsDigest: "sha256:" + strings.Repeat("c", 64), IdempotencyIdentity: operationID, OperationID: operationID,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID, RequestID: "request:" + operationID,
		ContractDigest: testManifestDigest, Now: time.Unix(40, 0).UTC(),
	})
}

// s2CompletedJobBoundAttempt completes a dispatched job-bound attempt's
// report without dispositioning it, leaving the result open to rejection.
func s2CompletedJobBoundAttempt(t *testing.T, s *Store, workID, attemptID string) {
	t.Helper()
	dispatched, err := workflowDispatchedJobForAttempt(context.Background(), s.DatabaseForTesting(), workID, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	lane := reviewGateLane(t, "implementation")
	if err := completeJobBoundAttemptForTest(s, workID, attemptID, lane, dispatched); err != nil {
		t.Fatalf("complete the dispatched attempt %s: %v", attemptID, err)
	}
}

func s2DispatchJobBinding(t *testing.T, s *Store, workID, attemptID string) *WorkerJobBinding {
	t.Helper()
	found, job, err := dispatchCompletionJobForAttempt(context.Background(), s.DatabaseForTesting(), workID, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if !found || job == nil {
		t.Fatalf("the dispatch authorization for %s carries no worker-job binding", attemptID)
	}
	return job
}

// TestCombinedRejectRetryDispatchesThroughTheDispatchOwner proves the CD-0198
// D5 combined route: one reject_worker_result call carrying retry:{lane_id}
// records the rejection and, through the existing dispatch admission and
// packet owner in the same transaction, mints a fresh fenced attempt whose
// authorization binds the same job revision, the recorded packet digest, and
// the original claimed worktree — so the host's post-commit dispatch boundary
// can launch it, and the rejection's job-bound debt stays open for it.
func TestCombinedRejectRetryDispatchesThroughTheDispatchOwner(t *testing.T) {
	t.Parallel()
	const workID = "s2-combined-reject-retry"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.implementation", "execution")
	s := fixture.store
	dispatcher := diagnosisS1Actor(workID, "dispatcher")
	attempt := "attempt:" + workID + ":1"
	dispatchJobBoundAttempt(t, s, workID, "execution", attempt, nil, dispatcher, 0, "s2-combined-dispatch-"+workID, &WorkerJobBinding{JobID: "job:" + workID})
	epoch := latestStepStartEpoch(t, s, workID, "execution")
	s2CompletedJobBoundAttempt(t, s, workID, attempt)
	originalJob := s2DispatchJobBinding(t, s, workID, attempt)

	before := readWorkVersion(t, s, workID)
	result, err := s2CombinedReject(t, s, workID, fixture.owner, s2CombinedRejectPayload(attempt, epoch, json.RawMessage(`{"lane_id":"implement"}`)), "combined-reject-"+workID)
	if err != nil {
		t.Fatalf("the combined rejection and retry: %v", err)
	}
	retryAttempt := "combined-reject-" + workID + ":retry"
	// The combined response serializes the exact admitted retry dispatch:
	// the minted attempt identity, the derived packet bytes, the digest the
	// fresh window opened for, and the claimed worktree the authorization
	// rested on. The adapter's host launch routes through these values alone.
	var resultMap map[string]any
	if err := json.Unmarshal(result.Result, &resultMap); err != nil {
		t.Fatalf("the combined result is not one JSON object: %v", err)
	}
	if resultMap["retry_attempt_id"] != retryAttempt {
		t.Fatalf("combined retry_attempt_id = %v, want %q", resultMap["retry_attempt_id"], retryAttempt)
	}
	packetJSON, ok := resultMap["retry_worker_packet"]
	if !ok {
		t.Fatal("the combined result carries no retry_worker_packet")
	}
	packetBytes, err := json.Marshal(packetJSON)
	if err != nil {
		t.Fatal(err)
	}
	if err := payloadschema.Validate(pinnedDispatchPacketSchemaRef(t, s, workID), packetBytes); err != nil {
		t.Fatalf("the serialized retry packet does not satisfy the published worker packet schema: %v", err)
	}
	var serializedPacket map[string]any
	if err := json.Unmarshal(packetBytes, &serializedPacket); err != nil {
		t.Fatal(err)
	}
	if serializedPacket["attempt_id"] != retryAttempt || serializedPacket["lane_id"] != "implement" {
		t.Fatalf("serialized retry packet identity = %v/%v, want %s on the implement lane", serializedPacket["attempt_id"], serializedPacket["lane_id"], retryAttempt)
	}
	resultDigest, _ := resultMap["worker_packet_digest"].(string)
	if resultDigest == "" {
		t.Fatal("the combined result carries no retry packet digest")
	}
	resultWorktree, _ := resultMap["worker_worktree"].(string)
	if resultWorktree == "" {
		t.Fatal("the combined result carries no claimed worktree the retry authorization rested on")
	}
	// The combined result rides the agent envelope with the packet inline.
	// The byte budget is owned at the public mutation boundary, which
	// validates the fully enriched result inside the transaction and rolls
	// the whole call back on overflow: TestCombinedRejectOversizedResultRefusesZeroWrite
	// and TestCombinedRejectNormalSizeCommits (internal/agent) carry that
	// evidence, so no second, hand-picked byte constant stands in for the
	// owner here.
	// The rejection recorded.
	var rejects int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='reject_worker_result' AND json_extract(payload,'$.worker_attempt_id')=?`, workID, WorkflowActionCompleted, attempt).Scan(&rejects); err != nil {
		t.Fatal(err)
	}
	if rejects != 1 {
		t.Fatalf("rejection completions = %d, want one", rejects)
	}
	// The retry authorization mints the fresh fenced attempt in the same
	// transaction: one start at the next epoch and one completion carrying
	// the lane identity, the recorded packet digest, and the same job
	// revision.
	starts := workflowStartedEventsAtStep(t, s, workID, "execution")
	if len(starts) != 2 || starts[1].ActionID != "dispatch_worker" || starts[1].Epoch != 2 {
		t.Fatalf("execution starts after the combined call = %+v, want the retry dispatch start at epoch 2", starts)
	}
	var packetDigest, jobID, laneID string
	var jobRevision int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(json_extract(payload,'$.worker_packet_digest'),''),json_extract(payload,'$.worker_job.job_id'),json_extract(payload,'$.worker_job.revision'),json_extract(payload,'$.worker_lane_id') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=?`, workID, WorkflowActionCompleted, retryAttempt).Scan(&packetDigest, &jobID, &jobRevision, &laneID); err != nil {
		t.Fatalf("the retry dispatch completion: %v", err)
	}
	if packetDigest == "" || laneID != "implement" {
		t.Fatalf("retry completion digest=%q lane=%q, want a recorded packet digest on the implement lane", packetDigest, laneID)
	}
	if resultDigest != packetDigest {
		t.Fatalf("combined worker_packet_digest = %q, want the retry dispatch's recorded digest %q", resultDigest, packetDigest)
	}
	if jobID != originalJob.JobID || jobRevision != originalJob.Revision {
		t.Fatalf("retry job binding = %s@%d, want the original revision %s@%d", jobID, jobRevision, originalJob.JobID, originalJob.Revision)
	}
	var lifecycle string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, retryAttempt).Scan(&lifecycle); err != nil {
		t.Fatalf("the fresh retry attempt binding: %v", err)
	}
	if lifecycle != "in_flight" {
		t.Fatalf("the fresh retry attempt lifecycle = %q, want the durable in-flight binding", lifecycle)
	}
	if result.ResultingVersion <= before {
		t.Fatalf("the combined call moved the version %d -> %d, want the rejection and the dispatch both recorded", before, result.ResultingVersion)
	}
	// The host's post-commit original dispatch boundary admits the fresh
	// attempt's launch against the recorded digest.
	if err := s.Transact(context.Background(), func(transaction *Transaction) error {
		return ValidateWorkerDispatchWindow(context.Background(), transaction, workID, "execution", retryAttempt, packetDigest)
	}); err != nil {
		t.Fatalf("the post-commit dispatch window for the retry attempt: %v", err)
	}
	// The rejection's job-bound debt stays open for the fresh attempt: the
	// correction reads the same rejected record after the combined call.
	correction, err := workflowCorrectionContextForDispatch(context.Background(), s.DatabaseForTesting(), workID, "execution", retryAttempt)
	if err != nil {
		t.Fatal(err)
	}
	if correction == nil || correction.Disposition != "rejected" || correction.SourceEventID == "" {
		t.Fatalf("correction after the combined call = %+v, want the open rejected record the retry consumes", correction)
	}
	// Rebuild reaches the same durable state.
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("rebuild from the log: %v", err)
	}
	var rebuiltDigest string
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(json_extract(payload,'$.worker_packet_digest'),'') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=?`, workID, WorkflowActionCompleted, retryAttempt).Scan(&rebuiltDigest); err != nil {
		t.Fatal(err)
	}
	if rebuiltDigest != packetDigest {
		t.Fatalf("rebuilt retry digest = %q, want %q", rebuiltDigest, packetDigest)
	}
}

// TestCombinedRejectRetryAtTheWallRefusesAndRejectionOnlyRecords proves the
// CD-0173 wall keeps its operator-owned escape: when the rejection is the
// attempt that reaches the limit, the combined call refuses with no
// rejection or dispatch partial writes, and the rejection without the retry
// payload still records.
func TestCombinedRejectRetryAtTheWallRefusesAndRejectionOnlyRecords(t *testing.T) {
	t.Parallel()
	const workID = "s2-combined-reject-wall"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.implementation", "execution")
	s := fixture.store
	dispatcher := diagnosisS1Actor(workID, "dispatcher")
	// Two rejected attempts arm the count at two; the third rejection is the
	// attempt that reaches the limit.
	for round := 1; round <= 2; round++ {
		attempt := fmt.Sprintf("attempt:%s:%d", workID, round)
		var correction *WorkflowCorrectionContext
		if round > 1 {
			correction = issue1013Pin(t, s, workID).Correction
		}
		dispatchJobBoundAttempt(t, s, workID, "execution", attempt, correction, dispatcher, 0, fmt.Sprintf("s2-wall-dispatch-%d-%s", round, workID), &WorkerJobBinding{JobID: fmt.Sprintf("job:%s:%d", workID, round)})
		s2CompletedJobBoundAttempt(t, s, workID, attempt)
		if _, err := s2CombinedReject(t, s, workID, fixture.owner, s2CombinedRejectPayload(attempt, latestStepStartEpoch(t, s, workID, "execution"), nil), fmt.Sprintf("s2-wall-reject-%d-%s", round, workID)); err != nil {
			t.Fatalf("rejection round %d: %v", round, err)
		}
	}
	third := "attempt:" + workID + ":3"
	dispatchJobBoundAttempt(t, s, workID, "execution", third, issue1013Pin(t, s, workID).Correction, dispatcher, 0, "s2-wall-dispatch-3-"+workID, &WorkerJobBinding{JobID: fmt.Sprintf("job:%s:3", workID)})
	s2CompletedJobBoundAttempt(t, s, workID, third)
	before := readWorkVersion(t, s, workID)
	var eventsBefore int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=?`, workID).Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := s2CombinedReject(t, s, workID, fixture.owner, s2CombinedRejectPayload(third, latestStepStartEpoch(t, s, workID, "execution"), json.RawMessage(`{"lane_id":"implement"}`)), "s2-combined-wall-"+workID); err == nil {
		t.Fatal("the combined retry was admitted at the escalation wall")
	} else {
		var failure *Failure
		if !failureAs(err, &failure) || failure.Kind != KindMissingEvidence || !strings.Contains(failure.Detail, "three-attempt limit") {
			t.Fatalf("the combined retry at the wall refused with %v, want the wall's own convergence refusal", err)
		}
	}
	if got := readWorkVersion(t, s, workID); got != before {
		t.Fatalf("the refused combined call moved the version %d -> %d", before, got)
	}
	var eventsAfter int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=?`, workID).Scan(&eventsAfter); err != nil {
		t.Fatal(err)
	}
	if eventsAfter != eventsBefore {
		t.Fatalf("the refused combined call left %d events (before %d); the wall keeps no partial writes", eventsAfter, eventsBefore)
	}
	// The rejection without the retry payload still records.
	if _, err := s2CombinedReject(t, s, workID, fixture.owner, s2CombinedRejectPayload(third, latestStepStartEpoch(t, s, workID, "execution"), nil), "s2-wall-reject-only-"+workID); err != nil {
		t.Fatalf("the rejection-only escape at the wall: %v", err)
	}
	correction, err := workflowCorrectionContext(context.Background(), s.db, workID, "execution")
	if err != nil {
		t.Fatal(err)
	}
	if correction == nil || !correction.Escalated {
		t.Fatalf("correction after the rejection-only escape = %+v, want the escalated rejected record", correction)
	}
}

// TestCombinedRejectRetryRefusalsLeaveZeroWrites proves the combined route's
// closed inputs: an unknown lane, a retry member with unknown fields or a
// missing lane_id, and a retry offered on a released pin all refuse before
// any write, and the plain rejection stays available behind every refusal.
func TestCombinedRejectRetryRefusalsLeaveZeroWrites(t *testing.T) {
	t.Parallel()
	const workID = "s2-combined-reject-refusals"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.implementation", "execution")
	s := fixture.store
	dispatcher := diagnosisS1Actor(workID, "dispatcher")
	attempt := "attempt:" + workID + ":1"
	dispatchJobBoundAttempt(t, s, workID, "execution", attempt, nil, dispatcher, 0, "s2-refusal-dispatch-"+workID, &WorkerJobBinding{JobID: "job:" + workID})
	epoch := latestStepStartEpoch(t, s, workID, "execution")
	s2CompletedJobBoundAttempt(t, s, workID, attempt)
	cases := []struct {
		name  string
		retry json.RawMessage
	}{
		{name: "unknown lane", retry: json.RawMessage(`{"lane_id":"nonexistent-lane"}`)},
		{name: "unknown member", retry: json.RawMessage(`{"lane_id":"implement","packet":{}}`)},
		{name: "missing lane", retry: json.RawMessage(`{}`)},
	}
	for _, tc := range cases {
		before := readWorkVersion(t, s, workID)
		if _, err := s2CombinedReject(t, s, workID, fixture.owner, s2CombinedRejectPayload(attempt, epoch, tc.retry), "s2-refusal-"+workID+"-"+strings.ReplaceAll(tc.name, " ", "-")); err == nil {
			t.Fatalf("the combined call with %s was admitted", tc.name)
		}
		if got := readWorkVersion(t, s, workID); got != before {
			t.Fatalf("the %s refusal moved the version %d -> %d", tc.name, before, got)
		}
	}
	// The plain rejection stays available behind every refusal and
	// dispositions the result.
	if _, err := s2CombinedReject(t, s, workID, fixture.owner, s2CombinedRejectPayload(attempt, epoch, nil), "s2-reject-after-refusals-"+workID); err != nil {
		t.Fatalf("the plain rejection behind the refusals: %v", err)
	}
	// A released pin refuses the retry intent and keeps its callable failure
	// record: the derivation and the combined route are new-pin behavior.
	const releasedID = "s2-released-pin-boundary"
	registered, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 28)
	if !ok {
		t.Fatal("workflow.implementation v28 is not registered")
	}
	released := seedWorkflowReturnRouteFixtureWithDefinition(t, releasedID, registered, "execution", []string{"verification"}, []string{"verification", "review", "artifact"})
	releasedDispatcher := diagnosisS1Actor(releasedID, "dispatcher")
	releasedAttempt := "attempt:" + releasedID + ":1"
	dispatchJobBoundAttempt(t, released.store, releasedID, "execution", releasedAttempt, nil, releasedDispatcher, 0, "s2-released-dispatch-"+releasedID, &WorkerJobBinding{JobID: "job:" + releasedID})
	releasedEpoch := latestStepStartEpoch(t, released.store, releasedID, "execution")
	// The retry intent refuses on the released pin before any write.
	if _, err := s2CombinedReject(t, released.store, releasedID, released.owner, s2CombinedRejectPayload(releasedAttempt, releasedEpoch, json.RawMessage(`{"lane_id":"implement"}`)), "s2-combined-released-"+releasedID); err == nil {
		t.Fatal("the retry intent was admitted on the released pin")
	}
	// The released pin keeps its callable failure record: the attempt fails
	// through the terminal seam, the manual record dispositions it, and no
	// derivation runs.
	lane := reviewGateLane(t, "implementation")
	if err := s2WorkerFailedLive(t, released.store, s2WorkerFailedEvent(releasedID, releasedAttempt, "failed-"+releasedID, "the released attempt failed", preferredModelForLane(lane))); err != nil {
		t.Fatalf("the released attempt's terminal ingest: %v", err)
	}
	if err := runVerdictActionAs(t, released.store, releasedID, "record_worker_failure", json.RawMessage(`{"attempt_id":"`+releasedAttempt+`","attempt_epoch":`+fmt.Sprint(releasedEpoch)+`}`), 0, released.owner); err != nil {
		t.Fatalf("the callable failure record on the released pin: %v", err)
	}
	got := s2DerivedFailureRecords(t, released.store, releasedID, releasedAttempt)
	if len(got) != 1 || strings.HasSuffix(got[0]["event_id"].(string), ":workflow-failure") {
		t.Fatalf("the released pin's failure records = %+v, want the manual record alone with no derivation pair", got)
	}
}

// TestRetryLaunchFailureAfterCommittedIntentStaysTruthful proves the launch
// boundary: after the combined call commits its durable intent, a real launch
// failure records the abandonment through the terminal seam, the abandoned
// attempt derives its own failure record from that original terminal event
// exactly once, and no second launch exists to reconcile.
func TestRetryLaunchFailureAfterCommittedIntentStaysTruthful(t *testing.T) {
	t.Parallel()
	const workID = "s2-retry-launch-failure"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.implementation", "execution")
	s := fixture.store
	dispatcher := diagnosisS1Actor(workID, "dispatcher")
	attempt := "attempt:" + workID + ":1"
	dispatchJobBoundAttempt(t, s, workID, "execution", attempt, nil, dispatcher, 0, "s2-launch-dispatch-"+workID, &WorkerJobBinding{JobID: "job:" + workID})
	epoch := latestStepStartEpoch(t, s, workID, "execution")
	s2CompletedJobBoundAttempt(t, s, workID, attempt)
	if _, err := s2CombinedReject(t, s, workID, fixture.owner, s2CombinedRejectPayload(attempt, epoch, json.RawMessage(`{"lane_id":"implement"}`)), "s2-combined-launch-"+workID); err != nil {
		t.Fatalf("the combined rejection and retry: %v", err)
	}
	retryAttempt := "s2-combined-launch-" + workID + ":retry"
	// The launch fails: the host abandons the never-started attempt through
	// the worker-abandon verb's terminal seam. The host observed the
	// abandonment it performed; the observation states that fact with an
	// unknown cause, and the store derives the identity from the retry's own
	// dispatch authorization.
	abandon := s2WorkerFailedAttributedEvent(workID, retryAttempt, "abandoned-"+retryAttempt, "", WorkerFailureAbandoned, "the host launch failed after the committed intent", time.Unix(50, 0).UTC())
	if err := s2WorkerFailedLive(t, s, abandon); err != nil {
		t.Fatalf("the abandonment of the retry attempt: %v", err)
	}
	records := s2DerivedFailureRecords(t, s, workID, retryAttempt)
	if len(records) != 1 {
		t.Fatalf("the abandoned retry attempt derived %d records, want one from its own terminal event", len(records))
	}
	correction, err := workflowCorrectionContext(context.Background(), s.db, workID, "execution")
	if err != nil {
		t.Fatal(err)
	}
	if correction == nil || correction.Disposition != "failed" || correction.FailedAttemptID != retryAttempt {
		t.Fatalf("correction after the launch failure = %+v, want the truthful failed disposition of the original retry attempt", correction)
	}
	if err := s2WorkerFailedLive(t, s, abandon); err == nil {
		t.Fatal("the abandonment replayed twice")
	}
	if got := s2DerivedFailureRecords(t, s, workID, retryAttempt); len(got) != 1 {
		t.Fatalf("the abandoned retry attempt derived %d records after the replay, want one", len(got))
	}
}

// TestCombinedRejectDerivedRetryIdentityStaysBounded proves the derived
// retry identities answer to the published bounds before the transaction
// writes: an idempotency key or request id with no room for the :retry
// suffix refuses typed with the rejection unwritten and the attempt window
// unopened, and the longest admitted key derives exactly the bounded
// identities and commits one rejection and one retry dispatch.
func TestCombinedRejectDerivedRetryIdentityStaysBounded(t *testing.T) {
	const workID = "s2-bound"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	dispatcher := diagnosisS1Actor(workID, "dispatcher")
	lane := reviewGateLane(t, "implementation")
	attempt := "attempt:" + workID + ":1"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt, nil, dispatcher, 0, "s2-bound-dispatch-"+workID, &WorkerJobBinding{JobID: "job:" + workID})
	s2CompletedJobBoundAttempt(t, s, workID, attempt)

	boundReject := func(t *testing.T, idempotencyKey, requestID string) error {
		t.Helper()
		_, err := invokeWorkflowActionForCD0059(context.Background(), t, s, WorkflowActionExecutionRequest{
			WorkID: workID, ExpectedVersion: readWorkVersion(t, s, workID), ActionID: "reject_worker_result",
			Payload:         s2CombinedRejectPayload(attempt, latestStepStartEpoch(t, s, workID, "repair"), json.RawMessage(`{"lane_id":"implement"}`)),
			SessionWorktree: dispatchSessionWorktree(t, s, workID),
			Actor:           fixture.owner, AcceptedInputsDigest: "sha256:" + strings.Repeat("c", 64), IdempotencyIdentity: "s2-bound-key", OperationID: "s2-bound-key",
			PrincipalRef: fixture.owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: idempotencyKey, RequestID: requestID,
			ContractDigest: testManifestDigest, Now: time.Unix(40, 0).UTC(),
		})
		return err
	}
	overKey, overRequest := strings.Repeat("k", 123), strings.Repeat("r", 123)
	workflowActionEvents := func(t *testing.T) int {
		t.Helper()
		var events int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND json_extract(payload,'$.action_id') IN ('reject_worker_result','dispatch_worker')`, workID).Scan(&events); err != nil {
			t.Fatal(err)
		}
		return events
	}
	for name, keys := range map[string][2]string{"idempotency key": {overKey, "request:s2-bound"}, "request id": {"s2-bound-key", overRequest}} {
		before := readWorkVersion(t, s, workID)
		eventsBefore := workflowActionEvents(t)
		err := boundReject(t, keys[0], keys[1])
		if err == nil {
			t.Fatalf("the combined rejection with an over-bound %s committed", name)
		}
		var failure *Failure
		if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload {
			t.Fatalf("the over-bound %s refusal is not the typed payload failure: %v", name, err)
		}
		if !strings.Contains(failure.Detail, "derived retry identity") {
			t.Fatalf("the over-bound %s refusal does not name the derived identity: %s", name, failure.Detail)
		}
		if readWorkVersion(t, s, workID) != before {
			t.Fatalf("the over-bound %s refusal moved the work version", name)
		}
		if got := workflowActionEvents(t) - eventsBefore; got != 0 {
			t.Fatalf("the over-bound %s refusal left %d rejection or dispatch events", name, got)
		}
	}

	// The longest admitted key derives exactly the bounded identities: the
	// key and the request id land at 128 bytes with the suffix inside the
	// bound, and the combined call commits one rejection and one retry.
	exact := strings.Repeat("k", 122)
	if err := boundReject(t, exact, exact); err != nil {
		t.Fatalf("the combined rejection at the identity bound: %v", err)
	}
	if len(exact+workflowDerivedRetryAttemptIDSuffix) != maxRetryDerivedKeyLength {
		t.Fatalf("the bounded key derivation is %d bytes, want exactly the %d-byte bound", len(exact+workflowDerivedRetryAttemptIDSuffix), maxRetryDerivedKeyLength)
	}
	var retryAttempts int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, "s2-bound-key:retry").Scan(&retryAttempts); err != nil {
		t.Fatal(err)
	}
	if retryAttempts != 1 {
		t.Fatalf("bounded retry attempts = %d, want one", retryAttempts)
	}
	_ = lane
}

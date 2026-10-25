package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The cross-attempt fixture pairs a completed research result at an earlier
// fenced epoch with a later, independent completed corrective review, then
// rejects the named earlier result without disturbing the later review. A
// rejection binds the exact attempt/epoch pair it names at the prefix it
// names: the guard and the fold read the same interval-bounded eligibility
// reader.

func crossAttemptDispatchEpoch(t *testing.T, s *Store, workID, attemptID string) int64 {
	t.Helper()
	var epoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM worker_authorizations WHERE work_id=? AND attempt_id=?`, workID, attemptID).Scan(&epoch); err != nil {
		t.Fatalf("read the dispatch epoch for %s: %v", attemptID, err)
	}
	return epoch
}

func crossAttemptDispatchedLaneActor(t *testing.T, s *Store, workID, attemptID string) string {
	t.Helper()
	var laneActor string
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(json_extract(dispatch_payload,'$.lane_actor_ref'),'') FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, attemptID).Scan(&laneActor); err != nil {
		t.Fatalf("read the dispatched lane actor for %s: %v", attemptID, err)
	}
	return laneActor
}

func crossAttemptEventPayload(t *testing.T, s *Store, workID, kind, attemptID string) []byte {
	t.Helper()
	query := ""
	args := []any{workID, attemptID}
	switch kind {
	case WorkflowActionCompleted:
		query = `SELECT payload FROM worker_authorizations WHERE work_id=? AND attempt_id=?`
	case WorkerDispatched:
		query = `SELECT dispatch_payload FROM worker_attempts WHERE work_id=? AND attempt_id=?`
	case WorkerCompleted, WorkerFailed:
		query = `SELECT terminal_payload FROM worker_attempts WHERE work_id=? AND attempt_id=? AND terminal_kind=?`
		args = append(args, kind)
	default:
		t.Fatalf("no current worker payload owner for %s", kind)
	}
	var payload []byte
	if err := s.DatabaseForTesting().QueryRow(query, args...).Scan(&payload); err != nil {
		t.Fatalf("read the %s payload for %s: %v", kind, attemptID, err)
	}
	return payload
}

// crossAttemptFixture is one current research instance parked at
// the worker-dispatch step, with the shared lane, dispatcher, and attempt
// identities the disposition tests reuse. The owner dispatches both lanes and
// rejects: another session's dispatch over an undecided result is a
// takeover. The fixture is synthetic and
// openTemp-backed; no live store state participates.
type crossAttemptFixture struct {
	fixture      workflowReturnRouteFixture
	s            *Store
	workID       string
	step         string
	definition   WorkflowDefinition
	researchLane LaneDefinition
	reviewLane   LaneDefinition
	dispatcher   WorkflowActor
	attemptR     string
	attemptV     string
}

func seedCrossAttemptFixture(t *testing.T, workID string) crossAttemptFixture {
	t.Helper()
	return seedCrossAttemptFixtureAtStep(t, workID, "investigate")
}

// seedCrossAttemptFixtureAtStep parks the same fixture at the named step, so
// the checkpoint-step evidence-acceptance shape shares the builders.
func seedCrossAttemptFixtureAtStep(t *testing.T, workID, step string) crossAttemptFixture {
	t.Helper()
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.research")
	if err != nil {
		t.Fatal(err)
	}
	f := crossAttemptFixture{
		fixture:      seedWorkflowReturnRouteFixtureWithDefinition(t, workID, registered, step, []string{}, []string{}),
		workID:       workID,
		step:         step,
		definition:   registered.Definition,
		researchLane: reviewGateLane(t, "research"),
		reviewLane:   reviewGateLane(t, "review"),
		attemptR:     "attempt:" + workID + ":1",
		attemptV:     "attempt:" + workID + ":2",
	}
	f.s = f.fixture.store
	f.dispatcher = f.fixture.owner
	return f
}

// authorizeDispatch runs the dispatch_worker action route alone: the fenced
// authorization opens and completes without lane evidence, the CD-0193
// unused-window shape a later worker dispatch would have to materialize.
func (f crossAttemptFixture) authorizeDispatch(t *testing.T, attemptID string, lane LaneDefinition, dispatcher WorkflowActor) {
	t.Helper()
	packet := joinPacketFor(t, f.s, f.workID, f.step, attemptID, lane.ID, lane.Digest)
	correction, err := workflowCorrectionContextForDispatch(t.Context(), f.s.db, f.workID, f.step, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if correction != nil {
		packet["inputs"].(map[string]any)["correction"] = correction
	}
	if _, err := dispatchJoinAttempt(context.Background(), t, f.s, f.workID, readWorkVersion(t, f.s, f.workID), dispatcher, packet); err != nil {
		t.Fatalf("authorize the dispatch of %s: %v", attemptID, err)
	}
}

// materializeDispatch records the authoritative lane-actor evidence for one
// authorized attempt, so the recorded dispatch carries the executor its own
// authorization minted, never a later attempt's.
func (f crossAttemptFixture) materializeDispatch(t *testing.T, attemptID string, lane LaneDefinition, dispatcher WorkflowActor) {
	t.Helper()
	var packetDigest string
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.worker_packet_digest') FROM worker_authorizations WHERE work_id=? AND attempt_id=?`, f.workID, attemptID).Scan(&packetDigest); err != nil {
		t.Fatal(err)
	}
	dispatched := Event{EventID: "cross-attempt-dispatched-" + attemptID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: f.workID, Actor: "worker:test", OccurredAt: time.Unix(20, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion(WorkerDispatched), Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion, PacketDigest: packetDigest})}
	if err := f.s.Transact(context.Background(), func(transaction *Transaction) error {
		prepared, err := PrepareLaneActorDispatch(context.Background(), transaction, dispatched, dispatcher.PrincipalRef, dispatcher.ClientRef)
		if err != nil {
			return err
		}
		_, err = AppendLaneActorDispatchTx(context.Background(), transaction, prepared)
		return err
	}); err != nil {
		t.Fatalf("materialize the dispatched lane attempt %s: %v", attemptID, err)
	}
}

func (f crossAttemptFixture) dispatch(t *testing.T, attemptID string, lane LaneDefinition, dispatcher WorkflowActor) {
	t.Helper()
	f.authorizeDispatch(t, attemptID, lane, dispatcher)
	f.materializeDispatch(t, attemptID, lane, dispatcher)
}

func (f crossAttemptFixture) completeReport(t *testing.T, attemptID string, lane LaneDefinition, at int64) {
	t.Helper()
	if err := ApplyOperation(context.Background(), f.s, Operation{Events: []Event{
		jobBoundCompletionEvent(f.workID, "cross-attempt-completed-"+attemptID, attemptID, lane, time.Unix(at, 0).UTC(), nil, workerFixtureDirectory(t, f.s, f.workID)),
	}}); err != nil {
		t.Fatalf("record the completed report of %s: %v", attemptID, err)
	}
}

// completeCorrectiveReview records a corrective report whose disposition and
// findings must survive another attempt's rejection unchanged.
func (f crossAttemptFixture) completeCorrectiveReview(t *testing.T, attemptID string, at int64) {
	t.Helper()
	event := reviewCompleteEventV3(f.workID, attemptID, f.reviewLane, reviewBlockFixture(), workerFixtureDirectory(t, f.s, f.workID))
	event.EventID, event.OccurredAt = "cross-attempt-completed-"+attemptID, time.Unix(at, 0).UTC()
	if err := ApplyOperation(context.Background(), f.s, Operation{Events: []Event{event}}); err != nil {
		t.Fatalf("record the corrective review report of %s: %v", attemptID, err)
	}
}

// buildTwoAttemptHistory materializes the paired history: the research result
// R dispatched and completed at epoch 1, then the independent corrective review V
// dispatched and completed at epoch 2.
func (f crossAttemptFixture) buildTwoAttemptHistory(t *testing.T) {
	t.Helper()
	f.dispatch(t, f.attemptR, f.researchLane, f.dispatcher)
	f.completeReport(t, f.attemptR, f.researchLane, 21)
	f.dispatch(t, f.attemptV, f.reviewLane, f.dispatcher)
	f.completeCorrectiveReview(t, f.attemptV, 22)
}

func (f crossAttemptFixture) rejectRequest(t *testing.T, expectedVersion int64, attemptID string, epoch int64, suffix string, at int64) WorkflowActionExecutionRequest {
	t.Helper()
	key := "reject-" + f.workID
	if suffix != "" {
		key += "-" + suffix
	}
	return WorkflowActionExecutionRequest{
		WorkID: f.workID, ExpectedVersion: expectedVersion, ActionID: "reject_worker_result",
		Payload: mustJSONValue(map[string]any{
			"attempt_id": attemptID, "attempt_epoch": epoch,
			"diagnosis":     "the epoch-1 research result does not satisfy the approved outcome",
			"strategy":      "redo the research obligation in a fresh fenced attempt",
			"predicate_ids": []string{"predicate:return-route"}, "evidence_refs": []string{"evidence:research-rejection"},
		}),
		SessionWorktree: dispatchSessionWorktree(t, f.s, f.workID), Actor: f.fixture.owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("e", 64), IdempotencyIdentity: key, OperationID: key,
		PrincipalRef: f.fixture.owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: key,
		RequestID: "request:" + key, ContractDigest: testManifestDigest, Now: time.Unix(at, 0).UTC(),
	}
}

func (f crossAttemptFixture) invokeReject(t *testing.T, expectedVersion int64, attemptID string, epoch int64, suffix string, at int64) (WorkflowActionExecutionResult, error) {
	t.Helper()
	return invokeWorkflowActionForCD0059(context.Background(), t, f.s, f.rejectRequest(t, expectedVersion, attemptID, epoch, suffix, at))
}

// acceptAttemptRequest builds one accept_worker_result request at the named attempt pair.
func (f crossAttemptFixture) acceptAttemptRequest(t *testing.T, expectedVersion int64, attemptID string, epoch int64, suffix string, at int64) WorkflowActionExecutionRequest {
	t.Helper()
	const actionID = "accept_worker_result"
	key := actionID + "-" + suffix + "-" + f.workID
	return WorkflowActionExecutionRequest{
		WorkID: f.workID, ExpectedVersion: expectedVersion, ActionID: actionID,
		Payload:         mustJSONValue(map[string]any{"attempt_id": attemptID, "attempt_epoch": epoch}),
		SessionWorktree: dispatchSessionWorktree(t, f.s, f.workID), Actor: f.fixture.owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("c", 64), IdempotencyIdentity: key, OperationID: key,
		PrincipalRef: f.fixture.owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: key,
		RequestID: "request:" + key, ContractDigest: testManifestDigest, Now: time.Unix(at, 0).UTC(),
	}
}

func (f crossAttemptFixture) eventCount(t *testing.T, kind, actionID string) int64 {
	t.Helper()
	query, args := `SELECT count(*) FROM effect_receipts WHERE subject_type='work_item' AND subject_id=? AND kind=?`, []any{f.workID, kind}
	if actionID != "" {
		if kind != WorkflowActionCompleted {
			t.Fatalf("action-specific count requires a completion, got %s", kind)
		}
		switch actionID {
		case "dispatch_worker":
			query, args = `SELECT count(*) FROM worker_authorizations WHERE work_id=?`, []any{f.workID}
		case "accept_worker_result", "accept_worker_evidence", "reject_worker_result", "record_worker_failure":
			query, args = `SELECT count(*) FROM worker_attempt_dispositions WHERE work_id=? AND action_id=?`, []any{f.workID, actionID}
		case "record_verdict":
			query, args = `SELECT count(*) FROM effect_receipts WHERE subject_type='work_item' AND subject_id=? AND kind=?`, []any{f.workID, WorkflowVerdictRecorded}
		default:
			t.Fatalf("no current action-count owner for %s", actionID)
		}
	}
	var count int64
	if err := f.s.DatabaseForTesting().QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func (f crossAttemptFixture) rejectionCount(t *testing.T, attemptID string, epoch int64) int64 {
	t.Helper()
	var count int64
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_attempt_dispositions WHERE work_id=? AND action_id='reject_worker_result' AND attempt_id=? AND json_extract(payload,'$.attempt_epoch')=?`, f.workID, attemptID, epoch).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// rejectableResults reads current rejection eligibility through admission's signature.
func (f crossAttemptFixture) rejectableResults(t *testing.T, _ int64) []workflowRejectableResult {
	t.Helper()
	results, err := workflowRejectableWorkerResults(context.Background(), f.s.DatabaseForTesting(), f.workID, f.definition, f.step, "test", 0)
	if err != nil {
		t.Fatalf("read the rejectable worker results: %v", err)
	}
	return results
}

func crossAttemptResultsContain(results []workflowRejectableResult, attemptID string, epoch int64) bool {
	for _, result := range results {
		if result.AttemptID == attemptID && result.AttemptEpoch == epoch {
			return true
		}
	}
	return false
}

// crossAttemptOwnerRef derives the canonical actor reference for one fixture
// actor.
func crossAttemptOwnerRef(t *testing.T, owner WorkflowActor) string {
	t.Helper()
	ref, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

// expectMembershipRefusal asserts the typed exact-pair refusal the rejection
// admission returns when the named pair is not an eligible completed result.
func expectMembershipRefusal(t *testing.T, err error, context string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s unexpectedly succeeded", context)
	}
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindIllegalLifecycleTransition {
		t.Fatalf("%s = %v, want %s", context, err, KindIllegalLifecycleTransition)
	}
	if failure.Detail != "worker result rejection does not name an eligible completed result" {
		t.Fatalf("%s detail = %q, want the exact-pair membership refusal", context, failure.Detail)
	}
}

// forgedRejectCompletionEvent authors one action_completed rejection event
// directly at the completion fold, bypassing the public payload guard. The
// completion values mirror the fields the boundary route records.
func (f crossAttemptFixture) forgedRejectCompletionEvent(t *testing.T, eventID, attemptID string, epoch int64, actorRef string, at int64) Event {
	t.Helper()
	version := readWorkVersion(t, f.s, f.workID)
	return workflowTypedEvent(eventID, WorkflowActionCompleted, f.workID, actorRef, time.Unix(at, 0).UTC(), version, map[string]any{
		"step_id": f.step, "action_id": "reject_worker_result",
		"attempt_epoch": epoch, "worker_attempt_id": attemptID, "actor_ref": actorRef,
		"correction_diagnosis":     "the research result does not satisfy the approved outcome",
		"correction_strategy":      "redo the research obligation in a fresh fenced attempt",
		"correction_predicate_ids": []string{"predicate:return-route"},
		"correction_evidence_refs": []string{"evidence:research-rejection"},
	})
}

// applyWitnessEvent applies one directly authored workflow event through the
// white-box fold route; a refusal rolls the whole operation back.
func (f crossAttemptFixture) applyWitnessEvent(t *testing.T, event Event) error {
	t.Helper()
	version := readWorkVersion(t, f.s, f.workID)
	return applyWorkflowTestOperation(context.Background(), f.s, Operation{
		Events:           []Event{event},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, f.workID): version},
	})
}

func TestCrossAttemptResultDisposition(t *testing.T) {
	t.Parallel()
	const workID = "cross-attempt-disposition"
	ctx := context.Background()
	f := seedCrossAttemptFixture(t, workID)
	s := f.s
	attemptR, attemptV := f.attemptR, f.attemptV
	f.buildTwoAttemptHistory(t)

	epochR := crossAttemptDispatchEpoch(t, s, workID, attemptR)
	epochV := crossAttemptDispatchEpoch(t, s, workID, attemptV)
	if epochR != 1 || epochV != 2 {
		t.Fatalf("attempt epochs = %d/%d, want the research result at epoch 1 and the review at epoch 2", epochR, epochV)
	}
	laneActorR := crossAttemptDispatchedLaneActor(t, s, workID, attemptR)
	laneActorV := crossAttemptDispatchedLaneActor(t, s, workID, attemptV)
	if laneActorR == "" || laneActorR == laneActorV {
		t.Fatalf("dispatched lane actors = %q versus %q, want the earlier attempt's own recorded executor, distinct from the review's", laneActorR, laneActorV)
	}
	pin := issue1013Pin(t, s, workID)
	if pin.Step != "investigate" || !workPinContainsAction(pin.NextValidIntents, "reject_worker_result") {
		t.Fatalf("work pin = %q with intents %#v, want investigate advertising reject_worker_result", pin.Step, pin.NextValidIntents)
	}

	reviewDispatchBytes := crossAttemptEventPayload(t, s, workID, WorkerDispatched, attemptV)
	reviewCompletedBytes := crossAttemptEventPayload(t, s, workID, WorkerCompleted, attemptV)
	researchDispatchBytes := crossAttemptEventPayload(t, s, workID, WorkerDispatched, attemptR)
	startsBefore := f.eventCount(t, WorkflowActionStarted, "")
	dispatchesBefore := f.eventCount(t, WorkerDispatched, "")
	reportsBefore := f.eventCount(t, WorkerCompleted, "")
	versionBefore := readWorkVersion(t, s, workID)

	// The latest-start freshness fence keeps its own answer beside the
	// completed review, before any rejection exists: the research attempt
	// dispatched at epoch 1, the step's latest start is the review's epoch-2
	// dispatch, so accepting that stale result as a positive step advance
	// refuses on the exact epoch mismatch. The probe stands before the
	// rejection because the unconsumed-rejection advance hold refuses every
	// accept after it before a payload can reach this fence.
	if _, err := invokeWorkflowActionForCD0059(ctx, t, s, f.acceptAttemptRequest(t, versionBefore, attemptR, epochR, "rejected", 45)); err == nil {
		t.Fatal("accepting the stale epoch-1 research result as a positive step advance unexpectedly succeeded")
	} else {
		var freshness *Failure
		if !errors.As(err, &freshness) || freshness.Kind != KindIllegalLifecycleTransition {
			t.Fatalf("accept of the stale epoch-1 research result = %v, want %s", err, KindIllegalLifecycleTransition)
		}
		if freshness.Detail != "worker attempt epoch does not match the latest workflow action start" {
			t.Fatalf("accept of the stale epoch-1 research result detail = %q, want the latest-start freshness refusal", freshness.Detail)
		}
	}
	if got := readWorkVersion(t, s, workID); got != versionBefore {
		t.Fatalf("refused accept of the stale research result left version = %d, want %d", got, versionBefore)
	}
	if step := currentStep(t, s, workID); step != "investigate" {
		t.Fatalf("refused accept of the stale research result left step = %q, want investigate", step)
	}

	result, err := f.invokeReject(t, versionBefore, attemptR, epochR, "", 50)
	if err != nil {
		t.Fatalf("reject the epoch-1 research result beside the later review: %v", err)
	}
	if result.ResultingVersion != versionBefore+1 {
		t.Fatalf("rejection resulting version = %d, want one appended version over %d", result.ResultingVersion, versionBefore)
	}
	if rejections := f.rejectionCount(t, attemptR, epochR); rejections != 1 {
		t.Fatalf("rejection completions naming the research attempt = %d, want exactly one", rejections)
	}
	if step := currentStep(t, s, workID); step != "investigate" {
		t.Fatalf("current step after the rejection = %q, want investigate", step)
	}
	if got := f.eventCount(t, WorkflowActionStarted, ""); got != startsBefore {
		t.Fatalf("action starts after the rejection = %d, want %d", got, startsBefore)
	}
	if got := f.eventCount(t, WorkerDispatched, ""); got != dispatchesBefore {
		t.Fatalf("worker dispatches after the rejection = %d, want %d", got, dispatchesBefore)
	}
	if got := f.eventCount(t, WorkerCompleted, ""); got != reportsBefore {
		t.Fatalf("worker reports after the rejection = %d, want %d", got, reportsBefore)
	}
	for _, actionID := range []string{"accept_worker_result", "accept_worker_evidence", "record_verdict"} {
		if got := f.eventCount(t, WorkflowActionCompleted, actionID); got != 0 {
			t.Fatalf("%s events after the rejection = %d, want none", actionID, got)
		}
	}
	pin = issue1013Pin(t, s, workID)
	if pin.Correction == nil || pin.Correction.Disposition != "rejected" || pin.Correction.FailedAttemptID != attemptR || pin.Correction.FailedAttemptEpoch != epochR {
		t.Fatalf("correction pin after the rejection = %#v, want the rejected epoch-1 research attempt", pin.Correction)
	}
	if pin.Correction.SourceEventID == "" || pin.Correction.SourceEventSeq < 1 {
		t.Fatalf("correction pin carries no source event identity: %#v", pin.Correction)
	}
	if string(crossAttemptEventPayload(t, s, workID, WorkerDispatched, attemptV)) != string(reviewDispatchBytes) {
		t.Fatal("the rejection rewrote the review dispatch bytes")
	}
	if string(crossAttemptEventPayload(t, s, workID, WorkerCompleted, attemptV)) != string(reviewCompletedBytes) {
		t.Fatal("the rejection rewrote the review report bytes")
	}
	if string(crossAttemptEventPayload(t, s, workID, WorkerDispatched, attemptR)) != string(researchDispatchBytes) {
		t.Fatal("the rejection rewrote the research dispatch bytes")
	}
	var reviewBefore, reviewAfter WorkerCompletedPayload
	if err := json.Unmarshal(reviewCompletedBytes, &reviewBefore); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(crossAttemptEventPayload(t, s, workID, WorkerCompleted, attemptV), &reviewAfter); err != nil {
		t.Fatal(err)
	}
	if reviewAfter.Review == nil || reviewAfter.Review.Disposition != "correction_required" {
		t.Fatalf("review disposition after the rejection = %#v, want the untouched correction_required judgment", reviewAfter.Review)
	}
	if !reflect.DeepEqual(reviewAfter.Review.Findings, reviewBefore.Review.Findings) {
		t.Fatalf("review findings after the rejection = %#v, want the reported finding preserved as completed", reviewAfter.Review.Findings)
	}
	var reviewDisposals int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_attempt_dispositions WHERE work_id=? AND action_id IN ('accept_worker_result','accept_worker_evidence','reject_worker_result','record_worker_failure') AND attempt_id=?`, workID, attemptV).Scan(&reviewDisposals); err != nil {
		t.Fatal(err)
	}
	if reviewDisposals != 0 {
		t.Fatalf("dispositions naming the review attempt = %d, want none", reviewDisposals)
	}

	acceptVersion := result.ResultingVersion
	startsBeforeAccepts := f.eventCount(t, WorkflowActionStarted, "")
	completionsBeforeAccepts := f.eventCount(t, WorkflowActionCompleted, "")

	// A report without admitted and host-observed candidate bindings cannot
	// acquire current Review authority. Refusal leaves the research rejection
	// and its correction untouched.
	if _, err := invokeWorkflowActionForCD0059(ctx, t, s, f.acceptAttemptRequest(t, acceptVersion, attemptV, epochV, "review", 52)); err == nil {
		t.Fatal("accepting the completed review while the rejected research result is unconsumed unexpectedly succeeded")
	} else {
		var hold *Failure
		if !errors.As(err, &hold) || hold.Kind != KindStaleRequiresReview {
			t.Fatalf("accept of the unbound review after the rejection = %v, want %s", err, KindStaleRequiresReview)
		}
		if hold.Detail != "Review input/output does not match its admitted input and clean host-observed output" {
			t.Fatalf("unbound review acceptance detail = %q, want the candidate-binding refusal", hold.Detail)
		}
	}
	if got := readWorkVersion(t, s, workID); got != acceptVersion {
		t.Fatalf("refused accept of the completed review left version = %d, want %d", got, acceptVersion)
	}
	if step := currentStep(t, s, workID); step != "investigate" {
		t.Fatalf("refused accept of the completed review left step = %q, want investigate", step)
	}
	if got := f.eventCount(t, WorkflowActionStarted, ""); got != startsBeforeAccepts {
		t.Fatalf("action starts after the refused accepts = %d, want %d", got, startsBeforeAccepts)
	}
	if got := f.eventCount(t, WorkflowActionCompleted, ""); got != completionsBeforeAccepts {
		t.Fatalf("action completions after the refused accepts = %d, want %d", got, completionsBeforeAccepts)
	}
	if pinAfter := issue1013Pin(t, s, workID); !reflect.DeepEqual(pinAfter.Correction, pin.Correction) {
		t.Fatalf("correction pin after the refused accepts = %#v, want the rejected epoch-1 research attempt preserved: %#v", pinAfter.Correction, pin.Correction)
	}
}

// TestCrossAttemptRejectionExactPairTable pins the exact-pair refusal table at
// both the public payload guard and the direct completion fold, with the real
// pair staying positive.
func TestCrossAttemptRejectionExactPairTable(t *testing.T) {
	t.Parallel()
	const workID = "cross-attempt-exact-pair"
	f := seedCrossAttemptFixture(t, workID)
	f.buildTwoAttemptHistory(t)
	attemptW := "attempt:" + workID + ":3"
	f.dispatch(t, attemptW, f.researchLane, f.dispatcher) // dispatched, never completed
	epochR := crossAttemptDispatchEpoch(t, f.s, workID, f.attemptR)
	epochV := crossAttemptDispatchEpoch(t, f.s, workID, f.attemptV)
	epochW := crossAttemptDispatchEpoch(t, f.s, workID, attemptW)

	// A foreign work item's completed result, materialized in its own
	// openTemp-backed fixture so the named attempt exists but belongs to
	// another work item's history.
	foreign := seedCrossAttemptFixture(t, "cross-attempt-exact-pair-foreign")
	foreign.buildTwoAttemptHistory(t)

	versionBefore := readWorkVersion(t, f.s, workID)
	startsBefore := f.eventCount(t, WorkflowActionStarted, "")
	dispatchesBefore := f.eventCount(t, WorkerDispatched, "")
	reportsBefore := f.eventCount(t, WorkerCompleted, "")
	completionsBefore := f.eventCount(t, WorkflowActionCompleted, "")

	refusals := []struct {
		name      string
		attemptID string
		epoch     int64
	}{
		{name: "the research result at the review's epoch", attemptID: f.attemptR, epoch: epochV},
		{name: "the research result at an unearned epoch", attemptID: f.attemptR, epoch: epochV + 1},
		{name: "the review at the research result's epoch", attemptID: f.attemptV, epoch: epochR},
		{name: "an unknown attempt", attemptID: "attempt:cross-attempt-nowhere", epoch: 1},
		{name: "a foreign work item's completed result", attemptID: foreign.attemptR, epoch: 1},
		{name: "a dispatched but uncompleted attempt", attemptID: attemptW, epoch: epochW},
	}
	// Direct completion-fold membership: each forged rejection completion
	// refuses through validateWorkerResultRejection at the fold — the fold
	// validates exact membership before it queries the attempt projection, so
	// unknown and foreign targets refuse with the same typed membership
	// refusal as every other pair outside the table.
	for _, bad := range refusals {
		event := f.forgedRejectCompletionEvent(t, "cross-attempt-fold-refusal-"+bad.attemptID+"-"+strconv.FormatInt(bad.epoch, 10), bad.attemptID, bad.epoch, crossAttemptOwnerRef(t, f.fixture.owner), 40)
		if err := f.applyWitnessEvent(t, event); err == nil {
			t.Fatalf("the fold admitted a forged rejection of %s", bad.name)
		} else {
			expectMembershipRefusal(t, err, "the fold's rejection of "+bad.name)
		}
	}

	// Public payload guard: the same table through the action boundary.
	for i, bad := range refusals {
		_, err := f.invokeReject(t, readWorkVersion(t, f.s, workID), bad.attemptID, bad.epoch, "table-"+strconv.Itoa(i), 41)
		expectMembershipRefusal(t, err, "the guard's rejection of "+bad.name)
	}

	if got := readWorkVersion(t, f.s, workID); got != versionBefore {
		t.Fatalf("version after the refused table = %d, want %d", got, versionBefore)
	}
	if got := f.eventCount(t, WorkflowActionStarted, ""); got != startsBefore {
		t.Fatalf("action starts after the refused table = %d, want %d", got, startsBefore)
	}
	if got := f.eventCount(t, WorkerDispatched, ""); got != dispatchesBefore {
		t.Fatalf("worker dispatches after the refused table = %d, want %d", got, dispatchesBefore)
	}
	if got := f.eventCount(t, WorkerCompleted, ""); got != reportsBefore {
		t.Fatalf("worker reports after the refused table = %d, want %d", got, reportsBefore)
	}
	if got := f.eventCount(t, WorkflowActionCompleted, ""); got != completionsBefore {
		t.Fatalf("action completions after the refused table = %d, want %d", got, completionsBefore)
	}
	if pin := issue1013Pin(t, f.s, workID); pin.Correction != nil {
		t.Fatalf("correction pin after the refused table = %#v, want no recorded correction", pin.Correction)
	}
	if results := f.rejectableResults(t, 0); !crossAttemptResultsContain(results, f.attemptR, epochR) || !crossAttemptResultsContain(results, f.attemptV, epochV) || crossAttemptResultsContain(results, attemptW, epochW) {
		t.Fatalf("rejectable results after the refused table = %#v, want the two completed results without the uncompleted one", results)
	}

	// The real R/1 pair remains positive at the guard.
	_, err := f.invokeReject(t, versionBefore, f.attemptR, epochR, "", 42)
	if err != nil {
		t.Fatalf("reject the real epoch-1 research result: %v", err)
	}
	if got := readWorkVersion(t, f.s, workID); got != versionBefore+1 {
		t.Fatalf("version after the real rejection = %d, want %d", got, versionBefore+1)
	}
	if rejections := f.rejectionCount(t, f.attemptR, epochR); rejections != 1 {
		t.Fatalf("rejection completions naming the research attempt = %d, want exactly one", rejections)
	}

	// The direct completion fold admits the exact V/2 pair on its own: the
	// exact-pair table governs the fold the same way it governs the guard.
	witness := f.forgedRejectCompletionEvent(t, "cross-attempt-fold-reject-review", f.attemptV, epochV, crossAttemptOwnerRef(t, f.fixture.owner), 43)
	if err := f.applyWitnessEvent(t, witness); err != nil {
		t.Fatalf("the fold refused the exact epoch-2 review pair: %v", err)
	}
	if rejections := f.rejectionCount(t, f.attemptV, epochV); rejections != 1 {
		t.Fatalf("rejection completions naming the review attempt = %d, want exactly one", rejections)
	}
	pin := issue1013Pin(t, f.s, workID)
	if pin.Correction == nil || pin.Correction.FailedAttemptID != f.attemptV || pin.Correction.FailedAttemptEpoch != epochV {
		t.Fatalf("correction pin after the fold-level review rejection = %#v, want the rejected epoch-2 review attempt", pin.Correction)
	}
	if got := f.rejectionCount(t, attemptW, epochW); got != 0 {
		t.Fatalf("rejection completions naming the uncompleted attempt = %d, want none", got)
	}
}

// TestCrossAttemptRejectionOnceOnly pins the once-only rule: a rejected pair
// leaves the eligibility table and cannot be rejected twice.
func TestCrossAttemptRejectionOnceOnly(t *testing.T) {
	t.Parallel()
	const workID = "cross-attempt-once-only"
	ctx := context.Background()
	f := seedCrossAttemptFixture(t, workID)
	f.buildTwoAttemptHistory(t)
	epochR := crossAttemptDispatchEpoch(t, f.s, workID, f.attemptR)
	epochV := crossAttemptDispatchEpoch(t, f.s, workID, f.attemptV)
	versionBefore := readWorkVersion(t, f.s, workID)
	startsBefore := f.eventCount(t, WorkflowActionStarted, "")
	completionsBefore := f.eventCount(t, WorkflowActionCompleted, "")

	result, err := f.invokeReject(t, versionBefore, f.attemptR, epochR, "", 50)
	if err != nil {
		t.Fatalf("reject the epoch-1 research result: %v", err)
	}
	if rejections := f.rejectionCount(t, f.attemptR, epochR); rejections != 1 {
		t.Fatalf("rejection completions after the first rejection = %d, want exactly one", rejections)
	}

	// A second, freshly keyed rejection of the same pair refuses: the
	// rejection consumed R's eligibility, and the refusal leaves no effects.
	_, err = f.invokeReject(t, result.ResultingVersion, f.attemptR, epochR, "again", 51)
	expectMembershipRefusal(t, err, "the second rejection of the already-rejected result")
	if got := readWorkVersion(t, f.s, workID); got != result.ResultingVersion {
		t.Fatalf("version after the refused second rejection = %d, want %d", got, result.ResultingVersion)
	}
	if rejections := f.rejectionCount(t, f.attemptR, epochR); rejections != 1 {
		t.Fatalf("rejection completions after the refused second rejection = %d, want exactly one", rejections)
	}
	// The sibling review keeps its open rejection route: the pin still
	// advertises reject_worker_result for V, and the correction still names R.
	pin := issue1013Pin(t, f.s, workID)
	if !workPinContainsAction(pin.NextValidIntents, "reject_worker_result") {
		t.Fatalf("work pin intents %#v, want reject_worker_result still advertised for the undispositioned review", pin.NextValidIntents)
	}
	if pin.Correction == nil || pin.Correction.FailedAttemptID != f.attemptR || pin.Correction.FailedAttemptEpoch != epochR {
		t.Fatalf("correction pin after the refused second rejection = %#v, want the rejected research attempt preserved", pin.Correction)
	}
	if results := f.rejectableResults(t, 0); !crossAttemptResultsContain(results, f.attemptV, epochV) || crossAttemptResultsContain(results, f.attemptR, epochR) {
		t.Fatalf("rejectable results after the first rejection = %#v, want the review without the rejected result", results)
	}

	// Repeating the ORIGINAL identical request at the operation boundary
	// cannot append a second completion. The byte-identical repeat carries the
	// original expected version, so the boundary's version gate refuses it
	// before anything else; the refusal leaves the original rejection's
	// committed state standing.
	_, err = f.invokeReject(t, versionBefore, f.attemptR, epochR, "", 50)
	if err == nil {
		t.Fatal("the repeated original rejection request unexpectedly succeeded")
	} else {
		var conflict *Failure
		if !errors.As(err, &conflict) || conflict.Kind != KindVersionConflict {
			t.Fatalf("the repeated original rejection request = %v, want %s", err, KindVersionConflict)
		}
	}
	if rejections := f.rejectionCount(t, f.attemptR, epochR); rejections != 1 {
		t.Fatalf("rejection completions after the repeated original request = %d, want exactly one", rejections)
	}
	if got := readWorkVersion(t, f.s, workID); got != result.ResultingVersion {
		t.Fatalf("version after the repeated original request = %d, want %d", got, result.ResultingVersion)
	}

	// The same operation identity replayed at the current version reaches the
	// recovery guard, whose exact-pair table refuses the consumed target
	// before any write.
	_, err = f.invokeReject(t, result.ResultingVersion, f.attemptR, epochR, "", 51)
	expectMembershipRefusal(t, err, "the replayed identity against the consumed target")
	if rejections := f.rejectionCount(t, f.attemptR, epochR); rejections != 1 {
		t.Fatalf("rejection completions after the replayed identity = %d, want exactly one", rejections)
	}

	// The same durable operation identity naming a still-eligible target
	// reaches the durable claim and collides: one operation identity claims
	// exactly one rejection, whatever payload it re-carries.
	_, err = f.invokeReject(t, result.ResultingVersion, f.attemptV, epochV, "", 52)
	if err == nil {
		t.Fatal("the claimed identity against another target unexpectedly succeeded")
	} else {
		var claimed *Failure
		if !errors.As(err, &claimed) || claimed.Kind != KindIdempotencyConflict {
			t.Fatalf("the claimed identity against another target = %v, want %s", err, KindIdempotencyConflict)
		}
	}
	if rejections := f.rejectionCount(t, f.attemptV, epochV); rejections != 0 {
		t.Fatalf("rejection completions naming the review after the identity collision = %d, want none", rejections)
	}
	if got := readWorkVersion(t, f.s, workID); got != result.ResultingVersion {
		t.Fatalf("version after the identity collision = %d, want %d", got, result.ResultingVersion)
	}

	// The boundary's exact-replay hook preserves the original result: a
	// caller that finds the claimed operation commits nothing new, and the
	// mutation closure never runs.
	preflight := WorkflowActionPreflightRequest{
		WorkID: workID, ExpectedVersion: versionBefore, ActionID: "reject_worker_result",
		Payload: f.rejectRequest(t, versionBefore, f.attemptR, epochR, "", 50).Payload, Actor: f.fixture.owner,
	}
	replayRan := false
	if err := AuthorizeWorkflowActionAtBoundaryWithPreflightTx(ctx, f.s, BuiltinWorkflowRegistry(), preflight, func(transaction *Transaction) (bool, error) {
		tx, err := transactionSQL(transaction, "cross-attempt-replay-witness")
		if err != nil {
			return false, err
		}
		var claimed int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM durable_operations WHERE op_id=?`, "reject-"+workID).Scan(&claimed); err != nil {
			return false, err
		}
		return claimed == 1, nil
	}, nil, func(transaction *Transaction) error {
		replayRan = true
		return nil
	}); err != nil {
		t.Fatalf("the exact-replay boundary hook failed: %v", err)
	}
	if replayRan {
		t.Fatal("the exact-replay boundary hook ran the action mutation a second time")
	}
	if rejections := f.rejectionCount(t, f.attemptR, epochR); rejections != 1 {
		t.Fatalf("rejection completions after the exact-replay hook = %d, want exactly one", rejections)
	}
	if got := readWorkVersion(t, f.s, workID); got != result.ResultingVersion {
		t.Fatalf("version after the exact-replay hook = %d, want %d", got, result.ResultingVersion)
	}
	if got := f.eventCount(t, WorkflowActionStarted, ""); got != startsBefore {
		t.Fatalf("action starts after the once-only section = %d, want %d", got, startsBefore)
	}
	if got := f.eventCount(t, WorkflowActionCompleted, ""); got != completionsBefore+1 {
		t.Fatalf("action completions after the once-only section = %d, want %d", got, completionsBefore+1)
	}
}

// A checkpoint start closes the prior result's interval. Rejection consumes
// only the current result, and a fresh result remains independently rejectable.
func TestCrossAttemptCheckpointRejectsEachResultOnce(t *testing.T) {
	t.Parallel()
	const workID = "cross-attempt-evidence-hold"
	f := seedCrossAttemptFixtureAtStep(t, workID, "conclude")
	attemptC1 := "attempt:" + workID + ":1"
	attemptC2 := "attempt:" + workID + ":2"
	attemptC3 := "attempt:" + workID + ":3"
	f.dispatch(t, attemptC1, f.reviewLane, f.dispatcher)
	epochC1 := crossAttemptDispatchEpoch(t, f.s, workID, attemptC1)
	f.completeCorrectiveReview(t, attemptC1, 31)

	// The next dispatch start at a checkpoint step closes the eligibility
	// interval: the checkpoint exact-once timing retires C1's eligibility
	// even before C2 completes.
	f.dispatch(t, attemptC2, f.reviewLane, f.dispatcher)
	epochC2 := crossAttemptDispatchEpoch(t, f.s, workID, attemptC2)
	if results := f.rejectableResults(t, 0); len(results) != 0 {
		t.Fatalf("rejectable results after the later checkpoint dispatch start = %#v, want the earlier review retired", results)
	}
	f.completeCorrectiveReview(t, attemptC2, 33)
	if results := f.rejectableResults(t, 0); !crossAttemptResultsContain(results, attemptC2, epochC2) || crossAttemptResultsContain(results, attemptC1, epochC1) {
		t.Fatalf("rejectable results after the second review completed = %#v, want only the current review", results)
	}

	// The rejection route itself stays available — the completed second
	// review is an eligible target — but the later checkpoint dispatch start
	// retired the earlier review from the exact-pair table, so rejecting C1
	// refuses on payload membership.
	_, err := f.invokeReject(t, readWorkVersion(t, f.s, workID), attemptC1, epochC1, "checkpoint", 34)
	expectMembershipRefusal(t, err, "the rejection of the interval-retired review")

	if _, err := f.invokeReject(t, readWorkVersion(t, f.s, workID), attemptC2, epochC2, "current", 36); err != nil {
		t.Fatalf("reject the current checkpoint result: %v", err)
	}
	if step := currentStep(t, f.s, workID); step != "conclude" {
		t.Fatalf("current step after rejection = %q, want conclude held", step)
	}
	if results := f.rejectableResults(t, 0); len(results) != 0 {
		t.Fatalf("rejectable results after rejection = %#v, want the exact result consumed", results)
	}
	// The disposed result can no longer be rejected. No completed candidate
	// stands open, so the action-only preflight classifies the request
	// off-step and workflowAdmit refuses it before any action guard runs —
	// the pure admission owns this refusal, not a guard.
	_, err = f.invokeReject(t, readWorkVersion(t, f.s, workID), attemptC2, epochC2, "after-hold", 37)
	if err == nil {
		t.Fatal("the rejection of the hold-accepted review unexpectedly succeeded")
	} else {
		var offStep *Failure
		if !errors.As(err, &offStep) || offStep.Kind != KindIllegalLifecycleTransition || offStep.Detail != "workflow action is not declared on the current step" {
			t.Fatalf("the rejection of the hold-accepted review = %v, want the admission off-step refusal", err)
		}
	}
	if pin := issue1013Pin(t, f.s, workID); workPinContainsAction(pin.NextValidIntents, "reject_worker_result") {
		t.Fatalf("work pin intents %#v, want reject_worker_result hidden while no completed result stands open", pin.NextValidIntents)
	}

	// Disposing one result does not dispose a later result.
	f.dispatch(t, attemptC3, f.reviewLane, f.dispatcher)
	epochC3 := crossAttemptDispatchEpoch(t, f.s, workID, attemptC3)
	f.completeCorrectiveReview(t, attemptC3, 38)
	if results := f.rejectableResults(t, 0); !crossAttemptResultsContain(results, attemptC3, epochC3) {
		t.Fatalf("rejectable results after the third review completed = %#v, want the fresh review open", results)
	}
	pin := issue1013Pin(t, f.s, workID)
	if !workPinContainsAction(pin.NextValidIntents, "reject_worker_result") {
		t.Fatalf("work pin intents %#v, want reject_worker_result advertised for the fresh review", pin.NextValidIntents)
	}
	if _, err := f.invokeReject(t, readWorkVersion(t, f.s, workID), attemptC3, epochC3, "fresh", 39); err != nil {
		t.Fatalf("reject the fresh review beside the held acceptance: %v", err)
	}
	if step := currentStep(t, f.s, workID); step != "conclude" {
		t.Fatalf("current step after the fresh rejection = %q, want conclude held throughout", step)
	}
}

// TestCrossAttemptRejectionTargetActor checks Main authority for both dispatched
// workers and preserves the target's own executor provenance. The witness runs in a
// rollback transaction only.
func TestCrossAttemptRejectionTargetActor(t *testing.T) {
	t.Parallel()
	const workID = "cross-attempt-target-actor"
	f := seedCrossAttemptFixture(t, workID)
	f.buildTwoAttemptHistory(t)
	epochR := crossAttemptDispatchEpoch(t, f.s, workID, f.attemptR)
	// The witness transaction below holds the pool's sole connection, so every
	// epoch this test compares inside it is read before BeginTx.
	epochV := crossAttemptDispatchEpoch(t, f.s, workID, f.attemptV)
	laneActorR := crossAttemptDispatchedLaneActor(t, f.s, workID, f.attemptR)
	laneActorV := crossAttemptDispatchedLaneActor(t, f.s, workID, f.attemptV)
	ownerRef := crossAttemptOwnerRef(t, f.fixture.owner)
	versionBefore := readWorkVersion(t, f.s, workID)
	startsBefore := f.eventCount(t, WorkflowActionStarted, "")
	dispatchesBefore := f.eventCount(t, WorkerDispatched, "")
	reportsBefore := f.eventCount(t, WorkerCompleted, "")

	// The later review's dispatch replaced the instance executor, so a
	// comparison against the instance executor would ADMIT the research
	// attempt's own executor here. The refusal proves the original tuple.
	var instanceExecutor string
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT execution_actor_ref FROM workflow_instances WHERE work_id=?`, workID).Scan(&instanceExecutor); err != nil {
		t.Fatal(err)
	}
	if instanceExecutor != laneActorV {
		t.Fatalf("instance executor after the review dispatch = %q, want the review's recorded lane actor %q", instanceExecutor, laneActorV)
	}

	// Neither dispatched worker is Main, including the target's sibling.
	for index, actor := range []string{laneActorR, laneActorV} {
		wantDetail := []string{"a worker cannot accept its own result", "a worker cannot accept results, associate criterion proof, or complete work"}[index]
		event := f.forgedRejectCompletionEvent(t, "cross-attempt-worker-reject-"+strconv.Itoa(index), f.attemptR, epochR, actor, 44)
		if err := f.applyWitnessEvent(t, event); err == nil {
			t.Fatal("a dispatched worker rejected a result")
		} else {
			var refusal *Failure
			if !errors.As(err, &refusal) || refusal.Kind != KindUnauthorized {
				t.Fatalf("the worker rejection = %v, want %s", err, KindUnauthorized)
			}
			if refusal.Detail != wantDetail {
				t.Fatalf("the worker rejection detail = %q, want the Main authority refusal", refusal.Detail)
			}
		}
	}

	// An event actor and a payload actor that disagree refuse at the fold.
	mismatch := f.forgedRejectCompletionEvent(t, "cross-attempt-actor-mismatch", f.attemptR, epochR, ownerRef, 45)
	mismatch.Payload = mustJSONValue(map[string]any{
		"work_id": workID, "expected_version": versionBefore, "resulting_version": versionBefore + 1,
		"step_id": f.step, "action_id": "reject_worker_result",
		"attempt_epoch": epochR, "worker_attempt_id": f.attemptR, "actor_ref": laneActorV,
		"correction_diagnosis":     "the research result does not satisfy the approved outcome",
		"correction_strategy":      "redo the research obligation in a fresh fenced attempt",
		"correction_predicate_ids": []string{"predicate:return-route"},
		"correction_evidence_refs": []string{"evidence:research-rejection"},
	})
	if err := f.applyWitnessEvent(t, mismatch); err == nil {
		t.Fatal("a rejection whose event and payload actors disagree was admitted")
	} else {
		var refusal *Failure
		if !errors.As(err, &refusal) || refusal.Kind != KindUnauthorized {
			t.Fatalf("the actor-mismatch rejection = %v, want %s", err, KindUnauthorized)
		}
		if refusal.Detail != "completed action actor must match the authenticated event actor" {
			t.Fatalf("the actor-mismatch rejection detail = %q, want the completed-action actor match refusal", refusal.Detail)
		}
	}

	// Provenance witness: blanking the research dispatch's recorded executor
	// inside one transaction drops the attempt from the eligibility reader —
	// a missing modern authorization/executor grants no cross-epoch authority
	// under the step's later start. The witness is test damage: it mutates the
	// current descriptor only inside a rollback transaction, and no production route blanks a
	// recorded lane actor.
	var beforeWitness []byte
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT dispatch_payload FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, f.attemptR).Scan(&beforeWitness); err != nil {
		t.Fatal(err)
	}
	tx, err := f.s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(context.Background(), `UPDATE worker_attempts SET dispatch_payload=json_set(dispatch_payload,'$.lane_actor_ref','') WHERE work_id=? AND attempt_id=?`, workID, f.attemptR); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(context.Background(), `DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	damaged, err := workflowRejectableWorkerResults(context.Background(), tx, workID, f.definition, f.step, "test", 0)
	if err != nil {
		t.Fatalf("the provenance witness reader failed: %v", err)
	}
	if crossAttemptResultsContain(damaged, f.attemptR, epochR) {
		t.Fatalf("the provenance-stripped research attempt stayed eligible: %#v, want it dropped — no cross-epoch authority without a recorded executor", damaged)
	}
	if !crossAttemptResultsContain(damaged, f.attemptV, epochV) {
		t.Fatalf("the provenance-stripping witness disturbed the review's eligibility: %#v", damaged)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var afterWitness []byte
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT dispatch_payload FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, f.attemptR).Scan(&afterWitness); err != nil {
		t.Fatal(err)
	}
	if string(afterWitness) != string(beforeWitness) {
		t.Fatal("the rolled-back witness left the research dispatch provenance rewritten")
	}

	// The coordinator remains positive: the owner rejects the research result.
	if _, err := f.invokeReject(t, versionBefore, f.attemptR, epochR, "", 46); err != nil {
		t.Fatalf("the coordinator's rejection of the research result: %v", err)
	}
	if rejections := f.rejectionCount(t, f.attemptR, epochR); rejections != 1 {
		t.Fatalf("rejection completions naming the research attempt = %d, want exactly one", rejections)
	}
	if got := readWorkVersion(t, f.s, workID); got != versionBefore+1 {
		t.Fatalf("version after the coordinator's rejection = %d, want %d", got, versionBefore+1)
	}
	if got := f.eventCount(t, WorkflowActionStarted, ""); got != startsBefore {
		t.Fatalf("action starts after the actor section = %d, want %d", got, startsBefore)
	}
	if got := f.eventCount(t, WorkerDispatched, ""); got != dispatchesBefore {
		t.Fatalf("worker dispatches after the actor section = %d, want %d", got, dispatchesBefore)
	}
	if got := f.eventCount(t, WorkerCompleted, ""); got != reportsBefore {
		t.Fatalf("worker reports after the actor section = %d, want %d", got, reportsBefore)
	}
}

func TestCrossAttemptRejectionCurrentDisposalAndRetry(t *testing.T) {
	t.Parallel()
	const workID = "cross-attempt-current-disposal"
	f := seedCrossAttemptFixture(t, workID)
	f.buildTwoAttemptHistory(t)
	epochR := crossAttemptDispatchEpoch(t, f.s, workID, f.attemptR)
	epochV := crossAttemptDispatchEpoch(t, f.s, workID, f.attemptV)
	if _, err := f.invokeReject(t, readWorkVersion(t, f.s, workID), f.attemptR, epochR, "", 50); err != nil {
		t.Fatalf("reject the epoch-1 research result: %v", err)
	}
	current := f.rejectableResults(t, 0)
	if crossAttemptResultsContain(current, f.attemptR, epochR) || !crossAttemptResultsContain(current, f.attemptV, epochV) {
		t.Fatalf("current results=%#v, want only the undisposed review", current)
	}
	attemptW := "attempt:" + workID + ":3"
	correction, err := workflowCorrectionContextForDispatch(context.Background(), f.s.DatabaseForTesting(), workID, f.step, attemptW)
	if err != nil || correction == nil {
		t.Fatalf("correction-consuming retry context: %+v, %v", correction, err)
	}
	packet := joinPacketFor(t, f.s, workID, f.step, attemptW, f.researchLane.ID, f.researchLane.Digest)
	packet["inputs"].(map[string]any)["correction"] = correction
	if _, err := dispatchJoinAttempt(context.Background(), t, f.s, workID, readWorkVersion(t, f.s, workID), f.dispatcher, packet); err != nil {
		t.Fatalf("dispatch the correction-consuming retry: %v", err)
	}
	f.materializeDispatch(t, attemptW, f.researchLane, f.dispatcher)
	f.completeReport(t, attemptW, f.researchLane, 60)
	if _, err := invokeWorkflowActionForCD0059(context.Background(), t, f.s, f.acceptAttemptRequest(t, readWorkVersion(t, f.s, workID), attemptW, epochV+1, "retry", 61)); err != nil {
		t.Fatalf("accept the retry's completed result: %v", err)
	}
	if step := currentStep(t, f.s, workID); step != f.step {
		t.Fatalf("local retry acceptance advanced from %q to %q", f.step, step)
	}
	if results := f.rejectableResults(t, 0); crossAttemptResultsContain(results, f.attemptR, epochR) || crossAttemptResultsContain(results, attemptW, epochV+1) {
		t.Fatalf("disposed result became rejectable: %#v", results)
	}
}

// TestCrossAttemptCorrectionRetryConsumption pins the correction-consumption
// rule: the retry dispatch must consume the rejection's exact correction
// source identity, and refusal is atomic.
func TestCrossAttemptCorrectionRetryConsumption(t *testing.T) {
	t.Parallel()
	const workID = "cross-attempt-correction-retry"
	f := seedCrossAttemptFixture(t, workID)
	f.buildTwoAttemptHistory(t)
	epochR := crossAttemptDispatchEpoch(t, f.s, workID, f.attemptR)
	epochV := crossAttemptDispatchEpoch(t, f.s, workID, f.attemptV)
	versionBefore := readWorkVersion(t, f.s, workID)
	dispatchesBefore := f.eventCount(t, WorkerDispatched, "")
	reportsBefore := f.eventCount(t, WorkerCompleted, "")
	attemptW := "attempt:" + workID + ":3"

	if _, err := f.invokeReject(t, versionBefore, f.attemptR, epochR, "", 50); err != nil {
		t.Fatalf("reject the epoch-1 research result: %v", err)
	}

	correctionPacket := func(t *testing.T) map[string]any {
		t.Helper()
		correction, err := workflowCorrectionContextForDispatch(context.Background(), f.s.DatabaseForTesting(), workID, f.step, attemptW)
		if err != nil {
			t.Fatal(err)
		}
		if correction == nil {
			t.Fatal("no correction context stands after the rejection")
		}
		packet := joinPacketFor(t, f.s, workID, f.step, attemptW, f.researchLane.ID, f.researchLane.Digest)
		packet["inputs"].(map[string]any)["correction"] = correction
		return packet
	}

	// Missing correction: the plain packet refuses the dispatch.
	if _, err := dispatchJoinAttempt(context.Background(), t, f.s, workID, readWorkVersion(t, f.s, workID), f.dispatcher, joinPacketFor(t, f.s, workID, f.step, attemptW, f.researchLane.ID, f.researchLane.Digest)); err == nil {
		t.Fatal("a dispatch whose packet omits the standing correction unexpectedly succeeded")
	} else {
		var refusal *Failure
		if !errors.As(err, &refusal) || refusal.Kind != KindInvalidPayload || refusal.Detail != "worker packet does not consume the current correction context" {
			t.Fatalf("the correction-less dispatch = %v, want the correction-consumption refusal", err)
		}
	}
	// Substituted correction: a rewritten source identity refuses too.
	substitutedPacket := correctionPacket(t)
	substituted := *correctionPacketSource(t, substitutedPacket)
	substituted.SourceEventSeq++
	substitutedPacket["inputs"].(map[string]any)["correction"] = &substituted
	if _, err := dispatchJoinAttempt(context.Background(), t, f.s, workID, readWorkVersion(t, f.s, workID), f.dispatcher, substitutedPacket); err == nil {
		t.Fatal("a dispatch whose packet substitutes the correction source unexpectedly succeeded")
	} else {
		var refusal *Failure
		if !errors.As(err, &refusal) || refusal.Kind != KindInvalidPayload || refusal.Detail != "worker packet does not consume the current correction context" {
			t.Fatalf("the substituted-correction dispatch = %v, want the correction-consumption refusal", err)
		}
	}
	// Both refusals were atomic: no dispatch, no report, no version move.
	if got := readWorkVersion(t, f.s, workID); got != versionBefore+1 {
		t.Fatalf("version after the refused dispatches = %d, want %d", got, versionBefore+1)
	}
	if got := f.eventCount(t, WorkerDispatched, ""); got != dispatchesBefore {
		t.Fatalf("worker dispatches after the refused dispatches = %d, want %d", got, dispatchesBefore)
	}
	if got := f.eventCount(t, WorkerCompleted, ""); got != reportsBefore {
		t.Fatalf("worker reports after the refused dispatches = %d, want %d", got, reportsBefore)
	}

	// The correct dispatch materializes a fresh attempt at the next epoch.
	researchDispatchBytes := crossAttemptEventPayload(t, f.s, workID, WorkerDispatched, f.attemptR)
	reviewReportBytes := crossAttemptEventPayload(t, f.s, workID, WorkerCompleted, f.attemptV)
	if _, err := dispatchJoinAttempt(context.Background(), t, f.s, workID, readWorkVersion(t, f.s, workID), f.dispatcher, correctionPacket(t)); err != nil {
		t.Fatalf("dispatch the correction-consuming retry: %v", err)
	}
	f.materializeDispatch(t, attemptW, f.researchLane, f.dispatcher)
	epochW := crossAttemptDispatchEpoch(t, f.s, workID, attemptW)
	if epochW != epochV+1 {
		t.Fatalf("the retry attempt's epoch = %d, want the next epoch %d", epochW, epochV+1)
	}
	// R and V keep their identities and bytes: the retry relabeled nothing.
	if after := crossAttemptEventPayload(t, f.s, workID, WorkerDispatched, f.attemptR); string(after) != string(researchDispatchBytes) {
		t.Fatal("the retry rewrote the research dispatch bytes")
	}
	if after := crossAttemptEventPayload(t, f.s, workID, WorkerCompleted, f.attemptV); string(after) != string(reviewReportBytes) {
		t.Fatal("the retry rewrote the review report bytes")
	}
	if crossAttemptDispatchEpoch(t, f.s, workID, f.attemptR) != epochR || crossAttemptDispatchEpoch(t, f.s, workID, f.attemptV) != epochV {
		t.Fatal("the retry moved an earlier attempt's recorded epoch")
	}
	// The materialized dispatch consumes the correction: the pin clears and
	// the unconsumed-rejection advance hold no longer names this step.
	if pin := issue1013Pin(t, f.s, workID); pin.Correction != nil {
		t.Fatalf("correction pin after the consumed retry dispatch = %#v, want no standing correction", pin.Correction)
	}

	// The retry completes and local acceptance holds the step. Its materialized
	// dispatch already consumed the correction without granting a workflow exit.
	f.completeReport(t, attemptW, f.researchLane, 60)
	if _, err := invokeWorkflowActionForCD0059(context.Background(), t, f.s, f.acceptAttemptRequest(t, readWorkVersion(t, f.s, workID), attemptW, epochW, "cleared", 61)); err != nil {
		t.Fatalf("accept the retry's completed result after the hold cleared: %v", err)
	}
	if step := currentStep(t, f.s, workID); step != f.step {
		t.Fatalf("local retry acceptance advanced from %q to %q", f.step, step)
	}
}

// correctionPacketSource reads the correction context back out of one packet
// the correction builder produced, so the substituted witness mutates a real
// derived value rather than an invented one.
func correctionPacketSource(t *testing.T, packet map[string]any) *WorkflowCorrectionContext {
	t.Helper()
	inputs, ok := packet["inputs"].(map[string]any)
	if !ok {
		t.Fatal("the correction packet carries no inputs object")
	}
	correction, ok := inputs["correction"].(*WorkflowCorrectionContext)
	if !ok || correction == nil {
		t.Fatal("the correction packet carries no derived correction context")
	}
	return correction
}

// The combined rejection's retry answers to the dispatch admission (CD-0198
// D5), the takeover rule included. Rejecting an earlier result with a retry
// while another session's later attempt is undecided refuses the whole call;
// the rejection alone still commits, and the retry dispatch then carries the
// takeover approval of its own.
func TestCrossAttemptCombinedRetryOverAnotherSessionsAttemptRefusesWhole(t *testing.T) {
	t.Parallel()
	const workID = "cross-attempt-combined-takeover"
	ctx := context.Background()
	f := seedCrossAttemptFixture(t, workID)
	other := diagnosisS1Actor(workID, "other-dispatcher")
	otherRef, err := WorkflowActorRef(other)
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, f.s, workID)
	if err := applyWorkflowTestOperation(ctx, f.s, Operation{Events: []Event{
		workflowEvent("combined-takeover-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "actor_ref": otherRef, "principal_ref": other.PrincipalRef, "client_ref": other.ClientRef, "agent_ref": other.AgentRef, "session_ref": other.SessionRef, "actor_class": "agent"}),
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("record the other session's actor: %v", err)
	}
	f.dispatch(t, f.attemptR, f.researchLane, f.dispatcher)
	f.completeReport(t, f.attemptR, f.researchLane, 21)
	epochR := crossAttemptDispatchEpoch(t, f.s, workID, f.attemptR)
	var laneR string
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT json_extract(dispatch_payload,'$.lane_actor_ref') FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, f.attemptR).Scan(&laneR); err != nil {
		t.Fatal(err)
	}
	fields, err := json.Marshal(map[string]any{"attempt_id": f.attemptV, "worker_packet": joinPacketFor(t, f.s, workID, f.step, f.attemptV, f.reviewLane.ID, f.reviewLane.Digest)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := invokeWorkflowActionForCD0059(ctx, t, f.s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: readWorkVersion(t, f.s, workID), ActionID: "dispatch_worker", Payload: fields, Actor: other,
		ApprovedTakeover: &WorkflowStartTakeover{HeldEpoch: epochR, HolderActorRef: laneR}, SessionWorktree: dispatchSessionWorktree(t, f.s, workID),
		AcceptedInputsDigest: cd0059TestDigest(t, "combined-takeover-inputs"), ContractDigest: testManifestDigest, Tool: "concord_work_transition",
		IdempotencyKey: "combined-takeover-review", RequestID: "combined-takeover-review", IdempotencyIdentity: "combined-takeover-review", OperationID: "combined-takeover-review", PrincipalRef: other.PrincipalRef, Now: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("the other session's approved review dispatch: %v", err)
	}
	f.materializeDispatch(t, f.attemptV, f.reviewLane, other)
	f.completeCorrectiveReview(t, f.attemptV, 22)

	versionBefore := readWorkVersion(t, f.s, workID)
	combined := f.rejectRequest(t, versionBefore, f.attemptR, epochR, "combined", 51)
	combined.Payload = mustJSONValue(map[string]any{
		"attempt_id": f.attemptR, "attempt_epoch": epochR,
		"diagnosis":     "the epoch-1 research result does not satisfy the approved outcome",
		"strategy":      "redo the research obligation in a fresh fenced attempt",
		"predicate_ids": []string{"predicate:return-route"}, "evidence_refs": []string{"evidence:research-rejection"},
		"retry": map[string]any{"lane_id": f.researchLane.ID},
	})
	_, err = invokeWorkflowActionForCD0059(ctx, t, f.s, combined)
	var refusal *Failure
	if !errors.As(err, &refusal) || refusal.Kind != KindApprovalRequired || !strings.Contains(refusal.RecoveryAction, "reject without the retry payload") || !strings.Contains(refusal.Detail, "reject without the retry payload") {
		t.Fatalf("combined retry over the other session's undecided review = %v, want approval_required naming the rejection-only route", err)
	}
	if rejections := f.rejectionCount(t, f.attemptR, epochR); rejections != 0 {
		t.Fatalf("rejection completions after the refused combined call = %d, want none", rejections)
	}
	if got := readWorkVersion(t, f.s, workID); got != versionBefore {
		t.Fatalf("version after the refused combined call = %d, want %d", got, versionBefore)
	}
	if _, err := f.invokeReject(t, versionBefore, f.attemptR, epochR, "rejection-only", 52); err != nil {
		t.Fatalf("the rejection-only escape: %v", err)
	}
	epochV := crossAttemptDispatchEpoch(t, f.s, workID, f.attemptV)
	var laneV string
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT json_extract(dispatch_payload,'$.lane_actor_ref') FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, f.attemptV).Scan(&laneV); err != nil {
		t.Fatal(err)
	}
	retryAttempt := f.attemptR + ":retry"
	correction, err := workflowCorrectionContextForDispatch(ctx, f.s.DatabaseForTesting(), workID, f.step, retryAttempt)
	if err != nil || correction == nil {
		t.Fatalf("correction context for the retry dispatch = %v err %v, want the rejection's", correction, err)
	}
	retryPacket := joinPacketFor(t, f.s, workID, f.step, retryAttempt, f.researchLane.ID, f.researchLane.Digest)
	retryPacket["inputs"].(map[string]any)["correction"] = correction
	retryFields, err := json.Marshal(map[string]any{"attempt_id": retryAttempt, "worker_packet": retryPacket})
	if err != nil {
		t.Fatal(err)
	}
	retry := func(key string, approved *WorkflowStartTakeover) error {
		_, err := invokeWorkflowActionForCD0059(ctx, t, f.s, WorkflowActionExecutionRequest{
			WorkID: workID, ExpectedVersion: readWorkVersion(t, f.s, workID), ActionID: "dispatch_worker", Payload: retryFields, Actor: f.dispatcher,
			ApprovedTakeover: approved, SessionWorktree: dispatchSessionWorktree(t, f.s, workID),
			AcceptedInputsDigest: cd0059TestDigest(t, key+"-inputs"), ContractDigest: testManifestDigest, Tool: "concord_work_transition",
			IdempotencyKey: key, RequestID: key, IdempotencyIdentity: key, OperationID: key, PrincipalRef: f.dispatcher.PrincipalRef, Now: time.Now().UTC(),
		})
		return err
	}
	if err := retry("combined-escape-unapproved", nil); !errors.As(err, &refusal) || refusal.Kind != KindApprovalRequired {
		t.Fatalf("unapproved retry dispatch over the other session's undecided review = %v, want approval_required", err)
	}
	if err := retry("combined-escape-approved", &WorkflowStartTakeover{HeldEpoch: epochV, HolderActorRef: laneV}); err != nil {
		t.Fatalf("the approved retry dispatch that completes the escape: %v", err)
	}
}

// TestCrossAttemptRejectRetryCombinedAtomicity pins the combined reject+retry
// route (CD-0198 D5): the rejection and its retry authorization commit in one
// transaction, and a refused retry leaves no rejection.
func TestCrossAttemptRejectRetryCombinedAtomicity(t *testing.T) {
	t.Parallel()
	const workID = "cross-attempt-combined"
	f := seedCrossAttemptFixture(t, workID)
	f.buildTwoAttemptHistory(t)
	epochR := crossAttemptDispatchEpoch(t, f.s, workID, f.attemptR)
	epochV := crossAttemptDispatchEpoch(t, f.s, workID, f.attemptV)
	versionBefore := readWorkVersion(t, f.s, workID)

	// A refused retry leaves no rejection: a retry intent naming a lane
	// outside the closed validLaneID allowlist refuses the whole combined
	// operation inside its transaction, before any registry resolution.
	// resolveLaneByID's registry-missing branch stands beside this gate as the
	// drift detector for an allowlist and generated registry that disagree;
	// the parse refusal is the failure a caller reaches today.
	refusedRequest := f.rejectRequest(t, versionBefore, f.attemptR, epochR, "refused", 50)
	refusedRequest.Payload = mustJSONValue(map[string]any{
		"attempt_id": f.attemptR, "attempt_epoch": epochR,
		"diagnosis":     "the epoch-1 research result does not satisfy the approved outcome",
		"strategy":      "redo the research obligation in a fresh fenced attempt",
		"predicate_ids": []string{"predicate:return-route"}, "evidence_refs": []string{"evidence:research-rejection"},
		"retry": map[string]any{"lane_id": "not-a-lane"},
	})
	if _, err := invokeWorkflowActionForCD0059(context.Background(), t, f.s, refusedRequest); err == nil {
		t.Fatal("the combined rejection with an off-allowlist retry lane unexpectedly succeeded")
	} else {
		var refusal *Failure
		if !errors.As(err, &refusal) || refusal.Kind != KindInvalidPayload || refusal.Detail != "reject_worker_result retry lane_id is not a registered lane identity" {
			t.Fatalf("the combined rejection with an off-allowlist retry lane = %v, want the closed lane-identity parse refusal", err)
		}
	}
	if rejections := f.rejectionCount(t, f.attemptR, epochR); rejections != 0 {
		t.Fatalf("rejection completions after the refused combined call = %d, want none — the refused retry must leave no rejection", rejections)
	}
	if got := readWorkVersion(t, f.s, workID); got != versionBefore {
		t.Fatalf("version after the refused combined call = %d, want %d", got, versionBefore)
	}

	// The combined rejection with a legal retry lane commits the rejection and
	// the retry dispatch AUTHORIZATION in one core transaction; the worker
	// lane evidence is the adapter's post-commit materialization step, not a
	// core write.
	reviewReportBytes := crossAttemptEventPayload(t, f.s, workID, WorkerCompleted, f.attemptV)
	combinedRequest := f.rejectRequest(t, versionBefore, f.attemptR, epochR, "combined", 51)
	combinedRequest.Payload = mustJSONValue(map[string]any{
		"attempt_id": f.attemptR, "attempt_epoch": epochR,
		"diagnosis":     "the epoch-1 research result does not satisfy the approved outcome",
		"strategy":      "redo the research obligation in a fresh fenced attempt",
		"predicate_ids": []string{"predicate:return-route"}, "evidence_refs": []string{"evidence:research-rejection"},
		"retry": map[string]any{"lane_id": f.researchLane.ID},
	})
	combinedResult, err := invokeWorkflowActionForCD0059(context.Background(), t, f.s, combinedRequest)
	if err != nil {
		t.Fatalf("the combined rejection with a retry dispatch: %v", err)
	}
	if rejections := f.rejectionCount(t, f.attemptR, epochR); rejections != 1 {
		t.Fatalf("rejection completions after the combined call = %d, want exactly one", rejections)
	}
	// The combined response names the minted retry attempt identity it
	// authorized; the test derives the identity from that recorded envelope.
	var combinedEnvelope map[string]any
	if err := json.Unmarshal(combinedResult.Result, &combinedEnvelope); err != nil {
		t.Fatalf("the combined result is not one JSON object: %v", err)
	}
	retryAttemptID, ok := combinedEnvelope["retry_attempt_id"].(string)
	if !ok || retryAttemptID == "" {
		t.Fatalf("the combined result carries no retry_attempt_id: %#v", combinedEnvelope)
	}
	// Before materialization the core has committed the dispatch
	// authorization only: the rejection's correction stays open for its
	// retry, and the authorization's epoch is the next one past the review's
	// dispatch.
	if pin := issue1013Pin(t, f.s, workID); pin.Correction == nil || pin.Correction.FailedAttemptID != f.attemptR {
		t.Fatalf("correction pin before the retry materialization = %#v, want the rejection still open for its retry", pin.Correction)
	}
	if epochRetry := crossAttemptDispatchEpoch(t, f.s, workID, retryAttemptID); epochRetry != epochV+1 {
		t.Fatalf("the retry authorization's epoch = %d, want the next epoch %d", epochRetry, epochV+1)
	}
	// The adapter materializes the worker lane evidence afterward through the
	// typed lane route, under the actor that authorized the retry — the
	// combined request's own actor, which the derived retry request copies.
	f.materializeDispatch(t, retryAttemptID, f.researchLane, combinedRequest.Actor)
	// The materialized retry binds the minted identity on the research lane
	// and its dispatch consumes the rejection's correction record.
	retryDispatch := crossAttemptEventPayload(t, f.s, workID, WorkerDispatched, retryAttemptID)
	var retryBinding WorkerDispatchedPayload
	if err := json.Unmarshal(retryDispatch, &retryBinding); err != nil {
		t.Fatal(err)
	}
	if retryBinding.LaneID != f.researchLane.ID || retryBinding.CapabilityClass != f.researchLane.CapabilityClass {
		t.Fatalf("materialized retry binding = %s/%s, want the research lane identity", retryBinding.LaneID, retryBinding.CapabilityClass)
	}
	if pin := issue1013Pin(t, f.s, workID); pin.Correction != nil {
		t.Fatalf("correction pin after the combined rejection's retry = %#v, want the correction consumed", pin.Correction)
	}
	// The review stays untouched beside the combined route.
	if after := crossAttemptEventPayload(t, f.s, workID, WorkerCompleted, f.attemptV); string(after) != string(reviewReportBytes) {
		t.Fatal("the combined rejection rewrote the review report bytes")
	}
	var reviewDisposals int64
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_attempt_dispositions WHERE work_id=? AND action_id IN ('accept_worker_result','accept_worker_evidence','reject_worker_result','record_worker_failure') AND attempt_id=?`, workID, f.attemptV).Scan(&reviewDisposals); err != nil {
		t.Fatal(err)
	}
	if reviewDisposals != 0 {
		t.Fatalf("dispositions naming the review attempt = %d, want none", reviewDisposals)
	}
}

// TestCrossAttemptRejectionIntervalBoundaries pins which recorded events close
// the rejection eligibility interval. The closures the shared fixture cannot
// reach legally are witnessed on an isolated synthetic reader history through
// the staleness suite's established raw-row witness.
func TestCrossAttemptRejectionIntervalBoundaries(t *testing.T) {
	t.Parallel()
	const workID = "cross-attempt-interval"
	f := seedCrossAttemptFixture(t, workID)
	f.buildTwoAttemptHistory(t)
	epochR := crossAttemptDispatchEpoch(t, f.s, workID, f.attemptR)
	epochV := crossAttemptDispatchEpoch(t, f.s, workID, f.attemptV)

	// Baseline: both completed results are rejectable.
	if results := f.rejectableResults(t, 0); !crossAttemptResultsContain(results, f.attemptR, epochR) || !crossAttemptResultsContain(results, f.attemptV, epochV) {
		t.Fatalf("the baseline rejectable results = %#v, want the research result and the review", results)
	}

	// Another same-class completed result must not silently dispose of R.
	attemptW := "attempt:" + workID + ":3"
	f.dispatch(t, attemptW, f.researchLane, f.dispatcher)
	epochW := crossAttemptDispatchEpoch(t, f.s, workID, attemptW)
	f.completeReport(t, attemptW, f.researchLane, 60)
	if results := f.rejectableResults(t, 0); !crossAttemptResultsContain(results, f.attemptR, epochR) || !crossAttemptResultsContain(results, f.attemptV, epochV) || !crossAttemptResultsContain(results, attemptW, epochW) {
		t.Fatalf("rejectable results after the same-class completion = %#v, want all three completed results", results)
	}
	// The exact-pair rejection consumes only its own target.
	if _, err := f.invokeReject(t, readWorkVersion(t, f.s, workID), attemptW, epochW, "third", 61); err != nil {
		t.Fatalf("reject the third completed result: %v", err)
	}
	if results := f.rejectableResults(t, 0); !crossAttemptResultsContain(results, f.attemptR, epochR) || !crossAttemptResultsContain(results, f.attemptV, epochV) || crossAttemptResultsContain(results, attemptW, epochW) {
		t.Fatalf("rejectable results after the third result's rejection = %#v, want R and V still open and W consumed", results)
	}

	// Isolated reader-history witness: the interval closures the shared
	// fixture cannot reach through legal workflow transitions. Every witness
	// row below is labeled and synthetic; none of them fabricates an
	// admission route.
	const witnessWorkID = "cross-attempt-interval-witness"
	w := seedCrossAttemptFixture(t, witnessWorkID)
	w.buildTwoAttemptHistory(t)
	wEpochR := crossAttemptDispatchEpoch(t, w.s, witnessWorkID, w.attemptR)
	wEpochV := crossAttemptDispatchEpoch(t, w.s, witnessWorkID, w.attemptV)
	if results := w.rejectableResults(t, 0); !crossAttemptResultsContain(results, w.attemptR, wEpochR) || !crossAttemptResultsContain(results, w.attemptV, wEpochV) {
		t.Fatalf("the witness history's baseline = %#v, want the research result and the review", results)
	}

	// Local acceptance consumes only its exact result, not sibling eligibility.
	heldAttempt := "attempt:" + witnessWorkID + ":held"
	w.dispatch(t, heldAttempt, w.researchLane, w.dispatcher)
	w.completeReport(t, heldAttempt, w.researchLane, 70)
	heldEpoch := crossAttemptDispatchEpoch(t, w.s, witnessWorkID, heldAttempt)
	if _, err := invokeWorkflowActionForCD0059(t.Context(), t, w.s, w.acceptAttemptRequest(t, readWorkVersion(t, w.s, witnessWorkID), heldAttempt, heldEpoch, "local", 71)); err != nil {
		t.Fatalf("local result acceptance refused: %v", err)
	}
	if step := currentStep(t, w.s, witnessWorkID); step != "investigate" {
		t.Fatalf("the witness held acceptance advanced the step to %q, want investigate held", step)
	}
	if results := w.rejectableResults(t, 0); !crossAttemptResultsContain(results, w.attemptR, wEpochR) || !crossAttemptResultsContain(results, w.attemptV, wEpochV) || crossAttemptResultsContain(results, heldAttempt, heldEpoch) {
		t.Fatalf("rejectable results after local acceptance = %#v, want siblings retained and only the accepted result consumed", results)
	}

	// Witness B: one contract supersession closes the interval.
	stalenessEvent(t, w.s, witnessWorkID, string(WorkflowContractSuperseded), `{"new_contract_version":2}`)
	if _, err := w.s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_contracts SET supersession_seq=(SELECT MAX(seq) FROM domain_events WHERE subject_id=?) WHERE work_id=? AND superseded_by IS NULL; DELETE FROM write_guard`, witnessWorkID, witnessWorkID); err != nil {
		t.Fatal(err)
	}
	if results := w.rejectableResults(t, 0); len(results) != 0 {
		t.Fatalf("rejectable results after the supersession witness = %#v, want the interval closed", results)
	}

	// Witness C: an unprovenanced completed dispatch joins only under the
	// step's latest start, at that start's own epoch.
	const attemptE = "attempt:cross-attempt-interval-witness:9"
	stalenessEvent(t, w.s, witnessWorkID, string(WorkerDispatched), `{"attempt_id":"`+attemptE+`"}`)
	stalenessEvent(t, w.s, witnessWorkID, string(WorkerCompleted), `{"attempt_id":"`+attemptE+`","readback_model":"model/witness"}`)
	if _, err := w.s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
UPDATE worker_attempts SET lane_id='lane:witness',capability_class='research',readback_model='model/witness',lifecycle_state='completed',completed_at='2026-01-02T00:00:01Z',dispatch_payload='{}',
 terminal_seq=(SELECT seq FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.attempt_id')=?),terminal_payload=?
 WHERE work_id=? AND attempt_id=?;
DELETE FROM write_guard`, witnessWorkID, WorkerCompleted, attemptE, string(mustJSONValue(map[string]any{"attempt_id": attemptE, "readback_model": "model/witness"})), witnessWorkID, attemptE); err != nil {
		t.Fatal(err)
	}
	if results := w.rejectableResults(t, 0); !crossAttemptResultsContain(results, attemptE, heldEpoch) {
		t.Fatalf("rejectable results after the unprovenanced witness = %#v, want the witness attempt at the latest start's epoch %d", results, heldEpoch)
	}
	if _, err := validateWorkerResultRejection(context.Background(), w.s.DatabaseForTesting(), witnessWorkID, w.definition, w.step, "test", attemptE, heldEpoch-1, 0); err == nil {
		t.Fatal("the unprovenanced witness attempt claimed a cross-epoch authority")
	} else {
		expectMembershipRefusal(t, err, "the unprovenanced witness attempt at a foreign epoch")
	}

	// Witness D: a non-dispatch action start closes the interval again.
	stalenessEvent(t, w.s, witnessWorkID, string(WorkflowActionStarted), stalenessActionPayload("investigate", "record_finding"))
	if _, err := w.s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_instances SET latest_non_dispatch_start_seq=(SELECT MAX(seq) FROM domain_events WHERE subject_id=?) WHERE work_id=?; DELETE FROM write_guard`, witnessWorkID, witnessWorkID); err != nil {
		t.Fatal(err)
	}
	if results := w.rejectableResults(t, 0); len(results) != 0 {
		t.Fatalf("rejectable results after the non-dispatch start witness = %#v, want the interval closed", results)
	}
}

// TestCrossAttemptRejectionDispatchWindowAuthority pins the window rule: each
// completed attempt stays eligible at its own dispatch-start equality, and a
// later unused start grants no cross-epoch authority.
func TestCrossAttemptRejectionDispatchWindowAuthority(t *testing.T) {
	t.Parallel()
	const workID = "cross-attempt-window"
	f := seedCrossAttemptFixture(t, workID)
	f.buildTwoAttemptHistory(t)
	epochR := crossAttemptDispatchEpoch(t, f.s, workID, f.attemptR)
	epochV := crossAttemptDispatchEpoch(t, f.s, workID, f.attemptV)

	// A later dispatch authorization that no worker evidence ever consumes:
	// the CD-0193 unused-window shape at a non-checkpoint step.
	attemptW3 := "attempt:" + workID + ":3"
	f.authorizeDispatch(t, attemptW3, f.researchLane, f.dispatcher)
	epochW3 := crossAttemptDispatchEpoch(t, f.s, workID, attemptW3)
	if epochW3 != epochV+1 {
		t.Fatalf("the unused authorization's epoch = %d, want the next epoch %d", epochW3, epochV+1)
	}
	// The legal authorization appended its own version; the positive rejection
	// below must present exactly this current version at the boundary's
	// version gate.
	versionAfterAuthorization := readWorkVersion(t, f.s, workID)

	// The unused start closes nothing: both completed results keep their own
	// dispatch-start epochs, and the unmaterialized authorization is not a
	// candidate.
	results := f.rejectableResults(t, 0)
	if !crossAttemptResultsContain(results, f.attemptR, epochR) || !crossAttemptResultsContain(results, f.attemptV, epochV) {
		t.Fatalf("rejectable results after the unused dispatch start = %#v, want both completed results at their own epochs", results)
	}
	if crossAttemptResultsContain(results, attemptW3, epochW3) {
		t.Fatalf("the unmaterialized authorization became a candidate: %#v", results)
	}

	// The later unused start moved the freshness anchor: a positive acceptance
	// of the epoch-1 result refuses on the latest-start mismatch even though
	// the unused window produced no worker evidence at all.
	if _, err := invokeWorkflowActionForCD0059(context.Background(), t, f.s, f.acceptAttemptRequest(t, readWorkVersion(t, f.s, workID), f.attemptR, epochR, "stale-window", 60)); err == nil {
		t.Fatal("accepting the epoch-1 result after the later unused start unexpectedly succeeded")
	} else {
		var freshness *Failure
		if !errors.As(err, &freshness) || freshness.Kind != KindIllegalLifecycleTransition || freshness.Detail != "worker attempt epoch does not match the latest workflow action start" {
			t.Fatalf("the stale acceptance after the unused start = %v, want the latest-start freshness refusal", err)
		}
	}

	// The later start grants no cross-epoch authority: neither completed
	// result can be rejected at the newer epoch, and each still rejects at
	// its OWN dispatch start equality.
	_, err := f.invokeReject(t, readWorkVersion(t, f.s, workID), f.attemptR, epochW3, "cross-epoch-r", 62)
	expectMembershipRefusal(t, err, "the research result at the later window's epoch")
	_, err = f.invokeReject(t, readWorkVersion(t, f.s, workID), f.attemptV, epochW3, "cross-epoch-v", 63)
	expectMembershipRefusal(t, err, "the review at the later window's epoch")
	// The failed stale-accept and cross-epoch probes changed nothing: the
	// version still stands exactly where the authorization left it.
	if got := readWorkVersion(t, f.s, workID); got != versionAfterAuthorization {
		t.Fatalf("version after the refused cross-epoch probes = %d, want %d", got, versionAfterAuthorization)
	}
	if _, err := f.invokeReject(t, versionAfterAuthorization, f.attemptR, epochR, "own-window-r", 64); err != nil {
		t.Fatalf("reject the research result at its own dispatch start equality: %v", err)
	}
	if rejections := f.rejectionCount(t, f.attemptR, epochR); rejections != 1 {
		t.Fatalf("rejection completions naming the research attempt = %d, want exactly one", rejections)
	}

	// The review keeps its own-epoch eligibility after the rejection and the
	// unused start alike.
	if after := f.rejectableResults(t, 0); !crossAttemptResultsContain(after, f.attemptV, epochV) || crossAttemptResultsContain(after, f.attemptR, epochR) {
		t.Fatalf("rejectable results after the own-window rejection = %#v, want the review at its own epoch without the rejected result", after)
	}
	if _, err := f.invokeReject(t, readWorkVersion(t, f.s, workID), f.attemptV, epochV, "own-window-v", 65); err != nil {
		t.Fatalf("reject the review at its own dispatch start equality: %v", err)
	}
	if rejections := f.rejectionCount(t, f.attemptV, epochV); rejections != 1 {
		t.Fatalf("rejection completions naming the review attempt = %d, want exactly one", rejections)
	}
}

// TestCrossAttemptFoldRejectionHoldDirectWitness pins the fold-level negative:
// a direct advancing accept beside an unconsumed rejection refuses at the
// fold. applyWitnessEvent folds the event directly, so the refusal witnessed
// here is the fold's own decision, not the boundary admission's.
func TestCrossAttemptFoldRejectionHoldDirectWitness(t *testing.T) {
	t.Parallel()
	const workID = "cross-attempt-fold-hold"
	f := seedCrossAttemptFixture(t, workID)
	f.buildTwoAttemptHistory(t)
	epochR := crossAttemptDispatchEpoch(t, f.s, workID, f.attemptR)
	epochV := crossAttemptDispatchEpoch(t, f.s, workID, f.attemptV)
	ownerRef := crossAttemptOwnerRef(t, f.fixture.owner)

	if _, err := f.invokeReject(t, readWorkVersion(t, f.s, workID), f.attemptR, epochR, "", 50); err != nil {
		t.Fatalf("reject the epoch-1 research result: %v", err)
	}
	versionHeld := readWorkVersion(t, f.s, workID)
	startsBefore := f.eventCount(t, WorkflowActionStarted, "")
	completionsBefore := f.eventCount(t, WorkflowActionCompleted, "")
	pinHeld := issue1013Pin(t, f.s, workID)
	if pinHeld.Correction == nil || pinHeld.Correction.FailedAttemptID != f.attemptR || pinHeld.Correction.FailedAttemptEpoch != epochR {
		t.Fatalf("correction pin before the fold witness = %#v, want the rejected epoch-1 research attempt standing", pinHeld.Correction)
	}

	// The witness event mirrors the shape the CD-0201 held-accept witness
	// authors, minus review_advance_held and worker_job: the fold derives a
	// positive advance for the completed review and must meet the standing
	// rejection before any attempt validation runs.
	witness := workflowTypedEvent("cross-attempt-fold-hold-accept", WorkflowActionCompleted, workID, ownerRef, time.Unix(53, 0).UTC(), versionHeld, map[string]any{
		"step_id": f.step, "action_id": "accept_worker_result",
		"attempt_epoch": epochV, "worker_attempt_id": f.attemptV, "actor_ref": ownerRef,
	})
	if err := f.applyWitnessEvent(t, witness); err == nil {
		t.Fatal("the fold admitted a direct advancing accept of the completed review beside the unconsumed rejection")
	} else {
		var hold *Failure
		if !errors.As(err, &hold) || hold.Kind != KindIllegalLifecycleTransition {
			t.Fatalf("the fold's direct advancing accept = %v, want %s", err, KindIllegalLifecycleTransition)
		}
		if hold.Detail != "an unconsumed rejected worker result holds the step's advance" {
			t.Fatalf("the fold's direct advancing accept detail = %q, want the unconsumed-rejection advance refusal", hold.Detail)
		}
	}
	if got := readWorkVersion(t, f.s, workID); got != versionHeld {
		t.Fatalf("refused fold witness left version = %d, want %d", got, versionHeld)
	}
	if step := currentStep(t, f.s, workID); step != "investigate" {
		t.Fatalf("refused fold witness left step = %q, want investigate", step)
	}
	if got := f.eventCount(t, WorkflowActionStarted, ""); got != startsBefore {
		t.Fatalf("action starts after the refused fold witness = %d, want %d", got, startsBefore)
	}
	if got := f.eventCount(t, WorkflowActionCompleted, ""); got != completionsBefore {
		t.Fatalf("action completions after the refused fold witness = %d, want %d", got, completionsBefore)
	}
	if got := f.rejectionCount(t, f.attemptV, epochV); got != 0 {
		t.Fatalf("rejection completions naming the review after the refused fold witness = %d, want none", got)
	}
	if pin := issue1013Pin(t, f.s, workID); !reflect.DeepEqual(pin.Correction, pinHeld.Correction) {
		t.Fatalf("correction pin after the refused fold witness = %#v, want the rejected research attempt preserved: %#v", pin.Correction, pinHeld.Correction)
	}
}

// TestCrossAttemptDispatchWindowEpochBounds pins the window reader's bounds:
// the exclusive sequence bound, the completion's own recorded epoch beside
// the window, and the epoch-mismatch refusal on the strict read while the
// common current-prefix read keeps the recorded start epoch. The epoch damage
// is rollback-only test damage, and no s.db-backed helper runs while the
// transaction holds the pool's connection.
func TestCrossAttemptDispatchWindowEpochBounds(t *testing.T) {
	t.Parallel()
	const workID = "cross-attempt-window-epoch"
	ctx := context.Background()
	f := seedCrossAttemptFixture(t, workID)
	f.buildTwoAttemptHistory(t)

	// The transaction compares only values read before BeginTx.
	var completionSeq, completionEpoch int64
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT seq,COALESCE(json_extract(payload,'$.attempt_epoch'),0) FROM worker_authorizations WHERE work_id=? AND attempt_id=?`, workID, f.attemptR).Scan(&completionSeq, &completionEpoch); err != nil {
		t.Fatalf("read the research dispatch authorization completion: %v", err)
	}
	if epochR := crossAttemptDispatchEpoch(t, f.s, workID, f.attemptR); completionEpoch != epochR {
		t.Fatalf("the authorization completion's recorded epoch = %d, want the dispatch epoch %d", completionEpoch, epochR)
	}
	// The same authorization's recorded start epoch, read before the witness
	// transaction so the damaged-prefix comparisons stay off s.db inside it:
	// the common current-prefix read must retain exactly this start-derived
	// epoch even when the completion's own epoch is damaged.
	var startStep string
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.step_id') FROM worker_authorizations WHERE work_id=? AND attempt_id=?`, workID, f.attemptR).Scan(&startStep); err != nil {
		t.Fatalf("read the research authorization's step: %v", err)
	}
	var startEpoch int64
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT COALESCE(json_extract(start_payload,'$.attempt_epoch'),0) FROM worker_authorizations WHERE work_id=? AND attempt_id=? AND json_extract(start_payload,'$.action_id')='dispatch_worker' AND json_extract(start_payload,'$.step_id')=? AND start_seq>0 AND start_seq<?`, workID, f.attemptR, startStep, completionSeq).Scan(&startEpoch); err != nil {
		t.Fatalf("read the research authorization's start epoch: %v", err)
	}
	if startEpoch != completionEpoch {
		t.Fatalf("the authorization start epoch = %d, want the undamaged completion epoch %d", startEpoch, completionEpoch)
	}
	completionPayload := func() []byte {
		t.Helper()
		var payload []byte
		if err := f.s.DatabaseForTesting().QueryRow(`SELECT payload FROM worker_authorizations WHERE work_id=? AND attempt_id=?`, workID, f.attemptR).Scan(&payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}
	bytesBefore := completionPayload()

	// At the completion's own sequence the exclusive bound excludes it: the
	// prefix names no window for the attempt.
	if _, err := findAuthorizedDispatchWindowBefore(ctx, f.s.DatabaseForTesting(), workID, f.attemptR, completionSeq); err == nil {
		t.Fatal("the window read at its own completion sequence unexpectedly admitted the window")
	} else {
		var missing *Failure
		if !errors.As(err, &missing) || missing.Kind != KindUnauthorizedDispatch || missing.Detail != "no authorized dispatch window exists for this work item bound to this attempt" {
			t.Fatalf("the window read at the completion sequence = %v, want %s with the exact missing-window refusal", err, KindUnauthorizedDispatch)
		}
	}

	// One past the completion admits the attempt's own window at its own
	// recorded epoch, and the current prefix reads the identical window.
	bounded, err := findAuthorizedDispatchWindowBefore(ctx, f.s.DatabaseForTesting(), workID, f.attemptR, completionSeq+1)
	if err != nil {
		t.Fatalf("the window read one past the completion: %v", err)
	}
	if bounded.AttemptID != f.attemptR || bounded.AttemptEpoch != completionEpoch || bounded.StartSeq <= 0 || bounded.StartSeq >= completionSeq {
		t.Fatalf("bounded window = %#v, want the research attempt's own window at its recorded epoch, anchored below the completion", bounded)
	}
	current, err := findAuthorizedDispatchWindowBefore(ctx, f.s.DatabaseForTesting(), workID, f.attemptR, 0)
	if err != nil {
		t.Fatalf("the current window read: %v", err)
	}
	if !reflect.DeepEqual(current, bounded) {
		t.Fatalf("current window = %#v, want the bounded window %#v", current, bounded)
	}

	// Rollback-only test damage: move the completion's recorded epoch away
	// from the start it authorized, inside one transaction.
	tx, err := f.s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	damage, err := tx.ExecContext(ctx, `UPDATE worker_authorizations SET payload=json_set(payload,'$.attempt_epoch',?) WHERE work_id=? AND attempt_id=?`, completionEpoch+1, workID, f.attemptR)
	if err != nil {
		t.Fatal(err)
	}
	if moved, err := damage.RowsAffected(); err != nil || moved != 1 {
		t.Fatalf("the epoch damage touched %d rows (%v), want exactly the research authorization completion", moved, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := findAuthorizedDispatchWindowBefore(ctx, tx, workID, f.attemptR, completionSeq+1); err == nil {
		t.Fatal("the damaged window read silently adopted the start epoch and returned a window")
	} else {
		var broken *Failure
		if !errors.As(err, &broken) || broken.Kind != KindInvariantViolation {
			t.Fatalf("the damaged window read = %v, want %s", err, KindInvariantViolation)
		}
		if broken.Detail != "dispatch_worker completion attempt_epoch does not match the starting authorization epoch" {
			t.Fatalf("the damaged window read detail = %q, want the exact epoch-mismatch refusal", broken.Detail)
		}
	}
	// The same damaged prefix through the common current-prefix read: it
	// keeps the recorded start epoch and acquires no epoch-mismatch refusal,
	// so the common acceptance/failure readers' behavior survives the strict
	// disposition bound.
	coreWindow, coreErr := findAuthorizedDispatchWindowCore(ctx, tx, workID, f.attemptR)
	if coreErr != nil {
		t.Fatalf("the common current-prefix read refused the damaged prefix: %v", coreErr)
	}
	if coreWindow.AttemptEpoch != startEpoch {
		t.Fatalf("the common current-prefix read epoch = %d, want the recorded start epoch %d", coreWindow.AttemptEpoch, startEpoch)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	// The rolled-back witness left the authorization intact: identical
	// completion bytes and the identical window.
	if after := completionPayload(); string(after) != string(bytesBefore) {
		t.Fatal("the rolled-back epoch witness left the authorization completion bytes rewritten")
	}
	windowAfter, err := findAuthorizedDispatchWindowBefore(ctx, f.s.DatabaseForTesting(), workID, f.attemptR, completionSeq+1)
	if err != nil {
		t.Fatalf("the window read after the rollback: %v", err)
	}
	if !reflect.DeepEqual(windowAfter, bounded) {
		t.Fatalf("window after the rollback = %#v, want the original window %#v", windowAfter, bounded)
	}
}

// TestCrossAttemptAdvanceTruthTable pins the pure advance decision the fold
// reads over the pinned definition's own modes, and the unconsumed-rejection
// gate's refusal of the advancing accept shape alone.
func TestCrossAttemptAdvanceTruthTable(t *testing.T) {
	t.Parallel()
	f := seedCrossAttemptFixture(t, "cross-attempt-advance-truth-table")

	// The modes the table reads, derived from the definition the fixture
	// pinned rather than restated from another copy.
	for actionID, want := range map[string]ActionExecutionMode{
		"accept_worker_result":   ActionAdvance,
		"accept_worker_evidence": ActionHold,
		"reject_worker_result":   ActionHold,
	} {
		if mode, ok := workflowActionExecutionMode(f.definition, actionID); !ok || mode != want {
			t.Fatalf("workflow.research v19 mode of %s = %q (declared %t), want %q", actionID, mode, ok, want)
		}
	}

	job := &WorkerJobBinding{JobID: "job:advance-truth-table", Revision: 1, Digest: "sha256:" + strings.Repeat("d", 64)}
	rows := []struct {
		name    string
		payload workflowActionCompletedPayload
		want    bool
	}{
		{name: "an ordinary accept advances", payload: workflowActionCompletedPayload{ActionID: "accept_worker_result", StepID: f.step, AttemptEpoch: 1}, want: true},
		{name: "a recorded review hold keeps the step", payload: workflowActionCompletedPayload{ActionID: "accept_worker_result", StepID: f.step, AttemptEpoch: 1, ReviewAdvanceHeld: true}, want: false},
		{name: "a job-bound acceptance without a delivery assertion holds", payload: workflowActionCompletedPayload{ActionID: "accept_worker_result", StepID: f.step, AttemptEpoch: 1, WorkerJob: job}, want: false},
		{name: "the same binding with a delivery assertion advances", payload: workflowActionCompletedPayload{ActionID: "accept_worker_result", StepID: f.step, AttemptEpoch: 1, WorkerJob: job, DeliveryState: "delivered"}, want: true},
		{name: "a rejection never advances", payload: workflowActionCompletedPayload{ActionID: "reject_worker_result", StepID: f.step, AttemptEpoch: 1}, want: false},
		{name: "an evidence acceptance never advances", payload: workflowActionCompletedPayload{ActionID: "accept_worker_evidence", StepID: f.step, AttemptEpoch: 1}, want: false},
	}
	for _, row := range rows {
		if got := workflowCompletedActionAdvances(f.definition, row.payload); got != row.want {
			t.Fatalf("%s: workflowCompletedActionAdvances = %t, want %t", row.name, got, row.want)
		}
	}

	// With a hold standing, the gate refuses only the advancing accept shape.
	if failure := workflowRejectionAdvanceFailure(true, "accept_worker_result", true); failure == nil || failure.Kind != KindIllegalLifecycleTransition || failure.Detail != "an unconsumed rejected worker result holds the step's advance" {
		t.Fatalf("the unconsumed-rejection gate over an advancing accept = %#v, want the exact advance-hold refusal", failure)
	}
	for _, row := range rows {
		if row.want {
			continue
		}
		if failure := workflowRejectionAdvanceFailure(true, row.payload.ActionID, false); failure != nil {
			t.Fatalf("%s: the unconsumed-rejection gate refused a non-advancing shape: %#v", row.name, failure)
		}
	}
	// Even a hypothetical advancing evidence or rejection shape is not the
	// accept the gate names.
	for _, actionID := range []string{"accept_worker_evidence", "reject_worker_result"} {
		if failure := workflowRejectionAdvanceFailure(true, actionID, true); failure != nil {
			t.Fatalf("the unconsumed-rejection gate refused %s at an advancing shape: %#v", actionID, failure)
		}
	}
	if failure := workflowRejectionAdvanceFailure(false, "accept_worker_result", true); failure != nil {
		t.Fatalf("the unconsumed-rejection gate refused an advancing accept without a hold: %#v", failure)
	}
}

// authorJobBoundRejection inserts the two synthetic job-bound rows one
// reader-history witness needs. The rows live only inside the caller's
// rollback transaction; no production or admission route authors them.
func authorJobBoundRejection(ctx context.Context, t *testing.T, tx *sql.Tx, workID, attemptID, step string, epoch, at int64) {
	t.Helper()
	dispatchPayload := mustJSONValue(map[string]any{
		"work_id": workID, "step_id": step, "action_id": "dispatch_worker",
		"attempt_epoch": epoch, "worker_attempt_id": attemptID,
		"worker_job": map[string]any{"job_id": "job:cross-attempt-unbound-witness", "revision": 1, "digest": "sha256:" + strings.Repeat("w", 64)},
	})
	if _, err := tx.ExecContext(ctx, `INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?)`,
		workID+":witness-dispatch-job", string(WorkflowActionCompleted), string(SubjectWorkItem), workID, "worker:witness", time.Unix(at, 0).UTC().Format(time.RFC3339Nano), CurrentEventPayloadVersion(WorkflowActionCompleted), dispatchPayload); err != nil {
		t.Fatalf("insert the witness dispatch binding: %v", err)
	}
	rejectionPayload := mustJSONValue(map[string]any{
		"work_id": workID, "step_id": step, "action_id": "reject_worker_result",
		"attempt_epoch": epoch, "worker_attempt_id": attemptID,
	})
	if _, err := tx.ExecContext(ctx, `INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?)`,
		workID+":witness-reject-job", string(WorkflowActionCompleted), string(SubjectWorkItem), workID, "worker:witness", time.Unix(at, 0).UTC().Format(time.RFC3339Nano), CurrentEventPayloadVersion(WorkflowActionCompleted), rejectionPayload); err != nil {
		t.Fatalf("insert the witness job-bound rejection: %v", err)
	}
}

// TestCrossAttemptUnboundRejectionIgnoresJobBinding pins the reader ownership
// inside workflowUnconsumedUnboundRejection: a job-bound rejection creates no
// unbound advance hold and never hides an earlier open one. The job-bound
// rows are synthetic witnesses inside rollback transactions.
func TestCrossAttemptUnboundRejectionIgnoresJobBinding(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Part one: a job-bound rejection standing alone creates no unbound
	// advance hold.
	const witnessWorkID = "cross-attempt-unbound-witness"
	w := seedCrossAttemptFixture(t, witnessWorkID)
	w.buildTwoAttemptHistory(t)
	before, err := workflowUnconsumedUnboundRejection(ctx, w.s.DatabaseForTesting(), witnessWorkID, w.step, "test", 0)
	if err != nil {
		t.Fatal(err)
	}
	if before {
		t.Fatal("the untouched two-attempt history already reported an open unbound rejection")
	}
	tx, err := w.s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	authorJobBoundRejection(ctx, t, tx, witnessWorkID, "attempt:"+witnessWorkID+":9", w.step, 2, 90)
	held, err := workflowUnconsumedUnboundRejection(ctx, tx, witnessWorkID, w.step, "test", 0)
	if err != nil {
		t.Fatalf("the unbound reader over the job-bound witness history: %v", err)
	}
	if held {
		t.Fatal("the job-bound rejection created an unbound advance hold; the reader must ignore its recorded job binding")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if after, err := workflowUnconsumedUnboundRejection(ctx, w.s.DatabaseForTesting(), witnessWorkID, w.step, "test", 0); err != nil || after {
		t.Fatalf("the rolled-back witness left an open unbound rejection = %t (%v), want none", after, err)
	}

	// Part two: the job-bound rejection never hides an earlier open unbound
	// rejection. R's real rejection stands open — the fact the fold's advance
	// hold reads — and adding the job-bound witness rows must not consume or
	// conceal it.
	const workID = "cross-attempt-unbound-hides"
	f := seedCrossAttemptFixture(t, workID)
	f.buildTwoAttemptHistory(t)
	epochR := crossAttemptDispatchEpoch(t, f.s, workID, f.attemptR)
	if _, err := f.invokeReject(t, readWorkVersion(t, f.s, workID), f.attemptR, epochR, "", 50); err != nil {
		t.Fatalf("reject the epoch-1 research result: %v", err)
	}
	open, err := workflowUnconsumedUnboundRejection(ctx, f.s.DatabaseForTesting(), workID, f.step, "test", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !open {
		t.Fatal("the unbound rejection of the research result did not stand open; the fold's advance hold presumes it")
	}
	bound, err := f.s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer bound.Rollback()
	authorJobBoundRejection(ctx, t, bound, workID, "attempt:"+workID+":9", f.step, 3, 91)
	stillOpen, err := workflowUnconsumedUnboundRejection(ctx, bound, workID, f.step, "test", 0)
	if err != nil {
		t.Fatalf("the unbound reader beside the job-bound witness rejection: %v", err)
	}
	if !stillOpen {
		t.Fatal("the job-bound rejection hid the earlier open unbound rejection")
	}
	if err := bound.Rollback(); err != nil {
		t.Fatal(err)
	}
	if after, err := workflowUnconsumedUnboundRejection(ctx, f.s.DatabaseForTesting(), workID, f.step, "test", 0); err != nil || !after {
		t.Fatalf("after the rollback the unbound rejection stands open = %t (%v), want open", after, err)
	}
}

// TestCrossAttemptRejectionIntervalClosesOnAdvanceAndReturn witnesses the two
// closure boundaries the interval test leaves unexercised. Each row is a
// labeled synthetic reader-history witness on its own fixture.
func TestCrossAttemptRejectionIntervalClosesOnAdvanceAndReturn(t *testing.T) {
	t.Parallel()
	job := &WorkerJobBinding{JobID: "job:cross-attempt-close-witness", Revision: 1, Digest: "sha256:" + strings.Repeat("d", 64)}
	for _, row := range []struct {
		name, actionID string
		job            *WorkerJobBinding
		closes         bool
	}{
		{name: "advance", actionID: "accept_worker_result", closes: true},
		{name: "return", actionID: "request_correction", closes: true},
		{name: "local-job", actionID: "accept_worker_result", job: job, closes: false},
	} {
		w := seedCrossAttemptFixture(t, "cross-attempt-close-"+row.name)
		w.buildTwoAttemptHistory(t)
		epochR := crossAttemptDispatchEpoch(t, w.s, w.workID, w.attemptR)
		payload := map[string]any{"step_id": w.step, "action_id": row.actionID, "worker_attempt_id": w.attemptV}
		if row.job != nil {
			payload["worker_job"] = row.job
		}
		stalenessEvent(t, w.s, w.workID, string(WorkflowActionCompleted), string(mustJSONValue(payload)))
		var seq int64
		var eventID string
		if err := w.s.DatabaseForTesting().QueryRow(`SELECT seq,event_id FROM domain_events WHERE subject_id=? ORDER BY seq DESC LIMIT 1`, w.workID).Scan(&seq, &eventID); err != nil {
			t.Fatal(err)
		}
		tx, err := w.s.DatabaseForTesting().BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
			t.Fatal(err)
		}
		if row.closes {
			if _, err := tx.Exec(`INSERT INTO workflow_phase_exits(work_id,action_id,exit_seq) VALUES(?,?,?) ON CONFLICT(work_id,action_id) DO UPDATE SET exit_seq=excluded.exit_seq`, w.workID, row.actionID, seq); err != nil {
				t.Fatal(err)
			}
		}
		if row.actionID == "accept_worker_result" {
			if _, err := tx.Exec(`INSERT INTO worker_attempt_dispositions(work_id,attempt_id,action_id,seq,event_id,payload) VALUES(?,?,?,?,?,?) ON CONFLICT(work_id,attempt_id,action_id) DO UPDATE SET seq=excluded.seq,event_id=excluded.event_id,payload=excluded.payload`, w.workID, w.attemptV, row.actionID, seq, eventID, string(mustJSONValue(payload))); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := tx.Exec(`DELETE FROM write_guard`); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		results := w.rejectableResults(t, 0)
		if row.closes && len(results) != 0 {
			t.Fatalf("%s: rejectable results = %#v, want the interval closed", row.name, results)
		}
		if !row.closes && (len(results) != 1 || !crossAttemptResultsContain(results, w.attemptR, epochR)) {
			t.Fatalf("%s: rejectable results = %#v, want the research result open and the accepted review consumed", row.name, results)
		}
	}
}

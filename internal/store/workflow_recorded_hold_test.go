package store

// Recorded dispatch-hold regressions over the same synthetic-journal harness
// the recorded-admission tests use. The candidate shape is a break_fix v15
// instance at its human_checkpoint verify step: one worker attempt actually
// dispatched there and never dispositioned, a second authorization window
// started after that dispatch, and a confirm_premise completion naming the
// newer window's epoch. That shape satisfies the released hold predicate —
// the only actual dispatch precedes the latest matching-step start, and no
// actual dispatch falls between the start and the completion — so the
// recorded hold admission must fold it, and every incomplete or foreign
// shape must keep the hold refusal for its own clause. Dispatch scope is the
// whole work item, never one step. Every negative names the guard it holds,
// repeats deterministically, and leaves no workflow projection committed.
// The context checkpoint/boundary pair pins the unchanged boundary guards:
// a pair agreeing on version, step, and epoch folds, and a stale one
// refuses. Every identity is invented.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// recordedHoldDetail is the hold guard the recorded admission may overturn
// alone, and the refusal every negative must preserve.
const recordedHoldDetail = "a dispatched worker attempt must advance through accept_worker_result"

// recordedHoldVariant selects the journal shape one test exercises.
type recordedHoldVariant struct {
	family          string
	version         int64
	digest          string
	targetAction    string // the completion action under the hold gate
	targetStepID    string // the completion payload's step_id
	targetEpoch     int64  // the completion payload's attempt_epoch
	targetSourceVer int    // the completion's stored payload version
	olderWindow     bool   // epoch-1 verify dispatch window (start + authorization)
	olderActual     bool   // a real worker.dispatched event for the older attempt
	newerWindow     bool   // epoch-2 verify start + authorization, never dispatched
	withStart       bool   // window one carries its own action_started event
	suffixTail      bool   // a later start, dispatch, and disposition recorded after the target completion
}

func recordedHoldPositiveVariant() recordedHoldVariant {
	return recordedHoldVariant{
		family: "break_fix", version: 15, digest: recordedPinBreakFixV15,
		targetAction: "confirm_premise", targetStepID: "verify", targetEpoch: 2, targetSourceVer: 2,
		olderWindow: true, olderActual: true, newerWindow: true, withStart: true,
	}
}

type recordedHoldFixture struct {
	work          *recordedAdmissionWork
	laneVersion   int64
	laneDigest    string
	attempt1      string
	attempt2      string
	attempt3      string
	owner         string
	newerStartSeq int64 // journal seq of the epoch-2 verify start, 0 when absent
}

// recordedHoldBuildJournal walks the pinned break_fix graph to verify with
// the CD-0187 checkpoint worker actions, records the dispatch history the
// hold reads, and appends the target completion as the journal's last event
// unless a suffix tail follows it.
func recordedHoldBuildJournal(t *testing.T, workID string, variant recordedHoldVariant) *recordedHoldFixture {
	t.Helper()
	w := recordedAdmissionStartWork(t, workID)
	executor := w.recordActor(t, "agent", recordedAdmissionExecutorAgent)
	owner := w.recordActor(t, "agent", recordedAdmissionOwnerAgent)
	lane := w.recordActor(t, "agent", recordedAdmissionLaneAgent)
	w.selectDefinition(t, variant.family, "workflow."+variant.family, variant.version, variant.digest)
	w.approveContract(t, true)
	for _, step := range recordedAdmissionWalkWithAlignment(recordedAdmissionBreakFixWalk, variant.family, variant.version) {
		if step.actionID == "record_alignment" {
			w.journal.append("workflow.backlog_alignment_recorded", "work_item", workID, executor, 1, map[string]any{
				"expected_version": w.version, "outcome": "none_found",
				"resulting_version": w.version + 1,
				"searched":          "synthetic recorded-hold backlog alignment search", "work_id": workID,
			})
			w.version++
		}
		w.completeAction(t, step.actionID, step.stepID, executor, 1, nil)
	}
	for _, step := range []string{"repair", "refine", "delivery"} {
		w.completeAction(t, "record_delivery", step, executor, 1, nil)
	}
	laneVersion, laneDigest := registeredLaneIdentity(t, "research")
	fixture := &recordedHoldFixture{
		work: w, laneVersion: laneVersion, laneDigest: laneDigest,
		attempt1: "attempt/" + workID + "-older", attempt2: "attempt/" + workID + "-newer",
		attempt3: "attempt/" + workID + "-suffix",
		owner:    owner,
	}
	if variant.olderWindow && variant.withStart {
		w.startAction(t, "dispatch_worker", "verify", owner, 1, "synthetic:"+workID+":verify:1")
	}
	if variant.olderWindow {
		w.dispatchCompletion(t, "verify", owner, 1, fixture.attempt1, laneVersion, laneDigest)
	}
	if variant.olderActual {
		w.workerDispatched(t, fixture.attempt1, lane, laneVersion, laneDigest)
	}
	if variant.newerWindow {
		fixture.newerStartSeq = int64(len(w.journal.events) + 1)
		w.startAction(t, "dispatch_worker", "verify", owner, 2, "synthetic:"+workID+":verify:2")
		w.dispatchCompletion(t, "verify", owner, 2, fixture.attempt2, laneVersion, laneDigest)
	}
	w.completeAction(t, variant.targetAction, variant.targetStepID, owner, variant.targetSourceVer, map[string]any{
		"attempt_epoch": variant.targetEpoch,
	})
	if variant.suffixTail {
		// Every suffix kind the prefix bound must ignore: an actual dispatch
		// of the newer window's attempt, a later same-step start with its own
		// dispatched attempt, and that attempt's failure disposition.
		w.workerDispatched(t, fixture.attempt2, lane, laneVersion, laneDigest)
		w.startAction(t, "dispatch_worker", "verify", owner, 3, "synthetic:"+workID+":verify:3")
		w.dispatchCompletion(t, "verify", owner, 3, fixture.attempt3, laneVersion, laneDigest)
		w.workerDispatched(t, fixture.attempt3, lane, laneVersion, laneDigest)
		w.workerFailed(t, fixture.attempt3)
		w.completeAction(t, "record_worker_failure", "verify", owner, 4, map[string]any{
			"attempt_epoch": int64(3), "worker_attempt_id": fixture.attempt3,
		})
	}
	return fixture
}

// recordedHoldProbeAdmission restores the journal without its final event,
// rebuilds the prefix, and judges the withheld completion with the recorded
// hold helper alone, under the private replay context and a real transaction
// over the rebuilt state. The withheld completion is by construction a
// source payload version 2 recorded shape; the probe stamps that marker the
// way the rebuild would. seqOverride judges a modified event sequence;
// zero keeps the recorded one. The deferred rollback discards the probe.
func recordedHoldProbeAdmission(t *testing.T, work *recordedAdmissionWork, seqOverride int64) (bool, error) {
	t.Helper()
	prefix := &recordedAdmissionJournal{workID: work.workID, events: work.journal.events[:len(work.journal.events)-1]}
	s := restoreRecordedAdmissionJournal(t, prefix, nil)
	defer s.Close()
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("the probe prefix must fold: %v", err)
	}
	target := work.journal.events[len(work.journal.events)-1]
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	entry, err := VerifyWorkflowInstanceDefinitionTx(context.Background(), tx, BuiltinWorkflowRegistry(), work.workID)
	if err != nil {
		t.Fatalf("the probe prefix must carry the registered archived pin: %v", err)
	}
	var currentStep string
	if err := tx.QueryRowContext(context.Background(), `SELECT current_step FROM workflow_instances WHERE work_id=?`, work.workID).Scan(&currentStep); err != nil {
		t.Fatal(err)
	}
	var p workflowActionCompletedPayload
	if err := json.Unmarshal(target.payload, &p); err != nil {
		t.Fatal(err)
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, target.occurredAt)
	if err != nil {
		t.Fatal(err)
	}
	seq := target.seq
	if seqOverride != 0 {
		seq = seqOverride
	}
	event := Event{
		Seq: seq, EventID: target.eventID, Kind: target.kind, SubjectType: SubjectType(target.subjectType),
		SubjectID: target.subjectID, Actor: target.actor, OccurredAt: occurredAt,
		PayloadVersion: target.payloadVersion, Payload: json.RawMessage(target.payload),
	}
	event.replaySourcePayloadVersion = 2
	return workflowRecordedHoldAdmission(workflowReplayContext(context.Background()), tx, event, p, entry.Definition, currentStep)
}

// The full-fold positive: the candidate shape folds through RebuildFromLog
// with the hold decision alone overturned, and the resulting step, the exact
// work version, and every log byte survive.
func TestRecordedWorkflowHoldFullFoldPositiveAdmitsRecordedConfirmPremise(t *testing.T) {
	fixture := recordedHoldBuildJournal(t, "recorded-hold-positive", recordedHoldPositiveVariant())
	s := recordedAdmissionRequireFold(t, fixture.work, nil)
	defer s.Close()
	if state, step, _ := recordedAdmissionInstanceState(t, s, fixture.work.workID); state != "running" || step != "complete" {
		t.Fatalf("replayed instance = %q/%q, want running/complete", state, step)
	}
	if version := readWorkVersion(t, s, fixture.work.workID); version != fixture.work.version {
		t.Fatalf("replayed work version = %d, want the journal's exact %d", version, fixture.work.version)
	}
	recordedAdmissionRebuildStable(t, s, fixture.work.journal)
	recordedAdmissionBackupRoundtrip(t, s, fixture.work.journal)
}

// Future-suffix independence: a later actual dispatch, a later same-step
// start with its own dispatched attempt, and a later failure disposition all
// sit outside the completion's prefix and cannot change its decision. The
// completion folds first, the suffix folds after it, and the later start
// leaves the instance at the step it reopened.
func TestRecordedWorkflowHoldFutureSuffixCannotChangeThePrefixDecision(t *testing.T) {
	fixture := recordedHoldBuildJournal(t, "recorded-hold-suffix", func() recordedHoldVariant {
		variant := recordedHoldPositiveVariant()
		variant.suffixTail = true
		return variant
	}())
	s := recordedAdmissionRequireFold(t, fixture.work, nil)
	defer s.Close()
	if state, step, _ := recordedAdmissionInstanceState(t, s, fixture.work.workID); state != "running" || step != "verify" {
		t.Fatalf("replayed instance = %q/%q, want running/verify", state, step)
	}
	if version := readWorkVersion(t, s, fixture.work.workID); version != fixture.work.version {
		t.Fatalf("replayed work version = %d, want the journal's exact %d", version, fixture.work.version)
	}
	recordedAdmissionRebuildStable(t, s, fixture.work.journal)
}

// A negative: the live (non-replay) append of the same completion keeps the
// hold refusal; the recorded alternative is replay-only.
func TestRecordedWorkflowHoldLiveAppendKeepsHoldRefusal(t *testing.T) {
	fixture := recordedHoldBuildJournal(t, "recorded-hold-live", recordedHoldPositiveVariant())
	prefix := &recordedAdmissionJournal{workID: fixture.work.workID, events: fixture.work.journal.events[:len(fixture.work.journal.events)-1]}
	s := restoreRecordedAdmissionJournal(t, prefix, nil)
	defer s.Close()
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("the live-append prefix must fold: %v", err)
	}
	version := fixture.work.version - 1
	event := workflowEventWithActor(fixture.work.workID+"-live-confirm", WorkflowActionCompleted, fixture.work.workID, fixture.owner, map[string]any{
		"action_id": "confirm_premise", "actor_ref": fixture.owner, "attempt_epoch": int64(2),
		"changed_refs": []string{}, "expected_version": version, "result_evidence_refs": []string{},
		"resulting_version": version + 1, "step_id": "verify", "work_id": fixture.work.workID,
	})
	event.PayloadVersion = 2
	err := applyWorkflowTestOperationDirect(context.Background(), s, Operation{
		Events:           []Event{event},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, fixture.work.workID): version},
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindIllegalLifecycleTransition || !strings.Contains(failure.Detail, recordedHoldDetail) {
		t.Fatalf("live confirm_premise refusal = %v, want the unchanged dispatch hold", err)
	}
}

// A negative: a completion stored at source payload version 1 is outside the
// recorded hold's closed source window and keeps the hold refusal.
func TestRecordedWorkflowHoldSourceV1KeepsHoldRefusal(t *testing.T) {
	variant := recordedHoldPositiveVariant()
	variant.targetSourceVer = 1
	fixture := recordedHoldBuildJournal(t, "recorded-hold-source1", variant)
	recordedAdmissionRequireRefusal(t, fixture.work, nil, KindIllegalLifecycleTransition, recordedHoldDetail)
}

// A negative: a completion stored at the current payload version is not a
// recorded historical shape and keeps the hold refusal.
func TestRecordedWorkflowHoldCurrentSourceV5KeepsHoldRefusal(t *testing.T) {
	variant := recordedHoldPositiveVariant()
	variant.targetSourceVer = 5
	fixture := recordedHoldBuildJournal(t, "recorded-hold-source5", variant)
	recordedAdmissionRequireRefusal(t, fixture.work, nil, KindIllegalLifecycleTransition, recordedHoldDetail)
}

// A negative: a registered adjacent pin outside the closed one-pin
// population keeps the hold refusal even though the registry verifies the
// stored tuple.
func TestRecordedWorkflowHoldAdjacentPinKeepsHoldRefusal(t *testing.T) {
	variant := recordedHoldPositiveVariant()
	variant.version = 18
	variant.digest = recordedPinBreakFixV18
	fixture := recordedHoldBuildJournal(t, "recorded-hold-pin", variant)
	recordedAdmissionRequireRefusal(t, fixture.work, nil, KindIllegalLifecycleTransition, recordedHoldDetail)
}

// A negative: the stored pin digest must match the registry exactly. The
// prefix rebuild leaves a canonical instance row, the stored digest is then
// corrupted under the fold's own transaction guard, and the recorded
// completion is folded directly from its row; a refusal can come only from
// the actual stored mismatch. The deferred rollback discards the probe
// transaction.
func TestRecordedWorkflowHoldStoredDigestMismatchKeepsHoldRefusal(t *testing.T) {
	fixture := recordedHoldBuildJournal(t, "recorded-hold-digest", recordedHoldPositiveVariant())
	recordedAdmissionRequireStoredDigestRefusal(t, fixture.work, nil, recordedAdmissionWrongDigest, KindIllegalLifecycleTransition, recordedHoldDetail)
}

// A negative: record_delivery never uses the recorded hold even with the
// allowed pin and source; the action is outside the closed map and the hold
// refusal stands.
func TestRecordedWorkflowHoldForeignActionRecordDeliveryKeepsHoldRefusal(t *testing.T) {
	variant := recordedHoldPositiveVariant()
	variant.targetAction = "record_delivery"
	fixture := recordedHoldBuildJournal(t, "recorded-hold-action", variant)
	recordedAdmissionRequireRefusal(t, fixture.work, nil, KindIllegalLifecycleTransition, recordedHoldDetail)
}

// A negative: a completion whose payload names a step other than the pinned
// projected step is outside the recorded hold and keeps the hold refusal.
func TestRecordedWorkflowHoldForeignSourceStepKeepsHoldRefusal(t *testing.T) {
	variant := recordedHoldPositiveVariant()
	variant.targetStepID = "delivery"
	fixture := recordedHoldBuildJournal(t, "recorded-hold-step", variant)
	recordedAdmissionRequireRefusal(t, fixture.work, nil, KindIllegalLifecycleTransition, recordedHoldDetail)
}

// A negative: a completion naming a stale epoch behind the latest verify
// start is outside the recorded hold and keeps the hold refusal.
func TestRecordedWorkflowHoldStaleEpochKeepsHoldRefusal(t *testing.T) {
	variant := recordedHoldPositiveVariant()
	variant.targetEpoch = 1
	fixture := recordedHoldBuildJournal(t, "recorded-hold-epoch", variant)
	recordedAdmissionRequireRefusal(t, fixture.work, nil, KindIllegalLifecycleTransition, recordedHoldDetail)
}

// A negative: the dispatched attempt holds the step with no verify start in
// the log at all; the recorded hold's missing-start clause keeps the
// refusal.
func TestRecordedWorkflowHoldWithoutStepStartKeepsHoldRefusal(t *testing.T) {
	variant := recordedHoldPositiveVariant()
	variant.olderWindow = true
	variant.withStart = false
	variant.newerWindow = false
	variant.targetEpoch = 1
	fixture := recordedHoldBuildJournal(t, "recorded-hold-nostart", variant)
	recordedAdmissionRequireRefusal(t, fixture.work, nil, KindIllegalLifecycleTransition, recordedHoldDetail)
}

// A negative: the only actual dispatch follows the latest matching-step
// start, so the released hold predicate holds the step itself and no
// recorded proof may overturn the refusal.
func TestRecordedWorkflowHoldDispatchAfterLatestStartKeepsHoldRefusal(t *testing.T) {
	variant := recordedHoldPositiveVariant()
	variant.newerWindow = false
	variant.targetEpoch = 1
	fixture := recordedHoldBuildJournal(t, "recorded-hold-after-start", variant)
	recordedAdmissionRequireRefusal(t, fixture.work, nil, KindIllegalLifecycleTransition, recordedHoldDetail)
}

// A direct negative of the recorded hold helper: with no actual dispatch
// anywhere before the latest verify start, the older-dispatch clause is the
// only missing proof. A full fold never reaches the held guard on this
// shape, so the helper alone proves the clause.
func TestRecordedWorkflowHoldHelperWithoutOlderActualDispatchRefuses(t *testing.T) {
	variant := recordedHoldPositiveVariant()
	variant.olderActual = false
	fixture := recordedHoldBuildJournal(t, "recorded-hold-noolder", variant)
	admitted, err := recordedHoldProbeAdmission(t, fixture.work, 0)
	if err != nil {
		t.Fatalf("the recorded hold helper must refuse the missing older dispatch without erroring: %v", err)
	}
	if admitted {
		t.Fatal("the recorded hold helper admitted a prefix with no older actual dispatch")
	}
}

// A direct negative of the recorded hold helper: a nonpositive event
// sequence carries no fold position, so the helper refuses before any read.
// A full fold always carries a positive sequence, so the helper alone
// proves this bound.
func TestRecordedWorkflowHoldHelperNonpositiveSequenceRefuses(t *testing.T) {
	fixture := recordedHoldBuildJournal(t, "recorded-hold-nonpositive", recordedHoldPositiveVariant())
	admitted, err := recordedHoldProbeAdmission(t, fixture.work, -1)
	if err != nil {
		t.Fatalf("the recorded hold helper must refuse a nonpositive sequence without erroring: %v", err)
	}
	if admitted {
		t.Fatal("the recorded hold helper admitted a nonpositive event sequence")
	}
}

// A negative: with the stored definition authority unreadable, folding the
// target completion must still end in a typed failure and never in an
// admission. The probe removes the instance projection under the fold's own
// transaction guard and folds the recorded completion directly from its
// row; the exact guard that fires is whatever reads the authority first,
// so this test pins only the failure boundary itself. The deferred rollback
// discards the probe transaction.
func TestRecordedWorkflowHoldUnreadableAuthorityRemainsFailure(t *testing.T) {
	fixture := recordedHoldBuildJournal(t, "recorded-hold-query", recordedHoldPositiveVariant())
	prefix := &recordedAdmissionJournal{workID: fixture.work.workID, events: fixture.work.journal.events[:len(fixture.work.journal.events)-1]}
	s := restoreRecordedAdmissionJournal(t, prefix, nil)
	defer s.Close()
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("the query-failure prefix must fold: %v", err)
	}
	target := fixture.work.journal.events[len(fixture.work.journal.events)-1]
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	scope, err := beginFold(context.Background(), tx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = scope.close(context.Background()) }()
	if _, err := tx.ExecContext(context.Background(), `DELETE FROM workflow_instances WHERE work_id=?`, fixture.work.workID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO domain_events(seq,event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?,?)`,
		target.seq, target.eventID, target.kind, target.subjectType, target.subjectID, target.actor, target.occurredAt, target.payloadVersion, string(target.payload)); err != nil {
		t.Fatalf("record the target event %s: %v", target.eventID, err)
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, target.occurredAt)
	if err != nil {
		t.Fatal(err)
	}
	event := Event{
		Seq: target.seq, EventID: target.eventID, Kind: target.kind, SubjectType: SubjectType(target.subjectType),
		SubjectID: target.subjectID, Actor: target.actor, OccurredAt: occurredAt,
		PayloadVersion: target.payloadVersion, Payload: json.RawMessage(target.payload),
	}
	foldErr := foldRecordedEvent(workflowReplayContext(context.Background()), tx, event, scope)
	var failure *Failure
	if !errors.As(foldErr, &failure) {
		t.Fatalf("the unreadable definition authority must refuse the completion with a typed failure, got: %v", foldErr)
	}
}

// A direct negative of the recorded hold helper: an otherwise-eligible
// replay event judged over an already rolled-back transaction must return
// false with a nonnil error — a query error propagates, never admits.
func TestRecordedWorkflowHoldHelperQueryErrorPropagates(t *testing.T) {
	fixture := recordedHoldBuildJournal(t, "recorded-hold-queryerr", recordedHoldPositiveVariant())
	prefix := &recordedAdmissionJournal{workID: fixture.work.workID, events: fixture.work.journal.events[:len(fixture.work.journal.events)-1]}
	s := restoreRecordedAdmissionJournal(t, prefix, nil)
	defer s.Close()
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("the query-error prefix must fold: %v", err)
	}
	target := fixture.work.journal.events[len(fixture.work.journal.events)-1]
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := VerifyWorkflowInstanceDefinitionTx(context.Background(), tx, BuiltinWorkflowRegistry(), fixture.work.workID)
	if err != nil {
		t.Fatal(err)
	}
	var currentStep string
	if err := tx.QueryRowContext(context.Background(), `SELECT current_step FROM workflow_instances WHERE work_id=?`, fixture.work.workID).Scan(&currentStep); err != nil {
		t.Fatal(err)
	}
	var p workflowActionCompletedPayload
	if err := json.Unmarshal(target.payload, &p); err != nil {
		t.Fatal(err)
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, target.occurredAt)
	if err != nil {
		t.Fatal(err)
	}
	event := Event{
		Seq: target.seq, EventID: target.eventID, Kind: target.kind, SubjectType: SubjectType(target.subjectType),
		SubjectID: target.subjectID, Actor: target.actor, OccurredAt: occurredAt,
		PayloadVersion: target.payloadVersion, Payload: json.RawMessage(target.payload),
	}
	event.replaySourcePayloadVersion = 2
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	admitted, queryErr := workflowRecordedHoldAdmission(workflowReplayContext(context.Background()), tx, event, p, entry.Definition, currentStep)
	if admitted {
		t.Fatal("the recorded hold helper admitted over a rolled-back transaction")
	}
	if queryErr == nil {
		t.Fatal("the rolled-back transaction's query error must propagate, not admit")
	}
}

// recordedHoldContextPairJournal records one synthetic context checkpoint at
// the named step of the pinned instance and a summary boundary over it. The
// boundary epoch is the caller's knob: the passing pair binds the
// checkpoint's epoch, the mismatches name a stale epoch or a checkpoint
// recorded at another step, with version and epoch otherwise equal.
func recordedHoldContextPairJournal(t *testing.T, workID string, boundaryEpoch int64, checkpointStep string) *recordedAdmissionWork {
	t.Helper()
	w := recordedAdmissionStartWork(t, workID)
	executor := w.recordActor(t, "agent", recordedAdmissionExecutorAgent)
	w.selectDefinition(t, "break_fix", "workflow.break_fix", 15, recordedPinBreakFixV15)
	checkpointID := "checkpoint/" + workID
	w.journal.append(WorkflowContextCheckpointed, "work_item", workID, executor, 1, map[string]any{
		"checkpoint_id": checkpointID, "checkpoint_sequence": int64(0), "step_id": checkpointStep,
		"attempt_epoch": int64(1), "active_unit": "unit/" + workID,
		"hypothesis":        "synthetic recorded-hold hypothesis",
		"diagnosis":         "synthetic recorded-hold diagnosis",
		"strategy":          "synthetic recorded-hold strategy",
		"touched_refs":      []string{"ref/" + workID},
		"evidence_refs":     []string{"evidence/" + workID},
		"pending_questions": []string{}, "pending_decisions": []string{},
		"workflow_ref": "workflow.break_fix", "workflow_definition_version": int64(15),
		"workflow_definition_digest": recordedPinBreakFixV15,
		"actor_ref":                  executor, "request_id": "request/" + workID + "/checkpoint",
		"expected_version": w.version, "resulting_version": w.version + 1, "work_id": workID,
	})
	w.version++
	w.journal.append(WorkflowContextBoundaryCrossed, "work_item", workID, executor, 1, map[string]any{
		"boundary_id": "boundary/" + workID, "boundary_sequence": int64(0), "boundary_kind": "summary",
		"checkpoint_id": checkpointID, "checkpoint_sequence": int64(0),
		"summary":      "synthetic recorded-hold boundary summary",
		"workflow_ref": "workflow.break_fix", "workflow_definition_version": int64(15),
		"workflow_definition_digest": recordedPinBreakFixV15,
		"attempt_epoch":              boundaryEpoch,
		"actor_ref":                  executor, "request_id": "request/" + workID + "/boundary",
		"expected_version": w.version, "resulting_version": w.version + 1, "work_id": workID,
	})
	w.version++
	return w
}

// The unchanged boundary guard: a synthetic checkpoint/boundary pair that
// agrees on version, step, and epoch folds untouched, with one checkpoint
// row, one boundary row, and the instance still at its step.
func TestRecordedWorkflowHoldContextBoundaryPairFoldsUnchanged(t *testing.T) {
	w := recordedHoldContextPairJournal(t, "recorded-hold-context-pass", 1, "reproduce")
	s := recordedAdmissionRequireFold(t, w, nil)
	defer s.Close()
	if state, step, _ := recordedAdmissionInstanceState(t, s, w.workID); state != "planned" || step != "reproduce" {
		t.Fatalf("replayed instance = %q/%q, want planned/reproduce", state, step)
	}
	var checkpoints, boundaries int
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT count(*) FROM workflow_context_checkpoints WHERE work_id=?`, w.workID).Scan(&checkpoints); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT count(*) FROM workflow_context_boundaries WHERE work_id=?`, w.workID).Scan(&boundaries); err != nil {
		t.Fatal(err)
	}
	if checkpoints != 1 || boundaries != 1 {
		t.Fatalf("context rows = %d checkpoints, %d boundaries, want 1 and 1", checkpoints, boundaries)
	}
	recordedAdmissionRebuildStable(t, s, w.journal)
}

// The unchanged boundary guard's epoch mismatch: a boundary naming an epoch
// the durable checkpoint does not carry keeps its stale-reference refusal.
func TestRecordedWorkflowHoldContextBoundaryEpochMismatchKeepsRefusal(t *testing.T) {
	w := recordedHoldContextPairJournal(t, "recorded-hold-context-mismatch", 2, "reproduce")
	recordedAdmissionRequireRefusal(t, w, nil, KindStaleAttempt, "context boundary references a stale checkpoint or attempt")
}

// The unchanged boundary guard's step mismatch: a checkpoint recorded at a
// step other than the projected one, with version and epoch otherwise
// equal, keeps the boundary's stale-reference refusal; only the step
// differs from the passing pair.
func TestRecordedWorkflowHoldContextBoundaryForeignCheckpointStepKeepsRefusal(t *testing.T) {
	w := recordedHoldContextPairJournal(t, "recorded-hold-context-step", 1, "diagnose")
	recordedAdmissionRequireRefusal(t, w, nil, KindStaleAttempt, "context boundary references a stale checkpoint or attempt")
}

package store

// Prefix-bound premise routing regressions over the recorded-admission
// synthetic-journal harness. One implementation journal records a non-ok
// verdict, confirms the premise at acceptance, re-delivers from refine to the
// delivery gate, and crosses one summary context checkpoint/boundary pair.
// The intact log then carries later ok verdicts on the same predicate and
// contract. The confirming completion's route must read the verdicts before
// its own sequence, so both journals rebuild the same prefix.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// recordedBoundaryPin selects one archived implementation pin and the
// bind_evidence completion payload version whose producer window registered
// it, so each scenario reuses the mapped recovery-binding fixture unchanged.
type recordedBoundaryPin struct {
	name          string
	workID        string
	version       int64
	digest        string
	sourceVersion int
}

// recordedBoundaryFold carries the built journal plus the values the rebuilt
// projections must reproduce: the prefix/suffix split, each journal's final
// work version, and the pair's checkpoint work version.
type recordedBoundaryFold struct {
	work                  *recordedAdmissionWork
	receipts              []recordedAdmissionReceipt
	prefixEventCount      int
	checkpointID          string
	checkpointWorkVersion int64
	prefixVersion         int64
	finalVersion          int64
}

// recordedBoundaryBuildFold builds one prefix/suffix journal on the named
// pin. The prefix ends at the context boundary; the suffix carries later ok
// verdicts on the same predicate and contract.
func recordedBoundaryBuildFold(t *testing.T, pin recordedBoundaryPin) *recordedBoundaryFold {
	t.Helper()
	plan := recordedAdmissionBindingPinPlan(pin.workID, "implementation", "workflow.implementation", pin.digest, pin.version,
		[]string{"execution", "refine", "delivery"}, "acceptance", "execution", pin.sourceVersion)
	w, receipts := recordedAdmissionBuildBindingScenario(t, plan, recordedAdmissionBindingOptions{withContract: true, withEarlierBinding: true})
	executor := w.actorRef[recordedAdmissionExecutorAgent]
	owner := w.actorRef[recordedAdmissionOwnerAgent]
	evaluator := w.recordActor(t, "agent", "agent/evaluator")
	checkpointID := "checkpoint/" + pin.workID

	// The confirming completion routes on the verdicts before its own
	// sequence: the non-ok verdict takes the failure edge from acceptance
	// back to refine.
	w.journal.append(WorkflowVerdictRecorded, "work_item", pin.workID, evaluator, 1, map[string]any{
		"contract_version":           int64(1),
		"evaluation_evidence":        []string{"evidence:" + pin.workID + "-acceptance"},
		"expected_version":           w.version,
		"incomparable_with_approved": true,
		"predicate_id":               "predicate:primary",
		"resulting_version":          w.version + 1,
		"verdict_actor_ref":          evaluator,
		"verdict_kind":               "insufficient_evidence",
		"work_id":                    pin.workID,
	})
	w.version++
	w.completeAction(t, "confirm_premise", "acceptance", owner, 1, nil)
	w.completeAction(t, "record_delivery", "refine", executor, 1, nil)
	w.journal.append(WorkflowContextCheckpointed, "work_item", pin.workID, executor, 1, map[string]any{
		"checkpoint_id": checkpointID, "checkpoint_sequence": int64(0), "step_id": "delivery",
		"attempt_epoch": int64(1), "active_unit": "unit/" + pin.workID,
		"hypothesis":        "synthetic recorded-boundary hypothesis",
		"diagnosis":         "synthetic recorded-boundary diagnosis",
		"strategy":          "synthetic recorded-boundary strategy",
		"touched_refs":      []string{"ref/" + pin.workID},
		"evidence_refs":     []string{"evidence/" + pin.workID},
		"pending_questions": []string{}, "pending_decisions": []string{},
		"workflow_ref": "workflow.implementation", "workflow_definition_version": pin.version,
		"workflow_definition_digest": pin.digest,
		"actor_ref":                  executor, "request_id": "request/" + pin.workID + "/checkpoint",
		"expected_version": w.version, "resulting_version": w.version + 1, "work_id": pin.workID,
	})
	w.version++
	w.journal.append(WorkflowContextBoundaryCrossed, "work_item", pin.workID, executor, 1, map[string]any{
		"boundary_id": "boundary/" + pin.workID, "boundary_sequence": int64(0), "boundary_kind": "summary",
		"checkpoint_id": checkpointID, "checkpoint_sequence": int64(0),
		"summary":      "synthetic recorded-boundary summary",
		"workflow_ref": "workflow.implementation", "workflow_definition_version": pin.version,
		"workflow_definition_digest": pin.digest,
		"attempt_epoch":              int64(1),
		"actor_ref":                  executor, "request_id": "request/" + pin.workID + "/boundary",
		"expected_version": w.version, "resulting_version": w.version + 1, "work_id": pin.workID,
	})
	w.version++
	fold := &recordedBoundaryFold{
		work: w, receipts: receipts, prefixEventCount: len(w.journal.events), checkpointID: checkpointID,
		checkpointWorkVersion: w.version - 1, prefixVersion: w.version,
	}
	// The suffix: later ok verdicts on the same approved predicate and
	// contract, recorded after the boundary.
	for _, evidence := range []string{"evidence:" + pin.workID + "-later-a", "evidence:" + pin.workID + "-later-b"} {
		w.journal.append(WorkflowVerdictRecorded, "work_item", pin.workID, evaluator, 1, map[string]any{
			"contract_version":           int64(1),
			"evaluation_evidence":        []string{evidence},
			"expected_version":           w.version,
			"incomparable_with_approved": false,
			"predicate_id":               "predicate:primary",
			"resulting_version":          w.version + 1,
			"verdict_actor_ref":          evaluator,
			"verdict_kind":               "ok",
			"work_id":                    pin.workID,
		})
		w.version++
	}
	fold.finalVersion = w.version
	return fold
}

// recordedBoundaryAssertPrefix requires one rebuilt store to carry its
// journal's route and pair: the instance at the delivery gate, the pair
// folded exactly once, and the work version the passed journal ends at.
func recordedBoundaryAssertPrefix(t *testing.T, s *Store, fold *recordedBoundaryFold, wantVersion int64) {
	t.Helper()
	if state, step, _ := recordedAdmissionInstanceState(t, s, fold.work.workID); state != "running" || step != "delivery" {
		t.Fatalf("replayed prefix instance = %q/%q, want running/delivery", state, step)
	}
	var version int64
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT version FROM work_items WHERE id=?`, fold.work.workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != wantVersion {
		t.Fatalf("replayed work version = %d, want this journal's own %d", version, wantVersion)
	}
	var checkpoints int
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT count(*) FROM workflow_context_checkpoints WHERE work_id=?`, fold.work.workID).Scan(&checkpoints); err != nil {
		t.Fatal(err)
	}
	if checkpoints != 1 {
		t.Fatalf("context checkpoints = %d, want the prefix pair's single row", checkpoints)
	}
	var checkpointVersion int64
	var checkpointSequence int64
	var checkpointStep string
	var checkpointEpoch int64
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(),
		`SELECT work_version,checkpoint_sequence,step_id,attempt_epoch FROM workflow_context_checkpoints WHERE work_id=? AND checkpoint_id=?`,
		fold.work.workID, fold.checkpointID).Scan(&checkpointVersion, &checkpointSequence, &checkpointStep, &checkpointEpoch); err != nil {
		t.Fatal(err)
	}
	if checkpointVersion != fold.checkpointWorkVersion || checkpointSequence != 1 || checkpointStep != "delivery" || checkpointEpoch != 1 {
		t.Fatalf("context checkpoint row = version %d sequence %d step %q epoch %d, want version %d sequence 1 delivery epoch 1",
			checkpointVersion, checkpointSequence, checkpointStep, checkpointEpoch, fold.checkpointWorkVersion)
	}
	var boundaries int
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT count(*) FROM workflow_context_boundaries WHERE work_id=?`, fold.work.workID).Scan(&boundaries); err != nil {
		t.Fatal(err)
	}
	if boundaries != 1 {
		t.Fatalf("context boundaries = %d, want the prefix pair's single row", boundaries)
	}
	var boundaryVersion, boundarySequence, boundaryCheckpointSequence, boundaryEpoch int64
	var boundaryKind, boundaryCheckpoint string
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(),
		`SELECT work_version,boundary_sequence,boundary_kind,checkpoint_id,checkpoint_sequence,attempt_epoch FROM workflow_context_boundaries WHERE work_id=?`,
		fold.work.workID).Scan(&boundaryVersion, &boundarySequence, &boundaryKind, &boundaryCheckpoint, &boundaryCheckpointSequence, &boundaryEpoch); err != nil {
		t.Fatal(err)
	}
	if boundaryVersion != fold.prefixVersion || boundarySequence != 1 || boundaryKind != "summary" || boundaryCheckpoint != fold.checkpointID || boundaryCheckpointSequence != 1 || boundaryEpoch != 1 {
		t.Fatalf("context boundary row = version %d sequence %d kind %q checkpoint %q/%d epoch %d, want version %d sequence 1 summary %q/1 epoch 1",
			boundaryVersion, boundarySequence, boundaryKind, boundaryCheckpoint, boundaryCheckpointSequence, boundaryEpoch, fold.prefixVersion, fold.checkpointID)
	}
}

// recordedBoundaryAssertSamePrefix requires the prefix-only and full-journal
// rebuilds to reproduce the same prefix instance row; each store's pair rows
// are already pinned field-exact by recordedBoundaryAssertPrefix.
func recordedBoundaryAssertSamePrefix(t *testing.T, prefixStore, fullStore *Store, fold *recordedBoundaryFold) {
	t.Helper()
	prefixState, prefixStep, prefixActor := recordedAdmissionInstanceState(t, prefixStore, fold.work.workID)
	fullState, fullStep, fullActor := recordedAdmissionInstanceState(t, fullStore, fold.work.workID)
	if prefixState != fullState || prefixStep != fullStep || prefixActor != fullActor {
		t.Fatalf("full-journal instance = %q/%q/%q, want the prefix-only %q/%q/%q", fullState, fullStep, fullActor, prefixState, prefixStep, prefixActor)
	}
}

// Across both mapped implementation pins, the intact journal with its later
// ok verdicts must rebuild the same prefix the prefix-only journal rebuilds:
// bytes, work version, and the pair's rows conserved across a second rebuild.
func TestRecordedWorkflowBoundaryFullFoldPrefixConservedAcrossLaterOKVerdicts(t *testing.T) {
	pins := []recordedBoundaryPin{
		{name: "implementation-v16-source2", workID: "recorded-boundary-impl16", version: 16, digest: recordedPinImplementationV16, sourceVersion: 2},
		{name: "implementation-v21-source3", workID: "recorded-boundary-impl21", version: 21, digest: recordedPinImplementationV21, sourceVersion: 3},
	}
	for _, pin := range pins {
		t.Run(pin.name, func(t *testing.T) {
			fold := recordedBoundaryBuildFold(t, pin)
			prefixJournal := &recordedAdmissionJournal{workID: fold.work.workID, events: fold.work.journal.events[:fold.prefixEventCount]}
			prefixStore := recordedAdmissionRequireFold(t, &recordedAdmissionWork{journal: prefixJournal, workID: fold.work.workID}, fold.receipts)
			defer prefixStore.Close()
			recordedBoundaryAssertPrefix(t, prefixStore, fold, fold.prefixVersion)

			// The intact journal must rebuild the same prefix.
			fullStore := recordedAdmissionRequireFold(t, fold.work, fold.receipts)
			defer fullStore.Close()
			recordedBoundaryAssertPrefix(t, fullStore, fold, fold.finalVersion)
			recordedBoundaryAssertSamePrefix(t, prefixStore, fullStore, fold)
			recordedAdmissionRebuildStable(t, fullStore, fold.work.journal)
		})
	}
}

// recordedBoundaryRoutingPin is the single pin the routing-negative scenarios
// reuse. Routing defaults and error semantics are not pin-dependent, so one
// mapped implementation pin carries them all.
var recordedBoundaryRoutingPin = recordedBoundaryPin{
	name: "implementation-v16-source2", workID: "recorded-boundary-impl16", version: 16,
	digest: recordedPinImplementationV16, sourceVersion: 2,
}

// recordedBoundaryRoutingJournal builds the binding scenario and ends the
// journal with the confirming acceptance completion. With withVerdict the
// journal carries one non-ok verdict before that completion, so the route
// would take the failure edge to refine; without one the forward default
// must hold.
func recordedBoundaryRoutingJournal(t *testing.T, workID string, withVerdict bool) (*recordedAdmissionWork, []recordedAdmissionReceipt) {
	t.Helper()
	pin := recordedBoundaryRoutingPin
	pin.workID = workID
	plan := recordedAdmissionBindingPinPlan(workID, "implementation", "workflow.implementation", pin.digest, pin.version,
		[]string{"execution", "refine", "delivery"}, "acceptance", "execution", pin.sourceVersion)
	w, receipts := recordedAdmissionBuildBindingScenario(t, plan, recordedAdmissionBindingOptions{withContract: true, withEarlierBinding: true})
	owner := w.actorRef[recordedAdmissionOwnerAgent]
	if withVerdict {
		evaluator := w.recordActor(t, "agent", "agent/evaluator")
		w.journal.append(WorkflowVerdictRecorded, "work_item", workID, evaluator, 1, map[string]any{
			"contract_version":           int64(1),
			"evaluation_evidence":        []string{"evidence:" + workID + "-acceptance"},
			"expected_version":           w.version,
			"incomparable_with_approved": true,
			"predicate_id":               "predicate:primary",
			"resulting_version":          w.version + 1,
			"verdict_actor_ref":          evaluator,
			"verdict_kind":               "insufficient_evidence",
			"work_id":                    workID,
		})
		w.version++
	}
	w.completeAction(t, "confirm_premise", "acceptance", owner, 1, nil)
	return w, receipts
}

// recordedBoundaryRestoreThroughVerdict restores the journal without its
// final event and folds that prefix, so the caller can mutate or probe under
// its own transaction and fold the returned target event.
func recordedBoundaryRestoreThroughVerdict(t *testing.T, w *recordedAdmissionWork, receipts []recordedAdmissionReceipt) (*Store, recordedAdmissionEvent) {
	t.Helper()
	prefix := &recordedAdmissionJournal{workID: w.workID, events: w.journal.events[:len(w.journal.events)-1]}
	s := restoreRecordedAdmissionJournal(t, prefix, receipts)
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("the routing prefix must fold: %v", err)
	}
	return s, w.journal.events[len(w.journal.events)-1]
}

// recordedBoundaryProbeEvent rebuilds the stored target event the replay
// fold consumes: the stored header bytes plus the stored payload version the
// rebuild itself sets as the replay source marker, at the caller's sequence.
func recordedBoundaryProbeEvent(t *testing.T, target recordedAdmissionEvent, seq int64) Event {
	t.Helper()
	occurredAt, err := time.Parse(time.RFC3339Nano, target.occurredAt)
	if err != nil {
		t.Fatal(err)
	}
	event := Event{
		Seq: seq, EventID: target.eventID, Kind: target.kind, SubjectType: SubjectType(target.subjectType),
		SubjectID: target.subjectID, Actor: target.actor, OccurredAt: occurredAt,
		PayloadVersion: target.payloadVersion, Payload: json.RawMessage(target.payload),
	}
	event.replaySourcePayloadVersion = target.payloadVersion
	return event
}

// A routing default: a recorded confirmation at the acceptance step with no
// verdict in its history takes the forward edge to release, never the
// failure edge to refine.
func TestRecordedWorkflowBoundaryMissingVerdictDefaultsForwardRoute(t *testing.T) {
	w, receipts := recordedBoundaryRoutingJournal(t, "recorded-boundary-route-missing", false)
	s := recordedAdmissionRequireFold(t, w, receipts)
	defer s.Close()
	if state, step, _ := recordedAdmissionInstanceState(t, s, w.workID); state != "running" || step != "release" {
		t.Fatalf("missing-verdict confirmation = %q/%q, want running/release on the forward edge", state, step)
	}
	recordedAdmissionRebuildStable(t, s, w.journal)
}

// A routing default: a verdict naming a contract version above the active
// one is outside the confirming completion's verdict input, so the route
// takes the forward edge to release instead of the failure edge. The probe
// inserts the foreign-version verdict row and the confirmation under the
// fold's own transaction guard, because no contract approves that verdict
// and its own fold can never record it; the commit persists the probe so
// the folded step is observable.
func TestRecordedWorkflowBoundaryIncompatibleVerdictDefaultsForwardRoute(t *testing.T) {
	w, receipts := recordedBoundaryRoutingJournal(t, "recorded-boundary-route-incompat", false)
	s, target := recordedBoundaryRestoreThroughVerdict(t, w, receipts)
	defer s.Close()
	if state, step, _ := recordedAdmissionInstanceState(t, s, w.workID); state != "running" || step != "acceptance" {
		t.Fatalf("routing prefix instance = %q/%q, want running/acceptance", state, step)
	}
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	scope, err := beginFold(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	incompatible := map[string]any{
		"contract_version":           int64(5),
		"evaluation_evidence":        []string{"evidence:" + w.workID + "-foreign"},
		"expected_version":           w.version,
		"incomparable_with_approved": true,
		"predicate_id":               "predicate:primary",
		"resulting_version":          w.version + 1,
		"verdict_actor_ref":          w.actorRef[recordedAdmissionExecutorAgent],
		"verdict_kind":               "insufficient_evidence",
		"work_id":                    w.workID,
	}
	verdictRaw, err := json.Marshal(incompatible)
	if err != nil {
		t.Fatal(err)
	}
	if err := recordedBoundaryInsertProbeRows(t, tx, w.workID, target, verdictRaw); err != nil {
		t.Fatal(err)
	}
	// The folded event carries its own inserted sequence, so the bounded
	// verdict read examines every row before the completion, including the
	// probe verdict directly below it.
	if err := foldRecordedEvent(workflowReplayContext(ctx), tx, recordedBoundaryProbeEvent(t, target, target.seq+1), scope); err != nil {
		t.Fatalf("the incompatible verdict must default to the forward route, not refuse: %v", err)
	}
	if err := scope.close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if state, step, _ := recordedAdmissionInstanceState(t, s, w.workID); state != "running" || step != "release" {
		t.Fatalf("incompatible-verdict confirmation = %q/%q, want running/release on the forward edge", state, step)
	}
}

// recordedBoundaryInsertProbeRows writes the caller's never-folded verdict
// row at the target's sequence and shifts the confirming completion one
// sequence past it, mirroring the append-only log's insert rules.
func recordedBoundaryInsertProbeRows(t *testing.T, tx *sql.Tx, workID string, target recordedAdmissionEvent, verdictPayload json.RawMessage) error {
	t.Helper()
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO domain_events(seq,event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?,?)`,
		target.seq, target.eventID+"-probe-verdict", WorkflowVerdictRecorded, target.subjectType, workID, target.actor, target.occurredAt, 1, string(verdictPayload)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO domain_events(seq,event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?,?)`,
		target.seq+1, target.eventID, target.kind, target.subjectType, target.subjectID, target.actor, target.occurredAt, target.payloadVersion, string(target.payload)); err != nil {
		return err
	}
	return nil
}

// A typed error: a verdict payload that cannot unmarshal into the verdict
// record keeps the typed refusal the verdict readers share and never folds
// the confirmation. The probe inserts the unreadable verdict row under the
// fold's own transaction guard, because such a verdict's own fold can never
// record it; the rollback discards the probe, so no confirm lands.
func TestRecordedWorkflowBoundaryMalformedVerdictKeepsTypedRoutingFailure(t *testing.T) {
	w, receipts := recordedBoundaryRoutingJournal(t, "recorded-boundary-route-malformed", false)
	s, target := recordedBoundaryRestoreThroughVerdict(t, w, receipts)
	defer s.Close()
	if state, step, _ := recordedAdmissionInstanceState(t, s, w.workID); state != "running" || step != "acceptance" {
		t.Fatalf("routing prefix instance = %q/%q, want running/acceptance", state, step)
	}
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	scope, err := beginFold(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = scope.close(ctx) }()
	if err := recordedBoundaryInsertProbeRows(t, tx, w.workID, target, json.RawMessage(`{"evaluation_evidence":"not-an-array"}`)); err != nil {
		t.Fatal(err)
	}
	foldErr := foldRecordedEvent(workflowReplayContext(ctx), tx, recordedBoundaryProbeEvent(t, target, target.seq+1), scope)
	var failure *Failure
	if !errors.As(foldErr, &failure) {
		t.Fatalf("malformed-verdict fold error is not a typed failure: %v", foldErr)
	}
	if failure.Kind != KindInvariantViolation || !strings.Contains(failure.Detail, "workflow verdict payload is malformed") {
		t.Fatalf("malformed-verdict refusal %s/%q does not name the typed verdict-payload refusal", failure.Kind, failure.Detail)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var confirms int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='confirm_premise'`, w.workID, WorkflowActionCompleted).Scan(&confirms); err != nil {
		t.Fatal(err)
	}
	if confirms != 0 {
		t.Fatalf("the rolled-back probe left %d confirming completions in the log", confirms)
	}
}

// A typed error: a transaction whose reads can no longer succeed makes the
// verdict read fail closed. The read must return a typed failure, never an
// empty verdict history that would route the confirmation forward.
func TestRecordedWorkflowBoundaryVerdictQueryFailurePropagatesTypedError(t *testing.T) {
	w, receipts := recordedBoundaryRoutingJournal(t, "recorded-boundary-route-queryerr", true)
	s, target := recordedBoundaryRestoreThroughVerdict(t, w, receipts)
	defer s.Close()
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	_, readErr := latestWorkflowVerdictsBefore(context.Background(), tx, w.workID, 1, target.seq)
	var failure *Failure
	if !errors.As(readErr, &failure) {
		t.Fatalf("the rolled-back transaction's verdict read error is not a typed failure: %v", readErr)
	}
}

// The live invariant that makes live/replay equivalence hold: at the moment
// a real confirmation completion folds, its own persisted sequence exceeds
// every verdict sequence in the log, so no verdict after the completion can
// exist in that transaction. The live fold routes the healthy confirmation
// to the forward edge, and a replay of the same log reproduces that route.
func TestRecordedWorkflowBoundaryLiveConfirmRoutesOnPersistedSeq(t *testing.T) {
	const workID = "recorded-boundary-live-seq"
	s, _, reviewer := seedItemAtAcceptance(t, workID, true)
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:primary","verdict_kind":"ok"}`), 0, reviewer); err != nil {
		t.Fatalf("record the healthy verdict: %v", err)
	}
	operatorRef := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/operator", "session/"+workID)
	premiseVersion := verdictItemVersion(t, s, workID)
	operatorRecorded := workflowEvent("operator-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": premiseVersion, "resulting_version": premiseVersion + 1, "actor_ref": operatorRef, "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/operator", "session_ref": "session/" + workID, "actor_class": "operator"})
	premise := workflowEventWithActor("premise-"+workID, WorkflowPremiseConfirmed, workID, operatorRef, map[string]any{"work_id": workID, "expected_version": premiseVersion + 1, "resulting_version": premiseVersion + 2, "contract_version": 1, "confirming_actor_ref": operatorRef})
	confirmCompleted := workflowActionCompletedFixture("premise-completed-"+workID, workID, operatorRef, premiseVersion+2, "acceptance", "confirm_premise")
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{operatorRecorded, premise, confirmCompleted}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): premiseVersion}}); err != nil {
		t.Fatalf("the live confirmation must fold: %v", err)
	}
	if state, step, _ := recordedAdmissionInstanceState(t, s, workID); state != "running" || step != "release" {
		t.Fatalf("live confirmation = %q/%q, want running/release on the forward edge", state, step)
	}
	var confirmSeq int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT seq FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='confirm_premise'`, workID, WorkflowActionCompleted).Scan(&confirmSeq); err != nil {
		t.Fatal(err)
	}
	if confirmSeq <= 0 {
		t.Fatalf("the confirming completion persisted seq %d, want its own positive sequence", confirmSeq)
	}
	var verdictMax int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(MAX(seq),0) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkflowVerdictRecorded).Scan(&verdictMax); err != nil {
		t.Fatal(err)
	}
	if verdictMax <= 0 || verdictMax >= confirmSeq {
		t.Fatalf("verdict max seq %d vs confirming completion seq %d, want the verdict strictly before the completion", verdictMax, confirmSeq)
	}
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("the replay of the live log must fold: %v", err)
	}
	if state, step, _ := recordedAdmissionInstanceState(t, s, workID); state != "running" || step != "release" {
		t.Fatalf("replayed live log = %q/%q, want the live route running/release", state, step)
	}
}

// The unchanged boundary guard's latest-checkpoint clause: a boundary whose
// checkpoint row is no longer the latest one keeps its stale-reference
// refusal even with version, step, and epoch otherwise equal. The later
// checkpoint row is inserted under the fold's own transaction guard, because
// the version-coupled fold cannot record two checkpoints at one version.
func TestRecordedWorkflowBoundaryNonLatestCheckpointKeepsRefusal(t *testing.T) {
	w := recordedHoldContextPairJournal(t, "recorded-boundary-nonlatest", 1, "reproduce")
	s, target := recordedBoundaryRestoreThroughVerdict(t, w, nil)
	defer s.Close()
	if state, step, _ := recordedAdmissionInstanceState(t, s, w.workID); state != "planned" || step != "reproduce" {
		t.Fatalf("checkpoint prefix instance = %q/%q, want planned/reproduce", state, step)
	}
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	scope, err := beginFold(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = scope.close(ctx) }()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO workflow_context_checkpoints(work_id,work_version,checkpoint_sequence,checkpoint_id,step_id,attempt_epoch,active_unit,hypothesis,diagnosis,strategy,touched_refs,evidence_refs,pending_questions,pending_decisions,workflow_ref,workflow_definition_version,workflow_definition_digest,actor_ref,request_id,recorded_at)
		 SELECT work_id,work_version,2,checkpoint_id||'-later',step_id,attempt_epoch,active_unit,hypothesis,diagnosis,strategy,touched_refs,evidence_refs,pending_questions,pending_decisions,workflow_ref,workflow_definition_version,workflow_definition_digest,actor_ref,request_id||'-later',recorded_at
		 FROM workflow_context_checkpoints WHERE work_id=?`, w.workID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO domain_events(seq,event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?,?)`,
		target.seq, target.eventID, target.kind, target.subjectType, target.subjectID, target.actor, target.occurredAt, target.payloadVersion, string(target.payload)); err != nil {
		t.Fatalf("record the boundary %s: %v", target.eventID, err)
	}
	foldErr := foldRecordedEvent(workflowReplayContext(ctx), tx, recordedBoundaryProbeEvent(t, target, target.seq), scope)
	var failure *Failure
	if !errors.As(foldErr, &failure) {
		t.Fatalf("nonlatest-checkpoint fold error is not a typed failure: %v", foldErr)
	}
	if failure.Kind != KindStaleAttempt || !strings.Contains(failure.Detail, "must reference the latest context checkpoint") {
		t.Fatalf("nonlatest-checkpoint refusal %s/%q does not name the latest-checkpoint guard", failure.Kind, failure.Detail)
	}
}

// The unchanged boundary guard's active-operation clause: a boundary folded
// while an operation row is still pending keeps its typed refusal. The
// pending row is written between the restore and the rebuild, so the
// refusal comes from the guard's own durable_operations read.
func TestRecordedWorkflowBoundaryActiveOperationKeepsRefusal(t *testing.T) {
	w := recordedHoldContextPairJournal(t, "recorded-boundary-activeop", 1, "reproduce")
	s := restoreRecordedAdmissionJournal(t, w.journal, nil)
	defer s.Close()
	if _, err := s.DatabaseForTesting().Exec(
		`INSERT INTO durable_operations(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_version,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,result_kind,result_payload,evidence_refs,changed_refs,principal_ref,request_id,observed_at,completed_at,contract_digest)
		 VALUES('op:recorded-boundary-activeop-pending',1,?, 'workflow.break_fix',15,'reproduce','internal_sqlite',?, '{}', 'pending','','[]','[]','principal/test','request/recorded-boundary-activeop-pending',?,?,'sha256:0000000000000000000000000000000000000000000000000000000000000000')`,
		w.workID, recordedAdmissionInputsDigest, recordedAdmissionObservedAt, recordedAdmissionObservedAt); err != nil {
		t.Fatal(err)
	}
	err := RebuildFromLog(context.Background(), s)
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("active-operation rebuild error is not a typed failure: %v", err)
	}
	if failure.Kind != KindInvalidOperation || !strings.Contains(failure.Detail, "summary boundary is forbidden during an active effect or attempt") {
		t.Fatalf("active-operation refusal %s/%q does not name the active-attempt guard", failure.Kind, failure.Detail)
	}
	recordedAdmissionAssertNoWorkflowProjections(t, s)
	recordedAdmissionAssertLogIntact(t, s, w.journal)
}

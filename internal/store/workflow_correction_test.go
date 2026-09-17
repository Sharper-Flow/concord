package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSameWorkflowCorrectionIncludesFailureDetails(t *testing.T) {
	base := &WorkflowCorrectionContext{
		Disposition: "failed", AttemptCount: 1, AttemptLimit: 3, Diagnosis: "diagnosis", Strategy: "strategy",
		PredicateIDs: []string{"predicate:test"}, EvidenceRefs: []string{"evidence:test"},
	}
	for name, mutate := range map[string]func(*WorkflowCorrectionContext){
		"failure kind":   func(value *WorkflowCorrectionContext) { value.FailureKind = "transport_failure" },
		"failure detail": func(value *WorkflowCorrectionContext) { value.FailureDetail = "worker could not reach the service" },
	} {
		t.Run(name, func(t *testing.T) {
			left := *base
			right := *base
			mutate(&right)
			if sameWorkflowCorrection(&left, &right) {
				t.Fatalf("sameWorkflowCorrection treated %s as equal", name)
			}
		})
	}
}

// issue1013SuccessorContract is a complete recovery payload: the correction
// replaces the wrong subject the pinned contract named with the delivered one.
func issue1013SuccessorContract() json.RawMessage {
	return json.RawMessage(`{"contract_version":2,"premise":"corrected predicate subject","outcome_predicates":[{"predicate_id":"predicate:primary","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:workflow","immutable_subject_ref":"commit:e3d7c6e6","expected_result":"pass"}}],"required_evidence":["verification"],"route_conventions":[],"spec_mandate":[],"law_modifies":[],"rigor_class":"prototype_internal","supersede_reason":"the pinned subject named an unrelated commit","audit_evidence":["evidence:issue1013"]}`)
}

func issue1013Preflight(t *testing.T, s *Store, workID, action string, payload json.RawMessage, actor WorkflowActor) error {
	t.Helper()
	return WorkflowActionPreflightWithRegistry(context.Background(), s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
		WorkID: workID, ActionID: action, Payload: payload, Actor: actor,
	})
}

// The late verdict recovery admitted by the owning transaction must also pass
// the read-only preflight, and the admission closes again once the predicate
// holds a healthy verdict (#1013). The same journey shows the supersede
// refusal outside a recovery state names the stale-contract recovery route.
func TestIssue1013LateVerdictRecoveryPassesReadOnlyPreflight(t *testing.T) {
	t.Parallel()
	const workID = "issue1013-late-verdict-preflight"
	s, owner, _ := seedItemAtAcceptance(t, workID, false)
	reviewer := verdictReviewer(t, s, workID)
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:primary","verdict_kind":"outcome_mismatch","incomparable_with_approved":true}`), 0, reviewer); err != nil {
		t.Fatalf("record mismatch verdict: %v", err)
	}
	operator := operatorVerdictActor(t, workID)
	if err := runIssue933OperatorAction(t, s, workID, "confirm_premise", json.RawMessage(`{"contract_version":1}`), owner, operator); err != nil {
		t.Fatalf("confirm premise: %v", err)
	}
	healthy := json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:primary","verdict_kind":"ok"}`)
	if err := issue1013Preflight(t, s, workID, "record_verdict", healthy, owner); err != nil {
		t.Fatalf("read-only preflight refused the late verdict recovery: %v", err)
	}
	if err := runVerdictActionAs(t, s, workID, "record_verdict", healthy, 0, reviewer); err != nil {
		t.Fatalf("late healthy verdict recovery: %v", err)
	}
	// The predicate now holds a healthy verdict, so the recovery admission
	// must close and the read-only preflight return to the step refusal.
	err := issue1013Preflight(t, s, workID, "record_verdict", healthy, owner)
	if err == nil {
		t.Fatal("healthy comparable verdict replacement passed the read-only preflight")
	}
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindIllegalLifecycleTransition || failure.Detail != "workflow action is not declared on the current step" {
		t.Fatalf("post-recovery verdict failure=%v, want the current-step refusal", err)
	}
	// At release, away from the premise checkpoint and with the active
	// contract current, contract correction keeps its recovery refusal.
	err = issue1013Preflight(t, s, workID, "supersede_contract", issue1013SuccessorContract(), owner)
	if err == nil {
		t.Fatal("supersede_contract passed the read-only preflight outside a recovery state")
	}
	if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || failure.Detail != "contract recovery is available only for a stale workflow contract" {
		t.Fatalf("outside-recovery supersede failure=%v, want the stale-contract recovery refusal", err)
	}
}

// TestIssue1013ContractCorrectionAtFailedWorkerDispatch walks the route the
// issue reporter could not find. A live dispatch holds every advancing exit,
// the correction opens only once record_worker_failure puts the failure in the
// durable record, and a fresh fenced start returns the ordinary delivery exit.
// The work pin states the same facts the fold enforces at every stop
// (CD-0133 D1, D4).
func TestIssue1013ContractCorrectionAtFailedWorkerDispatch(t *testing.T) {
	const workID = "issue1013-failed-worker-correction"
	ctx := context.Background()
	s, owner, attemptID, entry := seedOldDefinitionWorker(t, workID)
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,1,'failed worker contract','internal_sqlite','["verification"]','[]','now',?,'[]','[]',1,'prototype_internal'); INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES(?,1,'predicate:primary',0,'check','{"kind":"check","check_ref":"check:workflow","immutable_subject_ref":"commit:old","expected_result":"pass"}'); DELETE FROM fold_guard`, workID, ownerRef, workID); err != nil {
		t.Fatal(err)
	}

	pin := issue1013Pin(t, s, workID)
	if issue1013HasIntent(pin, "record_delivery") {
		t.Fatalf("dispatched step offers record_delivery the fold refuses: %#v", pin.NextValidIntents)
	}
	if !issue1013HasIntent(pin, "accept_worker_result") {
		t.Fatalf("dispatched step hides accept_worker_result: %#v", pin.NextValidIntents)
	}
	if _, _, err := WorkflowActionDefinitionFor(ctx, s, BuiltinWorkflowRegistry(), workID, "supersede_contract"); err == nil {
		t.Fatal("contract correction resolved while the worker attempt was live")
	}

	failAbandonedWorkerAttempt(t, s, workID, attemptID)
	if _, _, err := WorkflowActionDefinitionFor(ctx, s, BuiltinWorkflowRegistry(), workID, "supersede_contract"); err == nil {
		t.Fatal("contract correction resolved before record_worker_failure put the failure in the record")
	}

	pin = issue1013Pin(t, s, workID)
	applyRecordWorkerFailureForTest(t, s, workID, owner, attemptID, 1, pin.Version, "issue1013-record-failure")
	_, action, err := WorkflowActionDefinitionFor(ctx, s, BuiltinWorkflowRegistry(), workID, "supersede_contract")
	if err != nil {
		t.Fatalf("resolve contract correction after the recorded failure: %v", err)
	}
	if action.ID != "supersede_contract" || action.Approval != ActionApprovalRequired {
		t.Fatalf("correction action = %#v, want operator-approved supersession", action)
	}
	pin = issue1013Pin(t, s, workID)
	var found bool
	for _, intent := range pin.NextValidIntents {
		if intent.ActionID == "supersede_contract" && intent.ExpectedVersion == pin.Version && intent.ReasonCode == "operator_contract_correction" {
			found = true
		}
	}
	if !found {
		t.Fatalf("work pin has no failed-worker correction intent: %#v", pin.NextValidIntents)
	}
	if issue1013HasIntent(pin, "record_delivery") {
		t.Fatalf("recorded failure alone restored record_delivery: %#v", pin.NextValidIntents)
	}

	workerRef, err := WorkflowActorRef(WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/worker", SessionRef: "session/" + workID, ActorClass: ActorAgent})
	if err != nil {
		t.Fatal(err)
	}
	start := workflowEventWithActor("issue1013-restart-"+workID, WorkflowActionStarted, workID, workerRef, map[string]any{
		"work_id": workID, "expected_version": pin.Version, "resulting_version": pin.Version + 1, "step_id": "repair",
		"action_id": "start_repair", "attempt_epoch": 2, "accepted_inputs_digest": "sha256:" + strings.Repeat("b", 64),
		"idempotency_identity": "issue1013-restart:" + workID, "actor_ref": workerRef,
	})
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{start}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): pin.Version}}); err != nil {
		t.Fatalf("start a fresh repair attempt: %v", err)
	}
	pin = issue1013Pin(t, s, workID)
	if !issue1013HasIntent(pin, "record_delivery") {
		t.Fatalf("fresh attempt did not restore record_delivery: %#v", pin.NextValidIntents)
	}

	registered, ok := BuiltinWorkflowRegistry().Lookup(entry.Definition.Ref, entry.Definition.Version)
	if !ok || registered.Digest != entry.Digest {
		t.Fatalf("pinned definition changed: before=%s after=%s", entry.Digest, registered.Digest)
	}
}

func TestRejectWorkerResultRecordsCorrectionContext(t *testing.T) {
	const workID = "issue1013-rejected-worker-result"
	s, workerRef, owner, attemptID := seedCompletedWorkerAtExecution(t, workID)
	defer s.Close()
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,1,'rejected worker contract','internal_sqlite','["verification"]','[]','now',?,'[]','[]',1,'prototype_internal'); INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES(?,1,'predicate:primary',0,'check','{"kind":"check","check_ref":"check:workflow","immutable_subject_ref":"commit:old","expected_result":"pass"}'); DELETE FROM fold_guard`, workID, ownerRef, workID); err != nil {
		t.Fatal(err)
	}

	_, action, err := WorkflowActionDefinitionFor(context.Background(), s, BuiltinWorkflowRegistry(), workID, "reject_worker_result")
	if err != nil {
		t.Fatalf("resolve worker result rejection: %v", err)
	}
	if action.ID != "reject_worker_result" || action.ExecutionMode != ActionHold {
		t.Fatalf("rejection action = %#v, want hold-mode reject_worker_result", action)
	}
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	result, err := applyWorkflowActionRawTx(ctx, tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: 10, ActionID: "reject_worker_result", Payload: mustJSONValue(map[string]any{
			"attempt_id": attemptID, "attempt_epoch": 1, "diagnosis": "the result misses the boundary case", "strategy": "change the helper and add a test",
			"predicate_ids": []string{"predicate:primary"}, "evidence_refs": []string{"evidence:review"},
		}), Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("f", 64), IdempotencyIdentity: "reject:" + workID, OperationID: "reject:" + workID,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "reject:" + workID, RequestID: "request:reject:" + workID, ContractDigest: testManifestDigest, Now: time.Unix(4, 0).UTC(),
	})
	_ = leaveFold(ctx, tx)
	if err != nil {
		tx.Rollback()
		t.Fatalf("reject worker result: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if result.ResultingVersion != 11 {
		t.Fatalf("rejection result version=%d, want 11", result.ResultingVersion)
	}

	var workerAttempt, diagnosis string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.worker_attempt_id'),json_extract(payload,'$.correction_diagnosis') FROM domain_events WHERE event_id=?`, "reject:"+workID+":completed").Scan(&workerAttempt, &diagnosis); err != nil {
		t.Fatal(err)
	}
	if workerAttempt != attemptID || diagnosis == "" {
		t.Fatalf("rejection event attempt=%q diagnosis=%q, want %q and a diagnosis", workerAttempt, diagnosis, attemptID)
	}
	pin, err := ReadWorkPin(ctx, s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if pin.Correction == nil || pin.Correction.Disposition != "rejected" || pin.Correction.Diagnosis != "the result misses the boundary case" {
		t.Fatalf("correction pin = %#v, want the recorded rejection context", pin.Correction)
	}
	_, correctionAction, err := WorkflowActionDefinitionFor(ctx, s, BuiltinWorkflowRegistry(), workID, "supersede_contract")
	if err != nil {
		t.Fatalf("resolve contract correction after rejected result: %v", err)
	}
	if correctionAction.ID != "supersede_contract" || correctionAction.Approval != ActionApprovalRequired {
		t.Fatalf("rejected-result correction action = %#v, want operator-approved supersession", correctionAction)
	}
	var correctionIntent bool
	for _, intent := range pin.NextValidIntents {
		if intent.ActionID == "supersede_contract" && intent.ExpectedVersion == pin.Version && intent.ReasonCode == "operator_contract_correction" {
			correctionIntent = true
		}
	}
	if !correctionIntent {
		t.Fatalf("work pin has no rejected-result correction intent: %#v", pin.NextValidIntents)
	}
	if binding, err := WorkflowFailedWorkerRetryBinding(ctx, s, workID); err != nil {
		t.Fatalf("read retry binding after rejected result: %v", err)
	} else if binding != nil {
		t.Fatalf("rejected completed result has retry approval binding %#v", binding)
	}
	start := workflowEventWithActor("restart-"+workID, WorkflowActionStarted, workID, workerRef, map[string]any{
		"work_id": workID, "expected_version": 11, "resulting_version": 12, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 2,
		"accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": "restart:" + workID, "actor_ref": workerRef,
	})
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{start}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 11}}); err != nil {
		t.Fatalf("start fresh correction attempt: %v", err)
	}
	packetPayload, err := json.Marshal(dispatchWorkerPacket(workID, "execution", "attempt:fresh-"+workID))
	if err != nil {
		t.Fatal(err)
	}
	fieldsPayload, err := json.Marshal(map[string]any{"attempt_id": "attempt:fresh-" + workID, "worker_packet": json.RawMessage(packetPayload)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = invokeWorkflowActionForCD0059(ctx, t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: 12, ActionID: "dispatch_worker", Payload: fieldsPayload, SessionWorktree: dispatchSessionWorktree(t, s, workID),
		Actor: owner, AcceptedInputsDigest: "sha256:" + strings.Repeat("b", 64), IdempotencyIdentity: "dispatch:" + workID, OperationID: "dispatch:" + workID,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "dispatch:" + workID, RequestID: "request:dispatch:" + workID, ContractDigest: testManifestDigest, Now: time.Unix(5, 0).UTC(),
	})
	if !hasFailureKind(err, KindInvalidPayload) {
		t.Fatalf("dispatch without correction context error=%v, want invalid_payload", err)
	}
	correction := pin.Correction
	correctionPayload := map[string]any{
		"disposition": correction.Disposition, "attempt_count": correction.AttemptCount, "attempt_limit": correction.AttemptLimit, "escalated": correction.Escalated,
		"diagnosis": correction.Diagnosis, "strategy": correction.Strategy, "failure_kind": correction.FailureKind, "failure_detail": correction.FailureDetail, "predicate_ids": correction.PredicateIDs, "evidence_refs": correction.EvidenceRefs,
	}
	packet := dispatchWorkerPacket(workID, "execution", "attempt:fresh-"+workID)
	packet["inputs"].(map[string]any)["correction"] = correctionPayload
	packetPayload, err = json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	fieldsPayload, err = json.Marshal(map[string]any{"attempt_id": "attempt:fresh-" + workID, "worker_packet": json.RawMessage(packetPayload)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = invokeWorkflowActionForCD0059(ctx, t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: 12, ActionID: "dispatch_worker", Payload: fieldsPayload, SessionWorktree: dispatchSessionWorktree(t, s, workID),
		Actor: owner, AcceptedInputsDigest: "sha256:" + strings.Repeat("c", 64), IdempotencyIdentity: "dispatch-valid:" + workID, OperationID: "dispatch-valid:" + workID,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "dispatch-valid:" + workID, RequestID: "request:dispatch-valid:" + workID, ContractDigest: testManifestDigest, Now: time.Unix(6, 0).UTC(),
	}); err != nil {
		t.Fatalf("dispatch with current correction context: %v", err)
	}
	if pin, err = ReadWorkPin(ctx, s, workID); err != nil {
		t.Fatal(err)
	} else if pin.Correction != nil {
		t.Fatalf("fresh dispatch did not consume correction context: %#v", pin.Correction)
	}
	if _, _, err = WorkflowActionDefinitionFor(ctx, s, BuiltinWorkflowRegistry(), workID, "supersede_contract"); err == nil {
		t.Fatal("contract correction remained available after a later dispatch consumed the rejection")
	}
}

func TestCorrectionDispatchClearsPinContextForLaterLane(t *testing.T) {
	const workID = "work-c1b9adae6631673333fc94b8"
	ctx := context.Background()
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s, owner := fixture.store, fixture.owner
	defer s.Close()

	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	reviewer := acceptReturnRouteWorker(t, fixture, workID, ownerRef)
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:return-route","verdict_kind":"outcome_mismatch","incomparable_with_approved":true}`), 0, reviewer); err != nil {
		t.Fatalf("record non-ok verdict: %v", err)
	}
	correction := json.RawMessage(`{"diagnosis":"the repaired subject still reproduces the defect","strategy":"repeat the repair external effect","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:return-route-verification"]}`)
	if err := runIssue933OperatorAction(t, s, workID, "request_correction", correction, owner, fixture.operator); err != nil {
		t.Fatalf("request correction: %v", err)
	}

	pin := issue1013Pin(t, s, workID)
	if pin.Correction == nil {
		t.Fatal("work pin omitted the undischarged correction")
	}
	attemptID := "attempt:" + workID + ":corrective"
	validatorCorrection, err := workflowCorrectionContextForDispatch(ctx, s.db, workID, "repair", attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if !sameWorkflowCorrection(pin.Correction, validatorCorrection) {
		t.Fatalf("work pin correction=%#v, validator correction=%#v", pin.Correction, validatorCorrection)
	}
	withoutCorrection := dispatchWorkerPacket(workID, "repair", attemptID)
	packetPayload, err := json.Marshal(withoutCorrection)
	if err != nil {
		t.Fatal(err)
	}
	withoutCorrectionPayload := mustJSONValue(map[string]any{"attempt_id": attemptID, "worker_packet": json.RawMessage(packetPayload)})
	if _, err := invokeWorkflowActionForCD0059(context.Background(), t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: pin.Version, ActionID: "dispatch_worker", Payload: withoutCorrectionPayload, SessionWorktree: dispatchSessionWorktree(t, s, workID),
		Actor: owner, AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64), IdempotencyIdentity: "dispatch-without-correction:" + workID, OperationID: "dispatch-without-correction:" + workID,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "dispatch-without-correction:" + workID, RequestID: "request:dispatch-without-correction:" + workID, ContractDigest: testManifestDigest, Now: time.Unix(40, 0).UTC(),
	}); !hasFailureKind(err, KindInvalidPayload) {
		t.Fatalf("dispatch without correction error=%v, want invalid_payload", err)
	}

	validPayload := issue1013CorrectionDispatchPayload(t, workID, "repair", attemptID, pin.Correction)
	if _, err := invokeWorkflowActionForCD0059(ctx, t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: pin.Version, ActionID: "dispatch_worker", Payload: validPayload, SessionWorktree: dispatchSessionWorktree(t, s, workID),
		Actor: owner, AcceptedInputsDigest: "sha256:" + strings.Repeat("b", 64), IdempotencyIdentity: "dispatch-corrective:" + workID, OperationID: "dispatch-corrective:" + workID,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "dispatch-corrective:" + workID, RequestID: "request:dispatch-corrective:" + workID, ContractDigest: testManifestDigest, Now: time.Unix(41, 0).UTC(),
	}); err != nil {
		t.Fatalf("corrective dispatch: %v", err)
	}

	lane := BuiltinLaneDefinitions()[0]
	issue1013RecordWorkerDispatch(t, s, workID, attemptID)
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{{
		EventID: "worker-completed-corrective-" + workID,
		Kind:    WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID,
		Actor: "worker:test", OccurredAt: time.Unix(41, 1).UTC(), PayloadVersion: 1,
		Payload: mustJSONValue(WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion}),
	}}}); err != nil {
		t.Fatalf("complete corrective worker attempt: %v", err)
	}
	var attemptEpoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionStarted).Scan(&attemptEpoch); err != nil {
		t.Fatal(err)
	}
	acceptor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/correction-acceptor", SessionRef: "session/" + workID + "-acceptor", ActorClass: ActorAgent}
	if err := runVerdictActionAs(t, s, workID, "accept_worker_result", mustJSONValue(map[string]any{"attempt_id": attemptID, "attempt_epoch": attemptEpoch}), 0, acceptor); err != nil {
		t.Fatalf("accept corrective worker result: %v", err)
	}

	pin = issue1013Pin(t, s, workID)
	if pin.Correction != nil {
		t.Fatalf("work pin retained discharged correction: %#v", pin.Correction)
	}
	nextAttemptID := "attempt:" + workID + ":later"
	nextPacket, err := json.Marshal(dispatchWorkerPacket(workID, "repair", nextAttemptID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := invokeWorkflowActionForCD0059(ctx, t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: pin.Version, ActionID: "dispatch_worker", Payload: mustJSONValue(map[string]any{"attempt_id": nextAttemptID, "worker_packet": json.RawMessage(nextPacket)}), SessionWorktree: dispatchSessionWorktree(t, s, workID),
		Actor: owner, AcceptedInputsDigest: "sha256:" + strings.Repeat("c", 64), IdempotencyIdentity: "dispatch-later:" + workID, OperationID: "dispatch-later:" + workID,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "dispatch-later:" + workID, RequestID: "request:dispatch-later:" + workID, ContractDigest: testManifestDigest, Now: time.Unix(42, 0).UTC(),
	}); err != nil {
		t.Fatalf("dispatch after discharged correction: %v", err)
	}
}

func TestWorkflowFourthCorrectionDispatchRefusesWithApprovalRequired(t *testing.T) {
	const workID = "issue1013-fourth-correction-preflight"
	s, owner, pin := seedIssue1013EscalatedCorrection(t, workID)
	defer s.Close()

	attemptID := "attempt:" + workID + ":4"
	payload := issue1013CorrectionDispatchPayload(t, workID, "repair", attemptID, pin.Correction)
	base := WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: pin.Version, ActionID: "dispatch_worker", Payload: payload, SessionWorktree: dispatchSessionWorktree(t, s, workID),
		Actor: owner, AcceptedInputsDigest: "sha256:" + strings.Repeat("e", 64), IdempotencyIdentity: "issue1013-escalated-dispatch", OperationID: "issue1013-escalated-dispatch",
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "issue1013-escalated-dispatch", RequestID: "request:issue1013-escalated-dispatch", ContractDigest: testManifestDigest, Now: time.Unix(50, 0).UTC(),
	}
	_, err := invokeWorkflowActionForCD0059(context.Background(), t, s, base)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindApprovalRequired {
		t.Fatalf("fourth correction dispatch failure=%v, want approval_required", err)
	}

	// The wall admits exactly one dispatch behind the boundary-consumed
	// approval representation. Nothing else opens it.
	approved := base
	approved.EscalatedRetryApproved = true
	approved.IdempotencyIdentity = "issue1013-escalated-dispatch-approved"
	approved.OperationID = approved.IdempotencyIdentity
	approved.IdempotencyKey = approved.IdempotencyIdentity
	approved.RequestID = "request:" + approved.IdempotencyIdentity
	if _, err := invokeWorkflowActionForCD0059(context.Background(), t, s, approved); err != nil {
		t.Fatalf("approved fourth correction dispatch: %v", err)
	}
	var dispatched int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.worker_attempt_id')=?`, workID, WorkflowActionCompleted, attemptID).Scan(&dispatched); err != nil {
		t.Fatal(err)
	}
	if dispatched != 1 {
		t.Fatalf("approved escalated dispatch recorded %d completions for %s, want 1", dispatched, attemptID)
	}
}

func TestWorkPinEscalatedCorrectionRemovesDispatchIntent(t *testing.T) {
	const workID = "issue1013-escalated-correction-pin"
	s, _, pin := seedIssue1013EscalatedCorrection(t, workID)
	defer s.Close()

	if workflowCorrectionAttemptLimit != 3 {
		t.Fatalf("workflow correction attempt limit = %d, want 3", workflowCorrectionAttemptLimit)
	}
	if pin.Correction == nil || pin.Correction.AttemptCount != 3 || !pin.Correction.Escalated {
		t.Fatalf("correction = %#v, want three attempts and escalation", pin.Correction)
	}
	if issue1013HasIntent(pin, "dispatch_worker") {
		t.Fatalf("escalated correction retained dispatch_worker: %#v", pin.NextValidIntents)
	}
}

func seedIssue1013EscalatedCorrection(t *testing.T, workID string) (*Store, WorkflowActor, WorkPin) {
	t.Helper()
	s, owner, attemptID, _ := seedOldDefinitionWorker(t, workID)
	worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	workerEpoch := int64(1)
	for correctionAttempt := int64(1); correctionAttempt <= 3; correctionAttempt++ {
		version := readWorkVersion(t, s, workID)
		failWorkerAttempt(t, s, workID, attemptID)
		applyRecordWorkerFailureForTest(t, s, workID, owner, attemptID, workerEpoch, version, "issue1013-record-failure-"+workID+fmt.Sprint(correctionAttempt))
		pin := issue1013Pin(t, s, workID)
		issue1013StartRepair(t, s, workID, worker, pin.Version, workerEpoch+1)
		pin = issue1013Pin(t, s, workID)
		if correctionAttempt == 3 {
			return s, owner, pin
		}
		attemptID = "attempt:" + workID + ":" + fmt.Sprint(correctionAttempt+1)
		payload := issue1013CorrectionDispatchPayload(t, workID, "repair", attemptID, pin.Correction)
		if _, err := invokeWorkflowActionForCD0059(context.Background(), t, s, WorkflowActionExecutionRequest{
			WorkID: workID, ExpectedVersion: pin.Version, ActionID: "dispatch_worker", Payload: payload, SessionWorktree: dispatchSessionWorktree(t, s, workID),
			Actor: worker, AcceptedInputsDigest: "sha256:" + strings.Repeat("d", 64), IdempotencyIdentity: "issue1013-dispatch-" + attemptID, OperationID: "issue1013-dispatch-" + attemptID,
			PrincipalRef: worker.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "issue1013-dispatch-" + attemptID, RequestID: "request:issue1013-dispatch-" + attemptID, ContractDigest: testManifestDigest, Now: time.Unix(10+correctionAttempt, 0).UTC(),
		}); err != nil {
			t.Fatalf("dispatch correction attempt %d: %v", correctionAttempt+1, err)
		}
		issue1013RecordWorkerDispatch(t, s, workID, attemptID)
		workerEpoch += 2
	}
	t.Fatal("escalated correction fixture did not return")
	return nil, WorkflowActor{}, WorkPin{}
}

func issue1013RecordWorkerDispatch(t *testing.T, s *Store, workID, attemptID string) {
	t.Helper()
	lane := BuiltinLaneDefinitions()[0]
	dispatch := Event{EventID: "issue1013-worker-dispatch-" + workID + "-" + attemptID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(20, 0).UTC(), PayloadVersion: 2, Payload: mustJSONValue(WorkerDispatchedPayload{
		AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass,
		ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion,
	})}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{dispatch}}); err != nil {
		t.Fatalf("record worker dispatch %s: %v", attemptID, err)
	}
}

func issue1013StartRepair(t *testing.T, s *Store, workID string, owner WorkflowActor, expectedVersion, attemptEpoch int64) {
	t.Helper()
	actorRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	start := workflowEventWithActor("issue1013-start-"+workID+"-"+fmt.Sprint(attemptEpoch), WorkflowActionStarted, workID, actorRef, map[string]any{
		"work_id": workID, "expected_version": expectedVersion, "resulting_version": expectedVersion + 1, "step_id": "repair",
		"action_id": "start_repair", "attempt_epoch": attemptEpoch, "accepted_inputs_digest": "sha256:" + strings.Repeat("b", 64),
		"idempotency_identity": "issue1013-start:" + workID + ":" + fmt.Sprint(attemptEpoch), "actor_ref": actorRef,
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{start}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): expectedVersion}}); err != nil {
		t.Fatalf("start correction attempt %d: %v", attemptEpoch, err)
	}
}

func issue1013CorrectionDispatchPayload(t *testing.T, workID, stepID, attemptID string, correction *WorkflowCorrectionContext) json.RawMessage {
	t.Helper()
	packet := dispatchWorkerPacket(workID, stepID, attemptID)
	packet["inputs"].(map[string]any)["correction"] = map[string]any{
		"disposition": correction.Disposition, "attempt_count": correction.AttemptCount, "attempt_limit": correction.AttemptLimit, "escalated": correction.Escalated,
		"diagnosis": correction.Diagnosis, "strategy": correction.Strategy, "failure_kind": correction.FailureKind, "failure_detail": correction.FailureDetail, "predicate_ids": correction.PredicateIDs, "evidence_refs": correction.EvidenceRefs,
	}
	packetPayload, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	return mustJSONValue(map[string]any{"attempt_id": attemptID, "worker_packet": json.RawMessage(packetPayload)})
}

func issue1013Pin(t *testing.T, s *Store, workID string) WorkPin {
	t.Helper()
	pin, err := ReadWorkPin(context.Background(), s, workID)
	if err != nil {
		t.Fatalf("read work pin: %v", err)
	}
	return pin
}

func issue1013HasIntent(pin WorkPin, actionID string) bool {
	for _, intent := range pin.NextValidIntents {
		if intent.ActionID == actionID {
			return true
		}
	}
	return false
}

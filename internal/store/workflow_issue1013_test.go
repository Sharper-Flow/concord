package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

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
	const workID = "issue1013-late-verdict-preflight"
	s, owner := seedItemAtAcceptance(t, workID, false)
	reviewer := verdictReviewer(t, workID)
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

	failWorkerAttempt(t, s, workID, attemptID)
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
		"diagnosis": correction.Diagnosis, "strategy": correction.Strategy, "predicate_ids": correction.PredicateIDs, "evidence_refs": correction.EvidenceRefs,
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

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/payloadschema"
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
	result, err := applyWorkflowActionRawTx(ctx, tx, newFoldScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: 10, ActionID: "reject_worker_result", Payload: mustJSONValue(map[string]any{
			"attempt_id": attemptID, "attempt_epoch": 1, "diagnosis": "the result misses the boundary case", "strategy": "change the helper and add a test",
			"predicate_ids": []string{"predicate:primary"}, "evidence_refs": []string{"evidence:review"},
		}), Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("f", 64), IdempotencyIdentity: "reject:" + workID, OperationID: "reject:" + workID,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "reject:" + workID, RequestID: "request:reject:" + workID, ContractDigest: testManifestDigest, Now: time.Unix(4, 0).UTC(),
	})
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
	if marshaled, marshalErr := json.Marshal(pin); marshalErr != nil {
		t.Fatal(marshalErr)
	} else if validErr := payloadschema.Validate("work_pin", marshaled); validErr != nil {
		t.Fatalf("clean reject work pin does not satisfy the closed response schema: %v", validErr)
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

// An escalated correction keeps dispatch_worker visible, but only as the
// approval-gated route: the pin advertises the action under the escalated
// reason so an agent finds the CD-0148 approval wall instead of a dead end.
func TestWorkPinEscalatedCorrectionAdvertisesApprovalGatedRetry(t *testing.T) {
	t.Parallel()
	const workID = "issue1013-escalated-correction-pin"
	s, _, pin := seedIssue1013EscalatedCorrection(t, workID)
	defer s.Close()

	if workflowCorrectionAttemptLimit != 3 {
		t.Fatalf("workflow correction attempt limit = %d, want 3", workflowCorrectionAttemptLimit)
	}
	if pin.Correction == nil || pin.Correction.AttemptCount != 3 || !pin.Correction.Escalated {
		t.Fatalf("correction = %#v, want three attempts and escalation", pin.Correction)
	}
	advertised := false
	for _, intent := range pin.NextValidIntents {
		if intent.ActionID != "dispatch_worker" {
			continue
		}
		advertised = true
		if intent.ReasonCode != "escalated_retry_requires_approval" {
			t.Fatalf("escalated dispatch intent reason = %q, want escalated_retry_requires_approval", intent.ReasonCode)
		}
	}
	if !advertised {
		t.Fatalf("escalated correction hid dispatch_worker: %#v", pin.NextValidIntents)
	}
}

// CD-0164 D4 fixes the verification wall to the request population with a
// strict comparator: the request path records every correction the verdict
// admits while the dispatch wall arms only when the recorded count passes the
// limit. Each correction is one declared cycle — the reviewer records the
// outcome_mismatch verdict, then the correction request records through the
// same declared action — so the log holds four recorded requests with no
// synthetic events. Three recorded verification corrections keep dispatch
// approval-free; the fourth request arms the wall, and its dispatch faces the
// operator approval bound to the correction's attempt count.
func TestVerificationCorrectionWallArmsOnTheFourthRequest(t *testing.T) {
	t.Parallel()
	const workID = "verification-wall-fourth-request"
	ctx := context.Background()
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.implementation", "execution")
	ownerRef, err := WorkflowActorRef(fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	reviewer := acceptReturnRouteWorker(t, fixture, workID, ownerRef)
	if workflowCorrectionAttemptLimit != 3 {
		t.Fatalf("workflow correction attempt limit = %d, want 3", workflowCorrectionAttemptLimit)
	}
	// One declared worker delivery per cycle: the correction request returns
	// the workflow to execution, and the next verdict needs a fresh accepted
	// delivery to be recordable at its normal verification step. The loop's
	// acceptor is distinct from the verdict reviewer, so no actor evaluates
	// its own delivery.
	acceptor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/return-route-acceptor", SessionRef: "session/" + workID + "-acceptor", ActorClass: ActorAgent}
	runCorrectionWorkerLoop := func(cycle int64) {
		t.Helper()
		s := fixture.store
		lane := BuiltinLaneDefinitions()[0]
		attemptID := fmt.Sprintf("attempt:%s:%d", workID, cycle)
		if err := runVerdictActionAs(t, s, workID, "start_execution", json.RawMessage(`{}`), 0, fixture.owner); err != nil {
			t.Fatalf("cycle %d start_execution: %v", cycle, err)
		}
		var attemptEpoch int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='start_execution' ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionStarted).Scan(&attemptEpoch); err != nil {
			t.Fatal(err)
		}
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{{
			EventID: fmt.Sprintf("dispatch-%s-%d", workID, cycle), Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID,
			Actor: ownerRef, OccurredAt: time.Unix(30+cycle, 0).UTC(), PayloadVersion: 2,
			Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion}),
		}}}); err != nil {
			t.Fatal(err)
		}
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{{
			EventID: fmt.Sprintf("completed-%s-%d", workID, cycle), Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID,
			Actor: "worker:test", OccurredAt: time.Unix(31+cycle, 0).UTC(), PayloadVersion: 1,
			Payload: mustJSONValue(WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion}),
		}}}); err != nil {
			t.Fatal(err)
		}
		if err := runVerdictActionAs(t, s, workID, "accept_worker_result", json.RawMessage(`{"attempt_id":"`+attemptID+`","attempt_epoch":`+fmt.Sprint(attemptEpoch)+`}`), 0, acceptor); err != nil {
			t.Fatalf("cycle %d accept worker result: %v", cycle, err)
		}
		if err := runVerdictActionAs(t, s, workID, "start_refine", json.RawMessage(`{}`), 0, acceptor); err != nil {
			t.Fatalf("cycle %d start refinement: %v", cycle, err)
		}
		for _, deliveryStep := range []string{"refine", "delivery"} {
			if err := runVerdictActionAs(t, s, workID, "record_delivery", json.RawMessage(`{"delivery_artifact":"artifact:return-route-`+deliveryStep+`-`+fmt.Sprint(cycle)+`","delivery_state":"asserted"}`), 0, acceptor); err != nil {
				t.Fatalf("cycle %d record %s delivery: %v", cycle, deliveryStep, err)
			}
		}
	}
	recordCorrectionCycle := func(count int64) {
		t.Helper()
		if count > 1 {
			// The fixture's first delivery loop already ran, and each
			// correction request returns the workflow to execution, so every
			// later verdict needs one fresh declared delivery loop.
			runCorrectionWorkerLoop(count)
		}
		if err := runVerdictActionAs(t, fixture.store, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:return-route","verdict_kind":"outcome_mismatch","evaluation_evidence":["evidence:return-route-verification"],"incomparable_with_approved":true}`), 0, reviewer); err != nil {
			t.Fatalf("cycle %d mismatch verdict: %v", count, err)
		}
		payload := json.RawMessage(`{"diagnosis":"the delivered subject still fails","strategy":"repeat the external effect","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:return-route-verification"]}`)
		if err := runIssue933OperatorAction(t, fixture.store, workID, "request_correction", payload, fixture.owner, fixture.operator); err != nil {
			t.Fatalf("cycle %d correction request: %v", count, err)
		}
		pin, pinErr := ReadWorkPin(ctx, fixture.store, workID)
		if pinErr != nil {
			t.Fatal(pinErr)
		}
		if pin.Correction == nil || pin.Correction.Disposition != "verification" || pin.Correction.AttemptCount != count || pin.Correction.Escalated != (count > workflowCorrectionAttemptLimit) {
			t.Fatalf("verification correction after cycle %d = %#v, want count %d escalated=%v", count, pin.Correction, count, count > workflowCorrectionAttemptLimit)
		}
		binding, bindingErr := WorkflowFailedWorkerRetryBinding(ctx, fixture.store, workID)
		if bindingErr != nil {
			t.Fatal(bindingErr)
		}
		if count <= workflowCorrectionAttemptLimit && binding != nil {
			t.Fatalf("cycle %d minted a retry binding %+v, want none below the wall", count, binding)
		}
	}
	for count := int64(1); count <= 3; count++ {
		recordCorrectionCycle(count)
	}
	recordCorrectionCycle(4)
	binding, bindingErr := WorkflowFailedWorkerRetryBinding(ctx, fixture.store, workID)
	if bindingErr != nil {
		t.Fatal(bindingErr)
	}
	if binding == nil || binding.FailedAttemptID != "" || binding.FailedAttemptEpoch != 0 || binding.CorrectionAttempts != 4 || binding.ContractVersion != 1 {
		t.Fatalf("fourth request binding = %+v, want the correction-count binding", binding)
	}
	// The armed wall admits no fresh delivery, so no new verdict exists for a
	// fifth request to correct, and the escalated binding stays unchanged.
	payload := json.RawMessage(`{"diagnosis":"the delivered subject still fails","strategy":"repeat the external effect","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:return-route-verification"]}`)
	if err := runIssue933OperatorAction(t, fixture.store, workID, "request_correction", payload, fixture.owner, fixture.operator); err == nil {
		t.Fatal("fifth request_correction recorded while the escalation wall is armed")
	}
	after, afterErr := WorkflowFailedWorkerRetryBinding(ctx, fixture.store, workID)
	if afterErr != nil {
		t.Fatal(afterErr)
	}
	if after == nil || after.CorrectionAttempts != 4 {
		t.Fatalf("binding after refused fifth request = %+v, want correction attempts 4", after)
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

// completeStepSuccessorPayload is a typed successor whose outcome predicates
// repeat the predecessor's byte for byte. The complete-step cut must not
// depend on a predicate payload change: the route exists because durable
// evidence disproved the premise, not because the predicate shape moved. The
// required evidence names every kind the predecessor bound, so the successor
// refuses completion until each kind is freshly bound after the supersession.
func completeStepSuccessorPayload(workID string) json.RawMessage {
	binding := `{"domain_registry_content_hash":"sha256:` + strings.Repeat("b", 64) + `","home_domain_id":"root","affected_domain_ids":["root"],"domain_modifies":[],"domain_relation_modifies":[],"law_additions":[],"verification_obligations":[]}`
	return json.RawMessage(`{"contract_version":2,"premise":"the delivered subject the durable evidence names","outcome_predicates":[{"predicate_id":"predicate:return-route","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:return-route","immutable_subject_ref":"commit:` + workID + `","expected_result":"pass"}}],"required_evidence":["verification","review","artifact"],"route_conventions":["complete_step_correction"],"spec_mandate":[],"law_modifies":[],"rigor_class":"prototype_internal","supersede_reason":"durable evidence contradicted the approved premise","audit_evidence":["evidence:complete-correction-` + workID + `"],"architecture_binding":` + binding + `}`)
}

// seedCompleteStepCorrection drives a real pinned workflow instance to its
// completion step under an approved contract with a recorded healthy verdict,
// then records one same-work observation through the public generic operation
// route. The observation postdates the verdict, so the complete-step
// correction route's contradiction-record condition holds.
func seedCompleteStepCorrection(t *testing.T, workID, definitionRef, verdictStep, completeStep string) (workflowReturnRouteFixture, WorkflowActor) {
	t.Helper()
	fixture, reviewer := seedCompleteStep(t, workID, definitionRef, verdictStep, completeStep)
	recordCompleteStepContradictionObservation(t, fixture.store, workID, fixture.owner)
	return fixture, reviewer
}

// seedCompleteStep leaves the contradiction record unrecorded, so refusal
// fixtures can supply exactly the conditions under test.
func seedCompleteStep(t *testing.T, workID, definitionRef, verdictStep, completeStep string) (workflowReturnRouteFixture, WorkflowActor) {
	t.Helper()
	fixture := seedWorkflowReturnRouteFixture(t, workID, definitionRef, verdictStep)
	reviewer := completeCompleteStepReview(t, fixture, workID, completeStep)
	return fixture, reviewer
}

// completeCompleteStepReview records the reviewer actor, the healthy verdict
// under the active contract, and the premise confirmation that parks the
// instance on the pinned completion step, and returns the reviewer for later
// verdict actions.
func completeCompleteStepReview(t *testing.T, fixture workflowReturnRouteFixture, workID, completeStep string) WorkflowActor {
	t.Helper()
	s := fixture.store
	reviewer := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/reviewer", SessionRef: "session/" + workID + "-reviewer", ActorClass: ActorAgent}
	reviewerRef, err := WorkflowActorRef(reviewer)
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, workID)
	reviewerEvent := workflowEventWithActor("complete-correction-reviewer-"+workID, WorkflowActorRecorded, workID, reviewerRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"actor_ref": reviewerRef, "principal_ref": reviewer.PrincipalRef, "client_ref": reviewer.ClientRef,
		"agent_ref": reviewer.AgentRef, "session_ref": reviewer.SessionRef, "actor_class": "agent",
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{reviewerEvent}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("record the reviewer actor: %v", err)
	}
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:return-route","verdict_kind":"ok"}`), 0, reviewer); err != nil {
		t.Fatalf("record the healthy verdict: %v", err)
	}
	if err := runIssue933OperatorAction(t, s, workID, "confirm_premise", json.RawMessage(`{"contract_version":1}`), fixture.owner, fixture.operator); err != nil {
		t.Fatalf("confirm the premise: %v", err)
	}
	var step string
	if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	if step != completeStep {
		t.Fatalf("step before correction = %q, want %q", step, completeStep)
	}
	return reviewer
}

// recordCompleteStepContradictionObservation records the contradiction
// observation on the stranded work item through the public generic operation
// route, the route concord_work_define.observation_record drives. The pinned
// completion step declares no evidence-binding action, so workflow-scoped
// event families are unreachable there and the observation is the durable
// contradiction record a public route can produce.
func recordCompleteStepContradictionObservation(t *testing.T, s *Store, workID string, owner WorkflowActor) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"observation_id": "obs:" + strings.Repeat("c", 16),
		"statement":      "Durable evidence contradicts the approved contract premise at the completion step.",
		"refs":           []string{"work:" + workID},
		"tags":           []string{"correction"},
	})
	if err != nil {
		t.Fatal(err)
	}
	event := Event{EventID: "complete-correction-observation-" + workID, Kind: WorkObservationRecorded, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: owner.PrincipalRef, OccurredAt: time.Unix(50, 0).UTC(), PayloadVersion: 1, Payload: payload}
	version := readWorkVersion(t, s, workID)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("record the contradiction observation: %v", err)
	}
}

func completeStepInstancePin(t *testing.T, s *Store, workID string) (step, state, definitionRef string, definitionVersion int64, definitionDigest string) {
	t.Helper()
	if err := s.DatabaseForTesting().QueryRow(`SELECT current_step,instance_state,definition_ref,definition_version,definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&step, &state, &definitionRef, &definitionVersion, &definitionDigest); err != nil {
		t.Fatal(err)
	}
	return step, state, definitionRef, definitionVersion, definitionDigest
}

// The complete-step route admits operator-approved contract correction when
// the pinned completion step holds a nonterminal break-fix item with one
// active contract, no unsettled attempt, and a durable post-verdict
// contradiction observation. The supersession preserves predecessor
// contracts, verdicts, attempt history, and the pinned definition, returns
// the instance to repair, and cuts the historical verdicts off the
// successor's predicates, so fresh delivery and a fresh verdict are the only
// path back to completion (CD-0166).
func TestCompleteStepContractCorrectionReturnsBreakFixToRepair(t *testing.T) {
	t.Parallel()
	const workID = "complete-correction-breakfix"
	ctx := context.Background()
	fixture, reviewer := seedCompleteStepCorrection(t, workID, "workflow.break_fix", "verify", "complete")
	s := fixture.store

	_, action, err := WorkflowActionDefinitionFor(ctx, s, BuiltinWorkflowRegistry(), workID, "supersede_contract")
	if err != nil {
		t.Fatalf("discover contract correction at the complete step: %v", err)
	}
	if action.ID != "supersede_contract" || action.Approval != ActionApprovalRequired {
		t.Fatalf("correction action = %#v, want operator-approved supersession", action)
	}
	if err := issue1013Preflight(t, s, workID, "supersede_contract", completeStepSuccessorPayload(workID), fixture.owner); err != nil {
		t.Fatalf("read-only preflight refused the complete-step correction: %v", err)
	}
	pin := issue1013Pin(t, s, workID)
	if !issue1013HasIntent(pin, "supersede_contract") {
		t.Fatalf("work pin omitted the correction intent at the complete step: %#v", pin.NextValidIntents)
	}
	if marshaled, marshalErr := json.Marshal(pin); marshalErr != nil {
		t.Fatal(marshalErr)
	} else if validErr := payloadschema.Validate("work_pin", marshaled); validErr != nil {
		t.Fatalf("complete-step correction pin does not satisfy the closed response schema: %v", validErr)
	}

	var verdicts, predecessorPredicates, attempts int
	if err := s.DatabaseForTesting().QueryRow(`SELECT (SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?), (SELECT count(*) FROM workflow_contract_predicates WHERE work_id=? AND contract_version=1), (SELECT count(*) FROM worker_attempts WHERE work_id=?)`, workID, WorkflowVerdictRecorded, workID, workID).Scan(&verdicts, &predecessorPredicates, &attempts); err != nil {
		t.Fatal(err)
	}
	_, _, definitionRef, definitionVersion, definitionDigest := completeStepInstancePin(t, s, workID)

	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", completeStepSuccessorPayload(workID), fixture.owner, fixture.operator); err != nil {
		t.Fatalf("supersede the contract at the complete step: %v", err)
	}

	var activeCount, activeVersion, supersededBy int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&activeCount); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&activeVersion); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT superseded_by FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&supersededBy); err != nil {
		t.Fatal(err)
	}
	if activeCount != 1 || activeVersion != 2 || supersededBy != 2 {
		t.Fatalf("contracts after supersession: active=%d version=%d predecessor superseded_by=%d", activeCount, activeVersion, supersededBy)
	}
	var verdictsAfter, predecessorPredicatesAfter, attemptsAfter int
	if err := s.DatabaseForTesting().QueryRow(`SELECT (SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?), (SELECT count(*) FROM workflow_contract_predicates WHERE work_id=? AND contract_version=1), (SELECT count(*) FROM worker_attempts WHERE work_id=?)`, workID, WorkflowVerdictRecorded, workID, workID).Scan(&verdictsAfter, &predecessorPredicatesAfter, &attemptsAfter); err != nil {
		t.Fatal(err)
	}
	if verdictsAfter != verdicts || predecessorPredicatesAfter != predecessorPredicates || attemptsAfter != attempts {
		t.Fatalf("supersession disturbed history: verdicts %d->%d predecessor predicates %d->%d attempts %d->%d", verdicts, verdictsAfter, predecessorPredicates, predecessorPredicatesAfter, attempts, attemptsAfter)
	}
	step, state, refAfter, versionAfter, digestAfter := completeStepInstancePin(t, s, workID)
	if refAfter != definitionRef || versionAfter != definitionVersion || digestAfter != definitionDigest {
		t.Fatalf("pinned definition changed: %s@%d (%s) -> %s@%d (%s)", definitionRef, definitionVersion, definitionDigest, refAfter, versionAfter, digestAfter)
	}
	if step != "repair" || state != "running" {
		t.Fatalf("instance after correction: step=%q state=%q, want repair/running", step, state)
	}

	// Historical healthy verdicts never satisfy the successor: identical
	// predicate payloads carry no verdict across this supersession.
	satisfied, err := latestWorkflowVerdicts(ctx, s.db, workID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(satisfied) != 0 {
		t.Fatalf("historical verdicts satisfied the successor: %#v", satisfied)
	}
	cutoff, cutoffErr := workflowCompleteStepCorrectionEvidenceCutoff(ctx, s.db, workID, 2)
	if cutoffErr != nil {
		t.Fatal(cutoffErr)
	}
	if cutoff == 0 {
		t.Fatal("the corrected successor derived no evidence cutoff")
	}

	// The returned instance runs the ordinary route: fresh delivery through
	// the repair and refine external effects, a fresh verdict under the
	// successor, and only then the completion step again.
	workerRef, err := WorkflowActorRef(fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	issue1013StartRepair(t, s, workID, fixture.owner, readWorkVersion(t, s, workID), latestStepStartEpoch(t, s, workID, "repair")+1)
	epoch := latestStepStartEpoch(t, s, workID, "repair")
	deliverAt := func(stepID string, attemptEpoch int64) {
		t.Helper()
		version := readWorkVersion(t, s, workID)
		delivery := workflowEventWithActor("complete-correction-redelivery-"+workID+"-"+stepID, WorkflowActionCompleted, workID, workerRef, map[string]any{
			"work_id": workID, "expected_version": version, "resulting_version": version + 1,
			"step_id": stepID, "action_id": "record_delivery", "attempt_epoch": attemptEpoch,
			"delivery_artifact": "evidence:complete-correction-" + workID, "delivery_state": "asserted",
			"result_evidence_refs": []string{"evidence:complete-correction-" + workID}, "changed_refs": []string{workID},
			"actor_ref": workerRef,
		})
		if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{delivery}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
			t.Fatalf("record the fresh delivery at %s: %v", stepID, err)
		}
	}
	deliverAt("repair", epoch)
	deliverAt("refine", epoch+1)
	deliverAt("delivery", epoch+2)
	if step, _, _, _, _ := completeStepInstancePin(t, s, workID); step != "verify" {
		t.Fatalf("step after the fresh deliveries = %q, want verify", step)
	}

	// The successor inherits no predecessor evidence either. Every
	// predecessor binding is still on the log, yet the requirement scan cuts
	// them all: the outstanding set names every required kind, the premise
	// confirmation refuses, and completion refuses at its evidence clause
	// rather than reaching its verdict clause.
	pinned, pinnedOK := BuiltinWorkflowRegistry().Lookup(definitionRef, definitionVersion)
	if !pinnedOK {
		t.Fatalf("pinned definition %s@%d is not registered", definitionRef, definitionVersion)
	}
	outstanding, outstandingErr := outstandingWorkflowEvidenceRequirementsForWork(ctx, s.db, workID, pinned.Definition)
	if outstandingErr != nil {
		t.Fatal(outstandingErr)
	}
	if len(outstanding) != 3 {
		t.Fatalf("outstanding requirements with only predecessor evidence = %v, want verification, review, and artifact", outstanding)
	}
	for _, requirement := range outstanding {
		if requirement.Kind != "verification" && requirement.Kind != "review" && requirement.Kind != "artifact" {
			t.Fatalf("outstanding requirement = %#v, want a required evidence kind", requirement)
		}
	}
	var failure *Failure
	if err := runIssue933OperatorAction(t, s, workID, "confirm_premise", json.RawMessage(`{"contract_version":2}`), fixture.owner, fixture.operator); err == nil {
		t.Fatal("confirm_premise passed with only predecessor evidence")
	} else if !errors.As(err, &failure) || failure.Kind != KindMissingEvidence {
		t.Fatalf("confirm_premise with only predecessor evidence = %v, want missing-evidence", err)
	}
	operatorRef, operatorRefErr := WorkflowActorRef(fixture.operator)
	if operatorRefErr != nil {
		t.Fatal(operatorRefErr)
	}
	reviewerRef, reviewerRefErr := WorkflowActorRef(reviewer)
	if reviewerRefErr != nil {
		t.Fatal(reviewerRefErr)
	}
	completion := workflowEventWithActor("complete-correction-early-"+workID, WorkflowCompleted, workID, operatorRef, map[string]any{
		"work_id": workID, "expected_version": readWorkVersion(t, s, workID), "resulting_version": readWorkVersion(t, s, workID) + 1,
		"terminal_state": "completed", "final_verdict_kind": "ok", "verdict_actor_ref": reviewerRef, "premise_confirmed": true,
		"evidence_count": 3, "changed_refs_digest": "sha256:" + strings.Repeat("a", 64), "impact_verdict": "non-breaking",
	})
	completion.PayloadVersion = 2
	if err := CompleteWorkflow(ctx, s, completion); err == nil {
		t.Fatal("completion passed with only predecessor evidence")
	} else if !errors.As(err, &failure) || failure.Kind != KindMissingEvidence || failure.Clause != 1 {
		t.Fatalf("completion with only predecessor evidence = %v, want the clause-1 evidence refusal", err)
	}

	// The fresh verdict under the successor is born bound: its minted
	// bindings land after the cutoff, one per required kind, and they are the
	// fresh evidence the path back to completion requires.
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":2,"predicate_id":"predicate:return-route","verdict_kind":"ok"}`), 0, reviewer); err != nil {
		t.Fatalf("record the fresh verdict under the successor: %v", err)
	}
	outstanding, outstandingErr = outstandingWorkflowEvidenceRequirementsForWork(ctx, s.db, workID, pinned.Definition)
	if outstandingErr != nil {
		t.Fatal(outstandingErr)
	}
	if len(outstanding) != 0 {
		t.Fatalf("outstanding requirements after the successor verdict = %v, want none", outstanding)
	}
	satisfied, err = latestWorkflowVerdicts(ctx, s.db, workID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(satisfied) != 1 || satisfied[0].ContractVersion != 2 {
		t.Fatalf("fresh verdicts = %#v, want one successor verdict", satisfied)
	}
	if err := runIssue933OperatorAction(t, s, workID, "confirm_premise", json.RawMessage(`{"contract_version":2}`), fixture.owner, fixture.operator); err != nil {
		t.Fatalf("confirm the successor premise after the fresh bindings: %v", err)
	}
	if step, _, _, _, _ := completeStepInstancePin(t, s, workID); step != "complete" {
		t.Fatalf("step after the successor premise = %q, want complete", step)
	}
}

// A workflow instance that already recorded completion while the work item
// stayed nonterminal is the stranded regression shape. Correction at the
// complete step reopens it: the return sets the instance running on the
// external-effect step under the unchanged pinned definition.
func TestCompleteStepContractCorrectionReopensCompletedInstance(t *testing.T) {
	t.Parallel()
	const workID = "complete-correction-reopen"
	fixture, _ := seedCompleteStepCorrection(t, workID, "workflow.break_fix", "verify", "complete")
	s := fixture.store
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE workflow_instances SET instance_state='completed' WHERE work_id=?; DELETE FROM fold_guard`, workID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := WorkflowActionDefinitionFor(context.Background(), s, BuiltinWorkflowRegistry(), workID, "supersede_contract"); err != nil {
		t.Fatalf("discovery refused the completed-instance correction: %v", err)
	}
	pin := issue1013Pin(t, s, workID)
	if len(pin.NextValidIntents) != 1 || pin.NextValidIntents[0].ActionID != "supersede_contract" {
		t.Fatalf("completed nonterminal instance intents = %#v, want only supersede_contract", pin.NextValidIntents)
	}
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", completeStepSuccessorPayload(workID), fixture.owner, fixture.operator); err != nil {
		t.Fatalf("supersede on the completed instance: %v", err)
	}
	step, state, _, _, _ := completeStepInstancePin(t, s, workID)
	if step != "repair" || state != "running" {
		t.Fatalf("instance after correction: step=%q state=%q, want repair/running", step, state)
	}
}

// TestCompleteStepContractCorrectionReturnsImplementationToExecution proves
// the implementation mapping of the same route: the release step returns to
// execution under the unchanged pinned definition.
func TestCompleteStepContractCorrectionReturnsImplementationToExecution(t *testing.T) {
	t.Parallel()
	const workID = "complete-correction-implementation"
	fixture, _ := seedCompleteStepCorrection(t, workID, "workflow.implementation", "acceptance", "release")
	s := fixture.store
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", completeStepSuccessorPayload(workID), fixture.owner, fixture.operator); err != nil {
		t.Fatalf("supersede the contract at the release step: %v", err)
	}
	step, state, _, _, _ := completeStepInstancePin(t, s, workID)
	if step != "execution" || state != "running" {
		t.Fatalf("instance after correction: step=%q state=%q, want execution/running", step, state)
	}
}

// Every complete-step refusal fires before any effect: missing durable
// contradiction evidence, an unsettled worker attempt, a terminal work item,
// an unsupported workflow shape, a missing operator approval, and a stale
// work version.
func TestCompleteStepContractCorrectionRefusals(t *testing.T) {
	t.Parallel()

	t.Run("missing contradiction evidence", func(t *testing.T) {
		t.Parallel()
		const workID = "complete-correction-no-evidence"
		fixture, _ := seedCompleteStep(t, workID, "workflow.break_fix", "verify", "complete")
		err := issue1013Preflight(t, fixture.store, workID, "supersede_contract", completeStepSuccessorPayload(workID), fixture.owner)
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || failure.Detail != "contract recovery is available only for a stale workflow contract" {
			t.Fatalf("preflight without contradiction evidence = %v, want the stale-contract refusal", err)
		}
		if _, _, err := WorkflowActionDefinitionFor(context.Background(), fixture.store, BuiltinWorkflowRegistry(), workID, "supersede_contract"); err == nil {
			t.Fatal("discovery admitted correction without contradiction evidence")
		}
	})

	t.Run("observation before the verdict", func(t *testing.T) {
		t.Parallel()
		const workID = "complete-correction-pre-verdict-observation"
		fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "verify")
		recordCompleteStepContradictionObservation(t, fixture.store, workID, fixture.owner)
		completeCompleteStepReview(t, fixture, workID, "complete")
		err := issue1013Preflight(t, fixture.store, workID, "supersede_contract", completeStepSuccessorPayload(workID), fixture.owner)
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || failure.Detail != "contract recovery is available only for a stale workflow contract" {
			t.Fatalf("preflight with only a pre-verdict observation = %v, want the stale-contract refusal", err)
		}
	})

	t.Run("unsettled worker attempt", func(t *testing.T) {
		t.Parallel()
		const workID = "complete-correction-live-attempt"
		fixture, _ := seedCompleteStepCorrection(t, workID, "workflow.break_fix", "verify", "complete")
		db := fixture.store.DatabaseForTesting()
		if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO worker_attempts(work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,dispatched_at) VALUES(?, 'attempt:unsettled', 'lane:implement', 1, ?, 'work_transition', 'model/test', '1.0', '1.0', 'dispatched', '2026-09-22T00:00:00Z'); DELETE FROM fold_guard`, workID, "sha256:"+strings.Repeat("a", 64)); err != nil {
			t.Fatal(err)
		}
		err := issue1013Preflight(t, fixture.store, workID, "supersede_contract", completeStepSuccessorPayload(workID), fixture.owner)
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation {
			t.Fatalf("preflight with a live attempt = %v, want refusal", err)
		}
	})

	t.Run("completed report without disposition", func(t *testing.T) {
		t.Parallel()
		const workID = "complete-correction-undisposed-report"
		const attemptID = "attempt:undisposed"
		fixture, _ := seedCompleteStepCorrection(t, workID, "workflow.break_fix", "verify", "complete")
		db := fixture.store.DatabaseForTesting()
		if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO worker_attempts(work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,dispatched_at,completed_at) VALUES(?, ?, 'lane:implement', 1, ?, 'work_transition', 'model/test', '1.0', '1.0', 'completed', '2026-09-22T00:00:00Z', '2026-09-22T00:00:01Z'); DELETE FROM fold_guard`, workID, attemptID, "sha256:"+strings.Repeat("a", 64)); err != nil {
			t.Fatal(err)
		}
		err := issue1013Preflight(t, fixture.store, workID, "supersede_contract", completeStepSuccessorPayload(workID), fixture.owner)
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation {
			t.Fatalf("preflight with an undisposed completed report = %v, want refusal", err)
		}
		if _, _, err := WorkflowActionDefinitionFor(context.Background(), fixture.store, BuiltinWorkflowRegistry(), workID, "supersede_contract"); err == nil {
			t.Fatal("discovery admitted correction with an undisposed completed report")
		}
		if _, err := db.Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?)`, "completed-report-disposition", WorkflowActionCompleted, SubjectWorkItem, workID, fixture.owner.PrincipalRef, "2026-09-23T00:00:00Z", 1, `{"action_id":"accept_worker_result","worker_attempt_id":"attempt:undisposed"}`); err != nil {
			t.Fatal(err)
		}
		unsettled, err := workflowUnsettledWorkerAttempt(context.Background(), fixture.store.db, workID, "test")
		if err != nil || unsettled {
			t.Fatalf("accepted report remains unsettled: unsettled=%t err=%v", unsettled, err)
		}
	})

	t.Run("terminal work item", func(t *testing.T) {
		t.Parallel()
		const workID = "complete-correction-terminal"
		fixture, _ := seedCompleteStepCorrection(t, workID, "workflow.break_fix", "verify", "complete")
		if _, err := fixture.store.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE work_items SET lifecycle='completed' WHERE id=?; DELETE FROM fold_guard`, workID); err != nil {
			t.Fatal(err)
		}
		_, _, err := WorkflowActionDefinitionFor(context.Background(), fixture.store, BuiltinWorkflowRegistry(), workID, "supersede_contract")
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || failure.Detail != "contract recovery is unavailable for terminal work" {
			t.Fatalf("discovery on a terminal item = %v, want the terminal-work refusal", err)
		}
	})

	t.Run("unsupported workflow shape", func(t *testing.T) {
		t.Parallel()
		const workID = "complete-correction-shape"
		s, owner, _ := seedItemAtAcceptance(t, workID, false)
		reviewer := verdictReviewer(t, s, workID)
		if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:primary","verdict_kind":"ok"}`), 0, reviewer); err != nil {
			t.Fatalf("record the verdict: %v", err)
		}
		if err := runIssue933OperatorAction(t, s, workID, "confirm_premise", json.RawMessage(`{"contract_version":1}`), owner, operatorVerdictActor(t, workID)); err != nil {
			t.Fatalf("confirm the premise: %v", err)
		}
		recordCompleteStepContradictionObservation(t, s, workID, owner)
		err := issue1013Preflight(t, s, workID, "supersede_contract", completeStepSuccessorPayload(workID), owner)
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || failure.Detail != "contract recovery is available only for a stale workflow contract" {
			t.Fatalf("preflight on the static-analysis shape = %v, want the shape refusal", err)
		}
	})

	t.Run("missing operator approval", func(t *testing.T) {
		t.Parallel()
		const workID = "complete-correction-no-approval"
		fixture, _ := seedCompleteStepCorrection(t, workID, "workflow.break_fix", "verify", "complete")
		version := verdictItemVersion(t, fixture.store, workID)
		tx, err := fixture.store.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		operationID := "complete-correction-no-approval"
		_, err = applyWorkflowActionRawTx(context.Background(), tx, newFoldScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
			WorkID: workID, ExpectedVersion: version, ActionID: "supersede_contract", Payload: completeStepSuccessorPayload(workID), Actor: fixture.owner,
			AcceptedInputsDigest: "sha256:" + strings.Repeat("f", 64), IdempotencyIdentity: operationID, OperationID: operationID,
			PrincipalRef: fixture.owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID, RequestID: "request:" + operationID,
			ContractDigest: testManifestDigest, Now: time.Unix(30, 0).UTC(),
		})
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindApprovalRequired {
			t.Fatalf("unapproved correction = %v, want approval_required", err)
		}
	})

	t.Run("stale work version", func(t *testing.T) {
		t.Parallel()
		const workID = "complete-correction-stale-version"
		fixture, _ := seedCompleteStepCorrection(t, workID, "workflow.break_fix", "verify", "complete")
		version := verdictItemVersion(t, fixture.store, workID)
		tx, err := fixture.store.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		operationID := "complete-correction-stale-version"
		_, err = applyWorkflowActionRawTx(context.Background(), tx, newFoldScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
			WorkID: workID, ExpectedVersion: version + 1, ActionID: "supersede_contract", Payload: completeStepSuccessorPayload(workID), Actor: fixture.owner, OperatorActor: &fixture.operator,
			AcceptedInputsDigest: "sha256:" + strings.Repeat("f", 64), IdempotencyIdentity: operationID, OperationID: operationID,
			PrincipalRef: fixture.owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID, RequestID: "request:" + operationID,
			ContractDigest: testManifestDigest, Now: time.Unix(31, 0).UTC(),
		})
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindVersionConflict {
			t.Fatalf("stale-version correction = %v, want version_conflict", err)
		}
	})

	t.Run("missing declared supersession event", func(t *testing.T) {
		t.Parallel()
		const workID = "complete-correction-missing-supersession"
		fixture, _ := seedCompleteStepCorrection(t, workID, "workflow.break_fix", "verify", "complete")
		if _, err := fixture.store.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE workflow_contracts SET route_conventions='["complete_step_correction"]' WHERE work_id=? AND contract_version=1; DELETE FROM fold_guard`, workID); err != nil {
			t.Fatal(err)
		}
		_, err := workflowCompleteStepCorrectionEvidenceCutoff(context.Background(), fixture.store.db, workID, 1)
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvariantViolation {
			t.Fatalf("missing declared supersession = %v, want invariant_violation", err)
		}
	})
}

func issue1013HasIntent(pin WorkPin, actionID string) bool {
	for _, intent := range pin.NextValidIntents {
		if intent.ActionID == actionID {
			return true
		}
	}
	return false
}

// The fold must refuse correction refs the response schema cannot carry:
// the correction pin serializes predicate_ids as ids (no slashes) and
// evidence_refs as whitespace-free references. Input rules and fold checks
// accept values those types forbid, so a reject can commit and then fail
// its own closed response schema, which the caller sees as a
// malformed_response after the effect.
func TestRejectWorkerResultRefusesSchemaBreakingCorrectionRefs(t *testing.T) {
	const workID = "reject-correction-refs-charset"
	s, _, owner, attemptID := seedCompletedWorkerAtExecution(t, workID)
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,1,'rejected worker contract','internal_sqlite','["verification"]','[]','now',?,'[]','[]',1,'prototype_internal'); INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES(?,1,'predicate:primary',0,'check','{"kind":"check","check_ref":"check:workflow","immutable_subject_ref":"commit:old","expected_result":"pass"}'); DELETE FROM fold_guard`, workID, ownerRef, workID); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = applyWorkflowActionRawTx(ctx, tx, newFoldScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: 10, ActionID: "reject_worker_result", Payload: mustJSONValue(map[string]any{
			"attempt_id": attemptID, "attempt_epoch": 1, "diagnosis": "the result misses the boundary case", "strategy": "change the helper and add a test",
			"predicate_ids": []string{"predicate:primary/slash"}, "evidence_refs": []string{"evidence:review"},
		}), Actor: owner, EvidenceRefs: []string{"evidence:mutation envelope locator with spaces and a length far beyond the one hundred twenty eight byte response bound"},
		AcceptedInputsDigest: "sha256:" + strings.Repeat("f", 64), IdempotencyIdentity: "rejectcharset:" + workID, OperationID: "rejectcharset:" + workID,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "rejectcharset:" + workID, RequestID: "request:rejectcharset:" + workID, ContractDigest: testManifestDigest, Now: time.Unix(4, 0).UTC(),
	})
	if err == nil {
		err = tx.Commit()
		if err != nil {
			t.Fatal(err)
		}
		pin, pinErr := ReadWorkPin(ctx, s, workID)
		if pinErr != nil {
			t.Fatal(pinErr)
		}
		if pin.Correction == nil {
			t.Fatal("fold accepted the reject but the pin carries no correction context")
		}
		marshaled, marshalErr := json.Marshal(pin)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if validErr := payloadschema.Validate("work_pin", marshaled); validErr != nil {
			t.Fatalf("fold committed a reject whose work pin fails the closed response schema (caller sees malformed_response): %v", validErr)
		}
		t.Fatal("fold accepted correction values without any schema disagreement to report")
	}
	tx.Rollback()
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidPayload || !strings.Contains(failure.Detail, "correction") {
		t.Fatalf("reject failure=%v, want an invalid-payload refusal naming the correction values", err)
	}
}

// failWorkerAttemptWithKind records a worker.failed event with an explicit
// failure kind. The counting journeys use it to prove the attempt bound
// consumes every dispatch whatever the attempt returned.
func failWorkerAttemptWithKind(t *testing.T, s *Store, workID, attemptID, failureKind, detail string) {
	t.Helper()
	fail := Event{EventID: "failed-" + workID + "-" + attemptID, Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(WorkerFailedPayload{AttemptID: attemptID, ReadbackModel: preferredModelForLane(BuiltinLaneDefinitions()[0]), FailureKind: failureKind, Detail: detail})}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{fail}}); err != nil {
		t.Fatal(err)
	}
}

// correctionCountingJourney drives one correction round against the counting
// projection: record the failure of attemptID at attemptEpoch, start a fresh
// repair, and when nextAttemptID is not empty dispatch it with the current
// correction context. It returns the pin after the fresh start, while the
// round's failure still holds the open correction context.
func correctionCountingJourney(t *testing.T, s *Store, workID string, owner, worker WorkflowActor, attemptID, nextAttemptID string, attemptEpoch int64, operationID string) WorkPin {
	t.Helper()
	version := readWorkVersion(t, s, workID)
	failWorkerAttemptWithKind(t, s, workID, attemptID, WorkerFailureFallbackBlocked, "lane fallback blocked before the model ran")
	applyRecordWorkerFailureForTest(t, s, workID, owner, attemptID, attemptEpoch, version, operationID)
	pin := issue1013Pin(t, s, workID)
	issue1013StartRepair(t, s, workID, worker, pin.Version, attemptEpoch+1)
	pin = issue1013Pin(t, s, workID)
	if nextAttemptID == "" {
		return pin
	}
	payload := issue1013CorrectionDispatchPayload(t, workID, "repair", nextAttemptID, pin.Correction)
	key := operationID + "-dispatch"
	if _, err := invokeWorkflowActionForCD0059(context.Background(), t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: pin.Version, ActionID: "dispatch_worker", Payload: payload, SessionWorktree: dispatchSessionWorktree(t, s, workID),
		Actor: worker, AcceptedInputsDigest: "sha256:" + strings.Repeat("d", 64), IdempotencyIdentity: key, OperationID: key,
		PrincipalRef: worker.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: key, RequestID: "request:" + key, ContractDigest: testManifestDigest, Now: time.Unix(10+attemptEpoch, 0).UTC(),
	}); err != nil {
		t.Fatalf("dispatch corrective attempt %s: %v", nextAttemptID, err)
	}
	issue1013RecordWorkerDispatch(t, s, workID, nextAttemptID)
	return pin
}

// The bound counts every dispatched attempt since the last accepted result
// (CD-0164), whatever the attempt returned. fallback_blocked is an
// infrastructure failure kind: the lane never returned a judgeable result,
// and each dispatch still consumes the bound. The third such failure
// escalates and removes dispatch_worker.
func TestInfrastructureFailureKindConsumesCorrectionAttemptBound(t *testing.T) {
	const workID = "correction-count-infra"
	s, owner, attemptID, _ := seedOldDefinitionWorker(t, workID)
	defer s.Close()
	worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}

	pin := correctionCountingJourney(t, s, workID, owner, worker, attemptID, "attempt:"+workID+":2", 1, "infra-count-1")
	if pin.Correction == nil || pin.Correction.AttemptCount != 1 || pin.Correction.Escalated || pin.Correction.FailureKind != WorkerFailureFallbackBlocked {
		t.Fatalf("correction after one infrastructure failure = %#v, want one counted attempt", pin.Correction)
	}

	pin = correctionCountingJourney(t, s, workID, owner, worker, "attempt:"+workID+":2", "attempt:"+workID+":3", 3, "infra-count-2")
	if pin.Correction == nil || pin.Correction.AttemptCount != 2 || pin.Correction.Escalated {
		t.Fatalf("correction after two infrastructure failures = %#v, want two counted attempts without escalation", pin.Correction)
	}
	if !issue1013HasIntent(pin, "dispatch_worker") {
		t.Fatalf("correction below the limit lost dispatch_worker: %#v", pin.NextValidIntents)
	}

	pin = correctionCountingJourney(t, s, workID, owner, worker, "attempt:"+workID+":3", "", 5, "infra-count-3")
	if pin.Correction == nil || pin.Correction.AttemptCount != 3 || !pin.Correction.Escalated {
		t.Fatalf("correction after three infrastructure failures = %#v, want three counted attempts and escalation", pin.Correction)
	}
	// CD-0173: the escalated pin advertises the retry route under the
	// approval-gated reason instead of hiding it.
	if !issue1013HasIntent(pin, "dispatch_worker") {
		t.Fatalf("escalated correction hid dispatch_worker: %#v", pin.NextValidIntents)
	}
	for _, intent := range pin.NextValidIntents {
		if intent.ActionID == "dispatch_worker" && intent.ReasonCode != "escalated_retry_requires_approval" {
			t.Fatalf("escalated dispatch intent reason = %q, want escalated_retry_requires_approval", intent.ReasonCode)
		}
	}
	if workflowCorrectionAttemptLimit != 3 {
		t.Fatalf("workflow correction attempt limit = %d, want 3", workflowCorrectionAttemptLimit)
	}

	payload := issue1013CorrectionDispatchPayload(t, workID, "repair", "attempt:"+workID+":4", pin.Correction)
	_, err := invokeWorkflowActionForCD0059(context.Background(), t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: pin.Version, ActionID: "dispatch_worker", Payload: payload, SessionWorktree: dispatchSessionWorktree(t, s, workID),
		Actor: worker, AcceptedInputsDigest: "sha256:" + strings.Repeat("e", 64), IdempotencyIdentity: "infra-count-4", OperationID: "infra-count-4",
		PrincipalRef: worker.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "infra-count-4", RequestID: "request:infra-count-4", ContractDigest: testManifestDigest, Now: time.Unix(30, 0).UTC(),
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindApprovalRequired {
		t.Fatalf("fourth dispatch after infrastructure failures error=%v, want approval_required", err)
	}
}

// latestStepStartEpoch reads the newest workflow action start epoch the step
// recorded, so record payloads can bind the exact epoch the fold will check.
func latestStepStartEpoch(t *testing.T, s *Store, workID, stepID string) int64 {
	t.Helper()
	var epoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(MAX(json_extract(payload,'$.attempt_epoch')),0) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.step_id')=?`, workID, WorkflowActionStarted, stepID).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	return epoch
}

// dispatchCountingAttempt dispatches one worker attempt on the named step and
// records the worker.dispatched event, with the correction context when one
// is open and a bare packet when none is.
func dispatchCountingAttempt(t *testing.T, s *Store, workID, stepID, attemptID string, correction *WorkflowCorrectionContext, actor WorkflowActor, expectedVersion int64, key string) {
	t.Helper()
	var payload json.RawMessage
	if correction == nil {
		packetPayload, err := json.Marshal(dispatchWorkerPacket(workID, stepID, attemptID))
		if err != nil {
			t.Fatal(err)
		}
		payload = mustJSONValue(map[string]any{"attempt_id": attemptID, "worker_packet": json.RawMessage(packetPayload)})
	} else {
		payload = issue1013CorrectionDispatchPayload(t, workID, stepID, attemptID, correction)
	}
	if _, err := invokeWorkflowActionForCD0059(context.Background(), t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: expectedVersion, ActionID: "dispatch_worker", Payload: payload, SessionWorktree: dispatchSessionWorktree(t, s, workID),
		Actor: actor, AcceptedInputsDigest: "sha256:" + strings.Repeat("d", 64), IdempotencyIdentity: key, OperationID: key,
		PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: key, RequestID: "request:" + key, ContractDigest: testManifestDigest, Now: time.Unix(10, 0).UTC(),
	}); err != nil {
		t.Fatalf("dispatch attempt %s: %v", attemptID, err)
	}
	issue1013RecordWorkerDispatch(t, s, workID, attemptID)
}

// accept_worker_result is the reset condition (CD-0164): the counting window
// opens after the last accepted result, so a fresh infrastructure failure on
// the next attempt starts a new sequence at one instead of inheriting the
// consumed bound.
func TestAcceptedWorkerResultResetsCorrectionAttemptCount(t *testing.T) {
	const workID = "correction-count-accept-reset"
	ctx := context.Background()
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s, owner := fixture.store, fixture.owner
	defer s.Close()
	lane := BuiltinLaneDefinitions()[0]
	worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	workerRef, err := WorkflowActorRef(worker)
	if err != nil {
		t.Fatal(err)
	}
	workerVersion := verdictItemVersion(t, s, workID)
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{workflowEventWithActor("reset-count-worker-"+workID, WorkflowActorRecorded, workID, workerRef, map[string]any{
		"work_id": workID, "expected_version": workerVersion, "resulting_version": workerVersion + 1,
		"actor_ref": workerRef, "principal_ref": worker.PrincipalRef, "client_ref": worker.ClientRef,
		"agent_ref": worker.AgentRef, "session_ref": worker.SessionRef, "actor_class": "agent",
	})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): workerVersion}}); err != nil {
		t.Fatal(err)
	}
	acceptor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/correction-acceptor", SessionRef: "session/" + workID + "-acceptor", ActorClass: ActorAgent}

	attempt1 := "attempt:" + workID + ":1"
	dispatchCountingAttempt(t, s, workID, "repair", attempt1, nil, worker, issue1013Pin(t, s, workID).Version, "reset-dispatch-1")
	failWorkerAttemptWithKind(t, s, workID, attempt1, WorkerFailureFallbackBlocked, "lane fallback blocked before the model ran")
	applyRecordWorkerFailureForTest(t, s, workID, owner, attempt1, latestStepStartEpoch(t, s, workID, "repair"), readWorkVersion(t, s, workID), "reset-count-1")
	pin := issue1013Pin(t, s, workID)
	if pin.Correction == nil || pin.Correction.AttemptCount != 1 || pin.Correction.Escalated || pin.Correction.FailureKind != WorkerFailureFallbackBlocked {
		t.Fatalf("correction after one infrastructure failure = %#v, want one counted attempt", pin.Correction)
	}

	attempt2 := "attempt:" + workID + ":2"
	issue1013StartRepair(t, s, workID, worker, pin.Version, latestStepStartEpoch(t, s, workID, "repair")+1)
	pin = issue1013Pin(t, s, workID)
	dispatchCountingAttempt(t, s, workID, "repair", attempt2, pin.Correction, worker, pin.Version, "reset-dispatch-2")
	failWorkerAttemptWithKind(t, s, workID, attempt2, WorkerFailureFallbackBlocked, "lane fallback blocked before the model ran")
	applyRecordWorkerFailureForTest(t, s, workID, owner, attempt2, latestStepStartEpoch(t, s, workID, "repair"), readWorkVersion(t, s, workID), "reset-count-2")
	pin = issue1013Pin(t, s, workID)
	if pin.Correction == nil || pin.Correction.AttemptCount != 2 || pin.Correction.Escalated {
		t.Fatalf("correction after two infrastructure failures = %#v, want two counted attempts", pin.Correction)
	}

	attempt3 := "attempt:" + workID + ":3"
	issue1013StartRepair(t, s, workID, worker, pin.Version, latestStepStartEpoch(t, s, workID, "repair")+1)
	pin = issue1013Pin(t, s, workID)
	dispatchCountingAttempt(t, s, workID, "repair", attempt3, pin.Correction, worker, pin.Version, "reset-dispatch-3")
	completed := Event{EventID: "worker-completed-" + attempt3, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(16, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(WorkerCompletedPayload{AttemptID: attempt3, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion})}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{completed}}); err != nil {
		t.Fatalf("complete corrective worker attempt: %v", err)
	}
	if err := runVerdictActionAs(t, s, workID, "accept_worker_result", mustJSONValue(map[string]any{"attempt_id": attempt3, "attempt_epoch": latestStepStartEpoch(t, s, workID, "repair")}), 0, acceptor); err != nil {
		t.Fatalf("accept corrective worker result: %v", err)
	}
	pin = issue1013Pin(t, s, workID)
	if pin.Correction != nil {
		t.Fatalf("accepted result left correction context: %#v", pin.Correction)
	}

	attempt4 := "attempt:" + workID + ":4"
	dispatchCountingAttempt(t, s, workID, "refine", attempt4, nil, worker, pin.Version, "reset-dispatch-4")
	failWorkerAttemptWithKind(t, s, workID, attempt4, WorkerFailureFallbackBlocked, "lane fallback blocked before the model ran")
	applyRecordWorkerFailureForTest(t, s, workID, owner, attempt4, latestStepStartEpoch(t, s, workID, "refine"), readWorkVersion(t, s, workID), "reset-count-4")
	pin = issue1013Pin(t, s, workID)
	if pin.Correction == nil || pin.Correction.AttemptCount != 1 || pin.Correction.Escalated {
		t.Fatalf("correction after the accepted result = %#v, want one counted attempt in a fresh sequence", pin.Correction)
	}
}

// The counting window is scoped to the work item's attempt history, not to a
// contract version (CD-0164): operator-approved contract supersession keeps
// the counted dispatches, and the bound still escalates under the successor.
func TestCorrectionAttemptCountSurvivesContractSupersession(t *testing.T) {
	const workID = "correction-count-supersede"
	s, owner, attemptID, _ := seedOldDefinitionWorker(t, workID)
	defer s.Close()
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	seedIssue31DomainRegistry(t, s)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,1,'failed worker contract','internal_sqlite','["verification"]','[]','now',?,'[]','[]',1,'prototype_internal'); INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES(?,1,'predicate:primary',0,'check','{"kind":"check","check_ref":"check:workflow","immutable_subject_ref":"commit:old","expected_result":"pass"}'); DELETE FROM fold_guard`, workID, ownerRef, workID); err != nil {
		t.Fatal(err)
	}
	worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}

	correctionCountingJourney(t, s, workID, owner, worker, attemptID, "attempt:"+workID+":2", 1, "supersede-count-1")
	pin := correctionCountingJourney(t, s, workID, owner, worker, "attempt:"+workID+":2", "", 3, "supersede-count-2")
	if pin.Correction == nil || pin.Correction.AttemptCount != 2 || pin.Correction.Escalated {
		t.Fatalf("correction before supersession = %#v, want two counted attempts", pin.Correction)
	}

	successor := json.RawMessage(`{"contract_version":2,"premise":"corrected predicate subject","outcome_predicates":[{"predicate_id":"predicate:primary","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:workflow","immutable_subject_ref":"commit:e3d7c6e6","expected_result":"pass"}}],"required_evidence":["verification"],"route_conventions":[],"spec_mandate":[],"law_modifies":[],"rigor_class":"prototype_internal","architecture_binding":{"domain_registry_content_hash":"sha256:` + strings.Repeat("b", 64) + `","home_domain_id":"root","affected_domain_ids":["root"],"domain_modifies":[],"domain_relation_modifies":[],"law_additions":[],"verification_obligations":[]},"supersede_reason":"the pinned subject named an unrelated commit","audit_evidence":["evidence:issue1013"]}`)
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", successor, owner, operatorVerdictActor(t, workID)); err != nil {
		t.Fatalf("supersede the contract across the correction sequence: %v", err)
	}
	var activeVersion int
	if err := s.DatabaseForTesting().QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&activeVersion); err != nil {
		t.Fatal(err)
	}
	if activeVersion != 2 {
		t.Fatalf("active contract version = %d, want 2", activeVersion)
	}
	pin = issue1013Pin(t, s, workID)
	if pin.Correction == nil || pin.Correction.AttemptCount != 2 || pin.Correction.Escalated {
		t.Fatalf("correction across supersession = %#v, want the two pre-supersession dispatches still counted", pin.Correction)
	}

	correctiveAttempt := "attempt:" + workID + ":3"
	payload := issue1013CorrectionDispatchPayload(t, workID, "repair", correctiveAttempt, pin.Correction)
	if _, err := invokeWorkflowActionForCD0059(context.Background(), t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: pin.Version, ActionID: "dispatch_worker", Payload: payload, SessionWorktree: dispatchSessionWorktree(t, s, workID),
		Actor: worker, AcceptedInputsDigest: "sha256:" + strings.Repeat("d", 64), IdempotencyIdentity: "supersede-count-3", OperationID: "supersede-count-3",
		PrincipalRef: worker.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "supersede-count-3", RequestID: "request:supersede-count-3", ContractDigest: testManifestDigest, Now: time.Unix(24, 0).UTC(),
	}); err != nil {
		t.Fatalf("dispatch the corrective attempt under the successor contract: %v", err)
	}
	issue1013RecordWorkerDispatch(t, s, workID, correctiveAttempt)
	version := readWorkVersion(t, s, workID)
	failWorkerAttemptWithKind(t, s, workID, correctiveAttempt, WorkerFailureFallbackBlocked, "lane fallback blocked before the model ran")
	applyRecordWorkerFailureForTest(t, s, workID, owner, correctiveAttempt, 5, version, "supersede-count-3-record")
	pin = issue1013Pin(t, s, workID)
	if pin.Correction == nil || pin.Correction.AttemptCount != 3 || !pin.Correction.Escalated {
		t.Fatalf("correction under the successor contract = %#v, want three counted attempts and escalation", pin.Correction)
	}
	// CD-0173: the escalated pin keeps dispatch_worker visible as the
	// approval-gated route, also under a successor contract.
	if !issue1013HasIntent(pin, "dispatch_worker") {
		t.Fatalf("escalated correction under the successor hid dispatch_worker: %#v", pin.NextValidIntents)
	}
}

// The declared payload gate is the input surface that names the field: a
// predicate id outside the id charset must be refused by the payload
// declaration itself, before any correction-specific check runs.
func TestRejectPayloadDeclarationRefusesNonIDPredicateID(t *testing.T) {
	err := validateWorkflowActionPayload(WorkflowDefinition{}, "reject_worker_result", json.RawMessage(`{"predicate_ids":["predicate:primary/slash"]}`))
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidPayload || !strings.Contains(failure.Detail, `"predicate_ids"`) {
		t.Fatalf("payload gate failure=%v, want an invalid-payload refusal naming predicate_ids", err)
	}
}

// laterOrdinarySuccessorPayload is the typed successor a later ordinary
// supersession approves after the complete-step correction: identical
// predicate payloads, no reserved convention, version 3.
func laterOrdinarySuccessorPayload(workID string) json.RawMessage {
	binding := `{"domain_registry_content_hash":"sha256:` + strings.Repeat("b", 64) + `","home_domain_id":"root","affected_domain_ids":["root"],"domain_modifies":[],"domain_relation_modifies":[],"law_additions":[],"verification_obligations":[]}`
	return json.RawMessage(`{"contract_version":3,"premise":"the delivered subject the durable evidence names","outcome_predicates":[{"predicate_id":"predicate:return-route","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:return-route","immutable_subject_ref":"commit:` + workID + `","expected_result":"pass"}}],"required_evidence":["verification","review","artifact"],"route_conventions":[],"spec_mandate":[],"law_modifies":[],"rigor_class":"prototype_internal","architecture_binding":` + binding + `,"supersede_reason":"the corrected contract premise moved again","audit_evidence":["evidence:later-successor-` + workID + `"]}`)
}

// TestCompleteStepCorrectionCutoffPersistsAcrossLaterSuccessor proves the
// correction cut survives the route marker. A later ordinary supersession of
// the corrected contract inherits neither the pre-correction healthy verdict
// nor the pre-correction evidence bindings, however identical the predicate
// payloads remain: the cutoff is ancestry state, not a property of the active
// contract row (CD-0172 D4).
func TestCompleteStepCorrectionCutoffPersistsAcrossLaterSuccessor(t *testing.T) {
	t.Parallel()
	const workID = "complete-correction-cutoff-persists"
	ctx := context.Background()
	fixture, _ := seedCompleteStepCorrection(t, workID, "workflow.break_fix", "verify", "complete")
	s := fixture.store

	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", completeStepSuccessorPayload(workID), fixture.owner, fixture.operator); err != nil {
		t.Fatalf("supersede the contract at the complete step: %v", err)
	}
	cutoffV2, cutoffErr := workflowCompleteStepCorrectionEvidenceCutoff(ctx, s.db, workID, 2)
	if cutoffErr != nil {
		t.Fatal(cutoffErr)
	}
	if cutoffV2 == 0 {
		t.Fatal("the corrected successor derived no evidence cutoff")
	}

	// A later ordinary supersession at the returned repair step retires the
	// corrected contract without the reserved convention.
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", laterOrdinarySuccessorPayload(workID), fixture.owner, fixture.operator); err != nil {
		t.Fatalf("ordinary supersession of the corrected contract: %v", err)
	}
	var activeVersion int
	if err := s.DatabaseForTesting().QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&activeVersion); err != nil {
		t.Fatal(err)
	}
	if activeVersion != 3 {
		t.Fatalf("active contract version = %d, want 3", activeVersion)
	}

	cutoffV3, cutoffErr := workflowCompleteStepCorrectionEvidenceCutoff(ctx, s.db, workID, 3)
	if cutoffErr != nil {
		t.Fatal(cutoffErr)
	}
	if cutoffV3 != cutoffV2 {
		t.Fatalf("cutoff moved across the ordinary successor: %d -> %d", cutoffV2, cutoffV3)
	}

	// The pre-correction healthy verdict satisfies nothing: the successor's
	// predicate history is byte-identical, yet no verdict is admitted.
	satisfied, err := latestWorkflowVerdicts(ctx, s.db, workID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(satisfied) != 0 {
		t.Fatalf("ordinary successor inherited pre-correction verdicts: %#v", satisfied)
	}

	// The pre-correction bindings satisfy nothing either: every kind the
	// predecessor bound stays outstanding under the ordinary successor.
	pinned, pinnedOK := completeStepPinnedDefinition(t, s, workID)
	if !pinnedOK {
		t.Fatal("pinned break-fix definition is not registered")
	}
	outstanding, outstandingErr := outstandingWorkflowEvidenceRequirementsForWork(ctx, s.db, workID, pinned.Definition)
	if outstandingErr != nil {
		t.Fatal(outstandingErr)
	}
	if len(outstanding) != 3 {
		t.Fatalf("outstanding requirements under the ordinary successor = %v, want verification, review, and artifact", outstanding)
	}
}

func completeStepPinnedDefinition(t *testing.T, s *Store, workID string) (RegisteredDefinition, bool) {
	t.Helper()
	var definitionRef string
	var definitionVersion int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_version FROM workflow_instances WHERE work_id=?`, workID).Scan(&definitionRef, &definitionVersion); err != nil {
		t.Fatal(err)
	}
	return BuiltinWorkflowRegistry().Lookup(definitionRef, definitionVersion)
}

// staleLawSuccessorPayload mandates the accepted successor law: the composed
// successor derives its law revision pins from the mandate's current state, so
// the stale-law recovery finds the accepted successor pinned (CD-0041 D5).
func staleLawSuccessorPayload(workID string) json.RawMessage {
	binding := `{"domain_registry_content_hash":"sha256:` + strings.Repeat("b", 64) + `","home_domain_id":"root","affected_domain_ids":["root"],"domain_modifies":[],"domain_relation_modifies":[],"law_additions":[],"verification_obligations":[]}`
	return json.RawMessage(`{"contract_version":2,"premise":"the delivered subject under the successor law","outcome_predicates":[{"predicate_id":"predicate:return-route","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:return-route","immutable_subject_ref":"commit:` + workID + `","expected_result":"pass"}}],"required_evidence":["verification","review","artifact"],"route_conventions":["complete_step_correction"],"spec_mandate":["spec:two"],"law_modifies":[],"rigor_class":"prototype_internal","architecture_binding":` + binding + `,"supersede_reason":"the approved premise moved to the successor law","audit_evidence":["evidence:complete-correction-` + workID + `"]}`)
}

// TestCompleteStepCorrectionStaleLawStaysBehindTheGate proves the stale-law
// recovery route does not bypass the complete-step admission. With the active
// contract's mandated law superseded but no durable contradiction record,
// discovery and preflight refuse; the route opens only when the contradiction
// observation postdates the verdict (CD-0172 D1).
func TestCompleteStepCorrectionStaleLawStaysBehindTheGate(t *testing.T) {
	t.Parallel()
	const workID = "complete-correction-stale-law-gate"
	fixture, _ := seedCompleteStep(t, workID, "workflow.break_fix", "verify", "complete")
	s := fixture.store
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		UPDATE workflow_contracts SET spec_mandate='["spec:one"]' WHERE work_id=? AND contract_version=1;
		UPDATE law_subjects SET status='superseded' WHERE home_project_id='project' AND home_locator_id='workflow-law-locator' AND law_id='spec:one';
		INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','spec:two','spec','accepted','docs/spec-two.md','Synthetic successor law','sha256:`+strings.Repeat("b", 64)+`','test');
		INSERT INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid) VALUES('project','workflow-law-locator','spec:two','supersedes','spec:one','test');
		INSERT INTO law_domain_homes(home_project_id,home_locator_id,law_id,product_id,domain_id,law_content_hash,scanned_commit_oid,product_wide_rationale) VALUES('project','workflow-law-locator','spec:two','product','root','sha256:`+strings.Repeat("b", 64)+`','test','');
		DELETE FROM fold_guard`, workID); err != nil {
		t.Fatal(err)
	}

	err := issue1013Preflight(t, s, workID, "supersede_contract", completeStepSuccessorPayload(workID), fixture.owner)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || failure.Detail != "contract recovery is available only for a stale workflow contract" {
		t.Fatalf("stale-law supersession without the contradiction record = %v, want the complete-step gate refusal", err)
	}
	if _, _, err := WorkflowActionDefinitionFor(context.Background(), s, BuiltinWorkflowRegistry(), workID, "supersede_contract"); err == nil {
		t.Fatal("discovery admitted the complete-step correction without the contradiction record")
	}

	// Recording the durable contradiction opens the shared gate; the stale-law
	// condition neither excuses the reserved convention nor the state gate,
	// and the successor still pins the accepted successor law.
	recordCompleteStepContradictionObservation(t, s, workID, fixture.owner)
	if err := issue1013Preflight(t, s, workID, "supersede_contract", staleLawSuccessorPayload(workID), fixture.owner); err != nil {
		t.Fatalf("preflight refused the complete-step correction behind a stale law: %v", err)
	}
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", staleLawSuccessorPayload(workID), fixture.owner, fixture.operator); err != nil {
		t.Fatalf("supersede behind a stale law through the gate: %v", err)
	}
	if step, state, _, _, _ := completeStepInstancePin(t, s, workID); step != "repair" || state != "running" {
		t.Fatalf("instance after correction: step=%q state=%q, want repair/running", step, state)
	}
}

// TestCompleteStepCorrectionDuplicateRecoveryUnreachable keeps the separate
// duplicate recovery on its declared earlier steps: at the pinned complete
// step a duplicate contract projection refuses discovery and preflight instead
// of admitting the recovery route (CD-0172 D1). The reachable duplicate
// recovery at an earlier step stays covered by
// TestDuplicateActiveContractsRecoverWithExactPredecessorSet.
func TestCompleteStepCorrectionDuplicateRecoveryUnreachable(t *testing.T) {
	t.Parallel()
	const workID = "complete-correction-duplicate"
	fixture, _ := seedCompleteStepCorrection(t, workID, "workflow.break_fix", "verify", "complete")
	s := fixture.store
	ownerRef, err := WorkflowActorRef(fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	version := verdictItemVersion(t, s, workID)
	legacyApproval, marshalErr := json.Marshal(map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"contract_version": 2, "premise": "legacy duplicate approval",
		"outcome_predicates": []map[string]any{{
			"predicate_id": "predicate:return-route", "ordinal": 0, "outcome_kind": "check",
			"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:duplicate", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"},
		}},
		"required_evidence": []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{},
		"law_modifies": []string{}, "law_revisions": []WorkflowLawRevision{}, "law_boundary_version": 1,
		"architecture_binding": WorkflowArchitectureBinding{
			DomainRegistryContentHash: "sha256:" + strings.Repeat("b", 64), HomeDomainID: "root", AffectedDomainIDs: []string{"root"},
			DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{},
			VerificationObligations: []WorkflowVerificationObligation{},
		},
		"rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
	})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?)`,
		"complete-correction-duplicate-approval", WorkflowContractApproved, SubjectWorkItem, workID, ownerRef, "2026-09-22T00:00:00Z", 3, legacyApproval); err != nil {
		t.Fatal(err)
	}
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	var activeCount int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&activeCount); err != nil {
		t.Fatal(err)
	}
	if activeCount != 2 {
		t.Fatalf("duplicate fixture active contracts = %d, want 2", activeCount)
	}
	pin := issue1013Pin(t, s, workID)
	if issue1013HasIntent(pin, "supersede_contract") {
		t.Fatalf("duplicate complete-step pin advertises refused supersession: %#v", pin.NextValidIntents)
	}

	if _, _, err := WorkflowActionDefinitionFor(context.Background(), s, BuiltinWorkflowRegistry(), workID, "supersede_contract"); err == nil {
		t.Fatal("discovery offered contract recovery at the complete step with a duplicate projection")
	}
	if err := issue1013Preflight(t, s, workID, "supersede_contract", completeStepSuccessorPayload(workID), fixture.owner); err == nil {
		t.Fatal("read-only preflight admitted the complete-step correction with a duplicate projection")
	}
	duplicateRecovery, payloadErr := json.Marshal(map[string]any{
		"contract_version": 3, "predecessor_contract_versions": []int64{1, 2}, "premise": "recovered premise",
		"outcome_predicates": []map[string]any{{
			"predicate_id": "predicate:return-route", "ordinal": 0, "outcome_kind": "check",
			"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:return-route", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"},
		}}, "required_evidence": []string{"verification"}, "route_conventions": []string{},
		"spec_mandate": []string{}, "law_modifies": []string{}, "law_revisions": []WorkflowLawRevision{}, "law_boundary_version": 1,
		"rigor_class":      "prototype_internal",
		"supersede_reason": "repair the duplicate projection", "audit_evidence": []string{"evidence:duplicate-recovery"},
	})
	if payloadErr != nil {
		t.Fatal(payloadErr)
	}
	err = InspectWorkflowActionAdmission(context.Background(), s, WorkflowActionPreflightRequest{
		WorkID: workID, ExpectedVersion: verdictItemVersion(t, s, workID), ActionID: "supersede_contract", Payload: duplicateRecovery, Actor: fixture.owner,
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvariantViolation || failure.Detail != "duplicate contract recovery is unavailable at the pinned complete step" {
		t.Fatalf("duplicate recovery admission at the complete step = %v, want the complete-step duplicate refusal", err)
	}
}

// TestCompleteStepCorrectionSurvivesRebuildFromLog proves the fold's return
// and its shared admission are replay-stable: rebuilding the log refolds the
// route supersession to the same instance state, active contract, and verdict
// cut (CD-0172 D1, D3).
func TestCompleteStepCorrectionSurvivesRebuildFromLog(t *testing.T) {
	t.Parallel()
	const workID = "complete-correction-replay"
	ctx := context.Background()
	fixture, _ := seedCompleteStepCorrection(t, workID, "workflow.break_fix", "verify", "complete")
	s := fixture.store
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", completeStepSuccessorPayload(workID), fixture.owner, fixture.operator); err != nil {
		t.Fatalf("supersede the contract at the complete step: %v", err)
	}
	step, state, definitionRef, definitionVersion, definitionDigest := completeStepInstancePin(t, s, workID)
	if step != "repair" || state != "running" {
		t.Fatalf("instance before rebuild: step=%q state=%q, want repair/running", step, state)
	}
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatalf("rebuild from log: %v", err)
	}
	stepAfter, stateAfter, refAfter, versionAfter, digestAfter := completeStepInstancePin(t, s, workID)
	if stepAfter != step || stateAfter != state || refAfter != definitionRef || versionAfter != definitionVersion || digestAfter != definitionDigest {
		t.Fatalf("replay moved the correction return: %s/%s %s@%d (%s) -> %s/%s %s@%d (%s)", step, state, definitionRef, definitionVersion, definitionDigest, stepAfter, stateAfter, refAfter, versionAfter, digestAfter)
	}
	var activeVersion int
	if err := s.DatabaseForTesting().QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&activeVersion); err != nil {
		t.Fatal(err)
	}
	if activeVersion != 2 {
		t.Fatalf("active contract version after rebuild = %d, want 2", activeVersion)
	}
	satisfied, err := latestWorkflowVerdicts(ctx, s.db, workID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(satisfied) != 0 {
		t.Fatalf("replayed successor inherited pre-correction verdicts: %#v", satisfied)
	}
}

// TestCompleteStepCorrectionHistoricalSupersessionReplays proves the fold
// tolerates a typed supersession recorded at the pinned complete step before
// the reserved convention existed: it replays exactly as it folded, with no
// route, no instance return, and no complete-step admission demanded of
// history (CD-0172 D2).
func TestCompleteStepCorrectionHistoricalSupersessionReplays(t *testing.T) {
	t.Parallel()
	const workID = "complete-correction-historical-replay"
	fixture, _ := seedCompleteStepCorrection(t, workID, "workflow.break_fix", "verify", "complete")
	s := fixture.store
	ownerRef, err := WorkflowActorRef(fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	version := verdictItemVersion(t, s, workID)
	historical, marshalErr := json.Marshal(map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"previous_contract_version": 1, "new_contract_version": 2,
		"supersede_reason": "stale-law recovery recorded before the reserved convention existed",
		"audit_evidence":   []string{"evidence:historical-stale-law"},
		"successor_contract": map[string]any{
			"contract_version": 2, "premise": "the delivered subject the durable evidence names",
			"outcome_predicates": []map[string]any{{"predicate_id": "predicate:return-route", "ordinal": 0, "outcome_kind": "check",
				"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:return-route", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"}}},
			"required_evidence": []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{},
			"law_modifies": []string{}, "law_revisions": []WorkflowLawRevision{}, "law_boundary_version": 1,
			"rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
			"architecture_binding": WorkflowArchitectureBinding{
				DomainRegistryContentHash: "sha256:" + strings.Repeat("b", 64), HomeDomainID: "root", AffectedDomainIDs: []string{"root"},
				DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{},
				VerificationObligations: []WorkflowVerificationObligation{},
			},
		},
	})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?)`,
		"complete-correction-historical-supersession", WorkflowContractSuperseded, SubjectWorkItem, workID, ownerRef, "2026-09-22T00:00:00Z", 2, historical); err != nil {
		t.Fatal(err)
	}
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("rebuild with the historical supersession: %v", err)
	}
	step, _, _, _, _ := completeStepInstancePin(t, s, workID)
	if step != "complete" {
		t.Fatalf("historical supersession moved the instance to %q, want complete", step)
	}
	var activeVersion int
	if err := s.DatabaseForTesting().QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&activeVersion); err != nil {
		t.Fatal(err)
	}
	if activeVersion != 2 {
		t.Fatalf("active contract version after the historical rebuild = %d, want 2", activeVersion)
	}
}

// seedOffShapeCompletedInstance parks a completed workflow instance on a
// verified break-fix step whose pinned shape does not carry the complete-step
// correction route, and supersedes the active contract's mandated law so the
// stale-law recovery is the admission under test.
func seedOffShapeCompletedInstance(t *testing.T, workID string) workflowReturnRouteFixture {
	t.Helper()
	fixture, _ := seedCompleteStep(t, workID, "workflow.break_fix", "verify", "complete")
	if _, err := fixture.store.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		UPDATE workflow_instances SET current_step='verify', instance_state='completed' WHERE work_id=?;
		UPDATE workflow_contracts SET spec_mandate='["spec:one"]' WHERE work_id=? AND contract_version=1;
		UPDATE law_subjects SET status='superseded' WHERE home_project_id='project' AND home_locator_id='workflow-law-locator' AND law_id='spec:one';
		INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','spec:two','spec','accepted','docs/spec-two.md','Synthetic successor law','sha256:`+strings.Repeat("b", 64)+`','test');
		INSERT INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid) VALUES('project','workflow-law-locator','spec:two','supersedes','spec:one','test');
		INSERT INTO law_domain_homes(home_project_id,home_locator_id,law_id,product_id,domain_id,law_content_hash,scanned_commit_oid,product_wide_rationale) VALUES('project','workflow-law-locator','spec:two','product','root','sha256:`+strings.Repeat("b", 64)+`','test','');
		DELETE FROM fold_guard`, workID, workID); err != nil {
		t.Fatal(err)
	}
	return fixture
}

// offShapeStaleLawSuccessorPayload is the stale-law successor without the
// reserved convention: the shape under test carries no complete-step
// correction route, so a shape-compliant successor declares no route.
func offShapeStaleLawSuccessorPayload(workID string) json.RawMessage {
	return json.RawMessage(strings.Replace(string(staleLawSuccessorPayload(workID)), `"route_conventions":["complete_step_correction"]`, `"route_conventions":[]`, 1))
}

// TestCompleteStepCorrectionReadOnlyPreflightRequiresReservedRoute pins the
// shared admission the read-only surface once skipped: a completed nonterminal
// instance at the pinned complete step whose successor omits the reserved
// complete_step_correction convention refuses at the read-only preflight
// exactly as the transaction preflight, guard, and fold refuse, and no effect
// records (CD-0172).
func TestCompleteStepCorrectionReadOnlyPreflightRequiresReservedRoute(t *testing.T) {
	t.Parallel()
	const workID = "complete-correction-missing-marker"
	fixture, _ := seedCompleteStepCorrection(t, workID, "workflow.break_fix", "verify", "complete")
	s := fixture.store
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE workflow_instances SET instance_state='completed' WHERE work_id=?; DELETE FROM fold_guard`, workID); err != nil {
		t.Fatal(err)
	}
	withoutRoute := json.RawMessage(strings.Replace(string(completeStepSuccessorPayload(workID)), `"route_conventions":["complete_step_correction"]`, `"route_conventions":[]`, 1))
	if !strings.Contains(string(withoutRoute), `"route_conventions":[]`) {
		t.Fatal("the missing-marker payload kept the reserved convention")
	}
	err := issue1013Preflight(t, s, workID, "supersede_contract", withoutRoute, fixture.owner)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || failure.Detail != "complete-step correction requires the reserved route convention complete_step_correction" {
		t.Fatalf("read-only preflight without the reserved route = %v, want the reserved-route refusal", err)
	}
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", withoutRoute, fixture.owner, fixture.operator); err == nil {
		t.Fatal("the owning action admitted a successor without the reserved route")
	}
	var activeVersion int
	if err := s.DatabaseForTesting().QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&activeVersion); err != nil {
		t.Fatal(err)
	}
	if activeVersion != 1 {
		t.Fatalf("active contract version = %d, want the predecessor 1", activeVersion)
	}
	if step, state, _, _, _ := completeStepInstancePin(t, s, workID); step != "complete" || state != "completed" {
		t.Fatalf("instance moved without effect: step=%q state=%q, want complete/completed", step, state)
	}
}

// TestCompletedInstanceOffShapeSupersessionRefusesBeforeEffect pins the
// completed-instance boundary every admission surface shares: a completed
// workflow instance whose pinned shape does not carry the complete-step
// correction route refuses supersession at discovery, the read-only preflight,
// the transaction admission, the owning mutation, and the live fold, while
// the same historical event still replays and the instance stays closed with
// its contract active (CD-0172).
func TestCompletedInstanceOffShapeSupersessionRefusesBeforeEffect(t *testing.T) {
	t.Parallel()

	const sharedDetail = "a completed workflow instance supersedes its contract only on the pinned complete-step correction shape"

	t.Run("discovery", func(t *testing.T) {
		t.Parallel()
		const workID = "complete-correction-offshape-discovery"
		fixture := seedOffShapeCompletedInstance(t, workID)
		_, _, err := WorkflowActionDefinitionFor(context.Background(), fixture.store, BuiltinWorkflowRegistry(), workID, "supersede_contract")
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || failure.Detail != sharedDetail {
			t.Fatalf("discovery on the off-shape completed instance = %v, want the shape refusal", err)
		}
	})

	t.Run("read-only preflight", func(t *testing.T) {
		t.Parallel()
		const workID = "complete-correction-offshape-preflight"
		fixture := seedOffShapeCompletedInstance(t, workID)
		err := issue1013Preflight(t, fixture.store, workID, "supersede_contract", offShapeStaleLawSuccessorPayload(workID), fixture.owner)
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || failure.Detail != sharedDetail {
			t.Fatalf("read-only preflight on the off-shape completed instance = %v, want the shape refusal", err)
		}
	})

	t.Run("transaction admission", func(t *testing.T) {
		t.Parallel()
		const workID = "complete-correction-offshape-admission"
		fixture := seedOffShapeCompletedInstance(t, workID)
		err := InspectWorkflowActionAdmission(context.Background(), fixture.store, WorkflowActionPreflightRequest{
			WorkID: workID, ActionID: "supersede_contract", Payload: offShapeStaleLawSuccessorPayload(workID), Actor: fixture.owner,
		})
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || failure.Detail != sharedDetail {
			t.Fatalf("transaction admission on the off-shape completed instance = %v, want the shape refusal", err)
		}
	})

	t.Run("mutation", func(t *testing.T) {
		t.Parallel()
		const workID = "complete-correction-offshape-mutation"
		fixture := seedOffShapeCompletedInstance(t, workID)
		err := runIssue933OperatorAction(t, fixture.store, workID, "supersede_contract", offShapeStaleLawSuccessorPayload(workID), fixture.owner, fixture.operator)
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || failure.Detail != sharedDetail {
			t.Fatalf("mutation on the off-shape completed instance = %v, want the shape refusal", err)
		}
		var activeVersion int
		if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&activeVersion); err != nil {
			t.Fatal(err)
		}
		if activeVersion != 1 {
			t.Fatalf("active contract version after the refused mutation = %d, want the predecessor 1", activeVersion)
		}
	})

	t.Run("live fold refuses and replay preserves", func(t *testing.T) {
		t.Parallel()
		const workID = "complete-correction-offshape-fold"
		fixture := seedOffShapeCompletedInstance(t, workID)
		successor := &workflowContractApprovedPayload{ContractVersion: 2, RouteConventions: []string{}}
		tx, txErr := fixture.store.DatabaseForTesting().BeginTx(context.Background(), nil)
		if txErr != nil {
			t.Fatal(txErr)
		}
		defer tx.Rollback()
		err := foldCompleteStepContractCorrectionTx(context.Background(), tx, workID, successor)
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvariantViolation || failure.Detail != sharedDetail {
			t.Fatalf("live fold of the off-shape completed supersession = %v, want the shape refusal", err)
		}
		if err := foldCompleteStepContractCorrectionTx(workflowReplayContext(context.Background()), tx, workID, successor); err != nil {
			t.Fatalf("replay of the historical off-shape supersession = %v, want replay preservation", err)
		}
	})
}

// TestCompletedInstanceDuplicateRecoveryStaysOffThePin keeps the duplicate
// recovery exception off a completed instance: the work pin and discovery
// refuse when the duplicated projection sits on a completed instance whose
// pinned shape does not carry the complete-step correction route (CD-0172).
func TestCompletedInstanceDuplicateRecoveryStaysOffThePin(t *testing.T) {
	t.Parallel()
	const workID = "complete-correction-duplicate-offshape"
	fixture, _ := seedCompleteStep(t, workID, "workflow.break_fix", "verify", "complete")
	s := fixture.store
	ownerRef, err := WorkflowActorRef(fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	version := verdictItemVersion(t, s, workID)
	legacyApproval, marshalErr := json.Marshal(map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"contract_version": 2, "premise": "legacy duplicate approval",
		"outcome_predicates": []map[string]any{{
			"predicate_id": "predicate:return-route", "ordinal": 0, "outcome_kind": "check",
			"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:duplicate", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"},
		}},
		"required_evidence": []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{},
		"law_modifies": []string{}, "law_revisions": []WorkflowLawRevision{}, "law_boundary_version": 1,
		"architecture_binding": WorkflowArchitectureBinding{
			DomainRegistryContentHash: "sha256:" + strings.Repeat("b", 64), HomeDomainID: "root", AffectedDomainIDs: []string{"root"},
			DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{},
			VerificationObligations: []WorkflowVerificationObligation{},
		},
		"rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
	})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?)`,
		"complete-correction-duplicate-offshape-approval", WorkflowContractApproved, SubjectWorkItem, workID, ownerRef, "2026-09-22T00:00:00Z", 3, legacyApproval); err != nil {
		t.Fatal(err)
	}
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	var activeCount int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&activeCount); err != nil {
		t.Fatal(err)
	}
	if activeCount != 2 {
		t.Fatalf("off-shape duplicate fixture active contracts = %d, want 2", activeCount)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE workflow_instances SET current_step='verify', instance_state='completed' WHERE work_id=?; DELETE FROM fold_guard`, workID); err != nil {
		t.Fatal(err)
	}
	pin := issue1013Pin(t, s, workID)
	if issue1013HasIntent(pin, "supersede_contract") {
		t.Fatalf("completed off-shape pin advertises duplicate recovery: %#v", pin.NextValidIntents)
	}
	if _, _, err := WorkflowActionDefinitionFor(context.Background(), s, BuiltinWorkflowRegistry(), workID, "supersede_contract"); err == nil {
		t.Fatal("discovery offered duplicate recovery on the completed off-shape instance")
	}
}

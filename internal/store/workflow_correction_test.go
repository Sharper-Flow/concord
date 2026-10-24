package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

func TestWorkflowCorrectionAdmissionRejectsNBSPReference(t *testing.T) {
	t.Parallel()
	const legacyReference = "evidence:\u00a0legacy"
	if fault := workflowCorrectionSchemaValuesFaultForAdmission(nil, []string{legacyReference}, nil); fault == "" {
		t.Fatal("new correction admission accepted a non-breaking-space reference")
	}
}

func TestSameWorkflowCorrectionIncludesFailureDetails(t *testing.T) {
	base := &WorkflowCorrectionContext{
		Disposition: "failed", Diagnosis: "diagnosis", Strategy: "strategy",
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

// Late criterion association agrees across discovery, the work pin, preflight
// and mutation. It closes when every criterion has applicable proof.
func TestIssue1013LateVerdictRecoveryPassesReadOnlyPreflight(t *testing.T) {
	t.Parallel()
	const workID = "issue1013-late-verdict-preflight"
	fixture, ref := seedLateCriterionAssociation(t, workID)
	s, owner := fixture.store, fixture.owner
	for _, predicate := range []string{"predicate:first", "predicate:second"} {
		payload := criterionAssociationPayload(ref, predicate)
		if !issue1013HasIntent(issue1013Pin(t, s, workID), "record_verdict") {
			t.Fatal("missing criterion proof has no late-association intent")
		}
		_, action, err := WorkflowActionDefinitionFor(t.Context(), s, BuiltinWorkflowRegistry(), workID, "record_verdict")
		if err != nil || action.ExecutionMode != ActionHold {
			t.Fatalf("late association discovery=%+v err=%v", action, err)
		}
		before := contractCorrectionEffectSnapshot(t, s, workID)
		if err := issue1013Preflight(t, s, workID, "record_verdict", payload, owner); err != nil {
			t.Fatalf("late association preflight: %v", err)
		}
		if after := contractCorrectionEffectSnapshot(t, s, workID); after != before {
			t.Fatal("read-only late association preflight changed state")
		}
		if err := runVerdictActionAs(t, s, workID, "record_verdict", payload, 0, owner); err != nil {
			t.Fatalf("late association: %v", err)
		}
		if got := currentStep(t, s, workID); got != "complete" {
			t.Fatalf("late association moved to %s", got)
		}
	}
	payload := criterionAssociationPayload(ref, "predicate:first")
	before := contractCorrectionEffectSnapshot(t, s, workID)
	if issue1013HasIntent(issue1013Pin(t, s, workID), "record_verdict") {
		t.Fatal("all criteria proved but the late route remains advertised")
	}
	for _, err := range []error{issue1013Preflight(t, s, workID, "record_verdict", payload, owner), runVerdictActionAs(t, s, workID, "record_verdict", payload, 0, owner)} {
		if !hasFailureKind(err, KindIllegalLifecycleTransition) {
			t.Fatalf("closed late route=%v, want current-step refusal", err)
		}
	}
	if after := contractCorrectionEffectSnapshot(t, s, workID); after != before {
		t.Fatal("closed-route refusals changed durable state")
	}
	if err := issue1013Preflight(t, s, workID, "supersede_contract", issue1013SuccessorContract(), owner); !hasFailureKind(err, KindInvalidOperation) {
		t.Fatalf("unrelated contract recovery became available: %v", err)
	}
}

// A live dispatch holds contract correction. Terminal ingestion opens the
// correction atomically, and another authorized dispatch closes that route
// without promoting the failed report into delivery authority.
func TestIssue1013ContractCorrectionAtFailedWorkerDispatch(t *testing.T) {
	const workID = "issue1013-failed-worker-correction"
	ctx := context.Background()
	fixture, attemptID, epoch := seedCurrentRepairWorker(t, workID)
	s := fixture.store
	job := *s2DispatchJobBinding(t, s, workID, attemptID)
	pin := issue1013Pin(t, s, workID)
	if issue1013HasIntent(pin, "record_delivery") {
		t.Fatalf("dispatched step offers record_delivery the fold refuses: %#v", pin.NextValidIntents)
	}
	if issue1013HasIntent(pin, "accept_worker_result") {
		t.Fatalf("unfinished attempt is advertised as an acceptable result: %#v", pin.NextValidIntents)
	}
	if _, _, err := WorkflowActionDefinitionFor(ctx, s, BuiltinWorkflowRegistry(), workID, "supersede_contract"); err == nil {
		t.Fatal("contract correction resolved while the worker attempt was live")
	}

	s2WorkerFailedAttributedLive(t, s, workID, attemptID, "the host observed the failed launch")
	_, action, err := WorkflowActionDefinitionFor(ctx, s, BuiltinWorkflowRegistry(), workID, "supersede_contract")
	if err != nil {
		t.Fatalf("resolve contract correction after the recorded failure: %v", err)
	}
	if action.ID != "supersede_contract" || action.Approval != ActionApprovalRequired {
		t.Fatalf("correction action = %#v, want operator-approved supersession", action)
	}
	pin = issue1013Pin(t, s, workID)
	if pin.Correction == nil || pin.Correction.FailedAttemptID != attemptID || pin.Correction.FailedAttemptEpoch != epoch {
		t.Fatalf("terminal ingestion lost its exact failure identity: %+v", pin.Correction)
	}
	source := *pin.Correction
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

	next := attemptID + ":retry"
	if err := dispatchWorkContextAttempt(t, fixture, issue1013CurrentDispatchPacket(t, fixture, next, job)); err != nil {
		t.Fatalf("authorize the fresh correction attempt: %v", err)
	}
	if window := reviewSubjectWindow(t, s, workID, next); window.AttemptEpoch <= epoch {
		t.Fatalf("fresh authorization epoch=%d, want greater than %d", window.AttemptEpoch, epoch)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{implementDispatchEvidenceEvent(workID, next, laneForID(t), job)}}); err != nil {
		t.Fatal(err)
	}
	before := contractCorrectionEffectSnapshot(t, s, workID)
	if _, _, err := WorkflowActionDefinitionFor(ctx, s, BuiltinWorkflowRegistry(), workID, "supersede_contract"); err == nil {
		t.Fatal("old failure reopened contract correction beside the live retry")
	}
	if after := contractCorrectionEffectSnapshot(t, s, workID); after != before {
		t.Fatal("closed correction discovery changed durable state")
	}
	if pin = issue1013Pin(t, s, workID); !sameWorkflowCorrection(pin.Correction, &source) || issue1013HasIntent(pin, "record_delivery") {
		t.Fatalf("materialized retry erased an unresolved job source or acquired delivery authority: %+v", pin)
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
		WorkID: workID, ExpectedVersion: 12, ActionID: "reject_worker_result", Payload: mustJSONValue(map[string]any{
			"attempt_id": attemptID, "attempt_epoch": 2, "diagnosis": "the result misses the boundary case", "strategy": "change the helper and add a test",
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
	if result.ResultingVersion != 13 {
		t.Fatalf("rejection result version=%d, want 13", result.ResultingVersion)
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
	start := workflowEventWithActor("restart-"+workID, WorkflowActionStarted, workID, workerRef, map[string]any{
		"work_id": workID, "expected_version": 13, "resulting_version": 14, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 3,
		"accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": "restart:" + workID, "actor_ref": workerRef,
	})
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{start}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 13}}); err != nil {
		t.Fatalf("start fresh correction attempt: %v", err)
	}
	packetPayload, err := json.Marshal(dispatchWorkerPacket(t, s, workID, "execution", "attempt:fresh-"+workID))
	if err != nil {
		t.Fatal(err)
	}
	fieldsPayload, err := json.Marshal(map[string]any{"attempt_id": "attempt:fresh-" + workID, "worker_packet": json.RawMessage(packetPayload)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = invokeWorkflowActionForCD0059(ctx, t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: 14, ActionID: "dispatch_worker", Payload: fieldsPayload, SessionWorktree: dispatchSessionWorktree(t, s, workID),
		Actor: owner, AcceptedInputsDigest: "sha256:" + strings.Repeat("b", 64), IdempotencyIdentity: "dispatch:" + workID, OperationID: "dispatch:" + workID,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "dispatch:" + workID, RequestID: "request:dispatch:" + workID, ContractDigest: testManifestDigest, Now: time.Unix(5, 0).UTC(),
	})
	if !hasFailureKind(err, KindInvalidPayload) {
		t.Fatalf("dispatch without correction context error=%v, want invalid_payload", err)
	}
	// Equal prose and counts from a different source event do not consume the
	// current correction: the source identity is part of the typed state.
	if pin.Correction.SourceEventID == "" || pin.Correction.SourceEventSeq < 1 {
		t.Fatalf("pin correction carries no source event identity: %#v", pin.Correction)
	}
	foreign := *pin.Correction
	foreign.SourceEventSeq++
	foreignPacket := dispatchWorkerPacket(t, s, workID, "execution", "attempt:fresh-"+workID)
	foreignPacket["inputs"].(map[string]any)["correction"] = foreign
	packetPayload, err = json.Marshal(foreignPacket)
	if err != nil {
		t.Fatal(err)
	}
	fieldsPayload, err = json.Marshal(map[string]any{"attempt_id": "attempt:fresh-" + workID, "worker_packet": json.RawMessage(packetPayload)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = invokeWorkflowActionForCD0059(ctx, t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: 14, ActionID: "dispatch_worker", Payload: fieldsPayload, SessionWorktree: dispatchSessionWorktree(t, s, workID),
		Actor: owner, AcceptedInputsDigest: "sha256:" + strings.Repeat("e", 64), IdempotencyIdentity: "dispatch-foreign:" + workID, OperationID: "dispatch-foreign:" + workID,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "dispatch-foreign:" + workID, RequestID: "request:dispatch-foreign:" + workID, ContractDigest: testManifestDigest, Now: time.Unix(5, 0).UTC(),
	})
	if !hasFailureKind(err, KindInvalidPayload) || !strings.Contains(err.Error(), "does not consume the current correction context") {
		t.Fatalf("dispatch with a foreign correction source error=%v, want the correction-consumption refusal", err)
	}
	packet := dispatchWorkerPacket(t, s, workID, "execution", "attempt:fresh-"+workID)
	packet["inputs"].(map[string]any)["correction"] = pin.Correction
	packetPayload, err = json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	fieldsPayload, err = json.Marshal(map[string]any{"attempt_id": "attempt:fresh-" + workID, "worker_packet": json.RawMessage(packetPayload)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = invokeWorkflowActionForCD0059(ctx, t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: 14, ActionID: "dispatch_worker", Payload: fieldsPayload, SessionWorktree: dispatchSessionWorktree(t, s, workID),
		Actor: owner, AcceptedInputsDigest: "sha256:" + strings.Repeat("c", 64), IdempotencyIdentity: "dispatch-valid:" + workID, OperationID: "dispatch-valid:" + workID,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "dispatch-valid:" + workID, RequestID: "request:dispatch-valid:" + workID, ContractDigest: testManifestDigest, Now: time.Unix(6, 0).UTC(),
	}); err != nil {
		t.Fatalf("dispatch with current correction context: %v", err)
	}
	// The dispatch intent alone consumes nothing: the fresh attempt must
	// materialize before the rejection record is consumed.
	if pin, err = ReadWorkPin(ctx, s, workID); err != nil {
		t.Fatal(err)
	} else if pin.Correction == nil {
		t.Fatalf("pin correction vanished before the fresh attempt materialized: %#v", pin.Correction)
	}
	issue1013RecordWorkerDispatch(t, s, workID, "attempt:fresh-"+workID)
	if pin, err = ReadWorkPin(ctx, s, workID); err != nil {
		t.Fatal(err)
	} else if pin.Correction != nil {
		t.Fatalf("materialized dispatch did not consume correction context: %#v", pin.Correction)
	}
	if _, _, err = WorkflowActionDefinitionFor(ctx, s, BuiltinWorkflowRegistry(), workID, "supersede_contract"); err == nil {
		t.Fatal("contract correction remained available after a later dispatch consumed the rejection")
	}
}

// An accepted correction_required Review opens the correction (CD-0242 D8).
// Its context binds the next dispatch at the producer step, a dispatch that
// omits it refuses, and the accepted corrective delivery discharges it, so a
// later lane dispatches with no correction.
func TestCorrectionDispatchClearsPinContextForLaterLane(t *testing.T) {
	const workID = "work-c1b9adae6631673333fc94b8"
	ctx := context.Background()
	j := pReturnReviewFixture(t, workID, "workflow.break_fix", "correction_required")
	s, owner := j.s, j.fixture.owner
	if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), owner); err != nil {
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
	job := reviewSubjectReadyRevision(t, j.fixture)
	version, digest := registeredLaneIdentity(t, "implement")
	withoutCorrection := joinPacketFor(t, s, workID, "repair", attemptID, "implement", version, digest)
	withoutCorrection["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, job, "implement")
	prepared, err := PrepareWorkerDispatchFreshness(ctx, s, PrepareWorkerDispatchRequest{WorkID: workID, SessionWorktree: j.repo})
	if err != nil {
		t.Fatal(err)
	}
	before := j.effects(t)
	_, err = dispatchJoinAttempt(WithWorkerDispatchPreparation(ctx, prepared), t, s, workID, readWorkVersion(t, s, workID), owner, withoutCorrection)
	if !hasFailureKind(err, KindInvalidPayload) || !strings.Contains(err.Error(), "correction") {
		t.Fatalf("dispatch without correction error=%v, want the correction invalid_payload refusal", err)
	}
	if j.effects(t) != before {
		t.Fatal("refused dispatch changed durable state")
	}

	pReturnProduce(t, j, "corrective")
	pin = issue1013Pin(t, s, workID)
	if pin.Correction != nil {
		t.Fatalf("work pin retained discharged correction: %#v", pin.Correction)
	}
	later := "attempt:" + workID + ":later"
	if correction, err := workflowCorrectionContextForDispatch(ctx, s.db, workID, "repair", later); err != nil || correction != nil {
		t.Fatalf("later lane correction=%#v err=%v, want none", correction, err)
	}
	if err := descendantDispatch(t, j, "review", later, reviewSubjectReadyRevision(t, j.fixture), nil); err != nil {
		t.Fatalf("later review dispatch after discharged correction: %v", err)
	}
}

// convergenceSupersedeBasis supersedes a worker-correction fixture's active
// contract through the real operator action with a complete architecture binding.
func convergenceSupersedeBasis(t *testing.T, s *Store, workID string, owner WorkflowActor) error {
	t.Helper()
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	seedIssue31DomainRegistry(t, s)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,1,'failed worker contract','internal_sqlite','["verification"]','[]','now',?,'[]','[]',1,'prototype_internal'); INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES(?,1,'predicate:primary',0,'check','{"kind":"check","check_ref":"check:workflow","immutable_subject_ref":"commit:old","expected_result":"pass"}'); DELETE FROM fold_guard`, workID, ownerRef, workID); err != nil {
		t.Fatal(err)
	}
	binding := `{"domain_registry_content_hash":"sha256:` + strings.Repeat("b", 64) + `","home_domain_id":"root","affected_domain_ids":["root"],"domain_modifies":[],"domain_relation_modifies":[],"law_additions":[],"verification_obligations":[]}`
	successor := json.RawMessage(`{"contract_version":2,"premise":"corrected predicate subject","outcome_predicates":[{"predicate_id":"predicate:primary","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:workflow","immutable_subject_ref":"commit:e3d7c6e6","expected_result":"pass"}}],"required_evidence":["verification"],"route_conventions":[],"spec_mandate":[],"law_modifies":[],"rigor_class":"prototype_internal","architecture_binding":` + binding + `,"supersede_reason":"the pinned subject named an unrelated commit","audit_evidence":["evidence:` + workID + `-supersede"]}`)
	return runIssue933OperatorAction(t, s, workID, "supersede_contract", successor, owner, operatorVerdictActor(t, workID))
}

// Repeated failed corrections retain their source without a retry budget.
func TestWorkflowFourthCorrectionDispatchRetainsLaunchWindow(t *testing.T) {
	const workID = "issue1013-fourth-correction-preflight"
	s, owner, pin := seedIssue1013EscalatedCorrection(t, workID)
	defer s.Close()
	fixture := workContextFixture{store: s, owner: owner, workID: workID}
	job := *s2DispatchJobBinding(t, s, workID, pin.Correction.FailedAttemptID)

	attemptID := "attempt:" + workID + ":4"
	if err := dispatchWorkContextAttempt(t, fixture, issue1013CurrentDispatchPacket(t, fixture, attemptID, job)); err != nil {
		t.Fatalf("fourth correction dispatch: %v", err)
	}
	// An unresolved launch outcome still owns the authorization window.
	fifth := "attempt:" + workID + ":5"
	packet := issue1013CurrentDispatchPacket(t, fixture, fifth, job)
	before := contractCorrectionEffectSnapshot(t, s, workID)
	if err := dispatchWorkContextAttempt(t, fixture, packet); !hasFailureKind(err, KindOperationConflict) {
		t.Fatalf("second dispatch beside pending authorization = %v, want operation_conflict", err)
	}
	if after := contractCorrectionEffectSnapshot(t, s, workID); after != before {
		t.Fatal("refused parallel launch changed durable state")
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{implementDispatchEvidenceEvent(workID, attemptID, laneForID(t), job)}}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkPinRepeatedCorrectionAdvertisesRetry(t *testing.T) {
	t.Parallel()
	const workID = "issue1013-escalated-correction-pin"
	s, _, pin := seedIssue1013EscalatedCorrection(t, workID)
	defer s.Close()

	if pin.Correction == nil || pin.Correction.FailedAttemptID != "attempt:"+workID+":3" {
		t.Fatalf("correction = %#v, want the latest failed attempt", pin.Correction)
	}
	advertised := false
	for _, intent := range pin.NextValidIntents {
		if intent.ActionID != "dispatch_worker" {
			continue
		}
		advertised = true
	}
	if !advertised {
		t.Fatalf("escalated correction hid dispatch_worker: %#v", pin.NextValidIntents)
	}
}

// Every correction consumes the accepted Review that admitted it. Each later
// correction needs a fresh corrective delivery and a fresh accepted
// correction_required Review; a consumed Review never admits another.
func TestRepeatedVerificationCorrectionRequiresFreshVerdict(t *testing.T) {
	t.Parallel()
	const workID = "verification-wall-fourth-request"
	j := pReturnReviewFixture(t, workID, "workflow.implementation", "correction_required")
	for cycle := 1; cycle <= 4; cycle++ {
		if cycle > 1 {
			pReturnProduce(t, j, fmt.Sprintf("correction-%d", cycle))
			acceptCurrentReviewForTest(t, j.fixture, j.repo, "correction_required")
		}
		if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner); err != nil {
			t.Fatalf("cycle %d correction request: %v", cycle, err)
		}
		reviewGateRequireStep(t, j.s, workID, "execution")
		pin := issue1013Pin(t, j.s, workID)
		if pin.Correction == nil || len(pin.Correction.PredicateIDs) != 1 || pin.Correction.PredicateIDs[0] != "predicate:return-route" {
			t.Fatalf("correction after cycle %d = %#v, want the current predicate source", cycle, pin.Correction)
		}
	}
	before := j.effects(t)
	if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner); err == nil {
		t.Fatal("request_correction reused a consumed Review")
	}
	if j.effects(t) != before {
		t.Fatal("refused correction changed durable state")
	}
}

// A dispatch intent whose worker attempt never materialized — the session died
// between the dispatch_worker action and the host worker-dispatch call — leaves
// the correction record live: the retry binding keeps naming the failed
// attempt, and a later dispatch consumes the record only once its worker
// attempt actually exists.
func TestHalfMaterializedDispatchLeavesTheCorrectionRecordLive(t *testing.T) {
	const workID = "correction-half-dispatch-live"
	fixture := seedCrossAttemptFixture(t, workID)
	s, attemptID := fixture.s, fixture.attemptR
	fixture.dispatch(t, attemptID, fixture.researchLane, fixture.dispatcher)
	epoch := crossAttemptDispatchEpoch(t, s, workID, attemptID)
	failed := s2WorkerFailedAttributedEvent(workID, attemptID, "failed:"+attemptID, preferredModelForLane(fixture.researchLane), WorkerFailureFallbackBlocked, "the host observed the failed launch", corpusNow)
	if err := s2WorkerFailedLive(t, s, failed); err != nil {
		t.Fatal(err)
	}

	interrupted := "attempt:" + workID + ":interrupted"
	pin := issue1013Pin(t, s, workID)
	if pin.Correction == nil || pin.Correction.FailedAttemptID != attemptID || pin.Correction.FailedAttemptEpoch != epoch {
		t.Fatalf("pin correction before the interrupted dispatch = %#v, want the failed attempt", pin.Correction)
	}
	source := *pin.Correction
	fixture.authorizeDispatch(t, interrupted, fixture.researchLane, fixture.dispatcher)

	pin = issue1013Pin(t, s, workID)
	if !sameWorkflowCorrection(pin.Correction, &source) {
		t.Fatalf("pin correction after the interrupted dispatch = %#v, want the live record", pin.Correction)
	}

	fixture.materializeDispatch(t, interrupted, fixture.researchLane, fixture.dispatcher)
	pin = issue1013Pin(t, s, workID)
	if pin.Correction != nil {
		t.Fatalf("materialized dispatch retained the correction: %#v", pin.Correction)
	}
}

func issue1013CurrentDispatchPacket(t *testing.T, fixture workContextFixture, attemptID string, job WorkerJobBinding) map[string]any {
	t.Helper()
	view, err := readWorkContextView(context.Background(), fixture.store.DatabaseForTesting(), fixture.workID)
	if err != nil || view == nil {
		t.Fatalf("current dispatch context=%+v err=%v", view, err)
	}
	packet := workContextDispatchPacket(t, fixture, attemptID, job, view)
	correction, err := workflowCorrectionContextForDispatch(context.Background(), fixture.store.DatabaseForTesting(), fixture.workID, "repair", attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if correction != nil {
		packet["inputs"].(map[string]any)["correction"] = correction
	}
	return packet
}

func seedIssue1013EscalatedCorrection(t *testing.T, workID string) (*Store, WorkflowActor, WorkPin) {
	t.Helper()
	fixture, attemptID, epoch := seedCurrentRepairWorker(t, workID)
	s, owner := fixture.store, fixture.owner
	job := *s2DispatchJobBinding(t, s, workID, attemptID)
	for round := 1; round <= 3; round++ {
		s2WorkerFailedAttributedLive(t, s, workID, attemptID, "the host observed a failed corrective launch")
		pin := issue1013Pin(t, s, workID)
		if pin.Correction == nil || pin.Correction.FailedAttemptID != attemptID || pin.Correction.FailedAttemptEpoch != epoch || pin.Correction.FailureKind != WorkerFailureFallbackBlocked || pin.Correction.SourceEventID == "" || pin.Correction.SourceEventSeq < 1 {
			t.Fatalf("failure %d lost its exact correction source: %+v", round, pin.Correction)
		}
		if !issue1013HasIntent(pin, "dispatch_worker") {
			t.Fatalf("failure %d hid ordinary retry: %+v", round, pin.NextValidIntents)
		}
		if round == 3 {
			return s, owner, pin
		}
		attemptID = fmt.Sprintf("attempt:%s:%d", workID, round+1)
		if err := dispatchWorkContextAttempt(t, fixture, issue1013CurrentDispatchPacket(t, fixture, attemptID, job)); err != nil {
			t.Fatalf("authorize corrective launch %d: %v", round+1, err)
		}
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{implementDispatchEvidenceEvent(workID, attemptID, laneForID(t), job)}}); err != nil {
			t.Fatal(err)
		}
		epoch = reviewSubjectWindow(t, s, workID, attemptID).AttemptEpoch
	}
	t.Fatal("repeated correction fixture did not return")
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
	// CD-0198 D3: on a derived-start pin the callable start is gone, and the
	// following dispatch opens the fresh epoch itself, so no raw start is
	// crafted here.
	entry, err := VerifyWorkflowInstanceDefinition(context.Background(), s, BuiltinWorkflowRegistry(), workID)
	if err != nil {
		t.Fatal(err)
	}
	if workflowDerivedStepStartActive(entry.Definition) {
		return
	}
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

func issue1013CorrectionDispatchPayload(t *testing.T, s *Store, workID, stepID, attemptID string, correction *WorkflowCorrectionContext) json.RawMessage {
	t.Helper()
	packet := dispatchWorkerPacket(t, s, workID, stepID, attemptID)
	packet["inputs"].(map[string]any)["correction"] = correction
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
	return json.RawMessage(`{"contract_version":2,"premise":"the delivered subject the durable evidence names","outcome_predicates":[{"predicate_id":"predicate:return-route","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:return-route","immutable_subject_ref":"commit:` + workID + `","expected_result":"pass"}}],"required_evidence":["verification","review","artifact"],"route_conventions":["complete_step_correction"],"spec_mandate":[],"law_modifies":[],"rigor_class":"prototype_internal","decision_domains":[],"supersede_reason":"durable evidence contradicted the approved premise","audit_evidence":["evidence:complete-correction-` + workID + `"],"architecture_binding":` + binding + `}`)
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
	effectStep := workflowUnhealthyVerdictRouteTarget(mustBuiltinDefinition(t, definitionRef).Definition, verdictStep)
	fixture := seedWorkflowReturnRouteFixture(t, workID, definitionRef, effectStep)
	reviewer := completeCompleteStepReview(t, fixture, workID, verdictStep, completeStep)
	return fixture, reviewer
}

// completeCompleteStepReview records the reviewer actor, the current proof
// association under the active contract, and the premise confirmation that
// parks the instance on the pinned completion step, and returns the reviewer
// for later verdict actions.
func completeCompleteStepReview(t *testing.T, fixture workflowReturnRouteFixture, workID, verdictStep, completeStep string) WorkflowActor {
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
	wf := workContextFixture{store: s, workID: workID, owner: fixture.owner, operator: fixture.operator}
	proof := qAssociateCurrentProofAt(t, wf, verdictStep, "", true)
	if err := pConfirmReceiptPremise(t, wf, proof.repo); err != nil {
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
	entry, entryErr := VerifyWorkflowInstanceDefinition(ctx, s, BuiltinWorkflowRegistry(), workID)
	if entryErr != nil {
		t.Fatal(entryErr)
	}
	if workflowDerivedStepStartActive(entry.Definition) {
		if err := runVerdictActionAs(t, s, workID, "checkpoint_repair", json.RawMessage(`{}`), 0, fixture.owner); err != nil {
			t.Fatalf("the correction re-entry derives the repair start: %v", err)
		}
	} else {
		issue1013StartRepair(t, s, workID, fixture.owner, readWorkVersion(t, s, workID), latestStepStartEpoch(t, s, workID, "repair")+1)
	}
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
	debtPin, err := ReadWorkPin(ctx, s, workID)
	if err != nil {
		t.Fatal(err)
	}
	requireWorkPinOutstandingKinds(t, &debtPin, []string{"artifact", "review", "verification"})
	wf := workContextFixture{store: s, workID: workID, owner: fixture.owner, operator: fixture.operator}
	var repo string
	if err := s.db.QueryRow(`SELECT path FROM worktree_entries WHERE set_id=? AND state='active'`, WorktreeSetID(workID)).Scan(&repo); err != nil {
		t.Fatal(err)
	}
	var failure *Failure
	if err := pConfirmReceiptPremise(t, wf, repo); err == nil {
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

	// Fresh proof under the successor is a new producer receipt on the
	// current candidate, a fresh accepted Review, its association, and fresh
	// required-kind bindings, all recorded after the cutoff: that is the fresh
	// evidence the path back to completion requires (CD-0172 D4, CD-0242 D8).
	journey := &candidateJourney{fixture: wf, s: s, workID: workID, repo: repo, acceptor: fixture.owner}
	run, runErr := s.VerifyWorktree(ctx, journey.request("q-proof-successor", nil))
	if runErr != nil || run.ExitCode != 0 {
		t.Fatalf("capture the successor's candidate receipt: %+v, %v", run, runErr)
	}
	qAcceptCriterionReview(t, wf, repo, run.OperationRef)
	if err := runVerdictActionAs(t, s, workID, "record_verdict", qAssociationPayload(t, acceptanceRecoveryFixture{store: s, workID: workID, receipt: run.OperationRef}), 0, fixture.owner); err != nil {
		t.Fatalf("associate the successor's fresh proof: %v", err)
	}
	journey.bindVerification(t, run)
	if err := runVerdictActionAs(t, s, workID, "bind_evidence", json.RawMessage(`{"evidence_kind":"artifact","immutable_subject_ref":"evidence:complete-correction-`+workID+`"}`), 0, fixture.owner); err != nil {
		t.Fatalf("bind the successor's fresh artifact: %v", err)
	}
	outstanding, outstandingErr = outstandingWorkflowEvidenceRequirementsForWork(ctx, s.db, workID, pinned.Definition)
	if outstandingErr != nil {
		t.Fatal(outstandingErr)
	}
	if len(outstanding) != 0 {
		t.Fatalf("outstanding requirements after the successor verdict = %v, want none", outstanding)
	}
	debtPin, err = ReadWorkPin(ctx, s, workID)
	if err != nil {
		t.Fatal(err)
	}
	requireWorkPinOutstandingKinds(t, &debtPin, []string{})
	satisfied, err = latestWorkflowVerdicts(ctx, s.db, workID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(satisfied) != 1 || satisfied[0].ContractVersion != 2 {
		t.Fatalf("fresh verdicts = %#v, want one successor verdict", satisfied)
	}
	if err := pConfirmReceiptPremise(t, wf, repo); err != nil {
		t.Fatalf("confirm the successor premise after the fresh bindings: %v", err)
	}
	if step, _, _, _, _ := completeStepInstancePin(t, s, workID); step != "complete" {
		t.Fatalf("step after the successor premise = %q, want complete", step)
	}
	if step, state, ref, version, digest := completeStepInstancePin(t, s, workID); step != "complete" || state != "running" || ref != definitionRef || version != definitionVersion || digest != definitionDigest {
		t.Fatalf("instance = %s/%s %s@%d (%s), want the corrected completion step under its original pin", step, state, ref, version, digest)
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
		fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
		recordCompleteStepContradictionObservation(t, fixture.store, workID, fixture.owner)
		completeCompleteStepReview(t, fixture, workID, "verify", "complete")
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
		unsettled, err := workflowUnsettledWorkerAttempt(context.Background(), fixture.store.db, workID, "test", 0)
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
		if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || failure.Detail != "contract recovery is available only for a stale workflow contract" {
			t.Fatalf("discovery on a terminal item = %v, want the shared admission's stale-contract refusal", err)
		}
	})

	t.Run("unsupported workflow shape", func(t *testing.T) {
		t.Parallel()
		// CD-0242 D14 removes retired definitions from the registry. The
		// unsupported family now refuses before a recovery shape can be pinned.
		_, err := BuiltinWorkflowDefinitionForRef("workflow.static_analysis")
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindDefinitionDigestMismatch || !strings.Contains(failure.Detail, "workflow type reference is not registered") {
			t.Fatalf("resolve the static-analysis shape = %v, want the retired-family refusal", err)
		}
	})

	// consequence_v1 classifies a complete-step correction that makes no
	// serious law change as routine record-keeping (CD-0230 D4): it records
	// without an operator approval. The serious-law-change consent stays
	// covered by the law-change suite.
	t.Run("routine authority without operator approval", func(t *testing.T) {
		t.Parallel()
		const workID = "complete-correction-no-approval"
		fixture, _ := seedCompleteStepCorrection(t, workID, "workflow.break_fix", "verify", "complete")
		version := readWorkVersion(t, fixture.store, workID)
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
		if err != nil {
			t.Fatalf("routine complete-step correction without approval = %v, want admitted", err)
		}
		classified, err := ClassifyWorkflowApproval(mustBuiltinDefinition(t, "workflow.break_fix").Definition, "supersede_contract", WorkflowApprovalFacts{})
		if err != nil || classified.Class != WorkflowApprovalClassRoutine || classified.Consent != WorkflowConsentNone {
			t.Fatalf("supersede_contract classification = %+v err=%v, want routine without consent", classified, err)
		}
	})

	t.Run("stale work version", func(t *testing.T) {
		t.Parallel()
		const workID = "complete-correction-stale-version"
		fixture, _ := seedCompleteStepCorrection(t, workID, "workflow.break_fix", "verify", "complete")
		version := readWorkVersion(t, fixture.store, workID)
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
		WorkID: workID, ExpectedVersion: 12, ActionID: "reject_worker_result", Payload: mustJSONValue(map[string]any{
			"attempt_id": attemptID, "attempt_epoch": 2, "diagnosis": "the result misses the boundary case", "strategy": "change the helper and add a test",
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

func TestInfrastructureFailuresRetainCorrectionSource(t *testing.T) {
	const workID = "correction-count-infra"
	s, owner, pin := seedIssue1013EscalatedCorrection(t, workID)
	defer s.Close()
	if pin.Correction == nil || pin.Correction.FailedAttemptID != "attempt:"+workID+":3" || pin.Correction.FailureKind != WorkerFailureFallbackBlocked {
		t.Fatalf("correction after three infrastructure failures = %#v, want the latest source", pin.Correction)
	}
	source := *pin.Correction
	fixture := workContextFixture{store: s, owner: owner, workID: workID}
	job := *s2DispatchJobBinding(t, s, workID, source.FailedAttemptID)
	next := "attempt:" + workID + ":4"
	if err := dispatchWorkContextAttempt(t, fixture, issue1013CurrentDispatchPacket(t, fixture, next, job)); err != nil {
		t.Fatalf("fourth dispatch after infrastructure failures: %v", err)
	}
	if after := issue1013Pin(t, s, workID); !sameWorkflowCorrection(after.Correction, &source) {
		t.Fatalf("authorization erased the unresolved job source: %+v", after.Correction)
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

// An accepted worker result closes the current correction source. A later
// failed attempt owns a fresh source without inheriting the consumed one.
func TestAcceptedWorkerResultClosesCorrectionSource(t *testing.T) {
	const workID = "correction-count-accept-reset"
	ctx := context.Background()
	fixture, attempt1, _ := seedCurrentRepairWorker(t, workID)
	s := fixture.store
	defer s.Close()
	job := *s2DispatchJobBinding(t, s, workID, attempt1)
	s2WorkerFailedAttributedLive(t, s, workID, attempt1, "lane fallback blocked before the model ran")
	pin := issue1013Pin(t, s, workID)
	if pin.Correction == nil || pin.Correction.FailedAttemptID != attempt1 || pin.Correction.FailureKind != WorkerFailureFallbackBlocked {
		t.Fatalf("correction after infrastructure failure = %#v, want its exact source and kind", pin.Correction)
	}

	attempt2 := "attempt:" + workID + ":2"
	if err := dispatchWorkContextAttempt(t, fixture, issue1013CurrentDispatchPacket(t, fixture, attempt2, job)); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{implementDispatchEvidenceEvent(workID, attempt2, laneForID(t), job)}}); err != nil {
		t.Fatal(err)
	}
	s2WorkerFailedAttributedLive(t, s, workID, attempt2, "lane fallback blocked before the model ran")
	pin = issue1013Pin(t, s, workID)
	if pin.Correction == nil || pin.Correction.FailedAttemptID != attempt2 {
		t.Fatalf("correction after two infrastructure failures = %#v, want the latest failed source", pin.Correction)
	}

	// The accepted delivery continues the same job; a new job would open the
	// next execution group, which needs an accepted Review first.
	attempt3 := "attempt:" + workID + ":3"
	if err := dispatchWorkContextAttempt(t, fixture, issue1013CurrentDispatchPacket(t, fixture, attempt3, job)); err != nil {
		t.Fatal(err)
	}
	j := &candidateJourney{fixture: fixture, s: s, workID: workID, repo: workerFixtureDirectory(t, s, workID)}
	var branch string
	if err := s.db.QueryRow(`SELECT branch FROM worktree_entries WHERE set_id=? AND state='active'`, WorktreeSetID(workID)).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, j.repo, "branch", "-m", branch)
	j.laneDispatch(t, attempt3, "implementation", &job)
	path := filepath.Join(j.repo, "internal", "store", job.JobID, "value.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	j.commit(t, "worker@example.invalid", "produce bounded implementation")
	j.completeLane(t, attempt3, "implementation", &job, nil)
	if err := j.action(t, "accept_worker_result", map[string]any{"attempt_id": attempt3, "attempt_epoch": reviewSubjectWindow(t, s, workID, attempt3).AttemptEpoch}, fixture.owner); err != nil {
		t.Fatalf("accept the same-job delivery: %v", err)
	}
	j.observe(t, "accepted-delivery")
	pin = issue1013Pin(t, s, workID)
	if pin.Correction != nil {
		t.Fatalf("accepted result left correction context: %#v", pin.Correction)
	}
	if step := currentStep(t, s, workID); step != "repair" {
		t.Fatalf("local result acceptance advanced the step to %q, want repair", step)
	}

	// The accepted job is satisfied; a continue Review admits the next job.
	acceptCurrentReviewForTest(t, fixture, j.repo, "continue")
	next := reviewSubjectReadyRevision(t, fixture)
	attempt4 := "attempt:" + workID + ":4"
	if err := dispatchWorkContextAttempt(t, fixture, issue1013CurrentDispatchPacket(t, fixture, attempt4, next)); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{implementDispatchEvidenceEvent(workID, attempt4, laneForID(t), next)}}); err != nil {
		t.Fatal(err)
	}
	s2WorkerFailedAttributedLive(t, s, workID, attempt4, "lane fallback blocked before the model ran")
	pin = issue1013Pin(t, s, workID)
	if pin.Correction == nil || pin.Correction.FailedAttemptID != attempt4 {
		t.Fatalf("correction after the accepted result = %#v, want the new failed source", pin.Correction)
	}
}

// A scope-preserving contract revision cannot erase an unresolved job source.
func TestCorrectionSourceSurvivesContractSupersession(t *testing.T) {
	const workID = "correction-count-supersede"
	s, owner, pin := seedIssue1013EscalatedCorrection(t, workID)
	defer s.Close()
	if pin.Correction == nil || pin.Correction.FailedAttemptID != "attempt:"+workID+":3" {
		t.Fatalf("correction before supersession = %#v, want the latest failed source", pin.Correction)
	}
	source := *pin.Correction
	projection, err := ReadWorkflowProjection(t.Context(), s, WorkflowReadRequest{WorkID: workID})
	if err != nil || projection.Contract == nil {
		t.Fatalf("current contract=%+v err=%v", projection.Contract, err)
	}
	contract := projection.Contract
	predicates := make([]map[string]any, len(contract.OutcomePredicates))
	for i, predicate := range contract.OutcomePredicates {
		predicates[i] = map[string]any{"predicate_id": predicate.PredicateID, "ordinal": predicate.Ordinal, "outcome_kind": predicate.OutcomeKind, "outcome_payload": json.RawMessage(predicate.OutcomePayload)}
	}
	successor, err := json.Marshal(map[string]any{
		"contract_version": contract.Version + 1, "premise": "The same approved outcome needs a corrected approach.",
		"outcome_predicates": predicates, "required_evidence": contract.RequiredEvidence,
		"route_conventions": contract.RouteConventions, "spec_mandate": contract.SpecMandate, "law_modifies": contract.LawModifies,
		"rigor_class": contract.RigorClass, "architecture_binding": contract.ArchitectureBinding, "decision_domains": []string{},
		"supersede_reason": "correct the approach without changing the approved outcome", "audit_evidence": source.EvidenceRefs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runVerdictActionAs(t, s, workID, "supersede_contract", successor, 0, owner); err != nil {
		t.Fatalf("supersede the contract across the correction sequence: %v", err)
	}
	var activeVersion int
	if err := s.DatabaseForTesting().QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&activeVersion); err != nil {
		t.Fatal(err)
	}
	if int64(activeVersion) != contract.Version+1 {
		t.Fatalf("active contract version = %d, want %d", activeVersion, contract.Version+1)
	}
	pin = issue1013Pin(t, s, workID)
	if !sameWorkflowCorrection(pin.Correction, &source) {
		t.Fatalf("correction across supersession = %#v, want the same failed source", pin.Correction)
	}

	fixture := workContextFixture{store: s, owner: owner, workID: workID}
	job := *s2DispatchJobBinding(t, s, workID, source.FailedAttemptID)
	recordWorkerJobRevisionForTest(t, s, workID, owner, &job)
	correctiveAttempt := "attempt:" + workID + ":4"
	if err := dispatchWorkContextAttempt(t, fixture, issue1013CurrentDispatchPacket(t, fixture, correctiveAttempt, job)); err != nil {
		t.Fatalf("dispatch the corrective attempt under the successor contract: %v", err)
	}
	if err := ApplyOperation(t.Context(), s, Operation{Events: []Event{implementDispatchEvidenceEvent(workID, correctiveAttempt, laneForID(t), job)}}); err != nil {
		t.Fatal(err)
	}
	epoch := reviewSubjectWindow(t, s, workID, correctiveAttempt).AttemptEpoch
	s2WorkerFailedAttributedLive(t, s, workID, correctiveAttempt, "the host observed a failed corrective launch")
	pin = issue1013Pin(t, s, workID)
	if pin.Correction == nil || pin.Correction.FailedAttemptID != correctiveAttempt || pin.Correction.FailedAttemptEpoch != epoch || pin.Correction.SourceEventID == source.SourceEventID {
		t.Fatalf("correction under the successor contract = %#v, want the new failed source", pin.Correction)
	}
	// The successor keeps the corrective dispatch route visible.
	if !issue1013HasIntent(pin, "dispatch_worker") {
		t.Fatalf("correction under the successor hid dispatch_worker: %#v", pin.NextValidIntents)
	}
}

// The declared payload gate is the input surface that names the field: a
// predicate id outside the id charset must be refused by the payload
// declaration itself, before any correction-specific check runs.
func TestRejectPayloadDeclarationRefusesNonIDPredicateID(t *testing.T) {
	payload := json.RawMessage(`{"attempt_id":"attempt-correction-1","attempt_epoch":1,"diagnosis":"the delivered predicate id carries a slash","strategy":"re-request with an in-charset id","predicate_ids":["predicate:primary/slash"],"evidence_refs":["evidence:correction-gate"]}`)
	err := validateWorkflowActionPayload(WorkflowDefinition{}, "reject_worker_result", payload)
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
	return json.RawMessage(`{"contract_version":3,"premise":"the delivered subject the durable evidence names","outcome_predicates":[{"predicate_id":"predicate:return-route","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:return-route","immutable_subject_ref":"commit:` + workID + `","expected_result":"pass"}}],"required_evidence":["verification","review","artifact"],"route_conventions":[],"spec_mandate":[],"law_modifies":[],"rigor_class":"prototype_internal","decision_domains":[],"architecture_binding":` + binding + `,"supersede_reason":"the corrected contract premise moved again","audit_evidence":["evidence:later-successor-` + workID + `"]}`)
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
// the stale-law recovery finds the accepted successor pinned (CD-0222 D5).
func staleLawSuccessorPayload(workID string) json.RawMessage {
	binding := `{"domain_registry_content_hash":"sha256:` + strings.Repeat("b", 64) + `","home_domain_id":"root","affected_domain_ids":["root"],"domain_modifies":[],"domain_relation_modifies":[],"law_additions":[],"verification_obligations":[]}`
	return json.RawMessage(`{"contract_version":2,"premise":"the delivered subject under the successor law","outcome_predicates":[{"predicate_id":"predicate:return-route","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:return-route","immutable_subject_ref":"commit:` + workID + `","expected_result":"pass"}}],"required_evidence":["verification","review","artifact"],"route_conventions":["complete_step_correction"],"spec_mandate":["spec:two"],"law_modifies":[],"rigor_class":"prototype_internal","decision_domains":[],"architecture_binding":` + binding + `,"supersede_reason":"the approved premise moved to the successor law","audit_evidence":["evidence:complete-correction-` + workID + `"]}`)
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
		INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','spec:two','spec','accepted','.concord/docs/spec-two.md','Synthetic successor law','sha256:`+strings.Repeat("b", 64)+`','test');
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

// TestCompleteStepCorrectionRecoversDuplicateProjection proves a duplicated
// contract projection at the pinned complete step keeps an exit: the
// complete-step correction admits it once the contradiction record exists,
// the successor names every active version, and the instance returns to its
// external-effect step under one active contract (CD-0203 D1). Before the
// contradiction record, the shared gate refuses as for one contract. The
// loader's fold matches the abstract model before and after the recovery.
func TestCompleteStepCorrectionRecoversDuplicateProjection(t *testing.T) {
	t.Parallel()
	const workID = "complete-correction-duplicate"
	fixture, _ := seedCompleteStep(t, workID, "workflow.break_fix", "verify", "complete")
	s := fixture.store
	ownerRef, err := WorkflowActorRef(fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	seedDuplicateContractProjection(t, s, workID, ownerRef)
	seedWorkflowLawProjection(t, s)
	var activeCount int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&activeCount); err != nil {
		t.Fatal(err)
	}
	if activeCount != 2 {
		t.Fatalf("duplicate fixture active contracts = %d, want 2", activeCount)
	}
	def := pinnedWorkflowDefinitionForTest(t, s, workID)
	// The seeded proof records and satisfies one worker job in the primary
	// Project, so the delivery facet folds a recorded, satisfied job.
	model := admissionModelState{step: "complete", debt: ReviewDebtNone, contracts: 2, verdict: "ok", artifact: true, jobs: "satisfied", jobsScope: "project"}
	conformanceCheckpoint(t, s, BuiltinWorkflowRegistry(), workID, def, model, "duplicate at the complete step")
	if issue1013HasIntent(issue1013Pin(t, s, workID), "supersede_contract") {
		t.Fatal("duplicate complete-step pin advertises supersession before the contradiction record")
	}
	duplicateRecovery := json.RawMessage(strings.Replace(string(completeStepSuccessorPayload(workID)), `"contract_version":2,`, `"contract_version":3,"predecessor_contract_versions":[1,2],`, 1))
	err = issue1013Preflight(t, s, workID, "supersede_contract", duplicateRecovery, fixture.owner)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || failure.Detail != "contract recovery is available only for a stale workflow contract" {
		t.Fatalf("duplicate recovery without the contradiction record = %v, want the complete-step gate refusal", err)
	}

	recordCompleteStepContradictionObservation(t, s, workID, fixture.owner)
	model.observed = true
	conformanceCheckpoint(t, s, BuiltinWorkflowRegistry(), workID, def, model, "duplicate with the contradiction record")
	if !issue1013HasIntent(issue1013Pin(t, s, workID), "supersede_contract") {
		t.Fatal("duplicate complete-step pin withholds the admitted correction")
	}
	if err := issue1013Preflight(t, s, workID, "supersede_contract", duplicateRecovery, fixture.owner); err != nil {
		t.Fatalf("preflight refused the duplicate complete-step correction: %v", err)
	}
	// consequence_v1 supersedes through attributed supersession, not
	// verified operator approval (CD-0172).
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", duplicateRecovery, fixture.owner, fixture.operator); err != nil {
		t.Fatalf("duplicate complete-step correction: %v", err)
	}
	if step, state, _, _, _ := completeStepInstancePin(t, s, workID); step != "repair" || state != "running" {
		t.Fatalf("instance after duplicate correction: step=%q state=%q, want repair/running", step, state)
	}
	var active []int64
	rows, err := s.DatabaseForTesting().Query(`SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var version int64
		if err := rows.Scan(&version); err != nil {
			t.Fatal(err)
		}
		active = append(active, version)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0] != 3 {
		t.Fatalf("active contracts after duplicate correction = %v, want [3]", active)
	}
	model = admissionSuccessor(def, model, "supersede_contract")
	// The single successor restores the seeded journey's facets the
	// duplicates hid: its started delivery, the accepted current Review of
	// the delivered candidate, and the engineering successor's coordinator
	// confirmation.
	model.started, model.engineering = true, true
	model.review = admissionModelReview{candidate: true, accepted: "ready_for_final_proof", current: true}
	conformanceCheckpoint(t, s, BuiltinWorkflowRegistry(), workID, def, model, "after the duplicate correction")
}

func TestCompleteStepCorrectionAdmissionUsesOnlyPriorHistory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, test := range []struct {
		name              string
		observationBefore bool
		laterKind         string
		state             string
		action            string
		wantBefore        bool
		wantLive          bool
	}{
		{"later verdict", true, WorkflowVerdictRecorded, "", "", true, false},
		{"later observation", false, WorkObservationRecorded, "", "", false, true},
		{"accepted result", true, WorkflowActionCompleted, "completed", "accept_worker_result", false, true},
		{"accepted evidence", true, WorkflowActionCompleted, "completed", "accept_worker_evidence", false, true},
		{"rejected result", true, WorkflowActionCompleted, "completed", "reject_worker_result", false, true},
		{"recorded failure", true, WorkflowActionCompleted, "failed", "record_worker_failure", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			const workID = "correction-history-boundary"
			fixture, _ := seedCompleteStep(t, workID, "workflow.break_fix", "verify", "complete")
			s := fixture.store
			db := s.DatabaseForTesting()
			if test.observationBefore {
				recordCompleteStepContradictionObservation(t, s, workID, fixture.owner)
			}
			if test.state != "" {
				var completedAt, failedAt any
				failureKind := ""
				if test.state == "completed" {
					completedAt = "2026-10-13T00:00:01Z"
				} else {
					failedAt = "2026-10-13T00:00:01Z"
					failureKind = "abandoned"
				}
				if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO worker_attempts(work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,dispatched_at,completed_at,failed_at,failure_kind) VALUES(?, 'attempt:history', 'lane:implement', 1, ?, 'work_transition', 'model/test', '1.0', '1.0', ?, '2026-10-13T00:00:00Z',?,?,?); DELETE FROM fold_guard`, workID, "sha256:"+strings.Repeat("a", 64), test.state, completedAt, failedAt, failureKind); err != nil {
					t.Fatal(err)
				}
			}
			// Keep projections at the correction boundary while the intact log
			// already contains the later record, as it does during replay.
			payload := mustJSONValue(map[string]any{"action_id": test.action, "worker_attempt_id": "attempt:history"})
			if _, err := db.Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?)`, "later-history", test.laterKind, SubjectWorkItem, workID, fixture.owner.PrincipalRef, "2026-10-13T00:00:01Z", 1, payload); err != nil {
				t.Fatal(err)
			}
			var beforeSeq int64
			if err := db.QueryRow(`SELECT seq FROM domain_events WHERE event_id='later-history'`).Scan(&beforeSeq); err != nil {
				t.Fatal(err)
			}
			definition, ok := completeStepPinnedDefinition(t, s, workID)
			if !ok {
				t.Fatal("missing pinned definition")
			}
			for _, boundary := range []struct {
				seq  int64
				want bool
			}{{beforeSeq, test.wantBefore}, {beforeSeq + 1, test.wantLive}, {0, test.wantLive}} {
				available, err := workflowCompleteStepCorrectionAvailable(ctx, db, workID, definition.Definition, "complete", "test", boundary.seq)
				if err != nil || available != boundary.want {
					t.Errorf("admission before sequence %d = %t, error = %v; want %t", boundary.seq, available, err, boundary.want)
				}
			}
		})
	}
}

func TestCompleteStepCorrectionKeepsSuccessorVerdictsFresh(t *testing.T) {
	t.Parallel()
	const workID = "complete-correction-fresh-verdicts"
	ctx := context.Background()
	fixture, _ := seedCompleteStepCorrection(t, workID, "workflow.break_fix", "verify", "complete")
	s := fixture.store
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", completeStepSuccessorPayload(workID), fixture.owner, fixture.operator); err != nil {
		t.Fatalf("supersede the contract at the complete step: %v", err)
	}
	step, state, _, _, _ := completeStepInstancePin(t, s, workID)
	if step != "repair" || state != "running" {
		t.Fatalf("instance after correction: step=%q state=%q, want repair/running", step, state)
	}
	var activeVersion int
	if err := s.DatabaseForTesting().QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&activeVersion); err != nil {
		t.Fatal(err)
	}
	if activeVersion != 2 {
		t.Fatalf("active contract version after correction = %d, want 2", activeVersion)
	}
	satisfied, err := latestWorkflowVerdicts(ctx, s.db, workID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(satisfied) != 0 {
		t.Fatalf("successor inherited pre-correction verdicts: %#v", satisfied)
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
		INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','spec:two','spec','accepted','.concord/docs/spec-two.md','Synthetic successor law','sha256:`+strings.Repeat("b", 64)+`','test');
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
		err := WorkflowActionPreflightWithRegistry(context.Background(), fixture.store, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
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

	t.Run("live fold refuses", func(t *testing.T) {
		t.Parallel()
		const workID = "complete-correction-offshape-fold"
		fixture := seedOffShapeCompletedInstance(t, workID)
		successor := &workflowContractApprovedPayload{ContractVersion: 2, RouteConventions: []string{}}
		tx, txErr := fixture.store.DatabaseForTesting().BeginTx(context.Background(), nil)
		if txErr != nil {
			t.Fatal(txErr)
		}
		defer tx.Rollback()
		err := foldCompleteStepContractCorrectionTx(context.Background(), tx, Event{SubjectID: workID}, successor)
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvariantViolation || failure.Detail != sharedDetail {
			t.Fatalf("live fold of the off-shape completed supersession = %v, want the shape refusal", err)
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
	seedDuplicateContractProjection(t, s, workID, ownerRef)
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

// foldCompleteStepContractCorrectionTx resolves the builtin registry for
// direct callers. The core keeps the route marker and
// admission checks together before it returns the pinned instance to its
// external-effect step, and the scoped supersession fold carries the
// transaction's registry to the same core. The caller holds the fold
// transaction.
func foldCompleteStepContractCorrectionTx(ctx context.Context, tx *sql.Tx, event Event, successor *workflowContractApprovedPayload) error {
	return foldCompleteStepContractCorrectionTxForRegistry(ctx, tx, BuiltinWorkflowRegistry(), event, successor)
}

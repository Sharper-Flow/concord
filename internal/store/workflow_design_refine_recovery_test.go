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

const refineRecoveryDesign = `{"approach":"replace the approach after the corrected contract","decisions":[{"id":"decision:refine-recovery","question":"Which approach?","choice":"replacement","rationale":"matches the corrected contract","rejected":["original"]}],"touched_refs":["internal/store/workflow_design.go"]}`

func refineRecoverySuccessor(contractVersion int64, designRecord string) json.RawMessage {
	design := ""
	if designRecord != "" {
		design = `,"design_record":` + designRecord
	}
	binding := `"architecture_binding":{"domain_registry_content_hash":"sha256:` + strings.Repeat("b", 64) + `","home_domain_id":"root","affected_domain_ids":["root"],"domain_modifies":[],"domain_relation_modifies":[],"law_additions":[],"verification_obligations":[]}`
	return json.RawMessage(`{"contract_version":` + jsonInt(contractVersion) + `,"premise":"deliver the corrected change","outcome_predicates":[{"predicate_id":"predicate:return-route","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:return-route","immutable_subject_ref":"commit:corrected","expected_result":"pass"}}],"required_evidence":["verification"],"route_conventions":[],"spec_mandate":[],"law_modifies":[],"rigor_class":"prototype_internal",` + binding + `,"supersede_reason":"the operator corrected the requirement at acceptance","audit_evidence":["evidence:return-route-verification"]` + design + `}`)
}

// dispatchRefineAttemptOnly dispatches a review lane at refine through the
// dispatch_worker action and records the lane dispatch. The attempt stays
// live: no report is recorded. A non-nil correction makes the packet consume
// that correction context, the way a post-failure retry does.
func dispatchRefineAttemptOnly(t *testing.T, fixture workflowReturnRouteFixture, workID, label string, correction map[string]any) (string, int64, error) {
	t.Helper()
	ctx := context.Background()
	lane := reviewGateLane(t, "review")
	laneVersion, laneDigest := registeredLaneIdentity(t, "review")
	attemptID := "attempt:" + workID + ":" + label
	packet := joinPacketFor(workID, "refine", attemptID, "review", laneVersion, laneDigest)
	if correction != nil {
		packet["inputs"].(map[string]any)["correction"] = correction
	}
	if _, err := dispatchJoinAttempt(ctx, t, fixture.store, workID, verdictItemVersion(t, fixture.store, workID), fixture.owner, packet); err != nil {
		return attemptID, 0, err
	}
	var packetDigest string
	var epoch int64
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.worker_packet_digest'), json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionCompleted).Scan(&packetDigest, &epoch); err != nil {
		t.Fatal(err)
	}
	laneDispatch := Event{EventID: "refine-dispatch-" + label + "-" + workID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(40, 0).UTC(), PayloadVersion: 2, Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion, PacketDigest: packetDigest})}
	if err := fixture.store.Transact(ctx, func(transaction *Transaction) error {
		prepared, err := PrepareLaneActorDispatch(ctx, transaction, laneDispatch, fixture.owner.PrincipalRef, fixture.owner.ClientRef)
		if err != nil {
			return err
		}
		_, err = AppendLaneActorDispatchTx(ctx, transaction, prepared)
		return err
	}); err != nil {
		t.Fatalf("lane actor dispatch: %v", err)
	}
	return attemptID, epoch, nil
}

// dispatchRefineAttempt dispatches a review lane at refine through the
// dispatch_worker action, completes its report, and returns the attempt epoch.
func dispatchRefineAttempt(t *testing.T, fixture workflowReturnRouteFixture, workID, label string) (string, int64, error) {
	t.Helper()
	attemptID, epoch, err := dispatchRefineAttemptOnly(t, fixture, workID, label, nil)
	if err != nil {
		return attemptID, epoch, err
	}
	lane := reviewGateLane(t, "review")
	if err := ApplyOperation(context.Background(), fixture.store, Operation{Events: []Event{
		workerCompleteEventForLane(workID, "refine-completed-"+label+"-"+workID, attemptID, lane, time.Unix(41, 0).UTC()),
	}}); err != nil {
		t.Fatal(err)
	}
	return attemptID, epoch, nil
}

// seedReturnedRefineCorrection drives an implementation item through a first
// pass that dispatches and accepts a review lane at refine, corrects the
// contract at acceptance without design_record, and returns to refine through
// the confirm_premise failure edge. It returns the accepted first-pass review.
func seedReturnedRefineCorrection(t *testing.T, workID string) workflowReturnRouteFixture {
	t.Helper()
	ctx := context.Background()
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.implementation", "execution")
	s, owner, operator := fixture.store, fixture.owner, fixture.operator
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	version := verdictItemVersion(t, s, workID)
	var design map[string]any
	if err := json.Unmarshal([]byte(refineRecoveryDesign), &design); err != nil {
		t.Fatal(err)
	}
	design["work_id"], design["expected_version"], design["resulting_version"] = workID, version, version+1
	if err := applyWorkflowTestOperation(ctx, s, Operation{
		Events:           []Event{workflowEventWithActor("design-"+workID, WorkflowDesignRecorded, workID, ownerRef, design)},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version},
	}); err != nil {
		t.Fatalf("seed design: %v", err)
	}

	// First pass: execution worker accepted, refine starts, a review lane runs
	// at refine and is accepted, and delivery reaches acceptance.
	lane := BuiltinLaneDefinitions()[0]
	executionAttempt := "attempt:" + workID
	version = verdictItemVersion(t, s, workID)
	start := workflowEventWithActor("start-"+workID, WorkflowActionStarted, workID, ownerRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 1,
		"accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": "start:" + workID, "actor_ref": ownerRef,
	})
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{start}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
	for _, event := range []Event{
		{EventID: "dispatch-" + workID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: ownerRef, OccurredAt: time.Unix(30, 0).UTC(), PayloadVersion: 2, Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: executionAttempt, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion})},
		workerCompleteEventForLane(workID, "completed-"+workID, executionAttempt, lane, time.Unix(31, 0).UTC()),
	} {
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{event}}); err != nil {
			t.Fatal(err)
		}
	}
	acceptor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/refine-acceptor", SessionRef: "session/" + workID + "-acceptor", ActorClass: ActorAgent}
	if err := runVerdictActionAs(t, s, workID, "accept_worker_result", json.RawMessage(`{"attempt_id":"`+executionAttempt+`","attempt_epoch":1}`), 0, acceptor); err != nil {
		t.Fatalf("accept execution worker: %v", err)
	}
	if err := runVerdictActionAs(t, s, workID, "start_refine", json.RawMessage(`{}`), 0, acceptor); err != nil {
		t.Fatalf("start refine: %v", err)
	}
	reviewAttempt, reviewEpoch, err := dispatchRefineAttempt(t, fixture, workID, "refine-review-1")
	if err != nil {
		t.Fatalf("first-pass refine dispatch with a current design: %v", err)
	}
	if err := acceptRefineResult(t, s, workID, reviewAttempt, reviewEpoch, acceptor); err != nil {
		t.Fatalf("accept refine review: %v", err)
	}
	delivery := json.RawMessage(`{"delivery_artifact":"artifact:return-route","delivery_state":"asserted"}`)
	for i := 0; i < 2 && currentStep(t, s, workID) != "acceptance"; i++ {
		if err := runVerdictActionAs(t, s, workID, "record_delivery", delivery, 0, acceptor); err != nil {
			t.Fatalf("record delivery %d at %s: %v", i, currentStep(t, s, workID), err)
		}
	}
	if step := currentStep(t, s, workID); step != "acceptance" {
		t.Fatalf("first pass ended at %q, want acceptance", step)
	}

	// Correction at acceptance without design_record.
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", refineRecoverySuccessor(2, ""), owner, operator); err != nil {
		t.Fatalf("supersede_contract without design_record at acceptance: %v", err)
	}
	_, stale, err := readCurrentWorkflowDesign(ctx, s.DatabaseForTesting(), workID)
	if err != nil {
		t.Fatal(err)
	}
	if !stale {
		t.Fatal("design is current after the acceptance supersede, want stale")
	}

	// A non-ok verdict and premise confirmation return to refine.
	verdict := json.RawMessage(`{"contract_version":2,"predicate_id":"predicate:return-route","verdict_kind":"outcome_mismatch","evaluation_evidence":["evidence:return-route-verification"],"incomparable_with_approved":true}`)
	if err := runIssue933OperatorAction(t, s, workID, "record_verdict", verdict, owner, operator); err != nil {
		t.Fatalf("record_verdict: %v", err)
	}
	if err := runIssue933OperatorAction(t, s, workID, "confirm_premise", json.RawMessage(`{"contract_version":2}`), owner, operator); err != nil {
		t.Fatalf("confirm_premise: %v", err)
	}
	if step := currentStep(t, s, workID); step != "refine" {
		t.Fatalf("step = %q, want refine", step)
	}

	return fixture
}

func refineRecoveryStepSeq(t *testing.T, s *Store, workID, kind, stepID, actionID string) int64 {
	t.Helper()
	var seq int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(MAX(seq),0) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.step_id')=? AND json_extract(payload,'$.action_id')=?`, workID, kind, stepID, actionID).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	return seq
}

// A return edge re-enters refine without a start event, so the pass boundary
// is the confirm_premise completion, not the previous pass's start_refine.
func TestReturnedRefinePassBoundaryStartsNewPass(t *testing.T) {
	const workID = "refine-pass-boundary"
	ctx := context.Background()
	fixture := seedReturnedRefineCorrection(t, workID)
	s := fixture.store
	var definitionRef string
	var definitionVersion int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_version FROM workflow_instances WHERE work_id=?`, workID).Scan(&definitionRef, &definitionVersion); err != nil {
		t.Fatal(err)
	}
	registered, ok := BuiltinWorkflowRegistry().Lookup(definitionRef, definitionVersion)
	if !ok {
		t.Fatalf("pinned definition %s@%d is not registered", definitionRef, definitionVersion)
	}
	startSeq := refineRecoveryStepSeq(t, s, workID, WorkflowActionStarted, "refine", "start_refine")
	if startSeq == 0 {
		t.Fatal("no start_refine recorded at refine")
	}
	entrySeq := refineRecoveryStepSeq(t, s, workID, WorkflowActionCompleted, "acceptance", "confirm_premise")
	if entrySeq <= startSeq {
		t.Fatalf("latest confirm_premise entry seq %d does not follow the previous pass start %d", entrySeq, startSeq)
	}
	boundary, started, err := workflowStepPassBoundary(ctx, s.DatabaseForTesting(), registered.Definition, workID, "refine", "test")
	if err != nil {
		t.Fatal(err)
	}
	if !started {
		t.Fatal("refine reports no pass activity")
	}
	if boundary != entrySeq {
		t.Fatalf("pass boundary = %d, want the latest entry %d", boundary, entrySeq)
	}
}

// A contract corrected at acceptance without design_record leaves the design
// stale. At the returned refine the stale-design dispatch refusal names
// supersede_contract, the pin advertises it, and a correction that carries the
// replacement design restores dispatch. The accepted first-pass review is not
// offered for rejection.
func TestReturnedRefineRecordsReplacementDesignAndDispatches(t *testing.T) {
	const workID = "refine-design-recovery"
	ctx := context.Background()
	fixture := seedReturnedRefineCorrection(t, workID)
	s, owner, operator := fixture.store, fixture.owner, fixture.operator
	_, _, dispatchErr := dispatchRefineAttempt(t, fixture, workID, "refine-stale")
	if dispatchErr == nil {
		t.Fatal("dispatch_worker admitted at the returned refine with a stale design")
	}
	var staleDispatchFailure *Failure
	if !errors.As(dispatchErr, &staleDispatchFailure) {
		t.Fatalf("stale-design dispatch refusal is not a typed failure: %v", dispatchErr)
	}
	if staleDispatchFailure.RecoveryAction != "use supersede_contract with design_record before worker dispatch" {
		t.Fatalf("stale-design dispatch refusal does not name the supersede route: %q", staleDispatchFailure.RecoveryAction)
	}
	pin, err := ReadWorkPin(ctx, s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if !workPinContainsAction(pin.NextValidIntents, "supersede_contract") {
		t.Fatalf("pin does not advertise supersede_contract at the returned refine; intents = %v", intentActionIDs(pin.NextValidIntents))
	}
	supersedeIntent := workPinIntentForActionID(t, pin, "supersede_contract")
	if supersedeIntent.ReasonCode != "operator_contract_correction" {
		t.Fatalf("supersede_contract reason code = %q, want operator_contract_correction", supersedeIntent.ReasonCode)
	}
	if workPinContainsAction(pin.NextValidIntents, "reject_worker_result") {
		t.Fatal("pin advertises reject_worker_result for a worker result an accept already dispositioned")
	}
	if workPinContainsAction(pin.NextValidIntents, "dispatch_worker") {
		t.Fatal("pin advertises dispatch_worker while the recorded design is stale")
	}
	if _, _, err := WorkflowActionDefinitionFor(ctx, s, BuiltinWorkflowRegistry(), workID, "supersede_contract"); err != nil {
		t.Fatalf("supersede_contract discovery refused at the returned refine: %v", err)
	}
	if _, _, err := WorkflowActionDefinitionFor(ctx, s, BuiltinWorkflowRegistry(), workID, "reject_worker_result"); err == nil {
		t.Fatal("reject_worker_result discovery admitted for a worker result an accept already dispositioned")
	}
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", refineRecoverySuccessor(3, refineRecoveryDesign), owner, operator); err != nil {
		t.Fatalf("supersede_contract with design_record at the returned refine: %v", err)
	}
	_, stale, err := readCurrentWorkflowDesign(ctx, s.DatabaseForTesting(), workID)
	if err != nil {
		t.Fatal(err)
	}
	if stale {
		t.Fatal("design is still stale after the returned-refine supersede with design_record")
	}
	if _, _, err := dispatchRefineAttempt(t, fixture, workID, "refine-after-recovery"); err != nil {
		t.Fatalf("dispatch_worker refused at the returned refine after the replacement design: %v", err)
	}
	var contractVersion int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(MAX(contract_version),0) FROM workflow_contracts WHERE work_id=?`, workID).Scan(&contractVersion); err != nil {
		t.Fatal(err)
	}
	if contractVersion != 3 {
		t.Fatalf("latest contract version = %d, want 3", contractVersion)
	}
}

// failReviewWorkerAttempt records the WorkerFailed event for a dispatched
// review lane attempt.
func failReviewWorkerAttempt(t *testing.T, s *Store, workID, attemptID string, lane LaneDefinition) {
	t.Helper()
	fail := Event{EventID: "refine-failed-" + attemptID, Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(42, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(WorkerFailedPayload{AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), FailureKind: WorkerFailureWorkerError, Detail: "the review lane reported a failure"})}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{fail}}); err != nil {
		t.Fatal(err)
	}
}

// refineRetryCorrection reads the work pin's current correction context in the
// payload shape a retry packet consumes.
func refineRetryCorrection(t *testing.T, s *Store, workID string) map[string]any {
	t.Helper()
	pin, err := ReadWorkPin(context.Background(), s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if pin.Correction == nil {
		t.Fatalf("no correction context at %s after the recorded refine failure", workID)
	}
	return map[string]any{
		"disposition": pin.Correction.Disposition, "attempt_count": pin.Correction.AttemptCount, "attempt_limit": pin.Correction.AttemptLimit, "escalated": pin.Correction.Escalated,
		"diagnosis": pin.Correction.Diagnosis, "strategy": pin.Correction.Strategy, "failure_kind": pin.Correction.FailureKind, "failure_detail": pin.Correction.FailureDetail,
		"predicate_ids": pin.Correction.PredicateIDs, "evidence_refs": pin.Correction.EvidenceRefs,
	}
}

// seedRefineFirstPass seeds the design, the accepted execution pass, and the
// started refine step of an implementation item.
func seedRefineFirstPass(t *testing.T, workID string) (workflowReturnRouteFixture, WorkflowActor) {
	t.Helper()
	ctx := context.Background()
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.implementation", "execution")
	s, owner := fixture.store, fixture.owner
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	version := verdictItemVersion(t, s, workID)
	var design map[string]any
	if err := json.Unmarshal([]byte(refineRecoveryDesign), &design); err != nil {
		t.Fatal(err)
	}
	design["work_id"], design["expected_version"], design["resulting_version"] = workID, version, version+1
	if err := applyWorkflowTestOperation(ctx, s, Operation{
		Events:           []Event{workflowEventWithActor("design-"+workID, WorkflowDesignRecorded, workID, ownerRef, design)},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version},
	}); err != nil {
		t.Fatalf("seed design: %v", err)
	}

	lane := BuiltinLaneDefinitions()[0]
	executionAttempt := "attempt:" + workID
	version = verdictItemVersion(t, s, workID)
	start := workflowEventWithActor("start-"+workID, WorkflowActionStarted, workID, ownerRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 1,
		"accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": "start:" + workID, "actor_ref": ownerRef,
	})
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{start}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
	for _, event := range []Event{
		{EventID: "dispatch-" + workID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: ownerRef, OccurredAt: time.Unix(30, 0).UTC(), PayloadVersion: 2, Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: executionAttempt, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion})},
		workerCompleteEventForLane(workID, "completed-"+workID, executionAttempt, lane, time.Unix(31, 0).UTC()),
	} {
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{event}}); err != nil {
			t.Fatal(err)
		}
	}
	acceptor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/refine-acceptor", SessionRef: "session/" + workID + "-acceptor", ActorClass: ActorAgent}
	if err := runVerdictActionAs(t, s, workID, "accept_worker_result", json.RawMessage(`{"attempt_id":"`+executionAttempt+`","attempt_epoch":1}`), 0, acceptor); err != nil {
		t.Fatalf("accept execution worker: %v", err)
	}
	if err := runVerdictActionAs(t, s, workID, "start_refine", json.RawMessage(`{}`), 0, acceptor); err != nil {
		t.Fatalf("start refine: %v", err)
	}
	return fixture, acceptor
}

// returnRefineFromAcceptance corrects the contract at acceptance without a
// design record, records a mismatch verdict, and confirms the premise so the
// failure edge returns the item to refine. The caller then repairs the stale
// design with a supersede that carries the replacement design.
func returnRefineFromAcceptance(t *testing.T, fixture workflowReturnRouteFixture, workID string, _, operator WorkflowActor) {
	t.Helper()
	s, owner := fixture.store, fixture.owner
	if step := currentStep(t, s, workID); step != "acceptance" {
		t.Fatalf("step = %q, want acceptance before the return", step)
	}
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", refineRecoverySuccessor(2, ""), owner, operator); err != nil {
		t.Fatalf("supersede_contract without design_record at acceptance: %v", err)
	}
	verdict := json.RawMessage(`{"contract_version":2,"predicate_id":"predicate:return-route","verdict_kind":"outcome_mismatch","evaluation_evidence":["evidence:return-route-verification"],"incomparable_with_approved":true}`)
	if err := runIssue933OperatorAction(t, s, workID, "record_verdict", verdict, owner, operator); err != nil {
		t.Fatalf("record_verdict: %v", err)
	}
	if err := runIssue933OperatorAction(t, s, workID, "confirm_premise", json.RawMessage(`{"contract_version":2}`), owner, operator); err != nil {
		t.Fatalf("confirm_premise: %v", err)
	}
	if step := currentStep(t, s, workID); step != "refine" {
		t.Fatalf("step = %q, want refine after the return edge", step)
	}
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", refineRecoverySuccessor(3, refineRecoveryDesign), owner, operator); err != nil {
		t.Fatalf("supersede_contract with design_record at the returned refine: %v", err)
	}
	_, stale, err := readCurrentWorkflowDesign(context.Background(), s.DatabaseForTesting(), workID)
	if err != nil {
		t.Fatal(err)
	}
	if stale {
		t.Fatal("design is stale after the returned-refine supersede with design_record")
	}
}

// deliverRefineFirstPass advances refine through the delivery gate to
// acceptance.
func deliverRefineFirstPass(t *testing.T, fixture workflowReturnRouteFixture, workID string, acceptor WorkflowActor) {
	t.Helper()
	s := fixture.store
	delivery := json.RawMessage(`{"delivery_artifact":"artifact:return-route","delivery_state":"asserted"}`)
	for i := 0; i < 2 && currentStep(t, s, workID) != "acceptance"; i++ {
		if err := runVerdictActionAs(t, s, workID, "record_delivery", delivery, 0, acceptor); err != nil {
			t.Fatalf("record delivery %d at %s: %v", i, currentStep(t, s, workID), err)
		}
	}
	if step := currentStep(t, s, workID); step != "acceptance" {
		t.Fatalf("first pass ended at %q, want acceptance", step)
	}
}

// seedReturnedRefineWithRecordedFailure drives the returned-refine correction
// with one change in the first pass: the first refine review lane fails and
// its failure is recorded before a second review lane is accepted. The pass
// that returns from acceptance therefore carries a recorded refine worker
// failure behind it.
func seedReturnedRefineWithRecordedFailure(t *testing.T, workID string) (workflowReturnRouteFixture, string, int64) {
	t.Helper()
	fixture, acceptor := seedRefineFirstPass(t, workID)
	s, owner, operator := fixture.store, fixture.owner, fixture.operator
	failedAttempt, failedEpoch, err := dispatchRefineAttemptOnly(t, fixture, workID, "refine-review-1", nil)
	if err != nil {
		t.Fatalf("first-pass refine dispatch with a current design: %v", err)
	}
	failReviewWorkerAttempt(t, s, workID, failedAttempt, reviewGateLane(t, "review"))
	applyRecordWorkerFailureForTest(t, s, workID, owner, failedAttempt, failedEpoch, verdictItemVersion(t, s, workID), "refine-failure:"+workID)
	reviewAttempt, reviewEpoch, err := dispatchRefineAttemptOnly(t, fixture, workID, "refine-review-2", refineRetryCorrection(t, s, workID))
	if err != nil {
		t.Fatalf("first-pass refine recovery dispatch after the recorded failure: %v", err)
	}
	reviewLane := reviewGateLane(t, "review")
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{
		workerCompleteEventForLane(workID, "refine-completed-refine-review-2-"+workID, reviewAttempt, reviewLane, time.Unix(41, 0).UTC()),
	}}); err != nil {
		t.Fatal(err)
	}
	if err := acceptRefineResult(t, s, workID, reviewAttempt, reviewEpoch, acceptor); err != nil {
		t.Fatalf("accept refine review: %v", err)
	}
	deliverRefineFirstPass(t, fixture, workID, acceptor)
	returnRefineFromAcceptance(t, fixture, workID, acceptor, operator)
	return fixture, failedAttempt, failedEpoch
}

// A recorded first-pass refine failure stays in the previous pass. After the
// return edge and a live new-pass dispatch, contract correction stays closed
// while the worker is live (CD-0133 D1): the pin does not advertise
// supersede_contract, discovery refuses it, and the fold refuses the action.
func TestReturnedRefineRecordedFailureKeepsCorrectionClosed(t *testing.T) {
	const workID = "refine-live-dispatch-correction"
	ctx := context.Background()
	fixture, _, _ := seedReturnedRefineWithRecordedFailure(t, workID)
	s, owner, operator := fixture.store, fixture.owner, fixture.operator
	liveAttempt, liveEpoch, err := dispatchRefineAttemptOnly(t, fixture, workID, "refine-live", nil)
	if err != nil {
		t.Fatalf("new-pass dispatch with a current design: %v", err)
	}

	pin, err := ReadWorkPin(ctx, s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if workPinContainsAction(pin.NextValidIntents, "supersede_contract") {
		t.Fatalf("pin advertises supersede_contract during a live new-pass worker; intents = %v", intentActionIDs(pin.NextValidIntents))
	}
	if _, _, err := WorkflowActionDefinitionFor(ctx, s, BuiltinWorkflowRegistry(), workID, "supersede_contract"); err == nil {
		t.Fatal("supersede_contract discovery admitted during a live new-pass worker")
	}
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", refineRecoverySuccessor(4, refineRecoveryDesign), owner, operator); err == nil {
		t.Fatal("supersede_contract admitted during a live new-pass worker")
	}

	// The live worker route itself stays intact: the attempt completes its
	// report and the accept disposes it.
	lane := reviewGateLane(t, "review")
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		workerCompleteEventForLane(workID, "refine-completed-refine-live-"+workID, liveAttempt, lane, time.Unix(43, 0).UTC()),
	}}); err != nil {
		t.Fatal(err)
	}
	acceptor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/refine-acceptor", SessionRef: "session/" + workID + "-acceptor", ActorClass: ActorAgent}
	if err := acceptRefineResult(t, s, workID, liveAttempt, liveEpoch, acceptor); err != nil {
		t.Fatalf("accept the live new-pass attempt: %v", err)
	}
}

func workPinIntentForActionID(t *testing.T, pin WorkPin, actionID string) WorkPinIntent {
	t.Helper()
	for _, intent := range pin.NextValidIntents {
		if intent.ActionID == actionID {
			return intent
		}
	}
	t.Fatalf("pin carries no %s intent; intents = %v", actionID, intentActionIDs(pin.NextValidIntents))
	return WorkPinIntent{}
}

// A same-pass worker result an accept already dispositioned is not offered for
// rejection again. The reachable window is a checkpoint step, where
// accept_worker_evidence holds the step instead of advancing it.
func TestAcceptedCheckpointReviewIsNotRejectable(t *testing.T) {
	const workID = "refine-accepted-checkpoint-review"
	ctx := context.Background()
	fixture := seedWorkflowReturnRouteFixtureRequiring(t, workID, "workflow.break_fix", "verify", []string{"verification", "review"}, []string{"verification", "artifact"})
	s := fixture.store
	dispatchVerifyReviewAttempt(t, fixture, workID)
	var epoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionStarted).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	attemptID := "attempt:" + workID + ":verify-review"
	if err := runVerdictActionAs(t, s, workID, "accept_worker_evidence", json.RawMessage(`{"attempt_id":"`+attemptID+`","attempt_epoch":`+fmt.Sprint(epoch)+`}`), 0, fixture.owner); err != nil {
		t.Fatalf("accept worker evidence: %v", err)
	}
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	available, err := workflowRejectedWorkerResultAvailable(ctx, s.DatabaseForTesting(), workID, registered.Definition, "verify", "test", 0)
	if err != nil {
		t.Fatal(err)
	}
	if available {
		t.Fatal("reject_worker_result is available for a result accept_worker_evidence already dispositioned")
	}
	pin, err := ReadWorkPin(ctx, s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if workPinContainsAction(pin.NextValidIntents, "reject_worker_result") {
		t.Fatalf("pin advertises reject_worker_result for an accepted same-pass result; intents = %v", intentActionIDs(pin.NextValidIntents))
	}
}

// A retry dispatched after a recorded same-pass failure closes contract
// correction again while the retry is live: the retry's own fenced start is
// the pass boundary, so the earlier failure no longer opens the route
// (CD-0133 D1).
func TestReturnedRefineSamePassRetryClosesCorrection(t *testing.T) {
	const workID = "refine-same-pass-retry-correction"
	ctx := context.Background()
	fixture := seedReturnedRefineCorrection(t, workID)
	s, owner, operator := fixture.store, fixture.owner, fixture.operator
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", refineRecoverySuccessor(3, refineRecoveryDesign), owner, operator); err != nil {
		t.Fatalf("supersede_contract with design_record at the returned refine: %v", err)
	}
	failedAttempt, failedEpoch, err := dispatchRefineAttemptOnly(t, fixture, workID, "refine-same-fail", nil)
	if err != nil {
		t.Fatalf("same-pass dispatch with a current design: %v", err)
	}
	failReviewWorkerAttempt(t, s, workID, failedAttempt, reviewGateLane(t, "review"))
	applyRecordWorkerFailureForTest(t, s, workID, owner, failedAttempt, failedEpoch, verdictItemVersion(t, s, workID), "same-pass-failure:"+workID)
	if _, _, err := dispatchRefineAttemptOnly(t, fixture, workID, "refine-same-retry", refineRetryCorrection(t, s, workID)); err != nil {
		t.Fatalf("same-pass retry dispatch after the recorded failure: %v", err)
	}
	pin, err := ReadWorkPin(ctx, s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if workPinContainsAction(pin.NextValidIntents, "supersede_contract") {
		t.Fatalf("pin advertises supersede_contract during a live same-pass retry; intents = %v", intentActionIDs(pin.NextValidIntents))
	}
	if _, _, err := WorkflowActionDefinitionFor(ctx, s, BuiltinWorkflowRegistry(), workID, "supersede_contract"); err == nil {
		t.Fatal("supersede_contract discovery admitted during a live same-pass retry")
	}
}

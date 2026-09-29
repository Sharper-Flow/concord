package store

import (
	"context"
	"encoding/json"
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

// dispatchRefineAttempt dispatches a review lane at refine through the
// dispatch_worker action, completes its report, and returns the attempt epoch.
func dispatchRefineAttempt(t *testing.T, fixture workflowReturnRouteFixture, workID, label string) (string, int64, error) {
	t.Helper()
	ctx := context.Background()
	lane := reviewGateLane(t, "review")
	laneVersion, laneDigest := registeredLaneIdentity(t, "review")
	attemptID := "attempt:" + workID + ":" + label
	packet := joinPacketFor(workID, "refine", attemptID, "review", laneVersion, laneDigest)
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
	if err := ApplyOperation(ctx, fixture.store, Operation{Events: []Event{{
		EventID: "refine-completed-" + label + "-" + workID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID,
		Actor: "worker:test", OccurredAt: time.Unix(41, 0).UTC(), PayloadVersion: 1,
		Payload: mustJSONValue(WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion}),
	}}}); err != nil {
		t.Fatal(err)
	}
	return attemptID, epoch, nil
}

func TestReproAcceptanceCorrectionWithoutDesignStrandsRefineDispatch(t *testing.T) {
	const workID = "refine-design-recovery"
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
		{EventID: "completed-" + workID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(31, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(WorkerCompletedPayload{AttemptID: executionAttempt, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion})},
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
	if err := runVerdictActionAs(t, s, workID, "accept_worker_result", json.RawMessage(`{"attempt_id":"`+reviewAttempt+`","attempt_epoch":`+fmt.Sprint(reviewEpoch)+`}`), 0, acceptor); err != nil {
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

	// Step 1: correction at acceptance without design_record.
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", refineRecoverySuccessor(2, ""), owner, operator); err != nil {
		t.Fatalf("step 1: supersede_contract without design_record at acceptance: %v", err)
	}
	_, stale, _ := readCurrentWorkflowDesign(ctx, s.DatabaseForTesting(), workID)
	t.Logf("step 1: supersede at acceptance admitted; design stale=%v", stale)

	// Step 2: non-ok verdict and premise confirmation return to refine.
	verdict := json.RawMessage(`{"contract_version":2,"predicate_id":"predicate:return-route","verdict_kind":"outcome_mismatch","evaluation_evidence":["evidence:return-route-verification"],"incomparable_with_approved":true}`)
	if err := runIssue933OperatorAction(t, s, workID, "record_verdict", verdict, owner, operator); err != nil {
		t.Fatalf("step 2: record_verdict: %v", err)
	}
	if err := runIssue933OperatorAction(t, s, workID, "confirm_premise", json.RawMessage(`{"contract_version":2}`), owner, operator); err != nil {
		t.Fatalf("step 2: confirm_premise: %v", err)
	}
	if step := currentStep(t, s, workID); step != "refine" {
		t.Fatalf("step 2: step = %q, want refine", step)
	}

	_, _, discovered := WorkflowActionDefinitionFor(ctx, s, BuiltinWorkflowRegistry(), workID, "supersede_contract")
	t.Logf("refine: supersede_contract discovery: %v", discovered)
	_, _, dispatchErr := dispatchRefineAttempt(t, fixture, workID, "refine-stale")
	t.Logf("step 3: dispatch_worker at refine: %v", dispatchErr)
	t.Logf("step 4: supersede_contract with design_record at refine: %v", runIssue933OperatorAction(t, s, workID, "supersede_contract", refineRecoverySuccessor(3, refineRecoveryDesign), owner, operator))
	t.Logf("step 5: reject_worker_result on the accepted review: %v", runVerdictActionAs(t, s, workID, "reject_worker_result", json.RawMessage(`{"attempt_id":"`+reviewAttempt+`","attempt_epoch":`+fmt.Sprint(reviewEpoch)+`,"diagnosis":"d","strategy":"s","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:return-route-verification"]}`), 0, owner))
	t.Logf("step 6: start_refine with design_record: %v", runVerdictActionAs(t, s, workID, "start_refine", json.RawMessage(`{"design_record":`+refineRecoveryDesign+`}`), 0, owner))

	t.Logf("probe: plain start_refine: %v", runVerdictActionAs(t, s, workID, "start_refine", json.RawMessage(`{}`), 0, owner))
	_, _, discovered = WorkflowActionDefinitionFor(ctx, s, BuiltinWorkflowRegistry(), workID, "supersede_contract")
	t.Logf("probe: supersede_contract discovery after start_refine: %v", discovered)
	t.Logf("probe: supersede_contract with design_record after start_refine: %v", runIssue933OperatorAction(t, s, workID, "supersede_contract", refineRecoverySuccessor(3, refineRecoveryDesign), owner, operator))
	_, _, final := dispatchRefineAttempt(t, fixture, workID, "refine-after")
	if final != nil {
		t.Fatalf("no admitted dispatch at refine: %v", final)
	}
}

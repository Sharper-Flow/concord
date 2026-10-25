package store

import (
	"context"
	"encoding/json"
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
func dispatchRefineAttemptOnly(t *testing.T, fixture workflowReturnRouteFixture, workID, label string, correction *WorkflowCorrectionContext) (string, int64, *WorkerJobBinding, error) {
	t.Helper()
	ctx := context.Background()
	lane := reviewGateLane(t, "review")
	laneDigest := registeredLaneIdentity(t, "review")
	attemptID := "attempt:" + workID + ":" + label
	packet := joinPacketFor(t, fixture.store, workID, "refine", attemptID, "review", laneDigest)
	binding := attachReadyWorkerJobIfJobCapable(t, fixture.store, workID, "refine", fixture.owner, packet)
	if correction != nil {
		packet["inputs"].(map[string]any)["correction"] = correction
	}
	if _, err := dispatchJoinAttempt(ctx, t, fixture.store, workID, readWorkVersion(t, fixture.store, workID), fixture.owner, packet); err != nil {
		return attemptID, 0, binding, err
	}
	var packetDigest string
	var epoch int64
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.worker_packet_digest'), json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionCompleted).Scan(&packetDigest, &epoch); err != nil {
		t.Fatal(err)
	}
	laneDispatch := Event{EventID: "refine-dispatch-" + label + "-" + workID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(40, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion(WorkerDispatched), Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion, PacketDigest: packetDigest, WorkerJob: binding})}
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
	return attemptID, epoch, binding, nil
}

// dispatchRefineAttempt dispatches a review lane at refine through the
// dispatch_worker action, completes its report, and returns the attempt epoch.
func dispatchRefineAttempt(t *testing.T, fixture workflowReturnRouteFixture, workID, label string) (string, int64, error) {
	t.Helper()
	attemptID, epoch, binding, err := dispatchRefineAttemptOnly(t, fixture, workID, label, nil)
	if err != nil {
		return attemptID, epoch, err
	}
	lane := reviewGateLane(t, "review")
	if err := ApplyOperation(context.Background(), fixture.store, Operation{Events: []Event{
		jobBoundCompletionEvent(workID, "refine-completed-"+label+"-"+workID, attemptID, lane, time.Unix(41, 0).UTC(), binding, workerFixtureDirectory(t, fixture.store, workID)),
	}}); err != nil {
		t.Fatal(err)
	}
	return attemptID, epoch, nil
}

// jobBoundCompletionEvent builds the worker.completed report event the
// dispatch helpers emit, attaching the recorded worker-job binding the
// dispatch authorization recorded so the fold's end-to-end check passes on
// job-capable pins. The helper carries no binding on legacy pins.
func jobBoundCompletionEvent(workID, eventID, attemptID string, lane LaneDefinition, occurredAt time.Time, binding *WorkerJobBinding, directory string) Event {
	payload := WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion, WorkerDirectory: directory, WorkerJob: binding, EvidenceOrigin: WorkerEvidenceReported}
	if len(lane.RequiredReportBlocks) > 0 {
		payload.Review = reviewBlockFixture()
		payload.Review.Disposition = "ready_for_final_proof"
		payload.Review.Findings = []WorkerReviewFinding{}
	}
	payload.Evidence = reportedLaneEvidenceForTest(lane, payload.Review)
	return Event{EventID: eventID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: occurredAt, PayloadVersion: CurrentEventPayloadVersion(WorkerCompleted), Payload: mustJSONValue(payload)}
}

func reportedLaneEvidenceForTest(lane LaneDefinition, review *WorkerReviewBlock) []WorkerReportEvidence {
	evidence := make([]WorkerReportEvidence, 0, len(lane.EvidenceObligations))
	for _, obligation := range lane.EvidenceObligations {
		if obligation == "severity" && review != nil {
			continue
		}
		evidence = append(evidence, WorkerReportEvidence{Obligation: obligation, Detail: "synthetic reported evidence for " + obligation})
	}
	return evidence
}

// refineRecoveryRefineEntry returns the refinement entry action the pinned
// definition declares: derived-start heads open refine through the qualifying
// checkpoint, and released pins keep the callable start.
func refineRecoveryRefineEntry(t *testing.T, s *Store, workID string) string {
	t.Helper()
	registered, err := VerifyWorkflowInstanceDefinition(context.Background(), s, BuiltinWorkflowRegistry(), workID)
	if err != nil {
		t.Fatal(err)
	}
	if workflowDerivedStepStartActive(registered.Definition) {
		return "checkpoint_refine"
	}
	return "start_refine"
}

// failReviewWorkerAttempt records the WorkerFailed event for a dispatched
// review lane attempt.
func failReviewWorkerAttempt(t *testing.T, s *Store, workID, attemptID string, lane LaneDefinition) {
	t.Helper()
	fail := Event{EventID: "refine-failed-" + attemptID, Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(42, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion(WorkerFailed), Payload: mustJSONValue(WorkerFailedPayload{AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), FailureKind: WorkerFailureWorkerError, Detail: "the review lane reported a failure"})}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{fail}}); err != nil {
		t.Fatal(err)
	}
}

// seedRefineFirstPass seeds the design, the accepted execution pass, and the
// started refine step of an implementation item.
func seedRefineFirstPass(t *testing.T, workID string) (workflowReturnRouteFixture, WorkflowActor) {
	t.Helper()
	ctx := context.Background()
	fixture := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, attributionInactiveWorkflowHeadForTest(t, "workflow.implementation"), "execution", []string{"verification"}, []string{"verification", "review", "artifact"})
	s, owner := fixture.store, fixture.owner
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, workID)
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

	reviewSubjectClaim(t, s, workID)
	seedAcceptedImplementationDeliveryAt(t, s, workID, owner, "execution")
	parkWorkflowInstanceAt(t, s, workID, "refine")
	acceptor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/refine-acceptor", SessionRef: "session/" + workID + "-acceptor", ActorClass: ActorAgent}
	if err := runVerdictActionAs(t, s, workID, refineRecoveryRefineEntry(t, s, workID), json.RawMessage(`{}`), 0, acceptor); err != nil {
		t.Fatalf("start refine: %v", err)
	}
	return fixture, acceptor
}

// returnRefineFromAcceptance corrects the contract at acceptance without a
// design record, records a mismatch verdict, drives a request_correction to
// the declared recovery-route target (execution) with a fresh dispatch +
// complete + accept cycle, then confirms the premise so the failure edge
// returns the item to refine. The caller then repairs the stale design with
// a supersede that carries the replacement design.
func returnRefineFromAcceptance(t *testing.T, fixture workflowReturnRouteFixture, workID string, _, operator WorkflowActor) {
	t.Helper()
	s, owner := fixture.store, fixture.owner
	if step := currentStep(t, s, workID); step != "acceptance" {
		t.Fatalf("step = %q, want acceptance before the return", step)
	}
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", refineRecoverySuccessor(2, ""), owner, operator); err != nil {
		t.Fatalf("supersede_contract without design_record at acceptance: %v", err)
	}
	verdict := json.RawMessage(`{"contract_version":2,"verdicts":[{"predicate_id":"predicate:return-route","verdict_kind":"outcome_mismatch","evaluation_evidence":["evidence:return-route-verification"],"incomparable_with_approved":true}]}`)
	if err := runIssue933OperatorAction(t, s, workID, "record_verdict", verdict, owner, operator); err != nil {
		t.Fatalf("record_verdict: %v", err)
	}
	if _, err := WorkflowActorRef(owner); err != nil {
		t.Fatal(err)
	}
	correction := json.RawMessage(`{"diagnosis":"the corrected contract needs re-production at the route target","strategy":"repeat the implementation external effect with the new contract","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:return-route-verification"]}`)
	if err := runIssue933OperatorAction(t, s, workID, "request_correction", correction, owner, operator); err != nil {
		t.Fatalf("request_correction between verdict and confirm_premise: %v", err)
	}
	_ = acceptReturnRouteWorkerLabeled(t, fixture, workID, "return-refine-failure")
	if err := runIssue933OperatorAction(t, s, workID, "confirm_premise", json.RawMessage(`{"contract_version":2}`), owner, operator); err != nil {
		t.Fatalf("confirm_premise: %v", err)
	}
	if step := currentStep(t, s, workID); step != "refine" {
		t.Fatalf("step = %q, want refine after the return edge", step)
	}
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", refineRecoverySuccessor(3, refineRecoveryDesign), owner, operator); err != nil {
		t.Fatalf("supersede_contract with design_record at the returned refine: %v", err)
	}
	design, err := readCurrentWorkflowDesign(context.Background(), s.DatabaseForTesting(), workID)
	if err != nil {
		t.Fatal(err)
	}
	var expected workflowDesignContent
	if err := json.Unmarshal([]byte(refineRecoveryDesign), &expected); err != nil {
		t.Fatal(err)
	}
	if design == nil || design.Approach != expected.Approach || string(mustJSONValue(design.Decisions)) != string(mustJSONValue(expected.Decisions)) || string(mustJSONValue(design.TouchedRefs)) != string(mustJSONValue(expected.TouchedRefs)) {
		t.Fatalf("current design = %+v, want the returned-refine replacement %+v", design, expected)
	}
}

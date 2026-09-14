package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type workflowReturnRouteFixture struct {
	store    *Store
	owner    WorkflowActor
	operator WorkflowActor
}

func seedWorkflowReturnRouteFixture(t *testing.T, workID, definitionRef, verdictStep string) workflowReturnRouteFixture {
	t.Helper()
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	seedIssue31DomainRegistry(t, s)

	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	operator := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/operator", SessionRef: "session/" + workID + "-operator", ActorClass: ActorOperator}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	operatorRef, err := WorkflowActorRef(operator)
	if err != nil {
		t.Fatal(err)
	}
	registered, err := BuiltinWorkflowDefinitionForRef(definitionRef)
	if err != nil {
		t.Fatal(err)
	}

	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: registered, Actor: owner, Now: time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	version := int64(4)
	nextEvent := func(event Event) Event {
		version++
		return event
	}
	actorEvent := func(id string, actor WorkflowActor, actorRef string) Event {
		return nextEvent(workflowEventWithActor(id, WorkflowActorRecorded, workID, actorRef, map[string]any{
			"work_id": workID, "expected_version": version, "resulting_version": version + 1,
			"actor_ref": actorRef, "principal_ref": actor.PrincipalRef, "client_ref": actor.ClientRef,
			"agent_ref": actor.AgentRef, "session_ref": actor.SessionRef, "actor_class": string(actor.ActorClass),
		}))
	}
	events := []Event{actorEvent("return-operator-"+workID, operator, operatorRef)}
	contract := workflowEventWithActor("return-contract-"+workID, WorkflowContractApproved, workID, ownerRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"contract_version": 1, "premise": "deliver the checked change", "outcome_kind": "check",
		"outcome_predicates": []map[string]any{{
			"predicate_id": "predicate:return-route", "ordinal": 0, "outcome_kind": "check",
			"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:return-route", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"},
		}},
		"required_evidence": []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{},
		"law_revisions": []WorkflowLawRevision{}, "law_boundary_version": 1, "rigor_class": "prototype_internal",
		"consequence_class": "internal_sqlite", "architecture_binding": WorkflowArchitectureBinding{
			DomainRegistryContentHash: "sha256:" + strings.Repeat("b", 64), HomeDomainID: "root", AffectedDomainIDs: []string{"root"},
			DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{},
			VerificationObligations: []WorkflowVerificationObligation{},
		},
	})
	contract.PayloadVersion = 3
	events = append(events, nextEvent(contract))
	for _, kind := range []string{"verification", "review", "artifact"} {
		evidenceRef := "evidence:return-route-" + kind
		seedWorkflowAuthority(t, s, "return-authority-"+kind+"-"+workID, workID, "principal/return-route-"+kind, "request/return-route-"+kind, []string{evidenceRef})
		events = append(events, nextEvent(workflowEventWithActor("return-evidence-"+kind+"-"+workID, WorkflowEvidenceBound, workID, ownerRef, map[string]any{
			"work_id": workID, "expected_version": version, "resulting_version": version + 1, "evidence_kind": kind,
			"immutable_subject_ref": evidenceRef, "producer_id": "principal/return-route-" + kind, "producer_run_ref": "return-authority-" + kind + "-" + workID,
			"producer_watermark": "request/return-route-" + kind, "observed_at": "2026-09-12T00:00:00Z",
		})))
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{
		Events:           events,
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 4},
	}); err != nil {
		t.Fatal(err)
	}
	if err := advanceWorkflowTestInstanceToStep(ctx, s, workID, verdictStep, ownerRef); err != nil {
		t.Fatal(err)
	}
	return workflowReturnRouteFixture{store: s, owner: owner, operator: operator}
}

func TestIssue1062PersistentMismatchReturnsImplementationAcceptanceToRefine(t *testing.T) {
	testWorkflowReturnRoute(t, "return-route-implementation", "workflow.implementation", "acceptance")
}

func TestIssue1062PersistentMismatchReturnsBreakFixVerificationToRefine(t *testing.T) {
	testWorkflowReturnRoute(t, "return-route-break-fix", "workflow.break_fix", "verify")
}

func TestNonOKVerdictCorrectionReturnsImplementationToExecution(t *testing.T) {
	const workID = "return-route-verdict-correction"
	ctx := context.Background()
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.implementation", "execution")
	s, owner := fixture.store, fixture.owner
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	reviewer := acceptReturnRouteWorker(t, fixture, workID, ownerRef)
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:return-route","verdict_kind":"outcome_mismatch","evaluation_evidence":["evidence:return-route-verification"],"incomparable_with_approved":true}`), 0, reviewer); err != nil {
		t.Fatalf("record non-ok verdict: %v", err)
	}
	operator := fixture.operator
	payload := json.RawMessage(`{"diagnosis":"the delivered subject does not satisfy the approved predicate","strategy":"repeat the implementation external effect","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:return-route-verification"]}`)
	if err := runIssue933OperatorAction(t, s, workID, "request_correction", payload, owner, operator); err != nil {
		t.Fatalf("request correction: %v", err)
	}
	if got := currentStep(t, s, workID); got != "execution" {
		t.Fatalf("step after verdict correction = %q, want execution", got)
	}
	correction, err := workflowCorrectionContextForDispatch(ctx, s.db, workID, "execution", "attempt:next")
	if err != nil {
		t.Fatal(err)
	}
	if correction == nil || correction.AttemptCount != 1 || correction.PredicateIDs[0] != "predicate:return-route" {
		t.Fatalf("dispatch correction context = %+v, want the first verdict correction", correction)
	}
	if err := WorkflowActionPreflight(ctx, s, WorkflowActionPreflightRequest{WorkID: workID, ActionID: "request_correction", Payload: payload, Actor: owner}); err == nil {
		t.Fatal("request_correction remained available before a fresh verdict")
	}
}

func TestNonOKVerdictCorrectionReturnsBreakFixToRepair(t *testing.T) {
	const workID = "return-route-break-fix-correction"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s, owner := fixture.store, fixture.owner
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	reviewer := acceptReturnRouteWorker(t, fixture, workID, ownerRef)
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:return-route","verdict_kind":"outcome_mismatch","evaluation_evidence":["evidence:return-route-verification"],"incomparable_with_approved":true}`), 0, reviewer); err != nil {
		t.Fatalf("record non-ok break-fix verdict: %v", err)
	}
	correction := json.RawMessage(`{"diagnosis":"the repaired subject still reproduces the defect","strategy":"repeat the repair external effect","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:return-route-verification"]}`)
	if err := runIssue933OperatorAction(t, s, workID, "request_correction", correction, owner, fixture.operator); err != nil {
		t.Fatalf("request break-fix correction: %v", err)
	}
	if got := currentStep(t, s, workID); got != "repair" {
		t.Fatalf("step after break-fix correction = %q, want repair", got)
	}
}

func TestVerdictCorrectionRequiresAcceptedWorkerDelivery(t *testing.T) {
	const workID = "return-route-requires-accepted-delivery"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.implementation", "acceptance")
	s := fixture.store
	ownerRef, err := WorkflowActorRef(fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	lane := BuiltinLaneDefinitions()[0]
	attemptID := "attempt:" + workID
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{{
		EventID: "dispatch-" + workID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID,
		Actor: ownerRef, OccurredAt: time.Unix(30, 0).UTC(), PayloadVersion: 2,
		Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion}),
	}}}); err != nil {
		t.Fatal(err)
	}
	reviewer := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/unaccepted-reviewer", SessionRef: "session/" + workID + "-reviewer", ActorClass: ActorAgent}
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:return-route","verdict_kind":"outcome_mismatch","incomparable_with_approved":true}`), 0, reviewer); err != nil {
		t.Fatalf("record non-ok verdict: %v", err)
	}
	err = WorkflowActionPreflight(context.Background(), s, WorkflowActionPreflightRequest{
		WorkID: workID, ActionID: "request_correction", Payload: json.RawMessage(`{"diagnosis":"missing accepted delivery","strategy":"accept a completed worker result first","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:return-route-verification"]}`), Actor: fixture.owner,
	})
	if err == nil || !strings.Contains(err.Error(), "correction request is unavailable") {
		t.Fatalf("correction without accepted delivery error=%v, want refusal", err)
	}
}

func TestVerdictCorrectionSequenceNeedsConjunctiveHealthyVerdicts(t *testing.T) {
	const workID = "return-route-conjunctive-sequence"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.implementation", "acceptance")
	s := fixture.store
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES(?,1,'predicate:second',1,'check','{"kind":"check","check_ref":"check:second","immutable_subject_ref":"commit:second","expected_result":"pass"}')`, workID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	firstReviewer := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/first-reviewer", SessionRef: "session/" + workID + "-first", ActorClass: ActorAgent}
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:return-route","verdict_kind":"outcome_mismatch","incomparable_with_approved":true}`), 0, firstReviewer); err != nil {
		t.Fatalf("record first non-ok verdict: %v", err)
	}
	secondReviewer := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/second-reviewer", SessionRef: "session/" + workID + "-second", ActorClass: ActorAgent}
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:second","verdict_kind":"ok"}`), 0, secondReviewer); err != nil {
		t.Fatalf("record second healthy verdict: %v", err)
	}
	var beforeSeq int64
	if err := db.QueryRow(`SELECT COALESCE(MAX(seq),0)+1 FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkflowVerdictRecorded).Scan(&beforeSeq); err != nil {
		t.Fatal(err)
	}
	healthySeq, err := workflowLatestComparableHealthySequence(context.Background(), s.db, workID, 1, beforeSeq)
	if err != nil {
		t.Fatal(err)
	}
	if healthySeq != 0 {
		t.Fatalf("partial healthy verdict set reset correction sequence at seq %d, want zero", healthySeq)
	}
}

func TestNonOKVerdictCorrectionReturnsTerminalImplementationToExecution(t *testing.T) {
	const workID = "return-route-terminal-verdict-correction"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.implementation", "execution")
	s, owner := fixture.store, fixture.owner
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	reviewer := acceptReturnRouteWorker(t, fixture, workID, ownerRef)
	operatorRef, err := WorkflowActorRef(fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	if err := advanceWorkflowTestInstanceToStep(context.Background(), s, workID, "release", operatorRef); err != nil {
		t.Fatalf("advance to terminal correction checkpoint: %v", err)
	}
	verdict := json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:return-route","verdict_kind":"insufficient_evidence","evaluation_evidence":["evidence:return-route-verification"],"incomparable_with_approved":true}`)
	if err := runVerdictActionAs(t, s, workID, "record_verdict", verdict, 0, reviewer); err != nil {
		t.Fatalf("record terminal non-ok verdict: %v", err)
	}
	correction := json.RawMessage(`{"diagnosis":"the delivered subject lacks the required proof","strategy":"repeat the implementation external effect with stronger evidence","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:return-route-verification"]}`)
	if err := runIssue933OperatorAction(t, s, workID, "request_correction", correction, owner, fixture.operator); err != nil {
		t.Fatalf("request terminal correction: %v", err)
	}
	if got := currentStep(t, s, workID); got != "execution" {
		t.Fatalf("step after terminal correction = %q, want execution", got)
	}
}

func acceptReturnRouteWorker(t *testing.T, fixture workflowReturnRouteFixture, workID, ownerRef string) WorkflowActor {
	t.Helper()
	ctx := context.Background()
	s := fixture.store
	lane := BuiltinLaneDefinitions()[0]
	attemptID := "attempt:" + workID
	var definitionRef string
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref FROM workflow_instances WHERE work_id=?`, workID).Scan(&definitionRef); err != nil {
		t.Fatal(err)
	}
	effectStep, startAction := "execution", "start_execution"
	if definitionRef == "workflow.break_fix" {
		effectStep, startAction = "repair", "start_repair"
	}
	version := verdictItemVersion(t, s, workID)
	start := workflowEventWithActor("start-"+workID, WorkflowActionStarted, workID, ownerRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1, "step_id": effectStep, "action_id": startAction, "attempt_epoch": 1,
		"accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": "start:" + workID, "actor_ref": ownerRef,
	})
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{start}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{{
		EventID: "dispatch-" + workID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID,
		Actor: ownerRef, OccurredAt: time.Unix(30, 0).UTC(), PayloadVersion: 2,
		Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion}),
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{{
		EventID: "completed-" + workID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID,
		Actor: "worker:test", OccurredAt: time.Unix(31, 0).UTC(), PayloadVersion: 1,
		Payload: mustJSONValue(WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion}),
	}}}); err != nil {
		t.Fatal(err)
	}
	acceptor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/return-route-acceptor", SessionRef: "session/" + workID + "-acceptor", ActorClass: ActorAgent}
	if err := runVerdictActionAs(t, s, workID, "accept_worker_result", json.RawMessage(`{"attempt_id":"`+attemptID+`","attempt_epoch":1}`), 0, acceptor); err != nil {
		t.Fatalf("accept worker result: %v", err)
	}
	if err := runVerdictActionAs(t, s, workID, "start_refine", json.RawMessage(`{}`), 0, acceptor); err != nil {
		t.Fatalf("start refinement: %v", err)
	}
	if err := runVerdictActionAs(t, s, workID, "record_delivery", json.RawMessage(`{}`), 0, acceptor); err != nil {
		t.Fatalf("record refinement delivery: %v", err)
	}
	return WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/return-route-reviewer", SessionRef: "session/" + workID + "-reviewer", ActorClass: ActorAgent}
}

func testWorkflowReturnRoute(t *testing.T, workID, definitionRef, verdictStep string) {
	t.Helper()
	ctx := context.Background()
	fixture := seedWorkflowReturnRouteFixture(t, workID, definitionRef, verdictStep)
	verdictPayload := json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:return-route","verdict_kind":"outcome_mismatch","evaluation_evidence":["evidence:return-route-verification"],"incomparable_with_approved":true}`)
	if err := runIssue933OperatorAction(t, fixture.store, workID, "record_verdict", verdictPayload, fixture.owner, fixture.operator); err != nil {
		t.Fatalf("record non-ok verdict: %v", err)
	}
	operator := fixture.operator
	if err := runIssue933OperatorAction(t, fixture.store, workID, "confirm_premise", json.RawMessage(`{"contract_version":1}`), fixture.owner, operator); err != nil {
		t.Fatalf("confirm premise: %v", err)
	}

	var currentStep string
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&currentStep); err != nil {
		t.Fatal(err)
	}
	if currentStep != "refine" {
		t.Fatalf("step after non-ok premise confirmation = %q, want refine", currentStep)
	}

	tx, err := fixture.store.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	verdicts, err := latestWorkflowVerdicts(ctx, tx, workID, 1)
	_ = tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	if len(verdicts) != 1 || verdicts[0].PredicateID != "predicate:return-route" || verdicts[0].VerdictKind != "outcome_mismatch" || !verdicts[0].IncomparableWithApproved {
		t.Fatalf("verdict after return = %+v, want the recorded non-ok verdict", verdicts)
	}

	lane := BuiltinLaneDefinitions()[0]
	packet := joinPacketFor(workID, "refine", "attempt:return-route-"+workID, lane.ID, lane.Version, lane.Digest)
	payload, err := json.Marshal(map[string]any{"attempt_id": packet["attempt_id"], "worker_packet": packet})
	if err != nil {
		t.Fatal(err)
	}
	version := verdictItemVersion(t, fixture.store, workID)
	if err := WorkflowActionPreflight(ctx, fixture.store, WorkflowActionPreflightRequest{
		WorkID: workID, ExpectedVersion: version, StepID: "refine", ActionID: "dispatch_worker", Payload: payload,
		Actor: fixture.owner, SessionWorktree: dispatchSessionWorktree(t, fixture.store, workID),
	}); err != nil {
		t.Fatalf("dispatch_worker preflight at returned refine: %v", err)
	}

	operatorRef, err := WorkflowActorRef(fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	completion := workflowEventWithActor("return-completion-"+workID, WorkflowCompleted, workID, operatorRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1, "terminal_state": "completed",
		"final_verdict_kind": "outcome_mismatch", "verdict_actor_ref": verdicts[0].VerdictActorRef, "premise_confirmed": true,
		"evidence_count": 3, "changed_refs_digest": "sha256:" + strings.Repeat("a", 64), "impact_verdict": "non-breaking",
	})
	completion.PayloadVersion = 2
	err = CompleteWorkflow(ctx, fixture.store, completion)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindOutcomeMismatch {
		t.Fatalf("completion with non-ok verdict = %v, want outcome mismatch", err)
	}
	if got := countWorkflowCompletionEvents(t, fixture.store, workID); got != 0 {
		t.Fatalf("completion guard appended %d workflow.completed events", got)
	}
}

func countWorkflowCompletionEvents(t *testing.T, s *Store, workID string) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkflowCompleted).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

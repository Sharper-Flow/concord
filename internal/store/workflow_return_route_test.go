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

func TestNonOKVerdictReturnsImplementationAcceptanceToRefine(t *testing.T) {
	testWorkflowReturnRoute(t, "return-route-implementation", "workflow.implementation", "acceptance")
}

func TestNonOKVerdictReturnsBreakFixVerificationToRefine(t *testing.T) {
	testWorkflowReturnRoute(t, "return-route-break-fix", "workflow.break_fix", "verify")
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

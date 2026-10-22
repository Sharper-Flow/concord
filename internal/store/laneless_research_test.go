package store

import (
	"context"
	"encoding/json"
	"testing"
)

// A lane-less workflow records its selecting session as the execution identity.

// seedLanelessResearchItem reproduces a production-shaped research capture:
// the actor-recorded and definition-selected events carry the same
// authenticated selector, no fenced action ever starts, and the item walks
// frame → investigate → findings with a bound report. Nothing writes
// workflow_instances directly.
func seedLanelessResearchItem(t *testing.T, workID string) (*Store, WorkflowActor) {
	t.Helper()
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/researcher", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := BuiltinWorkflowRegistry().Current("workflow.research")
	if !ok {
		t.Fatal("workflow.research v5 is not registered")
	}
	setup := []Event{
		workflowEventWithActor("laneless-actor-"+workID, WorkflowActorRecorded, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef, "agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": "agent"}),
		workflowEventWithActor("laneless-definition-"+workID, WorkflowDefinitionSelected, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "ref": definition.Definition.Ref, "version": definition.Definition.Version, "digest": definition.Digest, "work_kind": string(definition.Definition.WorkKind)}),
		workflowEventWithActor("laneless-contract-"+workID, WorkflowContractApproved, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 4, "resulting_version": 5, "contract_version": 1, "premise": "record the research report", "decision_domains": []string{}, "outcome_kind": "outcome", "outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{"report_recorded"}}, "outcome_predicates": []map[string]any{{"predicate_id": "predicate:research-report-exit", "ordinal": 0, "outcome_kind": "outcome", "outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{"report_recorded"}}}}, "required_evidence": []string{"artifact"}, "route_conventions": []string{}, "spec_mandate": []string{}, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite"}),
		workflowActionCompletedFixture("laneless-approval-"+workID, workID, ownerRef, 5, "frame", "approve_contract"),
		workflowActionCompletedFixture("laneless-finding-"+workID, workID, ownerRef, 6, "investigate", "record_finding"),
	}
	setup[2].PayloadVersion = workflowContractDecisionDomainsPayloadVersion
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: setup, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	if err := runVerdictActionAs(t, s, workID, "record_report", json.RawMessage(`{"evidence_kind":"artifact","immutable_subject_ref":"evidence:research-report"}`), 0, owner); err != nil {
		t.Fatalf("record research report: %v", err)
	}
	if err := runVerdictActionAs(t, s, workID, "record_conclusion", json.RawMessage(`{}`), 0, owner); err != nil {
		t.Fatalf("record research conclusion: %v", err)
	}
	return s, owner
}

// The definition-selected fold itself pins the selecting session: no other
// write path is needed for a lane-less instance to name an executor.
func TestDefinitionSelectedPinsSelectingActorAsExecutor(t *testing.T) {
	t.Parallel()
	const workID = "issue970-selector-pinned"
	s, owner := seedLanelessResearchItem(t, workID)
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	var executing string
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(execution_actor_ref,'') FROM workflow_instances WHERE work_id=?`, workID).Scan(&executing); err != nil {
		t.Fatal(err)
	}
	if executing != ownerRef {
		t.Fatalf("execution_actor_ref=%q, want the selecting actor %q", executing, ownerRef)
	}
	if step := currentStep(t, s, workID); step != "conclude" {
		t.Fatalf("current_step=%q, want conclude", step)
	}
}

// Missing execution identity derives from the selecting actor's recorded event.
func TestMissingExecutionProjectionDerivesSelectingIdentity(t *testing.T) {
	t.Parallel()
	const workID = "issue970-legacy-derivation"
	s, owner := seedLanelessResearchItem(t, workID)

	// Recreate the empty-executor projection: an instance whose executor was never
	// assigned.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE workflow_instances SET execution_actor_ref=NULL WHERE work_id=?; DELETE FROM fold_guard`, workID); err != nil {
		t.Fatal(err)
	}

	agentRef, sessionRef, found, err := WorkflowExecutingIdentity(context.Background(), s, workID)
	if err != nil || !found {
		t.Fatalf("executing identity found=%v err=%v, want the derived selector", found, err)
	}
	if agentRef != owner.AgentRef || sessionRef != owner.SessionRef {
		t.Fatalf("derived identity=%q/%q, want the selector %q/%q", agentRef, sessionRef, owner.AgentRef, owner.SessionRef)
	}
}

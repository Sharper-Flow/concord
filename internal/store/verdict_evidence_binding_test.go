package store

import (
	"context"
	"encoding/json"
	"testing"
)

// seedLanelessResearchItem records a current research report and conclusion
// without a worker dispatch, so the WorkPin evidence read has a bound artifact.
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
		t.Fatal("current workflow.research is not registered")
	}
	setup := []Event{
		workflowEventWithActor("laneless-actor-"+workID, WorkflowActorRecorded, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef, "agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": "agent"}),
		workflowEventWithActor("laneless-definition-"+workID, WorkflowDefinitionSelected, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "ref": definition.Definition.Ref, "digest": definition.Digest, "work_kind": string(definition.Definition.WorkKind)}),
		workflowEventWithActor("laneless-contract-"+workID, WorkflowContractApproved, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 4, "resulting_version": 5, "contract_version": 1, "premise": "record the research report", "decision_domains": []string{}, "outcome_kind": "outcome", "outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{"report_recorded"}}, "outcome_predicates": []map[string]any{{"predicate_id": "predicate:research-report-exit", "ordinal": 0, "outcome_kind": "outcome", "outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{"report_recorded"}}}}, "required_evidence": []string{"artifact"}, "route_conventions": []string{}, "spec_mandate": []string{}, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite"}),
		workflowActionCompletedFixture("laneless-approval-"+workID, workID, ownerRef, 5, "frame", "approve_contract"),
		workflowActionCompletedFixture("laneless-finding-"+workID, workID, ownerRef, 6, "investigate", "record_finding"),
	}
	setup[2].PayloadVersion = eventKindRegistry[WorkflowContractApproved].CurrentVersion
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

// The WorkPin exposes the bound evidence set with kinds at a step that
// declares record_verdict, and stays empty at a step that does not.
func TestWorkPinExposesBoundVerdictEvidenceAtVerdictSteps(t *testing.T) {
	t.Parallel()
	const workID = "issue974-pin-evidence"
	s, _ := seedLanelessResearchItem(t, workID)

	pin, err := ReadWorkPin(context.Background(), s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if !stepDeclaresActionByName(t, s, workID, pin.Step, "record_verdict") {
		t.Fatalf("fixture step %q must declare record_verdict", pin.Step)
	}
	if len(pin.VerdictEvidence) == 0 {
		t.Fatal("verdict_evidence is empty at a record_verdict step")
	}
	found := map[string]string{}
	for _, entry := range pin.VerdictEvidence {
		if entry.EvidenceKind == "" || entry.ImmutableSubjectRef == "" {
			t.Fatalf("verdict_evidence entry %+v carries an empty kind or ref", entry)
		}
		found[entry.ImmutableSubjectRef] = entry.EvidenceKind
	}
	if kind, ok := found["evidence:research-report"]; !ok || kind != "artifact" {
		t.Fatalf("verdict_evidence %v omits the artifact-bound research report", pin.VerdictEvidence)
	}

	// A step that does not declare record_verdict exposes no set.
	const earlyWork = "issue974-pin-no-verdict-step"
	early := seedLanelessResearchItemAtFrame(t, earlyWork)
	earlyPin, err := ReadWorkPin(context.Background(), early, earlyWork)
	if err != nil {
		t.Fatal(err)
	}
	if stepDeclaresActionByName(t, early, earlyWork, earlyPin.Step, "record_verdict") {
		t.Fatalf("fixture step %q must not declare record_verdict", earlyPin.Step)
	}
	if len(earlyPin.VerdictEvidence) != 0 {
		t.Fatalf("verdict_evidence %v must stay empty at a non-verdict step", earlyPin.VerdictEvidence)
	}
}

// seedLanelessResearchItemAtFrame pins a research instance that has only
// framed its question: it sits on a step that does not declare
// record_verdict.
func seedLanelessResearchItemAtFrame(t *testing.T, workID string) *Store {
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
		workflowEventWithActor("frame-actor-"+workID, WorkflowActorRecorded, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef, "agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": "agent"}),
		workflowEventWithActor("frame-definition-"+workID, WorkflowDefinitionSelected, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "ref": definition.Definition.Ref, "digest": definition.Digest, "work_kind": string(definition.Definition.WorkKind)}),
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: setup, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	return s
}

func stepDeclaresActionByName(t *testing.T, s *Store, workID, stepID, actionID string) bool {
	t.Helper()
	var pin WorkflowDefinitionPin
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&pin.Ref, &pin.Digest); err != nil {
		t.Fatal(err)
	}
	registered, err := VerifyWorkflowDefinitionPin(BuiltinWorkflowRegistry(), pin)
	if err != nil {
		t.Fatal(err)
	}
	return stepDeclaresAction(registered.Definition, stepID, actionID)
}

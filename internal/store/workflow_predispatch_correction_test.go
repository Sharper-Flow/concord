package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestPredispatchContractCorrectionSurvivesRebuild(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	const workID = "predispatch-replay"
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/predispatch", ActorClass: ActorAgent}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	definition := workflowFixtureDefinition(t, 2)
	events := []Event{
		workflowEvent("predispatch-owner", WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef, "agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": "agent"}),
		workflowEvent("predispatch-definition", WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "ref": definition.Definition.Ref, "version": definition.Definition.Version, "digest": definition.Digest, "work_kind": string(definition.Definition.WorkKind)}),
		workflowActionCompletedFixture("predispatch-proposal", workID, ownerRef, 4, "proposal", "record_proposal"),
		workflowActionCompletedFixture("predispatch-discovery", workID, ownerRef, 5, "discovery", "record_discovery"),
		workflowActionCompletedFixture("predispatch-design", workID, ownerRef, 6, "design", "record_design"),
		workflowEventWithActor("predispatch-contract", WorkflowContractApproved, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 7, "resulting_version": 8, "contract_version": 1, "premise": "original scope", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:original", "expected_result": "pass"}, "required_evidence": []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{}, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite"}),
		workflowActionCompletedFixture("predispatch-approved", workID, ownerRef, 8, "planning", "approve_contract"),
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: events, ExpectedVersions: workVersion(workID, 2)}); err != nil {
		t.Fatal(err)
	}
	var unsupportedDesign map[string]any
	if err := json.Unmarshal(issue1013SuccessorContract(), &unsupportedDesign); err != nil {
		t.Fatal(err)
	}
	unsupportedDesign["design_record"] = map[string]any{
		"approach":     "A design for work without a typed design",
		"decisions":    []map[string]any{{"id": "decision:unsupported", "question": "Which approach?", "choice": "new", "rationale": "Synthetic input", "rejected": []string{}}},
		"touched_refs": []string{"src/work.go"},
	}
	raw, err := json.Marshal(unsupportedDesign)
	if err != nil {
		t.Fatal(err)
	}
	version := verdictItemVersion(t, s, workID)
	err = runIssue933OperatorAction(t, s, workID, "supersede_contract", raw, owner, operatorVerdictActor(t, workID))
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidPayload {
		t.Fatalf("designless workflow acquired a design: %v", err)
	}
	if verdictItemVersion(t, s, workID) != version {
		t.Fatal("refused design addition changed work")
	}
	active, err := s.ActiveWorkflowContract(ctx, workID)
	if err != nil || active.Version != 1 {
		t.Fatalf("refused design addition partially changed the contract: %+v err=%v", active, err)
	}
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", issue1013SuccessorContract(), owner, operatorVerdictActor(t, workID)); err != nil {
		t.Fatalf("correct before dispatch: %v", err)
	}
	before, err := ReadWorkPin(ctx, s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatalf("rebuild corrected contract: %v", err)
	}
	after, err := ReadWorkPin(ctx, s, workID)
	if err != nil || after.Version != before.Version || after.Step != before.Step || after.Attempt != nil {
		t.Fatalf("rebuild changed execution identity: before=%+v after=%+v err=%v", before, after, err)
	}
	contract, err := s.ActiveWorkflowContract(ctx, workID)
	if err != nil || contract.Version != 2 || contract.Premise != "corrected predicate subject" {
		t.Fatalf("rebuild lost successor: %+v err=%v", contract, err)
	}
	entry, err := VerifyWorkflowInstanceDefinition(ctx, s, BuiltinWorkflowRegistry(), workID)
	if err != nil || entry.Digest != definition.Digest {
		t.Fatalf("definition changed across correction/replay: %s err=%v", entry.Digest, err)
	}
}

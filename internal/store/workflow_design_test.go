package store

import (
	"context"
	"encoding/json"
	"testing"
)

func TestContractDesignCorrectionSurvivesRebuild(t *testing.T) {
	t.Run("invalidated", func(t *testing.T) { testContractDesignCorrectionRebuild(t, false) })
	t.Run("replaced", func(t *testing.T) { testContractDesignCorrectionRebuild(t, true) })
}

func testContractDesignCorrectionRebuild(t *testing.T, replace bool) {
	t.Helper()
	ctx := context.Background()
	s := openTemp(t)
	const workID = "design-replay"
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	base, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 6)
	if !ok {
		t.Fatal("typed design workflow is not registered")
	}
	shape := workflowFixtureShape(base.Definition, 6)
	shape.Ref = "workflow.test_design_correction"
	definition, err := BuiltinWorkflowRegistry().Register(shape)
	if err != nil {
		t.Fatal(err)
	}
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/design-replay", ActorClass: ActorAgent}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	decisions := []map[string]any{{"id": "decision:approach", "question": "Which approach?", "choice": "original", "rationale": "Matches the original scope", "rejected": []string{}}}
	events := []Event{
		workflowEvent("design-owner", WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef, "agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": "agent"}),
		workflowEvent("design-definition", WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "ref": definition.Definition.Ref, "version": definition.Definition.Version, "digest": definition.Digest, "work_kind": string(definition.Definition.WorkKind)}),
		workflowActionCompletedFixture("design-proposal", workID, ownerRef, 4, "proposal", "record_proposal"),
		workflowActionCompletedFixture("design-discovery", workID, ownerRef, 5, "discovery", "record_discovery"),
		workflowEventWithActor("design-original", WorkflowDesignRecorded, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 6, "resulting_version": 7, "approach": "Original approach", "decisions": decisions, "touched_refs": []string{"src/original.go"}}),
		workflowActionCompletedFixture("design-recorded", workID, ownerRef, 7, "design", "record_design"),
		workflowEventWithActor("design-contract", WorkflowContractApproved, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 8, "resulting_version": 9, "contract_version": 1, "premise": "original scope", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:original", "expected_result": "pass"}, "required_evidence": []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{}, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite"}),
		workflowActionCompletedFixture("design-approved", workID, ownerRef, 9, "planning", "approve_contract"),
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: events, ExpectedVersions: workVersion(workID, 2)}); err != nil {
		t.Fatal(err)
	}
	operator := operatorVerdictActor(t, workID)
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", issue1013SuccessorContract(), owner, operator); err != nil {
		t.Fatal(err)
	}
	stale, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
	if err != nil || stale.DesignRecord != nil || stale.Contract == nil || stale.Contract.Version != 2 {
		t.Fatalf("correction retained the obsolete design: %+v, %v", stale.DesignRecord, err)
	}
	if !replace {
		if err := RebuildFromLog(ctx, s); err != nil {
			t.Fatal(err)
		}
		rebuilt, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
		if err != nil || rebuilt.DesignRecord != nil || rebuilt.Contract == nil || rebuilt.Contract.Version != 2 {
			t.Fatalf("replay resurrected the obsolete design: %+v, %v", rebuilt.DesignRecord, err)
		}
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_design_records WHERE work_id=?`, workID).Scan(&count); err != nil || count != 1 {
			t.Fatalf("replay lost the historical design: rows=%d err=%v", count, err)
		}
		return
	}
	var successor map[string]any
	if err := json.Unmarshal(issue1013SuccessorContract(), &successor); err != nil {
		t.Fatal(err)
	}
	successor["contract_version"] = 3
	decisions[0]["choice"] = "replacement"
	successor["design_record"] = map[string]any{"approach": "Replacement approach", "decisions": decisions, "touched_refs": []string{"src/replacement.go"}}
	raw, err := json.Marshal(successor)
	if err != nil {
		t.Fatal(err)
	}
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", raw, owner, operator); err != nil {
		t.Fatal(err)
	}
	before, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
	if err != nil || before.DesignRecord == nil || before.DesignRecord.Approach != "Replacement approach" {
		t.Fatalf("replacement design is not current: %+v, %v", before.DesignRecord, err)
	}
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatal(err)
	}
	after, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
	if err != nil || after.DesignRecord == nil || after.Contract == nil || after.Contract.Version != 3 || after.DesignRecord.WorkVersion != before.DesignRecord.WorkVersion || after.DesignRecord.Approach != before.DesignRecord.Approach {
		t.Fatalf("replay changed the replacement pair: contract=%+v design=%+v err=%v", after.Contract, after.DesignRecord, err)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_design_records WHERE work_id=?`, workID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("replay lost design history: rows=%d err=%v", count, err)
	}
	entry, err := VerifyWorkflowInstanceDefinition(ctx, s, BuiltinWorkflowRegistry(), workID)
	if err != nil || entry.Digest != definition.Digest {
		t.Fatalf("correction changed the definition: %+v, %v", entry, err)
	}
	version := verdictItemVersion(t, s, workID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterFold(ctx, tx); err != nil {
		t.Fatal(err)
	}
	// Replay retains future log entries while projections cover an applied prefix.
	future := workflowEventWithActor("design-future-correction", WorkflowContractSuperseded, workID, ownerRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"previous_contract_version": 3, "new_contract_version": 4,
		"supersede_reason": "future correction", "audit_evidence": []string{"evidence:future"},
	})
	if _, err := appendEvent(ctx, tx, future, true); err != nil {
		t.Fatal(err)
	}
	current, invalidated, err := readCurrentWorkflowDesign(ctx, tx, workID)
	if err != nil || invalidated || current == nil || current.WorkVersion != after.DesignRecord.WorkVersion {
		t.Fatalf("unapplied future event changed design validity: design=%+v invalidated=%v err=%v", current, invalidated, err)
	}
}

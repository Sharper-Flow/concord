package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The impact edge carries a foreign key to work_items, so the dispatch must
// refuse a target the caller never named instead of defaulting a phantom
// "<work>-target" id that only moves the failure to fold time (issue #823).

func seedImpactTargetFixture(t *testing.T, s *Store, workID, targetID string) WorkflowActor {
	t.Helper()
	ctx := context.Background()
	seedWork(t, s, workID)
	seedWork(t, s, targetID)
	actor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	digest := cd0059ImplementationDigest(t)
	events := []Event{
		workflowEvent("impact-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": actorRef, "principal_ref": actor.PrincipalRef, "client_ref": actor.ClientRef, "agent_ref": actor.AgentRef, "session_ref": actor.SessionRef, "actor_class": "agent"}),
		workflowEvent("impact-definition-"+workID, WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "ref": "workflow.implementation", "version": 1, "digest": digest, "work_kind": "implementation"}),
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_instances SET current_step=? WHERE work_id=?`, "execution", workID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := leaveFold(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return actor
}

func invokeDeclareImpact(ctx context.Context, t *testing.T, s *Store, workID string, actor WorkflowActor, fields map[string]any) error {
	t.Helper()
	payload, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	_, err = invokeWorkflowActionForCD0059(ctx, t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: readWorkVersion(t, s, workID), ActionID: "declare_impact",
		Payload: payload, Actor: actor, AcceptedInputsDigest: cd0059TestDigest(t, "impact-inputs"),
		IdempotencyIdentity: "impact-test-op", OperationID: "op-impact-test", PrincipalRef: actor.PrincipalRef,
		Tool: "concord_work_transition", IdempotencyKey: "impact-test-key", RequestID: "req-impact-test",
		AcceptedScope: `{}`, ContractDigest: testManifestDigest,
	})
	return err
}

func TestDeclareImpactWithoutTargetIsRefusedAtTheBoundary(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	actor := seedImpactTargetFixture(t, s, "work-impact-source", "work-impact-target")
	err := invokeDeclareImpact(ctx, t, s, "work-impact-source", actor, map[string]any{"severity": "non-breaking"})
	if err == nil {
		t.Fatal("declare_impact without target_work_id was accepted")
	}
	failure, ok := err.(*Failure)
	if !ok || failure.Kind != KindInvalidPayload || !strings.Contains(failure.Detail, "requires target_work_id") {
		t.Fatalf("refusal = %+v, want a typed invalid_payload refusal naming the missing target", err)
	}
}

func TestDeclareImpactWithUnknownTargetIsRefused(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	actor := seedImpactTargetFixture(t, s, "work-impact-source-2", "work-impact-target-2")
	err := invokeDeclareImpact(ctx, t, s, "work-impact-source-2", actor, map[string]any{"target_work_id": "work-that-does-not-exist"})
	if err == nil {
		t.Fatal("declare_impact with an unknown target was accepted")
	}
	failure, ok := err.(*Failure)
	if !ok || failure.Kind != KindInvalidPayload || !strings.Contains(failure.Detail, "does not name a work item") {
		t.Fatalf("refusal = %+v, want a typed invalid_payload refusal naming the unknown target", err)
	}
}

func TestDeclareImpactWithRealTargetRecordsTheEdge(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	actor := seedImpactTargetFixture(t, s, "work-impact-source-3", "work-impact-target-3")
	if err := invokeDeclareImpact(ctx, t, s, "work-impact-source-3", actor, map[string]any{"target_work_id": "work-impact-target-3", "severity": "non-breaking"}); err != nil {
		t.Fatalf("declare_impact with a real target was refused: %v", err)
	}
	var target string
	if err := s.DatabaseForTesting().QueryRow(`SELECT target_work_id FROM workflow_impact_edges WHERE work_id=?`, "work-impact-source-3").Scan(&target); err != nil {
		t.Fatalf("impact edge was not recorded: %v", err)
	}
	if target != "work-impact-target-3" {
		t.Fatalf("impact edge target = %q, want the named target", target)
	}
}

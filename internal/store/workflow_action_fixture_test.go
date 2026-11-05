package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func seedTestWorkflow(t *testing.T, s *Store, workID string) (WorkflowActor, int64) {
	t.Helper()
	seedWork(t, s, workID)
	actor := WorkflowActor{PrincipalRef: "principal:fixture", ClientRef: "client:fixture", AgentRef: "agent:fixture", SessionRef: "session:fixture", ActorClass: ActorAgent}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(context.Background(), tx, WorkflowInitializationRequest{WorkID: workID, Actor: actor, Now: time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	seedWorkflowPlanFrontier(t, s, workID, WorkflowStepPlanChange, "plan")
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return actor, version
}

func applyTestAction(t *testing.T, s *Store, workID string, version int64, actionID, operationID string, fields map[string]any, actor WorkflowActor) (int64, error) {
	t.Helper()
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(fields)
	result, actionErr := applyWorkflowActionRawTx(context.Background(), tx, newWriteScope(tx), WorkflowActionExecutionRequest{WorkID: workID, ExpectedVersion: version, ActionID: actionID, Payload: payload, Actor: actor, AcceptedInputsDigest: "sha256:fixture", IdempotencyIdentity: operationID, OperationID: operationID, PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID, RequestID: "request:" + operationID, ContractDigest: testManifestDigest, Now: time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)})
	if actionErr != nil {
		_ = tx.Rollback()
		return 0, actionErr
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return result.ResultingVersion, nil
}

func startFixtureExecution(t *testing.T, f workContextFixture) {
	t.Helper()
	if err := runVerdictActionAs(t, f.store, f.workID, "start_execution", mustJSONValue(map[string]any{}), 0, f.owner); err != nil {
		t.Fatalf("start_execution refused: %v", err)
	}
}

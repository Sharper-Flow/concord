package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestWorkPinRemainsReadableWithoutAuthorizingAmbiguousContracts(t *testing.T) {
	ctx := context.Background()
	route := seedWorkflowReturnRouteFixture(t, "ambiguous-pin", WorkflowStepPlanChange, "review")
	f := acceptanceRecoveryFixture{store: route.store, workID: "ambiguous-pin", owner: route.owner, operator: route.operator}
	contract := pContractSuccessor(t, f.store, f.workID)
	before, err := ReadWorkPin(ctx, f.store, f.workID)
	if err != nil {
		t.Fatal(err)
	}
	duplicateActiveWorkflowContract(ctx, t, f.store, f.workID)
	pin, err := ReadWorkPin(ctx, f.store, f.workID)
	if err != nil {
		t.Fatalf("read work pin with duplicate active contracts: %v", err)
	}
	if pin.Step != before.Step {
		t.Fatalf("step = %q, want %q", pin.Step, before.Step)
	}
	effects := contractRecordEffectSnapshot(t, f.store, f.workID)
	version := readWorkVersion(t, f.store, f.workID)
	snapshot := outsideRepairDatabaseSnapshot(t, f.store)
	_, err = WorkflowActionDefinitionFor(ctx, f.store, WorkflowActionPreflightRequest{WorkID: f.workID, ExpectedVersion: version, ActionID: "confirm_premise", Payload: json.RawMessage(`{}`), Actor: f.owner})
	if !hasFailureKind(err, KindInvariantViolation) || !strings.Contains(err.Error(), "multiple active contracts") {
		t.Fatalf("ambiguous premise preflight = %v", err)
	}
	err = runVerdictActionAs(t, f.store, f.workID, "confirm_premise", json.RawMessage(`{}`), version, f.owner)
	if !hasFailureKind(err, KindInvariantViolation) || !strings.Contains(err.Error(), "multiple active contracts") {
		t.Fatalf("ambiguous premise mutation = %v", err)
	}
	if outsideRepairDatabaseSnapshot(t, f.store) != snapshot || readWorkVersion(t, f.store, f.workID) != version {
		t.Fatal("refused ambiguous premise changed durable state")
	}
	err = reviseContractRecord(t, f.store, f.workID, f.owner, contract, readWorkVersion(t, f.store, f.workID))
	if !hasFailureKind(err, KindInvariantViolation) || !strings.Contains(err.Error(), "multiple active contracts") {
		t.Fatalf("ambiguous contract revision = %v", err)
	}
	if after := contractRecordEffectSnapshot(t, f.store, f.workID); after != effects {
		t.Fatal("ambiguous revision changed the record")
	}
}

// Tolerance belongs to the read, never to authority over a chosen contract.
func TestActiveWorkflowContractVersionRefusesAnAmbiguousProjection(t *testing.T) {
	ctx := context.Background()
	route := seedWorkflowReturnRouteFixture(t, "ambiguous-version", WorkflowStepPlanChange, "review")
	f := acceptanceRecoveryFixture{store: route.store, workID: "ambiguous-version", owner: route.owner, operator: route.operator}
	duplicateActiveWorkflowContract(ctx, t, f.store, f.workID)
	if _, err := activeWorkflowContractVersion(ctx, f.store.DatabaseForTesting(), f.workID, "test"); err == nil {
		t.Fatal("activeWorkflowContractVersion selected a contract from an ambiguous projection")
	}
}

func duplicateActiveWorkflowContract(ctx context.Context, t *testing.T, s *Store, workID string) {
	t.Helper()
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `
		INSERT INTO write_guard(active) VALUES(1);
		INSERT INTO workflow_contracts(work_id,contract_version,consequence_class,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,self_repair_json,legislative_subjects)
		SELECT work_id,contract_version+1,consequence_class,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,self_repair_json,legislative_subjects
		FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL
		ORDER BY contract_version DESC LIMIT 1;
		DELETE FROM write_guard`, workID); err != nil {
		t.Fatalf("duplicate active contract: %v", err)
	}
}

func TestContractlessInvestigatedCheckpointAdmitsNoContractRoute(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	const workID = "contractless-checkpoint"
	const otherID = "contractless-checkpoint-other"
	seedWork(t, s, workID)
	seedWork(t, s, otherID)
	seedWorkflowLaw(t, s)
	seedContractlessVerifyWorkflow(t, s, workID)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); DELETE FROM workflow_contracts WHERE work_id=?; DELETE FROM write_guard`, workID); err != nil {
		t.Fatal(err)
	}
	pin, err := ReadWorkPin(ctx, s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if pin.WorkID != workID {
		t.Fatalf("contractless pin work ID = %q, want %q", pin.WorkID, workID)
	}
	effects := contractRecordEffectSnapshot(t, s, workID)
	actor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/recovery", SessionRef: "session/contractless", ActorClass: ActorAgent}
	version := readWorkVersion(t, s, workID)
	before := outsideRepairDatabaseSnapshot(t, s)
	_, err = WorkflowActionDefinitionFor(ctx, s, WorkflowActionPreflightRequest{WorkID: workID, ExpectedVersion: version, ActionID: "confirm_premise", Payload: json.RawMessage(`{}`), Actor: actor})
	if err == nil {
		t.Error("contractless premise preflight succeeded")
	}
	if err := runVerdictActionAs(t, s, workID, "confirm_premise", json.RawMessage(`{}`), version, actor); err == nil {
		t.Error("contractless premise mutation succeeded")
	} else {
		t.Logf("contractless premise mutation refused: %v", err)
	}
	if outsideRepairDatabaseSnapshot(t, s) != before || readWorkVersion(t, s, workID) != version {
		t.Fatal("refused contractless premise changed durable state")
	}
	err = reviseContractRecord(t, s, workID, actor, map[string]any{}, readWorkVersion(t, s, workID))
	if !hasFailureKind(err, KindInvalidOperation) || !strings.Contains(err.Error(), "requires an existing contract") {
		t.Fatalf("contractless revision = %v", err)
	}
	if after := contractRecordEffectSnapshot(t, s, workID); after != effects {
		t.Fatal("contractless revision changed the record")
	}
}

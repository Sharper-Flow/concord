package store

import (
	"context"
	"strings"
	"testing"
)

func TestWorkPinRemainsReadableWithoutAuthorizingAmbiguousContracts(t *testing.T) {
	ctx := context.Background()
	f := newAcceptanceRecoveryFixture(ctx, t)
	contract := pContractSuccessor(t, f.store, f.workID, "Revise the recorded plan")
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
	if len(pin.VerifiedCriteria) != 0 {
		t.Fatalf("ambiguous pin carries verified criteria = %v", pin.VerifiedCriteria)
	}
	for _, action := range []string{"confirm_premise", "complete"} {
		if workPinContainsAction(pin.NextValidIntents, action) {
			t.Fatalf("ambiguous pin authorizes %s", action)
		}
	}
	effects := contractRecordEffectSnapshot(t, f.store, f.workID)
	err = reviseContractRecord(t, f.store, f.workID, f.owner, contract, readWorkVersion(t, f.store, f.workID), nil)
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
	f := newAcceptanceRecoveryFixture(ctx, t)
	duplicateActiveWorkflowContract(ctx, t, f.store, f.workID)
	if _, err := activeWorkflowContractVersion(ctx, f.store.DatabaseForTesting(), f.workID, "test"); err == nil {
		t.Fatal("activeWorkflowContractVersion selected a contract from an ambiguous projection")
	}
}

func duplicateActiveWorkflowContract(ctx context.Context, t *testing.T, s *Store, workID string) {
	t.Helper()
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `
		INSERT INTO write_guard(active) VALUES(1);
		INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,rigor_class,law_modifies,law_boundary_version,self_repair_json)
		SELECT work_id,contract_version+1,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,rigor_class,law_modifies,law_boundary_version,self_repair_json
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
	insertInvestigationGateObservation(t, s, workID, "obs:"+strings.Repeat("7", 16), []string{"root", otherID})
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); DELETE FROM workflow_contracts WHERE work_id=?; DELETE FROM write_guard`, workID); err != nil {
		t.Fatal(err)
	}
	pin, err := ReadWorkPin(ctx, s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if workPinContainsAction(pin.NextValidIntents, "confirm_premise") {
		t.Error("pin advertises confirm_premise with no active contract")
	}
	effects := contractRecordEffectSnapshot(t, s, workID)
	actor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/recovery", SessionRef: "session/contractless", ActorClass: ActorAgent}
	err = reviseContractRecord(t, s, workID, actor, map[string]any{}, readWorkVersion(t, s, workID), nil)
	if !hasFailureKind(err, KindInvalidOperation) || !strings.Contains(err.Error(), "requires an existing contract") {
		t.Fatalf("contractless revision = %v", err)
	}
	if after := contractRecordEffectSnapshot(t, s, workID); after != effects {
		t.Fatal("contractless revision changed the record")
	}
}

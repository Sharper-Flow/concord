package store

import (
	"context"
	"strings"
	"testing"
)

// duplicateActiveContractForTest copies the active contract and its
// architecture binding to a second unsuperseded version, reproducing the
// projection a second approval left behind.
func duplicateActiveContractForTest(ctx context.Context, t *testing.T, s *Store, workID string) {
	t.Helper()
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `
		INSERT INTO write_guard(active) VALUES(1);
		INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class)
		SELECT work_id,contract_version+1,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class
		FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL ORDER BY contract_version DESC LIMIT 1;
		INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,home_domain_id,projection_hash)
		SELECT work_id,contract_version+1,product_id,home_domain_id,projection_hash
		FROM workflow_architecture_bindings WHERE work_id=? ORDER BY contract_version DESC LIMIT 1;
		DELETE FROM write_guard`, workID, workID); err != nil {
		t.Fatalf("duplicate active contract: %v", err)
	}
}

// Relaxing the law-revision gate keeps the operator recovery reachable. It must
// not make an ambiguous projection ordinarily actionable: a normal action still
// has no single contract to read, so it refuses.
func TestNormalWorkflowActionRefusesAnAmbiguousProjection(t *testing.T) {
	ctx := context.Background()
	f := newAcceptanceRecoveryFixture(ctx, t)
	duplicateActiveWorkflowContract(ctx, t, f.store, f.workID)
	version := readWorkVersion(t, f.store, f.workID)
	err := WorkflowActionPreflightWithRegistry(ctx, f.store, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
		WorkID: f.workID, ExpectedVersion: version, ActionID: "confirm_premise", Payload: []byte(`{}`),
		Actor: WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/recovery", SessionRef: "session/recovery", ActorClass: ActorAgent},
	})
	if err == nil || !strings.Contains(err.Error(), "multiple active contracts") {
		t.Fatalf("normal action error = %v, want duplicate-contract refusal", err)
	}
}

package store

import (
	"context"
	"testing"
)

// A duplicate active contract is a projection the operator repairs through a
// recovery supersession, and every route to that recovery reads the pin first.
// The pin must therefore stay readable while the ambiguity exists, and it must
// still name the recovery. A pin that refuses makes the ambiguity permanent:
// the recovery cannot be reached, so the rows that caused it are never retired.
func TestWorkPinNamesTheRecoveryWithDuplicateActiveContracts(t *testing.T) {
	ctx := context.Background()
	f := newAcceptanceRecoveryFixture(ctx, t)

	before, err := ReadWorkPin(ctx, f.store, f.workID)
	if err != nil {
		t.Fatalf("read work pin before duplication: %v", err)
	}
	duplicateActiveWorkflowContract(ctx, t, f.store, f.workID)

	pin, err := ReadWorkPin(ctx, f.store, f.workID)
	if err != nil {
		t.Fatalf("read work pin with duplicate active contracts: %v", err)
	}
	if pin.Step != before.Step {
		t.Fatalf("step = %q, want %q: the ambiguity must not move the work item", pin.Step, before.Step)
	}
	if !workPinContainsAction(pin.NextValidIntents, "supersede_contract") {
		t.Fatalf("pin omits supersede_contract while the projection is ambiguous; intents = %v", intentActionIDs(pin.NextValidIntents))
	}
	if len(pin.VerifiedCriteria) != 0 {
		t.Fatalf("ambiguous pin carries verified criteria = %v", pin.VerifiedCriteria)
	}
}

// The strict selection still refuses for callers that must act on exactly one
// contract. Tolerance belongs to the read, never to the authority.
func TestActiveWorkflowContractVersionRefusesAnAmbiguousProjection(t *testing.T) {
	ctx := context.Background()
	f := newAcceptanceRecoveryFixture(ctx, t)
	duplicateActiveWorkflowContract(ctx, t, f.store, f.workID)

	if _, err := activeWorkflowContractVersion(ctx, f.store.DatabaseForTesting(), f.workID, "test"); err == nil {
		t.Fatal("activeWorkflowContractVersion selected a contract from an ambiguous projection")
	}
}

// duplicateActiveWorkflowContract copies the active contract to a second
// unsuperseded version, reproducing the projection a second approval left
// behind before the fold began refusing one.
func duplicateActiveWorkflowContract(ctx context.Context, t *testing.T, s *Store, workID string) {
	t.Helper()
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `
		INSERT INTO fold_guard(active) VALUES(1);
		INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,rigor_class,law_modifies,law_boundary_version,self_repair_json)
		SELECT work_id,contract_version+1,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,rigor_class,law_modifies,law_boundary_version,self_repair_json
		FROM workflow_contracts
		WHERE work_id=? AND superseded_by IS NULL
		ORDER BY contract_version DESC LIMIT 1;
		DELETE FROM fold_guard`, workID); err != nil {
		t.Fatalf("duplicate active contract: %v", err)
	}
}

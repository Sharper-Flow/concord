package store

import (
	"context"
	"errors"
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

// TestWorkflowSupersedeDuplicateRecoveryAdmissionAgreesAcrossPinPreflightAndFold
// holds the duplicate branch to the shared admission: the pin advertises the
// recovery, the preflight passes, and the dispatch fold accepts exactly when
// workflowAdmitSupersede admits, and once the instance is terminal all three
// refuse together. The branch once computed its own admission from local
// predicates and returned before the loader and the pure decision ran.
func TestWorkflowSupersedeDuplicateRecoveryAdmissionAgreesAcrossPinPreflightAndFold(t *testing.T) {
	ctx := context.Background()
	f := newAcceptanceRecoveryFixture(ctx, t)

	duplicateActiveWorkflowContract(ctx, t, f.store, f.workID)
	// The pinned definition is Product-changing, so the successor carries the
	// predecessor's architecture binding unchanged: the same registry hash and
	// home Domain the duplicate projection already records.
	var registryHash, homeDomain string
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT domain_registry_content_hash,home_domain_id FROM workflow_architecture_bindings WHERE work_id=? ORDER BY contract_version LIMIT 1`, f.workID).Scan(&registryHash, &homeDomain); err != nil {
		t.Fatal(err)
	}
	binding := map[string]any{
		"domain_registry_content_hash": registryHash,
		"home_domain_id":               homeDomain,
		"affected_domain_ids":          []string{homeDomain},
		"domain_modifies":              []string{},
		"domain_relation_modifies":     []any{},
		"law_additions":                []any{},
		"verification_obligations":     []any{},
	}
	payload := supersedeRecoveryPayload(t, f.workID, 3, binding, 1, 2)
	actor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/recovery", SessionRef: "session/duplicate-recovery", ActorClass: ActorAgent}

	pin, err := ReadWorkPin(ctx, f.store, f.workID)
	if err != nil {
		t.Fatalf("read work pin with duplicate active contracts: %v", err)
	}
	if !workPinContainsAction(pin.NextValidIntents, "supersede_contract") {
		t.Fatalf("pin omits supersede_contract while the projection is ambiguous; intents = %v", intentActionIDs(pin.NextValidIntents))
	}
	if err := WorkflowActionPreflightWithRegistry(ctx, f.store, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
		WorkID: f.workID, ExpectedVersion: verdictItemVersion(t, f.store, f.workID), ActionID: "supersede_contract", Payload: payload, Actor: actor,
	}); err != nil {
		t.Fatalf("preflight refused the duplicate recovery: %v", err)
	}
	if err := runSupersedeDuplicateRecoveryFold(t, f.store, f.workID, payload, actor); err != nil {
		t.Fatalf("dispatch fold refused the duplicate recovery: %v", err)
	}
	var active int
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, f.workID).Scan(&active); err != nil || active != 1 {
		t.Fatalf("active contracts after duplicate recovery=%d err=%v, want one", active, err)
	}
}

// The refusal side of the same agreement: a terminal instance keeps every
// action immutable, so the duplicate branch hides the recovery, discovery and
// the preflight refuse with the fold's refusal, and the fold retires nothing.
func TestWorkflowSupersedeDuplicateRecoveryStaysClosedOnATerminalInstance(t *testing.T) {
	ctx := context.Background()
	f := newAcceptanceRecoveryFixture(ctx, t)

	duplicateActiveWorkflowContract(ctx, t, f.store, f.workID)
	seedTerminalInstanceForTesting(t, f.store, f.workID, "cancelled")

	payload := supersedeRecoveryPayload(t, f.workID, 3, nil, 1, 2)
	actor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/recovery", SessionRef: "session/duplicate-terminal", ActorClass: ActorAgent}

	pin, err := ReadWorkPin(ctx, f.store, f.workID)
	if err != nil {
		t.Fatalf("read work pin on the terminal duplicate projection: %v", err)
	}
	if workPinContainsAction(pin.NextValidIntents, "supersede_contract") {
		t.Fatalf("pin advertises supersede_contract on a terminal instance; intents = %v", intentActionIDs(pin.NextValidIntents))
	}
	_, _, err = WorkflowActionDefinitionFor(ctx, f.store, BuiltinWorkflowRegistry(), f.workID, "supersede_contract")
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || failure.Detail != "terminal workflow instance is immutable" {
		t.Fatalf("discovery on the terminal instance = %v, want the shared immutability refusal", err)
	}
	if err := WorkflowActionPreflightWithRegistry(ctx, f.store, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
		WorkID: f.workID, ExpectedVersion: verdictItemVersion(t, f.store, f.workID), ActionID: "supersede_contract", Payload: payload, Actor: actor,
	}); err == nil {
		t.Fatal("preflight admitted supersede_contract on a terminal instance")
	} else if !errors.As(err, &failure) || failure.Detail != "terminal workflow instance is immutable" {
		t.Fatalf("preflight refusal = %v, want the shared immutability refusal", err)
	}
	if err := runSupersedeRecoveryFold(t, f.store, f.workID, payload, actor); err == nil {
		t.Fatal("dispatch fold accepted supersede_contract on a terminal instance")
	}
	var active int
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, f.workID).Scan(&active); err != nil || active != 2 {
		t.Fatalf("active contracts after the refused recovery=%d err=%v, want both duplicates intact", active, err)
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

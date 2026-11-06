package store

import (
	"database/sql"
	"strings"
	"testing"
	"time"
)

func TestFrontierEvidenceBindingWithoutContract(t *testing.T) {
	t.Parallel()
	const id, ref = "bind-without-contract", "evidence:bind-without-contract"
	ctx := t.Context()
	s := openTemp(t)
	seedWork(t, s, id)
	seedWorkflowLaw(t, s)
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + id, ActorClass: ActorAgent}
	proof, err := s.EstablishKnowledgeSourceSetProof(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(proof, func(tx *Transaction) error {
		return InitializeWorkflowTx(proof, tx, WorkflowInitializationRequest{WorkID: id, Actor: owner, Now: time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)})
	}); err != nil {
		t.Fatal(err)
	}
	// A historical contractless close can leave an item at this frontier.
	seedWorkflowPlanFrontier(t, s, id, WorkflowStepPlanChange, "implement")
	f := acceptanceRecoveryFixture{store: s, workID: id, owner: owner}
	if err := f.run(ctx, "start_execution", "start_execution", f.version(ctx, t), []byte(`{}`), nil); err != nil {
		t.Fatalf("start_execution: %v", err)
	}
	if _, err := activeWorkflowContractVersion(ctx, s.db, id, "test"); err != sql.ErrNoRows {
		t.Fatalf("expected no active contract, got %v", err)
	}
	pin, err := ReadWorkPin(ctx, s, id)
	if err != nil {
		t.Fatal(err)
	}
	if !workPinContainsAction(pin.NextValidIntents, "bind_evidence") {
		t.Fatal("frontier pin does not advertise bind_evidence")
	}
	seedWorkflowAuthority(t, s, "no-contract-producer", id, "principal/no-contract", "request/no-contract", []string{ref})
	payload := mustJSONValue(map[string]any{"evidence_kind": "artifact", "immutable_subject_ref": ref, "producer_id": "principal/no-contract", "producer_run_ref": "no-contract-producer", "producer_watermark": "request/no-contract"})
	if err := f.run(ctx, "bind_evidence", "bind", f.version(ctx, t), payload, []string{ref}); err != nil {
		t.Fatalf("bind_evidence without contract: %v", err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_evidence_bindings WHERE work_id=? AND json_extract(payload,'$.immutable_subject_ref')=?`, id, ref).Scan(&count); err != nil || count != 1 {
		t.Fatalf("bindings=%d err=%v, want one", count, err)
	}
}

func TestPlanCompleteEvidenceBindingRefusesAmbiguousContracts(t *testing.T) {
	t.Parallel()
	const id, ref = "bind-complete-ambiguous", "evidence:bind-complete-ambiguous"
	ctx := t.Context()
	route := seedWorkflowReturnRouteFixture(t, id, WorkflowStepPlanChange, "review")
	f := acceptanceRecoveryFixture{store: route.store, workID: id, owner: route.owner}
	completeWorkflowPlanFixture(t, route.store, id)
	duplicateActiveWorkflowContract(ctx, t, route.store, id)
	seedWorkflowAuthority(t, route.store, "complete-ambiguous-producer", id, "principal/complete-ambiguous", "request/complete-ambiguous", []string{ref})
	payload := mustJSONValue(map[string]any{"evidence_kind": "artifact", "immutable_subject_ref": ref, "producer_id": "principal/complete-ambiguous", "producer_run_ref": "complete-ambiguous-producer", "producer_watermark": "request/complete-ambiguous"})
	err := f.run(ctx, "bind_evidence", "complete-ambiguous-bind", f.version(ctx, t), payload, []string{ref})
	if !hasFailureKind(err, KindIllegalLifecycleTransition) || !strings.Contains(errDetail(err), "action is not declared on a frontier step") {
		t.Fatalf("ambiguous completed-plan binding refusal=%v, want frontier refusal", err)
	}
}

func TestFrontierEvidenceBindingRefusesAmbiguousContracts(t *testing.T) {
	t.Parallel()
	const id, ref = "bind-ambiguous-contracts", "evidence:bind-ambiguous"
	ctx := t.Context()
	route := seedWorkflowReturnRouteFixture(t, id, WorkflowStepPlanChange, "implement")
	f := acceptanceRecoveryFixture{store: route.store, workID: id, owner: route.owner}
	seedWorkflowAuthority(t, route.store, "ambiguous-bind-producer", id, "principal/ambiguous-bind", "request/ambiguous-bind", []string{ref})
	duplicateActiveWorkflowContract(ctx, t, route.store, id)
	payload := mustJSONValue(map[string]any{"evidence_kind": "artifact", "immutable_subject_ref": ref, "producer_id": "principal/ambiguous-bind", "producer_run_ref": "ambiguous-bind-producer", "producer_watermark": "request/ambiguous-bind"})
	err := f.run(ctx, "bind_evidence", "ambiguous-bind", f.version(ctx, t), payload, []string{ref})
	if !hasFailureKind(err, KindInvariantViolation) || !strings.Contains(err.Error(), "multiple active contracts") {
		t.Fatalf("ambiguous binding refusal=%v, want invariant violation", err)
	}
}

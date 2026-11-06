package store

import (
	"encoding/json"
	"testing"
	"time"
)

func contractlessStepFixture(t *testing.T, id string, planType WorkflowStepPlanType, frontier string) acceptanceRecoveryFixture {
	t.Helper()
	s := openTemp(t)
	seedWork(t, s, id)
	seedWorkflowLaw(t, s)
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + id, ActorClass: ActorAgent}
	proof, err := s.EstablishKnowledgeSourceSetProof(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(proof, func(tx *Transaction) error {
		return InitializeWorkflowTx(proof, tx, WorkflowInitializationRequest{WorkID: id, Actor: owner, Now: time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)})
	}); err != nil {
		t.Fatal(err)
	}
	seedWorkflowPlanFrontier(t, s, id, planType, frontier)
	return acceptanceRecoveryFixture{store: s, workID: id, owner: owner}
}

const contractStepApprovalPayload = `{"legislative_subjects":[],"spec_mandate":[],"law_modifies":[],"architecture_binding":{"home_domain_id":"root","affected_domain_ids":["root"],"domain_modifies":[],"domain_relation_modifies":[],"law_additions":[],"verification_obligations":[]}}`

func TestContractStepRefusesWithoutApproval(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		plan                  WorkflowStepPlanType
		step, action, payload string
	}{
		{"change", WorkflowStepPlanChange, "plan", "record_plan", testWorkflowPlanDocument},
		{"removal", WorkflowStepPlanRemoval, "list", "record_removal_scope", `{"removed":["file:one"],"retiring_clauses":[],"note":"scope"}`},
		{"research", WorkflowStepPlanResearch, "frame", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := contractlessStepFixture(t, "contract-step-"+tc.name, tc.plan, tc.step)
			if tc.action != "" {
				if err := f.run(t.Context(), tc.action, "record", f.version(t.Context(), t), json.RawMessage(tc.payload), nil); err != nil {
					t.Fatal(err)
				}
			}
			payload := mustJSONValue(map[string]any{"step_id": tc.step, "note": "complete contract step"})
			err := f.run(t.Context(), "complete_step", "close", f.version(t.Context(), t), payload, nil)
			if !hasFailureKind(err, KindIllegalLifecycleTransition) {
				t.Fatalf("refusal=%v, want illegal lifecycle transition", err)
			}
			failure, ok := err.(*Failure)
			if !ok || failure.Detail != "step declares approve_contract and no active contract exists" || failure.RecoveryAction != "approve the contract before completing this step" {
				t.Fatalf("unexpected refusal: %+v", err)
			}
		})
	}
}

func TestContractStepClosesAfterApproval(t *testing.T) {
	f := contractlessStepFixture(t, "contract-step-approved", WorkflowStepPlanChange, "plan")
	if err := f.run(t.Context(), "record_plan", "record", f.version(t.Context(), t), json.RawMessage(testWorkflowPlanDocument), nil); err != nil {
		t.Fatal(err)
	}
	if err := f.run(t.Context(), "approve_contract", "approve", f.version(t.Context(), t), json.RawMessage(contractStepApprovalPayload), nil); err != nil {
		t.Fatal(err)
	}
	if err := f.run(t.Context(), "complete_step", "close", f.version(t.Context(), t), json.RawMessage(`{"step_id":"plan","note":"approved"}`), nil); err != nil {
		t.Fatal(err)
	}
}

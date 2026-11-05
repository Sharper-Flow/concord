package store

import (
	"encoding/json"
	"testing"
)

// qCurrentAcceptance leaves the item at its declared confirmation step.
func qCurrentAcceptance(t *testing.T, workID string) acceptanceRecoveryFixture {
	t.Helper()
	return qCurrentAcceptanceWithDelivery(t, workID, "")
}

func qCurrentAcceptanceWithDelivery(t *testing.T, workID, artifact string) acceptanceRecoveryFixture {
	t.Helper()
	route := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanChange, "review", nil)
	repo := initKnowledgeRepo(t)
	writeKnowledgeFile(t, repo, "fixture.txt", "fixture\n")
	commitKnowledgeRepo(t, repo, "fixture")
	f := acceptanceRecoveryFixture{store: route.store, workID: workID, owner: route.owner, operator: route.operator, repo: repo}
	if artifact != "" {
		payload := mustJSONValue(map[string]any{"delivery_artifact": artifact, "delivery_state": "asserted"})
		if err := runVerdictActionAs(t, f.store, workID, "record_delivery", payload, 0, f.owner); err != nil {
			t.Fatalf("record delivery: %v", err)
		}
	}
	if err := runVerdictActionAs(t, f.store, workID, "confirm_premise", json.RawMessage(`{}`), 0, f.owner); err != nil {
		t.Fatalf("confirm premise: %v", err)
	}
	return f
}

func qContractRevisionFixture(t *testing.T, workID string) (*Store, string) {
	t.Helper()
	route := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanChange, "plan", nil)
	return route.store, mustActorRef(t, route.owner)
}

func TestQCurrentAcceptanceKeepsSeededContract(t *testing.T) {
	const workID = "q-seeded-acceptance"
	f := qCurrentAcceptance(t, workID)
	var contracts, confirmations int
	if err := f.store.db.QueryRow(`SELECT (SELECT count(*) FROM workflow_contracts WHERE work_id=?), (SELECT count(*) FROM workflow_premise_confirmations WHERE work_id=?)`, workID, workID).Scan(&contracts, &confirmations); err != nil {
		t.Fatal(err)
	}
	if contracts != 1 || confirmations != 1 {
		t.Fatalf("contracts=%d confirmations=%d, want one each", contracts, confirmations)
	}
}

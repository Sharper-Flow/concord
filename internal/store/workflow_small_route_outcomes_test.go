package store

import (
	"encoding/json"
	"testing"
)

func TestSmallImplementationCompletionRetainsDeliveryAndConfirmation(t *testing.T) {
	const id = "small-route-outcomes"
	f := qCurrentAcceptanceWithDelivery(t, id, "artifact:small-route")
	completeWorkflowPlanFixture(t, f.store, id)
	if err := f.run(t.Context(), "complete", "complete", f.version(t.Context(), t), json.RawMessage(`{"impact_verdict":"non-breaking"}`), nil); err != nil {
		t.Fatal(err)
	}
	var state string
	var confirmations, deliveries int
	if err := f.store.db.QueryRow(`SELECT instance_state, (SELECT count(*) FROM workflow_premise_confirmations WHERE work_id=?), (SELECT count(*) FROM workflow_delivery_state WHERE work_id=?) FROM workflow_instances WHERE work_id=?`, id, id, id).Scan(&state, &confirmations, &deliveries); err != nil {
		t.Fatal(err)
	}
	if state != "completed" || confirmations != 1 || deliveries != 1 {
		t.Fatalf("state=%q confirmations=%d deliveries=%d", state, confirmations, deliveries)
	}
}

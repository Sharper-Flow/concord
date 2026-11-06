package store

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestWorkflowPlanDeliverySharedAdmission(t *testing.T) {
	const workID = "plan-delivery"
	f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanChange, "review", []string{})
	pin, err := ReadWorkPin(t.Context(), f.store, workID)
	if err != nil || pin.Step != "review" || !workPinContainsAction(pin.NextValidIntents, "record_delivery") {
		t.Fatalf("plan pin=%+v err=%v", pin, err)
	}
	payload := json.RawMessage(`{"delivery_artifact":"artifact:plan-delivery","delivery_state":"asserted"}`)
	request := planAdmissionRequest(workID, "record_delivery", pin.Version, f.owner, payload)
	if err := planAdmissionPreflight(t, f.store, request); err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if err := planAdmissionDispatch(t, f.store, request); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	var actor string
	if err := f.store.db.QueryRow(`SELECT actor FROM effect_receipts WHERE subject_id=? AND kind=? ORDER BY effect_seq DESC LIMIT 1`, workID, WorkflowActionCompleted).Scan(&actor); err != nil || actor != mustActorRef(t, f.owner) {
		t.Fatalf("delivery actor=%q err=%v", actor, err)
	}
	if got := workflowPlanFixtureFrontier(t, f.store, workID); got != "review" {
		t.Fatalf("delivery changed frontier to %q", got)
	}
}

func TestWorkflowPlanDeliveryAmbiguousContractUpdateIsAtomic(t *testing.T) {
	const workID = "plan-delivery-duplicate-contract"
	f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanChange, "review", []string{})
	contract := pContractSuccessor(t, f.store, workID)
	duplicateActiveWorkflowContract(context.Background(), t, f.store, workID)
	before := lawUpgradeSnapshot(t, f.store)
	version := readWorkVersion(t, f.store, workID)
	err := reviseContractRecord(t, f.store, workID, f.owner, contract, version)
	_ = planAdmissionFailure(t, err, KindInvariantViolation)
	if after := lawUpgradeSnapshot(t, f.store); !reflect.DeepEqual(before, after) || readWorkVersion(t, f.store, workID) != version {
		t.Fatal("refused ambiguous contract update changed application tables")
	}
}

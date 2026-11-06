package store

import (
	"encoding/json"
	"testing"
)

func TestWorkflowPlanRevisedContractKeepsConfirmationRoute(t *testing.T) {
	const workID = "plan-confirmation-revision"
	f := rAcceptance(t, workID, []string{}, true)
	rReviseContract(t, f.store, workID, mustActorRef(t, f.owner))
	if err := approvalPlanAction(t, f, "confirm_premise", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("confirm revised contract: %v", err)
	}
	if actor, ok := approvalPremiseConfirmationRow(t, f.store, workID, 2); !ok || actor != mustActorRef(t, f.owner) {
		t.Fatalf("revised contract confirmation actor=%q present=%v", actor, ok)
	}
	completeWorkflowPlanFixture(t, f.store, workID)
	if err := approvalPlanAction(t, f, "complete", json.RawMessage(`{"impact_verdict":"non-breaking"}`)); err != nil {
		t.Fatalf("complete revised contract: %v", err)
	}
}

func TestWorkflowPlanConfirmationRefusesOperatorActor(t *testing.T) {
	const workID = "plan-confirmation-operator"
	f := rAcceptance(t, workID, nil, false)
	version, events := readWorkVersion(t, f.store, workID), approvalEventCount(t, f.store, workID)
	request := planAdmissionRequest(workID, "confirm_premise", version, f.operator, json.RawMessage(`{}`))
	dispatchErr := planAdmissionDispatch(t, f.store, request)
	_ = planAdmissionFailure(t, dispatchErr, KindInvalidOperation)
	assertApprovalRefusalUnchanged(t, f, version, events)
	if err := approvalPlanAction(t, f, "confirm_premise", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("authenticated coordinator confirmation: %v", err)
	}
}

func TestWorkflowPlanConfirmationLiveness(t *testing.T) {
	const workID = "plan-confirmation-liveness"
	f := qCurrentAcceptance(t, workID)
	pin, err := ReadWorkPin(t.Context(), f.store, workID)
	if err != nil {
		t.Fatalf("work pin=%+v err=%v", pin, err)
	}
	if pin.WorkflowType != string(WorkflowStepPlanChange) || pin.Step != "review" || !workPinContainsAction(pin.NextValidIntents, "confirm_premise") {
		t.Fatalf("work pin=%+v", pin)
	}
}

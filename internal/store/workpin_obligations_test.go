package store

import (
	"context"
	"reflect"
	"testing"
)

func TestWorkPinPublishesContractObligationsAndPlanIdentity(t *testing.T) {
	s := openTemp(t)
	const workID = "workpin-obligations"
	seedLawContextFixture(t, s, workID)
	binding := WorkflowArchitectureBinding{
		HomeDomainID: "root", AffectedDomainIDs: []string{"root"},
		DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{},
		VerificationObligations: []WorkflowVerificationObligation{{LawID: "spec:one", ObligationID: "verification"}},
	}
	approveLawContextContract(t, s, workID, []string{"spec:one"}, []string{}, binding)
	pin, err := ReadWorkPin(context.Background(), s, workID)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"verification"}
	if pin.Obligations == nil || !reflect.DeepEqual(*pin.Obligations, want) {
		t.Fatalf("pin obligations=%v, want active contract obligations %v", pin.Obligations, want)
	}
	snapshot, err := ReadWorkflowContinuity(context.Background(), s, ContinuityRequest{Work: workID})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.WorkPin == nil || !reflect.DeepEqual(snapshot.WorkPin.Obligations, pin.Obligations) || snapshot.WorkPin.WorkflowType != pin.WorkflowType {
		t.Fatalf("continuity pin=%+v, direct pin=%+v", snapshot.WorkPin, pin)
	}
}

package store

import (
	"context"
	"testing"
)

func TestWorkPinContinuityRetainsPlanIdentity(t *testing.T) {
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
	snapshot, err := ReadWorkflowContinuity(context.Background(), s, ContinuityRequest{Work: workID})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.WorkPin == nil || snapshot.WorkPin.WorkflowType != pin.WorkflowType || snapshot.WorkPin.Version != pin.Version {
		t.Fatalf("continuity pin=%+v, direct pin=%+v", snapshot.WorkPin, pin)
	}
}

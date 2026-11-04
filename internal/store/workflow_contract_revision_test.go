package store

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestPremiseRevisionPreservesPlanAndContractHistory(t *testing.T) {
	const id = "revision-preserves-plan"
	f := seedWorkflowReturnRouteFixture(t, id, WorkflowStepPlanResearch, "conclude")
	beforePlan, err := readWorkflowPlanTx(t.Context(), f.store.db, id)
	if err != nil {
		t.Fatal(err)
	}
	beforeContract, err := readWorkflowContractVersion(t.Context(), f.store.db, id, 1)
	if err != nil {
		t.Fatal(err)
	}
	successor := pContractSuccessor(t, f.store, id, "corrected premise")
	if err := reviseContractRecord(t, f.store, id, f.owner, successor, readWorkVersion(t, f.store, id)); err != nil {
		t.Fatalf("ordinary premise revision: %v", err)
	}
	afterPlan, err := readWorkflowPlanTx(t.Context(), f.store.db, id)
	if err != nil || !reflect.DeepEqual(beforePlan, afterPlan) {
		t.Fatalf("revision changed plan attempts: before=%+v after=%+v err=%v", beforePlan, afterPlan, err)
	}
	active, err := activeWorkflowContractVersion(t.Context(), f.store.db, id, "test")
	if err != nil || active != 2 {
		t.Fatalf("active contract=%d err=%v, want 2", active, err)
	}
	previous, err := readWorkflowContractVersion(t.Context(), f.store.db, id, 1)
	if err != nil {
		t.Fatal(err)
	}
	previous.SupersededBy = beforeContract.SupersededBy
	if !reflect.DeepEqual(beforeContract, previous) {
		t.Fatalf("revision changed historical contract: before=%+v after=%+v", beforeContract, previous)
	}
	current, err := readWorkflowContractVersion(t.Context(), f.store.db, id, 2)
	if err != nil || current.Premise != "corrected premise" {
		t.Fatalf("successor=%+v err=%v", current, err)
	}
}

func TestWorkflowActionPreflightRefusesUndeclaredActionAtCheckpoint(t *testing.T) {
	const id = "preflight-supersede-at-checkpoint"
	f := seedWorkflowReturnRouteFixture(t, id, WorkflowStepPlanResearch, "conclude")
	request := WorkflowActionPreflightRequest{WorkID: id, ExpectedVersion: readWorkVersion(t, f.store, id), StepID: "conclude", ActionID: "record_finding", Payload: json.RawMessage(`{}`), Actor: f.owner}
	before := contractCorrectionEffectSnapshot(t, f.store, id)
	if _, err := WorkflowActionDefinitionFor(t.Context(), f.store, request); !hasFailureKind(err, KindIllegalLifecycleTransition) {
		t.Fatalf("undeclared action preflight=%v", err)
	}
	if contractCorrectionEffectSnapshot(t, f.store, id) != before {
		t.Fatal("preflight changed effects")
	}
}

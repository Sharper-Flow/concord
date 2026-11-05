package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestPredispatchContractRevisionPreservesExecutionIdentity(t *testing.T) {
	ctx := context.Background()
	const workID = "predispatch-replay"
	f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanChange, "plan", nil)
	s, owner := f.store, f.owner
	before, err := readWorkflowPlanTx(ctx, s.db, workID)
	if err != nil || before == nil {
		t.Fatal(err)
	}
	unsupportedDesign := pContractSuccessor(t, s, workID, "corrected predicate subject")
	unsupportedDesign["design_record"] = map[string]any{
		"approach":     "A design for work without a typed design",
		"decisions":    []map[string]any{{"id": "decision:unsupported", "question": "Which approach?", "choice": "new", "rationale": "Synthetic input", "rejected": []string{}}},
		"touched_refs": []string{"src/work.go"},
	}
	version := readWorkVersion(t, s, workID)
	err = reviseContractRecord(t, s, workID, owner, unsupportedDesign, version)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidPayload {
		t.Fatalf("designless workflow acquired a design: %v", err)
	}
	if readWorkVersion(t, s, workID) != version {
		t.Fatal("refused design addition changed work")
	}
	var contractVersion int64
	err = s.db.QueryRowContext(ctx, `SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&contractVersion)
	if err != nil || contractVersion != 1 {
		t.Fatalf("refused design addition partially changed the contract: %d err=%v", contractVersion, err)
	}
	if err := reviseContractRecord(t, s, workID, owner, pContractSuccessor(t, s, workID, "corrected predicate subject"), version); err != nil {
		t.Fatalf("correct before dispatch: %v", err)
	}
	pin, err := ReadWorkPin(ctx, s, workID)
	if err != nil || pin.WorkID != workID {
		t.Fatalf("contract correction lost its current work identity: pin=%+v err=%v", pin, err)
	}
	var premise string
	err = s.db.QueryRowContext(ctx, `SELECT contract_version,premise FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&contractVersion, &premise)
	if err != nil || contractVersion != 2 || premise != "corrected predicate subject" {
		t.Fatalf("correction lost successor: %d %q err=%v", contractVersion, premise, err)
	}
	after, err := readWorkflowPlanTx(ctx, s.db, workID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("plan changed across correction: before=%+v after=%+v err=%v", before, after, err)
	}
}

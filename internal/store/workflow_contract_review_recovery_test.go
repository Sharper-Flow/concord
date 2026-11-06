package store

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestReviewPreflightAfterContractRevision(t *testing.T) {
	const id = "revision-review-preflight"
	f := seedWorkflowReturnRouteFixture(t, id, WorkflowStepPlanChange, "review")
	contract := pContractSuccessor(t, f.store, id)
	if err := reviseContractRecord(t, f.store, id, f.owner, contract, readWorkVersion(t, f.store, id)); err != nil {
		t.Fatal(err)
	}
	payload := mustJSONValue(map[string]any{"step_id": "review", "note": "review fixed the change", "reviewer": "concord-review", "result": "fixed", "commit": "commit:fixture", "link": "https://example.com/review"})
	before := contractCorrectionEffectSnapshot(t, f.store, id)
	request := WorkflowActionPreflightRequest{WorkID: id, ExpectedVersion: readWorkVersion(t, f.store, id), StepID: "review", ActionID: "record_review", Payload: payload, Actor: f.owner}
	action, err := WorkflowActionDefinitionFor(t.Context(), f.store, request)
	if err != nil || action.ID != "record_review" {
		t.Fatalf("Review preflight=%+v err=%v", action, err)
	}
	if contractCorrectionEffectSnapshot(t, f.store, id) != before {
		t.Fatal("preflight changed durable state")
	}
	if err := runVerdictActionAs(t, f.store, id, "record_review", payload, 0, f.owner); err != nil {
		t.Fatalf("Review after revision: %v", err)
	}
	if step := workflowPlanFixtureFrontier(t, f.store, id); step != "review" {
		t.Fatalf("fixed Review moved frontier to %q", step)
	}
}

func TestTerminalContractRevisionKeepsPlanAndConfirmationHistory(t *testing.T) {
	for _, family := range []WorkflowStepPlanType{WorkflowStepPlanChange, WorkflowStepPlanFix} {
		t.Run(string(family), func(t *testing.T) {
			id := "terminal-revision-" + string(family)
			f := seedWorkflowReturnRouteFixture(t, id, family, "review")
			if err := runVerdictActionAs(t, f.store, id, "confirm_premise", json.RawMessage(`{}`), 0, f.owner); err != nil {
				t.Fatalf("confirm premise at declared step: %v", err)
			}
			before, err := readWorkflowPlanTx(t.Context(), f.store.db, id)
			if err != nil {
				t.Fatal(err)
			}
			contract := pContractSuccessor(t, f.store, id)
			if err := reviseContractRecord(t, f.store, id, f.owner, contract, readWorkVersion(t, f.store, id)); err != nil {
				t.Fatal(err)
			}
			after, err := readWorkflowPlanTx(t.Context(), f.store.db, id)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("revision changed plan: before=%+v after=%+v err=%v", before, after, err)
			}
			var count, version int
			if err := f.store.db.QueryRow(`SELECT count(*),min(contract_version) FROM workflow_premise_confirmations WHERE work_id=?`, id).Scan(&count, &version); err != nil || count != 1 || version != 1 {
				t.Fatalf("confirmation count=%d version=%d err=%v", count, version, err)
			}
			active, err := activeWorkflowContractVersion(t.Context(), f.store.db, id, "test")
			if err != nil || active != 2 {
				t.Fatalf("active contract=%d err=%v", active, err)
			}
		})
	}
}

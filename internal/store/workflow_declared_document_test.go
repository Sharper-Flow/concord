package store

import (
	"encoding/json"
	"testing"
	"time"
)

func TestWorkflowPlanDocumentRejectsUndeclaredFieldsWithoutEffects(t *testing.T) {
	const workID = "plan-document-closed-fields"
	f := seedWorkflowReturnRouteFixture(t, workID, WorkflowStepPlanChange, "plan")
	defer f.store.Close()
	version := readWorkVersion(t, f.store, workID)
	before := approvalEventCount(t, f.store, workID)
	payload := json.RawMessage(`{"outcomes":"working outcome","approach":"bounded change","decisions":[{"id":"decision:one","question":"which?","choice":"one","rationale":"fits","rejected":[]}],"touches":["file:one"],"merge_split":"one item","risks":"bounded risk","unknown":"field"}`)
	err := f.store.Transact(t.Context(), func(tx *Transaction) error {
		_, err := ApplyWorkflowActionTx(t.Context(), tx, WorkflowActionExecutionRequest{
			WorkID: workID, ExpectedVersion: version, ActionID: "record_plan", Payload: payload, Actor: f.owner,
			OperationID: "document-invalid", IdempotencyIdentity: "document-invalid", PrincipalRef: f.owner.PrincipalRef,
			RequestID: "document-invalid", AcceptedInputsDigest: testManifestDigest, ContractDigest: testManifestDigest, Now: time.Unix(100, 0).UTC(),
		})
		return err
	})
	assertFailureKind(t, err, KindInvalidPayload)
	var rows int
	if err := f.store.db.QueryRow(`SELECT count(*) FROM workflow_plan_records WHERE work_id=?`, workID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 || readWorkVersion(t, f.store, workID) != version || approvalEventCount(t, f.store, workID) != before {
		t.Fatal("invalid plan document changed rows, version, or receipts")
	}
}

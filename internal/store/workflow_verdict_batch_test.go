package store

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func pConfirmReceiptPremise(t *testing.T, f workContextFixture) error {
	t.Helper()
	ctx, err := prepareTerminalTestContext(t.Context(), f.store, f.workID, "confirm_premise")
	if err != nil {
		return err
	}
	version := readWorkVersion(t, f.store, f.workID)
	id := fmt.Sprintf("confirm-%s-%d", f.workID, version)
	return f.store.Transact(ctx, func(tx *Transaction) error {
		_, err := ApplyWorkflowActionTx(ctx, tx, WorkflowActionExecutionRequest{
			WorkID: f.workID, ExpectedVersion: version, ActionID: "confirm_premise", Payload: json.RawMessage(`{}`), Actor: f.owner,
			AcceptedInputsDigest: testDigest(id), IdempotencyIdentity: id, OperationID: id,
			PrincipalRef: f.owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: id, RequestID: "request:" + id, ContractDigest: testManifestDigest, Now: time.Now().UTC(),
		})
		return err
	})
}

package store

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestOperationResultTxReadsAtomicDispatchWithoutMutation(t *testing.T) {
	s := openTemp(t)
	actor, version := seedTestWorkflow(t, s, "atomic-result-work")
	var plan map[string]any
	if err := json.Unmarshal([]byte(testWorkflowPlanDocument), &plan); err != nil {
		t.Fatal(err)
	}
	if _, err := applyTestAction(t, s, "atomic-result-work", version, "record_plan", "atomic-result", plan, actor); err != nil {
		t.Fatal(err)
	}
	before := retirementUpgradeSnapshot(t, s.db)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := OperationResultTx(t.Context(), &Transaction{tx: tx}, "atomic-result")
	if rollbackErr := tx.Rollback(); rollbackErr != nil {
		t.Fatal(rollbackErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if result.OpID != "atomic-result" || result.WorkID != "atomic-result-work" || result.ResultKind != "completed" || result.ResultPayload == "" {
		t.Fatalf("atomic result=%+v", result)
	}
	if after := retirementUpgradeSnapshot(t, s.db); !reflect.DeepEqual(before, after) {
		t.Fatal("reading the atomic result changed persisted state")
	}
	if _, err := OperationResultTx(context.Background(), nil, "atomic-result"); err == nil {
		t.Fatal("nil transaction admitted a result read")
	}
	if updated, err := s.db.Exec(`UPDATE durable_operations SET result_kind='completed' WHERE op_id=?`, result.OpID); err != nil {
		t.Fatalf("valid atomic result update: %v", err)
	} else if count, err := updated.RowsAffected(); err != nil || count != 1 {
		t.Fatalf("valid atomic result update affected %d rows: %v", count, err)
	}
	for _, kind := range []any{nil, "pending", "partial", "failed_stale", "failed"} {
		if _, err := s.db.Exec(`UPDATE durable_operations SET result_kind=? WHERE op_id=?`, kind, result.OpID); err == nil {
			t.Errorf("retired result kind %v remains admissible", kind)
		}
	}
	for _, kind := range []string{"external_effect", "cross_authority"} {
		if _, err := s.db.Exec(`UPDATE durable_operations SET step_kind=? WHERE op_id=?`, kind, result.OpID); err == nil {
			t.Errorf("retired claim kind %s remains admissible", kind)
		}
	}
	if _, err := s.db.Exec(`UPDATE durable_operations SET attempt_epoch=2 WHERE op_id=?`, result.OpID); err == nil {
		t.Fatal("claim epochs remain admissible")
	}
}

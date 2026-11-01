package store

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestOperationResultTxReadsAtomicDispatchWithoutMutation(t *testing.T) {
	s := openTemp(t)
	actor, version := continuityTestWorkflow(t, s, "atomic-result-work")
	if _, err := continuityAction(t, s, "atomic-result-work", version, "checkpoint_context", "atomic-result", map[string]any{
		"active_unit": "unit:atomic", "hypothesis": "The atomic result remains readable", "diagnosis": "No separate claim is needed", "strategy": "Read the committed result",
		"touched_refs": []string{"ref:atomic"}, "evidence_refs": []string{"evidence:atomic"}, "pending_questions": []string{}, "pending_decisions": []string{},
	}, actor); err != nil {
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
}

func TestAtomicOperationMigrationRetiresClaimsAndIntermediateResults(t *testing.T) {
	useStampedBuild(t)
	db, path := terminalStepsMigrationFixture(t)
	advanceHistoricalPrefix(t, db, 177)
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1)`)
	for _, kind := range []string{"pending", "partial", "failed_stale", "failed", "completed"} {
		id := "retired-" + kind
		retirementUpgradeExec(t, db, `INSERT INTO durable_operations(op_id,attempt_epoch,work_id,workflow_type_ref,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,principal_ref,request_id,observed_at,contract_digest,result_kind) VALUES(?,1,'work-upgrade-from','workflow.break_fix','repair','external_effect',?,'{}','operator',?,'2026-08-07T00:00:00Z',?,?)`, id, testDigest("inputs"), id, testManifestDigest, kind)
		retirementUpgradeExec(t, db, `INSERT INTO idempotency_records(principal_ref,tool,operation_kind,idempotency_key,canonical_digest,op_id,first_observed_at,last_observed_at) VALUES('operator','execute','claim',?,?,?,'t','t')`, id, testDigest(id), id)
	}
	for epoch, kind := range map[int]string{2: "partial", 3: "completed"} {
		retirementUpgradeExec(t, db, `INSERT INTO durable_operations(op_id,attempt_epoch,work_id,workflow_type_ref,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,principal_ref,request_id,observed_at,contract_digest,result_kind,result_payload) VALUES('ordinary-history',?,'work-upgrade-from','workflow.break_fix','repair','external_effect',?,'{}','operator','ordinary-request','2026-08-07T00:00:00Z',?,?,'{"retained":true}')`, epoch, testDigest("ordinary"), testManifestDigest, kind)
	}
	retirementUpgradeExec(t, db, `INSERT INTO idempotency_records(principal_ref,tool,operation_kind,idempotency_key,canonical_digest,op_id,first_observed_at,last_observed_at) VALUES('operator','transition','workflow_action','ordinary-history',?,'ordinary-history','t','t'),('operator','transition','mutation','retired-intermediate',?,'retired-pending','t','t')`, testDigest("ordinary"), testDigest("pending"))
	retirementUpgradeExec(t, db, `INSERT INTO workflow_evidence_bindings VALUES('work-upgrade-from',26,'binding:ordinary-history','operator','{"producer_run_ref":"ordinary-history"}'),('work-upgrade-from',27,'binding:retired-claim','operator','{"producer_run_ref":"retired-pending"}')`)
	retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
	before := retirementUpgradeSnapshot(t, db)
	if _, err := Upgrade(t.Context(), path, nil); err != nil {
		t.Fatal(err)
	}
	if got := retirementUpgradeRows(t, db, `SELECT op_id,result_kind,attempt_epoch,step_kind FROM durable_operations ORDER BY op_id`); !reflect.DeepEqual(got, [][]any{{"ordinary-history", "completed", int64(1), "internal_sqlite"}, {"successful-operation", "completed", int64(1), "internal_sqlite"}}) {
		t.Fatalf("atomic operations=%v", got)
	}
	if got := retirementUpgradeRows(t, db, `SELECT op_id FROM idempotency_records ORDER BY op_id`); !reflect.DeepEqual(got, [][]any{{"ordinary-history"}, {"successful-operation"}}) {
		t.Fatalf("ordinary replay records=%v", got)
	}
	if got := retirementUpgradeRows(t, db, `SELECT binding_id FROM workflow_evidence_bindings ORDER BY binding_id`); !reflect.DeepEqual(got, [][]any{{"binding:ordinary"}, {"binding:ordinary-history"}}) {
		t.Fatalf("ordinary bindings=%v", got)
	}
	if got := retirementUpgradeRows(t, db, `SELECT result_payload FROM durable_operations WHERE op_id='ordinary-history'`); !reflect.DeepEqual(got, [][]any{{`{"retained":true}`}}) {
		t.Fatalf("historical ordinary result changed=%v", got)
	}
	for _, table := range []string{"archived_work", "active_research_packs", "effect_receipts", "workflow_law_baselines", "workflow_delivery_state"} {
		if after := retirementUpgradeRows(t, db, "SELECT * FROM "+table+" ORDER BY rowid"); !reflect.DeepEqual(after, before[table]) {
			t.Errorf("atomic retirement changed %s", table)
		}
	}
	for _, kind := range []any{nil, "pending", "partial", "failed_stale", "failed"} {
		if _, err := db.Exec(`UPDATE durable_operations SET result_kind=?`, kind); err == nil {
			t.Errorf("retired result kind %v remains admissible", kind)
		}
	}
	for _, kind := range []string{"external_effect", "cross_authority"} {
		if _, err := db.Exec(`UPDATE durable_operations SET step_kind=?`, kind); err == nil {
			t.Errorf("retired claim kind %s remains admissible", kind)
		}
	}
	if _, err := db.Exec(`UPDATE durable_operations SET attempt_epoch=2`); err == nil {
		t.Fatal("claim epochs remain admissible")
	}
	if rows := retirementUpgradeRows(t, db, `SELECT name FROM pragma_table_info('durable_operations') WHERE name='resume_cursor'`); len(rows) != 0 {
		t.Fatalf("claim cursor remains=%v", rows)
	}
	retirementUpgradeHealthy(t, db)
}

func TestAtomicOperationMigrationRollsBackDiscardedClaims(t *testing.T) {
	useStampedBuild(t)
	db, path := terminalStepsMigrationFixture(t)
	advanceHistoricalPrefix(t, db, 177)
	retirementUpgradeExec(t, db, `INSERT INTO idempotency_records(principal_ref,tool,operation_kind,idempotency_key,canonical_digest,op_id,first_observed_at,last_observed_at) VALUES('operator','execute','claim','historical-claim',?,'successful-operation','t','t')`, testDigest("claim"))
	retirementUpgradeExec(t, db, `CREATE TRIGGER reject_claim_retirement BEFORE DELETE ON idempotency_records WHEN OLD.operation_kind='claim' BEGIN SELECT RAISE(ABORT,'atomic retirement rollback witness'); END`)
	before := retirementUpgradeSnapshot(t, db)
	_, err := Upgrade(t.Context(), path, nil)
	assertFailureKind(t, err, KindUnavailable)
	if !strings.Contains(err.Error(), "atomic retirement rollback witness") {
		t.Fatalf("upgrade missed the destructive rollback witness: %v", err)
	}
	if after := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(before, after) {
		t.Fatal("failed atomic retirement changed application state or schema")
	}
	retirementUpgradeExec(t, db, `DROP TRIGGER reject_claim_retirement`)
	if _, err := Upgrade(t.Context(), path, nil); err != nil {
		t.Fatal(err)
	}
	retirementUpgradeHealthy(t, db)
}

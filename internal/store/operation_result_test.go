package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
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
	ordinaryRecordsUpgradeExpectations(t, db, before)
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
	for _, table := range []string{"archived_work", "effect_receipts", "workflow_delivery_state"} {
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

func TestAtomicOperationMigrationSurvivorSet(t *testing.T) {
	useStampedBuild(t)
	db, path := terminalStepsMigrationFixture(t)
	advanceHistoricalPrefix(t, db, 177)
	const columns = `op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_digest,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,result_kind,result_payload,evidence_refs,changed_refs,principal_ref,request_id,observed_at,completed_at,contract_digest`
	const normalizedColumns = `op_id,1,work_id,workflow_type_ref,workflow_type_digest,step_id,'internal_sqlite',accepted_inputs_digest,accepted_scope_snapshot,result_kind,result_payload,evidence_refs,changed_refs,principal_ref,request_id,observed_at,completed_at,contract_digest`
	want := retirementUpgradeRows(t, db, `SELECT `+normalizedColumns+` FROM durable_operations ORDER BY op_id`)
	var keep []string
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1)`)
	for _, tc := range []struct {
		id        string
		results   []any
		kinds     []string
		keepEpoch int
	}{
		{"ordinary", []any{"completed"}, []string{"mutation"}, 1},
		{"repeated", []any{"completed", "partial", "completed", "pending", nil}, []string{"workflow_action", "mutation"}, 3},
		{"no-idempotency", []any{"completed"}, nil, 1},
		{"nonclaim-kinds", []any{"completed"}, []string{"claim_extra", "complete_extra", "workflow_action"}, 1},
		{"claim-owned", []any{"completed", "completed"}, []string{"claim"}, 0},
		{"complete-owned", []any{"completed"}, []string{"complete"}, 0},
		{"mixed-claim", []any{"completed"}, []string{"mutation", "claim", "workflow_action"}, 0},
		{"mixed-complete", []any{"completed"}, []string{"workflow_action", "complete", "complete"}, 0},
		{"intermediate", []any{nil, "pending", "partial", "failed", "failed_stale"}, []string{"mutation"}, 0},
	} {
		for i, result := range tc.results {
			epoch := i + 1
			retirementUpgradeExec(t, db, `INSERT INTO durable_operations(`+columns+`) VALUES(?,?,'work-upgrade-from','workflow.break_fix',?,'repair','external_effect',?,'{"scope":"retained"}',?,?,'["evidence:retained"]','["ref:retained"]','principal/survivor',?,'observed','completed',?)`, tc.id, epoch, testDigest("definition"), testDigest(tc.id), result, fmt.Sprintf(`{"id":%q,"epoch":%d}`, tc.id, epoch), "request:"+tc.id, testManifestDigest)
		}
		for i, kind := range tc.kinds {
			retirementUpgradeExec(t, db, `INSERT INTO idempotency_records(principal_ref,tool,operation_kind,idempotency_key,canonical_digest,op_id,result_event_ids,result_payload,changed_refs,authorized_scope_snapshot,replayed_count,first_observed_at,last_observed_at) VALUES('principal/survivor','tool',?,?,?,?,'["event:retained"]','{"result":"retained"}','["ref:retained"]','{"scope":"retained"}',7,'first','last')`, kind, fmt.Sprintf("%s-%d", tc.id, i), testDigest(tc.id), tc.id)
		}
		if tc.keepEpoch != 0 {
			keep = append(keep, tc.id)
			want = append(want, retirementUpgradeRows(t, db, `SELECT `+normalizedColumns+` FROM durable_operations WHERE op_id=? AND attempt_epoch=?`, tc.id, tc.keepEpoch)...)
		}
	}
	// An unrelated ordinary replay survives even without an operation, whereas
	// orphan claim/complete replays are retired with their protocol.
	for _, kind := range []string{"mutation", "claim", "complete"} {
		retirementUpgradeExec(t, db, `INSERT INTO idempotency_records(principal_ref,tool,operation_kind,idempotency_key,canonical_digest,op_id,first_observed_at,last_observed_at) VALUES('principal/orphan','tool',?,?,?,'orphan','first','last')`, kind, kind, testDigest(kind))
	}
	if _, err := db.Exec(`INSERT INTO idempotency_records(principal_ref,tool,operation_kind,idempotency_key,canonical_digest,op_id,first_observed_at,last_observed_at) VALUES('principal/null','tool','claim','null-op','digest',NULL,'first','last')`); err == nil {
		t.Fatal("predecessor idempotency schema admitted a NULL operation ID")
	}
	retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
	sort.Slice(want, func(i, j int) bool { return want[i][0].(string) < want[j][0].(string) })
	const replayOrder = ` ORDER BY principal_ref,tool,operation_kind,idempotency_key`
	wantReplays := retirementUpgradeRows(t, db, `SELECT * FROM idempotency_records WHERE op_id='successful-operation' OR (op_id='orphan' AND operation_kind='mutation')`+replayOrder)
	for _, id := range keep {
		wantReplays = append(wantReplays, retirementUpgradeRows(t, db, `SELECT * FROM idempotency_records WHERE op_id=?`+replayOrder, id)...)
	}
	sort.Slice(wantReplays, func(i, j int) bool {
		for column := 0; column < 4; column++ {
			left, right := wantReplays[i][column].(string), wantReplays[j][column].(string)
			if left != right {
				return left < right
			}
		}
		return false
	})
	before := retirementUpgradeSnapshot(t, db)
	ordinaryRecordsUpgradeExpectations(t, db, before)
	if _, err := Upgrade(t.Context(), path, nil); err != nil {
		t.Fatal(err)
	}
	if got := retirementUpgradeRows(t, db, `SELECT `+columns+` FROM durable_operations ORDER BY op_id`); !reflect.DeepEqual(got, want) {
		t.Fatalf("full operation survivors = %v, want %v", got, want)
	}
	if got := retirementUpgradeRows(t, db, `SELECT * FROM idempotency_records`+replayOrder); !reflect.DeepEqual(got, wantReplays) {
		t.Fatalf("full replay survivors = %v, want %v", got, wantReplays)
	}
	for _, table := range []string{"effect_receipts", "workflow_evidence_bindings", "archived_work", "workflow_delivery_state"} {
		if got := retirementUpgradeRows(t, db, `SELECT * FROM `+table+` ORDER BY rowid`); !reflect.DeepEqual(got, before[table]) {
			t.Errorf("migration changed retained %s", table)
		}
	}
	retirementUpgradeHealthy(t, db)
}

func TestAtomicOperationMigrationBoundedPopulation(t *testing.T) {
	// The full Upgrade seam is covered above. Build the operation tables from
	// their owning migration here so unrelated historical migrations do not
	// dominate this population check under the race detector.
	db, err := sql.Open(driverName, dataSourceName(filepath.Join(t.TempDir(), "atomic-population.db")))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := applyMigration(t.Context(), db, migrations[6]); err != nil {
		t.Fatal(err)
	}
	retirementUpgradeExec(t, db, `ALTER TABLE durable_operations ADD COLUMN workflow_type_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE durable_operations DROP COLUMN workflow_type_version;
CREATE TABLE workflow_evidence_bindings(payload TEXT);
CREATE TABLE write_guard(active INTEGER PRIMARY KEY)`)
	const population = 32768
	const projection = `op_id,attempt_epoch,step_kind,result_payload`
	var want [][]any
	retirementUpgradeExec(t, db, `WITH RECURSIVE population(n) AS (VALUES(0) UNION ALL SELECT n+1 FROM population WHERE n+1<?)
INSERT INTO durable_operations(op_id,attempt_epoch,work_id,workflow_type_ref,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,principal_ref,request_id,observed_at,contract_digest,result_kind,result_payload)
SELECT printf('population-%05d',n),1,'work-upgrade-from','workflow.break_fix','repair','external_effect','inputs','{}','principal/population','request','observed',?,CASE n%8 WHEN 3 THEN NULL WHEN 4 THEN 'pending' ELSE 'completed' END,json_object('n',n,'epoch',1) FROM population`, population, testManifestDigest)
	retirementUpgradeExec(t, db, `INSERT INTO durable_operations(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_digest,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,result_kind,result_payload,evidence_refs,changed_refs,resume_cursor,principal_ref,request_id,observed_at,completed_at,contract_digest)
SELECT op_id,3,work_id,workflow_type_ref,workflow_type_digest,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,'completed',json_set(result_payload,'$.epoch',3),evidence_refs,changed_refs,resume_cursor,principal_ref,request_id,observed_at,completed_at,contract_digest FROM durable_operations WHERE op_id LIKE 'population-%' AND CAST(substr(op_id,12) AS INTEGER)%8=6`)
	retirementUpgradeExec(t, db, `INSERT INTO idempotency_records(principal_ref,tool,operation_kind,idempotency_key,canonical_digest,op_id,first_observed_at,last_observed_at)
SELECT 'principal/population','tool','mutation',op_id,'digest',op_id,'first','last' FROM durable_operations WHERE op_id LIKE 'population-%' AND attempt_epoch=1 AND CAST(substr(op_id,12) AS INTEGER)%8<>7`)
	retirementUpgradeExec(t, db, `INSERT INTO idempotency_records(principal_ref,tool,operation_kind,idempotency_key,canonical_digest,op_id,first_observed_at,last_observed_at)
SELECT 'principal/population','tool',CASE CAST(substr(op_id,12) AS INTEGER)%8 WHEN 1 THEN 'complete' ELSE 'claim' END,op_id,'digest',op_id,'first','last' FROM durable_operations WHERE op_id LIKE 'population-%' AND attempt_epoch=1 AND CAST(substr(op_id,12) AS INTEGER)%8 IN (0,1,2)`)
	for n := 0; n < population; n++ {
		if n%8 < 5 {
			continue
		}
		epoch := 1
		if n%8 == 6 {
			epoch = 3
		}
		want = append(want, []any{fmt.Sprintf("population-%05d", n), int64(1), "internal_sqlite", fmt.Sprintf(`{"n":%d,"epoch":%d}`, n, epoch)})
	}
	sort.Slice(want, func(i, j int) bool { return want[i][0].(string) < want[j][0].(string) })
	// Only the migration consumes this budget, not fixture construction or host
	// admission. The population makes per-operation full scans exceed it while
	// leaving room for the set-based migration under the race detector.
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	start := time.Now()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := applyMigration(ctx, tx, migrations[177]); err != nil {
		t.Fatalf("atomic migration of %d operation IDs did not finish: %v", population, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	t.Logf("atomic migration of %d operation IDs: %s", population, time.Since(start))
	if got := retirementUpgradeRows(t, db, `SELECT `+projection+` FROM durable_operations ORDER BY op_id`); !reflect.DeepEqual(got, want) {
		t.Fatalf("full population survivor set differs: got %d rows, want %d", len(got), len(want))
	}
	retirementUpgradeHealthy(t, db)
}

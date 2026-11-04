package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func terminalStepsMigrationFixture(t *testing.T) (*sql.DB, string) {
	t.Helper()
	db, path := retirementUpgradeFixture(t)
	advanceHistoricalPrefix(t, db, 176)
	digest := "sha256:" + strings.Repeat("a", 64)
	definitionDigest := mustBuiltinDefinition(t, "workflow.implementation").Digest
	actor := "actor:" + strings.Repeat("a", 64)
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1)`)
	retirementUpgradeExec(t, db, `UPDATE work_items SET lifecycle='completed' WHERE id='work-upgrade-from'`)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_delivery_state(work_id,assertion_seq,assertion_id,actor,occurred_at,payload_version,payload) VALUES('work-upgrade-from',23,'event-upgrade-4','operator',?,1,'{ "delivery_artifact": "https://example.com/change/1", "delivery_state": "asserted" }')`, retirementUpgradeTime)
	for _, id := range []string{"work-upgrade-from", "work-upgrade-to"} {
		retirementUpgradeExec(t, db, `INSERT INTO workflow_instances(work_id,definition_ref,definition_digest,current_step,instance_state,execution_actor_ref,execution_model,started_at,completed_at,last_checkpoint_at,execution_started_at,outside_repair_state,completion_payload,completion_occurred_at,unhealthy_verdict_seq) VALUES(?,'workflow.implementation',?,'execution','outside_repair',?,?,'2026-08-01T00:00:00Z','2026-08-06T00:00:00Z','2026-08-03T00:00:00Z','2026-08-02T00:00:00Z','active','{"impact_verdict":"non-breaking"}','2026-08-05T00:00:00Z',19)`, id, definitionDigest, actor, id)
		retirementUpgradeExec(t, db, `INSERT INTO workflow_step_epochs(work_id,step_id,attempt_epoch) VALUES(?,'execute',1)`, id)
		retirementUpgradeExec(t, db, `INSERT INTO workflow_step_completions VALUES(?,'execute',23)`, id)
	}
	retirementUpgradeExec(t, db, `INSERT INTO outside_repair_dispositions VALUES('work-upgrade-to','active','synthetic hold','{}',?)`, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO outside_repair_reconciliations VALUES('synthetic-reconciliation','work-upgrade-from','synthetic completion','{}',?)`, retirementUpgradeTime)
	for _, op := range []struct{ id, family, result string }{
		{"successful-operation", "workflow.break_fix", "completed"},
		{"partial-publication", "concord.pm6.compaction", "partial"},
		{"pending-publication", "concord.pm6.compaction", "pending"},
	} {
		retirementUpgradeExec(t, db, `INSERT INTO durable_operations(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_digest,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,principal_ref,request_id,observed_at,contract_digest,result_kind) VALUES(?,1,'work-upgrade-from',? ,?,'execute','internal_sqlite',?,'{}','operator','request',?,?,?)`, op.id, op.family, digest, digest, retirementUpgradeTime, digest, op.result)
		retirementUpgradeExec(t, db, `INSERT INTO idempotency_records(principal_ref,tool,operation_kind,idempotency_key,canonical_digest,op_id,result_event_ids,first_observed_at,last_observed_at) VALUES('operator','tool','mutation',?,?,?,'["event-upgrade-4"]',?,?)`, op.id, digest, op.id, retirementUpgradeTime, retirementUpgradeTime)
	}
	retirementUpgradeExec(t, db, `INSERT INTO workflow_evidence_bindings VALUES('work-upgrade-from',24,'binding:ordinary','operator','{"producer_run_ref":"successful-operation"}'),('work-upgrade-from',25,'binding:publication','operator','{"producer_run_ref":"partial-publication"}')`)
	retirementUpgradeExec(t, db, `INSERT INTO project_locators(project_id,locator_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('project-upgrade','historical-home','canonical_path','/synthetic/history','/synthetic/history',?,?)`, retirementUpgradeTime, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO archived_work(id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,home_project_id,home_locator_id,note_path,commit_oid,content_hash) VALUES('historical-work','work_note','Historical outcome',?,'delivered','["lesson:independent"]','completed',0,'Preserve the historical locator','project-upgrade','historical-home','.concord/notes/historical-work.md',?,?)`, retirementUpgradeTime, strings.Repeat("a", 40), digest)
	retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
	retirementUpgradeHealthy(t, db)
	return db, path
}

func TestTerminalStepsRetirementVerifiedSnapshotUpgrade(t *testing.T) {
	useStampedBuild(t)
	db, path := terminalStepsMigrationFixture(t)
	before := retirementUpgradeSnapshot(t, db)
	retainedContracts := retainedWorkflowContractRows(t, db)
	const instanceColumns = `SELECT work_id,definition_ref,definition_digest,current_step,execution_actor_ref,execution_model,started_at,completed_at,last_checkpoint_at,execution_started_at,completion_payload,completion_occurred_at FROM workflow_instances ORDER BY work_id`
	instances := retirementUpgradeRows(t, db, instanceColumns)
	const deliveryColumns = `SELECT work_id,assertion_seq,assertion_id,actor,occurred_at,payload_version,payload FROM workflow_delivery_state ORDER BY work_id`
	deliveries := retirementUpgradeRows(t, db, deliveryColumns)
	bindings := retirementUpgradeRows(t, db, `SELECT * FROM workflow_evidence_bindings WHERE binding_id='binding:ordinary'`)
	if s, err := Open(t.Context(), path); err == nil {
		s.Close()
		t.Fatal("ordinary open crossed terminal step retirement")
	} else {
		assertFailureKind(t, err, KindUpgradeRequired)
	}
	if _, err := Upgrade(t.Context(), path, []HeldSchema{{PID: 1234, SchemaVersion: 176}}); err == nil {
		t.Fatal("upgrade crossed a live predecessor session")
	} else {
		assertFailureKind(t, err, KindUpgradeBlocked)
	}
	if after := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(before, after) {
		t.Fatal("refused upgrade changed predecessor state")
	}
	snapshot := filepath.Join(t.TempDir(), "pre-terminal-steps.db")
	manifest, err := BackupDatabase(t.Context(), path, snapshot)
	if err != nil || manifest.SchemaVersion != 176 {
		t.Fatalf("verified predecessor snapshot=%+v err=%v", manifest, err)
	}
	if _, err := Upgrade(t.Context(), path, nil); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"work_items", "effect_receipts", "archived_work"} {
		if after := retirementUpgradeRows(t, db, "SELECT * FROM "+table+" ORDER BY rowid"); !reflect.DeepEqual(before[table], after) {
			t.Errorf("retirement changed surviving %s", table)
		}
	}
	if got := retainedWorkflowContractRows(t, db); !reflect.DeepEqual(got, retainedContracts) {
		t.Error("current contract facts changed")
	}
	if after := retirementUpgradeRows(t, db, deliveryColumns); !reflect.DeepEqual(deliveries, after) {
		t.Errorf("retirement changed effective delivery: before=%v after=%v", deliveries, after)
	}
	for _, table := range []string{"workflow_step_epochs", "workflow_step_completions"} {
		if after := retirementUpgradeRows(t, db, "SELECT work_id FROM "+table); !reflect.DeepEqual(after, [][]any{{"work-upgrade-to"}}) {
			t.Errorf("%s retained=%v, want only the live work", table, after)
		}
	}
	for _, table := range []string{"durable_operations", "idempotency_records"} {
		if after := retirementUpgradeRows(t, db, "SELECT op_id FROM "+table); !reflect.DeepEqual(after, [][]any{{"successful-operation"}}) {
			t.Errorf("%s retained=%v, want only successful ordinary mutation", table, after)
		}
	}
	if after := retirementUpgradeRows(t, db, instanceColumns); !reflect.DeepEqual(after, instances) {
		t.Errorf("instance rebuild changed surviving fields: before=%v after=%v", instances, after)
	}
	if after := retirementUpgradeRows(t, db, `SELECT * FROM workflow_evidence_bindings`); !reflect.DeepEqual(after, bindings) {
		t.Errorf("evidence binding retirement changed ordinary bindings: before=%v after=%v", bindings, after)
	}
	if guards := retirementUpgradeRows(t, db, `SELECT * FROM write_guard`); len(guards) != 0 {
		t.Errorf("upgrade left write guards=%v", guards)
	}
	if states := retirementUpgradeRows(t, db, `SELECT work_id,instance_state FROM workflow_instances ORDER BY work_id`); !reflect.DeepEqual(states, [][]any{{"work-upgrade-from", "completed"}, {"work-upgrade-to", "running"}}) {
		t.Errorf("outside-repair retirement changed ordinary or terminal identity: %v", states)
	}
	if rows := retirementUpgradeRows(t, db, `SELECT name FROM sqlite_schema WHERE name IN ('outside_repair_dispositions','outside_repair_reconciliations','workflow_instances_v177','durable_operations_v178')`); len(rows) != 0 {
		t.Errorf("retired outside-repair residue=%v", rows)
	}
	if columns := retirementUpgradeRows(t, db, `SELECT name FROM pragma_table_info('workflow_instances') WHERE name='outside_repair_state'`); len(columns) != 0 {
		t.Errorf("retired outside-repair column=%v", columns)
	}
	if verified, err := VerifyBackup(t.Context(), snapshot); err != nil || verified.DBChecksum != manifest.DBChecksum {
		t.Fatalf("predecessor snapshot changed: %+v, %v", verified, err)
	}
	retirementUpgradeHealthy(t, db)
}

func TestTerminalStepsRetirementRollback(t *testing.T) {
	useStampedBuild(t)
	db, path := terminalStepsMigrationFixture(t)
	retirementUpgradeExec(t, db, `CREATE TRIGGER terminal_steps_failure BEFORE DELETE ON durable_operations WHEN OLD.workflow_type_ref='concord.pm6.compaction' BEGIN SELECT RAISE(ABORT,'terminal steps rollback witness'); END`)
	before := retirementUpgradeSnapshot(t, db)
	_, err := Upgrade(t.Context(), path, nil)
	assertFailureKind(t, err, KindUnavailable)
	if !strings.Contains(err.Error(), "terminal steps rollback witness") {
		t.Fatalf("upgrade did not reach the destructive rollback witness: %v", err)
	}
	if after := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(before, after) {
		t.Fatal("failed retirement changed predecessor rows or manifest")
	}
	retirementUpgradeExec(t, db, `DROP TRIGGER terminal_steps_failure`)
	if _, err := Upgrade(t.Context(), path, nil); err != nil {
		t.Fatal(err)
	}
	retirementUpgradeHealthy(t, db)
}

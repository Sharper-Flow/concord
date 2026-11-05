package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWorkflowAdmissionCutoverPreservesWorkAndLinearIdentity(t *testing.T) {
	useStampedBuild(t)
	ctx := t.Context()
	db, path := retirementUpgradeFixture(t)
	advanceHistoricalPrefix(t, db, 182)
	actor := "actor:" + strings.Repeat("a", 64)
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1)`)
	retirementUpgradeExec(t, db, `UPDATE work_items SET lifecycle='in_progress' WHERE id='work-upgrade-from'`)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_instances(work_id,definition_ref,definition_digest,current_step,instance_state,execution_actor_ref,started_at) VALUES('work-upgrade-from','workflow.implementation',?,'release','running',?,?)`, "sha256:"+strings.Repeat("b", 64), actor, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_instances(work_id,definition_ref,definition_digest,current_step,instance_state,execution_actor_ref,started_at) VALUES('work-upgrade-to','workflow.implementation',?,'release','running',?,?)`, "sha256:"+strings.Repeat("b", 64), actor, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_delivery_state(work_id,assertion_seq,assertion_id,actor,occurred_at,payload_version,payload) VALUES('work-upgrade-to',23,'event-upgrade-4','operator',?,1,'{"delivery_artifact":"https://example.com/change/1"}')`, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_predicate_verdicts(work_id,contract_version,predicate_id,verdict_seq,actor,payload) VALUES('work-upgrade-from',1,'predicate:retained',23,?,'{ "verdict": "pass", "evidence": ["ref:proof"] }')`, actor)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_step_epochs(work_id,step_id,attempt_epoch) VALUES('work-upgrade-from','release',1)`)
	retirementUpgradeExec(t, db, `INSERT INTO linear_issue_links(work_id,remote_issue_uuid,human_key,url,created_at,updated_at) VALUES('work-upgrade-from','uuid-confirmed','CON-1','https://linear.app/x/issue/CON-1',?,?)`, retirementUpgradeTime, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
	work := retirementUpgradeRows(t, db, `SELECT * FROM work_items WHERE id='work-upgrade-from'`)
	links := retirementUpgradeRows(t, db, `SELECT * FROM linear_issue_links ORDER BY work_id`)
	receipts := retirementUpgradeRows(t, db, `SELECT * FROM effect_receipts ORDER BY effect_seq`)
	actors := retirementUpgradeRows(t, db, `SELECT * FROM workflow_actors ORDER BY actor_ref`)
	delivery := retirementUpgradeRows(t, db, `SELECT * FROM workflow_delivery_state ORDER BY work_id`)
	snapshot := filepath.Join(t.TempDir(), "before-cutover.db")
	manifest, err := BackupDatabase(ctx, path, snapshot)
	if err != nil {
		t.Fatalf("snapshot predecessor: %v", err)
	}
	if _, err := Upgrade(ctx, path, nil, UpgradeOptions{SnapshotPath: snapshot}); err != nil {
		t.Fatalf("upgrade predecessor: %v", err)
	}
	if got := retirementUpgradeRows(t, db, `SELECT * FROM work_items WHERE id='work-upgrade-from'`); !reflect.DeepEqual(got, work) {
		t.Fatalf("work identity changed: before=%v after=%v", work, got)
	}
	if got := retirementUpgradeRows(t, db, `SELECT * FROM linear_issue_links ORDER BY work_id`); !reflect.DeepEqual(got, links) {
		t.Fatalf("Linear identity changed: before=%v after=%v", links, got)
	}
	if got := retirementUpgradeRows(t, db, `SELECT * FROM effect_receipts ORDER BY effect_seq`); !reflect.DeepEqual(got, receipts) {
		t.Fatalf("effect receipts changed: before=%v after=%v", receipts, got)
	}
	for query, want := range map[string][][]any{
		`SELECT * FROM workflow_actors ORDER BY actor_ref`:       actors,
		`SELECT * FROM workflow_delivery_state ORDER BY work_id`: delivery,
	} {
		if got := retirementUpgradeRows(t, db, query); !reflect.DeepEqual(got, want) {
			t.Errorf("retained facts changed for %s: before=%v after=%v", query, want, got)
		}
	}
	var state string
	var planType, plan, owner sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT instance_state,workflow_type,step_plan,execution_actor_ref FROM workflow_instances WHERE work_id='work-upgrade-from'`).Scan(&state, &planType, &plan, &owner); err != nil {
		t.Fatal(err)
	}
	if state != "intake" || planType.Valid || plan.Valid || owner.Valid {
		t.Fatalf("undelivered workflow did not return to intake: state=%q type=%v plan=%v owner=%v", state, planType, plan, owner)
	}
	if got := retirementUpgradeRows(t, db, `SELECT w.lifecycle,i.instance_state,i.workflow_type,i.step_plan,i.execution_actor_ref FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id='work-upgrade-to'`); !reflect.DeepEqual(got, [][]any{{"completed", "completed", nil, nil, actor}}) {
		t.Fatalf("delivered workflow did not complete: %v", got)
	}
	if got := retirementUpgradeRows(t, db, `SELECT * FROM workflow_step_epochs`); len(got) != 0 {
		t.Fatalf("retired step epochs survived: %v", got)
	}
	verified, err := VerifyBackup(ctx, snapshot)
	if err != nil || verified.DBChecksum != manifest.DBChecksum || verified.SchemaVersion != 182 {
		t.Fatalf("predecessor snapshot changed: %+v %v", verified, err)
	}
}

func TestWorkflowAdmissionCutoverRollback(t *testing.T) {
	useStampedBuild(t)
	db, path := retirementUpgradeFixture(t)
	advanceHistoricalPrefix(t, db, 182)
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1)`)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_step_epochs(work_id,step_id,attempt_epoch) VALUES('work-upgrade-from','release',1)`)
	retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
	retirementUpgradeExec(t, db, `CREATE TRIGGER cutover_rollback_witness BEFORE DELETE ON workflow_step_epochs BEGIN SELECT RAISE(ABORT,'cutover rollback witness'); END`)
	before := retirementUpgradeSnapshot(t, db)
	_, err := Upgrade(t.Context(), path, nil)
	if err == nil || !strings.Contains(err.Error(), "cutover rollback witness") {
		t.Fatalf("upgrade did not reach rollback witness: %v", err)
	}
	if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
		t.Fatal("failed cutover changed predecessor schema or rows")
	}
	retirementUpgradeExec(t, db, `DROP TRIGGER cutover_rollback_witness`)
	if _, err := Upgrade(t.Context(), path, nil); err != nil {
		t.Fatal(err)
	}
	retirementUpgradeHealthy(t, db)
}

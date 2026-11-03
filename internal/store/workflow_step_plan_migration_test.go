package store

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestWorkflowStepPlanCutover(t *testing.T) {
	useStampedBuild(t)
	db, path := terminalStepsMigrationFixture(t)
	advanceHistoricalPrefix(t, db, 182)
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1);
UPDATE work_items SET lifecycle='cancelled' WHERE id='work-upgrade-from';
INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES('work-delivered','task','Delivered','in_progress',0,1,?,?);
INSERT INTO work_projects VALUES('work-delivered','project-upgrade','primary');
INSERT INTO workflow_instances(work_id,definition_ref,definition_digest,current_step,instance_state) SELECT 'work-delivered',definition_ref,definition_digest,current_step,'running' FROM workflow_instances WHERE work_id='work-upgrade-to';
INSERT INTO workflow_delivery_state SELECT 'work-delivered',assertion_seq,assertion_id,actor,occurred_at,payload_version,payload FROM workflow_delivery_state WHERE work_id='work-upgrade-from';
INSERT INTO linear_issue_links VALUES('work-upgrade-to','synthetic-cutover-issue','TEST-1574','https://example.com/issues/TEST-1574','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
INSERT INTO workflow_coordinator_actions VALUES('work-delivered','actor-delivered',1,'record_delivery','2026-01-01T00:00:00Z');
DELETE FROM write_guard`, retirementUpgradeTime, retirementUpgradeTime)
	contracts := retirementUpgradeRows(t, db, `SELECT work_id,contract_version,premise,approved_at,approved_by FROM workflow_contracts ORDER BY work_id`)
	delivery := retirementUpgradeRows(t, db, `SELECT * FROM workflow_delivery_state ORDER BY work_id`)
	receipts := retirementUpgradeRows(t, db, `SELECT * FROM effect_receipts ORDER BY effect_seq`)
	membership := retirementUpgradeRows(t, db, `SELECT * FROM work_projects ORDER BY work_id`)
	links := retirementUpgradeRows(t, db, `SELECT * FROM linear_issue_links ORDER BY work_id`)
	instanceFacts := retirementUpgradeRows(t, db, `SELECT i.work_id,CASE WHEN w.lifecycle IN ('completed','cancelled','superseded') OR EXISTS(SELECT 1 FROM workflow_delivery_state d WHERE d.work_id=i.work_id) THEN i.execution_actor_ref ELSE NULL END,CASE WHEN w.lifecycle IN ('completed','cancelled','superseded') OR EXISTS(SELECT 1 FROM workflow_delivery_state d WHERE d.work_id=i.work_id) THEN i.execution_model ELSE '' END,i.started_at,i.completed_at,i.last_checkpoint_at,i.execution_started_at,i.completion_payload,i.completion_occurred_at FROM workflow_instances i JOIN work_items w ON w.id=i.work_id ORDER BY i.work_id`)
	before := retirementUpgradeSnapshot(t, db)
	if _, err := Upgrade(t.Context(), path, []HeldSchema{{PID: 1234, SchemaVersion: 182}}); err == nil {
		t.Fatal("upgrade crossed a held predecessor session")
	} else {
		assertFailureKind(t, err, KindUpgradeBlocked)
	}
	if after := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(before, after) {
		t.Fatal("refused upgrade changed predecessor state")
	}
	snapshot := filepath.Join(t.TempDir(), "before-step-plan.db")
	manifest, err := BackupDatabase(t.Context(), path, snapshot)
	if err != nil || manifest.SchemaVersion != 182 {
		t.Fatalf("predecessor backup=%+v err=%v", manifest, err)
	}
	if _, err := Upgrade(t.Context(), path, nil); err != nil {
		t.Fatal(err)
	}
	got := retirementUpgradeRows(t, db, `SELECT i.work_id,w.lifecycle,i.instance_state,i.workflow_type,i.step_plan FROM workflow_instances i JOIN work_items w ON w.id=i.work_id ORDER BY i.work_id`)
	want := [][]any{{"work-delivered", "completed", "completed", nil, nil}, {"work-upgrade-from", "cancelled", "cancelled", nil, nil}, {"work-upgrade-to", "needed", "intake", nil, nil}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cutover states=%v want %v", got, want)
	}
	// A delivered item the cutover completes is terminal like any completed
	// item: it carries its terminal time and keeps no step records.
	if got := retirementUpgradeRows(t, db, `SELECT w.terminal_time=d.occurred_at,(SELECT count(*) FROM workflow_coordinator_actions c WHERE c.work_id=w.id) FROM work_items w JOIN workflow_delivery_state d ON d.work_id=w.id WHERE w.id='work-delivered'`); !reflect.DeepEqual(got, [][]any{{int64(1), int64(0)}}) {
		t.Fatalf("cutover-completed item terminal facts=%v", got)
	}
	if got := retirementUpgradeRows(t, db, `SELECT work_id,contract_version,premise,approved_at,approved_by FROM workflow_contracts ORDER BY work_id`); !reflect.DeepEqual(got, contracts) {
		t.Fatalf("contracts changed: %v", got)
	}
	if got := retirementUpgradeRows(t, db, `SELECT * FROM workflow_delivery_state ORDER BY work_id`); !reflect.DeepEqual(got, delivery) {
		t.Fatalf("delivery changed: %v", got)
	}
	if got := retirementUpgradeRows(t, db, `SELECT * FROM effect_receipts ORDER BY effect_seq`); !reflect.DeepEqual(got, receipts) {
		t.Fatalf("receipts changed: %v", got)
	}
	if got := retirementUpgradeRows(t, db, `SELECT * FROM work_projects ORDER BY work_id`); !reflect.DeepEqual(got, membership) {
		t.Fatalf("membership changed: %v", got)
	}
	if got := retirementUpgradeRows(t, db, `SELECT * FROM linear_issue_links ORDER BY work_id`); !reflect.DeepEqual(got, links) {
		t.Fatalf("Linear links changed: %v", got)
	}
	if got := retirementUpgradeRows(t, db, `SELECT work_id,execution_actor_ref,execution_model,started_at,completed_at,last_checkpoint_at,execution_started_at,completion_payload,completion_occurred_at FROM workflow_instances ORDER BY work_id`); !reflect.DeepEqual(got, instanceFacts) {
		t.Fatalf("instance facts changed: %v", got)
	}
	columns := retirementUpgradeRows(t, db, `SELECT name FROM pragma_table_info('workflow_instances') ORDER BY cid`)
	wantColumns := [][]any{{"work_id"}, {"workflow_type"}, {"step_plan"}, {"prelude_state"}, {"instance_state"}, {"execution_actor_ref"}, {"execution_model"}, {"started_at"}, {"completed_at"}, {"last_checkpoint_at"}, {"execution_started_at"}, {"completion_payload"}, {"completion_occurred_at"}}
	if !reflect.DeepEqual(columns, wantColumns) {
		t.Fatalf("instance shape=%v want %v", columns, wantColumns)
	}
	stopped, err := workExecutionStoppedTx(t.Context(), db, "work-upgrade-to", "")
	if err != nil || !stopped {
		t.Fatalf("parked predecessor has unfinished execution: stopped=%v err=%v", stopped, err)
	}
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1)`)
	for _, stmt := range []string{
		`UPDATE workflow_instances SET workflow_type='unknown' WHERE work_id='work-upgrade-to'`,
		`UPDATE workflow_instances SET step_plan='[]' WHERE work_id='work-upgrade-to'`,
		`UPDATE workflow_instances SET step_plan='not json' WHERE work_id='work-upgrade-to'`,
	} {
		if _, err := db.Exec(stmt); err == nil {
			t.Fatalf("constraint accepted %s", stmt)
		}
	}
	retirementUpgradeExec(t, db, `UPDATE workflow_instances SET workflow_type='change',step_plan='{}' WHERE work_id='work-upgrade-to'; DELETE FROM write_guard`)
	if verified, err := VerifyBackup(t.Context(), snapshot); err != nil || verified.DBChecksum != manifest.DBChecksum {
		t.Fatalf("predecessor backup changed: %+v err=%v", verified, err)
	}
	retirementUpgradeHealthy(t, db)
}

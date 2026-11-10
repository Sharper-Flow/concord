package store

import (
	"reflect"
	"strings"
	"testing"
)

func TestWorkflowVerifyingRetirementUpgrade(t *testing.T) {
	useStampedBuild(t)
	db := captureRetirementFixture(t)
	ctx := verifiedOutcomeRetirementContext(t)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations[185:196] {
		if err := applyMigration(ctx, tx, m); err != nil {
			t.Fatal(err)
		}
		if err := advanceStoreRevision(ctx, tx); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,breaking,applied_at) VALUES(?,?,?,?,?)`, m.Version, m.Name, m.checksum(), m.Breaking, "2026-10-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1);
UPDATE workflow_instances SET instance_state='verifying' WHERE work_id='work-one';
INSERT INTO workflow_instances(work_id,instance_state,execution_model) VALUES('work-two','blocked','model-two');
INSERT INTO work_items(id,kind,title,lifecycle,version,created_at,updated_at) VALUES
 ('work-intake','task','Intake','needed',1,'t','t'),
 ('work-running','task','Running','in_progress',1,'t','t'),
 ('work-completed','task','Completed','completed',1,'t','t'),
 ('work-cancelled','task','Cancelled','cancelled',1,'t','t');
INSERT INTO workflow_instances(work_id,instance_state) VALUES
 ('work-intake','intake'),('work-running','running'),('work-completed','completed'),('work-cancelled','cancelled');
DELETE FROM write_guard`)
	const facts = `SELECT work_id,workflow_type,step_plan,prelude_state,execution_actor_ref,execution_model,started_at,completed_at,last_checkpoint_at,execution_started_at,completion_payload,completion_occurred_at FROM workflow_instances ORDER BY work_id`
	before := retirementUpgradeRows(t, db, facts)
	objects := retirementUpgradeRows(t, db, `SELECT type,name,sql FROM sqlite_master WHERE tbl_name='workflow_instances' AND type IN ('index','trigger') ORDER BY type,name`)
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if got := retirementUpgradeRows(t, db, `SELECT work_id,instance_state FROM workflow_instances ORDER BY work_id`); !reflect.DeepEqual(got, [][]any{{"work-cancelled", "cancelled"}, {"work-completed", "completed"}, {"work-intake", "intake"}, {"work-one", "running"}, {"work-running", "running"}, {"work-two", "blocked"}}) {
		t.Fatalf("upgraded states = %v", got)
	}
	if got := retirementUpgradeRows(t, db, facts); !reflect.DeepEqual(got, before) {
		t.Fatalf("instance facts changed: %v", got)
	}
	if got := retirementUpgradeRows(t, db, `SELECT type,name,sql FROM sqlite_master WHERE tbl_name='workflow_instances' AND type IN ('index','trigger') ORDER BY type,name`); !reflect.DeepEqual(got, objects) {
		t.Fatalf("instance indexes or triggers changed: %v", got)
	}
	if _, err := db.Exec(`UPDATE workflow_instances SET instance_state='running' WHERE work_id='work-two'`); err == nil || !strings.Contains(err.Error(), "write-seam-only") {
		t.Fatalf("write guard did not refuse update: %v", err)
	}
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1)`)
	if _, err := db.Exec(`UPDATE workflow_instances SET instance_state='verifying' WHERE work_id='work-one'`); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("upgraded store did not reject verifying by CHECK: %v", err)
	}
	retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
	retirementUpgradeHealthy(t, db)
}

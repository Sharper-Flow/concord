package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func checkpointV168(t *testing.T) *sql.DB {
	t.Helper()
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "checkpoint.db"), 168)
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1)`)
	retirementUpgradeExec(t, db, `INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('product-checkpoint','Checkpoint product','prototype','operator_only',1,'now','now')`)
	retirementUpgradeExec(t, db, `INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('project-checkpoint','Checkpoint project',1,'now','now')`)
	retirementUpgradeExec(t, db, `INSERT INTO product_projects(product_id,project_id,role) VALUES('product-checkpoint','project-checkpoint','primary')`)
	retirementUpgradeExec(t, db, `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES('work-checkpoint','task','Checkpoint migration','needed',0,1,'now','now')`)
	retirementUpgradeExec(t, db, `INSERT INTO work_projects(work_id,project_id,role) VALUES('work-checkpoint','project-checkpoint','primary')`)
	actor := "actor:" + strings.Repeat("a", 64)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,'principal','client','agent','session','operator','now')`, actor)
	for i, kind := range []string{"internal_sqlite", "cross_authority", "external_effect", "human_checkpoint"} {
		retirementUpgradeExec(t, db, `INSERT INTO workflow_checkpoints VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, "work-checkpoint", fmt.Sprintf("checkpoint-%d", i), fmt.Sprintf("step-%d", i), kind, i+1, fmt.Sprintf("digest-%d", i), `["evidence:one"]`, fmt.Sprintf("cursor-%d", i), fmt.Sprintf("identity-%d", i), actor, fmt.Sprintf("request-%d", i), "2026-01-02T03:04:05Z")
	}
	retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
	return db
}

func TestCheckpointKindMigrationPreservesRowsAndConstraints(t *testing.T) {
	db := checkpointV168(t)
	before := retirementUpgradeRows(t, db, `SELECT * FROM workflow_checkpoints ORDER BY checkpoint_id`)
	columns := tableColumnInventory(t, db, "workflow_checkpoints")
	fks := foreignKeyDeclarations(t, db, "workflow_checkpoints")
	objects := retirementUpgradeRows(t, db, `SELECT type,name,sql FROM sqlite_schema WHERE tbl_name='workflow_checkpoints' AND type='trigger' ORDER BY name`)
	if err := migrateAtOpen(t.Context(), db); err == nil {
		t.Fatal("ordinary open admitted the pending breaking migration")
	}
	if got := retirementUpgradeRows(t, db, `SELECT * FROM workflow_checkpoints ORDER BY checkpoint_id`); !reflect.DeepEqual(got, before) {
		t.Fatalf("refused open changed rows: %#v", got)
	}
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	var breaking int
	if err := db.QueryRow(`SELECT breaking FROM schema_migrations WHERE version=169`).Scan(&breaking); err != nil || breaking != 1 {
		t.Fatalf("migration 169 breaking bit = %d: %v", breaking, err)
	}
	before[3][3] = "internal_sqlite"
	if got := retirementUpgradeRows(t, db, `SELECT * FROM workflow_checkpoints ORDER BY checkpoint_id`); !reflect.DeepEqual(got, before) {
		t.Fatalf("rows changed: got %#v want %#v", got, before)
	}
	assertUnchangedDump(t, "columns", columns, tableColumnInventory(t, db, "workflow_checkpoints"))
	assertUnchangedDump(t, "foreign keys", fks, foreignKeyDeclarations(t, db, "workflow_checkpoints"))
	if got := retirementUpgradeRows(t, db, `SELECT type,name,sql FROM sqlite_schema WHERE tbl_name='workflow_checkpoints' AND type='trigger' ORDER BY name`); !reflect.DeepEqual(got, objects) || len(got) != 3 {
		t.Fatalf("guards changed: %#v before %#v", got, objects)
	}
	for _, query := range []string{`INSERT INTO workflow_checkpoints SELECT * FROM workflow_checkpoints LIMIT 1`, `UPDATE workflow_checkpoints SET step_kind=step_kind`, `DELETE FROM workflow_checkpoints`} {
		if _, err := db.Exec(query); err == nil || !strings.Contains(err.Error(), "write-seam-only") {
			t.Fatalf("unguarded %s: %v", query, err)
		}
	}
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1)`)
	for _, query := range []string{
		`UPDATE workflow_checkpoints SET step_kind='human_checkpoint' WHERE checkpoint_id='checkpoint-0'`,
		`UPDATE workflow_checkpoints SET checkpoint_id='checkpoint-0' WHERE checkpoint_id='checkpoint-1'`,
		`UPDATE workflow_checkpoints SET step_id='step-0',attempt_epoch=1 WHERE checkpoint_id='checkpoint-1'`,
		`UPDATE workflow_checkpoints SET idempotency_identity='identity-0' WHERE checkpoint_id='checkpoint-1'`,
		`UPDATE workflow_checkpoints SET actor_ref='missing-actor' WHERE checkpoint_id='checkpoint-0'`,
		`UPDATE workflow_checkpoints SET work_id='missing-work' WHERE checkpoint_id='checkpoint-0'`,
	} {
		if _, err := db.Exec(query); err == nil {
			t.Fatalf("constraint admitted %s", query)
		}
	}
	retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
	if got := retirementUpgradeRows(t, db, `PRAGMA foreign_key_check`); len(got) != 0 {
		t.Fatalf("foreign keys: %#v", got)
	}
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if got := retirementUpgradeRows(t, db, `SELECT * FROM workflow_checkpoints ORDER BY checkpoint_id`); !reflect.DeepEqual(got, before) {
		t.Fatalf("second migration changed rows: %#v", got)
	}
}

func TestCheckpointKindMigrationFailureAndSnapshotRecovery(t *testing.T) {
	useStampedBuild(t)
	db := checkpointV168(t)
	var source string
	if err := db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&source); err != nil {
		t.Fatal(err)
	}
	before := retirementUpgradeRows(t, db, `SELECT * FROM workflow_checkpoints ORDER BY checkpoint_id`)
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	manifest, err := BackupDatabase(t.Context(), source, snapshot)
	if err != nil || manifest.SchemaVersion != 168 {
		t.Fatalf("verified v168 backup: %+v %v", manifest, err)
	}
	retirementUpgradeExec(t, db, `CREATE TABLE workflow_checkpoints_v169(collision TEXT)`)
	if err := Migrate(t.Context(), db); err == nil {
		t.Fatal("collision did not fail migration")
	}
	if got := retirementUpgradeRows(t, db, `SELECT * FROM workflow_checkpoints ORDER BY checkpoint_id`); !reflect.DeepEqual(got, before) {
		t.Fatalf("failed migration changed rows: %#v", got)
	}
	var applied int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version=169`).Scan(&applied); err != nil || applied != 0 {
		t.Fatalf("failed migration manifest = %d: %v", applied, err)
	}
	retirementUpgradeExec(t, db, `DROP TABLE workflow_checkpoints_v169`)
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if _, err := VerifyBackup(t.Context(), snapshot); err != nil {
		t.Fatalf("verify retained snapshot: %v", err)
	}
	snapshotDB, err := sql.Open(driverName, dataSourceName(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if got := retirementUpgradeRows(t, snapshotDB, `SELECT * FROM workflow_checkpoints ORDER BY checkpoint_id`); !reflect.DeepEqual(got, before) {
		t.Fatalf("snapshot changed pre-upgrade rows: %#v", got)
	}
	if err := snapshotDB.Close(); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored.db")
	if _, err := RestoreBackup(t.Context(), snapshot, restored); err != nil {
		t.Fatalf("restore: %v", err)
	}
	recovered, err := sql.Open(driverName, dataSourceName(restored))
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	before[3][3] = "internal_sqlite"
	if got := retirementUpgradeRows(t, recovered, `SELECT * FROM workflow_checkpoints ORDER BY checkpoint_id`); !reflect.DeepEqual(got, before) {
		t.Fatalf("restored migrated rows changed: %#v", got)
	}
	if err := Migrate(t.Context(), recovered); err != nil {
		t.Fatalf("upgrade restored store: %v", err)
	}
	if got := retirementUpgradeRows(t, recovered, `SELECT * FROM workflow_checkpoints ORDER BY checkpoint_id`); !reflect.DeepEqual(got, before) {
		t.Fatalf("recovered upgrade changed rows: %#v", got)
	}
}

package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
)

func TestBacklogAlignmentRetirementUpgradeSnapshot(t *testing.T) {
	useStampedBuild(t)
	db := captureRetirementFixture(t)
	ctx := verifiedOutcomeRetirementContext(t)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations[185:197] {
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
	var seq int
	var name, path string
	if err := db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1);
INSERT INTO workflow_backlog_alignment(work_id,related_work_id,searched,outcome,recorded_at,recorded_by)
SELECT 'work-one',NULL,'searched','related_found','time',actor_ref FROM workflow_actors LIMIT 1;
INSERT INTO workflow_backlog_alignment(work_id,related_work_id,searched,outcome,recorded_at,recorded_by)
SELECT 'work-one','work-two','searched','related_found','time',actor_ref FROM workflow_actors LIMIT 1;
DELETE FROM write_guard`)
	if err := ensureInstallationKey(ctx, db); err != nil {
		t.Fatal(err)
	}
	before := retirementUpgradeSnapshot(t, db)
	triggers := retirementUpgradeRows(t, db, `SELECT name,tbl_name,sql FROM sqlite_schema WHERE type='trigger' AND tbl_name NOT IN ('workflow_backlog_alignment','workflow_design_records','workflow_proposal_records') ORDER BY name`)
	if len(before["workflow_backlog_alignment"]) != 2 {
		t.Fatal("predecessor alignment fixture missing")
	}
	rollback, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	failing := migrations[197]
	failing.SQL += "\nSELECT * FROM nonexistent_alignment_upgrade_failure;"
	if err := applyMigration(ctx, rollback, failing); err == nil {
		_ = rollback.Rollback()
		t.Fatal("injected post-drop failure succeeded")
	}
	if err := rollback.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
		t.Fatal("failed migration did not roll back predecessor state")
	}
	snapshot := filepath.Join(t.TempDir(), "predecessor.db")
	manifest, err := BackupDatabase(t.Context(), path, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyBackup(t.Context(), snapshot)
	if err != nil || verified.DBChecksum != manifest.DBChecksum || verified.SchemaVersion != 197 {
		t.Fatalf("verified snapshot: %+v, %v", verified, err)
	}
	predecessor, err := sql.Open(driverName, readOnlyDataSource(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if got := retirementUpgradeSnapshot(t, predecessor); !reflect.DeepEqual(got, before) {
		t.Fatal("verified snapshot changed predecessor state")
	}
	if err := predecessor.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Upgrade(t.Context(), path, nil, UpgradeOptions{SnapshotPath: snapshot}); err != nil {
		t.Fatal(err)
	}
	current := retirementUpgradeSnapshot(t, db)
	delete(before, "workflow_backlog_alignment")
	delete(before, "workflow_design_records")
	delete(before, "workflow_proposal_records")
	for _, state := range []map[string][][]any{before, current} {
		delete(state, "schema")
		delete(state, "schema_migrations")
		delete(state, "store_revision")
	}
	if !reflect.DeepEqual(current, before) {
		t.Fatal("upgrade changed surviving tables or rows")
	}
	if got := retirementUpgradeRows(t, db, `SELECT name,tbl_name,sql FROM sqlite_schema WHERE type='trigger' ORDER BY name`); !reflect.DeepEqual(got, triggers) {
		t.Fatal("upgrade changed surviving guard triggers")
	}
	if _, err := db.Exec(`UPDATE work_items SET title=title WHERE id='work-one'`); err == nil {
		t.Fatal("upgrade allowed an unguarded work-item write")
	}
	retirementUpgradeHealthy(t, db)
	restored := filepath.Join(t.TempDir(), "restored.db")
	report, err := RestoreBackup(t.Context(), snapshot, restored)
	if err != nil {
		t.Fatal(err)
	}
	if report.RestoredDatabase.SchemaVersion != 199 {
		t.Fatalf("restored schema = %d, want 199", report.RestoredDatabase.SchemaVersion)
	}
	restoredDB, err := sql.Open(driverName, readOnlyDataSource(restored))
	if err != nil {
		t.Fatal(err)
	}
	defer restoredDB.Close()
	got := retirementUpgradeSnapshot(t, restoredDB)
	delete(got, "schema")
	delete(got, "schema_migrations")
	delete(got, "store_revision")
	if !reflect.DeepEqual(got, current) {
		t.Fatal("forward-restored state differs from upgraded state")
	}
}

func TestBacklogAlignmentEventAppendRefused(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "alignment-event")
	version := readWorkVersion(t, s, "alignment-event")
	err := applyWorkEvent(t, s, operationEvent("retired-alignment", "workflow.backlog_alignment_recorded", SubjectWorkItem, "alignment-event", map[string]any{}), nil)
	assertFailureKind(t, err, KindInvalidOperation)
	if got := readWorkVersion(t, s, "alignment-event"); got != version {
		t.Fatalf("refused event changed version: %d -> %d", version, got)
	}
}

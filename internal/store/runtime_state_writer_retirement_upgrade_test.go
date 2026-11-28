package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
)

func assertRetirementForeignKeys(t *testing.T, db *sql.DB) {
	t.Helper()
	if got := retirementUpgradeRows(t, db, `PRAGMA foreign_key_check`); len(got) != 0 {
		t.Fatalf("foreign key violations: %v", got)
	}
}

func TestRuntimeStateWriterRetirementUpgrade(t *testing.T) {
	useStampedBuild(t)
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "store.db"), 200)
	ctx := t.Context()
	retirementUpgradeExec(t, db, `INSERT INTO runtime_state_writers(surface,definition_ref,knowledge_projection_version,digest,binary_version) VALUES ('knowledge','synthetic-one',1,'digest-one','binary-one'),('knowledge','synthetic-two',2,'digest-two','binary-two')`)
	assertRetirementForeignKeys(t, db)
	if err := ensureInstallationKey(ctx, db); err != nil {
		t.Fatal(err)
	}
	var seq int
	var name, path string
	if err := db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	before := designProposalSnapshot(t, db)
	if len(before["runtime_state_writers"]) != 2 {
		t.Fatal("predecessor writer rows missing")
	}
	const writerSchema = `SELECT type,name FROM sqlite_schema WHERE tbl_name='runtime_state_writers' ORDER BY type,name`
	if got := retirementUpgradeRows(t, db, writerSchema); !reflect.DeepEqual(got, [][]any{{"index", "sqlite_autoindex_runtime_state_writers_1"}, {"table", "runtime_state_writers"}}) {
		t.Fatalf("predecessor writer schema = %v", got)
	}
	const survivingSchema = `SELECT type,name,tbl_name,sql FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' AND tbl_name NOT IN ('runtime_state_writers','workflow_diagnosis_records') ORDER BY type,name`
	schema := retirementUpgradeRows(t, db, survivingSchema)
	snapshot := filepath.Join(t.TempDir(), "predecessor.db")
	manifest, err := BackupDatabase(ctx, path, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyBackup(ctx, snapshot)
	if err != nil || verified.DBChecksum != manifest.DBChecksum || verified.SchemaVersion != 200 {
		t.Fatalf("verified predecessor: %+v, %v", verified, err)
	}
	predecessor, err := sql.Open(driverName, readOnlyDataSource(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if got := designProposalSnapshot(t, predecessor); !reflect.DeepEqual(got, before) {
		t.Fatal("verified snapshot changed predecessor state")
	}
	if err := predecessor.Close(); err != nil {
		t.Fatal(err)
	}
	if store, err := Open(t.Context(), path); err == nil {
		store.Close()
		t.Fatal("ordinary open applied or missed the breaking migration")
	} else {
		assertFailureKind(t, err, KindUpgradeRequired)
	}
	if got := designProposalSnapshot(t, db); !reflect.DeepEqual(got, before) {
		t.Fatal("refused open changed predecessor")
	}
	rollback, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	failing := migrations[2]
	failing.SQL += "\nSELECT * FROM nonexistent_retirement_failure;"
	if err := applyMigration(ctx, rollback, failing); err == nil {
		_ = rollback.Rollback()
		t.Fatal("injected post-drop failure succeeded")
	}
	if err := rollback.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := designProposalSnapshot(t, db); !reflect.DeepEqual(got, before) {
		t.Fatal("failed migration changed predecessor")
	}
	if _, err := Upgrade(ctx, path, nil); err != nil {
		t.Fatal(err)
	}
	current := designProposalSnapshot(t, db)
	if rows, ok := current["workflow_diagnosis_records"]; !ok || len(rows) != 0 {
		t.Fatalf("diagnosis records after upgrade = %v, present=%v", rows, ok)
	}
	delete(current, "workflow_diagnosis_records")
	if got := retirementUpgradeRows(t, db, writerSchema); len(got) != 0 {
		t.Fatalf("writer table or automatic index survived: %v", got)
	}
	delete(before, "runtime_state_writers")
	for _, state := range []map[string][][]any{before, current} {
		delete(state, "schema")
		delete(state, "schema_migrations")
		delete(state, "store_revision")
	}
	if !reflect.DeepEqual(current, before) {
		t.Fatal("upgrade changed surviving rows or tables")
	}
	if got := retirementUpgradeRows(t, db, survivingSchema); !reflect.DeepEqual(got, schema) {
		t.Fatal("upgrade changed surviving schema, constraints or triggers")
	}
	assertRetirementForeignKeys(t, db)
	if got := retirementUpgradeRows(t, db, `PRAGMA integrity_check`); !reflect.DeepEqual(got, [][]any{{"ok"}}) {
		t.Fatalf("integrity_check = %v", got)
	}
	restored := filepath.Join(t.TempDir(), "restored.db")
	report, err := RestoreBackup(ctx, snapshot, restored)
	if err != nil {
		t.Fatal(err)
	}
	if report.RestoredDatabase.SchemaVersion != 202 {
		t.Fatalf("restored schema = %d", report.RestoredDatabase.SchemaVersion)
	}
	restoredDB, err := sql.Open(driverName, readOnlyDataSource(restored))
	if err != nil {
		t.Fatal(err)
	}
	defer restoredDB.Close()
	got := designProposalSnapshot(t, restoredDB)
	if rows, ok := got["workflow_diagnosis_records"]; !ok || len(rows) != 0 {
		t.Fatalf("diagnosis records after restore = %v, present=%v", rows, ok)
	}
	delete(got, "workflow_diagnosis_records")
	delete(got, "schema")
	delete(got, "schema_migrations")
	delete(got, "store_revision")
	if !reflect.DeepEqual(got, current) {
		t.Fatal("forward restore differs from upgraded state")
	}
}

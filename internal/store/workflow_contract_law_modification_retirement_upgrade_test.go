package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWorkflowContractLawModificationRetirementUpgrade(t *testing.T) {
	useStampedBuild(t)
	db := captureRetirementFixture(t)
	ctx := verifiedOutcomeRetirementContext(t)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations[185:199] {
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
	retirementUpgradeExec(t, db, `INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_contracts SET law_modifies='["law:one","law:existing"]' WHERE work_id='work-one' AND contract_version=1; DELETE FROM write_guard`)
	if err := ensureInstallationKey(ctx, db); err != nil {
		t.Fatal(err)
	}
	var seq int
	var name, path string
	if err := db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	before := designProposalSnapshot(t, db)
	if len(before["workflow_contract_law_modifications"]) == 0 {
		t.Fatal("missing duplicate predecessor row")
	}
	laws := retirementUpgradeRows(t, db, `SELECT law_modifies FROM workflow_contracts WHERE work_id='work-one' AND contract_version=1`)
	if !reflect.DeepEqual(laws, [][]any{{`["law:one","law:existing"]`}}) {
		t.Fatalf("authoritative laws: %v", laws)
	}
	const survivingSchema = `SELECT type,name,tbl_name,sql FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' AND tbl_name != 'workflow_contract_law_modifications' ORDER BY type,name`
	schema := retirementUpgradeRows(t, db, survivingSchema)
	snapshot := filepath.Join(t.TempDir(), "predecessor.db")
	manifest, err := BackupDatabase(ctx, path, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyBackup(ctx, snapshot)
	if err != nil || verified.DBChecksum != manifest.DBChecksum || verified.SchemaVersion != 199 {
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
		t.Fatal("refused open changed predecessor state")
	}
	rollback, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	failing := migrations[199]
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
	if _, err := Upgrade(ctx, path, nil, UpgradeOptions{SnapshotPath: snapshot}); err != nil {
		t.Fatal(err)
	}
	current := designProposalSnapshot(t, db)
	if _, ok := current["workflow_contract_law_modifications"]; ok {
		t.Fatal("duplicate table survived")
	}
	delete(before, "workflow_contract_law_modifications")
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
	if got := retirementUpgradeRows(t, db, `SELECT law_modifies FROM workflow_contracts WHERE work_id='work-one' AND contract_version=1`); !reflect.DeepEqual(got, laws) {
		t.Fatalf("authoritative laws changed: %v", got)
	}
	if _, err := db.Exec(`UPDATE work_items SET title=title WHERE id='work-one'`); err == nil {
		t.Fatal("unguarded work-item write succeeded")
	}
	assertRetirementContractGuards(t, db)
	retirementUpgradeHealthy(t, db)
	if got := retirementUpgradeRows(t, db, `PRAGMA integrity_check`); !reflect.DeepEqual(got, [][]any{{"ok"}}) {
		t.Fatalf("integrity_check = %v", got)
	}
	restored := filepath.Join(t.TempDir(), "restored.db")
	report, err := RestoreBackup(ctx, snapshot, restored)
	if err != nil {
		t.Fatal(err)
	}
	if report.RestoredDatabase.SchemaVersion != 200 {
		t.Fatalf("restored schema = %d", report.RestoredDatabase.SchemaVersion)
	}
	restoredDB, err := sql.Open(driverName, readOnlyDataSource(restored))
	if err != nil {
		t.Fatal(err)
	}
	defer restoredDB.Close()
	got := designProposalSnapshot(t, restoredDB)
	delete(got, "schema")
	delete(got, "schema_migrations")
	delete(got, "store_revision")
	if !reflect.DeepEqual(got, current) {
		t.Fatal("forward restore differs from upgraded state")
	}
}

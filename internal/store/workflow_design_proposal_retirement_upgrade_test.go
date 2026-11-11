package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func designProposalSnapshot(t *testing.T, db *sql.DB) map[string][][]any {
	t.Helper()
	state := map[string][][]any{"schema": retirementUpgradeRows(t, db, `SELECT type,name,tbl_name,sql FROM sqlite_schema ORDER BY type,name`)}
	for _, row := range retirementUpgradeRows(t, db, `SELECT name FROM sqlite_schema WHERE type='table' ORDER BY name`) {
		name := row[0].(string)
		state[name] = retirementUpgradeRows(t, db, `SELECT * FROM "`+strings.ReplaceAll(name, `"`, `""`)+`" ORDER BY rowid`)
	}
	return state
}

func TestDesignProposalRetirementUpgrade(t *testing.T) {
	useStampedBuild(t)
	db := captureRetirementFixture(t)
	ctx := verifiedOutcomeRetirementContext(t)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations[185:198] {
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
INSERT INTO workflow_design_records(work_id,work_version,approach,decisions,touched_refs,recorded_at) VALUES('work-one',1,'Approach','[1]','[1]','time');
INSERT INTO workflow_proposal_records(work_id,work_version,problem,affected,stakes,user_outcomes,constraints,open_questions,recorded_at) VALUES('work-one',1,'Problem','[1]','Stakes','[1]','[]','[]','time');
DELETE FROM write_guard`)
	if err := ensureInstallationKey(ctx, db); err != nil {
		t.Fatal(err)
	}
	var seq int
	var name, path string
	if err := db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	before := designProposalSnapshot(t, db)
	for _, table := range []string{"workflow_design_records", "workflow_proposal_records"} {
		if len(before[table]) != 1 {
			t.Fatalf("missing predecessor row in %s", table)
		}
	}
	const survivingSchema = `SELECT type,name,tbl_name,sql FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' AND tbl_name NOT IN ('workflow_design_records','workflow_proposal_records','workflow_contract_law_modifications') ORDER BY type,name`
	schema := retirementUpgradeRows(t, db, survivingSchema)
	rollback, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	failing := migrations[198]
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
	snapshot := filepath.Join(t.TempDir(), "predecessor.db")
	manifest, err := BackupDatabase(ctx, path, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyBackup(ctx, snapshot)
	if err != nil || verified.DBChecksum != manifest.DBChecksum || verified.SchemaVersion != 198 {
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
		t.Fatal("ordinary open applied a breaking migration")
	} else {
		assertFailureKind(t, err, KindUpgradeRequired)
	}
	if got := designProposalSnapshot(t, db); !reflect.DeepEqual(got, before) {
		t.Fatal("refused open changed predecessor state")
	}
	if _, err := Upgrade(ctx, path, nil, UpgradeOptions{SnapshotPath: snapshot}); err != nil {
		t.Fatal(err)
	}
	current := designProposalSnapshot(t, db)
	assertContractLawModificationsRetired(t, before, db)
	delete(before, "workflow_design_records")
	delete(before, "workflow_proposal_records")
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
	if _, err := db.Exec(`UPDATE work_items SET title=title WHERE id='work-one'`); err == nil {
		t.Fatal("unguarded work-item write succeeded")
	}
	retirementUpgradeHealthy(t, db)
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

func TestDesignProposalEventAppendRefused(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "retired-documents")
	version := readWorkVersion(t, s, "retired-documents")
	before := designProposalSnapshot(t, s.db)
	for _, kind := range []string{"workflow.design_recorded", "workflow.proposal_recorded"} {
		err := applyWorkEvent(t, s, operationEvent("retired-document", kind, SubjectWorkItem, "retired-documents", map[string]any{}), nil)
		assertFailureKind(t, err, KindUnknownEventKind)
		if got := readWorkVersion(t, s, "retired-documents"); got != version {
			t.Fatalf("refusal changed version: %d -> %d", version, got)
		}
		if got := designProposalSnapshot(t, s.db); !reflect.DeepEqual(got, before) {
			t.Fatalf("refused %s changed store state", kind)
		}
	}
}

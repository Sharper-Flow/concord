package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWorkflowContractLawModificationRetirementUpgrade(t *testing.T) {
	useStampedBuild(t)
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "store.db"), 199)
	ctx := t.Context()
	retirementUpgradeExec(t, db, `INSERT INTO write_guard(active) VALUES(1);
INSERT INTO work_items(id,kind,title,lifecycle,version,created_at,updated_at) VALUES('work-one','task','Work','needed',1,'time','time');
INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('product','Product','prototype','operator_only',1,'time','time');
INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('project','Project',1,'time','time');
INSERT INTO product_projects(product_id,project_id,role) VALUES('product','project','primary');
INSERT INTO work_projects(work_id,project_id,role) VALUES('work-one','project','primary');
INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES('actor:' || hex(zeroblob(32)),'principal','client','agent','session','agent','time');
INSERT INTO workflow_contracts(work_id,contract_version,consequence_class,approved_at,approved_by,spec_mandate,law_modifies) VALUES('work-one',1,'internal_sqlite','time','actor:' || hex(zeroblob(32)),'[]','["law:one","law:existing"]');
INSERT INTO workflow_architecture_bindings VALUES('work-one',1,'product','domain','sha256:' || lower(hex(zeroblob(32))));
INSERT INTO workflow_contract_law_modifications VALUES('work-one',1,'law:one');
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
	if len(before["workflow_contract_law_modifications"]) == 0 {
		t.Fatal("missing duplicate predecessor row")
	}
	laws := retirementUpgradeRows(t, db, `SELECT law_modifies FROM workflow_contracts WHERE work_id='work-one' AND contract_version=1`)
	if !reflect.DeepEqual(laws, [][]any{{`["law:one","law:existing"]`}}) {
		t.Fatalf("authoritative laws: %v", laws)
	}
	const survivingSchema = `SELECT type,name,tbl_name,sql FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' AND tbl_name NOT IN ('workflow_contract_law_modifications','runtime_state_writers') ORDER BY type,name`
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
	failing := migrations[1]
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
	if _, ok := current["workflow_contract_law_modifications"]; ok {
		t.Fatal("duplicate table survived")
	}
	delete(before, "workflow_contract_law_modifications")
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
	if got := retirementUpgradeRows(t, db, `SELECT law_modifies FROM workflow_contracts WHERE work_id='work-one' AND contract_version=1`); !reflect.DeepEqual(got, laws) {
		t.Fatalf("authoritative laws changed: %v", got)
	}
	if _, err := db.Exec(`UPDATE work_items SET title=title WHERE id='work-one'`); err == nil {
		t.Fatal("unguarded work-item write succeeded")
	}
	if _, err := db.Exec(`UPDATE workflow_contracts SET law_modifies='[]' WHERE work_id='work-one'`); err == nil {
		t.Fatal("unguarded contract write succeeded")
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
	if report.RestoredDatabase.SchemaVersion != 201 {
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

package store

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func defectRetirementFixture(t *testing.T) (*sql.DB, string) {
	t.Helper()
	db, path := retirementUpgradeFixture(t)
	advanceHistoricalPrefix(t, db, migrationPrefixBefore(t, "retire_defect_intake"))
	retirementUpgradeExec(t, db, `INSERT INTO write_guard(active) VALUES(1)`)
	for _, kind := range []string{"bug", "research", "task"} {
		intent := `{"title":"Retained work","task":"Keep instructions","value_statement":"Keep value","kind":"` + kind + `","priority":1,"tags":["retained"],"governing_requirements":[],"external_ref":"ref:` + kind + `"}`
		if kind != "task" {
			intent = strings.TrimSuffix(intent, "}") + `,"defect":{"failure_shape":"checksum-mismatch","reproduction":"Compare checksums","searched":"Fixture","related_defect_ids":[],"sibling_ids":["work-bug"],"root_cause_work_id":"work-research"}}`
		}
		retirementUpgradeExec(t, db, `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,intent_json) VALUES(?,?,'Retained work','needed',1,1,?,?,?)`, "work-"+kind, kind, retirementUpgradeTime, retirementUpgradeTime, intent)
		retirementUpgradeExec(t, db, `INSERT INTO work_projects(work_id,project_id,role) VALUES(?,'project-upgrade','primary')`, "work-"+kind)
	}
	retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
	return db, path
}

func TestDefectRetirementVerifiedSnapshotUpgrade(t *testing.T) {
	useStampedBuild(t)
	db, path := defectRetirementFixture(t)
	before := retirementUpgradeSnapshot(t, db)
	if s, err := Open(t.Context(), path); err == nil {
		s.Close()
		t.Fatal("ordinary open crossed the breaking retirement")
	} else {
		assertFailureKind(t, err, KindUpgradeRequired)
	}
	if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
		t.Fatal("ordinary open changed the predecessor")
	}
	snapshot := filepath.Join(t.TempDir(), "defect-predecessor.db")
	manifest, err := BackupDatabase(t.Context(), path, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if verified, err := VerifyBackup(t.Context(), snapshot); err != nil || verified.DBChecksum != manifest.DBChecksum || verified.SchemaVersion != migrationPrefixBefore(t, "retire_defect_intake") {
		t.Fatalf("verified predecessor = %+v, %v", verified, err)
	}
	report, err := Upgrade(t.Context(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	var wantApplied []int
	for _, m := range migrations {
		if m.Version > migrationPrefixBefore(t, "retire_defect_intake") {
			wantApplied = append(wantApplied, m.Version)
		}
	}
	if !reflect.DeepEqual(report.Applied, wantApplied) {
		t.Fatalf("applied migrations = %v, want %v", report.Applied, wantApplied)
	}
	for table, want := range before {
		if table == "schema" || table == "schema_migrations" || table == "store_revision" || table == "work_items" {
			continue
		}
		if got := retirementUpgradeRows(t, db, `SELECT * FROM "`+table+`" ORDER BY rowid`); !reflect.DeepEqual(got, want) {
			t.Errorf("unrelated table %s changed", table)
		}
	}
	backup, err := sql.Open(driverName, readOnlyDataSource(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if got := retirementUpgradeSnapshot(t, backup); !reflect.DeepEqual(got, before) {
		t.Fatal("predecessor snapshot lost schema or rows")
	}
	var intentColumn int
	if err := db.QueryRow(`SELECT cid FROM pragma_table_info('work_items') WHERE name='intent_json'`).Scan(&intentColumn); err != nil {
		t.Fatal(err)
	}
	afterWork := retirementUpgradeRows(t, db, `SELECT * FROM work_items ORDER BY rowid`)
	if len(afterWork) != len(before["work_items"]) {
		t.Fatal("migration changed work inventory")
	}
	for i, row := range before["work_items"] {
		for j, value := range row {
			if j != intentColumn && !reflect.DeepEqual(value, afterWork[i][j]) {
				t.Fatalf("migration changed work row %d column %d", i, j)
			}
		}
	}
	for _, kind := range []string{"bug", "research", "task", "upgrade-from", "upgrade-to"} {
		var oldIntent, newIntent string
		if err := backup.QueryRow(`SELECT intent_json FROM work_items WHERE id=?`, "work-"+kind).Scan(&oldIntent); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT intent_json FROM work_items WHERE id=?`, "work-"+kind).Scan(&newIntent); err != nil {
			t.Fatal(err)
		}
		var oldFields, newFields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(oldIntent), &oldFields); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(newIntent), &newFields); err != nil {
			t.Fatal(err)
		}
		classified := kind == "bug" || kind == "research"
		if classified && oldFields["defect"] == nil {
			t.Fatal("snapshot did not exercise populated classification")
		}
		delete(oldFields, "defect")
		if !reflect.DeepEqual(oldFields, newFields) {
			t.Fatalf("%s intent changed beyond classification: %s", kind, newIntent)
		}
		if !classified && oldIntent != newIntent {
			t.Fatal("unclassified intent bytes changed")
		}
	}
	retirementUpgradeHealthy(t, db)
	if _, err := PlanUpgradeReadiness(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	if _, err := Upgrade(t.Context(), path, nil); err != nil {
		t.Fatalf("idempotent upgrade: %v", err)
	}
}

func TestDefectRetirementAtomicRollback(t *testing.T) {
	useStampedBuild(t)
	db, path := defectRetirementFixture(t)
	retirementUpgradeExec(t, db, `CREATE TRIGGER defect_retirement_failure BEFORE UPDATE OF intent_json ON work_items WHEN OLD.id='work-research' BEGIN SELECT RAISE(ABORT,'defect retirement rollback witness'); END`)
	before := retirementUpgradeSnapshot(t, db)
	_, err := Upgrade(t.Context(), path, nil)
	assertFailureKind(t, err, KindUnavailable)
	if !strings.Contains(err.Error(), "defect retirement rollback witness") {
		t.Fatalf("wrong failure: %v", err)
	}
	if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
		t.Fatal("failed migration changed rows, guard or manifest")
	}
	retirementUpgradeExec(t, db, `DROP TRIGGER defect_retirement_failure`)
	if _, err := Upgrade(t.Context(), path, nil); err != nil {
		t.Fatalf("retry after rollback: %v", err)
	}
}

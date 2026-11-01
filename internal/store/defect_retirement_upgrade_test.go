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
	for _, kind := range []string{"bug", "research"} {
		seedDefectRetirementWork(t, db, "collected-"+kind, kind, "completed", `{"defect":{"failure_shape":"checksum-mismatch"}}`, "project-upgrade")
	}
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

func seedDefectRetirementWork(t *testing.T, db *sql.DB, id, kind, lifecycle, intent, project string) {
	t.Helper()
	retirementUpgradeExec(t, db, `INSERT INTO write_guard(active) VALUES(1)`)
	retirementUpgradeExec(t, db, `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,intent_json) VALUES(?,?,'Terminal witness',?,1,7,?,?,?)`, id, kind, lifecycle, retirementUpgradeTime, retirementUpgradeTime, intent)
	retirementUpgradeExec(t, db, `INSERT INTO work_projects(work_id,project_id,role) VALUES(?,?,'primary')`, id, project)
	state := lifecycle
	if state == "needed" {
		state = "planned"
	} else if state == "in_progress" {
		state = "running"
	}
	retirementUpgradeExec(t, db, `INSERT INTO workflow_instances(work_id,definition_ref,definition_digest,current_step,instance_state) VALUES(?,'workflow.break_fix',?,'closure',?)`, id, "sha256:"+strings.Repeat("a", 64), state)
	retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
}

func TestDefectRetirementCollectedAbsenceReaders(t *testing.T) {
	useStampedBuild(t)
	for _, test := range []struct{ name, kind, intent string }{
		{"bug", "bug", `{"defect":{"failure_shape":"checksum-mismatch"}}`},
		{"unclassified bug", "bug", `{}`},
		{"research", "research", `{"defect":{"failure_shape":"checksum-mismatch"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, path := defectRetirementFixture(t)
			repo := initKnowledgeRepo(t)
			notePath := ".concord/docs/work/work-published.md"
			content := canonicalWorkNote("work-published")
			writeKnowledgeFile(t, repo, notePath, content)
			commit := commitKnowledgeRepo(t, repo, "published outcome")
			home := KnowledgeHome{HomeProjectID: "proj-web", HomeLocatorID: "repo-alpha-web", RepoPath: repo, HeadRef: "HEAD"}
			retirementUpgradeExec(t, db, `INSERT INTO write_guard(active) VALUES(1)`)
			retirementUpgradeExec(t, db, `INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('prod-alpha','Published witness','prototype','operator_only',1,?,?)`, retirementUpgradeTime, retirementUpgradeTime)
			retirementUpgradeExec(t, db, `INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('proj-web','Published witness',1,?,?)`, retirementUpgradeTime, retirementUpgradeTime)
			retirementUpgradeExec(t, db, `INSERT INTO product_projects VALUES('prod-alpha','proj-web','primary')`)
			retirementUpgradeExec(t, db, `INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('repo-alpha-web','proj-web','canonical_path',?,?,?,?)`, repo, repo, retirementUpgradeTime, retirementUpgradeTime)
			retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
			seedDefectRetirementWork(t, db, "work-published", test.kind, "completed", test.intent, "proj-web")
			hash := seedArchivedWorkNote(t, &Store{db: db}, home, commit, notePath, content)
			beforeArchive := retirementUpgradeRows(t, db, `SELECT * FROM archived_work ORDER BY rowid`)
			if _, err := Upgrade(t.Context(), path, nil); err != nil {
				t.Fatal(err)
			}
			s, err := Open(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			t.Run("published absence", func(t *testing.T) {
				assertPublishedAbsence(t, publishedWorkAbsenceQ(t.Context(), s.db, "work-published", "upgrade_test"), home, commit, hash)
			})
			assertPublishedExecutionAbsenceReaders(t, s, home, commit, hash)
			for _, table := range []string{"workflow_instances", "work_projects", "work_items"} {
				column := "work_id"
				if table == "work_items" {
					column = "id"
				}
				if rows := retirementUpgradeRows(t, db, `SELECT * FROM `+table+` WHERE `+column+`='work-published'`); len(rows) != 0 {
					t.Errorf("collected %s survived upgrade: %v", table, rows)
				}
			}
			if got := retirementUpgradeRows(t, db, `SELECT * FROM archived_work ORDER BY rowid`); !reflect.DeepEqual(got, beforeArchive) {
				t.Error("upgrade changed the published outcome")
			}
			retirementUpgradeHealthy(t, db)
		})
	}
}

func TestDefectRetirementPreservesRetainedExecution(t *testing.T) {
	useStampedBuild(t)
	db, path := defectRetirementFixture(t)
	classified := `{"task":"Preserve instructions","defect":{"failure_shape":"checksum-mismatch"}}`
	tests := []struct {
		id, kind, lifecycle, intent, receipt, operation string
		retained                                        bool
	}{
		{"cancelled-bug", "bug", "cancelled", classified, "", "", false},
		{"superseded-research", "research", "superseded", classified, "", "", false},
		{"other-subject-receipt", "bug", "completed", classified, "project", "", false},
		{"needed-bug", "bug", "needed", classified, "", "", true},
		{"running-research", "research", "in_progress", classified, "", "", true},
		{"receipt-bug", "bug", "completed", classified, "work_item", "", true},
		{"receipt-research", "research", "cancelled", classified, "work_item", "", true},
		{"operation-bug", "bug", "superseded", classified, "", "completed", true},
		{"operation-research", "research", "completed", classified, "", "pending", true},
		{"unclassified-research", "research", "completed", `{}`, "", "", true},
		{"empty-classification", "research", "completed", `{"defect":{"failure_shape":""}}`, "", "", true},
		{"unrelated-task", "task", "completed", classified, "", "", true},
	}
	for i, test := range tests {
		seedDefectRetirementWork(t, db, test.id, test.kind, test.lifecycle, test.intent, "project-upgrade")
		retirementUpgradeExec(t, db, `INSERT INTO write_guard(active) VALUES(1)`)
		if test.receipt != "" {
			retirementUpgradeExec(t, db, `INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version,payload_digest) SELECT ?,?,'work.updated',?,?,actor,payload_version,payload_digest FROM effect_receipts WHERE event_id='event-upgrade-4'`, "receipt-"+test.id, 100+i, test.receipt, test.id)
		}
		if test.operation != "" {
			digest := "sha256:" + strings.Repeat("d", 64)
			retirementUpgradeExec(t, db, `INSERT INTO durable_operations(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_digest,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,principal_ref,request_id,observed_at,result_kind,evidence_refs,contract_digest) VALUES(?,1,?,'workflow.break_fix',?,'closure','internal_sqlite',?,'{}','operator-upgrade',?,?,?,'[]',?)`, "operation-"+test.id, test.id, digest, digest, "request-"+test.id, retirementUpgradeTime, test.operation, digest)
		}
		retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
	}
	var intentColumn int
	if err := db.QueryRow(`SELECT cid FROM pragma_table_info('work_items') WHERE name='intent_json'`).Scan(&intentColumn); err != nil {
		t.Fatal(err)
	}
	want := make(map[string]map[string][][]any)
	for _, test := range tests {
		want[test.id] = make(map[string][][]any)
		for _, table := range []string{"workflow_instances", "work_projects", "work_items"} {
			column := "work_id"
			if table == "work_items" {
				column = "id"
			}
			rows := retirementUpgradeRows(t, db, `SELECT * FROM `+table+` WHERE `+column+`=? ORDER BY rowid`, test.id)
			if table == "work_items" {
				rows[0][intentColumn] = retirementUpgradeRows(t, db, `SELECT json_remove(intent_json,'$.defect') FROM work_items WHERE id=?`, test.id)[0][0]
			}
			if test.retained {
				want[test.id][table] = rows
			} else {
				want[test.id][table] = nil
			}
		}
	}
	before := retirementUpgradeSnapshot(t, db)
	if _, err := Upgrade(t.Context(), path, nil); err != nil {
		t.Fatal(err)
	}
	for _, test := range tests {
		t.Run(test.id, func(t *testing.T) {
			for table, expected := range want[test.id] {
				column := "work_id"
				if table == "work_items" {
					column = "id"
				}
				if got := retirementUpgradeRows(t, db, `SELECT * FROM `+table+` WHERE `+column+`=? ORDER BY rowid`, test.id); !reflect.DeepEqual(got, expected) {
					t.Errorf("%s rows = %v, want %v", table, got, expected)
				}
			}
		})
	}
	for table, expected := range before {
		switch table {
		case "schema", "schema_migrations", "store_revision", "workflow_instances", "work_projects", "work_items":
			continue
		}
		if got := retirementUpgradeRows(t, db, `SELECT * FROM "`+table+`" ORDER BY rowid`); !reflect.DeepEqual(got, expected) {
			t.Errorf("unrelated table %s changed", table)
		}
	}
	retirementUpgradeHealthy(t, db)
}

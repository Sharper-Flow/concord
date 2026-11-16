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
	ctx := t.Context()
	db, err := sql.Open(driverName, dataSourceName(filepath.Join(t.TempDir(), "baseline.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	retirementUpgradeExec(t, db, schemaBaseline198)
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1);
INSERT INTO work_items(id,kind,title,lifecycle,version,created_at,updated_at) VALUES('work-one','task','Work','needed',1,'time','time');
INSERT INTO workflow_design_records(work_id,work_version,approach,decisions,touched_refs,recorded_at) VALUES('work-one',1,'Approach','[1]','[1]','time');
INSERT INTO workflow_proposal_records(work_id,work_version,problem,affected,stakes,user_outcomes,constraints,open_questions,recorded_at) VALUES('work-one',1,'Problem','[1]','Stakes','[1]','[]','[]','time');
DELETE FROM write_guard`)
	before := designProposalSnapshot(t, db)
	for _, table := range []string{"workflow_design_records", "workflow_proposal_records"} {
		if len(before[table]) != 1 {
			t.Fatalf("missing predecessor row in %s", table)
		}
	}
	const survivingSchema = `SELECT type,name,tbl_name,sql FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' AND tbl_name NOT IN ('workflow_design_records','workflow_proposal_records') ORDER BY type,name`
	schema := retirementUpgradeRows(t, db, survivingSchema)
	if err := applyMigration(ctx, db, migrations[0]); err != nil {
		t.Fatal(err)
	}
	current := designProposalSnapshot(t, db)
	delete(before, "workflow_design_records")
	delete(before, "workflow_proposal_records")
	delete(before, "schema")
	delete(current, "schema")
	if !reflect.DeepEqual(current, before) {
		t.Fatal("migration changed surviving rows or tables")
	}
	if got := retirementUpgradeRows(t, db, survivingSchema); !reflect.DeepEqual(got, schema) {
		t.Fatal("migration changed surviving schema, constraints or triggers")
	}
	if _, err := db.Exec(`UPDATE work_items SET title=title WHERE id='work-one'`); err == nil {
		t.Fatal("unguarded work-item write succeeded")
	}
	assertRetirementForeignKeys(t, db)
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

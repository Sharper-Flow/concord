package store

import (
	"database/sql"
	"path/filepath"
	"slices"
	"testing"
)

// migrationPrefixBefore returns the number of steps before the named owner,
// as required by openMigratedTo, independently of its version or append slot.
func migrationPrefixBefore(t *testing.T, name string) int {
	t.Helper()
	for i, migration := range migrations {
		if migration.Name == name {
			return i
		}
	}
	t.Fatalf("migration %q is not registered", name)
	return 0
}

func seedSixClassMigration(t *testing.T) *sql.DB {
	t.Helper()
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "six-class.db"), migrationPrefixBefore(t, "workflow_contract_six_decision_classes"))
	execSixClassSQL(t, db, `INSERT INTO write_guard(active) VALUES(1);
INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES('six-class','task','Six-class migration','needed',0,1,'t','t');`)
	execSixClassSQL(t, db, `INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,'principal/test','client/test','agent/test','session/test','agent','t')`, DeriveWorkflowActorRef("principal/test", "client/test", "agent/test", "session/test"))
	sets := [][]string{nil, {}, {"preference"}, {"product"}, {"ui_ux"}, {"preference", "product"}, {"preference", "ui_ux"}, {"product", "ui_ux"}, {"preference", "product", "ui_ux"}}
	for index, domains := range sets {
		var raw any
		if domains != nil {
			raw = workflowJSON(domains)
		}
		execSixClassSQL(t, db, `INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,decision_domains,law_change)
VALUES('six-class',?,'Preserve every stored fact','internal_sqlite','[]','[]','t',(SELECT actor_ref FROM workflow_actors LIMIT 1),'[]',?,'{ "reason": "retained law bytes" }')`, index+1, raw)
		execSixClassSQL(t, db, `INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES('six-class',?,'predicate:preserved',0,'exists','{"kind":"exists","surface":"repository","subjects":["artifact"]}')`, index+1)
		if index > 0 {
			execSixClassSQL(t, db, `UPDATE workflow_contracts SET superseded_by=? WHERE work_id='six-class' AND contract_version=?`, index+1, index)
		}
	}
	execSixClassSQL(t, db, `DELETE FROM write_guard`)
	return db
}

func execSixClassSQL(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

// Sort columns as well as rows: replacing one constrained column can move its
// ordinal without changing a stored fact or its name.
func sixClassSnapshot(t *testing.T, db *sql.DB, tables ...string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, table := range tables {
		rows, err := db.Query(`SELECT * FROM ` + table) //nolint:gosec // fixed test-owned table names.
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values, pointers := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			row := map[string]any{}
			for i, column := range columns {
				row[column] = values[i]
			}
			out[table] = append(out[table], workflowJSON(row))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		slices.Sort(out[table])
	}
	return out
}


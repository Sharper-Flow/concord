package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func seedSixClassMigration(t *testing.T) *sql.DB {
	t.Helper()
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "six-class.db"), 156)
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

func TestSixClassMigrationPreservesContractsAndGuards(t *testing.T) {
	t.Parallel()
	db := seedSixClassMigration(t)
	tables := []string{"work_items", "workflow_actors", "workflow_contracts", "workflow_contract_predicates"}
	before := sixClassSnapshot(t, db, tables...)
	var guardBefore string
	if err := db.QueryRow(`SELECT group_concat(sql,';') FROM (SELECT sql FROM sqlite_schema WHERE name LIKE 'workflow_contracts_guard_%' ORDER BY name)`).Scan(&guardBefore); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if got := sixClassSnapshot(t, db, tables...); !reflect.DeepEqual(got, before) {
		t.Fatalf("migration changed existing rows: got %v, want %v", got, before)
	}
	var guardAfter string
	if err := db.QueryRow(`SELECT group_concat(sql,';') FROM (SELECT sql FROM sqlite_schema WHERE name LIKE 'workflow_contracts_guard_%' ORDER BY name)`).Scan(&guardAfter); err != nil || guardAfter != guardBefore {
		t.Fatalf("migration changed write guards: %v", err)
	}
	for _, query := range []string{
		`UPDATE workflow_contracts SET premise=premise`,
		`DELETE FROM workflow_contracts WHERE contract_version=9`,
		`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate) VALUES('six-class',10,'Guard witness','internal_sqlite','[]','[]','t',(SELECT actor_ref FROM workflow_actors LIMIT 1),'[]')`,
	} {
		if _, err := db.Exec(query); err == nil || !strings.Contains(err.Error(), "write-seam-only") {
			t.Fatalf("write guard lost for %s: %v", query, err)
		}
	}
	var breaking, guards, oldColumns int
	if err := db.QueryRow(`SELECT breaking,(SELECT count(*) FROM write_guard),(SELECT count(*) FROM pragma_table_info('workflow_contracts') WHERE name='decision_domains_v156') FROM schema_migrations WHERE version=157`).Scan(&breaking, &guards, &oldColumns); err != nil || breaking != 1 || guards != 0 || oldColumns != 0 {
		t.Fatalf("breaking=%d guards=%d oldColumns=%d: %v", breaking, guards, oldColumns, err)
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("migration broke a foreign key")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestSixClassMigrationCanonicalConstraint(t *testing.T) {
	t.Parallel()
	db := seedSixClassMigration(t)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	execSixClassSQL(t, db, `INSERT INTO write_guard(active) VALUES(1)`)
	for _, set := range authoredDecisionDomainSets() {
		execSixClassSQL(t, db, `UPDATE workflow_contracts SET decision_domains=? WHERE work_id='six-class' AND contract_version=9`, workflowJSON(set))
	}
	// The stored vocabulary includes preference to preserve historical decoding.
	execSixClassSQL(t, db, `UPDATE workflow_contracts SET decision_domains='["preference"]' WHERE work_id='six-class' AND contract_version=9`)
	for _, raw := range []string{`null`, `{}`, `"security"`, `[1]`, `[true]`, `[null]`, `["unknown"]`, `["legal","legal"]`, `["security","legal"]`, `[ "legal" ]`, `["\u006cegal"]`, `not-json`} {
		for _, query := range []string{
			`UPDATE workflow_contracts SET decision_domains=? WHERE work_id='six-class' AND contract_version=9`,
			`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,decision_domains) VALUES('six-class',10,'Constraint witness','internal_sqlite','[]','[]','t',(SELECT actor_ref FROM workflow_actors LIMIT 1),'[]',?)`,
		} {
			if _, err := db.Exec(query, raw); err == nil {
				t.Errorf("stored %s accepted by %s", raw, query)
			}
		}
	}
	var stored string
	if err := db.QueryRow(`SELECT decision_domains FROM workflow_contracts WHERE work_id='six-class' AND contract_version=9`).Scan(&stored); err != nil || stored != `["preference"]` {
		t.Fatalf("malformed writes changed existing fact to %s: %v", stored, err)
	}
	execSixClassSQL(t, db, `DELETE FROM write_guard`)
}

func TestSixClassMigrationRollsBackAtomically(t *testing.T) {
	t.Parallel()
	db := seedSixClassMigration(t)
	execSixClassSQL(t, db, `CREATE TRIGGER six_class_fail_restore BEFORE UPDATE ON workflow_contracts BEGIN SELECT RAISE(ABORT,'injected six-class restore failure'); END`)
	before := sixClassSnapshot(t, db, "workflow_contracts", "workflow_contract_predicates", "sqlite_schema", "schema_migrations")
	if err := Migrate(context.Background(), db); err == nil || !strings.Contains(err.Error(), "injected six-class restore failure") {
		t.Fatalf("migration did not reach injected failure: %v", err)
	}
	if got := sixClassSnapshot(t, db, "workflow_contracts", "workflow_contract_predicates", "sqlite_schema", "schema_migrations"); !reflect.DeepEqual(got, before) {
		t.Fatal("failed migration changed rows, schema, or migration manifest")
	}
	var guards int
	if err := db.QueryRow(`SELECT count(*) FROM write_guard`).Scan(&guards); err != nil || guards != 0 {
		t.Fatalf("rollback left %d guards: %v", guards, err)
	}
	execSixClassSQL(t, db, `DROP TRIGGER six_class_fail_restore`)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("retry after rollback: %v", err)
	}
}

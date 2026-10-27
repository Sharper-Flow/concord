package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var ordinaryConsentMigrationTables = []string{"workflow_overlap_resolutions", "outside_repair_dispositions", "outside_repair_reconciliations"}

// These are migration data, not forged application approvals. The prefix owns
// every table and constraint; the fixture supplies only FK-valid row values.
func ordinaryConsentMigrationFixture(t *testing.T) *sql.DB {
	t.Helper()
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "ordinary-consent.db"), 157)
	execSixClassSQL(t, db, `INSERT INTO write_guard(active) VALUES(1);
INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('ordinary-product','Product','prototype','operator_only',1,'t','t');
INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES('ordinary-from','task','From','needed',0,1,'t','t'),('ordinary-to','task','To','needed',0,1,'t','t');`)
	actor := DeriveWorkflowActorRef("principal/test", "client/test", "agent/test", "session/test")
	execSixClassSQL(t, db, `INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,'principal/test','client/test','agent/test','session/test','operator','t')`, actor)
	for _, work := range []string{"ordinary-from", "ordinary-to"} {
		execSixClassSQL(t, db, `INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate) VALUES(?,1,'Migration witness','internal_sqlite','[]','[]','t',?,'[]')`, work, actor)
	}
	for seq := 1; seq <= 6; seq++ {
		execSixClassSQL(t, db, `INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version) VALUES(?,?,'fixture','work_item','ordinary-from',?,1)`, fmt.Sprintf("ordinary-receipt-%d", seq), seq, actor)
	}
	for index, kind := range []string{ResolutionCompatibleWith, ResolutionDependsOn, ResolutionBlocks, ResolutionMergedInto, ResolutionSupersedes} {
		claim := ""
		if index >= 3 {
			claim = "approval:historical"
		}
		var invalidated any
		if index%2 == 1 {
			invalidated = 6
		}
		execSixClassSQL(t, db, `INSERT INTO workflow_overlap_resolutions VALUES(?,?,'ordinary-product','ordinary-from','ordinary-to',1,1,?,'Preserve the settlement',?,'t',?)`, "ordinary-"+kind, index+1, kind, claim, invalidated)
	}
	execSixClassSQL(t, db, `INSERT INTO outside_repair_dispositions VALUES('ordinary-from','approval:historical','active','Preserve the hold','{}','t'),('ordinary-to','approval:historical','completed','Preserve the completed hold','{ "receipt": true }','t');
INSERT INTO outside_repair_reconciliations VALUES('ordinary-reconciliation','ordinary-to','approval:historical','Preserve the reconciliation','{ "receipt": true }','t');`)
	// Establish the immutable prefix's refusal before migrating it.
	for _, query := range []string{
		`UPDATE workflow_overlap_resolutions SET approval_ref='' WHERE resolution_kind IN ('merged_into','supersedes')`,
		`UPDATE outside_repair_dispositions SET approval_ref=''`,
		`UPDATE outside_repair_reconciliations SET approval_ref=''`,
	} {
		if _, err := db.Exec(query); err == nil || !isCheckViolation(err) {
			t.Fatalf("prefix did not refuse the ordinary empty claim: %s: %v", query, err)
		}
	}
	execSixClassSQL(t, db, `DELETE FROM write_guard`)
	return db
}

func ordinaryConsentSchemaMetadata(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()
	metadata := map[string][]string{}
	for _, table := range ordinaryConsentMigrationTables {
		metadata[table+" columns"] = tableColumnInventory(t, db, table)
		metadata[table+" foreign keys"] = foreignKeyDeclarations(t, db, table)
		rows, err := db.Query(`SELECT sql FROM sqlite_schema WHERE tbl_name=? AND type IN ('index','trigger') AND sql IS NOT NULL ORDER BY name`, table)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var ddl string
			if err := rows.Scan(&ddl); err != nil {
				t.Fatal(err)
			}
			metadata[table+" indexes and guards"] = append(metadata[table+" indexes and guards"], ddl)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	return metadata
}

func TestOrdinaryConsentMigrationPreservesRowsAndConstraints(t *testing.T) {
	t.Parallel()
	db := ordinaryConsentMigrationFixture(t)
	tables := append([]string{"effect_receipts", "workflow_contracts", "work_items"}, ordinaryConsentMigrationTables...)
	before, metadata := sixClassSnapshot(t, db, tables...), ordinaryConsentSchemaMetadata(t, db)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if got := sixClassSnapshot(t, db, tables...); !reflect.DeepEqual(got, before) {
		t.Fatal("migration changed historical rows or references")
	}
	if got := ordinaryConsentSchemaMetadata(t, db); !reflect.DeepEqual(got, metadata) {
		t.Fatalf("migration changed column/FK/index/guard declarations: got %v, want %v", got, metadata)
	}
	for _, table := range ordinaryConsentMigrationTables {
		for _, query := range []string{`INSERT INTO ` + table + ` SELECT * FROM ` + table, `UPDATE ` + table + ` SET reason=reason`, `DELETE FROM ` + table} {
			if _, err := db.Exec(query); err == nil || !strings.Contains(err.Error(), "write-seam-only") {
				t.Fatalf("lost write guard: %s: %v", query, err)
			}
		}
	}
	execSixClassSQL(t, db, `INSERT INTO write_guard(active) VALUES(1)`)
	for _, table := range ordinaryConsentMigrationTables {
		execSixClassSQL(t, db, `UPDATE `+table+` SET approval_ref=''`)
	}
	for _, table := range []string{"outside_repair_dispositions", "outside_repair_reconciliations"} {
		for _, claim := range []string{"short", strings.Repeat("a", 129)} {
			if _, err := db.Exec(`UPDATE `+table+` SET approval_ref=?`, claim); err == nil || !isCheckViolation(err) {
				t.Fatalf("invalid nonempty claim accepted by %s: %v", table, err)
			}
		}
	}
	for _, query := range []string{
		`UPDATE workflow_overlap_resolutions SET resolution_kind='unknown'`,
		`UPDATE workflow_overlap_resolutions SET to_work_id=from_work_id`,
		`UPDATE workflow_overlap_resolutions SET reason=''`,
		`UPDATE outside_repair_dispositions SET state='unknown'`,
		`UPDATE outside_repair_dispositions SET reason=''`,
		`UPDATE outside_repair_reconciliations SET evidence_json='[]'`,
	} {
		if _, err := db.Exec(query); err == nil || !isCheckViolation(err) {
			t.Fatalf("lost non-consent constraint: %s: %v", query, err)
		}
	}
	for _, query := range []string{
		`UPDATE workflow_overlap_resolutions SET event_seq=99 WHERE resolution_id='ordinary-compatible_with'`,
		`UPDATE workflow_overlap_resolutions SET invalidated_seq=99`,
		`UPDATE workflow_overlap_resolutions SET from_contract_version=99`,
		`UPDATE outside_repair_dispositions SET work_id='unknown-work' WHERE work_id='ordinary-from'`,
		`UPDATE outside_repair_reconciliations SET work_id='unknown-work'`,
	} {
		if _, err := db.Exec(query); err == nil || !strings.Contains(strings.ToLower(err.Error()), "foreign key") {
			t.Fatalf("lost foreign key: %s: %v", query, err)
		}
	}
	execSixClassSQL(t, db, `DELETE FROM write_guard`)
	var breaking, guards, leftovers int
	if err := db.QueryRow(`SELECT breaking,(SELECT count(*) FROM write_guard),(SELECT count(*) FROM sqlite_schema WHERE name LIKE '%_v158') FROM schema_migrations WHERE version=158`).Scan(&breaking, &guards, &leftovers); err != nil || breaking != 1 || guards != 0 || leftovers != 0 {
		t.Fatalf("breaking=%d guards=%d leftovers=%d: %v", breaking, guards, leftovers, err)
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("migration left an FK violation")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestOrdinaryConsentMigrationRollsBackAtomically(t *testing.T) {
	t.Parallel()
	db := ordinaryConsentMigrationFixture(t)
	// A collision at the third rebuild fails after the earlier two tables have
	// changed. The migration transaction must restore both their data and DDL.
	execSixClassSQL(t, db, `CREATE TABLE outside_repair_reconciliations_v158(collision TEXT)`)
	tables := append([]string{"sqlite_schema", "schema_migrations", "write_guard"}, ordinaryConsentMigrationTables...)
	before := sixClassSnapshot(t, db, tables...)
	if err := Migrate(context.Background(), db); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("migration did not reach the injected late collision: %v", err)
	}
	if got := sixClassSnapshot(t, db, tables...); !reflect.DeepEqual(got, before) {
		t.Fatal("failed migration changed rows, schema, guards or manifest")
	}
	execSixClassSQL(t, db, `DROP TABLE outside_repair_reconciliations_v158`)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("retry after rollback: %v", err)
	}
	before = sixClassSnapshot(t, db, tables...)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if got := sixClassSnapshot(t, db, tables...); !reflect.DeepEqual(got, before) {
		t.Fatal("second migration pass changed state")
	}
}

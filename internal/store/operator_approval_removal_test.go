package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
)

func TestOperatorApprovalStorageAbsent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	assertOperatorApprovalStorageAbsent(t, s.db)
}

func assertOperatorApprovalStorageAbsent(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range []string{"agent_approvals", "agent_approval_challenges"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE tbl_name=?`, table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count=%d err=%v, want absent", table, count, err)
		}
	}
	for _, table := range []string{"workflow_overlap_resolutions", "outside_repair_dispositions", "outside_repair_reconciliations"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info(?) WHERE name='approval_ref'`, table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s.approval_ref count=%d err=%v, want absent", table, count, err)
		}
	}
	for _, table := range []string{"agent_clients", "agent_client_keys", "agent_nonce_replay"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type='table' AND name=?`, table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s count=%d err=%v, want retained", table, count, err)
		}
	}
}

func operatorApprovalRemovalFixture(t *testing.T) *sql.DB {
	t.Helper()
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "approval-removal.db"), migrationPrefixBefore(t, "remove_operator_approval_system"))
	execSixClassSQL(t, db, `INSERT INTO write_guard(active) VALUES(1);
INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('removal-product','Product','prototype','operator_only',1,'t','t');
INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES('removal-from','task','From','needed',0,1,'t','t'),('removal-to','task','To','needed',0,1,'t','t');
INSERT INTO agent_clients(client_ref,status,principal_ref,capabilities_json,product_scope_json,project_scope_json,agent_scope_json,created_at) VALUES('removal-client','active','principal/test','["product_read"]','["removal-product"]','[]','["agent/test"]','t');
INSERT INTO agent_client_keys(client_ref,key_id,public_key,status,created_at) VALUES('removal-client','key/test',zeroblob(32),'active','t');
INSERT INTO agent_nonce_replay VALUES('removal-client','nonce-already-used','2026-01-01','2027-01-01');`)
	actor := DeriveWorkflowActorRef("principal/test", "removal-client", "agent/test", "session/test")
	execSixClassSQL(t, db, `INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,'principal/test','removal-client','agent/test','session/test','agent','t')`, actor)
	for _, work := range []string{"removal-from", "removal-to"} {
		execSixClassSQL(t, db, `INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate) VALUES(?,1,'Preserve the contract','internal_sqlite','[]','[]','t',?,'[]')`, work, actor)
	}
	for seq := 1; seq <= 6; seq++ {
		execSixClassSQL(t, db, `INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version) VALUES(?,?,'fixture','work_item','removal-from',?,1)`, fmt.Sprintf("removal-receipt-%d", seq), seq, actor)
	}
	for index, kind := range []string{ResolutionDependsOn, ResolutionBlocks, ResolutionMergedInto, ResolutionSupersedes} {
		var invalidated any
		if index%2 == 1 {
			invalidated = 6
		}
		execSixClassSQL(t, db, `INSERT INTO workflow_overlap_resolutions VALUES(?,?,'removal-product','removal-from','removal-to',1,1,?,'Preserve the settlement','approval:historical','t',?)`, "removal-"+kind, index+1, kind, invalidated)
	}
	execSixClassSQL(t, db, `INSERT INTO outside_repair_dispositions VALUES('removal-from','approval:historical','active','Preserve the hold','{}','t'),('removal-to','approval:historical','completed','Preserve the completed hold','{ "receipt": true }','t');
INSERT INTO outside_repair_reconciliations VALUES('removal-reconciliation','removal-to','approval:historical','Preserve the reconciliation','{ "receipt": true }','t');
DELETE FROM write_guard;`)
	for index, status := range []string{"active", "consumed", "revoked"} {
		ref := fmt.Sprintf("%064d", index)
		execSixClassSQL(t, db, `INSERT INTO agent_approvals(approval_ref,operation_digest,scope_json,version_json,consequence,human_principal_ref,client_ref,session_ref,issued_at,expires_at,max_uses,used_count,protected_evidence_ref,protected_evidence_digest) VALUES(?,'operation:test','{}','{}','lifecycle','principal:operator','removal-client','session/test','2026-01-01','2027-01-01',1,?,'evidence:test','digest:test')`, ref, index%2)
		execSixClassSQL(t, db, `INSERT INTO agent_approval_challenges(challenge_ref,client_ref,principal_ref,session_ref,agent_ref,directory,worktree,product_scope_json,operation_digest,scope_json,version_json,consequence,host_assertion_digest,issued_at,expires_at,status) VALUES(?,'removal-client','principal/test','session/test','agent/test','/synthetic/repository','/synthetic/repository','["removal-product"]','operation:test','{}','{}','lifecycle','host:test','2026-01-01','2027-01-01',?)`, ref, status)
	}
	return db
}

func TestOperatorApprovalRemovalPreservesPopulatedCoordinationAndAuthentication(t *testing.T) {
	t.Parallel()
	db := operatorApprovalRemovalFixture(t)
	tables := []string{"workflow_overlap_resolutions", "outside_repair_dispositions", "outside_repair_reconciliations", "effect_receipts", "workflow_contracts", "work_items", "products", "workflow_actors", "agent_clients", "agent_client_keys", "agent_nonce_replay"}
	before := sixClassSnapshot(t, db, tables...)
	for _, table := range tables[:3] {
		for index, raw := range before[table] {
			var fields map[string]any
			if err := json.Unmarshal([]byte(raw), &fields); err != nil {
				t.Fatal(err)
			}
			delete(fields, "approval_ref")
			before[table][index] = workflowJSON(fields)
		}
		slices.Sort(before[table])
	}
	metadata := ordinaryConsentSchemaMetadata(t, db)
	for _, table := range tables[:3] {
		metadata[table+" columns"] = slices.DeleteFunc(metadata[table+" columns"], func(column string) bool { return column == "approval_ref" })
	}
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	assertOperatorApprovalStorageAbsent(t, db)
	if after := sixClassSnapshot(t, db, tables...); !reflect.DeepEqual(after, before) {
		t.Fatalf("migration changed retained data: got %v, want %v", after, before)
	}
	if after := ordinaryConsentSchemaMetadata(t, db); !reflect.DeepEqual(after, metadata) {
		t.Fatalf("migration changed retained schema: got %v, want %v", after, metadata)
	}
	for _, table := range tables[:3] {
		for _, query := range []string{`INSERT INTO ` + table + ` SELECT * FROM ` + table, `UPDATE ` + table + ` SET reason=reason`, `DELETE FROM ` + table} {
			if _, err := db.Exec(query); err == nil || !strings.Contains(err.Error(), "write-seam-only") {
				t.Fatalf("lost write guard: %s: %v", query, err)
			}
		}
	}
	execSixClassSQL(t, db, `INSERT INTO write_guard(active) VALUES(1)`)
	for _, query := range []string{
		`UPDATE workflow_overlap_resolutions SET resolution_kind='unknown'`,
		`UPDATE workflow_overlap_resolutions SET to_work_id=from_work_id`,
		`UPDATE workflow_overlap_resolutions SET from_contract_version=0`,
		`UPDATE workflow_overlap_resolutions SET reason=''`,
		`UPDATE outside_repair_dispositions SET state='unknown'`,
		`UPDATE outside_repair_dispositions SET reason=''`,
		`UPDATE outside_repair_reconciliations SET reconciliation_id='x'`,
		`UPDATE outside_repair_reconciliations SET evidence_json='[]'`,
	} {
		if _, err := db.Exec(query); !sqlitedb.IsCheckViolation(err) {
			t.Fatalf("lost CHECK constraint: %s: %v", query, err)
		}
	}
	for _, query := range []string{
		`INSERT INTO workflow_overlap_resolutions SELECT * FROM workflow_overlap_resolutions`,
		`INSERT INTO agent_nonce_replay SELECT * FROM agent_nonce_replay`,
		`INSERT INTO agent_client_keys(client_ref,key_id,public_key,status,created_at) VALUES('removal-client','key/second',CAST('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' AS BLOB),'active','t')`,
	} {
		if _, err := db.Exec(query); !sqlitedb.IsIdentityConflict(err) {
			t.Fatalf("lost UNIQUE constraint: %s: %v", query, err)
		}
	}
	for _, query := range []string{
		`UPDATE workflow_overlap_resolutions SET event_seq=99 WHERE resolution_id='removal-depends_on'`,
		`UPDATE workflow_overlap_resolutions SET invalidated_seq=99`,
		`UPDATE workflow_overlap_resolutions SET from_contract_version=99`,
		`UPDATE workflow_overlap_resolutions SET product_id='unknown-product'`,
		`UPDATE outside_repair_dispositions SET work_id='unknown-work' WHERE work_id='removal-from'`,
		`UPDATE outside_repair_reconciliations SET work_id='unknown-work'`,
	} {
		if _, err := db.Exec(query); !sqlitedb.IsForeignKeyViolation(err) {
			t.Fatalf("lost foreign key: %s: %v", query, err)
		}
	}
	execSixClassSQL(t, db, `DELETE FROM write_guard`)
	var breaking, guards, leftovers int
	if err := db.QueryRow(`SELECT breaking,(SELECT count(*) FROM write_guard),(SELECT count(*) FROM sqlite_schema WHERE name LIKE '%_v168') FROM schema_migrations WHERE name='remove_operator_approval_system'`).Scan(&breaking, &guards, &leftovers); err != nil || breaking != 1 || guards != 0 || leftovers != 0 {
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

func TestOperatorApprovalRemovalRollsBackAtomically(t *testing.T) {
	t.Parallel()
	db := operatorApprovalRemovalFixture(t)
	execSixClassSQL(t, db, `CREATE TABLE outside_repair_reconciliations_v168(collision TEXT)`)
	tables := []string{"sqlite_schema", "schema_migrations", "write_guard", "agent_approvals", "agent_approval_challenges", "workflow_overlap_resolutions", "outside_repair_dispositions", "outside_repair_reconciliations", "agent_clients", "agent_client_keys", "agent_nonce_replay"}
	before := sixClassSnapshot(t, db, tables...)
	if err := Migrate(context.Background(), db); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("migration did not reach the injected late collision: %v", err)
	}
	if after := sixClassSnapshot(t, db, tables...); !reflect.DeepEqual(after, before) {
		t.Fatal("failed migration changed data, schema, guards or manifest")
	}
	execSixClassSQL(t, db, `DROP TABLE outside_repair_reconciliations_v168`)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("retry after rollback: %v", err)
	}
	assertOperatorApprovalStorageAbsent(t, db)
	before = sixClassSnapshot(t, db, tables[:3]...)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if after := sixClassSnapshot(t, db, tables[:3]...); !reflect.DeepEqual(after, before) {
		t.Fatal("second migration pass changed state")
	}
}

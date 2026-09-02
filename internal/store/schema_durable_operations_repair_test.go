package store

import (
	"context"
	"database/sql"
	"testing"
)

// tableColumns names every column a table holds.
func tableColumns(t *testing.T, db *sql.DB, table string) map[string]bool {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatalf("read columns of %s: %v", table, err)
	}
	defer rows.Close()
	names := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan column of %s: %v", table, err)
		}
		names[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate columns of %s: %v", table, err)
	}
	return names
}

// Migration 7 was edited in place to add durable_operations.contract_digest,
// and migration 16, which had added the column it replaced, was reduced to
// SELECT 1. A database created before that edit therefore never gains the
// column: it reports the current schema version, passes the manifest check
// through the shipped-variant entry for migration 7, and fails every insert
// into durable_operations with "no such column".
func TestMigrationRepairsDurableOperationsContractDigest(t *testing.T) {
	db := openMigrated(t)
	ctx := context.Background()

	// Reproduce a database built from migration 7's earlier text: the table
	// exists in its pre-edit shape and the repair has not run.
	for _, statement := range []string{
		`DROP TABLE durable_operations`,
		`CREATE TABLE durable_operations (
    op_id TEXT NOT NULL,
    attempt_epoch INTEGER NOT NULL,
    work_id TEXT NOT NULL,
    workflow_type_ref TEXT NOT NULL,
    workflow_type_version INTEGER NOT NULL,
    step_id TEXT NOT NULL,
    step_kind TEXT NOT NULL CHECK(step_kind IN ('internal_sqlite','cross_authority','external_effect')),
    accepted_inputs_digest TEXT NOT NULL,
    accepted_scope_snapshot TEXT NOT NULL,
    result_kind TEXT CHECK(result_kind IS NULL OR result_kind IN ('completed','pending','partial','failed','failed_stale')),
    result_payload TEXT,
    evidence_refs TEXT NOT NULL DEFAULT '[]',
    changed_refs TEXT NOT NULL DEFAULT '[]',
    resume_cursor TEXT,
    principal_ref TEXT NOT NULL,
    request_id TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    completed_at TEXT,
    PRIMARY KEY(op_id, attempt_epoch)
)`,
		`DELETE FROM schema_migrations WHERE version = 67`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("stage earlier history (%s): %v", statement, err)
		}
	}

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("repair migrate: %v", err)
	}
	if !tableColumns(t, db, "durable_operations")["contract_digest"] {
		t.Fatal("repair left durable_operations without contract_digest")
	}
}

// The repair must not disturb a database that already holds the column, and it
// must not discard the digests such a database recorded.
func TestDurableOperationsRepairPreservesRecordedDigests(t *testing.T) {
	db := openMigrated(t)
	ctx := context.Background()

	if !tableColumns(t, db, "durable_operations")["contract_digest"] {
		t.Fatal("a fresh database is missing durable_operations.contract_digest")
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO durable_operations(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_version,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,principal_ref,request_id,observed_at,contract_digest) VALUES('op-1',1,'work-1','workflow.implementation',1,'proposal','internal_sqlite','sha256:a','{}','principal','request','2026-01-01T00:00:00Z','sha256:kept')`); err != nil {
		t.Fatalf("seed durable operation: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = 67`); err != nil {
		t.Fatalf("clear repair marker: %v", err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("repair migrate: %v", err)
	}

	var digest string
	if err := db.QueryRowContext(ctx, `SELECT contract_digest FROM durable_operations WHERE op_id='op-1'`).Scan(&digest); err != nil {
		t.Fatalf("read digest after repair: %v", err)
	}
	if digest != "sha256:kept" {
		t.Fatalf("repair rewrote a recorded digest: got %q", digest)
	}
}

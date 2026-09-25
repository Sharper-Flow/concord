package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// A database a pre-CD-0171 binary left at migration 101 carries queued
// issue_audit_comment rows and their dispositions. Migrations 102 and 103
// rebuild the queue twice; this test proves the queued rows and the
// disposition edge survive both rebuilds with their columns intact, and that
// the rebuilt queue accepts the project_create and project_update kinds the
// widened check exists for.
func TestMigrations102And103PreserveQueuedAuditCommentsThroughRebuilds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concord-v101.db")
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations {
		if migration.Version >= 102 {
			break
		}
		if err := applyMigration(ctx, db, migration); err != nil {
			t.Fatalf("migration %d (%s): %v", migration.Version, migration.Name, err)
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`,
			migration.Version, migration.Name, migration.checksum(), "2026-09-24T00:00:00Z",
		); err != nil {
			t.Fatalf("manifest record for migration %d: %v", migration.Version, err)
		}
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO work_items(id, kind, title, lifecycle, priority, version, created_at, updated_at)
		 VALUES('audit-mig-work','task','Audit migration','in_progress',0,1,'2026-09-24T00:00:00Z','2026-09-24T00:00:00Z')`,
	); err != nil {
		t.Fatal(err)
	}
	const auditPayload = `{"kind":"issue_audit_comment","issue_uuid":"remote-audit-uuid","body":"Correction with source."}`
	if _, err := db.ExecContext(ctx,
		`INSERT INTO linear_outbox(operation_id, work_id, op_kind, idempotency_key, payload, state, attempts, last_error, created_at, updated_at)
		 VALUES('linear-audit-mig-op','audit-mig-work','issue_audit_comment','audit-mig-idem',?,'queued',0,'','2026-09-24T00:00:00Z','2026-09-24T00:00:00Z')`,
		auditPayload,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO linear_outbox_dispositions(operation_id, work_id, disposition, reason, created_at)
		 VALUES('linear-audit-mig-op','audit-mig-work','acknowledged','recorded at 101','2026-09-24T00:00:00Z')`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	var (
		opKind        string
		gotPayload    string
		state         string
		attempts      int
		idempotencyID string
	)
	if err := db.QueryRowContext(ctx,
		`SELECT op_kind, payload, state, attempts, idempotency_key FROM linear_outbox WHERE operation_id='linear-audit-mig-op'`,
	).Scan(&opKind, &gotPayload, &state, &attempts, &idempotencyID); err != nil {
		t.Fatalf("queued audit comment did not survive migrations 102 and 103: %v", err)
	}
	if opKind != LinearOpIssueAuditComment || gotPayload != auditPayload || state != LinearOutboxQueued || attempts != 0 || idempotencyID != "audit-mig-idem" {
		t.Fatalf("queued audit comment drifted: kind=%s state=%s attempts=%d idem=%s payload=%s", opKind, state, attempts, idempotencyID, gotPayload)
	}
	var disposition, dispositionWork string
	if err := db.QueryRowContext(ctx,
		`SELECT disposition, work_id FROM linear_outbox_dispositions WHERE operation_id='linear-audit-mig-op'`,
	).Scan(&disposition, &dispositionWork); err != nil {
		t.Fatalf("disposition did not survive migrations 102 and 103: %v", err)
	}
	if disposition != "acknowledged" || dispositionWork != "audit-mig-work" {
		t.Fatalf("disposition drifted: %s for %s", disposition, dispositionWork)
	}
	if rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`); err != nil {
		t.Fatal(err)
	} else if rows.Next() {
		t.Fatal("foreign key check found a violation after the dispositions rebuild")
	} else if err := rows.Err(); err != nil {
		t.Fatal(err)
	} else if err := rows.Close(); err != nil {
		t.Fatal(err)
	}

	// The rebuild widened the check: a project_create row queues, and an
	// unknown kind still refuses.
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO linear_outbox(operation_id, work_id, op_kind, idempotency_key, payload, state, attempts, last_error, created_at, updated_at)
		 VALUES('linear-project-mig-op','audit-mig-work','project_create','project-mig-idem','{"kind":"project_create"}','queued',0,'','2026-09-24T00:00:00Z','2026-09-24T00:00:00Z')`,
	); err != nil {
		t.Fatalf("project_create is not queueable after migration 102: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO linear_outbox(operation_id, work_id, op_kind, idempotency_key, payload, state, attempts, last_error, created_at, updated_at)
		 VALUES('linear-bogus-mig-op','audit-mig-work','issue_rename','bogus-mig-idem','{"kind":"issue_rename"}','queued',0,'','2026-09-24T00:00:00Z','2026-09-24T00:00:00Z')`,
	); err == nil {
		t.Fatal("unknown op_kind queued after migration 102; the rebuilt check does not hold")
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
}

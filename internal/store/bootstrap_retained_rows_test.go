package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestMigration59PreservesPopulatedBootstrapOperation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sql.Open(driverName, dataSourceName(filepath.Join(t.TempDir(), "concord-v58.db")))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations {
		if m.Version >= 59 {
			continue
		}
		if err := applyMigration(ctx, db, m); err != nil {
			t.Fatalf("migration %d: %v", m.Version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, m.Version, m.Name, m.checksum(), "2026-08-30T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	for _, seed := range []string{
		`INSERT INTO agent_installation_keys(key_name,key_bytes,created_at) VALUES('cursor',zeroblob(32),'2026-08-30T00:00:00Z')`,
		`INSERT INTO fold_guard(active) VALUES(1)`,
		`INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('product-bootstrap','Bootstrap','prototype','operator_only',1,'now','now')`,
		`INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('project-bootstrap','Bootstrap',1,'now','now')`,
		`INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES('work-bootstrap','task','Bootstrap','needed',1,2,'now','now')`,
		`DELETE FROM fold_guard`,
		`INSERT INTO bootstrap_operations(idempotency_key,operation_id,request_digest,request_json,product_id,project_id,work_id,repo_path,expected_version,state,created_at,updated_at) VALUES('fixture-key','fixture-operation','sha256:1111111111111111111111111111111111111111111111111111111111111111','{}','product-bootstrap','project-bootstrap','work-bootstrap','/synthetic/repo',2,'completed','now','now')`,
	} {
		if _, err := db.ExecContext(ctx, seed); err != nil {
			t.Fatalf("seed v58 fixture rows: %v\n%s", err, seed)
		}
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var id, state string
	if err := db.QueryRowContext(ctx, `SELECT operation_id,state FROM bootstrap_operations WHERE work_id='work-bootstrap'`).Scan(&id, &state); err != nil {
		t.Fatal(err)
	}
	if id != "fixture-operation" || state != "completed" {
		t.Fatalf("operation=%s state=%s", id, state)
	}
}

func TestClaimNativeFenceMigration154(t *testing.T) {
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "native-fence.db"), 153)
	assertFailureKind(t, migrateAtOpen(t.Context(), db), KindUpgradeRequired)
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	var floor int
	if err := db.QueryRow(`SELECT max(version) FROM schema_migrations WHERE breaking=1 AND version<=154`).Scan(&floor); err != nil || floor != 154 {
		t.Fatalf("native fence compatibility floor=%d error=%v", floor, err)
	}
	for _, query := range []string{
		`INSERT INTO worktree_claim_native_fences VALUES('other','','/synthetic/path','wts:fixture',1)`,
		`INSERT INTO worktree_claim_native_fences VALUES('path','/synthetic/repo','/synthetic/path','wts:fixture',1)`,
		`INSERT INTO worktree_claim_native_fences VALUES('branch','','work/fixture','wts:fixture',1)`,
		`INSERT INTO worktree_claim_native_fences VALUES('path','','/synthetic/path','wts:fixture',0)`,
	} {
		if _, err := db.Exec(query); err == nil {
			t.Fatalf("migration accepted an invalid fence identity/generation: %s", query)
		}
	}
	if _, err := db.Exec(`INSERT INTO worktree_claim_native_fences VALUES('path','','/synthetic/path','wts:fixture',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO worktree_claim_native_fences VALUES('path','','/synthetic/path','wts:other',2)`); err == nil {
		t.Fatal("migration admitted two generation owners for one native path")
	}
}

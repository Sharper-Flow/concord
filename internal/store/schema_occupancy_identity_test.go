package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// The occupancy worktree_id is the composite set:project:claim identity. Real
// claim_op_id values embed a sha256 hex digest plus ":worktree-claim:<project>"
// (internal/agent/mutations.go worktree_claim), so the composite reaches 146+
// characters on a real store. The table must accept that shape or every open
// of a store with legacy occupants fails migration 104 (CON-482).
func TestMigrateV103ToV104AcceptsRealClaimIdentityLength(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "concord-v103.db")
	ctx := context.Background()
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:103] {
		if err := applyMigration(ctx, db, migration); err != nil {
			t.Fatalf("migration %d (%s): %v", migration.Version, migration.Name, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.Version, migration.Name, migration.checksum(), "2026-09-25T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	// The exact identity shape the worktree_claim mutation writes on a
	// Product named pokeedge-web: 33-char set id + project id + a claim op id
	// of 99 characters, joined with ':'. The composite is 146 characters.
	longClaim := "sha256:" + strings.Repeat("2e4de7b16b09c5c28f7ccfe4deb90e7eeede103ed5408ae0c12e7ad09f6f8e8e", 1) + ":worktree-claim:pokeedge-web"
	if len(longClaim) != 99 {
		t.Fatalf("fixture claim_op_id length = %d, want 99", len(longClaim))
	}
	setID := "wts:work-6f765c1d0816fefd14b1a2fd"
	composite := setID + ":pokeedge-web:" + longClaim
	if len(composite) != 146 {
		t.Fatalf("fixture composite length = %d, want 146", len(composite))
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,reclaimed_at,git_facts,occupant_session_ref) VALUES
		(?, 'pokeedge-web', ?, 'work/x','b1','/wt/x','/repo','active','2026-09-25T10:00:00Z',NULL,'{}','ses-legacy')`,
		setID, longClaim); err != nil {
		t.Fatalf("seed v103 worktree entry: %v", err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migration 104 must accept the real composite length: %v", err)
	}
	var worktreeID string
	if err := db.QueryRowContext(ctx, `SELECT worktree_id FROM worktree_occupancy WHERE session_ref='ses-legacy'`).Scan(&worktreeID); err != nil {
		t.Fatalf("legacy row missing after migration 104: %v", err)
	}
	if worktreeID != composite {
		t.Fatalf("migrated worktree_id = %q, want the exact composite %q", worktreeID, composite)
	}
}

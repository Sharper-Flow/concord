package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestOpenAppliesSchemaManifest(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	var applied int
	if err := s.DB().QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if applied != len(migrations) {
		t.Fatalf("applied migrations = %d, want %d", applied, len(migrations))
	}

	got, err := SchemaVersion(ctx, s.DB())
	if err != nil {
		t.Fatalf("SchemaVersion() error = %v", err)
	}
	if want := migrations[len(migrations)-1].Version; got != want {
		t.Errorf("SchemaVersion() = %d, want %d", got, want)
	}
}

func TestMigrateV8ToV9AddsAgentAuthorityWithoutChangingPriorMigrations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concord-v8.db")
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
	for _, migration := range migrations[:8] {
		if _, err := db.ExecContext(ctx, migration.SQL); err != nil {
			t.Fatalf("migration %d: %v", migration.Version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.Version, migration.Name, migration.checksum(), "2026-08-08T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"agent_clients", "agent_client_keys", "agent_nonce_replay", "agent_grants", "agent_approval_challenges", "agent_approvals", "idempotency_records"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Errorf("table %s missing after v8->v9 migration", table)
		}
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != CurrentSchemaVersion() {
		t.Fatalf("schema version = %d, want %d", version, CurrentSchemaVersion())
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	before, err := SchemaVersion(ctx, s.DB())
	if err != nil {
		t.Fatalf("SchemaVersion() error = %v", err)
	}
	if err := Migrate(ctx, s.DB()); err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}
	after, err := SchemaVersion(ctx, s.DB())
	if err != nil {
		t.Fatalf("SchemaVersion() error = %v", err)
	}
	if before != after {
		t.Errorf("schema version moved on re-migration: %d -> %d", before, after)
	}

	var applied int
	if err := s.DB().QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if applied != len(migrations) {
		t.Errorf("applied migrations = %d, want %d", applied, len(migrations))
	}
}

func TestMigrateV7ToV8PreservesValidMultiParentRelations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concord.db")
	ctx := context.Background()
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:7] {
		if _, err := db.ExecContext(ctx, migration.SQL); err != nil {
			t.Fatalf("migration %d: %v", migration.Version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.Version, migration.Name, migration.checksum(), "2026-08-07T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1); INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('p','P','prototype','operator_only',1,'now','now'); INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('pr','PR',1,'now','now'); INSERT INTO product_projects(product_id,project_id,role) VALUES('p','pr','primary'); INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES('parent','task','Parent','needed',1,1,'now','now'),('child-a','task','A','needed',1,1,'now','now'),('child-b','task','B','needed',1,1,'now','now'); INSERT INTO work_projects(work_id,project_id,role) VALUES('parent','pr','primary'),('child-a','pr','primary'),('child-b','pr','primary'); INSERT INTO relations(work_id_from,work_id_to,kind,created_at) VALUES('parent','child-a','parent','now'),('parent','child-b','parent','now'); DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var count int
	if err := s.DB().QueryRowContext(ctx, `SELECT count(*) FROM relations WHERE kind='parent' AND work_id_from='parent'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("multi-parent relation count = %d, want 2", count)
	}
}

func TestMigrateLeavesPopulatedVersion3DatabaseUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concord.db")
	ctx := context.Background()
	db := seedVersion3Database(t, path)
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard (active) VALUES (1)`); err != nil {
		t.Fatalf("enable fold guard: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO products (id, display_name, stage_maturity, stage_audience_commitment, version, created_at, updated_at) VALUES ('product-1', 'Concord', 'prototype', 'operator_only', 1, '2026-08-07T12:00:00Z', '2026-08-07T12:00:00Z')`); err != nil {
		t.Fatalf("insert v3 Product fixture: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard WHERE active = 1`); err != nil {
		t.Fatalf("disable fold guard: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("seed database Close() error = %v", err)
	}

	_, err := Open(ctx, path)
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("Open() error = %v, want *Failure", err)
	}
	if failure.Kind != KindMembershipMigrationRequired {
		t.Fatalf("Open() failure kind = %q, want %q", failure.Kind, KindMembershipMigrationRequired)
	}
	if failure.RetrySafe {
		t.Fatal("membership migration failure is retry-safe; want explicit recovery")
	}
	if !strings.Contains(failure.RecoveryAction, "stable IDs") || !strings.Contains(failure.RecoveryAction, "v3 binary") {
		t.Fatalf("RecoveryAction = %q, want explicit stable-ID or v3 recovery", failure.RecoveryAction)
	}

	check, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatalf("reopen v3 database: %v", err)
	}
	check.SetMaxOpenConns(1)
	defer func() { _ = check.Close() }()
	if got, err := SchemaVersion(ctx, check); err != nil || got != 3 {
		t.Fatalf("SchemaVersion() = %d, error = %v, want exactly 3", got, err)
	}
	var membershipTables int
	if err := check.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name IN ('product_projects', 'work_projects')`).Scan(&membershipTables); err != nil {
		t.Fatalf("check membership tables: %v", err)
	}
	if membershipTables != 0 {
		t.Fatalf("membership tables = %d, want none", membershipTables)
	}
	var id, name string
	if err := check.QueryRowContext(ctx, `SELECT id, display_name FROM products`).Scan(&id, &name); err != nil {
		t.Fatalf("read original Product fixture: %v", err)
	}
	if id != "product-1" || name != "Concord" {
		t.Fatalf("original Product fixture = %q/%q, want product-1/Concord", id, name)
	}
}

func TestMigrateEmptyVersion3DatabaseToVersion4(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concord.db")
	ctx := context.Background()
	db := seedVersion3Database(t, path)
	if err := db.Close(); err != nil {
		t.Fatalf("seed database Close() error = %v", err)
	}

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() empty v3 database error = %v", err)
	}
	defer func() { _ = s.Close() }()
	got, err := SchemaVersion(ctx, s.DB())
	if err != nil {
		t.Fatalf("SchemaVersion() error = %v", err)
	}
	if got != CurrentSchemaVersion() {
		t.Fatalf("SchemaVersion() = %d, want %d", got, CurrentSchemaVersion())
	}
}

func seedVersion3Database(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		_ = db.Close()
		t.Fatalf("begin v3 seed transaction: %v", err)
	}
	rollback := func(err error) *sql.DB {
		_ = tx.Rollback()
		_ = db.Close()
		t.Fatalf("seed v3 database: %v", err)
		return nil
	}
	if _, err := tx.ExecContext(context.Background(), schemaManifestDDL); err != nil {
		return rollback(err)
	}
	for _, m := range migrations[:3] {
		if _, err := tx.ExecContext(context.Background(), m.SQL); err != nil {
			return rollback(err)
		}
		if _, err := tx.ExecContext(context.Background(), `INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, '2026-08-07T12:00:00Z')`, m.Version, m.Name, m.checksum()); err != nil {
			return rollback(err)
		}
	}
	if err := tx.Commit(); err != nil {
		_ = db.Close()
		t.Fatalf("commit v3 seed transaction: %v", err)
	}
	return db
}

func TestOpenConcurrentlyInitializesOneDatabase(t *testing.T) {
	const openers = 8

	path := filepath.Join(t.TempDir(), "concord.db")
	ctx := context.Background()
	// Seed only the empty manifest on a new file so every concurrent Open reaches
	// the pending-migration read-to-write path instead of serializing on table creation.
	seed, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatalf("seed sql.Open() error = %v", err)
	}
	if _, err := seed.ExecContext(ctx, schemaManifestDDL); err != nil {
		_ = seed.Close()
		t.Fatalf("seed schema manifest: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("seed Close() error = %v", err)
	}
	var ready sync.WaitGroup
	var start sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(openers)
	start.Add(1)
	done.Add(openers)

	stores := make([]*Store, openers)
	errs := make([]error, openers)
	for i := range openers {
		go func(i int) {
			defer done.Done()
			ready.Done()
			start.Wait()
			stores[i], errs[i] = Open(ctx, path)
		}(i)
	}
	ready.Wait()
	start.Done()
	done.Wait()

	for i, s := range stores {
		if s == nil {
			continue
		}
		t.Cleanup(func() {
			if err := s.Close(); err != nil {
				t.Errorf("Open(%d) Close() error = %v", i, err)
			}
		})
	}
	for i, err := range errs {
		if err != nil {
			t.Errorf("Open(%d) error = %v", i, err)
		}
	}

	var verifier *Store
	for _, s := range stores {
		if s != nil {
			verifier = s
			break
		}
	}
	if verifier == nil {
		t.Fatal("all concurrent Open calls failed")
	}

	got, err := SchemaVersion(ctx, verifier.DB())
	if err != nil {
		t.Fatalf("SchemaVersion() error = %v", err)
	}
	if want := migrations[len(migrations)-1].Version; got != want {
		t.Errorf("SchemaVersion() = %d, want %d", got, want)
	}

	var applied int
	if err := verifier.DB().QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if applied != len(migrations) {
		t.Errorf("applied migrations = %d, want %d", applied, len(migrations))
	}
}

func TestSchemaCompatibilityRejectsCallerOlderThanDatabase(t *testing.T) {
	s := openTemp(t)
	_, err := CheckSchemaCompatibility(context.Background(), s.DB(), CurrentSchemaVersion()-1)
	assertFailureKind(t, err, KindSchemaUnsupported)
}

// The manifest records a checksum per migration so an edited historical
// migration is detected instead of silently diverging from the live schema.
func TestMigrateDetectsEditedHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concord.db")
	ctx := context.Background()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := s.DB().ExecContext(ctx,
		`UPDATE schema_migrations SET checksum = 'tampered' WHERE version = ?`, migrations[0].Version); err != nil {
		t.Fatalf("tamper checksum: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	_, err = Open(ctx, path)
	var f *Failure
	if !errors.As(err, &f) {
		t.Fatalf("Open() on tampered history error = %v, want *Failure", err)
	}
	if f.Kind != KindSchemaDrift {
		t.Errorf("Kind = %q, want %q", f.Kind, KindSchemaDrift)
	}
}

// A database written by a newer binary must fail closed rather than be operated
// on by an older schema definition.
func TestMigrateRejectsNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concord.db")
	ctx := context.Background()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	future := migrations[len(migrations)-1].Version + 1
	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, 'from-the-future', 'x', '2026-01-01T00:00:00Z')`,
		future); err != nil {
		t.Fatalf("insert future migration: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	_, err = Open(ctx, path)
	var f *Failure
	if !errors.As(err, &f) {
		t.Fatalf("Open() on newer schema error = %v, want *Failure", err)
	}
	if f.Kind != KindSchemaUnsupported {
		t.Errorf("Kind = %q, want %q", f.Kind, KindSchemaUnsupported)
	}
	if !f.RetrySafe {
		t.Error("RetrySafe = false; upgrading the binary and retrying is the documented recovery")
	}
}

func TestMigrationsAreOrderedAndUnique(t *testing.T) {
	seen := make(map[int]bool, len(migrations))
	for i, m := range migrations {
		if m.Version <= 0 {
			t.Errorf("migration %d has non-positive version %d", i, m.Version)
		}
		if seen[m.Version] {
			t.Errorf("duplicate migration version %d", m.Version)
		}
		seen[m.Version] = true
		if i > 0 && m.Version <= migrations[i-1].Version {
			t.Errorf("migration %d version %d is not greater than previous %d", i, m.Version, migrations[i-1].Version)
		}
		if m.Name == "" {
			t.Errorf("migration %d has no name", i)
		}
		if m.SQL == "" {
			t.Errorf("migration %d has no statements", i)
		}
	}
}

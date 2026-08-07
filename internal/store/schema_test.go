package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
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

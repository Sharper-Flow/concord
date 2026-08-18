package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Concurrent openers of one database are serialized by SQLite: the first
// applies the manifest and the rest wait. The migrating transaction grows with
// every step added to the manifest while the connection busy timeout stays
// fixed, so one blocked attempt is not a safe ceiling — without a read-only
// fast path and a bounded retry, adding any migration eventually breaks
// concurrent first-open.

// openRawDatabase opens a second handle on one path. Store.Open caps its pool
// at a single connection, so a test that holds a transaction and then queries
// through the same handle would block on the pool rather than on the lock
// under test.
func openRawDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// holdWriteLock takes the write lock and keeps it until the returned release
// is called.
func holdWriteLock(t *testing.T, ctx context.Context, db *sql.DB, probe string) func() {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx() error = %v", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE `+probe+` (id TEXT PRIMARY KEY)`); err != nil {
		_ = tx.Rollback()
		t.Fatalf("holding the write lock failed: %v", err)
	}
	released := false
	t.Cleanup(func() {
		if !released {
			_ = tx.Rollback()
		}
	})
	return func() {
		released = true
		if err := tx.Rollback(); err != nil {
			t.Errorf("Rollback() error = %v", err)
		}
	}
}

func busyTimeout(t *testing.T) time.Duration {
	t.Helper()
	ms, err := strconv.Atoi(pragmaBusyTimeout)
	if err != nil {
		t.Fatalf("pragmaBusyTimeout is not a duration in milliseconds: %v", err)
	}
	return time.Duration(ms) * time.Millisecond
}

func TestMigrateOnCurrentDatabaseTakesNoWriteLock(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)

	// Hold the write lock for the whole call. A migrate that still opened a
	// write transaction on an already-current database would block here.
	holdWriteLock(t, ctx, openRawDatabase(t, s.Path()), "migrate_fastpath_probe")

	migrated := make(chan error, 1)
	go func() { migrated <- Migrate(ctx, openRawDatabase(t, s.Path())) }()
	select {
	case err := <-migrated:
		if err != nil {
			t.Fatalf("migrate on a current database returned %v", err)
		}
	case <-time.After(busyTimeout(t) / 2):
		t.Fatal("migrate on a current database contended for the write lock")
	}
}

func TestMigrateWaitsOutALockHeldPastOneBusyTimeout(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "unmigrated.db")

	// The blocker creates the database file and holds the write lock, so the
	// migrating handle finds an unmigrated database it cannot yet write.
	release := holdWriteLock(t, ctx, openRawDatabase(t, path), "migrate_retry_probe")

	migrateDB := openRawDatabase(t, path)
	migrated := make(chan error, 1)
	go func() { migrated <- Migrate(ctx, migrateDB) }()

	// Hold well past a single busy timeout. A migrate that gave up after one
	// blocked attempt has already failed by the time the lock is released.
	held := busyTimeout(t) + 1500*time.Millisecond
	time.Sleep(held)
	select {
	case err := <-migrated:
		t.Fatalf("migrate returned after %s while the lock was still held: %v", held, err)
	default:
	}
	release()

	select {
	case err := <-migrated:
		if err != nil {
			t.Fatalf("migrate did not wait out the contended lock: %v", err)
		}
	case <-time.After(migrationLockBudget):
		t.Fatal("migrate never returned within its lock budget")
	}

	version, err := SchemaVersion(ctx, migrateDB)
	if err != nil {
		t.Fatal(err)
	}
	if version != CurrentSchemaVersion() {
		t.Fatalf("schema version = %d, want %d", version, CurrentSchemaVersion())
	}
}

func TestMigrationLockContendedNeverRetriesDriftOrUnsupportedSchema(t *testing.T) {
	for _, testCase := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "busy database is contention", err: wrapFailure(KindUnavailable, "migrate", "cannot begin schema migration", true, "retry", errors.New("database is locked (5) (SQLITE_BUSY)")), want: true},
		{name: "locked table is contention", err: wrapFailure(KindUnavailable, "migrate", "cannot begin schema migration", true, "retry", errors.New("database table is locked")), want: true},
		{name: "schema drift is never retried", err: newFailure(KindSchemaDrift, "migrate", "migration no longer matches its recorded checksum", false, "restore"), want: false},
		{name: "unsupported schema is never retried", err: newFailure(KindSchemaUnsupported, "migrate", "database records an unknown migration", true, "upgrade"), want: false},
		{name: "membership requirement is never retried", err: newFailure(KindMembershipMigrationRequired, "migrate", "explicit memberships required", false, "map memberships"), want: false},
		{name: "unavailable without a lock cause is not contention", err: wrapFailure(KindUnavailable, "migrate", "cannot commit", true, "retry", errors.New("disk I/O error")), want: false},
		{name: "nil is not contention", err: nil, want: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := migrationLockContended(testCase.err); got != testCase.want {
				t.Fatalf("migrationLockContended = %v, want %v", got, testCase.want)
			}
		})
	}
}

// The fast path must not become a way to skip drift detection.
func TestMigrationManifestCurrentStillFailsClosedOnAnUnknownMigration(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`,
		9999, "unknown_future_step", "deadbeef", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}

	current, err := migrationManifestCurrent(ctx, s.DatabaseForTesting())
	if err == nil {
		t.Fatal("the fast path accepted a manifest this binary does not define")
	}
	if current {
		t.Fatal("the fast path reported a drifted manifest as current")
	}
	assertFailureKind(t, err, KindSchemaUnsupported)

	// The same refusal must survive the full entry point.
	assertFailureKind(t, Migrate(ctx, s.DatabaseForTesting()), KindSchemaUnsupported)
}

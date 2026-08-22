// Package store holds Concord's local SQLite authority: the append-only domain
// event log and, in later slices, the typed projections folded from it.
//
// One database file is the whole live authority for an operator installation.
// Product and Project scope are domain concepts inside that file, never
// separate files.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	// Registers the pure-Go "sqlite" driver.
	_ "modernc.org/sqlite"
)

const (
	driverName = "sqlite"

	// The connection settings are fixed by the accepted state-authority
	// decision. Write-ahead logging admits concurrent readers alongside a
	// writer; NORMAL synchronous mode trades an fsync per commit for the
	// recovery fold; the busy timeout absorbs contention between short-lived
	// processes.
	pragmaBusyTimeout = "5000"
	pragmaJournalMode = "WAL"
	pragmaSynchronous = "NORMAL"
	pragmaForeignKeys = "ON"
)

// Store is an open handle to the local authority.
type Store struct {
	db   *sql.DB
	path string
}

// DatabaseForTesting exposes the raw handle only to tests and fixtures.
// Production code must use typed store operations so the store owns its
// transaction and projection boundaries. boundary_test.go enforces this
// seam through the toolchain parser.
func (s *Store) DatabaseForTesting() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

// Path reports the database file backing this store.
func (s *Store) Path() string { return s.path }

// Close releases the handle.
func (s *Store) Close() error { return s.db.Close() }

// DefaultPath reports the platform data-directory location of the single local
// authority. The database lives outside any Project repository.
func DefaultPath() (string, error) {
	if dataHome := os.Getenv("XDG_DATA_HOME"); dataHome != "" {
		return filepath.Join(dataHome, "concord", "concord.db"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", newFailure(KindUnavailable, "default_path",
			"neither XDG_DATA_HOME nor a user home directory is set", false,
			"set XDG_DATA_HOME or HOME, or pass an explicit database path")
	}
	return filepath.Join(home, ".local", "share", "concord", "concord.db"), nil
}

// Open prepares the authority at path, creating the file and its parent
// directory when absent, and brings the schema up to date.
func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, newFailure(KindUnavailable, "open", "empty database path", false,
			"pass a database path or use DefaultPath")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, wrapFailure(KindUnavailable, "open", "cannot create the data directory", true,
			"check directory permissions", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return nil, wrapFailure(KindUnavailable, "open", "cannot secure the data directory", true,
			"check directory permissions", err)
	}

	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "open", "cannot open the database", true,
			"check the database path and permissions", err)
	}

	// Writes serialize on one connection. This keeps write-write contention
	// inside this process off SQLite entirely; cross-process contention is
	// absorbed by the busy timeout.
	db.SetMaxOpenConns(1)

	s := &Store{db: db, path: path}
	if err := s.verifyPragmas(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := Migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, wrapFailure(KindUnavailable, "open", "cannot secure the database file", true,
			"check database file permissions", err)
	}
	if err := ensureInstallationKey(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := validateMembershipInvariants(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// ensureInstallationKey creates the one authority-owned cursor signing key.
// INSERT is idempotent so concurrent short-lived opens converge on one key.
func ensureInstallationKey(ctx context.Context, db *sql.DB) error {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return wrapFailure(KindUnavailable, "open", "cannot create the installation cursor key", true,
			"retry once the operating system random source is available", err)
	}
	_, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO agent_installation_keys(key_name,key_bytes,created_at) VALUES('cursor',?,?)`, key, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return wrapFailure(KindUnavailable, "open", "cannot persist the installation cursor key", true,
			"retry once the database is writable", err)
	}
	return nil
}

// InstallationKey returns the authority-owned key for authenticated cursors.
// The bytes are never serialized into an agent response.
func InstallationKey(ctx context.Context, db *sql.DB) ([]byte, error) {
	if db == nil {
		return nil, newFailure(KindUnavailable, "cursor_key", "database is not open", true, "open the authority database")
	}
	var key []byte
	if err := db.QueryRowContext(ctx, `SELECT key_bytes FROM agent_installation_keys WHERE key_name='cursor'`).Scan(&key); err != nil {
		return nil, wrapFailure(KindUnavailable, "cursor_key", "cannot read the installation cursor key", true,
			"open a migrated authority database", err)
	}
	if len(key) != 32 {
		return nil, newFailure(KindInvariantViolation, "cursor_key", "installation cursor key has an invalid length", false,
			"rebuild the authority from its migration")
	}
	return append([]byte(nil), key...), nil
}

// dataSourceName builds the connection string. Every setting travels in the
// data source name because SQLite applies these settings per connection: a
// one-off statement after opening would configure a single pooled connection
// and silently leave the rest at their defaults.
//
// The busy timeout is listed first so it is already in force if this connection
// is the one that converts the journal to write-ahead logging.
func dataSourceName(path string) string {
	pragmas := []string{
		"busy_timeout(" + pragmaBusyTimeout + ")",
		"journal_mode(" + pragmaJournalMode + ")",
		"synchronous(" + pragmaSynchronous + ")",
		"foreign_keys(" + pragmaForeignKeys + ")",
	}
	// BEGIN IMMEDIATE acquires the write lock before migration reads begin. This
	// avoids SQLite's read-to-write upgrade path, where SQLITE_BUSY can skip the
	// busy handler and return immediately. This only affects explicit BeginTx
	// calls; plain autocommit QueryContext reads do not issue BEGIN.
	query := []string{"_txlock=immediate"}
	for _, p := range pragmas {
		query = append(query, "_pragma="+url.QueryEscape(p))
	}
	return "file:" + path + "?" + strings.Join(query, "&")
}

// verifyPragmas reads the settings back from a live connection. A data source
// name that is accepted but not applied is a silent failure mode, so the
// settings are confirmed rather than assumed.
func (s *Store) verifyPragmas(ctx context.Context) error {
	for _, want := range []struct {
		pragma string
		expect string
	}{
		{"journal_mode", strings.ToLower(pragmaJournalMode)},
		{"synchronous", "1"}, // NORMAL
		{"busy_timeout", pragmaBusyTimeout},
		{"foreign_keys", "1"}, // ON
	} {
		var got string
		if err := s.db.QueryRowContext(ctx, "PRAGMA "+want.pragma).Scan(&got); err != nil {
			return wrapFailure(KindUnavailable, "open",
				fmt.Sprintf("cannot read PRAGMA %s", want.pragma), true,
				"confirm the database file is a readable SQLite database", err)
		}
		if !strings.EqualFold(got, want.expect) {
			return newFailure(KindUnavailable, "open",
				fmt.Sprintf("PRAGMA %s is %q, want %q", want.pragma, got, want.expect), false,
				"confirm the driver applies connection settings from the data source name")
		}
	}
	return nil
}

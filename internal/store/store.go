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
	db    *sql.DB
	path  string
	Clock func() time.Time
}

func (s *Store) now() time.Time {
	if s == nil || s.Clock == nil {
		return time.Now().UTC()
	}
	return s.Clock().UTC()
}

// Now returns the store clock value for callers that compose durable records.
func (s *Store) Now() time.Time { return s.now() }

func nowFromClock(clock func() time.Time) time.Time {
	if clock == nil {
		return time.Now().UTC()
	}
	return clock().UTC()
}

func firstClock(clocks []func() time.Time) func() time.Time {
	if len(clocks) == 0 {
		return nil
	}
	return clocks[0]
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
// directory when absent, and brings the schema up to date. It applies additive
// migrations only: when the next pending migration is breaking it refuses with
// KindUpgradeRequired, and Upgrade applies that step (CD-0111 D3).
func Open(ctx context.Context, path string) (*Store, error) {
	s, err := openUnmigrated(ctx, path)
	if err != nil {
		return nil, err
	}
	if err := migrateAtOpen(ctx, s.db, s.Clock); err != nil {
		_ = s.db.Close()
		return nil, err
	}
	if err := s.finishOpen(ctx); err != nil {
		_ = s.db.Close()
		return nil, err
	}
	return s, nil
}

// HeldSchema is one live host session's claim on the schema its release
// defines. The caller observes sessions; the store only compares versions.
type HeldSchema struct {
	PID           int    `json:"pid"`
	ReleaseRoot   string `json:"release_root"`
	SchemaVersion int    `json:"schema_version"`
}

// UpgradeReport states what one Upgrade call applied.
type UpgradeReport struct {
	// Applied lists the migration versions this call applied, in order.
	Applied []int `json:"applied"`
	// SchemaVersion is the highest applied migration after the call.
	SchemaVersion int `json:"schema_version"`
}

// Upgrade applies every pending migration, breaking steps included. It refuses
// with KindUpgradeBlocked while any held schema predates the first pending
// breaking step, naming each session and the release it holds, and writes
// nothing in that case. With no pending breaking step it applies the pending
// additive steps, which Open would also apply.
func Upgrade(ctx context.Context, path string, held []HeldSchema) (UpgradeReport, error) {
	s, err := openUnmigrated(ctx, path)
	if err != nil {
		return UpgradeReport{}, err
	}
	defer func() { _ = s.db.Close() }()

	before, err := manifestVersions(ctx, s.db)
	if err != nil {
		return UpgradeReport{}, err
	}
	if pending := pendingBreaking(before); len(pending) > 0 {
		if err := refuseHeldOlderSchemas(pending, held); err != nil {
			return UpgradeReport{}, err
		}
	}
	if err := Migrate(ctx, s.db, s.Clock); err != nil {
		return UpgradeReport{}, err
	}
	if err := s.finishOpen(ctx); err != nil {
		return UpgradeReport{}, err
	}
	after, err := manifestVersions(ctx, s.db)
	if err != nil {
		return UpgradeReport{}, err
	}
	report := UpgradeReport{Applied: []int{}}
	for _, m := range migrations {
		if _, was := before[m.Version]; was {
			continue
		}
		if _, is := after[m.Version]; is {
			report.Applied = append(report.Applied, m.Version)
		}
	}
	for version := range after {
		if version > report.SchemaVersion {
			report.SchemaVersion = version
		}
	}
	return report, nil
}

// refuseHeldOlderSchemas is the CD-0111 D3 gate: a breaking step never applies
// under a live session whose release predates it. Each refused session is
// named with the first pending breaking step it predates.
func refuseHeldOlderSchemas(pending []migration, held []HeldSchema) error {
	var older []string
	for _, h := range held {
		for _, m := range pending {
			if h.SchemaVersion < m.Version {
				older = append(older, fmt.Sprintf("pid %d holds %s at schema version %d, before migration %d (%s)",
					h.PID, h.ReleaseRoot, h.SchemaVersion, m.Version, m.Name))
				break
			}
		}
	}
	if len(older) == 0 {
		return nil
	}
	return newFailure(KindUpgradeBlocked, "upgrade",
		fmt.Sprintf("a pending breaking migration waits for %d live session(s) that predate it: %s",
			len(older), strings.Join(older, "; ")),
		true, "end or move those sessions to the installed release, then run concord upgrade again")
}

// manifestVersions reads the applied manifest; a database with no manifest
// table reads as empty. A manifest that predates the breaking column cannot
// be read at all, so the additive repair runs first — the same one every
// migration pass applies — never a migration step.
func manifestVersions(ctx context.Context, db *sql.DB) (map[int]appliedMigration, error) {
	var present string
	err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_schema WHERE type='table' AND name='schema_migrations'`).Scan(&present)
	if err == sql.ErrNoRows {
		return map[int]appliedMigration{}, nil
	}
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "upgrade", "cannot inspect the schema manifest", true,
			"confirm the database is readable", err)
	}
	applied, err := appliedMigrations(ctx, db)
	if err == nil {
		return applied, nil
	}
	if !strings.Contains(err.Error(), "no such column: breaking") {
		return nil, err
	}
	if err := repairManifestBreakingColumn(ctx, db); err != nil {
		return nil, err
	}
	return appliedMigrations(ctx, db)
}

// repairManifestBreakingColumn re-adds the compatibility column an older
// binary's manifest lacks. It is additive and idempotent: no migration step
// applies, and the column's default marks every recorded row breaking, which
// is the conservative read the manifest check expects.
func repairManifestBreakingColumn(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return wrapFailure(KindUnavailable, "upgrade", "cannot begin the manifest repair", true,
			"retry once the database is writable", err)
	}
	if _, err := tx.ExecContext(ctx, schemaManifestDDL); err != nil {
		_ = tx.Rollback()
		return wrapFailure(KindUnavailable, "upgrade", "cannot create the schema manifest", true,
			"check database permissions", err)
	}
	if err := ensureManifestBreakingColumn(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrapFailure(KindUnavailable, "upgrade", "cannot commit the manifest repair", true,
			"retry once the database is writable", err)
	}
	return nil
}

// openUnmigrated prepares the file, the directory, and the connection without
// touching the schema. Open and Upgrade share it and differ only in the
// migration scope they apply next.
func openUnmigrated(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, newFailure(KindUnavailable, "open", "empty database path", false,
			"pass a database path or use DefaultPath")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil { //nolint:gosec // path is the explicit operator-selected authority path and private permissions constrain its parent.
		return nil, wrapFailure(KindUnavailable, "open", "cannot create the data directory", true,
			"check directory permissions", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil { //nolint:gosec // the explicit authority parent is forced to private directory permissions.
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

	s := &Store{db: db, path: path, Clock: func() time.Time { return time.Now().UTC() }}
	if err := s.verifyPragmas(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// finishOpen runs the post-migration steps every opener shares: private file
// permissions, the installation key, and the membership invariants.
func (s *Store) finishOpen(ctx context.Context) error {
	if err := os.Chmod(s.path, 0o600); err != nil { //nolint:gosec // the explicit authority file is forced to private file permissions after migration.
		return wrapFailure(KindUnavailable, "open", "cannot secure the database file", true,
			"check database file permissions", err)
	}
	if err := ensureInstallationKey(ctx, s.db, s.now()); err != nil {
		return err
	}
	return validateMembershipInvariants(ctx, s.db)
}

// ensureInstallationKey creates the one authority-owned cursor signing key.
// INSERT is idempotent so concurrent short-lived opens converge on one key.
func ensureInstallationKey(ctx context.Context, db *sql.DB, createdAt ...time.Time) error {
	when := nowFromClock(nil)
	if len(createdAt) > 0 {
		when = createdAt[0]
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return wrapFailure(KindUnavailable, "open", "cannot create the installation cursor key", true,
			"retry once the operating system random source is available", err)
	}
	_, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO agent_installation_keys(key_name,key_bytes,created_at) VALUES('cursor',?,?)`, key, when.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return wrapFailure(KindUnavailable, "open", "cannot persist the installation cursor key", true,
			"retry once the database is writable", err)
	}
	return nil
}

// InstallationKey returns the authority-owned key for authenticated cursors.
// The bytes are never serialized into an agent response.
//
// The queryer must be a live handle; callers guard their own handle before
// calling in.
func InstallationKey(ctx context.Context, q queryer) ([]byte, error) {
	var key []byte
	if err := q.QueryRowContext(ctx, `SELECT key_bytes FROM agent_installation_keys WHERE key_name='cursor'`).Scan(&key); err != nil {
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

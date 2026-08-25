// Package storetest opens migrated Concord stores for tests without paying
// the migration cost on every open.
//
// store.Open runs all schema migrations against a new file. That is correct
// for production, where it happens once per installation, but a test suite
// opens a fresh database per test and pays it every time. The cost is not
// visible in ordinary runs and is severe under the race detector, because
// SQLite here is modernc.org/sqlite - pure Go, so -race instruments the
// database engine itself. Measured on internal/agent: a cold open costs
// ~0.19s normally and ~7-10s under -race, against a package total of ~1133s.
//
// So migrate once per process into a template, then copy the file. The
// template deliberately omits the installation cursor key: store.Open inserts
// it with INSERT OR IGNORE, so a copy still mints its own distinct key and
// per-installation identity is preserved rather than shared.
package storetest

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"

	"github.com/sharper-flow/concord/internal/store"
)

var (
	once     sync.Once
	template []byte
	buildErr error
)

// Open returns a migrated store at dir/concord.db.
func Open(dir string) (*store.Store, error) {
	return OpenNamed(dir, "concord.db")
}

// OpenNamed returns a migrated store at dir/name.
func OpenNamed(dir, name string) (*store.Store, error) {
	if err := WriteMigratedDatabase(dir, name); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, name)
	// Migrate finds the schema current and returns without work;
	// ensureInstallationKey still mints this database its own key.
	s, err := store.Open(context.Background(), path)
	if err != nil {
		return nil, fmt.Errorf("storetest: open %s: %w", path, err)
	}
	return s, nil
}

// WriteMigratedDatabase materializes the shared migrated template at
// dir/name without opening it. Tests that drive the CLI in process point the
// database override at the file, so the first run validates a current schema
// instead of replaying every migration. Each written file mints its own
// installation key on first open, exactly as a copy from OpenNamed does.
func WriteMigratedDatabase(dir, name string) error {
	once.Do(build)
	if buildErr != nil {
		return buildErr
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("storetest: create dir %s: %w", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), template, 0o600); err != nil {
		return fmt.Errorf("storetest: write template to %s: %w", filepath.Join(dir, name), err)
	}
	return nil
}

// build migrates one database and captures its bytes. A failure here is
// returned to every caller rather than falling back to a per-test migration,
// so a broken template surfaces as itself instead of as a slow suite.
func build() {
	dir, err := os.MkdirTemp("", "concord-storetest")
	if err != nil {
		buildErr = fmt.Errorf("storetest: create template dir: %w", err)
		return
	}
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "template.db")
	s, err := store.Open(context.Background(), path)
	if err != nil {
		buildErr = fmt.Errorf("storetest: migrate template: %w", err)
		return
	}
	if err := s.Close(); err != nil {
		buildErr = fmt.Errorf("storetest: close template: %w", err)
		return
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		buildErr = fmt.Errorf("storetest: reopen template: %w", err)
		return
	}
	defer db.Close()
	// Drop the installation key so each copy mints its own, and VACUUM so the
	// captured bytes are a single self-contained file with no WAL sidecar.
	if _, err := db.Exec(`DELETE FROM agent_installation_keys`); err != nil {
		buildErr = fmt.Errorf("storetest: clear installation key: %w", err)
		return
	}
	if _, err := db.Exec(`VACUUM`); err != nil {
		buildErr = fmt.Errorf("storetest: vacuum template: %w", err)
		return
	}
	if err := db.Close(); err != nil {
		buildErr = fmt.Errorf("storetest: close template handle: %w", err)
		return
	}

	data, err := os.ReadFile(path)
	if err != nil {
		buildErr = fmt.Errorf("storetest: read template: %w", err)
		return
	}
	template = data
}

package storetest_test

import (
	"bytes"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

func openTwo(t *testing.T) (string, string) {
	t.Helper()
	first, err := storetest.Open(filepath.Join(t.TempDir(), "a"))
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	t.Cleanup(func() { first.Close() })
	second, err := storetest.Open(filepath.Join(t.TempDir(), "b"))
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	t.Cleanup(func() { second.Close() })
	return first.Path(), second.Path()
}

func query[T any](t *testing.T, path, statement string) T {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer db.Close()
	var value T
	if err := db.QueryRow(statement).Scan(&value); err != nil {
		t.Fatalf("query %q: %v", statement, err)
	}
	return value
}

// The installation cursor key is per-installation identity. Copying a template
// must not hand two databases the same one.
func TestTemplateBackedOpensMintDistinctInstallationKeys(t *testing.T) {
	first, second := openTwo(t)
	const statement = `SELECT key_bytes FROM agent_installation_keys WHERE key_name='cursor'`
	a := query[[]byte](t, first, statement)
	b := query[[]byte](t, second, statement)
	if len(a) != 32 {
		t.Fatalf("expected a 32-byte key, got %d bytes", len(a))
	}
	if bytes.Equal(a, b) {
		t.Fatal("two template-backed stores share an installation key")
	}
}

// A copy must carry the same schema a cold open produces, or tests would run
// against a database the production path never creates.
func TestTemplateSchemaMatchesAColdOpen(t *testing.T) {
	cold, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "cold.db"))
	if err != nil {
		t.Fatalf("cold open: %v", err)
	}
	t.Cleanup(func() { cold.Close() })

	warm, err := storetest.Open(filepath.Join(t.TempDir(), "warm"))
	if err != nil {
		t.Fatalf("template open: %v", err)
	}
	t.Cleanup(func() { warm.Close() })

	const objects = `SELECT COALESCE(GROUP_CONCAT(name || ':' || COALESCE(sql, '')), '') FROM (SELECT name, sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY name)`
	if got, want := query[string](t, warm.Path(), objects), query[string](t, cold.Path(), objects); got != want {
		t.Fatal("template schema differs from a cold open")
	}

	const version = `SELECT COUNT(*) FROM schema_migrations`
	if got, want := query[int](t, warm.Path(), version), query[int](t, cold.Path(), version); got != want {
		t.Fatalf("template applied %d migrations, cold open applied %d", got, want)
	}
}

// The template is a shared process-wide value; a mutation in one store must
// not reach another.
func TestTemplateBackedStoresAreIndependent(t *testing.T) {
	first, second := openTwo(t)
	db, err := sql.Open("sqlite", first)
	if err != nil {
		t.Fatalf("open first: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`DELETE FROM agent_installation_keys`); err != nil {
		t.Fatalf("mutate first: %v", err)
	}
	if got := query[int](t, second, `SELECT COUNT(*) FROM agent_installation_keys`); got != 1 {
		t.Fatalf("second store observed the first store's write: %d key rows", got)
	}
}

// WriteMigratedDatabase hands the caller a migrated file the CLI tests point
// the database override at; the first open must accept it as current schema
// and still mint per-installation identity.
func TestWriteMigratedDatabaseYieldsOpenableCurrentSchema(t *testing.T) {
	dir := t.TempDir()
	if err := storetest.WriteMigratedDatabase(dir, "staged.db"); err != nil {
		t.Fatalf("write migrated database: %v", err)
	}
	path := filepath.Join(dir, "staged.db")
	first, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("open written database: %v", err)
	}
	t.Cleanup(func() { first.Close() })

	if got := query[int](t, first.Path(), `SELECT COUNT(*) FROM schema_migrations`); got == 0 {
		t.Fatal("written database applied no migrations")
	}
	if got := query[int](t, first.Path(), `SELECT COUNT(*) FROM agent_installation_keys`); got != 1 {
		t.Fatalf("installation key rows = %d, want the per-open mint of exactly 1", got)
	}

	if err := storetest.WriteMigratedDatabase(dir, "second.db"); err != nil {
		t.Fatalf("write second database: %v", err)
	}
	second, err := store.Open(t.Context(), filepath.Join(dir, "second.db"))
	if err != nil {
		t.Fatalf("open second written database: %v", err)
	}
	t.Cleanup(func() { second.Close() })
	const statement = `SELECT key_bytes FROM agent_installation_keys WHERE key_name='cursor'`
	if bytes.Equal(query[[]byte](t, first.Path(), statement), query[[]byte](t, second.Path(), statement)) {
		t.Fatal("two written databases share an installation key")
	}
}

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/version"
)

func useStampedBuild(t *testing.T) {
	t.Helper()
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
}

// unstampedWindow identifies an applied schema version and pending tail.
type unstampedWindow struct {
	name    string
	applied int
}

func unstampedWindows(t *testing.T) []unstampedWindow {
	t.Helper()
	return []unstampedWindow{{name: "schema 199", applied: 199}, {name: "schema 200", applied: 200}}
}

// assertUnstampedFacts checks that the refusal reports the manifest it read
// against this binary's migrations: the applied head and the pending tail.
func assertUnstampedFacts(t *testing.T, err error, lead string, applied int) {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("error = %v, want an unstamped migration refusal", err)
	}
	index := -1
	for i, m := range migrations {
		if m.Version == applied {
			index = i
			break
		}
	}
	if index < 0 || index+1 >= len(migrations) {
		t.Fatalf("schema %d has no pending migration", applied)
	}
	breaking := 0
	for _, m := range migrations[index+1:] {
		if m.Breaking {
			breaking++
		}
	}
	head, next := migrations[index], migrations[index+1]
	want := fmt.Sprintf("%s at schema version %d (%q); this build would apply %d pending migration(s) from %d %q, %d breaking; the store does not record which release applied its migrations",
		lead, head.Version, head.Name, len(migrations)-index-1, next.Version, next.Name, breaking)
	if failure.Detail != want {
		t.Fatalf("refusal detail = %q, want %q", failure.Detail, want)
	}
}

func assertUnstampedRefusal(t *testing.T, err error, applied int) {
	t.Helper()
	assertUnstampedFacts(t, err, "unstamped development build refuses schema migration for an existing store", applied)
}

// An unstamped development build must not migrate a store that already
// carries applied migrations.
func TestUnstampedOpenDoesNotMigrateAnExistingStore(t *testing.T) {
	if version.Value != version.Development {
		t.Skip("binary is release-stamped; the unstamped refusal does not apply")
	}
	for _, window := range unstampedWindows(t) {
		t.Run(window.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "release-owned.db")
			if err := openMigratedTo(t, path, window.applied).Close(); err != nil {
				t.Fatal(err)
			}
			before := manifestMax(t, path)

			_, err := Open(context.Background(), path)
			if err == nil {
				t.Fatal("unstamped Open() operated a release-owned store; it must refuse instead")
			}
			assertUnstampedRefusal(t, err, window.applied)
			if got := manifestMax(t, path); got != before {
				t.Fatalf("schema version after unstamped open = %d, want %d: an unstamped build must write nothing", got, before)
			}
		})
	}
}

// A stamped release binary keeps today's behavior: it applies the pending
// additive run at open and stops before a breaking step. The refusal above
// is reserved for unstamped builds, so releases keep migrating forward.
func TestUnstampedOpenStillCreatesAFreshStore(t *testing.T) {
	if version.Value != version.Development {
		t.Skip("binary is release-stamped; the unstamped refusal does not apply")
	}
	path := filepath.Join(t.TempDir(), "fresh.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("unstamped Open() of a fresh store error = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got := manifestMax(t, path); got != CurrentSchemaVersion() {
		t.Fatalf("fresh database schema version = %d, want %d", got, CurrentSchemaVersion())
	}
	var count int
	if err := openRawDatabase(t, path).QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("fresh manifest count = %d, %v; want 3", count, err)
	}
}

func TestUnstampedUpgradeDoesNotMigrateAnExistingStore(t *testing.T) {
	if version.Value != version.Development {
		t.Skip("binary is release-stamped; the unstamped refusal does not apply")
	}
	for _, window := range unstampedWindows(t) {
		t.Run(window.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "release-owned.db")
			if err := openMigratedTo(t, path, window.applied).Close(); err != nil {
				t.Fatal(err)
			}
			before := manifestMax(t, path)

			_, err := Upgrade(context.Background(), path, nil)
			assertUnstampedRefusal(t, err, window.applied)
			var failure *Failure
			_ = errors.As(err, &failure)
			if !strings.Contains(failure.RecoveryAction, "CONCORD_DB_PATH") || !strings.Contains(failure.RecoveryAction, "outside any repository") {
				t.Fatalf("recovery action = %q, want the isolation route", failure.RecoveryAction)
			}
			if !failure.RetrySafe {
				t.Fatal("unstamped migration refusal must be retry-safe")
			}
			if got := manifestMax(t, path); got != before {
				t.Fatalf("schema version after unstamped upgrade = %d, want %d", got, before)
			}
		})
	}
}

// preColumnStore writes a store migrated through applied whose manifest
// predates the breaking column.
func preColumnStore(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at TEXT NOT NULL); INSERT INTO schema_migrations VALUES(198,'old','old','time')`); err != nil {
		t.Fatalf("stage a pre-column manifest: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertPreColumnRefusal(t *testing.T, err error, path string) {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindSchemaUnsupported || !strings.Contains(failure.Detail+failure.RecoveryAction, "199") || !strings.Contains(failure.Detail+failure.RecoveryAction, "v67.0.0") {
		t.Fatalf("refusal = %v, want unsupported schema with floor and bridge", err)
	}
	var count int
	err = openRawDatabase(t, path).QueryRowContext(context.Background(), `SELECT count(*) FROM pragma_table_info('schema_migrations') WHERE name='breaking'`).Scan(&count)
	if err != nil || count != 0 {
		t.Fatalf("breaking column count = %d, %v after the refusal; an unstamped build must repair nothing", count, err)
	}
}

// A below-floor manifest is refused without repair.
func TestUnstampedOpenReportsAManifestItRefusesToRepair(t *testing.T) {
	if version.Value != version.Development {
		t.Skip("binary is release-stamped; the unstamped refusal does not apply")
	}
	path := filepath.Join(t.TempDir(), "pre-column.db")
	preColumnStore(t, path)

	_, err := Open(context.Background(), path)
	assertPreColumnRefusal(t, err, path)
}

// Backup refuses a below-floor source before staging it.
func TestUnstampedBackupRefusesAPreColumnManifest(t *testing.T) {
	useDevelopmentBuild(t)
	root := t.TempDir()
	source := filepath.Join(root, "pre-column.db")
	preColumnStore(t, source)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := BackupDatabase(ctx, source, filepath.Join(root, "snapshot.db"))
	assertPreColumnRefusal(t, err, source)
}

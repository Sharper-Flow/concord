package store

import (
	"context"
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

// unstampedWindow is a store migrated through the first applied migrations,
// so every later shipped migration is pending for an unstamped build.
type unstampedWindow struct {
	name    string
	applied int
}

// unstampedWindows derives both pending shapes from the manifest: a breaking
// step pending behind an additive run, and the additive-only tail after the
// newest breaking migration. applied is zero when no such tail exists.
func unstampedWindows(t *testing.T) []unstampedWindow {
	t.Helper()
	_, mixed := breakingWindow(t)
	tail := unstampedWindow{name: "additive-only tail"}
	for i := len(migrations) - 1; i >= 0; i-- {
		if migrations[i].Breaking {
			if i+1 < len(migrations) {
				tail.applied = i + 1
			}
			break
		}
	}
	return []unstampedWindow{{name: "breaking pending", applied: mixed}, tail}
}

// assertUnstampedFacts checks that the refusal reports the manifest it read
// against this binary's migrations: the applied head and the pending tail.
func assertUnstampedFacts(t *testing.T, err error, lead string, applied int) {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("error = %v, want an unstamped migration refusal", err)
	}
	breaking := 0
	for _, m := range migrations[applied:] {
		if m.Breaking {
			breaking++
		}
	}
	head, next := migrations[applied-1], migrations[applied]
	want := fmt.Sprintf("%s at schema version %d (%q); this build would apply %d pending migration(s) from %d %q, %d breaking; the store does not record which release applied its migrations",
		lead, head.Version, head.Name, len(migrations)-applied, next.Version, next.Name, breaking)
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
			if window.applied == 0 {
				t.Skip("the newest migration is breaking; no additive-only tail exists")
			}
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
}

func TestUnstampedUpgradeDoesNotMigrateAnExistingStore(t *testing.T) {
	if version.Value != version.Development {
		t.Skip("binary is release-stamped; the unstamped refusal does not apply")
	}
	for _, window := range unstampedWindows(t) {
		t.Run(window.name, func(t *testing.T) {
			if window.applied == 0 {
				t.Skip("the newest migration is breaking; no additive-only tail exists")
			}
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
func preColumnStore(t *testing.T, path string, applied int) {
	t.Helper()
	db := openMigratedTo(t, path, applied)
	if _, err := db.ExecContext(context.Background(), `ALTER TABLE schema_migrations DROP COLUMN breaking`); err != nil {
		t.Fatalf("stage a pre-column manifest: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertPreColumnRefusal(t *testing.T, err error, path string, applied int) {
	t.Helper()
	assertUnstampedFacts(t, err, "unstamped development build refuses to repair an existing store's schema manifest", applied)
	present, err := columnPresent(context.Background(), openRawDatabase(t, path), "schema_migrations", "breaking")
	if err != nil || present {
		t.Fatalf("breaking column present = %v, %v after the refusal; an unstamped build must repair nothing", present, err)
	}
}

// A manifest that predates the breaking column is refused unrepaired, and the
// refusal still reports the applied head and the pending tail it read.
func TestUnstampedOpenReportsAManifestItRefusesToRepair(t *testing.T) {
	if version.Value != version.Development {
		t.Skip("binary is release-stamped; the unstamped refusal does not apply")
	}
	_, applied := breakingWindow(t)
	path := filepath.Join(t.TempDir(), "pre-column.db")
	preColumnStore(t, path, applied)

	_, err := Open(context.Background(), path)
	assertPreColumnRefusal(t, err, path, applied)
}

// The backup staging copy reaches the same refusal inside its migration
// transaction, on a one-connection pool. The refusal must read the manifest
// through that transaction: a read through the pool would wait for the
// connection the transaction holds, so the deadline turns that regression
// into a failed assertion instead of a hung test.
func TestUnstampedStagingReadsAPreColumnManifestThroughItsTransaction(t *testing.T) {
	useDevelopmentBuild(t)
	_, applied := breakingWindow(t)
	root := t.TempDir()
	source := filepath.Join(root, "pre-column.db")
	preColumnStore(t, source, applied)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := BackupDatabase(ctx, source, filepath.Join(root, "snapshot.db"))
	assertPreColumnRefusal(t, err, source, applied)
}

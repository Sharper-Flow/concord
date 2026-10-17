package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

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

// assertUnstampedCarrier checks that the refusal reports the manifest it read
// against this binary's migrations, without the caller parsing Detail.
func assertUnstampedCarrier(t *testing.T, err error, applied int) *Failure {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) || failure.UnstampedMigration == nil {
		t.Fatalf("error = %v, want an unstamped migration refusal carrying its manifest facts", err)
	}
	breaking := 0
	for _, m := range migrations[applied:] {
		if m.Breaking {
			breaking++
		}
	}
	head, next := migrations[applied-1], migrations[applied]
	want := UnstampedMigrationRefusal{SchemaVersion: head.Version, HeadName: head.Name,
		FirstPending: next.Version, FirstPendingName: next.Name, Pending: len(migrations) - applied, PendingBreaking: breaking}
	if *failure.UnstampedMigration != want {
		t.Fatalf("refusal carrier = %+v, want %+v", *failure.UnstampedMigration, want)
	}
	return failure
}

// assertUnstampedRefusal adds the Detail clause that states what the store
// cannot say: no manifest row names the release that applied it.
func assertUnstampedRefusal(t *testing.T, err error, applied int) {
	t.Helper()
	failure := assertUnstampedCarrier(t, err, applied)
	if !strings.Contains(failure.Detail, "the store does not record which release applied its migrations") {
		t.Fatalf("refusal detail = %q, want the absent writer provenance stated", failure.Detail)
	}
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

// A manifest that predates the breaking column is refused unrepaired, and the
// refusal still reports the applied head and the pending tail it read.
func TestUnstampedOpenReportsAManifestItRefusesToRepair(t *testing.T) {
	if version.Value != version.Development {
		t.Skip("binary is release-stamped; the unstamped refusal does not apply")
	}
	_, applied := breakingWindow(t)
	path := filepath.Join(t.TempDir(), "pre-column.db")
	db := openMigratedTo(t, path, applied)
	if _, err := db.ExecContext(context.Background(), `ALTER TABLE schema_migrations DROP COLUMN breaking`); err != nil {
		t.Fatalf("stage a pre-column manifest: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	_, err := Open(context.Background(), path)
	failure := assertUnstampedCarrier(t, err, applied)
	if !strings.Contains(failure.Detail, "refuses to repair an existing store's schema manifest") {
		t.Fatalf("refusal detail = %q, want the manifest repair refusal", failure.Detail)
	}
	present, err := columnPresent(context.Background(), openRawDatabase(t, path), "schema_migrations", "breaking")
	if err != nil || present {
		t.Fatalf("breaking column present = %v, %v after the refusal; an unstamped build must repair nothing", present, err)
	}
}

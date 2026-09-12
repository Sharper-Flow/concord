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

// An unstamped development build must not migrate a store that already
// carries applied migrations.
func TestUnstampedOpenDoesNotMigrateAnExistingStore(t *testing.T) {
	if version.Value != version.Development {
		t.Skip("binary is release-stamped; the unstamped refusal does not apply")
	}
	_, applied := breakingWindow(t)
	path := filepath.Join(t.TempDir(), "release-owned.db")
	if err := openMigratedTo(t, path, applied).Close(); err != nil {
		t.Fatal(err)
	}

	_, err := Open(context.Background(), path)
	if err == nil {
		t.Fatal("unstamped Open() operated a release-owned store; it must refuse instead")
	}
	if got, want := manifestMax(t, path), applied; got != want {
		t.Fatalf("schema version after unstamped open = %d, want %d: an unstamped build must write nothing", got, want)
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
	_, applied := breakingWindow(t)
	path := filepath.Join(t.TempDir(), "release-owned.db")
	if err := openMigratedTo(t, path, applied).Close(); err != nil {
		t.Fatal(err)
	}

	_, err := Upgrade(context.Background(), path, nil)
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("Upgrade() error = %v, want a typed refusal", err)
	}
	if !strings.Contains(failure.RecoveryAction, "CONCORD_DB_PATH") || !strings.Contains(failure.RecoveryAction, "outside any repository") {
		t.Fatalf("recovery action = %q, want the isolation route", failure.RecoveryAction)
	}
	if !failure.RetrySafe {
		t.Fatal("unstamped migration refusal must be retry-safe")
	}
	if got, want := manifestMax(t, path), applied; got != want {
		t.Fatalf("schema version after unstamped upgrade = %d, want %d", got, want)
	}
}

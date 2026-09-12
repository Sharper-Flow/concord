package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// breakingWindow selects the last breaking migration that an additive run
// precedes, and the number of migrations a database must carry so that run
// and the breaking step are both pending. Open must apply the run and stop
// before the step; the window is derived from the manifest so the test holds
// as migrations accrue.
func breakingWindow(t *testing.T) (breaking migration, applied int) {
	t.Helper()
	for i := len(migrations) - 1; i > 0; i-- {
		if !migrations[i].Breaking || migrations[i-1].Breaking {
			continue
		}
		start := i - 1
		for start > 0 && !migrations[start-1].Breaking {
			start--
		}
		if start == 0 {
			continue
		}
		return migrations[i], start
	}
	t.Fatal("no breaking migration has an additive run before it; the window cannot be built")
	return migration{}, 0
}

func manifestMax(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var version int
	if err := db.QueryRowContext(context.Background(), `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func TestOpenStopsBeforeAPendingBreakingMigration(t *testing.T) {
	useStampedBuild(t)
	breaking, applied := breakingWindow(t)
	path := filepath.Join(t.TempDir(), "older.db")
	if err := openMigratedTo(t, path, applied).Close(); err != nil {
		t.Fatal(err)
	}

	_, err := Open(context.Background(), path)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindUpgradeRequired {
		t.Fatalf("Open() error = %v, want %s", err, KindUpgradeRequired)
	}
	if !strings.Contains(failure.Detail, strconv.Itoa(breaking.Version)) {
		t.Fatalf("refusal %q must name migration %d", failure.Detail, breaking.Version)
	}
	if !strings.Contains(failure.RecoveryAction, "concord upgrade") {
		t.Fatalf("recovery %q must name concord upgrade", failure.RecoveryAction)
	}
	if strings.Contains(failure.Detail+failure.RecoveryAction, "restart") {
		t.Fatalf("refusal %q %q must not ask for a restart", failure.Detail, failure.RecoveryAction)
	}
	if !failure.RetrySafe {
		t.Fatal("a refusal that writes nothing must be retry safe")
	}
	if got, want := manifestMax(t, path), breaking.Version-1; got != want {
		t.Fatalf("schema version after open = %d, want %d: open applies the additive run and stops before the breaking step", got, want)
	}

	// A second open finds the additive run applied and refuses the same way.
	_, err = Open(context.Background(), path)
	assertFailureKind(t, err, KindUpgradeRequired)
	if got, want := manifestMax(t, path), breaking.Version-1; got != want {
		t.Fatalf("schema version after second open = %d, want %d", got, want)
	}
}

func TestOpenAppliesEveryMigrationToAFreshDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got := manifestMax(t, path); got != CurrentSchemaVersion() {
		t.Fatalf("fresh database schema version = %d, want %d", got, CurrentSchemaVersion())
	}
}

func TestUpgradeRefusesWhileALiveLeaseHoldsAnOlderSchema(t *testing.T) {
	useStampedBuild(t)
	breaking, applied := breakingWindow(t)
	path := filepath.Join(t.TempDir(), "older.db")
	if err := openMigratedTo(t, path, applied).Close(); err != nil {
		t.Fatal(err)
	}
	older := HeldSchema{PID: 4242, ReleaseRoot: "/releases/v1.2.3", SchemaVersion: breaking.Version - 1}
	current := HeldSchema{PID: 4343, ReleaseRoot: "/releases/v2.0.0", SchemaVersion: CurrentSchemaVersion()}

	_, err := Upgrade(context.Background(), path, []HeldSchema{current, older})
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindUpgradeBlocked {
		t.Fatalf("Upgrade() error = %v, want %s", err, KindUpgradeBlocked)
	}
	for _, want := range []string{"pid 4242", older.ReleaseRoot, strconv.Itoa(breaking.Version)} {
		if !strings.Contains(failure.Detail, want) {
			t.Fatalf("refusal %q must name %q", failure.Detail, want)
		}
	}
	if strings.Contains(failure.Detail, "pid 4343") {
		t.Fatalf("refusal %q must not name a session on the current schema", failure.Detail)
	}
	if got, want := manifestMax(t, path), applied; got != want {
		t.Fatalf("schema version after refused upgrade = %d, want %d: a refusal writes nothing", got, want)
	}

	report, err := Upgrade(context.Background(), path, []HeldSchema{current})
	if err != nil {
		t.Fatalf("Upgrade() with no older session error = %v", err)
	}
	if report.SchemaVersion != CurrentSchemaVersion() {
		t.Fatalf("report.SchemaVersion = %d, want %d", report.SchemaVersion, CurrentSchemaVersion())
	}
	if len(report.Applied) != CurrentSchemaVersion()-applied || report.Applied[0] != applied+1 {
		t.Fatalf("report.Applied = %v, want versions %d..%d", report.Applied, applied+1, CurrentSchemaVersion())
	}
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() after upgrade error = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	again, err := Upgrade(context.Background(), path, nil)
	if err != nil {
		t.Fatalf("Upgrade() on a current database error = %v", err)
	}
	if len(again.Applied) != 0 {
		t.Fatalf("a current database applied %v, want nothing", again.Applied)
	}
}

func TestUpgradeRepairsAManifestPredatingTheBreakingColumn(t *testing.T) {
	useStampedBuild(t)
	_, applied := breakingWindow(t)
	path := filepath.Join(t.TempDir(), "pre-column.db")
	db := openMigratedTo(t, path, applied)
	if _, err := db.ExecContext(context.Background(), `ALTER TABLE schema_migrations DROP COLUMN breaking`); err != nil {
		t.Fatalf("stage a pre-column manifest: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// The gate still holds: the repaired manifest admits a refused upgrade
	// under an older live session before anything applies.
	older := HeldSchema{PID: 4747, ReleaseRoot: "/releases/v0.13.0", SchemaVersion: applied - 1}
	if _, err := Upgrade(context.Background(), path, []HeldSchema{older}); err == nil {
		t.Fatal("upgrade applied a pending breaking step under a session that predates it")
	}

	report, err := Upgrade(context.Background(), path, nil)
	if err != nil {
		t.Fatalf("Upgrade() after the column repair error = %v", err)
	}
	if report.SchemaVersion != CurrentSchemaVersion() {
		t.Fatalf("report.SchemaVersion = %d, want %d", report.SchemaVersion, CurrentSchemaVersion())
	}
	if got := manifestMax(t, path); got != CurrentSchemaVersion() {
		t.Fatalf("schema version after upgrade = %d, want %d", got, CurrentSchemaVersion())
	}
}

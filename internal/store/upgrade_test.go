package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestSupportedMigrationFixture(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.db")
	if err := openMigratedTo(t, path, 199).Close(); err != nil {
		t.Fatal(err)
	}
	if got := manifestMax(t, path); got != 199 {
		t.Fatalf("fixture schema = %d, want 199", got)
	}
}

// breakingWindow selects the first pending breaking migration above the supported floor.
func breakingWindow(t *testing.T) (breaking migration, applied int) {
	t.Helper()
	for _, m := range migrations {
		if m.Version > 199 && m.Breaking {
			return m, 199
		}
	}
	t.Fatal("no breaking migration follows the supported floor")
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
	if got, want := manifestMax(t, path), applied; got != want {
		t.Fatalf("schema version after open = %d, want %d: open must not apply the breaking step", got, want)
	}

	// A second open refuses the same way without changing the manifest.
	_, err = Open(context.Background(), path)
	assertFailureKind(t, err, KindUpgradeRequired)
	if got, want := manifestMax(t, path), applied; got != want {
		t.Fatalf("schema version after second open = %d, want %d", got, want)
	}
}

func TestOpenAppliesEveryMigrationToAFreshDatabase(t *testing.T) {
	t.Parallel()
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
	older := HeldSchema{PID: 4242, ReleaseRoot: "/releases/v1.2.3", SchemaVersion: breaking.Version - 1, Directory: "/workspace/card-site"}
	current := HeldSchema{PID: 4343, ReleaseRoot: "/releases/v2.0.0", SchemaVersion: CurrentSchemaVersion()}

	_, err := Upgrade(context.Background(), path, []HeldSchema{current, older})
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindUpgradeBlocked {
		t.Fatalf("Upgrade() error = %v, want %s", err, KindUpgradeBlocked)
	}
	for _, want := range []string{"pid 4242", older.ReleaseRoot, strconv.Itoa(breaking.Version), older.Directory} {
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
	// The applied tail is the migration versions this binary defines above
	// the applied stamp — derived from the schema, never from a contiguous
	// range, so out-of-order version arrivals across branches stay honest.
	want := []int{}
	for _, m := range migrations {
		if m.Version > applied {
			want = append(want, m.Version)
		}
	}
	if !slices.Equal(report.Applied, want) {
		t.Fatalf("report.Applied = %v, want versions %v", report.Applied, want)
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

func TestUpgradeBlocksWhenOlderSchemaAppearsAfterEmptyManifestProbe(t *testing.T) {
	useStampedBuild(t)
	path := filepath.Join(t.TempDir(), "concord.db")
	hits := 0
	ctx := context.WithValue(context.Background(), schemaAbsentHookKey{}, func(probe string) {
		if probe != "migrationManifestCurrent" {
			return
		}
		hits++
		if err := openMigratedTo(t, path, 199).Close(); err != nil {
			t.Fatalf("close initializer: %v", err)
		}
	})
	report, err := Upgrade(ctx, path, []HeldSchema{{PID: 4242, SchemaVersion: 199}})
	if hits != 1 {
		t.Fatalf("migrationManifestCurrent probe reached %d times, want 1; Upgrade() error = %v", hits, err)
	}
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindUpgradeBlocked {
		t.Errorf("Upgrade() error = %v, report = %+v; want %s", err, report, KindUpgradeBlocked)
	}
	if len(report.Applied) != 0 {
		t.Errorf("Upgrade() applied = %v, want none", report.Applied)
	}
	if got := manifestMax(t, path); got != 199 {
		t.Errorf("manifest version = %d, want 199", got)
	}
}

func TestUpgradeMigratesOlderSchemaAppearingAfterEmptyManifestProbeWithoutHolders(t *testing.T) {
	useStampedBuild(t)
	path := filepath.Join(t.TempDir(), "concord.db")
	hits := 0
	ctx := context.WithValue(context.Background(), schemaAbsentHookKey{}, func(probe string) {
		if probe != "migrationManifestCurrent" {
			return
		}
		hits++
		if err := openMigratedTo(t, path, 199).Close(); err != nil {
			t.Fatalf("close initializer: %v", err)
		}
	})
	report, err := Upgrade(ctx, path, nil)
	if hits != 1 || err != nil {
		t.Fatalf("probe hits = %d, Upgrade() error = %v", hits, err)
	}
	want, current := migrationVersionsAfter(199), CurrentSchemaVersion()
	if !slices.Equal(report.Applied, want) || report.SchemaVersion != current || manifestMax(t, path) != current {
		t.Errorf("Upgrade() report = %+v, want migrations %v at schema %d", report, want, current)
	}
}

func TestUpgradeFreshStoreIgnoresOlderHeldSchema(t *testing.T) {
	useStampedBuild(t)
	path := filepath.Join(t.TempDir(), "concord.db")
	held := []HeldSchema{{PID: 4242, SchemaVersion: 199}}
	report, err := Upgrade(context.Background(), path, held)
	if err != nil || report.SchemaVersion != CurrentSchemaVersion() || len(report.Applied) == 0 {
		t.Fatalf("fresh Upgrade() = %+v, error = %v; want initialized store", report, err)
	}
	report, err = Upgrade(context.Background(), path, held)
	if err != nil || report.SchemaVersion != CurrentSchemaVersion() || len(report.Applied) != 0 {
		t.Fatalf("current Upgrade() = %+v, error = %v; want no migrations", report, err)
	}
}

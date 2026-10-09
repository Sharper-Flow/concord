package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/sharper-flow/concord/internal/version"
)

// stampAsRelease pins the build as a stamped release for one test, the way
// unstamped_open_test.go does, so Upgrade exercises the release migration
// path instead of the development-build isolation rule (CD-0139).
func stampAsRelease(t *testing.T) {
	t.Helper()
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
}

// migratedStoreWithTailRemoved builds a real, fully migrated store and then
// removes the manifest rows from `from` on. The schema objects those steps
// created stay behind, so re-applying the tail collides with them: a
// deterministic, synthetic pending tail whose migration fails exactly where
// the collision sits.
func migratedStoreWithTailRemoved(t *testing.T, from int) string {
	t.Helper()
	versions := []int{}
	for _, migration := range migrations {
		if migration.Version >= from {
			versions = append(versions, migration.Version)
		}
	}
	return migratedStoreWithVersionsRemoved(t, versions...)
}

func migratedStoreWithVersionsRemoved(t *testing.T, versions ...int) string {
	t.Helper()
	path := storePathForTest(t)
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, version := range versions {
		if _, err := db.ExecContext(context.Background(), `DELETE FROM schema_migrations WHERE version=?`, version); err != nil {
			t.Fatalf("cannot remove manifest step %d: %v", version, err)
		}
	}
	_ = db.Close()
	return path
}

func storePathForTest(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "store.db")
}

// A migration whose SQL collides with the live schema fails inside its one
// transaction and commits nothing: the manifest keeps recording the exact
// applied set it had, the store still reads, and the active release keeps
// operating it. This is the failure half of CON-807 recovery: nothing about
// a failed migration damages the usable active release (CON-807).
func TestUpgradeFailureCommitsNothingAndKeepsTheStoreReadable(t *testing.T) {
	stampAsRelease(t)
	path := migratedStoreWithTailRemoved(t, 112)
	held := []HeldSchema{}

	report, err := Upgrade(context.Background(), path, held)
	if err == nil {
		t.Fatalf("a migration colliding with the live schema must fail, got %+v", report)
	}
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindUnavailable {
		t.Fatalf("a failed migration must refuse with unavailable, got %v", err)
	}
	// The failed transaction committed nothing: the manifest still stops
	// before 112 and the pending tail is unchanged.
	plan, planErr := PlanUpgradeReadiness(context.Background(), path)
	if planErr != nil {
		t.Fatalf("the store must still read after a failed migration: %v", planErr)
	}
	for _, applied := range plan.AppliedVersions {
		if applied >= 112 {
			t.Fatalf("a failed migration committed step %d: %+v", applied, plan.AppliedVersions)
		}
	}
	if len(plan.Pending) == 0 || plan.ActivationBlocked && len(plan.PendingBreaking) == 0 {
		t.Fatalf("the pending tail changed after a failed migration: %+v", plan)
	}
}

// The resume half: once the collision is repaired, the same command applies
// the tail. An interrupted or failed migration is retried by running it
// again; no intermediate state survives the failed transaction.
func TestUpgradeResumesAfterTheCollisionIsRepaired(t *testing.T) {
	stampAsRelease(t)
	path := migratedStoreWithTailRemoved(t, 115)
	if _, err := Upgrade(context.Background(), path, nil); err == nil {
		t.Fatal("the poisoned tail must fail the first upgrade")
	}
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Remove the colliding objects; the pending migrations recreate them
	// with their checksummed definitions. The v119 worker_attempts rebuild
	// needs no drop: it renames and recreates, so its re-run is shaped by its
	// own DDL whether or not the objects already exist. Migration 120's
	// maintenance triggers attach to pre-existing tables, so they drop by
	// name before its tables do. Migration 121's ALTER TABLE refuses a
	// duplicate column, so dropMigration121Objects restores the pre-step
	// workflow_instances shape beside dropping its tables. The guarded
	// worktree_ref_outcomes projection is a plain CREATE TABLE, so its
	// object drops with the rest through its own helper.
	if _, err := db.ExecContext(context.Background(), `DROP TABLE project_handoffs; DROP TABLE durability_commits; DROP TABLE runtime_state_writers; DROP TABLE worker_job_revisions;`); err != nil {
		t.Fatalf("cannot drop the colliding table: %v", err)
	}
	if err := dropMigration120Objects(context.Background(), db); err != nil {
		t.Fatalf("cannot drop migration 120's colliding objects: %v", err)
	}
	if err := dropMigration121Objects(context.Background(), db); err != nil {
		t.Fatalf("cannot drop migration 121's colliding objects: %v", err)
	}
	if err := dropMigration122Objects(context.Background(), db); err != nil {
		t.Fatalf("cannot drop migration 122's colliding objects: %v", err)
	}
	_ = db.Close()
	report, err := Upgrade(context.Background(), path, nil)
	if err != nil {
		t.Fatalf("the repaired tail must apply: %v", err)
	}
	// The reapplied tail is exactly the migration versions this branch's
	// schema holds at and above the removed stamp — derived from the schema,
	// never hand-listed, so a renumbered or inserted migration cannot leave
	// the fixture pinning a stale ordering.
	want := []int{}
	for _, m := range migrations {
		if m.Version >= 115 {
			want = append(want, m.Version)
		}
	}
	if len(report.Applied) != len(want) || report.SchemaVersion != CurrentSchemaVersion() {
		t.Fatalf("the repaired tail must reapply exactly the removed steps: applied %+v want %+v, schema %d", report.Applied, want, report.SchemaVersion)
	}
	for i := range want {
		if report.Applied[i] != want[i] {
			t.Fatalf("the repaired tail must reapply in schema order: applied %+v want %+v", report.Applied, want)
		}
	}
	plan, err := PlanUpgradeReadiness(context.Background(), path)
	if err != nil {
		t.Fatalf("plan after resume: %v", err)
	}
	if plan.ActivationBlocked || len(plan.Pending) != 0 {
		t.Fatalf("a resumed store must read clean: %+v", plan)
	}
}

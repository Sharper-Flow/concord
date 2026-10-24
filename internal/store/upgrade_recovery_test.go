package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

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
	useStampedBuild(t)
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
	useStampedBuild(t)
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
	// object drops with the rest through its own helper. Migration 124 drops
	// the planning mirror, so undoMigration124 restores the pre-step schema
	// first: it recreates every pre-124 object the store lacks, so it must
	// run before the earlier steps' objects drop.
	if _, err := db.ExecContext(context.Background(), `
DROP TABLE workflow_phase_exits;
DROP TABLE workflow_action_obligations;
DROP TABLE workflow_worker_actors;
DROP TABLE workflow_step_completions;
DROP TABLE workflow_correction_state;
DROP TABLE workflow_predicate_verdicts;
DROP TABLE workflow_delivery_state;
DROP TABLE compaction_publications;
DROP TABLE workflow_evidence_bindings;
DROP TABLE workflow_staleness_observations;
ALTER TABLE workflow_contracts DROP COLUMN supersession_seq;
ALTER TABLE workflow_instances DROP COLUMN completion_payload;
ALTER TABLE workflow_instances DROP COLUMN completion_occurred_at;
ALTER TABLE workflow_instances DROP COLUMN unhealthy_verdict_seq;
ALTER TABLE work_observations DROP COLUMN recorded_seq;
ALTER TABLE workflow_step_epochs DROP COLUMN latest_start_seq;
ALTER TABLE workflow_step_epochs DROP COLUMN latest_start_event_id;
ALTER TABLE workflow_step_epochs DROP COLUMN latest_start_payload;
ALTER TABLE workflow_step_epochs DROP COLUMN latest_dispatch_start_seq;
ALTER TABLE workflow_step_epochs DROP COLUMN latest_dispatch_start_event_id;
ALTER TABLE workflow_step_epochs DROP COLUMN latest_dispatch_start_payload;
`); err != nil {
		t.Fatalf("cannot restore the pre-142 workflow evidence facts: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), workerCurrentFactsFixtureReversal); err != nil {
		t.Fatalf("cannot restore the pre-139 worker facts: %v", err)
	}
	if err := dropMigration125Objects(context.Background(), db); err != nil {
		t.Fatalf("cannot restore the pre-125 verify lease: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `ALTER TABLE worktree_verify_leases DROP COLUMN event_watermark`); err != nil {
		t.Fatalf("cannot restore the pre-127 verify lease: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `ALTER TABLE workflow_contracts DROP COLUMN law_change`); err != nil {
		t.Fatalf("cannot restore the pre-135 workflow contract: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `ALTER TABLE workflow_contracts DROP COLUMN decision_domains`); err != nil {
		t.Fatalf("cannot restore the pre-133 workflow contract: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `ALTER TABLE project_locators DROP COLUMN normalization_version`); err != nil {
		t.Fatalf("cannot restore the pre-remote-identity locator shape: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `DROP TABLE knowledge_package_domains; DROP TABLE law_coverage_anchors; DROP TABLE law_evidence_paths;`); err != nil {
		t.Fatalf("cannot restore the pre-package-map schema: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `ALTER TABLE law_relations DROP COLUMN declarer_law_id`); err != nil {
		t.Fatalf("cannot restore the pre-declarer law relation shape: %v", err)
	}
	if err := undoMigration124(t, context.Background(), db); err != nil {
		t.Fatalf("cannot restore the pre-124 schema: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `DROP TABLE project_handoffs; DROP TABLE durability_commits; DROP TABLE runtime_state_writers; DROP TABLE worker_job_revisions; DROP TABLE work_size_overrides; DROP TABLE work_size_assessments; DROP TABLE store_revision;`); err != nil {
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
	if err := dropMigration123Objects(context.Background(), db); err != nil {
		t.Fatalf("cannot drop migration 123's colliding objects: %v", err)
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

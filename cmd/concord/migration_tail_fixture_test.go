package main

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/version"
)

func TestMigrationTailFixtureRejectsUnknownStepBeforeChangingStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	s, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", store.SQLiteFileURI(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	missing := store.CurrentSchemaVersion() + 1
	if _, err := db.Exec(`INSERT INTO schema_migrations(version,name,checksum,applied_at,breaking)
SELECT ?, 'fixture_unmapped', checksum, applied_at, breaking FROM schema_migrations ORDER BY version DESC LIMIT 1`, missing); err != nil {
		t.Fatal(err)
	}
	var before, after string
	const snapshot = `SELECT json_array(
    (SELECT json_group_array(sql) FROM (SELECT sql FROM sqlite_schema ORDER BY type,name)),
    (SELECT json_group_array(version) FROM (SELECT version FROM schema_migrations ORDER BY version)))`
	if err := db.QueryRow(snapshot).Scan(&before); err != nil {
		t.Fatal(err)
	}
	err = removeMigrationManifestTail(db, 111)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("unknown fixture migration %d", missing)) {
		t.Fatalf("unknown migration must refuse before changing the manifest, got %v", err)
	}
	if err := db.QueryRow(snapshot).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("missing reversal changed the schema or manifest")
	}
}

func TestMigrationTailFixtureReappliesEveryDeclaredStep(t *testing.T) {
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
	path := filepath.Join(t.TempDir(), "store.db")
	pendingBreakingStore(t, path, true)
	if _, err := store.Upgrade(context.Background(), path, nil); err != nil {
		t.Fatalf("reapply the complete migration tail: %v", err)
	}
	plan, err := store.PlanUpgradeReadiness(context.Background(), path)
	if err != nil || len(plan.Pending) != 0 {
		t.Fatalf("reapplied tail remains pending: %+v, %v", plan, err)
	}
}

func TestMigrationTailFixtureRejectsMissingMiddleBeforeChangingStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	seedCLIMigrationPrefix(t, path, 142)
	db, err := sql.Open("sqlite", store.SQLiteFileURI(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version=111`); err != nil {
		t.Fatal(err)
	}
	before := releaseMigrationSnapshot(t, path)
	if err := removeMigrationManifestTail(db, 0); err == nil || !strings.Contains(err.Error(), "missing migration 111") {
		t.Fatalf("incomplete manifest must refuse before changing the store: %v", err)
	}
	if after := releaseMigrationSnapshot(t, path); after != before {
		t.Fatal("incomplete manifest changed schema or rows")
	}
}

func TestCLIMigrationPrefixRetainsRealHistoricalSchemaAndFloor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	seedCLIMigrationPrefix(t, path, 150)
	db, err := sql.Open("sqlite", store.SQLiteFileURI(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count, floor int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type='table' AND name IN ('domain_events','fold_guard')`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("real pre-151 tables=%d, want both historical tables: %v", count, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type='table' AND name='write_guard'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("prefix unexpectedly contains the 151 guard: count=%d err=%v", count, err)
	}
	if err := db.QueryRow(`SELECT max(version) FROM schema_migrations WHERE breaking=1`).Scan(&floor); err != nil {
		t.Fatal(err)
	}
	plan, err := store.PlanUpgradeReadiness(context.Background(), path)
	if err != nil || plan.SchemaVersion != 150 || plan.CompatibilityFloor != floor || len(plan.PendingBreaking) == 0 || plan.PendingBreaking[0].Version != 151 {
		t.Fatalf("prefix lost its compatibility floor or complete breaking tail: %+v, %v", plan, err)
	}
}

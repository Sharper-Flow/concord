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

func TestMigrationTailFixtureRejectsMissingReversalBeforeChangingStore(t *testing.T) {
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
	err = rewindMigrationTail(db, 111, true)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("missing fixture reversal for migration %d", missing)) {
		t.Fatalf("unmapped migration must refuse before reversal, got %v", err)
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

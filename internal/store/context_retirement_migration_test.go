package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func assertContextRetired(t *testing.T, db *sql.DB) {
	t.Helper()
	rows := retirementUpgradeRows(t, db, `SELECT type,name FROM sqlite_schema WHERE tbl_name IN ('workflow_context_checkpoints','workflow_context_boundaries') ORDER BY type,name`)
	if len(rows) != 0 {
		t.Fatalf("context tables or their indexes and triggers remain: %v", rows)
	}
}

func TestContextRetirementPopulatedUpgrade(t *testing.T) {
	db := checkpointV168(t)
	advanceHistoricalPrefix(t, db, migrationPrefixBefore(t, "context_continuity_retirement"))
	for _, query := range []string{retainedContextCheckpoints, retainedContextBoundaries} {
		if rows := retirementUpgradeRows(t, db, query); len(rows) != 1 {
			t.Fatalf("predecessor rows missing: %v", rows)
		}
	}
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	assertContextRetired(t, db)
	retirementUpgradeHealthy(t, db)
}

func TestContextRetirementEmptyUpgrade(t *testing.T) {
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "empty.db"), migrationPrefixBefore(t, "context_continuity_retirement"))
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	assertContextRetired(t, db)
	retirementUpgradeHealthy(t, db)
}

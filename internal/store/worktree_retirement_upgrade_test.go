package store

import (
	"database/sql"
	"testing"
)

var retiredWorktreeTables = []string{
	"bootstrap_operations", "worktree_claim_native_fences", "worktree_claims",
	"worktree_entries", "worktree_occupancy", "worktree_ref_outcomes",
	"session_vacate_requests", "session_claim_landings", "session_relocation_counts",
}

func assertWorktreeTablesAbsent(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range retiredWorktreeTables {
		if got := retirementUpgradeRows(t, db, `SELECT name FROM sqlite_schema WHERE name=? OR tbl_name=?`, table, table); len(got) != 0 {
			t.Errorf("retired ownership schema for %s survives: %v", table, got)
		}
	}
}

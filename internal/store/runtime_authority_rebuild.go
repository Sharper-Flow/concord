package store

import (
	"context"
	"database/sql"
)

// rebuildSnapshotTables are direct-authority tables the event log cannot
// restore, whose RESTRICT foreign keys to work_items would otherwise block
// the projection clear. The rebuild snapshots their rows into temp tables,
// deletes them so the clear can proceed, and restores them byte-for-byte
// after the replay — the same treatment active research receives in
// research_rebuild.go.
//
// linear_outbox is deliberately absent from both this set and the clear list:
// it carries no foreign key, blocks no clear, and a queued row is pending
// Linear work no event can restore, so a rebuild must never touch it.
var rebuildSnapshotTables = []string{
	"bootstrap_operations",
	"linear_outbox_dispositions",
	"worktree_verify_leases",
}

func snapshotRuntimeAuthorityForRebuild(ctx context.Context, tx *sql.Tx) error {
	for _, table := range rebuildSnapshotTables {
		if _, err := tx.ExecContext(ctx, "DROP TABLE IF EXISTS temp.rebuild_authority_"+table); err != nil {
			return wrapFailure(KindUnavailable, "rebuild_from_log", "cannot reset runtime authority rebuild snapshot", true,
				"retry once the database is writable", err)
		}
		if _, err := tx.ExecContext(ctx, "CREATE TEMP TABLE temp.rebuild_authority_"+table+" AS SELECT * FROM "+table); err != nil { //nolint:gosec // table comes only from the closed snapshot list above and no values are interpolated.
			return wrapFailure(KindUnavailable, "rebuild_from_log", "cannot snapshot runtime authority table "+table, true,
				"retry once the database is writable", err)
		}
	}
	for _, table := range rebuildSnapshotTables {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table); err != nil { //nolint:gosec // table comes only from the closed snapshot list above and no values are interpolated.
			return wrapFailure(KindUnavailable, "rebuild_from_log", "cannot stage runtime authority table "+table+" for the projection rebuild", true,
				"retry once the database is writable", err)
		}
	}
	return nil
}

func restoreRuntimeAuthorityAfterRebuild(ctx context.Context, tx *sql.Tx) error {
	for _, table := range rebuildSnapshotTables {
		if _, err := tx.ExecContext(ctx, "INSERT INTO "+table+" SELECT * FROM temp.rebuild_authority_"+table); err != nil { //nolint:gosec // table comes only from the closed snapshot list above and no values are interpolated.
			return wrapFailure(KindUnavailable, "rebuild_from_log", "cannot restore runtime authority table "+table+" after the projection rebuild", true,
				"retry once the database is writable", err)
		}
	}
	return nil
}

func dropRuntimeAuthorityRebuildSnapshot(ctx context.Context, tx *sql.Tx) error {
	for _, table := range rebuildSnapshotTables {
		if _, err := tx.ExecContext(ctx, "DROP TABLE IF EXISTS temp.rebuild_authority_"+table); err != nil {
			return wrapFailure(KindUnavailable, "rebuild_from_log", "cannot remove runtime authority rebuild snapshot", true,
				"retry once the database is writable", err)
		}
	}
	return nil
}

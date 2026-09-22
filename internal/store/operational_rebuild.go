package store

import (
	"context"
	"database/sql"
)

// Rebuild snapshot authority for direct-table state the event log never
// restores. bootstrap_operations, worktree_verify_leases, and
// linear_outbox_dispositions record live operations; domains and
// domain_registries are Git-derived knowledge projections. All five hold
// foreign keys into fold projections (work_items, products, projects), so the
// rebuild stages their rows in temporary tables before the projection clears
// and restores them byte-for-byte once the replay has rebuilt every
// referenced projection. The snapshot runs under the fold guard, which the
// fold-only delete triggers on these tables require.
var operationalRebuildTables = []string{
	"bootstrap_operations",
	"worktree_verify_leases",
	"linear_outbox_dispositions",
	"domains",
	"domain_registries",
}

func snapshotOperationalAuthorityForRebuild(ctx context.Context, tx *sql.Tx) error {
	for _, table := range operationalRebuildTables {
		if _, err := tx.ExecContext(ctx, "DROP TABLE IF EXISTS temp.rebuild_operational_"+table); err != nil { //nolint:gosec // table comes only from the closed snapshot list above and no values are interpolated.
			return wrapFailure(KindUnavailable, "rebuild_from_log", "cannot reset the operational rebuild snapshot for "+table, true, "retry once the database is writable", err)
		}
		if _, err := tx.ExecContext(ctx, "CREATE TEMP TABLE rebuild_operational_"+table+" AS SELECT * FROM "+table); err != nil { //nolint:gosec // see above.
			return wrapFailure(KindUnavailable, "rebuild_from_log", "cannot snapshot direct-authority "+table, true, "retry once the database is writable", err)
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table); err != nil { //nolint:gosec // see above.
			return wrapFailure(KindUnavailable, "rebuild_from_log", "cannot stage direct-authority "+table+" for projection rebuild", true, "retry once the database is writable", err)
		}
	}
	return nil
}

func restoreOperationalAuthorityAfterRebuild(ctx context.Context, tx *sql.Tx) error {
	for _, table := range operationalRebuildTables {
		if _, err := tx.ExecContext(ctx, "INSERT INTO "+table+" SELECT * FROM temp.rebuild_operational_"+table); err != nil { //nolint:gosec // see above.
			return wrapFailure(KindUnavailable, "rebuild_from_log", "cannot restore direct-authority "+table+" after projection rebuild", true, "retry once the database is writable", err)
		}
	}
	return nil
}

func dropOperationalAuthorityRebuildSnapshot(ctx context.Context, tx *sql.Tx) error {
	for _, table := range operationalRebuildTables {
		if _, err := tx.ExecContext(ctx, "DROP TABLE IF EXISTS temp.rebuild_operational_"+table); err != nil { //nolint:gosec // see above.
			return wrapFailure(KindUnavailable, "rebuild_from_log", "cannot remove the operational rebuild snapshot for "+table, true, "retry once the database is writable", err)
		}
	}
	return nil
}

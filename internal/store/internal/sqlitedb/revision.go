package sqlitedb

import (
	"context"
	"database/sql"
)

// Revision reads the publication revision from the caller's snapshot.
func Revision(ctx context.Context, q Queryer) (int64, error) {
	var revision int64
	if err := q.QueryRowContext(ctx, `SELECT revision FROM store_revision WHERE id=1`).Scan(&revision); err != nil {
		return 0, failure(Unavailable, "store_revision", "cannot read the store revision", true, "confirm the migrated store is readable", err)
	}
	return revision, nil
}

// AdvanceRevision stays in the admitted write's transaction. Unequal revisions
// mean possibly changed state, including a write that changes no domain row.
func AdvanceRevision(ctx context.Context, tx *sql.Tx) error {
	result, err := tx.ExecContext(ctx, `UPDATE store_revision SET revision=revision+1 WHERE id=1`)
	if err != nil {
		return failure(Unavailable, "store_revision", "cannot advance the store revision", true, "confirm the migrated store is writable", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return failure(Unavailable, "store_revision", "cannot confirm the store revision advance", true, "confirm the migrated store is writable", err)
	}
	if rows != 1 {
		return failure(InvariantViolation, "store_revision", "store revision singleton is missing", false, "restore a verified store snapshot", nil)
	}
	return nil
}

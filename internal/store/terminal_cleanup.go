package store

import (
	"context"
	"database/sql"
)

// refusePublishedWorkTx fences a work identity whose canonical outcome note is
// indexed from Git but whose live row is gone. Terminal cleanup deletes the
// execution state, including idempotency records, so a late retry would
// otherwise recreate the identity. Git, not a retained SQLite tombstone, is the
// proof the identity is closed.
func refusePublishedWorkTx(ctx context.Context, q queryer, workID, operation string) error {
	var found int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM archived_work WHERE id=? AND type='work_note' AND NOT EXISTS (SELECT 1 FROM work_items WHERE id=?) LIMIT 1`, workID, workID).Scan(&found)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return wrapFailure(KindUnavailable, operation, "cannot inspect published work identity", true, "retry once the knowledge index is readable", err)
	}
	return newFailure(KindProjectionNotFound, operation, "work identity is closed by its published outcome note and cannot be resumed", false, "read the canonical outcome note or start fresh work")
}

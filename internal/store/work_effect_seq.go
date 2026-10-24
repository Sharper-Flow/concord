package store

import (
	"context"
	"database/sql"
)

// advanceWorkEffectSeqTx moves work_items.effect_seq to the effect sequence of
// a work-item event that was just appended and folded in tx. The column is the
// work item's effect frontier: the highest effect sequence written against the
// work item. Readers that need "has anything about this work item changed"
// read it instead of scanning the event log.
//
// A work.removed fold deletes the row, so the update then touches nothing.
func advanceWorkEffectSeqTx(ctx context.Context, tx *sql.Tx, event Event) error {
	if event.SubjectType != SubjectWorkItem {
		return nil
	}
	if event.Seq <= 0 {
		return newFailure(KindInvariantViolation, "apply_operation",
			"work event "+event.EventID+" reached the effect frontier without its effect sequence", false,
			"pass the appended sequence to the effect frontier")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE work_items SET effect_seq=? WHERE id=? AND effect_seq<?`, event.Seq, event.SubjectID, event.Seq); err != nil {
		return wrapFailure(KindUnavailable, "apply_operation", "cannot advance the work effect frontier", true,
			"retry once the database is writable", err)
	}
	return nil
}

// readWorkEffectSeq returns a work item's effect frontier, zero when the work
// item is absent.
func readWorkEffectSeq(ctx context.Context, q queryer, workID string) (int64, error) {
	var seq int64
	err := q.QueryRowContext(ctx, `SELECT effect_seq FROM work_items WHERE id=?`, workID).Scan(&seq)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return seq, err
}

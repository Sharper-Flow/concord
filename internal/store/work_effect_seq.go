package store

import (
	"context"
	"database/sql"
	"time"
)

// advanceWorkEffectSeqTx moves work_items.effect_seq to the effect sequence of
// a work-item event that was just appended and folded in tx. The column is the
// work item's effect frontier: the highest effect sequence written against the
// work item. last_progress_at separately retains the lexical maximum stored
// timestamp of non-message, non-overlap effects; it is not an ordering clock.
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
	progress := ""
	switch event.Kind {
	case "work.message_sent", "work.message_withdrawn", "workflow.overlap_resolved":
	default:
		progress = event.OccurredAt.UTC().Format(time.RFC3339Nano)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE work_items SET effect_seq=MAX(effect_seq,?),last_progress_at=MAX(last_progress_at,?) WHERE id=?`, event.Seq, progress, event.SubjectID); err != nil {
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

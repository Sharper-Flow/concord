package store

import (
	"context"
	"database/sql"
	"fmt"
)

// Admission facts cannot invent a class for a corrupt completion. Inspect the
// entire backfill population before its migration changes schema or rows.
func workflowAdmissionFactsMigrationReady(ctx context.Context, q queryer) (bool, error) {
	var work, effect string
	var seq int64
	err := q.QueryRowContext(ctx, `SELECT e.subject_id,e.event_id,e.seq
FROM domain_events e JOIN work_items w ON w.id=e.subject_id
WHERE e.subject_type='work_item' AND e.kind='workflow.action_completed'
AND (json_extract(e.payload,'$.step_id') IS NULL OR json_extract(e.payload,'$.action_id') IS NULL)
ORDER BY e.seq LIMIT 1`).Scan(&work, &effect, &seq)
	if err == sql.ErrNoRows {
		return true, nil
	}
	if err != nil {
		return false, wrapFailure(KindUnavailable, "migrate", "cannot inspect admission fact backfill sources", false, "inspect the source store before retrying the upgrade", err)
	}
	return false, newFailure(KindInvariantViolation, "migrate",
		fmt.Sprintf("work %q completion %q at sequence %d has a NULL step or action", work, effect, seq),
		false, "inspect the named corrupt row and restore a verified backup before retrying the upgrade")
}

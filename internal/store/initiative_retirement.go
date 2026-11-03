package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

func retireInitiativeWork(ctx context.Context, q migrationRunner) error {
	checks := []struct{ table, query string }{
		{"active_research_packs", `SELECT owner_work_id,owner_work_id FROM active_research_packs WHERE owner_work_id IN (SELECT id FROM work_items WHERE kind='initiative') ORDER BY owner_work_id LIMIT 1`},
		{"active_research_consumers", `SELECT consumer_work_id,consumer_work_id FROM active_research_consumers WHERE consumer_work_id IN (SELECT id FROM work_items WHERE kind='initiative') ORDER BY consumer_work_id LIMIT 1`},
		{"workflow_impact_edges", `SELECT target_work_id,work_id FROM workflow_impact_edges WHERE target_work_id IN (SELECT id FROM work_items WHERE kind='initiative') AND work_id NOT IN (SELECT id FROM work_items WHERE kind='initiative') ORDER BY target_work_id,work_id LIMIT 1`},
		{"workflow_backlog_alignment", `SELECT related_work_id,work_id FROM workflow_backlog_alignment WHERE related_work_id IN (SELECT id FROM work_items WHERE kind='initiative') AND work_id NOT IN (SELECT id FROM work_items WHERE kind='initiative') ORDER BY related_work_id,work_id LIMIT 1`},
		{"workflow_overlap_resolutions", `SELECT to_work_id,from_work_id FROM workflow_overlap_resolutions WHERE to_work_id IN (SELECT id FROM work_items WHERE kind='initiative') AND from_work_id NOT IN (SELECT id FROM work_items WHERE kind='initiative') ORDER BY to_work_id,from_work_id LIMIT 1`},
	}
	for _, check := range checks {
		var initiative, owner string
		err := q.QueryRowContext(ctx, check.query).Scan(&initiative, &owner)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("initiative retirement: inspect %s: %w", check.table, err)
		}
		return newFailure(KindUpgradeBlocked, "migrate",
			fmt.Sprintf("initiative retirement: %s contains a research or foreign-owned dependency for initiative %s, owner work %s; no migration was applied", check.table, initiative, owner), false,
			"keep the installed release and report the named dependency to the Concord maintainer before retrying concord upgrade")
	}
	rows, err := q.QueryContext(ctx, `SELECT id FROM work_items WHERE kind='initiative' ORDER BY id`)
	if err != nil {
		return fmt.Errorf("initiative retirement: list work: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		return fmt.Errorf("initiative retirement: open write guard: %w", err)
	}
	for _, id := range ids {
		if err := deleteWorkOwnedProjections(ctx, q, id); err != nil {
			return fmt.Errorf("initiative retirement: %s: %w", id, err)
		}
	}
	if _, err := q.ExecContext(ctx, `DELETE FROM write_guard`); err != nil {
		return fmt.Errorf("initiative retirement: close write guard: %w", err)
	}
	return nil
}

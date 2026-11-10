package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

func retireInitiativeWork(ctx context.Context, q migrationRunner) error {
	checks := []struct{ table, query string }{
		{"workflow_impact_edges", `SELECT target_work_id,work_id FROM workflow_impact_edges WHERE target_work_id IN (SELECT id FROM work_items WHERE kind='initiative') AND work_id NOT IN (SELECT id FROM work_items WHERE kind='initiative') ORDER BY target_work_id,work_id LIMIT 1`},
		{"workflow_backlog_alignment", `SELECT alignment.related_work_id,alignment.work_id
FROM workflow_backlog_alignment alignment JOIN work_items owner ON owner.id=alignment.work_id
WHERE alignment.related_work_id IN (SELECT id FROM work_items WHERE kind='initiative')
  AND owner.kind<>'initiative'
  AND (owner.lifecycle NOT IN ('completed','cancelled','superseded')
    OR EXISTS (SELECT 1 FROM workflow_instances instance WHERE instance.work_id=owner.id AND instance.instance_state NOT IN ('completed','cancelled','superseded'))
    OR NOT EXISTS (SELECT 1 FROM workflow_backlog_alignment remaining JOIN work_items related ON related.id=remaining.related_work_id WHERE remaining.work_id=owner.id AND related.kind<>'initiative'))
ORDER BY alignment.related_work_id,alignment.work_id LIMIT 1`},
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
			fmt.Sprintf("initiative retirement: %s contains a foreign-owned dependency for initiative %s, owner work %s; no migration was applied", check.table, initiative, owner), false,
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
	if err := retireInitiativeResearch(ctx, q); err != nil {
		return err
	}
	for _, id := range ids {
		// Migration 198 drops this table, so the shared work-removal list no
		// longer clears it; this predecessor-schema step must.
		if _, err := q.ExecContext(ctx, `DELETE FROM workflow_backlog_alignment WHERE work_id=? OR related_work_id=?`, id, id); err != nil {
			return fmt.Errorf("initiative retirement: clear alignment: %w", err)
		}
		if err := deleteWorkOwnedProjections(ctx, q, id); err != nil {
			return fmt.Errorf("initiative retirement: %s: %w", id, err)
		}
	}
	if _, err := q.ExecContext(ctx, `DELETE FROM write_guard`); err != nil {
		return fmt.Errorf("initiative retirement: close write guard: %w", err)
	}
	return nil
}

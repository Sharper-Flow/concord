package store

import (
	"context"
	"database/sql"
	"fmt"
)

// knowledgeHomePairTables lists every Git-derived projection keyed on the
// (home_project_id, home_locator_id) identity pair. The
// project_locators_referenced_by_knowledge_no_delete trigger binds each one's
// pair to project_locators; this list is the Go-side mirror for
// locator-removal refusal. A test pins the list to the trigger set, so the two
// cannot drift apart.
var knowledgeHomePairTables = []string{
	"archived_work",
}

// knowledgeLocatorDeleteGuardName is the migration 52 trigger that refuses
// removing a Project locator Git-derived knowledge still references.
const knowledgeLocatorDeleteGuardName = "project_locators_referenced_by_knowledge_no_delete"

// locatorReferencedByKnowledge reports whether any Git-derived knowledge row
// still names the Project/locator pair. Locators are stable identity (PM6
// section 7); a removal that strands historical notes or law records is
// refused rather than allowed to orphan them.
func locatorReferencedByKnowledge(ctx context.Context, q queryer, projectID, locatorID string) (bool, error) {
	for _, table := range knowledgeHomePairTables {
		query := fmt.Sprintf(`SELECT 1 FROM %s WHERE home_project_id = ? AND home_locator_id = ? LIMIT 1`, table) //nolint:gosec // table comes only from the closed knowledgeHomePairTables list above and no values are interpolated.
		var found int
		err := q.QueryRowContext(ctx, query, projectID, locatorID).Scan(&found)
		if err == nil {
			return true, nil
		}
		if err != sql.ErrNoRows {
			return false, wrapFailure(KindUnavailable, "fold_event", "cannot read "+table+" references", true,
				"retry once the database is readable", err)
		}
	}
	return false, nil
}

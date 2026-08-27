package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// knowledgeHomePairTables lists every Git-derived projection keyed on the
// (home_project_id, home_locator_id) identity pair. Migration 52 binds each
// one's pair to project_locators through triggers; this list is the Go-side
// mirror for locator-removal refusal and rebuild orphan detection. A test
// pins the list to the trigger set, so the two cannot drift apart.
var knowledgeHomePairTables = []string{
	"archived_work",
	"knowledge_index_watermark",
	"knowledge_kind_coverage",
	"law_subjects",
	"law_relations",
	"domains",
	"domain_registries",
	"domain_architecture_relations",
	"domain_relation_governing_laws",
	"law_domain_homes",
	"law_domain_applicability",
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

// countOrphanedKnowledgeHomePairs counts Git-derived knowledge rows whose
// identity pair names no Project locator. RebuildFromLog runs it after replay:
// the log cannot restore a locator that was never event-derived, so a row the
// binding would refuse to insert must not silently survive the rebuild either.
func countOrphanedKnowledgeHomePairs(ctx context.Context, q queryer) (int64, error) {
	parts := make([]string, 0, len(knowledgeHomePairTables))
	for _, table := range knowledgeHomePairTables {
		parts = append(parts, fmt.Sprintf(`(SELECT count(*) FROM %s k WHERE NOT EXISTS (SELECT 1 FROM project_locators pl WHERE pl.project_id = k.home_project_id AND pl.locator_id = k.home_locator_id))`, table)) //nolint:gosec // table comes only from the closed knowledgeHomePairTables list above and no values are interpolated.
	}
	var orphans int64
	if err := q.QueryRowContext(ctx, "SELECT "+strings.Join(parts, " + ")).Scan(&orphans); err != nil {
		return 0, wrapFailure(KindUnavailable, "rebuild_from_log", "cannot audit knowledge home pairs", true,
			"retry once the database is readable", err)
	}
	return orphans, nil
}

// dropTriggerReturningDDL removes a trigger and returns the DDL text SQLite
// stored for it, so the caller can re-create the identical trigger without a
// second copy of its definition. An absent trigger returns "".
func dropTriggerReturningDDL(ctx context.Context, tx *sql.Tx, name string) (string, error) {
	var ddl sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&ddl)
	if err == sql.ErrNoRows {
		return "", nil
	} else if err != nil {
		return "", wrapFailure(KindUnavailable, "rebuild_from_log", "cannot read the knowledge locator guard", true,
			"retry once the database is readable", err)
	}
	if _, err := tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS `+name); err != nil {
		return "", wrapFailure(KindUnavailable, "rebuild_from_log", "cannot drop the knowledge locator guard", true,
			"retry once the database is writable", err)
	}
	return ddl.String, nil
}

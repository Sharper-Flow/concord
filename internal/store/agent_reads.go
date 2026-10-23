package store

import (
	"context"
	"database/sql"
)

// WorkItemSummary contains the bounded fields needed to establish an Initiative
// read target and render its coordination narrative.
type WorkItemSummary struct {
	Kind      string
	Title     string
	Narrative string
}

// ReadWorkItemSummary reads one work item's kind, title, and narrative in one
// bounded query. Missing projections remain typed so callers do not inspect
// SQL errors.
func (s *Store) ReadWorkItemSummary(ctx context.Context, workID string) (WorkItemSummary, error) {
	if s == nil || s.db == nil {
		return WorkItemSummary{}, newFailure(KindUnavailable, "read_work_item_summary", "database is not open", true, "open the authority database")
	}
	return readWorkItemSummary(ctx, s.db, workID)
}

func readWorkItemSummary(ctx context.Context, q queryer, workID string) (WorkItemSummary, error) {
	var summary WorkItemSummary
	err := q.QueryRowContext(ctx, `SELECT kind, title, narrative FROM work_items WHERE id=?`, workID).Scan(&summary.Kind, &summary.Title, &summary.Narrative)
	if err == sql.ErrNoRows {
		return WorkItemSummary{}, newFailure(KindProjectionNotFound, "read_work_item_summary", "work item does not exist", false, "reread_entities")
	}
	if err != nil {
		return WorkItemSummary{}, wrapFailure(KindUnavailable, "read_work_item_summary", "cannot read work item summary", true, "retry once the database is readable", err)
	}
	return summary, nil
}

// KnowledgeIndexWatermark returns the latest indexed commit for a home
// project. When locatorID is provided, the lookup is narrowed to that locator
// and head tuple; otherwise it is scoped only to the home project.
func (s *Store) KnowledgeIndexWatermark(ctx context.Context, homeProjectID, locatorID, headRef string) (string, error) {
	if s == nil || s.db == nil {
		return "", newFailure(KindUnavailable, "knowledge_index_watermark", "database is not open", true, "open the authority database")
	}
	return knowledgeIndexWatermark(ctx, s.db, homeProjectID, locatorID, headRef)
}

func knowledgeIndexWatermark(ctx context.Context, q queryer, homeProjectID, locatorID, headRef string) (string, error) {
	query := `SELECT COALESCE(max(scanned_commit_oid),'') FROM knowledge_index_watermark WHERE home_project_id=?`
	args := []any{homeProjectID}
	if locatorID != "" {
		query += ` AND home_locator_id=? AND head_ref=?`
		args = append(args, locatorID, headRef)
	}
	var watermark string
	if err := q.QueryRowContext(ctx, query, args...).Scan(&watermark); err != nil {
		return "", wrapFailure(KindUnavailable, "knowledge_index_watermark", "cannot read the knowledge-index watermark", true, "retry once the database is readable", err)
	}
	return watermark, nil
}

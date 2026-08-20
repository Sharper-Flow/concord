package store

import (
	"context"
	"database/sql"
)

// WorkItemSummary contains the bounded fields needed to establish an Initiative
// read target and render its coordination narrative.
type WorkItemSummary struct {
	Kind      string
	Narrative string
}

// ReadWorkItemSummary reads one work item's kind and narrative in one bounded
// query. Missing projections remain typed so callers do not inspect SQL errors.
func (s *Store) ReadWorkItemSummary(ctx context.Context, workID string) (WorkItemSummary, error) {
	if s == nil || s.db == nil {
		return WorkItemSummary{}, newFailure(KindUnavailable, "read_work_item_summary", "database is not open", true, "open the authority database")
	}
	var summary WorkItemSummary
	err := s.db.QueryRowContext(ctx, `SELECT kind, narrative FROM work_items WHERE id=?`, workID).Scan(&summary.Kind, &summary.Narrative)
	if err == sql.ErrNoRows {
		return WorkItemSummary{}, newFailure(KindProjectionNotFound, "read_work_item_summary", "work item does not exist", false, "reread_entities")
	}
	if err != nil {
		return WorkItemSummary{}, wrapFailure(KindUnavailable, "read_work_item_summary", "cannot read work item summary", true, "retry once the database is readable", err)
	}
	return summary, nil
}

// DomainEventWatermark returns the current append-only event sequence. The
// COALESCE keeps an empty authority deterministic at zero.
func (s *Store) DomainEventWatermark(ctx context.Context) (int64, error) {
	if s == nil || s.db == nil {
		return 0, newFailure(KindUnavailable, "domain_event_watermark", "database is not open", true, "open the authority database")
	}
	var watermark int64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(max(seq),0) FROM domain_events`).Scan(&watermark); err != nil {
		return 0, wrapFailure(KindUnavailable, "domain_event_watermark", "cannot read the domain-event watermark", true, "retry once the database is readable", err)
	}
	return watermark, nil
}

// KnowledgeIndexWatermark returns the latest indexed commit for a home
// project. When locatorID is provided, the lookup is narrowed to that locator
// and head tuple; otherwise it is scoped only to the home project.
func (s *Store) KnowledgeIndexWatermark(ctx context.Context, homeProjectID, locatorID, headRef string) (string, error) {
	if s == nil || s.db == nil {
		return "", newFailure(KindUnavailable, "knowledge_index_watermark", "database is not open", true, "open the authority database")
	}
	query := `SELECT COALESCE(max(scanned_commit_oid),'') FROM knowledge_index_watermark WHERE home_project_id=?`
	args := []any{homeProjectID}
	if locatorID != "" {
		query += ` AND home_locator_id=? AND head_ref=?`
		args = append(args, locatorID, headRef)
	}
	var watermark string
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&watermark); err != nil {
		return "", wrapFailure(KindUnavailable, "knowledge_index_watermark", "cannot read the knowledge-index watermark", true, "retry once the database is readable", err)
	}
	return watermark, nil
}

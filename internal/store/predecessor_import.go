package store

import (
	"context"
	"database/sql"
)

// ProductMembership returns the Project ids currently registered as members
// of the given Product, in deterministic sorted order. The result is empty
// (not a typed failure) when the Product does not exist; the caller decides
// whether absence is an error in its own contract. The query is a thin
// projection over the product_projects fold output and lives in the store
// package so operator verbs do not reach for raw database handles.
func (s *Store) ProductMembership(ctx context.Context, productID string) ([]string, error) {
	return productMembership(ctx, s.db, productID)
}

func productMembership(ctx context.Context, q queryer, productID string) ([]string, error) {
	if productID == "" {
		return nil, newFailure(KindInvalidOperation, "product_membership",
			"product ID is empty", false, "supply a non-empty product id")
	}
	rows, err := q.QueryContext(ctx, `SELECT project_id FROM product_projects WHERE product_id=? ORDER BY project_id`, productID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "product_membership",
			"cannot read Product membership", true,
			"retry once the database is readable", err)
	}
	defer func() { _ = rows.Close() }()
	var membership []string
	for rows.Next() {
		var projectID string
		if scanErr := rows.Scan(&projectID); scanErr != nil {
			return nil, wrapFailure(KindUnavailable, "product_membership",
				"cannot scan Product membership row", true,
				"retry once the database is readable", scanErr)
		}
		membership = append(membership, projectID)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, wrapFailure(KindUnavailable, "product_membership",
			"cannot iterate Product membership rows", true,
			"retry once the database is readable", rowsErr)
	}
	return membership, nil
}

// EventIDExists reports whether the durable log already carries an event with
// the given stable event id. It is the read-side companion of the append-time
// duplicate detection: an idempotent operator surfaces the existing row
// without paying for a doomed INSERT. The query is a single row read against
// the event_id primary index.
func (s *Store) EventIDExists(ctx context.Context, eventID string) (bool, error) {
	return eventIDExists(ctx, s.db, eventID)
}

// FirstWorkEventByOtherActor returns the first durable event on a work
// subject whose actor differs from the excluded actor, ordered by the log
// sequence. The predecessor import uses it as the CD-0097 D4 conflict probe:
// any event another actor appended to an imported work item is a Concord-side
// change to an item the predecessor still owns, and the migration must refuse
// naming the owning source instead of overwriting either side. Found is false
// when every event on the subject carries the excluded actor.
func (s *Store) FirstWorkEventByOtherActor(ctx context.Context, workID, excludedActor string) (kind, actor string, found bool, err error) {
	if workID == "" {
		return "", "", false, newFailure(KindInvalidOperation, "work_event_by_other_actor",
			"work id is empty", false, "supply a non-empty work id")
	}
	if excludedActor == "" {
		return "", "", false, newFailure(KindInvalidOperation, "work_event_by_other_actor",
			"excluded actor is empty", false, "supply a non-empty excluded actor")
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT kind, actor FROM domain_events
		 WHERE subject_type=? AND subject_id=? AND actor<>?
		 ORDER BY seq LIMIT 1`, string(SubjectWorkItem), workID, excludedActor)
	var eventKind, eventActor string
	if err := row.Scan(&eventKind, &eventActor); err != nil {
		if err == sql.ErrNoRows {
			return "", "", false, nil
		}
		return "", "", false, wrapFailure(KindUnavailable, "work_event_by_other_actor",
			"cannot probe work events for foreign actors", true,
			"retry once the database is readable", err)
	}
	return eventKind, eventActor, true, nil
}

func eventIDExists(ctx context.Context, q queryer, eventID string) (bool, error) {
	if eventID == "" {
		return false, newFailure(KindInvalidOperation, "event_id_exists",
			"event id is empty", false, "supply a non-empty event id")
	}
	var exists bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM domain_events WHERE event_id=?)`, eventID).Scan(&exists); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, wrapFailure(KindUnavailable, "event_id_exists",
			"cannot probe domain_events for event id", true,
			"retry once the database is readable", err)
	}
	return exists, nil
}

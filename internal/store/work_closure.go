package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

// WorkClosure carries everything the closure receipt restates for one
// completed work item: the terminal pin, the problem the recorded proposal
// stated, the scope the proposal excluded, the effective delivery artifact,
// and the work items linked raised_from the closed item (CD-0202). Every
// section is optional: a completed item with none renders the receipt the
// pin alone produced before this read existed.
type WorkClosure struct {
	Pin              WorkPin
	Problem          string
	OutOfScope       []string
	DeliveryArtifact string
	FollowUps        []WorkClosureFollowUp
}

// WorkClosureFollowUp is one direct raised_from successor of the closed item
// with its confirmed Linear issue key when the link state carries one.
type WorkClosureFollowUp struct {
	WorkID         string
	Title          string
	LinearIssueKey string
}

// ReadWorkClosure opens one read transaction and derives the closure facts
// beside the pin. It is the closure receipt's only store read path
// (CD-0202): the receipt renders from store data alone, and no other verb
// consumes the closure value. An unknown work item or an unreadable
// projection is a store failure; a completed item whose proposal, delivery,
// or relations are absent reads with those sections empty.
func ReadWorkClosure(ctx context.Context, s *Store, workID string) (WorkClosure, error) {
	var closure WorkClosure
	if s == nil || s.db == nil {
		return closure, newFailure(KindUnavailable, "work_closure", "store is not open", false, "open the authority database")
	}
	ctx, err := prepareWorkNavigation(ctx, s, workID)
	if err != nil {
		return closure, err
	}
	tx, err := beginReadTx(ctx, s.db)
	if err != nil {
		return closure, wrapFailure(KindUnavailable, "work_closure", "cannot open a consistent work closure snapshot", true, "retry once the database is readable", err)
	}
	defer tx.Rollback()
	return readWorkClosureTx(ctx, tx, workID)
}

// readWorkClosureTx derives the closure from the transaction its caller
// holds. The phases run after the pin's phases and add no early exit: an
// ambiguous contract projection already stops inside the pin read, and a
// missing proposal, delivery assertion, or relation degrades by omission.
func readWorkClosureTx(ctx context.Context, tx *sql.Tx, workID string) (WorkClosure, error) {
	pin, err := ReadWorkPinTx(ctx, tx, workID)
	if err != nil {
		return WorkClosure{}, err
	}
	closure := WorkClosure{Pin: pin}
	var outOfScopeJSON string
	if err := tx.QueryRowContext(ctx, `SELECT problem,out_of_scope FROM workflow_proposal_records WHERE work_id=? ORDER BY work_version DESC LIMIT 1`, workID).Scan(&closure.Problem, &outOfScopeJSON); err != nil {
		if err != sql.ErrNoRows {
			return closure, wrapFailure(KindUnavailable, "work_closure", "cannot read the recorded proposal", true, "retry once the database is readable", err)
		}
	}
	outOfScope, err := decodeWorkClosureOutOfScope(outOfScopeJSON)
	if err != nil {
		return closure, err
	}
	closure.OutOfScope = outOfScope
	assertion, err := workflowDeliveryAssertionRead(ctx, tx, workID)
	if err != nil {
		return closure, err
	}
	closure.DeliveryArtifact = effectiveDeliveryArtifact(assertion)
	followUps, err := workClosureFollowUpsTx(ctx, tx, workID)
	if err != nil {
		return closure, err
	}
	closure.FollowUps = followUps
	return closure, nil
}

// decodeWorkClosureOutOfScope decodes the proposal's defaulted out_of_scope
// array. A malformed projection is an invariant violation: the fold writes
// only arrays it validated.
func decodeWorkClosureOutOfScope(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, newFailure(KindInvariantViolation, "work_closure", "proposal out_of_scope projection is malformed", false, "rebuild projections from the event log")
	}
	return values, nil
}

// workClosureFollowUpsTx reads the closed item's direct raised_from
// successors in creation order with each confirmed Linear key (CD-0202).
// raised_from is not transitive, so depth stays at one.
func workClosureFollowUpsTx(ctx context.Context, tx *sql.Tx, workID string) ([]WorkClosureFollowUp, error) {
	rows, err := tx.QueryContext(ctx, `SELECT r.work_id_from,w.title,COALESCE(l.human_key,'') FROM relations r JOIN work_items w ON w.id=r.work_id_from LEFT JOIN linear_issue_links l ON l.work_id=r.work_id_from AND l.link_state='confirmed' WHERE r.work_id_to=? AND r.kind='raised_from' ORDER BY w.created_at,w.id`, workID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "work_closure", "cannot read the raised follow-up items", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	followUps := []WorkClosureFollowUp{}
	for rows.Next() {
		var followUp WorkClosureFollowUp
		if err := rows.Scan(&followUp.WorkID, &followUp.Title, &followUp.LinearIssueKey); err != nil {
			return nil, wrapFailure(KindUnavailable, "work_closure", "cannot scan a raised follow-up item", true, "retry once the database is readable", err)
		}
		followUps = append(followUps, followUp)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "work_closure", "cannot enumerate the raised follow-up items", true, "retry once the database is readable", err)
	}
	return followUps, nil
}

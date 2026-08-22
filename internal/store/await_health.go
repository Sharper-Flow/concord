package store

import (
	"context"
	"time"
)

// Issue #87: distinguish waiting from will-never-complete. A step that
// delegates completion to an external actor declares how long the wait is
// expected to take (expected_within_seconds, operator-approved through the
// ordinary add_condition approval path). Health is derived at read time by
// comparing the await's age against that bound — no timer, no state change,
// no background prober, and no authority from elapsed time. An await beyond
// its bound is overdue/unverified, never resolved-by-clock.

// AwaitHealth is the derived, read-time classification of one open await.
type AwaitHealth struct {
	ConditionID           string `json:"condition_id"`
	AwaitType             string `json:"await_type"`
	AwaitRef              string `json:"await_ref"`
	ResolutionAuthority   string `json:"resolution_authority"`
	State                 string `json:"state"`
	WaitingSince          string `json:"waiting_since"`
	ExpectedWithinSeconds int64  `json:"expected_within_seconds"`
	AgeSeconds            int64  `json:"age_seconds"`
	// Overdue is true only when a declared bound exists and the wait has
	// exceeded it. Undeclared awaits are never overdue — the honest output
	// for "no expectation recorded" is unknown, not alarm.
	Overdue bool `json:"overdue"`
	// Health is the closed classification: waiting (within bound or none
	// declared), overdue (beyond declared bound — unverified, not failed).
	Health string `json:"health"`
}

const (
	AwaitHealthWaiting = "waiting"
	AwaitHealthOverdue = "overdue"
)

// AwaitHealthForWork derives the health of every open external await on one
// work item. Bounded by the condition count; reads only the condition table.
func (s *Store) AwaitHealthForWork(ctx context.Context, workID string, now time.Time) ([]AwaitHealth, error) {
	if s == nil || s.db == nil {
		return nil, newFailure(KindUnavailable, "await_health", "store is not open", false, "open the authority database")
	}
	return awaitHealthForWork(ctx, s.db, workID, now)
}

func awaitHealthForWork(ctx context.Context, q queryer, workID string, now time.Time) ([]AwaitHealth, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	rows, err := q.QueryContext(ctx, `SELECT condition_id,await_type,await_ref,resolution_authority,recorded_at,coalesce(expected_within_seconds,0) FROM workflow_external_conditions WHERE work_id=? AND condition_state='open' ORDER BY condition_id`, workID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "await_health", "cannot read awaits", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	out := []AwaitHealth{}
	for rows.Next() {
		var a AwaitHealth
		if err := rows.Scan(&a.ConditionID, &a.AwaitType, &a.AwaitRef, &a.ResolutionAuthority, &a.WaitingSince, &a.ExpectedWithinSeconds); err != nil {
			return nil, wrapFailure(KindUnavailable, "await_health", "cannot decode await", true, "retry once the database is readable", err)
		}
		a.State = "open"
		if recorded, err := time.Parse(time.RFC3339Nano, a.WaitingSince); err == nil {
			a.AgeSeconds = int64(now.Sub(recorded).Seconds())
			if a.AgeSeconds < 0 {
				a.AgeSeconds = 0
			}
		}
		a.Health = AwaitHealthWaiting
		if a.ExpectedWithinSeconds > 0 && a.AgeSeconds > a.ExpectedWithinSeconds {
			a.Overdue = true
			a.Health = AwaitHealthOverdue
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// OverdueAwaitsInProduct lists open awaits across a Product's work that have
// exceeded their declared bounds, oldest wait first — the operator-facing
// "stalled, not progressing" surface. Work whose lifecycle is terminal is
// excluded: a terminal item no longer presents as in flight.
func (s *Store) OverdueAwaitsInProduct(ctx context.Context, productID string, now time.Time, limit int) ([]AwaitHealth, error) {
	if s == nil || s.db == nil {
		return nil, newFailure(KindUnavailable, "await_health", "store is not open", false, "open the authority database")
	}
	return overdueAwaitsInProduct(ctx, s.db, productID, now, limit)
}

func overdueAwaitsInProduct(ctx context.Context, q queryer, productID string, now time.Time, limit int) ([]AwaitHealth, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := q.QueryContext(ctx, `
		SELECT c.condition_id, c.await_type, c.await_ref, c.resolution_authority, c.recorded_at, coalesce(c.expected_within_seconds,0), c.work_id
		FROM workflow_external_conditions c
		JOIN work_items w ON w.id = c.work_id
		WHERE c.condition_state = 'open'
		  AND c.expected_within_seconds IS NOT NULL
		  AND w.lifecycle NOT IN ('completed','cancelled','superseded')
		  AND EXISTS (SELECT 1 FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=c.work_id AND pp.product_id=?)
		ORDER BY c.recorded_at ASC
		LIMIT ?`, productID, limit)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "await_health", "cannot read overdue awaits", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	type row struct {
		health AwaitHealth
		workID string
	}
	var candidates []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.health.ConditionID, &r.health.AwaitType, &r.health.AwaitRef, &r.health.ResolutionAuthority, &r.health.WaitingSince, &r.health.ExpectedWithinSeconds, &r.workID); err != nil {
			return nil, wrapFailure(KindUnavailable, "await_health", "cannot decode overdue await", true, "retry once the database is readable", err)
		}
		candidates = append(candidates, r)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "await_health", "cannot enumerate overdue awaits", true, "retry once the database is readable", err)
	}
	out := []AwaitHealth{}
	for _, candidate := range candidates {
		if recorded, err := time.Parse(time.RFC3339Nano, candidate.health.WaitingSince); err == nil {
			candidate.health.AgeSeconds = int64(now.Sub(recorded).Seconds())
			if candidate.health.AgeSeconds > candidate.health.ExpectedWithinSeconds {
				candidate.health.Overdue = true
				candidate.health.Health = AwaitHealthOverdue
				out = append(out, candidate.health)
			}
		}
	}
	return out, nil
}

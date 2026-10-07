package store

import (
	"context"
	"strings"
	"testing"
)

// TestWorkPinDrivingSessionsPlanSeeksActors pins the driving-session read to
// index seeks on workflow_actors. Every session boot and continuity render
// runs this read inside a read transaction, and workflow_actors grows with
// every session and lane. A full scan with a per-actor correlated probe of the
// work's events holds the WAL read mark for seconds on a production-sized
// store and starves checkpoints.
func TestWorkPinDrivingSessionsPlanSeeksActors(t *testing.T) {
	s := openTemp(t)
	rows, err := s.DatabaseForTesting().QueryContext(context.Background(), `EXPLAIN QUERY PLAN `+workPinDrivingSessionsSQL, workPinDrivingSessionsArgs("work-plan")...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(details, " | ")
	for _, detail := range details {
		if strings.HasPrefix(detail, "SCAN a") {
			t.Fatalf("driving-session read scans every actor: %s", plan)
		}
	}
	if strings.Count(plan, "SEARCH a USING INDEX sqlite_autoindex_workflow_actors_1 (actor_ref=?)") != 2 {
		t.Fatalf("driving-session read does not seek both actor arms by actor_ref: %s", plan)
	}
}

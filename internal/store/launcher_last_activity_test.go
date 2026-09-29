package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// stampedWorkEvent builds a work_item-subject event at an exact time, so a
// test can place activity where RFC3339Nano's variable-width text order and
// time order disagree.
func stampedWorkEvent(t *testing.T, id, kind, subjectID string, payload map[string]any, at time.Time) Event {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return Event{EventID: id, Kind: kind, SubjectType: SubjectWorkItem, SubjectID: subjectID, Actor: "operator", OccurredAt: at, PayloadVersion: 1, Payload: encoded}
}

// seedLastActivityFixture inserts one Product whose active work carries
// explicit fixed-width last-activity stamps. The stamps include both traps
// that break variable-width RFC3339Nano text ordering: a bare second
// ("...00.000000000Z") versus a nine ("...00.900000000Z"), and a one
// (".100000000") versus a twelve (".120000000") in the fraction. The caller
// owns the fold-guard deletion and store lifetime.
func seedLastActivityFixture(t *testing.T, s *Store) {
	t.Helper()
	_, err := s.DatabaseForTesting().Exec(`
		INSERT INTO fold_guard(active) VALUES (1);
		INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES ('rec','Recency','prototype','operator_only',1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z');
		INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES ('rec-project','Recency project',1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z');
		INSERT INTO product_projects(product_id,project_id,role) VALUES ('rec','rec-project','primary');
		INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,last_activity_at) VALUES
		('rec-old','task','Oldest','needed',1,1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z','2026-08-01T00:00:00.000000000Z'),
		('rec-tie-b','task','Tie two','needed',1,1,'2026-08-01T00:00:00Z','2026-08-03T00:00:00Z','2026-08-03T00:00:00.000000000Z'),
		('rec-tie-a','task','Tie one','needed',1,1,'2026-08-01T00:00:00Z','2026-08-03T00:00:00Z','2026-08-03T00:00:00.000000000Z'),
		('rec-c1','task','Fraction one','needed',1,1,'2026-08-01T00:00:00Z','2026-08-04T00:00:00Z','2026-08-04T00:00:00.100000000Z'),
		('rec-c12','task','Fraction twelve','needed',1,1,'2026-08-01T00:00:00Z','2026-08-04T00:00:00Z','2026-08-04T00:00:00.120000000Z'),
		('rec-a','task','Bare second','needed',1,1,'2026-08-01T00:00:00Z','2026-08-05T00:00:00Z','2026-08-05T00:00:00.000000000Z'),
		('rec-b','task','Nine fraction','in_progress',1,1,'2026-08-01T00:00:00Z','2026-08-05T00:00:00Z','2026-08-05T00:00:00.900000000Z'),
		('rec-z9','bug','Newest','in_progress',1,1,'2026-08-01T00:00:00Z','2026-08-09T00:00:00Z','2026-08-09T00:00:00.000000000Z');
		INSERT INTO work_projects(work_id,project_id,role) VALUES
		('rec-old','rec-project','primary'),('rec-tie-b','rec-project','primary'),
		('rec-tie-a','rec-project','primary'),('rec-c1','rec-project','primary'),
		('rec-c12','rec-project','primary'),('rec-a','rec-project','primary'),
		('rec-b','rec-project','primary'),('rec-z9','rec-project','primary');
	`)
	if err != nil {
		t.Fatal(err)
	}
}

// TestLauncherProductOrdersByLastActivityAcrossPageCut proves the store owns
// the work-list ordering: the fixed-width last_activity_at column orders in
// true time order under SQLite's TEXT comparison — the traps where
// variable-width RFC3339Nano text ordering inverts carry no weight — ties
// break by id, the read drains the complete active set across what a limit
// would have cut, and no omission-by-limit state exists for active work.
func TestLauncherProductOrdersByLastActivityAcrossPageCut(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	seedLastActivityFixture(t, s)
	defer func() {
		if _, err := s.DatabaseForTesting().Exec(`DELETE FROM fold_guard`); err != nil {
			t.Errorf("remove fold guard: %v", err)
		}
	}()
	ctx := context.Background()
	result, err := s.QueryLauncherProduct(ctx, LauncherProductRequest{Product: "rec", Limit: 3, Depth: 3})
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{
		"rec-z9", "rec-b", "rec-a", "rec-c12", "rec-c1", "rec-tie-a", "rec-tie-b", "rec-old",
	}
	if len(result.Works) != len(wantOrder) {
		t.Fatalf("active works=%d, want the complete set of %d: %#v", len(result.Works), len(wantOrder), result.Works)
	}
	for i, id := range wantOrder {
		if result.Works[i].ID != id {
			t.Fatalf("work %d = %s, want %s (true last-activity order, id tiebreak)", i, result.Works[i].ID, id)
		}
	}
	for _, omission := range result.Omissions {
		if strings.Contains(omission, "Product work omitted") {
			t.Fatalf("active work carried an omission-by-limit state: %v", result.Omissions)
		}
	}
	if len(result.OrderingKeys) != 2 || result.OrderingKeys[0] != "last_activity_at" || result.OrderingKeys[1] != "id" {
		t.Fatalf("ordering keys = %v, want the stored last-activity ordering", result.OrderingKeys)
	}
}

// TestWorkItemLastActivityAdvancesOnNonVersionedEvents proves the fold rule:
// a work_item-subject event that never touches the versioned write still
// advances last_activity_at, updated_at keeps its versioned-write meaning,
// the stored stamp stays fixed width, and work.removed advances nothing.
func TestWorkItemLastActivityAdvancesOnNonVersionedEvents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	if err := ApplyOperation(ctx, s, Operation{
		Events: []Event{
			operationEvent("la-product", "product.created", SubjectProduct, "la-product", map[string]any{
				"display_name": "Last Activity", "stage_maturity": "prototype", "stage_audience_commitment": "operator_only",
			}),
			operationEvent("la-project", "project.created", SubjectProject, "la-project", map[string]any{"display_name": "Last Activity Project"}),
			operationEvent("la-membership", "product_project.added", SubjectProduct, "la-product", map[string]any{
				"product_id": "la-product", "project_id": "la-project", "role": "primary", "reason": "fixture", "expected_version": 1, "resulting_version": 2,
			}),
		},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "la-product"): 0, VersionRef(SubjectProject, "la-project"): 0},
	}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{
		Events: []Event{
			workCreatedEvent("work-la", "la-create"),
			operationEvent("la-work-project", "work_project.added", SubjectWorkItem, "work-la", map[string]any{
				"work_id": "work-la", "project_id": "la-project", "role": "primary", "reason": "fixture", "expected_version": 1, "resulting_version": 2,
			}),
		},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-la"): 0},
	}); err != nil {
		t.Fatal(err)
	}
	const createdStamp = "2026-08-07T12:00:00.000000000Z"
	var updatedAt, lastActivity string
	if err := s.DatabaseForTesting().QueryRow(
		`SELECT updated_at, last_activity_at FROM work_items WHERE id='work-la'`).Scan(&updatedAt, &lastActivity); err != nil {
		t.Fatal(err)
	}
	if updatedAt != createdStamp[:19]+"Z" {
		t.Fatalf("updated_at = %q, want the created write %q", updatedAt, createdStamp[:19]+"Z")
	}
	if lastActivity != createdStamp {
		t.Fatalf("last_activity_at = %q, want the fixed-width creation stamp %q", lastActivity, createdStamp)
	}

	observationAt := time.Date(2026, 8, 8, 9, 30, 5, 120000000, time.UTC)
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		stampedWorkEvent(t, "la-obs", WorkObservationRecorded, "work-la", map[string]any{
			"observation_id": "obs:0123456789abcdef", "statement": "The fold recorded activity without a versioned write.", "refs": []string{}, "tags": []string{},
		}, observationAt),
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(
		`SELECT updated_at, last_activity_at FROM work_items WHERE id='work-la'`).Scan(&updatedAt, &lastActivity); err != nil {
		t.Fatal(err)
	}
	if updatedAt != createdStamp[:19]+"Z" {
		t.Fatalf("updated_at = %q, want the unchanged versioned write", updatedAt)
	}
	if lastActivity != "2026-08-08T09:30:05.120000000Z" {
		t.Fatalf("last_activity_at = %q, want the observation's fixed-width stamp", lastActivity)
	}

	// work.removed advances nothing: the guard keeps the marker untouched
	// even when the subject row still exists at fold time. work_items is
	// fold-only, so the direct call runs under a fold guard on its own
	// transaction.
	removalAt := observationAt.Add(time.Hour)
	db := s.DatabaseForTesting()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	if err := advanceWorkLastActivity(ctx, tx, stampedWorkEvent(t, "la-remove", WorkRemoved, "work-la", map[string]any{"reason": "test"}, removalAt)); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRowContext(ctx,
		`SELECT last_activity_at FROM work_items WHERE id='work-la'`).Scan(&lastActivity); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if lastActivity != "2026-08-08T09:30:05.120000000Z" {
		t.Fatalf("work.removed advanced last_activity_at to %q", lastActivity)
	}
}

// TestNonWorkSubjectEventsDoNotAdvanceLastActivity proves the subject-type
// gate: IDs are unique per entity table, not across tables, so a product,
// project, or session event whose subject ID equals a work item's ID leaves
// the work item's last_activity_at untouched. Without the gate, such an
// event would move the row in the launcher work list and a log rebuild would
// disagree with migration 108's work_item-only backfill.
func TestNonWorkSubjectEventsDoNotAdvanceLastActivity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`
		INSERT INTO fold_guard(active) VALUES (1);
		INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,last_activity_at) VALUES
		('cx-work','task','Collision','needed',1,1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z','2026-08-01T00:00:00.000000000Z');
	`); err != nil {
		t.Fatal(err)
	}
	const seeded = "2026-08-01T00:00:00.000000000Z"
	later := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
	for _, subject := range []SubjectType{SubjectProduct, SubjectProject, SubjectSession} {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		event := Event{EventID: "cx-" + string(subject), Kind: "product.updated", SubjectType: subject, SubjectID: "cx-work", Actor: "operator", OccurredAt: later, PayloadVersion: 1}
		if err := advanceWorkLastActivity(ctx, tx, event); err != nil {
			t.Fatal(err)
		}
		var lastActivity string
		if err := tx.QueryRowContext(ctx,
			`SELECT last_activity_at FROM work_items WHERE id='cx-work'`).Scan(&lastActivity); err != nil {
			t.Fatal(err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		if lastActivity != seeded {
			t.Fatalf("subject %s advanced last_activity_at to %q", subject, lastActivity)
		}
	}
}

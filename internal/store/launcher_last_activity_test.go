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

// seedLogEvent appends one event directly to the retained log. The launcher
// ordering derives from this log, so the helper places activity without a
// fold and without touching any work_items column — the write shape an older
// fold generation produces. The payload stays irrelevant to the read.
func seedLogEvent(t *testing.T, s *Store, eventID, subjectType, subjectID, kind, occurredAt string) {
	t.Helper()
	_, err := s.DatabaseForTesting().Exec(
		`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,1,'{}')`,
		eventID, kind, subjectType, subjectID, "operator", occurredAt)
	if err != nil {
		t.Fatal(err)
	}
}

// seedLastActivityFixture inserts one Product whose active work carries
// work_item-subject log events at explicit times. The times include both
// traps that break variable-width RFC3339Nano text ordering: a bare second
// ("...00.000000000Z") versus a nine ("...00.900000000Z"), and a one
// (".100000000") versus a twelve (".120000000") in the fraction. The
// updated_at stamps disagree with the event times on purpose, so only the
// log-derived order can pass. The caller owns the fold-guard deletion and
// store lifetime.
func seedLastActivityFixture(t *testing.T, s *Store) {
	t.Helper()
	_, err := s.DatabaseForTesting().Exec(`
		INSERT INTO fold_guard(active) VALUES (1);
		INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES ('rec','Recency','prototype','operator_only',1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z');
		INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES ('rec-project','Recency project',1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z');
		INSERT INTO product_projects(product_id,project_id,role) VALUES ('rec','rec-project','primary');
		INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES
		('rec-old','task','Oldest','needed',1,1,'2026-08-01T00:00:00Z','2026-08-09T00:00:00Z'),
		('rec-tie-b','task','Tie two','needed',1,1,'2026-08-01T00:00:00Z','2026-08-03T00:00:00Z'),
		('rec-tie-a','task','Tie one','needed',1,1,'2026-08-01T00:00:00Z','2026-08-03T00:00:00Z'),
		('rec-c1','task','Fraction one','needed',1,1,'2026-08-01T00:00:00Z','2026-08-04T00:00:00Z'),
		('rec-c12','task','Fraction twelve','needed',1,1,'2026-08-01T00:00:00Z','2026-08-04T00:00:00Z'),
		('rec-a','task','Bare second','needed',1,1,'2026-08-01T00:00:00Z','2026-08-05T00:00:00Z'),
		('rec-b','task','Nine fraction','in_progress',1,1,'2026-08-01T00:00:00Z','2026-08-05T00:00:00Z'),
		('rec-z9','bug','Newest','in_progress',1,1,'2026-08-01T00:00:00Z','2026-08-02T00:00:00Z');
		INSERT INTO work_projects(work_id,project_id,role) VALUES
		('rec-old','rec-project','primary'),('rec-tie-b','rec-project','primary'),
		('rec-tie-a','rec-project','primary'),('rec-c1','rec-project','primary'),
		('rec-c12','rec-project','primary'),('rec-a','rec-project','primary'),
		('rec-b','rec-project','primary'),('rec-z9','rec-project','primary');
	`)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ id, at string }{
		{"rec-old", "2026-08-01T00:00:00Z"},
		{"rec-tie-b", "2026-08-03T00:00:00Z"},
		{"rec-tie-a", "2026-08-03T00:00:00Z"},
		{"rec-c1", "2026-08-04T00:00:00.1Z"},
		{"rec-c12", "2026-08-04T00:00:00.12Z"},
		{"rec-a", "2026-08-05T00:00:00Z"},
		{"rec-b", "2026-08-05T00:00:00.9Z"},
		{"rec-z9", "2026-08-09T00:00:00Z"},
	} {
		seedLogEvent(t, s, "rec-"+tc.id+"-ev", "work_item", tc.id, "work.created", tc.at)
	}
}

// TestLauncherProductOrdersByLastActivityAcrossPageCut proves the store owns
// the work-list ordering: last activity derives from the event log, the
// fixed-width normalization orders in true time order under SQLite's TEXT
// comparison — the traps where variable-width RFC3339Nano text ordering
// inverts carry no weight — ties break by id, the read drains the complete
// active set across what a limit would have cut, and no omission-by-limit
// state exists for active work.
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
		t.Fatalf("ordering keys = %v, want the last-activity ordering", result.OrderingKeys)
	}
}

// TestWorkItemLastActivityAdvancesOnNonVersionedEvents proves the derivation
// the launcher orders by: a work_item-subject event that never touches the
// versioned write moves the work up the launcher order, updated_at keeps its
// versioned-write meaning, and work.removed contributes nothing.
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
	// A rival created a day later leads: its newest event outranks work-la's
	// creation stamp.
	rivalAt := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
	rivalCreate := workCreatedEvent("work-rival", "la-create-rival")
	rivalCreate.OccurredAt = rivalAt
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		rivalCreate,
		stampedWorkEvent(t, "la-rival-project", "work_project.added", "work-rival", map[string]any{
			"work_id": "work-rival", "project_id": "la-project", "role": "primary", "reason": "fixture", "expected_version": 1, "resulting_version": 2,
		}, rivalAt),
	}}); err != nil {
		t.Fatal(err)
	}
	const createdStamp = "2026-08-07T12:00:00.000000000Z"
	var updatedAt string
	if err := s.DatabaseForTesting().QueryRow(
		`SELECT updated_at FROM work_items WHERE id='work-la'`).Scan(&updatedAt); err != nil {
		t.Fatal(err)
	}
	if updatedAt != createdStamp[:19]+"Z" {
		t.Fatalf("updated_at = %q, want the created write %q", updatedAt, createdStamp[:19]+"Z")
	}
	assertLauncherOrder(t, s, []string{"work-rival", "work-la"})

	// A work_item-subject event that performs no versioned write still moves
	// work-la to the top, while updated_at stays on the created write.
	observationAt := time.Date(2026, 8, 9, 9, 30, 5, 120000000, time.UTC)
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		stampedWorkEvent(t, "la-obs", WorkObservationRecorded, "work-la", map[string]any{
			"observation_id": "obs:0123456789abcdef", "statement": "The fold recorded activity without a versioned write.", "refs": []string{}, "tags": []string{},
		}, observationAt),
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(
		`SELECT updated_at FROM work_items WHERE id='work-la'`).Scan(&updatedAt); err != nil {
		t.Fatal(err)
	}
	if updatedAt != createdStamp[:19]+"Z" {
		t.Fatalf("updated_at = %q, want the unchanged versioned write", updatedAt)
	}
	assertLauncherOrder(t, s, []string{"work-la", "work-rival"})

	// work.removed contributes nothing to the derivation: the guard keeps the
	// order untouched even when a later removal event sits in the log. The
	// event is appended directly, so the folded row stays in place for the
	// read.
	seedLogEvent(t, s, "la-remove", "work_item", "work-rival", WorkRemoved, observationAt.Add(time.Hour).Format(time.RFC3339Nano))
	assertLauncherOrder(t, s, []string{"work-la", "work-rival"})
}

// TestPre108FoldLeavesLastActivityStale pins the mixed-release shape the
// launcher must survive: a fold generation from before migration 108 appends
// work_item-subject events and maintains no stored last-activity marker, so
// a 108-era binary's stored column drifts behind the log under a rolling
// upgrade (CD-0111) and ranks a stale item below its true activity. The
// launcher derives last activity from the log, so the true event order wins
// whatever release wrote the events; a product-subject event whose ID
// collides with a work ID changes nothing, because IDs are unique per entity
// table, not across tables; and the dropped column stays dropped.
func TestPre108FoldLeavesLastActivityStale(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	_, err := s.DatabaseForTesting().Exec(`
		INSERT INTO fold_guard(active) VALUES (1);
		INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES ('mix','Mixed Release','prototype','operator_only',1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z');
		INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES ('mix-project','Mixed project',1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z');
		INSERT INTO product_projects(product_id,project_id,role) VALUES ('mix','mix-project','primary');
		INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES
		('mix-stale','task','Old binary wrote here','needed',1,1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z'),
		('mix-moved','task','Current binary wrote here','needed',1,1,'2026-08-01T00:00:00Z','2026-08-10T00:00:00Z');
		INSERT INTO work_projects(work_id,project_id,role) VALUES
		('mix-stale','mix-project','primary'),('mix-moved','mix-project','primary');
	`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := s.DatabaseForTesting().Exec(`DELETE FROM fold_guard`); err != nil {
			t.Errorf("remove fold guard: %v", err)
		}
	}()
	seedLogEvent(t, s, "mix-stale-create", "work_item", "mix-stale", "work.created", "2026-08-01T00:00:00Z")
	seedLogEvent(t, s, "mix-moved-create", "work_item", "mix-moved", "work.created", "2026-08-10T00:00:00Z")
	// The pre-108 write shape: the event lands in the log and nothing else
	// moves. A 108-era stored column backfills mix-stale to 2026-08-01 and
	// stays there, so the stored ordering would rank mix-moved first.
	seedLogEvent(t, s, "mix-stale-obs", "work_item", "mix-stale", WorkObservationRecorded, "2026-08-20T08:00:00Z")
	assertLauncherOrder(t, s, []string{"mix-stale", "mix-moved"})

	// The subject-type gate survives the derivation: a product event whose
	// subject ID equals a work ID must not move the work item.
	seedLogEvent(t, s, "mix-collision", "product", "mix-stale", "product.updated", "2026-08-21T08:00:00Z")
	assertLauncherOrder(t, s, []string{"mix-stale", "mix-moved"})

	var columns int
	if err := s.DatabaseForTesting().QueryRow(
		`SELECT count(*) FROM pragma_table_info('work_items') WHERE name='last_activity_at'`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if columns != 0 {
		t.Fatal("work_items still carries last_activity_at; the stored marker must stay gone so no fold-maintained copy can drift")
	}
}

// assertLauncherOrder reads the launcher Product screen and compares the
// active-segment order against the wanted work IDs.
func assertLauncherOrder(t *testing.T, s *Store, want []string) {
	t.Helper()
	result, err := s.QueryLauncherProduct(context.Background(), LauncherProductRequest{Product: productScopeOf(t, s, want), Limit: 20, Depth: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Works) != len(want) {
		t.Fatalf("active works=%d, want %d: %#v", len(result.Works), len(want), result.Works)
	}
	for i, id := range want {
		if result.Works[i].ID != id {
			t.Fatalf("work %d = %s, want %s (log-derived order: %#v)", i, result.Works[i].ID, id, result.Works)
		}
	}
}

// productScopeOf resolves the Product the wanted work IDs belong to, so the
// assertion helper stays usable from fixtures that name their own Product.
func productScopeOf(t *testing.T, s *Store, want []string) string {
	t.Helper()
	var product string
	if err := s.DatabaseForTesting().QueryRow(
		`SELECT pp.product_id FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=? ORDER BY pp.product_id LIMIT 1`, want[0]).Scan(&product); err != nil {
		t.Fatal(err)
	}
	return product
}

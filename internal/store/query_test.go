package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func seedQueryFixture(t *testing.T) *Store {
	t.Helper()
	s := openTemp(t)
	ctx := context.Background()
	if err := ApplyOperation(ctx, s, Operation{
		Events: []Event{
			operationEvent("q-product", "product.created", SubjectProduct, "prod", map[string]any{
				"display_name": "Product", "stage_maturity": "prototype", "stage_audience_commitment": "operator_only",
			}),
			operationEvent("q-project", "project.created", SubjectProject, "proj", map[string]any{"display_name": "Project"}),
			operationEvent("q-membership", "product_project.added", SubjectProduct, "prod", map[string]any{
				"product_id": "prod", "project_id": "proj", "role": "primary", "reason": "fixture", "expected_version": 1, "resulting_version": 2,
			}),
		},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "prod"): 0, VersionRef(SubjectProject, "proj"): 0},
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"blocker", "blocked"} {
		if err := ApplyOperation(ctx, s, Operation{
			Events: []Event{
				workCreatedEvent(id, "q-create-"+id),
				operationEvent("q-project-"+id, "work_project.added", SubjectWorkItem, id, map[string]any{
					"work_id": id, "project_id": "proj", "role": "primary", "reason": "fixture", "expected_version": 1, "resulting_version": 2,
				}),
			},
			ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, id): 0},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		relationAddedEvent("q-blocks", "blocks", "blocker", "blocked", 2, 3),
	}}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestQueryMigrationFiveAndIncomingIndex(t *testing.T) {
	s := openTemp(t)
	version, err := SchemaVersion(context.Background(), s.DB())
	if err != nil {
		t.Fatal(err)
	}
	if version != 6 {
		t.Fatalf("schema version = %d, want 6", version)
	}
	rows, err := s.DB().Query(`EXPLAIN QUERY PLAN SELECT work_id_from FROM relations WHERE work_id_to = ? AND kind = ?`, "blocked", "blocks")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan string
	for rows.Next() {
		var id, parent, notused int
		if err := rows.Scan(&id, &parent, &notused, &plan); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(plan, "idx_relations_to_kind") {
			return
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatalf("incoming relation query did not use idx_relations_to_kind")
}

func TestQueryQ1CarriesUniversalMetadata(t *testing.T) {
	s := seedQueryFixture(t)
	result, err := s.QueryQ1(context.Background(), Q1Request{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if result.QueryID != "PM1.Q1" || result.ContractVersion != "PM1/1.0" || result.Authority != "authoritative" {
		t.Fatalf("metadata = %#v", result.ResultMeta)
	}
	if result.Freshness.ObservedAt == "" || result.NextCursor != nil || result.Omissions == nil || result.Warnings == nil {
		t.Fatalf("metadata fields not populated: %#v", result.ResultMeta)
	}
	if len(result.Products) != 1 || result.Products[0].ID != "prod" {
		t.Fatalf("products = %#v", result.Products)
	}
}

func TestQueryRejectsInvalidFilterAndCursorBinding(t *testing.T) {
	s := seedQueryFixture(t)
	_, err := s.QueryQ3(context.Background(), Q3Request{Product: "prod", LifecycleStates: []string{"blocked"}})
	assertFailureKind(t, err, KindInvalidFilter)
	_, err = s.QueryQ3(context.Background(), Q3Request{Product: "prod", LifecycleStates: []string{"needed"}, Cursor: "not-base64"})
	assertFailureKind(t, err, KindInvalidCursor)
	page, err := s.QueryQ3(context.Background(), Q3Request{Product: "prod", LifecycleStates: []string{"needed"}, Limit: 1})
	if err != nil || page.NextCursor == nil {
		t.Fatalf("cursor seed = %#v, err %v", page, err)
	}
	_, err = s.QueryQ3(context.Background(), Q3Request{Product: "prod", LifecycleStates: []string{"in_progress"}, Limit: 1, Cursor: *page.NextCursor})
	assertFailureKind(t, err, KindInvalidCursor)
	malformed, err := json.Marshal(q3Cursor{Version: 1, QueryID: "PM1.Q3", Product: "prod", States: []string{"needed"}, Order: "priority:asc,relevant_time:desc,id:asc", Priority: 1, Timestamp: "not-a-timestamp", ID: "blocked"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.QueryQ3(context.Background(), Q3Request{Product: "prod", LifecycleStates: []string{"needed"}, Limit: 1, Cursor: base64.RawURLEncoding.EncodeToString(malformed)})
	assertFailureKind(t, err, KindInvalidCursor)
}

func TestQueryQ6ValidatesExplicitProductForWork(t *testing.T) {
	s := seedQueryFixture(t)
	_, err := s.QueryQ6(context.Background(), Q6Request{Product: "missing", Work: "blocked"})
	assertFailureKind(t, err, KindUnknownScope)
	_, err = s.QueryQ6(context.Background(), Q6Request{Product: "prod", Project: "missing", Work: "blocked"})
	assertFailureKind(t, err, KindUnknownScope)
}

func TestTerminalOnlyQ3RecognizesEveryTerminalFilter(t *testing.T) {
	for _, tc := range []struct {
		states []string
		want   bool
	}{
		{states: []string{"completed"}, want: true},
		{states: []string{"cancelled", "superseded"}, want: true},
		{states: []string{"needed", "completed"}, want: false},
		{states: nil, want: false},
	} {
		if got := terminalOnlyQ3(tc.states); got != tc.want {
			t.Fatalf("terminalOnlyQ3(%v) = %t, want %t", tc.states, got, tc.want)
		}
	}
}

func TestQueryDeduplicatesMultipleProjectMemberships(t *testing.T) {
	s := seedQueryFixture(t)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{
		operationEvent("q-project-2", "project.created", SubjectProject, "proj-2", map[string]any{"display_name": "Project 2"}),
		operationEvent("q-product-project-2", "product_project.added", SubjectProduct, "prod", map[string]any{
			"product_id": "prod", "project_id": "proj-2", "role": "secondary", "reason": "fixture", "expected_version": 2, "resulting_version": 3,
		}),
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProject, "proj-2"): 0, VersionRef(SubjectProduct, "prod"): 2}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{
		operationEvent("q-work-project-2", "work_project.added", SubjectWorkItem, "blocked", map[string]any{
			"work_id": "blocked", "project_id": "proj-2", "role": "secondary", "reason": "fixture", "expected_version": 2, "resulting_version": 3,
		}),
	}, ExpectedVersions: workVersion("blocked", 2)}); err != nil {
		t.Fatal(err)
	}
	result, err := s.QueryQ3(context.Background(), Q3Request{Product: "prod", LifecycleStates: []string{"needed"}})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, item := range result.Items {
		if seen[item.ID] {
			t.Fatalf("duplicate work item %q", item.ID)
		}
		seen[item.ID] = true
	}
}

func TestQueryQ4DerivesAndResolvesBlockers(t *testing.T) {
	s := seedQueryFixture(t)
	result, err := s.QueryQ4(context.Background(), Q4Request{Product: "prod"})
	if err != nil || len(result.Items) != 1 || result.Items[0].Blockers[0].ID != "blocker" {
		t.Fatalf("Q4 = %#v, err %v", result, err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{
		workTransitionEvent("q-resolve", "blocker", "needed", "completed", 3, 4),
	}, ExpectedVersions: workVersion("blocker", 3)}); err != nil {
		t.Fatal(err)
	}
	result, err = s.QueryQ4(context.Background(), Q4Request{Product: "prod"})
	if err != nil || len(result.Items) != 0 {
		t.Fatalf("resolved Q4 = %#v, err %v", result, err)
	}
}

func TestQueryQ8DependsOnUsesInverseWithoutMirroredRow(t *testing.T) {
	s := seedQueryFixture(t)
	result, err := s.QueryQ8(context.Background(), Q8Request{Work: "blocked", RelationKinds: []string{"depends_on"}, Direction: "outgoing"})
	if err != nil || len(result.Edges) != 1 || result.Edges[0].Source != "blocked" || result.Edges[0].Target != "blocker" {
		t.Fatalf("Q8 = %#v, err %v", result, err)
	}
	assertTableCount(t, s, "relations", 1)
}

func TestQuerySpecializedResultsCarryUniversalPayload(t *testing.T) {
	s := seedQueryFixture(t)
	cases := []struct {
		name  string
		value any
		field string
	}{
		{name: "Q1", value: mustQueryQ1(t, s), field: "products"},
		{name: "Q6-work", value: mustQueryQ6(t, s), field: "work"},
		{name: "Q7", value: mustQueryQ7(t, s), field: "events"},
		{name: "Q8", value: mustQueryQ8(t, s), field: "edges"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			var result map[string]any
			if err := json.Unmarshal(encoded, &result); err != nil {
				t.Fatal(err)
			}
			if payload, ok := result["result"]; !ok || payload == nil {
				t.Fatalf("missing non-null result payload: %#v", result)
			}
			if payload, ok := result[tc.field]; !ok || payload == nil {
				t.Fatalf("missing specialized %s payload: %#v", tc.field, result)
			}
		})
	}
}

func mustQueryQ1(t *testing.T, s *Store) Q1Result {
	t.Helper()
	result, err := s.QueryQ1(context.Background(), Q1Request{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mustQueryQ6(t *testing.T, s *Store) Q6Result {
	t.Helper()
	result, err := s.QueryQ6(context.Background(), Q6Request{Work: "blocked"})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mustQueryQ7(t *testing.T, s *Store) Q7Result {
	t.Helper()
	result, err := s.QueryQ7(context.Background(), Q7Request{Work: "blocked"})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mustQueryQ8(t *testing.T, s *Store) Q8Result {
	t.Helper()
	result, err := s.QueryQ8(context.Background(), Q8Request{Work: "blocked", RelationKinds: []string{"blocked_by"}, Direction: "outgoing"})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func q4WorkCreatedAt(id string, priority int64, when time.Time) Event {
	e := operationEvent("q4-create-"+id, "work.created", SubjectWorkItem, id, map[string]any{
		"work_kind": "task", "title": id, "priority": priority,
	})
	e.PayloadVersion = 2
	e.OccurredAt = when
	return e
}

func addQ4Work(t *testing.T, s *Store, id string, priority int64, when time.Time) {
	t.Helper()
	if err := ApplyOperation(context.Background(), s, Operation{
		Events: []Event{
			q4WorkCreatedAt(id, priority, when),
			operationEvent("q4-project-"+id, "work_project.added", SubjectWorkItem, id, map[string]any{
				"work_id": id, "project_id": "proj", "role": "primary", "reason": "q4 fixture", "expected_version": 1, "resulting_version": 2,
			}),
		},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, id): 0},
	}); err != nil {
		t.Fatal(err)
	}
}

func addQ4Blocker(t *testing.T, s *Store, id, blocked string, when time.Time) {
	t.Helper()
	if err := ApplyOperation(context.Background(), s, Operation{
		Events: []Event{
			q4WorkCreatedAt(id, 10, when),
			operationEvent("q4-project-"+id, "work_project.added", SubjectWorkItem, id, map[string]any{
				"work_id": id, "project_id": "proj", "role": "primary", "reason": "q4 fixture", "expected_version": 1, "resulting_version": 2,
			}),
		},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, id): 0},
	}); err != nil {
		t.Fatal(err)
	}
	relation := relationAddedEvent("q4-blocks-"+id+"-"+blocked, "blocks", id, blocked, 2, 3)
	relation.OccurredAt = when
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{relation}, ExpectedVersions: workVersion(id, 2)}); err != nil {
		t.Fatal(err)
	}
}

func seedQ4LimitFixture(t *testing.T, secondBlockerWhen time.Time) *Store {
	t.Helper()
	s := seedQueryFixture(t)
	addQ4Work(t, s, "blocked-z", 1, time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC))
	addQ4Work(t, s, "blocked-a", 1, time.Date(2026, 1, 11, 0, 0, 0, 0, time.UTC))
	addQ4Blocker(t, s, "z-blocker-1", "blocked-z", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	addQ4Blocker(t, s, "z-blocker-2", "blocked-z", time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
	addQ4Blocker(t, s, "z-blocker-3", "blocked-z", time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC))
	addQ4Blocker(t, s, "a-blocker-1", "blocked-a", secondBlockerWhen)
	return s
}

func TestQueryQ4LimitBoundsWorksBeforeBlockerJoin(t *testing.T) {
	s := seedQ4LimitFixture(t, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
	result, err := s.QueryQ4(context.Background(), Q4Request{Product: "prod", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].ID != "blocked-z" || len(result.Items[0].Blockers) != 3 {
		t.Fatalf("Q4 limit=1 = %#v", result.Items)
	}
}

func TestQueryQ4LimitDoesNotDropSecondWork(t *testing.T) {
	s := seedQ4LimitFixture(t, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
	result, err := s.QueryQ4(context.Background(), Q4Request{Product: "prod", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 || result.Items[0].ID != "blocked-z" || result.Items[1].ID != "blocked-a" || len(result.Items[0].Blockers) != 3 {
		t.Fatalf("Q4 limit=2 = %#v", result.Items)
	}
}

func TestQueryQ4OrdersOldestBlockerThenStableWorkID(t *testing.T) {
	s := seedQ4LimitFixture(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	result, err := s.QueryQ4(context.Background(), Q4Request{Product: "prod", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 || result.Items[0].ID != "blocked-a" || result.Items[1].ID != "blocked-z" {
		t.Fatalf("Q4 ordering = %#v", result.Items)
	}
}

func TestQueryQ4ExcludesTerminalBlockersAndReportsCaps(t *testing.T) {
	s := seedQ4LimitFixture(t, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workTransitionEvent("q4-resolve-z1", "z-blocker-1", "needed", "completed", 3, 4)}, ExpectedVersions: workVersion("z-blocker-1", 3)}); err != nil {
		t.Fatal(err)
	}
	result, err := s.QueryQ4(context.Background(), Q4Request{Product: "prod", Limit: 2, EdgeLimit: 2})
	if err != nil {
		t.Fatal(err)
	}
	var z WorkItem
	for _, item := range result.Items {
		if item.ID == "blocked-z" {
			z = item
		}
	}
	if z.ID == "" || len(z.Blockers) != 1 || z.Blockers[0].ID == "z-blocker-1" {
		t.Fatalf("Q4 terminal/cap result = %#v", result.Items)
	}
	if len(result.Warnings) == 0 || len(result.Omissions) == 0 {
		t.Fatalf("Q4 cap metadata = %#v", result.ResultMeta)
	}
}

func TestQueryQ4RejectsUnboundedGraphRequests(t *testing.T) {
	s := seedQueryFixture(t)
	for _, req := range []Q4Request{
		{Product: "prod", Depth: 4},
		{Product: "prod", NodeLimit: q4MaxNodeLimit + 1},
		{Product: "prod", EdgeLimit: q4MaxEdgeLimit + 1},
	} {
		_, err := s.QueryQ4(context.Background(), req)
		assertFailureKind(t, err, KindInvalidFilter)
	}
}

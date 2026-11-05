package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestQueryMigrationFiveAndIncomingIndex(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	version, err := readSchemaManifestVersion(context.Background(), s.DatabaseForTesting())
	if err != nil {
		t.Fatal(err)
	}
	if version != CurrentSchemaVersion() {
		t.Fatalf("schema version = %d, want %d", version, CurrentSchemaVersion())
	}
	rows, err := s.DatabaseForTesting().Query(`EXPLAIN QUERY PLAN SELECT work_id_from FROM relations WHERE work_id_to = ? AND kind = ?`, "blocked", "blocks")
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
	t.Parallel()
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

// TestQueryReadSnapshotIsLockFree pins design-constraints §4, "Reads are
// lock-free": a live read snapshot must not take the write lock. The store's
// data source name sets _txlock=immediate for mutations, so a snapshot opened
// without ReadOnly issues BEGIN IMMEDIATE and queues behind any writer in
// another process, then fails as an unavailable read once busy_timeout lapses.
func TestQueryReadSnapshotIsLockFree(t *testing.T) {
	t.Parallel()
	s := seedQueryFixture(t)
	writer, err := sql.Open(driverName, dataSourceName(s.Path()))
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	writeTx, err := writer.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("writer BEGIN IMMEDIATE: %v", err)
	}
	defer func() { _ = writeTx.Rollback() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := s.QueryQ1(ctx, Q1Request{Limit: 20})
	if err != nil {
		t.Fatalf("read snapshot while another connection holds the write lock: %v", err)
	}
	if len(result.Products) != 1 {
		t.Fatalf("products = %#v", result.Products)
	}
}

func TestQueryRejectsInvalidFilterAndCursorBinding(t *testing.T) {
	t.Parallel()
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
	malformed, err := json.Marshal(q3Cursor{Version: 1, QueryID: "PM1.Q3", Product: "prod", States: []string{"needed"}, Order: "relevant_time:desc,id:asc", Timestamp: "not-a-timestamp", ID: "blocked"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.QueryQ3(context.Background(), Q3Request{Product: "prod", LifecycleStates: []string{"needed"}, Limit: 1, Cursor: base64.RawURLEncoding.EncodeToString(malformed)})
	assertFailureKind(t, err, KindInvalidCursor)
}

func TestQueryDeduplicatesMultipleProjectMemberships(t *testing.T) {
	t.Parallel()
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

// An inverse label reads a stored edge backwards. The store keeps one row, not a
// mirrored pair, so the inverse is a read projection and never a second relation.
func TestQueryQ8InverseLabelReadsWithoutMirroredRow(t *testing.T) {
	t.Parallel()
	s := seedQueryFixture(t)
	result, err := s.QueryQ8(context.Background(), Q8Request{Work: "blocked", RelationKinds: []string{"blocked_by"}, Direction: "outgoing"})
	if err != nil || len(result.Edges) != 1 || result.Edges[0].Kind != "blocked_by" || result.Edges[0].Source != "blocked" || result.Edges[0].Target != "blocker" || result.Edges[0].Depth != 1 {
		t.Fatalf("Q8 = %#v, err %v", result, err)
	}
	assertTableCount(t, s, "relations", 1)
}

func TestQueryQ8DependsOnUsesForwardStoredKind(t *testing.T) {
	t.Parallel()
	s := seedQueryFixture(t)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{
		relationAddedEvent("q-depends-on", "depends_on", "blocked", "blocker", 2, 3),
	}}); err != nil {
		t.Fatal(err)
	}
	result, err := s.QueryQ8(context.Background(), Q8Request{Work: "blocked", RelationKinds: []string{"depends_on"}, Direction: "outgoing"})
	if err != nil || len(result.Edges) != 1 || result.Edges[0].Source != "blocked" || result.Edges[0].Target != "blocker" || result.Edges[0].Depth != 1 {
		t.Fatalf("Q8 = %#v, err %v", result, err)
	}
	assertTableCount(t, s, "relations", 2)
}

func TestQueryQ8RejectsNonTransitiveDepth(t *testing.T) {
	t.Parallel()
	s := seedQueryFixture(t)
	_, err := s.QueryQ8(context.Background(), Q8Request{Work: "blocked", RelationKinds: []string{"implements"}, Direction: "outgoing", Depth: 2})
	assertFailureKind(t, err, KindInvalidFilter)
	if err == nil || !strings.Contains(err.Error(), "relation kind implements is not transitive") {
		t.Fatalf("Q8 non-transitive depth error = %v", err)
	}
}

func TestQuerySpecializedResultsCarryUniversalPayload(t *testing.T) {
	t.Parallel()
	s := seedQueryFixture(t)
	cases := []struct {
		name  string
		value any
		field string
	}{
		{name: "Q1", value: mustQueryQ1(t, s), field: "products"},
		{name: "Q7", value: mustQueryQ7(t, s), field: "entries"},
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

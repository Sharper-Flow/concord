package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
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
	version, err := SchemaVersion(context.Background(), s.DatabaseForTesting())
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

func TestLauncherProductAndSearchProjectionsAreBoundedAndScoped(t *testing.T) {
	s := seedQueryFixture(t)
	defer s.Close()
	ctx := context.Background()
	result, err := s.QueryLauncherProduct(ctx, LauncherProductRequest{Product: "prod", Limit: 20, Depth: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Works) != 2 {
		t.Fatalf("works=%#v", result.Works)
	}
	var blocked LauncherWork
	for _, item := range result.Works {
		if item.ID == "blocked" {
			blocked = item
		}
	}
	if !blocked.Blocked || blocked.Ready || len(blocked.Blockers) != 1 || blocked.Blockers[0].ID != "blocker" {
		t.Fatalf("blocked=%#v", blocked)
	}
	if blocked.ProjectCount != 1 {
		t.Fatalf("project count=%d", blocked.ProjectCount)
	}
	foundInverse := false
	for _, edge := range result.Edges {
		if edge.Kind == "blocked_by" && edge.Source == "blocked" && edge.Target == "blocker" {
			foundInverse = true
		}
	}
	if !foundInverse {
		t.Fatalf("inverse edges=%#v", result.Edges)
	}
	limited, err := s.QueryLauncherProduct(ctx, LauncherProductRequest{Product: "prod", Limit: 1, Depth: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(limited.Works) != 1 || !containsString(limited.Omissions, "Product work omitted by launcher limit") {
		t.Fatalf("work limit must remain visible: %#v", limited)
	}
	search, err := s.QueryLauncherSearch(ctx, LauncherSearchRequest{Product: "prod", Query: "blocked", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(search.Works) != 1 || search.Works[0].ID != "blocked" {
		t.Fatalf("work matches were lost with unavailable knowledge: %#v", search.Works)
	}
	if search.KnowledgeAuthority != "unavailable" || !containsString(search.KnowledgeOmissions, "knowledge_home_unavailable") {
		t.Fatalf("knowledge availability was not typed: %#v", search)
	}
	home := KnowledgeHome{HomeProjectID: "knowledge-home", HomeLocatorID: "knowledge-locator", RepoPath: initKnowledgeRepo(t), HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "prod", home, "proj")
	search, err = s.QueryLauncherSearch(ctx, LauncherSearchRequest{Product: "prod", Query: "blocked", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if search.QueryID != "launcher.search" || search.ResolvedScope.ProductID != "prod" || len(search.Works) != 1 || search.Works[0].ID != "blocked" {
		t.Fatalf("Product-scoped search=%#v", search)
	}
	if search.SourceVersionWatermark == 0 || search.KnowledgeWatermark == "" || search.KnowledgeAuthority == "" {
		t.Fatalf("search watermarks missing: %#v", search)
	}
}

func TestLauncherProductDepthThreeRepresentativeP99(t *testing.T) {
	if productRowSkipPerformanceUnderRace {
		t.Skip("representative latency threshold is measured without race instrumentation")
	}
	s := openTemp(t)
	defer s.Close()
	var statements strings.Builder
	statements.WriteString("INSERT INTO fold_guard(active) VALUES(1);")
	statements.WriteString("INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('launcher-perf','Launcher Perf','prototype','operator_only',1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z');")
	statements.WriteString("INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('launcher-perf-project','Launcher Perf Project',1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z');")
	statements.WriteString("INSERT INTO product_projects(product_id,project_id,role) VALUES('launcher-perf','launcher-perf-project','primary');")
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("launcher-perf-work-%03d", i)
		fmt.Fprintf(&statements, "INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES('%s','task','Work %03d','needed',%d,1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z');", id, i, i)
		fmt.Fprintf(&statements, "INSERT INTO work_projects(work_id,project_id,role) VALUES('%s','launcher-perf-project','primary');", id)
		if i > 0 {
			previous := fmt.Sprintf("launcher-perf-work-%03d", i-1)
			fmt.Fprintf(&statements, "INSERT INTO relations(work_id_from,work_id_to,kind,created_at) VALUES('%s','%s','parent','2026-08-01T00:00:00Z');", previous, id)
		}
	}
	statements.WriteString("DELETE FROM fold_guard;")
	if _, err := s.DatabaseForTesting().Exec(statements.String()); err != nil {
		t.Fatal(err)
	}
	const samples = 100
	for i := 0; i < 10; i++ {
		if _, err := s.QueryLauncherProduct(context.Background(), LauncherProductRequest{Product: "launcher-perf", Limit: 100, Depth: 3}); err != nil {
			t.Fatal(err)
		}
	}
	durations := make([]time.Duration, 0, samples)
	for i := 0; i < samples; i++ {
		started := time.Now()
		if _, err := s.QueryLauncherProduct(context.Background(), LauncherProductRequest{Product: "launcher-perf", Limit: 100, Depth: 3}); err != nil {
			t.Fatal(err)
		}
		durations = append(durations, time.Since(started))
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p99 := durations[(99*len(durations)+99)/100-1]
	if !representativeP99WithinTarget(
		t,
		"C17 S2 depth-3 representative",
		p99,
		100*time.Millisecond,
		"100 work items, 99 structural edges",
		samples,
	) {
		t.Fatalf("C17 S2 depth-3 representative P99=%s exceeds 100ms target", p99)
	}
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

func TestQueryQ10AcceptsExactlyOneStableReferenceAndReturnsTypedStates(t *testing.T) {
	s := seedQueryFixture(t)
	home := KnowledgeHome{HomeProjectID: "home", HomeLocatorID: "locator", RepoPath: t.TempDir(), HeadRef: "HEAD"}
	authorizeKnowledgeLocator(t, s, home)
	result, err := s.QueryQ10(context.Background(), Q10Request{Work: "blocked", AllowDegraded: true, Home: home})
	if err != nil || result.Status != "not_compacted" {
		t.Fatalf("work-only result=%#v, err=%v", result, err)
	}
	result, err = s.QueryQ10(context.Background(), Q10Request{KnowledgeID: "missing-knowledge", AllowDegraded: true, Home: home})
	if err != nil || result.Status != "missing" || result.Result == nil || result.Result.Status != "missing" {
		t.Fatalf("knowledge-only missing result=%#v, err=%v", result, err)
	}
	for _, request := range []Q10Request{{}, {Work: "blocked", KnowledgeID: "knowledge"}} {
		_, err := s.QueryQ10(context.Background(), request)
		assertFailureKind(t, err, KindInvalidFilter)
	}
}

func TestQueryQ10ContainsKnowledgeInSelectedProduct(t *testing.T) {
	s := openTemp(t)
	insertArchivedKnowledge(t, s, "knowledge-b", "home", "locator", "missing.md", "missing", "missing", []string{"product-b"})
	result, err := s.QueryQ10(context.Background(), Q10Request{KnowledgeID: "knowledge-b", Product: "product-a", AllowDegraded: true, Home: KnowledgeHome{HomeProjectID: "home", HomeLocatorID: "locator", RepoPath: t.TempDir(), HeadRef: "HEAD"}})
	if err == nil {
		t.Fatalf("knowledge from another Product resolved: %#v", result)
	}
	assertFailureKind(t, err, KindUnknownScope)
}

func TestQueryQ10ReturnsAmbiguousAsTypedResult(t *testing.T) {
	s := openTemp(t)
	repo := initKnowledgeRepo(t)
	path := "docs/lessons/different.md"
	writeKnowledgeFile(t, repo, path, canonicalKnowledgeNote("different-id", "lesson", "2026-08-07T00:00:00Z", []string{"test"}))
	writeManifestFixture(t, repo, manifestFixtureFromFile(t, repo, "knowledge-id", "lesson", path, "published", "2026-08-07T00:00:00Z", "Durable lesson", "Durable summary", []string{"test"}, KnowledgeRecordScopes{Mode: "explicit"}))
	commitKnowledgeRepo(t, repo, "ambiguous knowledge")
	home := KnowledgeHome{HomeProjectID: "home", HomeLocatorID: "locator", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "prod-ambiguous", home)
	if err := s.RebuildKnowledgeIndex(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE archived_work SET title='tampered' WHERE id='knowledge-id'; DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	_, err := s.QueryQ10(context.Background(), Q10Request{KnowledgeID: "knowledge-id", Home: home})
	assertFailureKind(t, err, KindKnowledgeMissing)
}

func insertArchivedKnowledge(t *testing.T, s *Store, id, homeProject, homeLocator, path, commit, hash string, products []string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := s.DatabaseForTesting().ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
			t.Errorf("remove fold guard: %v", err)
		}
	}()
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT INTO archived_work (id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,home_project_id,home_locator_id,note_path,commit_oid,content_hash) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, "lesson", id, "2026-08-07T00:00:00Z", "published", "[]", "completed", 1, "summary", homeProject, homeLocator, path, commit, hash); err != nil {
		t.Fatal(err)
	}
	for _, product := range products {
		if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT INTO archived_work_products(work_id,product_id) VALUES (?,?)`, id, product); err != nil {
			t.Fatal(err)
		}
	}
}

func TestQueryQ6ProjectPaginationBoundsContinuationAndTamper(t *testing.T) {
	s := seedQueryFixture(t)
	for _, id := range []string{"scope-a", "scope-b", "scope-c"} {
		addQ4Work(t, s, id, 1, time.Now().UTC())
	}
	first, err := s.QueryQ6(context.Background(), Q6Request{Project: "proj", Limit: 2})
	if err != nil || len(first.Items) != 2 || first.NextCursor == nil {
		t.Fatalf("first scope page=%#v, err=%v", first, err)
	}
	second, err := s.QueryQ6(context.Background(), Q6Request{Project: "proj", Limit: 2, Cursor: *first.NextCursor})
	if err != nil || len(second.Items) == 0 || second.Items[0].ID == first.Items[0].ID {
		t.Fatalf("continuation scope page=%#v, err=%v", second, err)
	}
	tampered := *first.NextCursor + "x"
	_, err = s.QueryQ6(context.Background(), Q6Request{Project: "proj", Limit: 2, Cursor: tampered})
	assertFailureKind(t, err, KindInvalidCursor)
	_, err = s.QueryQ6(context.Background(), Q6Request{Project: "proj", Limit: 0, Cursor: *first.NextCursor})
	if err != nil {
		t.Fatalf("default bounded limit rejected continuation: %v", err)
	}
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

// An inverse label reads a stored edge backwards. The store keeps one row, not a
// mirrored pair, so the inverse is a read projection and never a second relation.
func TestQueryQ8InverseLabelReadsWithoutMirroredRow(t *testing.T) {
	s := seedQueryFixture(t)
	result, err := s.QueryQ8(context.Background(), Q8Request{Work: "blocked", RelationKinds: []string{"blocked_by"}, Direction: "outgoing"})
	if err != nil || len(result.Edges) != 1 || result.Edges[0].Kind != "blocked_by" || result.Edges[0].Source != "blocked" || result.Edges[0].Target != "blocker" || result.Edges[0].Depth != 1 {
		t.Fatalf("Q8 = %#v, err %v", result, err)
	}
	assertTableCount(t, s, "relations", 1)
}

func TestQueryQ8DependsOnUsesForwardStoredKind(t *testing.T) {
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
	s := seedQueryFixture(t)
	_, err := s.QueryQ8(context.Background(), Q8Request{Work: "blocked", RelationKinds: []string{"implements"}, Direction: "outgoing", Depth: 2})
	assertFailureKind(t, err, KindInvalidFilter)
	if err == nil || !strings.Contains(err.Error(), "relation kind implements is not transitive") {
		t.Fatalf("Q8 non-transitive depth error = %v", err)
	}
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

// The authoritative knowledge branch runs coverage omissions while the
// launcher search transaction is open. With one pooled connection
// (store.go SetMaxOpenConns(1)) a nested s.db query there parks on the pool
// forever — this test would hang, not fail, without tx scoping.
func TestLauncherSearchAuthoritativeKnowledgeDoesNotDeadlock(t *testing.T) {
	s := seedQueryFixture(t)
	defer s.Close()
	ctx := context.Background()

	repo := initKnowledgeRepo(t)
	path := "docs/lessons/durable.md"
	writeKnowledgeFile(t, repo, path, canonicalKnowledgeNote("durable-lesson", "lesson", "2026-08-07T00:00:00Z", []string{"sqlite"}))
	writeManifestFixture(t, repo, manifestFixtureFromFile(t, repo, "durable-lesson", "lesson", path, "published", "2026-08-07T00:00:00Z", "Durable lesson", "Durable summary", []string{"sqlite"}, KnowledgeRecordScopes{Mode: "home"}))
	commitKnowledgeRepo(t, repo, "durable lesson")
	home := KnowledgeHome{HomeProjectID: "knowledge-home", HomeLocatorID: "knowledge-locator", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "prod", home, "proj")
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}

	search, err := s.QueryLauncherSearch(ctx, LauncherSearchRequest{Product: "prod", Query: "blocked", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if search.KnowledgeAuthority != "authoritative" {
		t.Fatalf("authority=%q omissions=%v", search.KnowledgeAuthority, search.KnowledgeOmissions)
	}
}

package launcher

import (
	"context"
	"strings"
	"testing"
	"time"
)

type countingPort struct {
	requests []ReadRequest
	snapshot Snapshot
	err      error
}

func (p *countingPort) Read(_ context.Context, request ReadRequest) (Snapshot, error) {
	p.requests = append(p.requests, request)
	if p.err != nil {
		return Snapshot{}, p.err
	}
	return p.snapshot, nil
}

func TestModelReadsOnlyOnEntrySubmitAndRefresh(t *testing.T) {
	port := &countingPort{snapshot: Snapshot{Screen: ScreenPortfolio, Coverage: "authoritative"}}
	model := New(port)
	model.Resize(80, 24)
	if len(port.requests) != 0 {
		t.Fatal("resize caused a read")
	}
	if err := model.Enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := model.SubmitQuery(context.Background(), "blocked"); err != nil {
		t.Fatal(err)
	}
	if err := model.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(port.requests) != 2 || port.requests[0].Kind != ReadPortfolio || port.requests[1].Kind != ReadPortfolio {
		t.Fatalf("requests=%#v", port.requests)
	}
}

func TestSelectingProductReadsTheComposedWorkReadAndBackDoesNotRead(t *testing.T) {
	port := &countingPort{snapshot: Snapshot{Screen: ScreenPortfolio, Rows: []ProductRow{{ID: "p-1", Name: "One"}}, Coverage: "authoritative"}}
	model := New(port)
	if err := model.Enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	port.snapshot = Snapshot{Screen: ScreenProduct, Coverage: "authoritative"}
	if err := model.SelectProduct(context.Background(), "p-1"); err != nil {
		t.Fatal(err)
	}
	if got := model.Snapshot(); got.Screen != ScreenProduct || got.AmbientProduct != "p-1" {
		t.Fatalf("product = %#v", got)
	}
	if err := model.Back(); err != nil {
		t.Fatal(err)
	}
	if got := model.Snapshot(); got.Screen != ScreenPortfolio || got.AmbientProduct != "" {
		t.Fatalf("back snapshot = %#v", got)
	}
	if len(port.requests) != 2 || port.requests[1].Kind != ReadDomains {
		t.Fatalf("selection/back read requests = %#v, want one composed Product read", port.requests)
	}
}

func TestBackRestoresPortfolioSnapshotRows(t *testing.T) {
	portfolio := Snapshot{
		Screen:   ScreenPortfolio,
		Coverage: "authoritative",
		Rows:     []ProductRow{{ID: "p-1", Name: "One"}, {ID: "p-2", Name: "Two"}},
	}
	port := &countingPort{snapshot: portfolio}
	model := New(port)
	if err := model.Enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	port.snapshot = Snapshot{
		Screen:         ScreenProduct,
		AmbientProduct: "p-1",
		Coverage:       "authoritative",
		Ranked:         []RankedWork{{ID: "work-1", Title: "S2 row"}},
	}
	if err := model.SelectProduct(context.Background(), "p-1"); err != nil {
		t.Fatal(err)
	}
	if err := model.Back(); err != nil {
		t.Fatal(err)
	}
	got := model.Snapshot()
	if got.Screen != ScreenPortfolio || got.AmbientProduct != "" {
		t.Fatalf("restored portfolio state = %#v", got)
	}
	if len(got.Rows) != len(portfolio.Rows) || got.Rows[0].ID != "p-1" || got.Rows[1].ID != "p-2" {
		t.Fatalf("restored portfolio rows = %#v", got.Rows)
	}
}

func TestBackAtPortfolioIsNoOp(t *testing.T) {
	model := New(&countingPort{})
	before := model.Snapshot()
	if err := model.Back(); err != nil {
		t.Fatal(err)
	}
	if got := model.Snapshot(); got.Screen != before.Screen || got.AmbientProduct != before.AmbientProduct || len(got.Rows) != len(before.Rows) {
		t.Fatalf("first S1 back changed state = %#v", got)
	}
	if err := model.Back(); err != nil {
		t.Fatal(err)
	}
	if got := model.Snapshot(); got.Screen != ScreenPortfolio {
		t.Fatalf("second S1 back underflowed to %#v", got)
	}
}

func TestSnapshotCopiesRows(t *testing.T) {
	port := &countingPort{snapshot: Snapshot{Rows: []ProductRow{{ID: "p-1", Name: "One"}}}}
	model := New(port)
	if err := model.Enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows := model.Snapshot().Rows
	rows[0].Name = "mutated"
	if got := model.Snapshot().Rows[0].Name; got != "One" {
		t.Fatalf("snapshot exposed mutable row: %q", got)
	}
}

func TestProjectionIsDeterministicAndCarriesAttentionMarkers(t *testing.T) {
	snapshot := Snapshot{Screen: ScreenPortfolio, AmbientProduct: "Concord", Watermark: "w42", ObservedAt: "2m", Reliance: "blocked", Coverage: "authoritative", Rows: []ProductRow{{Name: "Launcher", Stage: "in_progress", Reliance: "blocked", Actions: 3, Focus: "Fix launcher input"}}}
	first, second := Project(snapshot, 80, fixtureMeasure(nil)), Project(snapshot, 80, fixtureMeasure(nil))
	if len(first.Rows) != 1 || len(first.Rows[0]) != 2 {
		t.Fatalf("projection rows=%#v", first.Rows)
	}
	for i := range first.Rows {
		for j := range first.Rows[i] {
			if first.Rows[i][j] != second.Rows[i][j] {
				t.Fatal("projection changed between renders")
			}
		}
	}
	if first.Columns[0] != "Product" || first.Columns[1] != "Actions" {
		t.Fatalf("portfolio columns=%v", first.Columns)
	}
	if first.Markers[0] != "!" {
		t.Fatalf("an abnormal reliance carried no attention marker: %#v", first.Markers)
	}
	clean := Snapshot{Screen: ScreenPortfolio, Coverage: "authoritative", Rows: []ProductRow{{Name: "Launcher", Reliance: "clear", Actions: 3}}}
	if got := Project(clean, 80, fixtureMeasure(nil)); got.Markers[0] != "" {
		t.Fatalf("a healthy product row carried a marker: %#v", got.Markers)
	}
}

func TestWorkScreenLeavesTheProjection(t *testing.T) {
	work := Project(Snapshot{Screen: ScreenWork, AmbientProduct: "p-1"}, 80, fixtureMeasure(nil))
	if len(work.Columns) != 0 || len(work.Rows) != 0 {
		t.Fatalf("the removed work screen still projects a table: %#v", work)
	}
}

func TestWorkListProjectionIsRecencyOrdered(t *testing.T) {
	snapshot := Snapshot{Screen: ScreenProduct, AmbientProduct: "p-1", Coverage: "authoritative", Ranked: []RankedWork{
		{ID: "work-old", Title: "Old", Lifecycle: "in_progress", UpdatedAt: "2026-09-20T08:00:00Z"},
		{ID: "work-new", Title: "New", Lifecycle: "in_progress", UpdatedAt: "2026-09-25T08:00:00Z"},
		{ID: "work-mid", Title: "Mid", Lifecycle: "needed", Ready: true, UpdatedAt: "2026-09-22T08:00:00Z"},
		{ID: "work-unstamped", Title: "No stamp", Lifecycle: "needed"},
	}}
	projection := Project(snapshot, 120, fixtureMeasure(nil))
	// The row carries the title, not the store ID: the number and title
	// identify the row on screen.
	wantOrder := []string{"New", "Mid", "Old", "No stamp"}
	if len(projection.Rows) != len(wantOrder) {
		t.Fatalf("work list rows=%#v", projection.Rows)
	}
	for i, want := range wantOrder {
		if !strings.Contains(projection.Rows[i][0], want) {
			t.Fatalf("row %d = %q, want the %q row (most recently updated first)", i, projection.Rows[i][0], want)
		}
	}
}

func TestSortRankedByRecencyKeepsEqualKeysAndDoesNotMutate(t *testing.T) {
	ranked := []RankedWork{
		{ID: "work-a", UpdatedAt: "2026-09-20T08:00:00Z"},
		{ID: "work-b", UpdatedAt: "2026-09-20T08:00:00Z"},
		{ID: "work-unstamped"},
	}
	sorted := SortRankedByRecency(ranked)
	if sorted[0].ID != "work-a" || sorted[1].ID != "work-b" || sorted[2].ID != "work-unstamped" {
		t.Fatalf("sort order = %v", []string{sorted[0].ID, sorted[1].ID, sorted[2].ID})
	}
	if ranked[0].ID != "work-a" {
		t.Fatal("recency sort mutated the input slice")
	}
}

func TestRelativeTimeRendersCompactAges(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		updatedAt string
		want      string
	}{
		{"", "-"},
		{"not-a-timestamp", "-"},
		{"2026-09-26T11:59:30Z", "now"},
		{"2026-09-26T11:30:00Z", "30m"},
		{"2026-09-26T09:00:00Z", "3h"},
		{"2026-09-20T12:00:00Z", "6d"},
		{"2026-05-01T12:00:00Z", "4mo"},
		{"2024-06-01T12:00:00Z", "2y"},
	}
	for _, tc := range cases {
		if got := RelativeTime(tc.updatedAt, now); got != tc.want {
			t.Fatalf("RelativeTime(%q) = %q, want %q", tc.updatedAt, got, tc.want)
		}
	}
}

func TestWorkListProjectionCarriesMarkerKeyTitleBlockersAndLive(t *testing.T) {
	snapshot := Snapshot{Screen: ScreenProduct, AmbientProduct: "p-1", Coverage: "authoritative", ActiveWorkOnly: true, Ranked: []RankedWork{
		{ID: "work-1", Title: "Linked and blocked", Lifecycle: "in_progress", LinearIssueKey: "CON-153", LinearIssueURL: "https://linear.example/CON-153", Live: 1, Blocked: true, Blockers: []Blocker{{ID: "work-9", IssueKey: "CON-99"}}},
		{ID: "work-2", Title: "Plain ready work", Lifecycle: "needed", Ready: true},
	}}
	projection := Project(snapshot, 200, fixtureMeasure(nil))
	if len(projection.Columns) != 3 || projection.Columns[1] != "Updated" || projection.Columns[2] != "Live" {
		t.Fatalf("work list columns=%v", projection.Columns)
	}
	blocked := strings.Join(projection.Rows[0], " ")
	for _, want := range []string{"1", "!BLOCKED", "CON-153", "Linked and blocked", "!CON-99", "yes"} {
		if !strings.Contains(blocked, want) {
			t.Fatalf("blocked row %q lost %q", blocked, want)
		}
	}
	if projection.Markers[0] != "!" {
		t.Fatalf("blocked row carried no attention marker: %#v", projection.Markers)
	}
	plain := strings.Join(projection.Rows[1], " ")
	for _, want := range []string{"2", "+READY", "Plain ready work", "no"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("plain row %q lost %q", plain, want)
		}
	}
	if strings.Contains(plain, "CON-") {
		t.Fatalf("unlinked row grew an issue key: %q", plain)
	}
}

func TestRankedDrillDownEmptyRendersTypedStateNotSilentBlank(t *testing.T) {
	authoritative := Project(Snapshot{Screen: ScreenProduct, Coverage: "authoritative"}, 80, fixtureMeasure(nil))
	if len(authoritative.Rows) != 1 || authoritative.Rows[0][0] != "authoritative-empty" {
		t.Fatalf("authoritative empty drill-down=%#v", authoritative.Rows)
	}
	degraded := Project(Snapshot{Screen: ScreenProduct, Coverage: "unavailable", StatusMessage: "unavailable: Product work omitted by launcher limit"}, 80, fixtureMeasure(nil))
	if len(degraded.Rows) != 1 || degraded.Rows[0][0] != "unavailable: Product work omitted by launcher limit" {
		t.Fatalf("degraded empty drill-down=%#v", degraded.Rows)
	}
	unreachable := Project(Snapshot{Screen: ScreenProduct, Coverage: "unreachable", StatusMessage: "database unavailable"}, 80, fixtureMeasure(nil))
	if len(unreachable.Rows) != 1 || unreachable.Rows[0][0] != "unavailable: database unavailable" {
		t.Fatalf("unreachable empty drill-down=%#v", unreachable.Rows)
	}
}

func TestRankedReadinessDerivesTerminalBeforeBlocked(t *testing.T) {
	if got := (RankedWork{Terminal: true, Blocked: true, Ready: true}).Readiness(); got != "terminal" {
		t.Fatalf("terminal readiness=%q", got)
	}
	if got := (RankedWork{Blocked: true}).Readiness(); got != "blocked" {
		t.Fatalf("blocked readiness=%q", got)
	}
	if got := (RankedWork{Ready: true}).Readiness(); got != "ready" {
		t.Fatalf("ready readiness=%q", got)
	}
	if got := (RankedWork{Lifecycle: "in_progress"}).Readiness(); got != "active" {
		t.Fatalf("active readiness=%q", got)
	}
}

func TestSelectingProductOpensOnTheWorkListAndKeepsDomainDataTyped(t *testing.T) {
	port := &countingPort{snapshot: Snapshot{Screen: ScreenPortfolio, Rows: []ProductRow{{ID: "p-1", Name: "One"}}, Coverage: "authoritative"}}
	model := New(port)
	if err := model.Enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	port.snapshot = Snapshot{Screen: ScreenProduct, Coverage: "authoritative", Ranked: []RankedWork{{ID: "work-1", Title: "One", Lifecycle: "in_progress"}}, Domains: DomainSection{Read: true, State: "authoritative", Domains: []DomainRow{{ID: "root", Name: "One", Home: true}}}}
	if err := model.SelectProduct(context.Background(), "p-1"); err != nil {
		t.Fatal(err)
	}
	if len(port.requests) != 2 || port.requests[1].Kind != ReadDomains {
		t.Fatalf("S2 entry requests=%#v", port.requests)
	}
	got := model.Snapshot()
	if len(got.Ranked) != 1 || got.Ranked[0].ID != "work-1" {
		t.Fatalf("S2 entry must seat the work list: %#v", got)
	}
	if len(got.Domains.Domains) != 1 || !got.Domains.Domains[0].Home {
		t.Fatalf("S2 entry must keep the Domain context typed: %#v", got.Domains)
	}
	cloned := model.Snapshot()
	cloned.Ranked[0].Title = "mutated"
	cloned.Domains.Domains[0].Name = "mutated"
	fresh := model.Snapshot()
	if fresh.Ranked[0].Title == "mutated" || fresh.Domains.Domains[0].Name == "mutated" {
		t.Fatal("Snapshot leaked Ranked or Domain rows by reference")
	}
}

// ambientPort answers every read from one fixed corpus, so two Models can share
// a single authority without either one supplying the other's state.
type ambientPort struct{}

func (p *ambientPort) Read(_ context.Context, request ReadRequest) (Snapshot, error) {
	switch request.Kind {
	case ReadPortfolio:
		return Snapshot{Screen: ScreenPortfolio, Coverage: "authoritative", Rows: []ProductRow{{ID: "p-1", Name: "One"}, {ID: "p-2", Name: "Two"}}}, nil
	default:
		return Snapshot{Screen: ScreenProduct, AmbientProduct: request.Product, Coverage: "authoritative", Domains: DomainSection{Read: true, State: "authoritative"}}, nil
	}
}

func TestTwoInstancesHoldDifferentAmbientProductsWithoutObservingEachOther(t *testing.T) {
	ctx := context.Background()
	port := &ambientPort{}
	first, second := New(port), New(port)
	for _, model := range []*Model{first, second} {
		if err := model.Enter(ctx); err != nil {
			t.Fatal(err)
		}
	}

	if err := first.SelectProduct(ctx, "p-1"); err != nil {
		t.Fatal(err)
	}
	if got := second.Snapshot(); got.Screen != ScreenPortfolio || got.AmbientProduct != "" {
		t.Fatalf("second instance observed the first instance's selection: %#v", got)
	}

	if err := second.SelectProduct(ctx, "p-2"); err != nil {
		t.Fatal(err)
	}
	if got := first.Snapshot(); got.AmbientProduct != "p-1" {
		t.Fatalf("first ambient Product = %q, want p-1", got.AmbientProduct)
	}
	if got := second.Snapshot(); got.AmbientProduct != "p-2" {
		t.Fatalf("second ambient Product = %q, want p-2", got.AmbientProduct)
	}
	if first.Handoff().ProductID != "p-1" || second.Handoff().ProductID != "p-2" {
		t.Fatalf("session handoffs crossed: first=%#v second=%#v", first.Handoff(), second.Handoff())
	}

	// A read on one instance resolves that instance's own ambient Product and
	// leaves the other instance's ambient Product untouched.
	if err := first.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if got := first.Snapshot(); got.AmbientProduct != "p-1" {
		t.Fatalf("refresh changed the first ambient Product to %q", got.AmbientProduct)
	}
	if got := second.Snapshot(); got.AmbientProduct != "p-2" {
		t.Fatalf("the first instance's refresh changed the second ambient Product to %q", got.AmbientProduct)
	}

	// Leaving the Product on one instance does not leave it on the other.
	if err := second.Back(); err != nil {
		t.Fatal(err)
	}
	if got := second.Snapshot(); got.Screen != ScreenPortfolio || got.AmbientProduct != "" {
		t.Fatalf("second instance after Back = %#v", got)
	}
	if got := first.Snapshot(); got.Screen != ScreenProduct || got.AmbientProduct != "p-1" {
		t.Fatalf("the second instance's Back changed the first instance: %#v", got)
	}
}

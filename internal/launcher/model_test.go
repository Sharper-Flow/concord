package launcher

import (
	"context"
	"strings"
	"testing"
)

type countingPort struct {
	requests []ReadRequest
	snapshot Snapshot
}

func (p *countingPort) Read(_ context.Context, request ReadRequest) (Snapshot, error) {
	p.requests = append(p.requests, request)
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

func TestSelectingProductReadsS2OnceAndBackDoesNotRead(t *testing.T) {
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
		t.Fatalf("S2 = %#v", got)
	}
	if err := model.Back(); err != nil {
		t.Fatal(err)
	}
	if got := model.Snapshot(); got.Screen != ScreenPortfolio || got.AmbientProduct != "" {
		t.Fatalf("back snapshot = %#v", got)
	}
	if len(port.requests) != 2 {
		t.Fatalf("selection/back read count = %d, want 2", len(port.requests))
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

func TestProjectionIsDeterministicAndCarriesC14Meaning(t *testing.T) {
	snapshot := Snapshot{Screen: ScreenPortfolio, AmbientProduct: "Concord", Watermark: "w42", ObservedAt: "2m", Reliance: "blocked", Coverage: "authoritative", Rows: []ProductRow{{Name: "Launcher", Stage: "in_progress", Reliance: "blocked", Actions: 3, Focus: "Fix launcher input"}}}
	first, second := Project(snapshot, 80), Project(snapshot, 80)
	if len(first.Rows) != 1 || len(first.Rows[0]) != 5 {
		t.Fatalf("projection rows=%#v", first.Rows)
	}
	for i := range first.Header {
		if first.Header[i] != second.Header[i] {
			t.Fatal("projection changed between renders")
		}
	}
	if first.Rows[0][2] == "" || first.Rows[0][3] == "" || first.Rows[0][4] == "" || first.Markers[0] != "!" {
		t.Fatalf("C14 meaning missing: %#v markers=%#v", first.Rows[0], first.Markers)
	}
}

func TestProductAndWorkProjectionCarriesTypedScreenSections(t *testing.T) {
	product := Project(Snapshot{Screen: ScreenProduct, AmbientProduct: "p-1", Watermark: "w", ObservedAt: "now", Reliance: "authoritative", Coverage: "authoritative", Section: SectionRanked, Ranked: []RankedWork{{ID: "w-1", Title: "Next", Priority: 1, Lifecycle: "needed", Ready: true}}}, 80)
	if product.Columns[0] != "Work" || len(product.Rows) != 1 || !strings.Contains(strings.Join(product.Rows[0], " "), "w-1") {
		t.Fatalf("product projection=%#v", product)
	}
	work := Project(Snapshot{Screen: ScreenWork, AmbientProduct: "p-1", Section: SectionKnowledge, Detail: WorkDetail{Item: RankedWork{ID: "w-1", Title: "Next", Lifecycle: "needed"}}}, 80)
	if work.Columns[0] != "Work" || len(work.Rows) != 1 || !strings.Contains(work.Header[len(work.Header)-1], "knowledge") {
		t.Fatalf("work projection=%#v", work)
	}
}

func TestSelectingProductOpensOnDomainSection(t *testing.T) {
	port := &countingPort{snapshot: Snapshot{Screen: ScreenPortfolio, Rows: []ProductRow{{ID: "p-1", Name: "One"}}, Coverage: "authoritative"}}
	model := New(port)
	if err := model.Enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	port.snapshot = Snapshot{Screen: ScreenProduct, Section: SectionDomains, Coverage: "authoritative", Domains: DomainSection{Read: true, State: "authoritative", Domains: []DomainRow{{ID: "root", Name: "One", Home: true}}}}
	if err := model.SelectProduct(context.Background(), "p-1"); err != nil {
		t.Fatal(err)
	}
	if len(port.requests) != 2 || port.requests[1].Kind != ReadDomains || port.requests[1].Section != SectionDomains {
		t.Fatalf("S2 entry requests=%#v", port.requests)
	}
	if got := model.Snapshot(); got.Section != SectionDomains || len(got.Domains.Domains) != 1 || !got.Domains.Domains[0].Home {
		t.Fatalf("S2 default section must be Domains: %#v", got)
	}
	cloned := model.Snapshot()
	cloned.Domains.Domains[0].Name = "mutated"
	if model.Snapshot().Domains.Domains[0].Name == "mutated" {
		t.Fatal("Snapshot leaked Domain rows by reference")
	}
}

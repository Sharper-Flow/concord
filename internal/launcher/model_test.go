package launcher

import (
	"context"
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
	if len(port.requests) != 3 || port.requests[1].Kind != ReadQuery {
		t.Fatalf("requests=%#v", port.requests)
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

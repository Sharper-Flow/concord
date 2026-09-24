package bubbletea

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/sharper-flow/concord/internal/launcher"
)

// interleavedPortfolioSnapshot builds the installed launcher's portfolio
// state: Product rows display while the hidden candidate list interleaves
// Products and work items by rank.
func interleavedPortfolioSnapshot() launcher.Snapshot {
	rows := []launcher.ProductRow{
		{ID: "p-alpha", Name: "Alpha"},
		{ID: "p-beta", Name: "Beta"},
		{ID: "p-gamma", Name: "Gamma"},
	}
	candidates := []launcher.Candidate{
		{ID: "p-alpha", Kind: launcher.CandidateProduct, Name: "Alpha", ProductID: "p-alpha", State: "available", Available: true},
		{ID: "work-1", Kind: launcher.CandidateWork, Name: "Work One", ProductID: "p-alpha", WorkID: "work-1", Available: true},
		{ID: "p-beta", Kind: launcher.CandidateProduct, Name: "Beta", ProductID: "p-beta", State: "available", Available: true},
		{ID: "work-2", Kind: launcher.CandidateWork, Name: "Work Two", ProductID: "p-beta", WorkID: "work-2", Available: true},
		{ID: "work-3", Kind: launcher.CandidateWork, Name: "Work Three", ProductID: "p-alpha", WorkID: "work-3", Available: true},
	}
	return launcher.Snapshot{Screen: launcher.ScreenPortfolio, Coverage: "authoritative", Rows: rows, Candidates: candidates}
}

type launchRecorder struct {
	handoffs []launcher.SessionHandoff
}

func newInterleavedModel() (*Model, *launcher.Model, *launchRecorder) {
	core := launcher.New(&coordinationPort{})
	core.RestoreSnapshot(interleavedPortfolioSnapshot())
	m := New(core, context.Background(), Profile{})
	rec := &launchRecorder{}
	m.SetSessionLauncher(func(handoff launcher.SessionHandoff) tea.Cmd {
		rec.handoffs = append(rec.handoffs, handoff)
		return nil
	})
	return m, core, rec
}

func TestPortfolioEnterOpensTheHighlightedProductRow(t *testing.T) {
	m, core, rec := newInterleavedModel()
	m.UpdateKey("j")
	m.UpdateKey("enter")
	got := core.Snapshot()
	if got.Screen != launcher.ScreenProduct || got.AmbientProduct != "p-beta" {
		t.Fatalf("enter on the second displayed row = %#v", got)
	}
	if len(rec.handoffs) != 0 {
		t.Fatalf("enter on a displayed Product row launched a hidden candidate: %#v", rec.handoffs)
	}
}

func TestPortfolioCursorStopsAtTheLastDisplayedRow(t *testing.T) {
	m, core, rec := newInterleavedModel()
	m.UpdateKey("G")
	if m.Cursor() != 2 {
		t.Fatalf("G cursor = %d, want the last displayed Product row index 2", m.Cursor())
	}
	m.UpdateKey("enter")
	got := core.Snapshot()
	if got.Screen != launcher.ScreenProduct || got.AmbientProduct != "p-gamma" {
		t.Fatalf("enter at the displayed bound = %#v", got)
	}
	if len(rec.handoffs) != 0 {
		t.Fatalf("enter past the last Product row launched a hidden candidate: %#v", rec.handoffs)
	}
}

func TestPortfolioPinIgnoresDisplayedProductRows(t *testing.T) {
	m, core, _ := newInterleavedModel()
	m.UpdateKey("j")
	m.UpdateKey("ctrl+p")
	for _, candidate := range core.Snapshot().Candidates {
		if candidate.Pinned {
			t.Fatalf("pin on a displayed Product row pinned hidden candidate %s", candidate.ID)
		}
	}
	m.UpdateKey("ctrl+u")
	for _, candidate := range core.Snapshot().Candidates {
		if candidate.Pinned {
			t.Fatalf("unpin on a displayed Product row pinned hidden candidate %s", candidate.ID)
		}
	}
}

func TestCandidateListKeepsItsBehaviorWhenNoProductRowDisplays(t *testing.T) {
	core := launcher.New(&coordinationPort{})
	core.RestoreSnapshot(launcher.Snapshot{
		Screen: launcher.ScreenPortfolio, Coverage: "authoritative",
		Rows: []launcher.ProductRow{{ID: "p-alpha", Name: "Alpha"}},
		Candidates: []launcher.Candidate{
			{ID: "p-alpha", Kind: launcher.CandidateProduct, Name: "Alpha", ProductID: "p-alpha", State: "available", Available: true},
			{ID: "work-9", Kind: launcher.CandidateWork, Name: "Work Nine", ProductID: "p-x", WorkID: "work-9", Available: true},
		},
	})
	m := New(core, context.Background(), Profile{})
	rec := &launchRecorder{}
	m.SetSessionLauncher(func(handoff launcher.SessionHandoff) tea.Cmd {
		rec.handoffs = append(rec.handoffs, handoff)
		return nil
	})
	m.OpenFilter()
	m.Update(keyPress('9', "9", 0))
	m.UpdateKey("enter")
	if got := core.Snapshot(); len(got.Rows) != 1 {
		t.Fatalf("filter fixture lost its unfiltered rows: %#v", got)
	}
	if m.Cursor() != 0 {
		t.Fatalf("cursor = %d, want the only displayed candidate", m.Cursor())
	}
	m.UpdateKey("enter")
	if len(rec.handoffs) != 1 || rec.handoffs[0].WorkID != "work-9" {
		t.Fatalf("enter on the candidate list when no Product row displays = %#v", rec.handoffs)
	}
}

func TestCandidatePinStillActsOnTheDisplayedCandidateList(t *testing.T) {
	core := launcher.New(&coordinationPort{})
	core.RestoreSnapshot(launcher.Snapshot{
		Screen: launcher.ScreenPortfolio, Coverage: "authoritative",
		Candidates: []launcher.Candidate{
			{ID: "work-9", Kind: launcher.CandidateWork, Name: "Work Nine", ProductID: "p-x", WorkID: "work-9", Path: "/trees/work-9", Available: true},
		},
	})
	m := New(core, context.Background(), Profile{})
	m.UpdateKey("ctrl+p")
	if got := core.Snapshot().Candidates[0]; !got.Pinned {
		t.Fatalf("ctrl+p did not pin the displayed candidate: %#v", got)
	}
	m.UpdateKey("ctrl+u")
	if got := core.Snapshot().Candidates[0]; got.Pinned {
		t.Fatalf("ctrl+u did not unpin the displayed candidate: %#v", got)
	}
}

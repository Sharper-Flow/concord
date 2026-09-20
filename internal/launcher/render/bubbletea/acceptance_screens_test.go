package bubbletea

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/sharper-flow/concord/internal/launcher"
)

// screenStub serves per-kind snapshots and, when the core asks through the
// ProjectPort or IssuePort extensions, a fixed Project select and a scripted
// issue resolution. Projects models the store port's contract: only Projects
// whose repository path a prior bootstrap recorded are offered.
type screenStub struct {
	state    launcher.Snapshot // portfolio and default answer
	product  launcher.Snapshot // Product screen answer
	work     launcher.Snapshot // Work screen answer
	projects []launcher.ProjectOption
	resolve  func(key string) (launcher.SessionHandoff, error)
}

func (p *screenStub) Read(_ context.Context, request launcher.ReadRequest) (launcher.Snapshot, error) {
	switch request.Kind {
	case launcher.ReadProduct, launcher.ReadDomains:
		if p.product.Screen != "" {
			return p.product, nil
		}
	case launcher.ReadWork:
		if p.work.Screen != "" {
			return p.work, nil
		}
	}
	return p.state, nil
}

func (p *screenStub) Projects(_ context.Context, _ string) ([]launcher.ProjectOption, error) {
	return p.projects, nil
}

func (p *screenStub) ResolveIssue(_ context.Context, key, _ string) (launcher.SessionHandoff, error) {
	return p.resolve(key)
}

func typeString(t *testing.T, m *Model, value string) {
	t.Helper()
	for _, runeValue := range value {
		m.Update(keyPress(runeValue, string(runeValue), 0))
	}
}

// TestProductSelectPrecedesWorkList proves
// check:launcher.product_select_precedes_work: the screen selector is the
// outer selector in renderContent, so a populated candidate feed can never
// push the Product select or the Product work list out of the frame. Before
// this precedence held, any snapshot carrying candidates rendered the
// candidate feed on every screen.
func TestProductSelectPrecedesWorkList(t *testing.T) {
	candidates := []launcher.Candidate{
		{ID: "/scan/root", Kind: launcher.CandidateProject, Name: "Scan Root", Path: "/scan/root", Available: true},
	}
	stub := &screenStub{
		state: launcher.Snapshot{
			Screen: launcher.ScreenPortfolio, Coverage: "authoritative",
			Rows:       []launcher.ProductRow{{ID: "product-1", Name: "Registered Product"}},
			Candidates: candidates,
		},
		product: launcher.Snapshot{
			Screen: launcher.ScreenProduct, AmbientProduct: "product-1", Section: launcher.SectionRanked,
			PanelFocus: launcher.S2PanelBlocked, Coverage: "authoritative",
			Ranked: []launcher.RankedWork{{ID: "work-1", Title: "Scoped work", Lifecycle: "needed", Priority: 1}},
		},
	}
	core := launcher.New(stub)
	if err := core.Enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := New(core, context.Background(), Profile{})
	rendered := m.Render()
	if !strings.Contains(rendered, "Registered Product") {
		t.Fatalf("Product select lost its Product rows to the candidate feed: %q", rendered)
	}
	if strings.Contains(rendered, "CANDIDATES") {
		t.Fatalf("candidate feed rendered over the Product select: %q", rendered)
	}
	// Selecting the Product reads the Product screen. The stale candidate feed
	// stays on the snapshot exactly as the broken precedence left it, and the
	// work list must still win the frame.
	if err := core.SelectProduct(context.Background(), "product-1"); err != nil {
		t.Fatal(err)
	}
	snapshot := core.Snapshot()
	snapshot.Candidates = candidates
	core.RestoreSnapshot(snapshot)
	m.Sync()
	rendered = m.Render()
	if !strings.Contains(rendered, "Scoped work") {
		t.Fatalf("Product screen lost its work list to the candidate feed: %q", rendered)
	}
	if strings.Contains(rendered, "CANDIDATES") {
		t.Fatalf("candidate feed rendered over the Product screen: %q", rendered)
	}
}

// TestWorkListExcludesTerminalItems proves the picker half of
// check:launcher.work_list_product_scoped_active: the Product screen keeps
// terminal history in the shared read projection but the active-only picker
// drops it, so Enter can never land on finished work.
func TestWorkListExcludesTerminalItems(t *testing.T) {
	stub := &screenStub{state: launcher.Snapshot{
		Screen: launcher.ScreenProduct, AmbientProduct: "product-1", Section: launcher.SectionRanked,
		PanelFocus: launcher.S2PanelBlocked, Coverage: "authoritative", ActiveWorkOnly: true,
		Ranked: []launcher.RankedWork{
			{ID: "work-live", Title: "Live work", Lifecycle: "in_progress", Priority: 1},
			{ID: "work-done", Title: "Finished work", Lifecycle: "completed", Priority: 2, Terminal: true, TerminalAt: "2026-09-01T00:00:00Z"},
		},
	}}
	core := launcher.New(stub)
	core.RestoreSnapshot(stub.state)
	m := New(core, context.Background(), Profile{})
	if rows := m.filteredRanked(); len(rows) != 1 || rows[0].ID != "work-live" {
		t.Fatalf("active picker rows = %#v, want only work-live", rows)
	}
	rendered := m.Render()
	if strings.Contains(rendered, "work-done") || strings.Contains(rendered, "lifecycle=completed") {
		t.Fatalf("active picker rendered terminal work: %q", rendered)
	}
	if !strings.Contains(rendered, "work-live") {
		t.Fatalf("active picker lost its live row: %q", rendered)
	}
}

// TestWorkRowRendersIssueKeyAndOccupancy proves
// check:launcher.work_row_issue_key_and_occupancy at the render boundary: a
// work row carries its lifecycle, the correlated issue key when one exists,
// and the host-attested occupancy state.
func TestWorkRowRendersIssueKeyAndOccupancy(t *testing.T) {
	stub := &screenStub{state: launcher.Snapshot{
		Screen: launcher.ScreenProduct, AmbientProduct: "product-1", Section: launcher.SectionRanked,
		PanelFocus: launcher.S2PanelBlocked, Coverage: "authoritative", ActiveWorkOnly: true,
		Ranked: []launcher.RankedWork{
			{ID: "work-linked", Title: "Linked work", Lifecycle: "in_progress", Priority: 1, LinearIssueKey: "CON-153", Live: 1},
			{ID: "work-plain", Title: "Plain work", Lifecycle: "needed", Priority: 2},
		},
	}}
	core := launcher.New(stub)
	core.RestoreSnapshot(stub.state)
	m := New(core, context.Background(), Profile{})
	rendered := m.Render()
	for _, want := range []string{"lifecycle=in_progress", "issue=CON-153", "live=yes", "live=no"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("work row missing %q: %q", want, rendered)
		}
	}
}

// TestOccupiedWorkRequiresExplicitConfirmation proves
// check:launcher.occupied_item_requires_confirmation: Enter on a work item a
// live session holds opens a confirmation gate, launches nothing until Enter
// confirms, and releases the item on Esc.
func TestOccupiedWorkRequiresExplicitConfirmation(t *testing.T) {
	stub := &screenStub{
		state: launcher.Snapshot{
			Screen: launcher.ScreenProduct, AmbientProduct: "product-1", Section: launcher.SectionRanked,
			PanelFocus: launcher.S2PanelBlocked, Coverage: "authoritative", ActiveWorkOnly: true,
			Ranked: []launcher.RankedWork{{ID: "work-occupied", Title: "Occupied work", Lifecycle: "in_progress", Priority: 1, Live: 1}},
		},
		work: launcher.Snapshot{
			Screen: launcher.ScreenWork, AmbientProduct: "product-1", SelectedWorkID: "work-occupied",
			Coverage: "authoritative",
		},
	}
	core := launcher.New(stub)
	core.RestoreSnapshot(stub.state)
	m := New(core, context.Background(), Profile{})
	launched := []launcher.SessionHandoff{}
	m.SetSessionLauncher(func(handoff launcher.SessionHandoff) tea.Cmd {
		launched = append(launched, handoff)
		return func() tea.Msg { return nil }
	})
	m.UpdateKey("enter")
	if rendered := m.Render(); !strings.Contains(rendered, "CONFIRM: a live session holds this work") {
		t.Fatalf("occupied work opened without a confirmation gate: %q", rendered)
	}
	if len(launched) != 0 {
		t.Fatalf("the confirmation gate leaked a launch: %#v", launched)
	}
	// Esc cancels: the gate clears and nothing launches.
	m.UpdateKey("esc")
	if rendered := m.Render(); strings.Contains(rendered, "CONFIRM:") {
		t.Fatalf("Esc left the confirmation gate up: %q", rendered)
	}
	// A fresh Enter confirms: the launch fires once, identity only.
	m.UpdateKey("enter")
	m.UpdateKey("enter")
	if len(launched) != 1 || launched[0].WorkID != "work-occupied" || launched[0].Agent != launcher.DefaultSessionAgent {
		t.Fatalf("confirmed launch = %#v, want one identity-only handoff", launched)
	}
}

// TestNewBacklogResolvesIssueKeyOrDegradesToProjects proves
// check:launcher.new_backlog_resolves_issue_or_project: the New/Backlog row
// opens the issue-key prompt; a confirmed key launches where the work lives;
// an unlinked key and an empty Enter degrade to the Product's Project select.
// The select offers only Projects with a recorded repository path, which the
// store port enforces and its own test proves.
func TestNewBacklogResolvesIssueKeyOrDegradesToProjects(t *testing.T) {
	product := func() launcher.Snapshot {
		return launcher.Snapshot{
			Screen: launcher.ScreenProduct, AmbientProduct: "product-1", Section: launcher.SectionRanked,
			PanelFocus: launcher.S2PanelBlocked, Coverage: "authoritative", ActiveWorkOnly: true, Backlog: true,
			Ranked: []launcher.RankedWork{{ID: "work-1", Title: "Live work", Lifecycle: "needed", Priority: 1}},
		}
	}
	t.Run("confirmed key resolves offline and launches the linked work", func(t *testing.T) {
		stub := &screenStub{state: product(), resolve: func(string) (launcher.SessionHandoff, error) {
			return launcher.SessionHandoff{ProductID: "product-1", WorkID: "work-linked", Agent: launcher.DefaultSessionAgent}, nil
		}}
		core := launcher.New(stub)
		core.RestoreSnapshot(stub.state)
		m := New(core, context.Background(), Profile{})
		launched := []launcher.SessionHandoff{}
		m.SetSessionLauncher(func(handoff launcher.SessionHandoff) tea.Cmd {
			launched = append(launched, handoff)
			return func() tea.Msg { return nil }
		})
		m.UpdateKey("j") // cursor onto the appended New/Backlog row
		m.UpdateKey("enter")
		typeString(t, m, "CON-153")
		m.UpdateKey("enter")
		if len(launched) != 1 || launched[0].WorkID != "work-linked" {
			t.Fatalf("confirmed key launched %#v, want the linked work", launched)
		}
		if core.Snapshot().ProjectSelect {
			t.Fatal("a resolved key must not degrade to the Project select")
		}
	})
	t.Run("unlinked key degrades to the Project select", func(t *testing.T) {
		stub := &screenStub{state: product(), projects: []launcher.ProjectOption{
			{ID: "proj-1", Name: "Pathed project", Role: "primary", Path: "/src/proj-1"},
		}, resolve: func(string) (launcher.SessionHandoff, error) {
			return launcher.SessionHandoff{}, nil
		}}
		core := launcher.New(stub)
		core.RestoreSnapshot(stub.state)
		m := New(core, context.Background(), Profile{})
		m.UpdateKey("j")
		m.UpdateKey("enter")
		typeString(t, m, "UNKNOWN-1")
		m.UpdateKey("enter")
		snapshot := core.Snapshot()
		if !snapshot.ProjectSelect {
			t.Fatalf("unlinked key must degrade to the Project select, got %#v", snapshot)
		}
		if len(snapshot.Projects) != 1 || snapshot.Projects[0].Path == "" {
			t.Fatalf("Project select = %#v, want the launchable Project", snapshot.Projects)
		}
		rendered := m.Render()
		if !strings.Contains(rendered, "Pathed project") || !strings.Contains(rendered, "path=/src/proj-1") {
			t.Fatalf("Project select lost its row: %q", rendered)
		}
	})
	t.Run("empty Enter degrades to the Project select", func(t *testing.T) {
		stub := &screenStub{state: product(), projects: []launcher.ProjectOption{
			{ID: "proj-1", Name: "Pathed project", Role: "primary", Path: "/src/proj-1"},
		}}
		core := launcher.New(stub)
		core.RestoreSnapshot(stub.state)
		m := New(core, context.Background(), Profile{})
		m.UpdateKey("j")
		m.UpdateKey("enter")
		m.UpdateKey("enter")
		if !core.Snapshot().ProjectSelect {
			t.Fatalf("empty Enter must degrade to the Project select, got %#v", core.Snapshot())
		}
	})
}

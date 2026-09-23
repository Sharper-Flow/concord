package bubbletea

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

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

func assertSizedFlowFrames(t *testing.T, m *Model, marker string) {
	t.Helper()
	for _, width := range []int{80, 100, 120, 200} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		frame := m.Render()
		if !strings.Contains(frame, marker) {
			t.Fatalf("width %d lost flow marker %q: %q", width, marker, frame)
		}
		for lineNumber, line := range strings.Split(frame, "\n") {
			if got := lipgloss.Width(line); got > width {
				t.Fatalf("width %d line %d exceeds terminal: %d", width, lineNumber, got)
			}
		}
		content := m.renderContent(m.snapshot, m.cursor)
		assertOneLinePerDataRow(t, width, frame, content)
		assertHeaderCellAlignment(t, width, frame, content)
		if got := strings.Count(frame, "arrows move"); got != 1 {
			t.Fatalf("width %d footer count=%d, want 1", width, got)
		}
	}
}

// assertOneLinePerDataRow measures the frame, not the projection: each data
// row the pane projects must render on exactly one terminal line, so a row
// that stacks onto a second line fails here instead of reaching the operator.
func assertOneLinePerDataRow(t *testing.T, width int, frame string, content renderedPane) {
	t.Helper()
	lines := strings.Split(frame, "\n")
	for _, row := range content.rows {
		identity := ""
		for _, cell := range row {
			if cell != "" {
				identity = cell
				break
			}
		}
		if identity == "" {
			continue
		}
		count := 0
		for _, line := range lines {
			if strings.Contains(line, identity) {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("width %d: data row %q renders on %d lines, want exactly one: %q", width, identity, count, frame)
		}
	}
}

// assertHeaderCellAlignment measures the frame: every header cell whose first
// data cell carries content must start at the same display column as that
// cell, so a gutter or padding shift between the header row and its rows
// fails here.
func assertHeaderCellAlignment(t *testing.T, width int, frame string, content renderedPane) {
	t.Helper()
	if len(content.tableHeaders) == 0 || len(content.rows) == 0 || len(content.rows[0]) == 0 {
		return
	}
	lines := strings.Split(frame, "\n")
	dataLine := -1
	for i, line := range lines {
		if cellStarts(line, content.rows[0]) != nil {
			dataLine = i
			break
		}
	}
	if dataLine < 0 {
		t.Fatalf("width %d: first data row not found in the frame: %q", width, frame)
	}
	for i := dataLine - 1; i >= 0; i-- {
		headerStarts := cellStarts(lines[i], content.tableHeaders)
		if headerStarts == nil {
			continue
		}
		dataStarts := cellStarts(lines[dataLine], content.rows[0])
		for col := range headerStarts {
			if col >= len(content.rows[0]) || content.tableHeaders[col] == "" || content.rows[0][col] == "" {
				continue
			}
			if headerStarts[col] != dataStarts[col] {
				t.Fatalf("width %d: header %q starts at column %d but its data cell %q starts at %d: %q",
					width, content.tableHeaders[col], headerStarts[col], content.rows[0][col], dataStarts[col], frame)
			}
		}
		return
	}
	t.Fatalf("width %d: header row above the first data row not found: %q", width, frame)
}

// cellStarts locates each non-empty cell of a rendered table line, left to
// right. It returns nil when any non-empty cell is missing, so callers can
// use it both as a line-shape probe and as a column locator.
func cellStarts(line string, cells []string) []int {
	starts := make([]int, len(cells))
	from := 0
	for i, cell := range cells {
		if cell == "" {
			starts[i] = -1
			continue
		}
		offset := strings.Index(line[from:], cell)
		if offset < 0 {
			return nil
		}
		starts[i] = from + offset
		from = starts[i] + len(cell)
	}
	return starts
}

// rankedRowLine locates the ranked row whose identity cell names id and
// returns the one frame line that carries it, so cell assertions inspect the
// row itself and never a detail pane that happens to repeat a value.
func rankedRowLine(t *testing.T, width int, frame string, content renderedPane, id string) string {
	t.Helper()
	for _, row := range content.rows {
		identity := ""
		for _, cell := range row {
			if cell != "" {
				identity = cell
				break
			}
		}
		if identity == "" || !strings.Contains(identity, id) {
			continue
		}
		for _, line := range strings.Split(frame, "\n") {
			if strings.Contains(line, identity) {
				return line
			}
		}
		t.Fatalf("width %d: row %q renders on no single frame line: %q", width, identity, frame)
	}
	t.Fatalf("width %d: no ranked row names %q: %q", width, id, frame)
	return ""
}

// TestHelpToggleShowsFullKeyListAndHoldsFrameHeight proves
// check:go.test.launcher.help: the ? key assigns help.ShowAll, the existing
// help view then renders the full key list the short footer elides, the
// taller footer is absorbed by the table, and the frame stays exactly the
// requested height at every width.
func TestHelpToggleShowsFullKeyListAndHoldsFrameHeight(t *testing.T) {
	snapshot := launcher.Snapshot{
		Screen: launcher.ScreenPortfolio, Coverage: "authoritative",
		Rows: []launcher.ProductRow{{ID: "p-1", Name: "Alpha", Stage: "in_progress", Reliance: "clear", Actions: 1, Focus: "Hold"}},
	}
	m := New(launcher.New(&port{state: snapshot}), context.Background(), Profile{})
	for _, width := range []int{80, 100, 120, 200} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		m.Sync()
		short := m.footerLines()
		m.UpdateKey("?")
		full := m.footerLines()
		fullFrame := m.Render()
		if len(full) <= len(short) {
			t.Fatalf("width %d: toggling help did not grow the footer: short=%d full=%d", width, len(short), len(full))
		}
		// Full help lays bindings out in key/description columns, so the
		// check collapses whitespace runs before matching.
		joined := strings.Join(strings.Fields(strings.Join(full, " ")), " ")
		for _, elided := range []string{"esc back", "ctrl+l clear"} {
			if !strings.Contains(joined, elided) {
				t.Fatalf("width %d: full help lost the short-elided binding %q: %q", width, elided, joined)
			}
		}
		lines := strings.Split(fullFrame, "\n")
		if len(lines) != 24 {
			t.Fatalf("width %d: frame height=%d, want 24", width, len(lines))
		}
		for i, line := range lines {
			if got := lipgloss.Width(line); got != width {
				t.Fatalf("width %d: line %d width=%d, want %d: %q", width, i, got, width, line)
			}
		}
		if !strings.Contains(strings.Join(strings.Fields(fullFrame), " "), "esc back") {
			t.Fatalf("width %d: full help bindings missing from the rendered frame", width)
		}
		m.UpdateKey("?")
		if restored := m.footerLines(); len(restored) != len(short) {
			t.Fatalf("width %d: second toggle did not restore the short footer: %d lines", width, len(restored))
		}
	}
}

func TestLauncherOperatorFlowUsesSizedTables(t *testing.T) {
	stub := &screenStub{
		state: launcher.Snapshot{
			Screen: launcher.ScreenPortfolio, Coverage: "authoritative",
			Rows: []launcher.ProductRow{{ID: "product-1", Name: "Readable Product"}},
		},
		product: launcher.Snapshot{
			Screen: launcher.ScreenProduct, AmbientProduct: "product-1", Section: launcher.SectionRanked,
			PanelFocus: launcher.S2PanelBlocked, Coverage: "authoritative",
			Ranked: []launcher.RankedWork{{ID: "work-1", Title: "Readable work", Lifecycle: "needed", Priority: 1}},
		},
		work: launcher.Snapshot{Screen: launcher.ScreenWork, AmbientProduct: "product-1", SelectedWorkID: "work-1", Coverage: "authoritative"},
	}
	core := launcher.New(stub)
	if err := core.Enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := New(core, context.Background(), Profile{})
	assertSizedFlowFrames(t, m, "Readable Product")
	// Selecting the Product lands on its work list; Enter acts on the
	// selected work without a section detour.
	m.UpdateKey("enter")
	m.Sync()
	assertSizedFlowFrames(t, m, "Readable work")
	var launched launcher.SessionHandoff
	m.SetSessionLauncher(func(handoff launcher.SessionHandoff) tea.Cmd {
		launched = handoff
		return nil
	})
	m.UpdateKey("enter")
	if launched.WorkID != "work-1" {
		t.Fatalf("session handoff = %#v", launched)
	}
	assertSizedFlowFrames(t, m, "S3 WORK DETAIL")

	degraded := &screenStub{
		state: launcher.Snapshot{
			Screen: launcher.ScreenProduct, AmbientProduct: "product-1", Section: launcher.SectionRanked,
			PanelFocus: launcher.S2PanelBlocked, Coverage: "authoritative", Backlog: true,
			Ranked: []launcher.RankedWork{{ID: "backlog", Title: "New / Backlog", Backlog: true}},
		},
		projects: []launcher.ProjectOption{{ID: "project-1", Name: "Project one", Role: "primary", Path: "/project-one"}},
		resolve:  func(string) (launcher.SessionHandoff, error) { return launcher.SessionHandoff{}, nil },
	}
	degradedCore := launcher.New(degraded)
	degradedCore.RestoreSnapshot(degraded.state)
	degradedModel := New(degradedCore, context.Background(), Profile{})
	degradedModel.UpdateKey("enter")
	typeString(t, degradedModel, "UNKNOWN-1")
	degradedModel.UpdateKey("enter")
	assertSizedFlowFrames(t, degradedModel, "PROJECTS")
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
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
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

// TestProductSelectOpensTheWorkListAndKeepsDomainReachable proves
// check:launcher.product_work_first: selecting
// a Product immediately shows the scrollable Product-scoped list of
// non-terminal work whose rows carry lifecycle, linked issue key, and
// live-session state, ending with the New / Backlog row, while the Domain and
// law context stays one Tab away with its data and never takes the entry
// focus.
func TestProductSelectOpensTheWorkListAndKeepsDomainReachable(t *testing.T) {
	stub := &screenStub{
		state: launcher.Snapshot{
			Screen: launcher.ScreenPortfolio, Coverage: "authoritative",
			Rows: []launcher.ProductRow{{ID: "product-1", Name: "Registered Product"}},
		},
		product: launcher.Snapshot{
			Screen: launcher.ScreenProduct, AmbientProduct: "product-1", Section: launcher.SectionDomains,
			Coverage: "authoritative", ActiveWorkOnly: true, Backlog: true,
			Domains: launcher.DomainSection{Read: true, State: "authoritative", Domains: []launcher.DomainRow{{ID: "product-root:one", Name: "Product One", Home: true}}},
			Ranked: []launcher.RankedWork{
				{ID: "work-linked", Title: "Linked work", Lifecycle: "in_progress", Priority: 1, LinearIssueKey: "CON-153", Live: 1},
				{ID: "work-plain", Title: "Plain work", Lifecycle: "needed", Priority: 2},
			},
		},
	}
	core := launcher.New(stub)
	if err := core.Enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := New(core, context.Background(), Profile{})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.UpdateKey("enter")
	m.Sync()
	snapshot := core.Snapshot()
	if snapshot.Section != launcher.SectionRanked || snapshot.PanelFocus != launcher.S2PanelNext {
		t.Fatalf("Product entry must seat the work list, got section=%q panel=%q", snapshot.Section, snapshot.PanelFocus)
	}
	rendered := m.Render()
	content := m.renderContent(m.snapshot, m.cursor)
	linkedRow := rankedRowLine(t, 120, rendered, content, "work-linked")
	for _, want := range []string{
		"1 ~ACTIVE work-linked Linked work", "in_progress", "CON-153", "yes",
	} {
		if !strings.Contains(linkedRow, want) {
			t.Fatalf("entry work list row missing %q: %q", want, linkedRow)
		}
	}
	if !strings.Contains(rendered, "New / Backlog") {
		t.Fatalf("entry work list missing the New / Backlog row: %q", rendered)
	}
	if !strings.Contains(rendered, "DOMAIN: no unresolved overlaps") {
		t.Fatalf("Domain context is not summarized on the Product screen: %q", rendered)
	}
	if strings.Contains(rendered, "DOMAINS:") && strings.Contains(rendered, "not_read") {
		t.Fatalf("reached Domain context rendered unread: %q", rendered)
	}
	// The Domain and law context panel is reachable by pane focus: at this
	// split width the detail pane is the outer Tab stop, so two Tabs land on
	// the Domain panel, which the entry read already populated.
	m.UpdateKey("tab")
	m.UpdateKey("tab")
	m.Sync()
	rendered = m.Render()
	for _, want := range []string{"product-root:one Product One", "HOME"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Domain panel lost %q: %q", want, rendered)
		}
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
	if strings.Contains(rendered, "work-done") || strings.Contains(rendered, "completed") {
		t.Fatalf("active picker rendered terminal work: %q", rendered)
	}
	if !strings.Contains(rendered, "work-live") {
		t.Fatalf("active picker lost its live row: %q", rendered)
	}
}

// TestWorkRowRendersIssueKeyAndOccupancy proves
// check:launcher.work_row_issue_key_and_occupancy at the render boundary: a
// work row carries its identity, lifecycle, the correlated issue key when
// one exists, and the host-attested occupancy state on its own line at every
// supported width. The row reserves the full linked key — a truncated key is
// a missing key — so the long real-store shapes drive the fixture: a
// 42-character issue key and a 37-character work ID. The row yields its
// title first, then its marker, and compacts the work identity to a
// head-and-tail form; the detail pane keeps the full identity.
func TestWorkRowRendersIssueKeyAndOccupancy(t *testing.T) {
	const (
		longKey = "LONG-LINKED-ISSUE-KEY-405-PLATFORM-FIXTURE"
		longID  = "work-3f9c1b2a4d5e6f708192a3b4c5d6e7f8"
	)
	if len(longKey) != 42 || len(longID) != 37 {
		t.Fatalf("fixture drift: key %d chars, id %d chars, want the long real-store shapes 42/37", len(longKey), len(longID))
	}
	title := "Deliver the launcher correction with a realistic title long enough to overflow every narrow row"
	stub := &screenStub{state: launcher.Snapshot{
		Screen: launcher.ScreenProduct, AmbientProduct: "product-1", Section: launcher.SectionRanked,
		PanelFocus: launcher.S2PanelBlocked, Coverage: "authoritative", ActiveWorkOnly: true, Backlog: true,
		Ranked: []launcher.RankedWork{
			{ID: longID, Title: title, Lifecycle: "in_progress", Priority: 1, LinearIssueKey: longKey, Live: 1},
			{ID: "work-plain", Title: "Plain work", Lifecycle: "needed", Priority: 2},
		},
	}}
	core := launcher.New(stub)
	core.RestoreSnapshot(stub.state)
	m := New(core, context.Background(), Profile{})
	for _, width := range []int{80, 100, 120, 200} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
		frame := m.Render()
		content := m.renderContent(m.snapshot, m.cursor)
		linkedRow := rankedRowLine(t, width, frame, content, longID[len(longID)-4:])
		for _, want := range []string{"in_progress", longKey, "yes", longID[len(longID)-4:]} {
			if !strings.Contains(linkedRow, want) {
				t.Fatalf("width %d: linked row line lost %q: %q", width, want, linkedRow)
			}
		}
		if width == 200 {
			if !strings.Contains(linkedRow, longID) || !strings.Contains(linkedRow, "~ACTIVE") {
				t.Fatalf("width %d: linked row line lost its full identity and marker: %q", width, linkedRow)
			}
		} else if strings.Contains(linkedRow, longID) {
			t.Fatalf("width %d: the full work ID crowded the mandated cells: %q", width, linkedRow)
		}
		plainRow := rankedRowLine(t, width, frame, content, "lain")
		for _, want := range []string{"needed", "no"} {
			if !strings.Contains(plainRow, want) {
				t.Fatalf("width %d: plain row line lost %q: %q", width, want, plainRow)
			}
		}
		if width == 80 && !strings.Contains(plainRow, "…lain") {
			t.Fatalf("width %d: the plain row lost its compacted identity: %q", width, plainRow)
		}
		backlogRow := rankedRowLine(t, width, frame, content, "acklog")
		if width == 80 {
			// A 42-character key starves the picker row's Work cell to its
			// own name; the row still ends the list.
			if !strings.Contains(backlogRow, "backlog") {
				t.Fatalf("width %d: the picker row lost its name: %q", width, backlogRow)
			}
		} else if !strings.Contains(backlogRow, "New / Backlog") {
			t.Fatalf("width %d: the New / Backlog row lost its picker label: %q", width, backlogRow)
		}
		if width == 80 {
			if strings.Contains(linkedRow, title) {
				t.Fatalf("width %d: the title crowded the full linked key: %q", width, linkedRow)
			}
			continue
		}
		// At a width that seats the detail pane, the pane wraps the
		// over-width identity instead of clipping it: the head and tail of
		// both identifiers render in full.
		for _, probe := range []string{longKey[:10], longKey[len(longKey)-6:], longID[:10], longID[len(longID)-6:]} {
			if !strings.Contains(frame, probe) {
				t.Fatalf("width %d: the detail pane lost identity probe %q: %q", width, probe, frame)
			}
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
		if rendered := m.Render(); !strings.Contains(rendered, "ISSUE KEY:") {
			t.Fatalf("issue mode lost its prompt: %q", rendered)
		}
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

// TestPortfolioRendersCandidateFeedWhenEmpty proves the demoted half of the
// screen precedence: with no portfolio rows to render, the Portfolio screen
// shows the candidate feed, so the first-run scan-root view survives the
// screen-first renderContent ordering.
func TestPortfolioRendersCandidateFeedWhenEmpty(t *testing.T) {
	stub := &screenStub{state: launcher.Snapshot{
		Screen: launcher.ScreenPortfolio, Coverage: "first_run",
		Candidates: []launcher.Candidate{
			{ID: "/scan/root", Kind: launcher.CandidateProject, Name: "Scan Root", Path: "/scan/root", Available: true},
		},
	}}
	core := launcher.New(stub)
	core.RestoreSnapshot(stub.state)
	m := New(core, context.Background(), Profile{})
	rendered := m.Render()
	if !strings.Contains(rendered, "CANDIDATES") || !strings.Contains(rendered, "Scan Root") {
		t.Fatalf("empty portfolio must render the candidate feed, got %q", rendered)
	}
	if !strings.Contains(rendered, "STATUS: first_run") {
		t.Fatalf("first-run coverage banner lost: %q", rendered)
	}
}

// TestDetailPaneCarriesFocusFieldsTheStatusBarDrops proves
// check:launcher/detail-pane-carries-focus-fields: the split frame's detail
// pane renders the focus fields the core computes for the selected Product
// row, which focusText reduces to one identifier string in the status bar.
func TestDetailPaneCarriesFocusFieldsTheStatusBarDrops(t *testing.T) {
	core := launcher.New(nil)
	core.RestoreSnapshot(focusedDetailSnapshot())
	m := New(core, context.Background(), Profile{})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	rendered := m.Render()
	if !strings.Contains(rendered, "FOCUS: product/Concord") {
		t.Fatalf("status bar lost the focus identifier: %q", rendered)
	}
	for _, want := range []string{
		"FOCUS: Ship the detail pane",
		"WORK: work-42",
		"KIND: task",
		"LIFECYCLE: in_progress",
		"ATTENTION: approval_required",
		"BLOCKED SESSIONS: 2",
		"OLDEST BLOCKED: session-7",
		"PRIORITY: 3",
		"WORKFLOW: execution",
		"PROJECTS: 1",
		"STAGE: build",
		"STAGE MATURITY: stable",
		"STAGE AUDIENCE: operator",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("detail pane dropped focus field %q: %q", want, rendered)
		}
	}
	if strings.Contains(rendered, "DETAIL *") {
		t.Fatalf("unfocused detail pane rendered the focus marker: %q", rendered)
	}
}

// TestPortfolioTabReachesDetailAndFocusedDetailOwnsThePrimaryKeys proves
// pane focus on the portfolio screen: Tab joins the detail pane into the
// Tab cycle there, the focused pane consumes movement and Enter without
// moving or activating the primary selection, and focus leaves the pane on
// Tab, on a narrow resize, and on a screen change.
func TestPortfolioTabReachesDetailAndFocusedDetailOwnsThePrimaryKeys(t *testing.T) {
	core := launcher.New(nil)
	core.RestoreSnapshot(focusedDetailSnapshot())
	m := New(core, context.Background(), Profile{})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	if strings.Contains(m.Render(), "DETAIL *") {
		t.Fatalf("unfocused detail pane rendered the focus marker: %q", m.Render())
	}
	m.UpdateKey("tab")
	if !m.detailFocus || !strings.Contains(m.Render(), "DETAIL *") {
		t.Fatalf("portfolio Tab did not focus the detail pane: focus=%v render=%q", m.detailFocus, m.Render())
	}
	if got := core.Snapshot().Screen; got != launcher.ScreenPortfolio {
		t.Fatalf("focusing the detail pane changed the screen: %q", got)
	}
	// The focused pane owns movement and Enter: the primary cursor stays
	// put and Enter activates nothing.
	m.UpdateKey("j")
	m.UpdateKey("enter")
	if m.cursor != 0 || m.scroll != 0 {
		t.Fatalf("focused detail movement moved the primary cursor: cursor=%d scroll=%d", m.cursor, m.scroll)
	}
	if got := core.Snapshot().Screen; got != launcher.ScreenPortfolio {
		t.Fatalf("focused detail Enter activated the primary row: screen=%q", got)
	}
	// Tab returns pane focus to the primary pane.
	m.UpdateKey("tab")
	if m.detailFocus || strings.Contains(m.Render(), "DETAIL *") {
		t.Fatalf("Tab did not return pane focus to the primary pane: %q", m.Render())
	}
	// The minimum split width (the two declared pane minima) still seats the
	// pane; one column below it does not, and Tab stays a no-op there.
	m.Update(tea.WindowSizeMsg{Width: detailPaneWidth + primaryPaneMinWidth, Height: 24})
	m.UpdateKey("tab")
	if !m.detailFocus {
		t.Fatal("minimum-split-width Tab did not focus the detail pane")
	}
	m.UpdateKey("tab")
	m.Update(tea.WindowSizeMsg{Width: detailPaneWidth + primaryPaneMinWidth - 1, Height: 24})
	m.UpdateKey("tab")
	if m.detailFocus || strings.Contains(m.Render(), "DETAIL") {
		t.Fatalf("narrow frame joined or focused the absent detail pane: %q", m.Render())
	}
	// A resize back into the split keeps the pane unfocused, and a screen
	// change drops a focused pane with it.
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	if m.detailFocus {
		t.Fatal("resize kept the detail focus the narrow frame dropped")
	}
	m.UpdateKey("tab")
	if !m.detailFocus {
		t.Fatal("split-width Tab did not focus the detail pane")
	}
	core.RestoreSnapshot(launcher.Snapshot{Screen: launcher.ScreenProduct, AmbientProduct: "Concord", Section: launcher.SectionDomains, Coverage: "authoritative"})
	m.Sync()
	if m.detailFocus || strings.Contains(m.Render(), "DETAIL *") {
		t.Fatalf("screen change kept the focused detail pane: %q", m.Render())
	}
}

// TestDetailPaneFollowsTheSelectedWork proves the split frame's detail pane
// renders the selected work's typed fields on the screens that seat a work
// list, and follows the cursor as it moves. An ambient product's computed
// focus is portfolio detail: it must never stand in for the selected work's
// detail on the Product and Work screens.
func TestDetailPaneFollowsTheSelectedWork(t *testing.T) {
	snapshot := launcher.Snapshot{
		Screen: launcher.ScreenProduct, AmbientProduct: "product-1", Section: launcher.SectionRanked,
		PanelFocus: launcher.S2PanelBlocked, Coverage: "authoritative",
		Rows: []launcher.ProductRow{{
			ID: "product-1", Name: "Alpha", Stage: "in_progress",
			Focus: "Ship the ambient focus", FocusID: "work-ambient", FocusWorkflowStepLabel: "execution",
		}},
		Ranked: []launcher.RankedWork{
			{ID: "work-1", Kind: "task", Title: "Live", Lifecycle: "in_progress", Priority: 3, LinearIssueKey: "CON-9", Live: 1},
			{ID: "work-2", Kind: "bug", Title: "Done", Lifecycle: "completed", Priority: 2, Terminal: true, TerminalAt: "2026-08-05T00:00:00Z"},
		},
	}
	core := launcher.New(nil)
	core.RestoreSnapshot(snapshot)
	m := New(core, context.Background(), Profile{})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	rendered := m.Render()
	for _, want := range []string{"WORK: work-1", "KIND: task", "LIFECYCLE: in_progress", "ISSUE: CON-9", "LIVE SESSIONS: 1", "PRIORITY: 3"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("detail pane lost the selected work field %q: %q", want, rendered)
		}
	}
	// The ambient product's focus fields are not selected-work detail.
	for _, absent := range []string{"Ship the ambient focus", "work-ambient"} {
		if strings.Contains(rendered, absent) {
			t.Fatalf("detail pane rendered ambient product focus %q as work detail: %q", absent, rendered)
		}
	}
	// The pane follows the cursor onto the second ranked row.
	m.UpdateKey("j")
	rendered = m.Render()
	for _, want := range []string{"WORK: work-2", "KIND: bug", "TERMINAL: 2026-08-05T00:00:00Z"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("detail pane did not follow the cursor to %q: %q", want, rendered)
		}
	}
	// The Work screen feeds the pane from its loaded work detail.
	core.RestoreSnapshot(launcher.Snapshot{
		Screen: launcher.ScreenWork, AmbientProduct: "product-1", SelectedWorkID: "work-2", Section: launcher.SectionRelations,
		Coverage: "authoritative",
		Detail: launcher.WorkDetail{
			Item:     launcher.RankedWork{ID: "work-2", Kind: "bug", Title: "Done", Lifecycle: "completed", Priority: 2},
			Workflow: "execution",
			Projects: []string{"core"},
		},
	})
	m.Sync()
	rendered = m.Render()
	for _, want := range []string{"WORK: work-2", "WORKFLOW: execution"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Work screen detail pane lost %q: %q", want, rendered)
		}
	}
}

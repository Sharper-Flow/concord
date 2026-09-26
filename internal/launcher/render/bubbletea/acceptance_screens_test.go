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
	projects []launcher.ProjectOption
	resolve  func(key string) (launcher.SessionHandoff, error)
}

func (p *screenStub) Read(_ context.Context, request launcher.ReadRequest) (launcher.Snapshot, error) {
	switch request.Kind {
	case launcher.ReadProduct, launcher.ReadDomains:
		if p.product.Screen != "" {
			return p.product, nil
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
		if got := strings.Count(frame, "╭"); got != 1 {
			t.Fatalf("width %d pane count=%d, want the single-surface pane", width, got)
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

// rankedRowLine locates the work-list row whose cells name needle and returns
// the one frame line that carries it, so cell assertions inspect the row
// itself.
func rankedRowLine(t *testing.T, width int, frame string, content renderedPane, needle string) string {
	t.Helper()
	for _, row := range content.rows {
		identity := ""
		for _, cell := range row {
			if cell != "" {
				identity = cell
				break
			}
		}
		if identity == "" || !strings.Contains(identity, needle) {
			continue
		}
		for _, line := range strings.Split(frame, "\n") {
			if strings.Contains(line, identity) {
				return line
			}
		}
		t.Fatalf("width %d: row %q renders on no single frame line: %q", width, identity, frame)
	}
	t.Fatalf("width %d: no work row names %q: %q", width, needle, frame)
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
			Screen: launcher.ScreenProduct, AmbientProduct: "product-1", Coverage: "authoritative",
			Ranked: []launcher.RankedWork{{ID: "work-1", Title: "Readable work", Lifecycle: "needed", Priority: 1}},
		},
	}
	core := launcher.New(stub)
	if err := core.Enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := New(core, context.Background(), Profile{})
	assertSizedFlowFrames(t, m, "Readable Product")
	// Selecting the Product lands on its work list; Enter launches the
	// selected work's session straight from the row.
	m.UpdateKey("enter")
	m.Sync()
	assertSizedFlowFrames(t, m, "Readable work")
	var launched launcher.SessionHandoff
	m.SetSessionLauncher(func(handoff launcher.SessionHandoff) tea.Cmd {
		launched = handoff
		return nil
	})
	m.UpdateKey("enter")
	if launched.WorkID != "work-1" || launched.ProductID != "product-1" || launched.Agent != launcher.DefaultSessionAgent {
		t.Fatalf("session handoff = %#v", launched)
	}
	if got := core.Snapshot().Screen; got != launcher.ScreenProduct {
		t.Fatalf("launching from the row changed the screen to %q", got)
	}
	assertSizedFlowFrames(t, m, "Readable work")

	degraded := &screenStub{
		state: launcher.Snapshot{
			Screen: launcher.ScreenProduct, AmbientProduct: "product-1", Coverage: "authoritative", Backlog: true,
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
	assertSizedFlowFrames(t, degradedModel, "Project one")
}

// TestProductSelectPrecedesWorkList proves
// check:launcher.product_select_precedes_work: the screen selector is the
// outer selector in renderContent, so a populated candidate feed can never
// push the Product select or the Product work list out of the frame.
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
			Screen: launcher.ScreenProduct, AmbientProduct: "product-1", Coverage: "authoritative",
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
	if strings.Contains(rendered, "Scan Root") {
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
	if strings.Contains(rendered, "Scan Root") {
		t.Fatalf("candidate feed rendered over the Product screen: %q", rendered)
	}
}

// TestProductEntrySeatsTheRecencyWorkList proves check:launcher-work-list-
// recency-order at the frame: selecting a Product seats the work list sorted
// most recently updated first, whose rows carry the number, the readiness
// marker, the linked Linear key, the title, the blocking ticket reference,
// and the relative Updated and Live columns, ending with the New / Backlog
// row.
func TestProductEntrySeatsTheRecencyWorkList(t *testing.T) {
	stub := &screenStub{
		state: launcher.Snapshot{
			Screen: launcher.ScreenPortfolio, Coverage: "authoritative",
			Rows: []launcher.ProductRow{{ID: "product-1", Name: "Registered Product"}},
		},
		product: launcher.Snapshot{
			Screen: launcher.ScreenProduct, AmbientProduct: "product-1", Coverage: "authoritative",
			ActiveWorkOnly: true, Backlog: true,
			Ranked: []launcher.RankedWork{
				{ID: "work-linked", Title: "Linked work", Lifecycle: "in_progress", Priority: 1, LinearIssueKey: "CON-153", Live: 1, UpdatedAt: "2026-09-25T08:00:00Z", Blockers: []launcher.Blocker{{ID: "work-b", IssueKey: "CON-77"}}},
				{ID: "work-plain", Title: "Plain work", Lifecycle: "needed", Priority: 2, UpdatedAt: "2026-09-20T08:00:00Z"},
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
	rendered := m.Render()
	content := m.renderContent(m.snapshot, m.cursor)
	linkedRow := rankedRowLine(t, 120, rendered, content, "Linked work")
	for _, want := range []string{"1", "~ACTIVE", "CON-153", "Linked work", "!CON-77", "yes"} {
		if !strings.Contains(linkedRow, want) {
			t.Fatalf("entry work list row missing %q: %q", want, linkedRow)
		}
	}
	if !strings.Contains(rendered, "New / Backlog") {
		t.Fatalf("entry work list missing the New / Backlog row: %q", rendered)
	}
	if strings.Contains(rendered, "DOMAIN") {
		t.Fatalf("quiet frame rendered a Domain line for a clean registry: %q", rendered)
	}
}

// TestWorkListExcludesTerminalItems proves the picker half of
// check:launcher.work_list_product_scoped_active: the Product screen keeps
// terminal history in the shared read projection but the active-only picker
// drops it, so Enter can never land on finished work.
func TestWorkListExcludesTerminalItems(t *testing.T) {
	stub := &screenStub{state: launcher.Snapshot{
		Screen: launcher.ScreenProduct, AmbientProduct: "product-1", Coverage: "authoritative", ActiveWorkOnly: true,
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
	if strings.Contains(rendered, "work-done") || strings.Contains(rendered, "Finished work") {
		t.Fatalf("active picker rendered terminal work: %q", rendered)
	}
	if !strings.Contains(rendered, "Live work") {
		t.Fatalf("active picker lost its live row: %q", rendered)
	}
}

// TestWorkRowRendersIssueKeyAndOccupancy proves
// check:launcher.work_row_issue_key_and_occupancy at the render boundary: a
// work row carries its number, readiness marker, the correlated issue key
// when one exists, the title, the blocking ticket reference, and the
// host-attested occupancy state on its own line at every supported width. The
// row reserves the full linked key — a truncated key is a missing key — so
// the long real-store shapes drive the fixture: a 42-character issue key and
// a 37-character work ID. The single-surface table bounds the title, so the
// mandated cells seat on every row at every width.
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
		Screen: launcher.ScreenProduct, AmbientProduct: "product-1", Coverage: "authoritative", ActiveWorkOnly: true, Backlog: true,
		Ranked: []launcher.RankedWork{
			{ID: longID, Title: title, Lifecycle: "in_progress", Priority: 1, LinearIssueKey: longKey, LinearIssueURL: "https://linear.example/" + longKey, Live: 1, UpdatedAt: "2026-09-25T08:00:00Z"},
			{ID: "work-plain", Title: "Plain work", Lifecycle: "needed", Priority: 2, Ready: true},
		},
	}}
	core := launcher.New(stub)
	core.RestoreSnapshot(stub.state)
	m := New(core, context.Background(), Profile{})
	for _, width := range []int{80, 100, 120, 200} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
		frame := m.Render()
		content := m.renderContent(m.snapshot, m.cursor)
		linkedRow := rankedRowLine(t, width, frame, content, longKey[len(longKey)-6:])
		for _, want := range []string{"~ACTIVE", longKey, "yes"} {
			if !strings.Contains(linkedRow, want) {
				t.Fatalf("width %d: linked row line lost %q: %q", width, want, linkedRow)
			}
		}
		if width == 200 {
			if !strings.Contains(linkedRow, title) {
				t.Fatalf("width %d: the row starved its own title: %q", width, linkedRow)
			}
		} else if !strings.Contains(linkedRow, "…") || strings.Contains(linkedRow, title) {
			t.Fatalf("width %d: the over-width title neither truncated nor yielded to the mandated cells: %q", width, linkedRow)
		}
		plainRow := rankedRowLine(t, width, frame, content, "Plain work")
		for _, want := range []string{"+READY", "no"} {
			if !strings.Contains(plainRow, want) {
				t.Fatalf("width %d: plain row line lost %q: %q", width, want, plainRow)
			}
		}
		if strings.Contains(plainRow, longKey) {
			t.Fatalf("width %d: the plain row grew the linked key: %q", width, plainRow)
		}
		backlogRow := rankedRowLine(t, width, frame, content, "New / Backlog")
		if !strings.Contains(backlogRow, "New / Backlog") {
			t.Fatalf("width %d: the New / Backlog row lost its picker label: %q", width, backlogRow)
		}
		// The single-surface table carries the whole row on one line inside
		// the frame at every width.
		for _, line := range strings.Split(frame, "\n") {
			if got := lipgloss.Width(line); got > width {
				t.Fatalf("width %d: line exceeds the terminal: %d: %q", width, got, line)
			}
		}
	}
}

// TestOccupiedWorkRequiresExplicitConfirmation proves
// check:launcher.occupied_item_requires_confirmation: Enter on a work item a
// live session holds opens a confirmation gate, launches nothing until Enter
// confirms, and releases the item on Esc.
func TestOccupiedWorkRequiresExplicitConfirmation(t *testing.T) {
	stub := &screenStub{state: launcher.Snapshot{
		Screen: launcher.ScreenProduct, AmbientProduct: "product-1", Coverage: "authoritative", ActiveWorkOnly: true,
		Ranked: []launcher.RankedWork{{ID: "work-occupied", Title: "Occupied work", Lifecycle: "in_progress", Priority: 1, Live: 1}},
	}}
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
	// A fresh Enter confirms: the launch fires once, identity only, and no
	// work-detail screen ever appears.
	m.UpdateKey("enter")
	m.UpdateKey("enter")
	if len(launched) != 1 || launched[0].WorkID != "work-occupied" || launched[0].Agent != launcher.DefaultSessionAgent {
		t.Fatalf("confirmed launch = %#v, want one identity-only handoff", launched)
	}
	if core.Snapshot().Screen != launcher.ScreenProduct {
		t.Fatalf("confirmed launch moved the screen to %q", core.Snapshot().Screen)
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
			Screen: launcher.ScreenProduct, AmbientProduct: "product-1", Coverage: "authoritative", ActiveWorkOnly: true, Backlog: true,
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
	if !strings.Contains(rendered, "Scan Root") || !strings.Contains(rendered, "Candidate") {
		t.Fatalf("empty portfolio must render the candidate feed, got %q", rendered)
	}
	if !strings.Contains(rendered, "COVERAGE: first_run") {
		t.Fatalf("first-run coverage banner lost: %q", rendered)
	}
}

// TestOpenIssueOpensTheLinkedIssueFromTheRow proves the `o` action: the
// linked row opens its Linear issue in the browser, and a row without a
// linked issue offers nothing to open.
func TestOpenIssueOpensTheLinkedIssueFromTheRow(t *testing.T) {
	linked := launcher.RankedWork{
		ID: "work-1", Title: "Linked work", Lifecycle: "in_progress",
		LinearIssueKey: "CON-153", LinearIssueURL: "https://linear.example/CON-153", Live: 1,
	}
	plain := launcher.RankedWork{ID: "work-2", Title: "Plain work", Lifecycle: "needed", Ready: true}
	opened := []string{}
	original := openIssueURL
	openIssueURL = func(url string) error { opened = append(opened, url); return nil }
	defer func() { openIssueURL = original }()

	stub := &screenStub{state: launcher.Snapshot{
		Screen: launcher.ScreenProduct, AmbientProduct: "product-1", Coverage: "authoritative", ActiveWorkOnly: true,
		Ranked: []launcher.RankedWork{linked, plain},
	}}
	core := launcher.New(stub)
	core.RestoreSnapshot(stub.state)
	m := New(core, context.Background(), Profile{})
	m.UpdateKey("o")
	if len(opened) != 1 || opened[0] != linked.LinearIssueURL {
		t.Fatalf("o on the linked row opened %q", opened)
	}
	// The unlinked row offers nothing to open.
	m.UpdateKey("j")
	m.UpdateKey("o")
	if len(opened) != 1 {
		t.Fatalf("o on the unlinked row opened %q", opened)
	}
	// The portfolio screen has no issue to open either.
	core.RestoreSnapshot(launcher.Snapshot{Screen: launcher.ScreenPortfolio, Coverage: "authoritative", Rows: []launcher.ProductRow{{ID: "p-1", Name: "One"}}})
	m.Sync()
	m.UpdateKey("o")
	if len(opened) != 1 {
		t.Fatalf("o on the portfolio opened %q", opened)
	}
}

// TestFrameHasNoDiagnosticLines proves check:launcher-frame-no-diagnostic-
// lines: a healthy frame carries no title line, no diagnostic block, no
// always-on probe or summary lines, and no retired section labels — the one
// status line, the bordered table, and the footer are the whole frame.
func TestFrameHasNoDiagnosticLines(t *testing.T) {
	portfolio := launcher.Snapshot{
		Screen: launcher.ScreenPortfolio, AmbientProduct: "Concord", Coverage: "authoritative",
		Probes: []launcher.ProbeStatus{{Name: "vision", Available: true}, {Name: "lgrep", Available: true}},
		Rows:   []launcher.ProductRow{{ID: "p-1", Name: "Alpha", Stage: "in_progress", Reliance: "clear", Actions: 1, Focus: "Hold"}},
	}
	product := launcher.Snapshot{
		Screen: launcher.ScreenProduct, AmbientProduct: "product-1", Coverage: "authoritative", ActiveWorkOnly: true,
		Probes:   []launcher.ProbeStatus{{Name: "vision", Available: true}, {Name: "lgrep", Available: true}},
		Ranked:   []launcher.RankedWork{{ID: "work-1", Title: "Live work", Lifecycle: "in_progress"}},
		Domains:  launcher.DomainSection{Read: true, State: "authoritative"},
		Reliance: "authoritative", Watermark: "w9", ObservedAt: "1m",
	}
	for name, snapshot := range map[string]launcher.Snapshot{"portfolio": portfolio, "product": product} {
		t.Run(name, func(t *testing.T) {
			core := launcher.New(nil)
			core.RestoreSnapshot(snapshot)
			m := New(core, context.Background(), Profile{})
			m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
			m.Sync()
			rendered := m.Render()
			for _, retired := range []string{
				"CONCORD LAUNCHER", "PRODUCT:", "WATERMARK:", "SCREEN:", "RELIANCE:", "SECTION:",
				"S2 PRODUCT COORDINATION", "S3 WORK DETAIL", "DETAIL", "KNOWLEDGE", "HISTORY",
				"VISION:", "LGREP:", "STATUS:",
			} {
				if strings.Contains(rendered, retired) {
					t.Fatalf("healthy frame rendered %q: %q", retired, rendered)
				}
			}
			for _, required := range []string{"FOCUS:", "COVERAGE: authoritative", "╭"} {
				if !strings.Contains(rendered, required) {
					t.Fatalf("healthy frame lost %q: %q", required, rendered)
				}
			}
			if got := strings.Count(rendered, "╭"); got != 1 {
				t.Fatalf("frame rendered %d panes, want the single surface: %q", got, rendered)
			}
		})
	}
}

// TestWorkScreenLeavesTheFrame proves check:launcher-no-work-detail-screen: a
// work-detail snapshot renders no work screen — the projection answers
// nothing, so the frame keeps its single surface with no detail pane and no
// S3 labels.
func TestWorkScreenLeavesTheFrame(t *testing.T) {
	core := launcher.New(nil)
	core.RestoreSnapshot(launcher.Snapshot{Screen: launcher.ScreenWork, AmbientProduct: "product-1", SelectedWorkID: "work-1", Coverage: "authoritative"})
	m := New(core, context.Background(), Profile{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.Sync()
	rendered := m.Render()
	for _, retired := range []string{"S3 WORK DETAIL", "LIFECYCLE:", "WORKFLOW:", "HISTORY", "BLOCKER:", "DETAIL"} {
		if strings.Contains(rendered, retired) {
			t.Fatalf("work-detail frame rendered %q: %q", retired, rendered)
		}
	}
	if got := strings.Count(rendered, "╭"); got != 1 {
		t.Fatalf("work-detail frame rendered %d panes, want the single surface", got)
	}
	if content := m.renderContent(m.snapshot, m.cursor); len(content.rows) != 0 || len(content.tableHeaders) != 0 {
		t.Fatalf("the removed work screen still projects a table: %#v", content)
	}
}

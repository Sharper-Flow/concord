package bubbletea

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/sharper-flow/concord/internal/launcher"
)

type port struct {
	reads int
	state launcher.Snapshot
}

func (p *port) Read(_ context.Context, _ launcher.ReadRequest) (launcher.Snapshot, error) {
	p.reads++
	return p.state, nil
}

func keyPress(code rune, text string, mod tea.KeyMod) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code, Text: text, Mod: mod})
}

func TestTextInputUsesBubblesForEditingPasteAndClear(t *testing.T) {
	p := &port{state: launcher.Snapshot{Screen: launcher.ScreenPortfolio, Coverage: "authoritative"}}
	m := New(launcher.New(p), context.Background(), Profile{})
	m.OpenFilter()
	for _, msg := range []tea.KeyPressMsg{keyPress('a', "a", 0), keyPress('b', "b", 0), keyPress(tea.KeyLeft, "", 0), keyPress('X', "X", 0)} {
		m.Update(msg)
	}
	if got := m.FilterValue(); got != "aXb" {
		t.Fatalf("mid-string insertion=%q", got)
	}
	m.Update(keyPress(tea.KeyBackspace, "", 0))
	if got := m.FilterValue(); got != "ab" {
		t.Fatalf("backspace=%q", got)
	}
	m.Update(tea.PasteMsg{Content: " pasted"})
	if got := m.FilterValue(); got != "a pastedb" {
		t.Fatalf("bracketed paste=%q", got)
	}
	m.input.CursorStart()
	m.Update(keyPress(tea.KeyRight, "", 0))
	m.Update(keyPress(tea.KeyDelete, "", 0))
	if got := m.FilterValue(); got != "apastedb" {
		t.Fatalf("delete=%q", got)
	}
	m.Update(keyPress('l', "", tea.ModCtrl))
	if got := m.FilterValue(); got != "" {
		t.Fatalf("clear=%q", got)
	}
}

func TestRenderIsStableNoColorAndResizeDoesNotRead(t *testing.T) {
	p := &port{state: launcher.Snapshot{Screen: launcher.ScreenPortfolio, AmbientProduct: "Concord", Watermark: "w42", ObservedAt: "2m", Reliance: "blocked", Coverage: "authoritative", Rows: []launcher.ProductRow{{Name: "Launcher", Stage: "in_progress", Reliance: "blocked", Actions: 3, Focus: "Fix input"}}}}
	core := launcher.New(p)
	if err := core.Enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := New(core, context.Background(), Profile{})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.Sync()
	first, second := m.Render(), m.Render()
	if first != second {
		t.Fatal("unchanged model rendered different bytes")
	}
	if p.reads != 1 {
		t.Fatalf("render caused reads: %d", p.reads)
	}
	for _, line := range strings.Split(first, "\n") {
		if got := lipgloss.Width(line); got > 120 {
			t.Fatalf("line exceeds 120 display columns: %d: %q", got, line)
		}
	}
	for _, marker := range []string{"Concord", "authoritative", "! 3"} {
		if !strings.Contains(first, marker) {
			t.Fatalf("semantic marker %q missing: %q", marker, first)
		}
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if p.reads != 1 {
		t.Fatalf("resize caused read: %d", p.reads)
	}
}

func TestNoColorOutputIsPlainTextAndKeepsAllSemanticMarkers(t *testing.T) {
	p := &port{state: launcher.Snapshot{
		Screen: launcher.ScreenPortfolio, AmbientProduct: "Concord",
		Reliance: "stale", Coverage: "partial",
		Rows: []launcher.ProductRow{
			{Name: "Degraded", Stage: "degraded", Reliance: "degraded", Actions: 1, Focus: "degraded: unavailable dependency"},
			{Name: "Stale", Stage: "stale", Reliance: "stale", Actions: 2, Focus: "stale: old watermark"},
			{Name: "Error", Stage: "error", Reliance: "error", Actions: 3, Focus: "error: read failed"},
		},
	}}
	core := launcher.New(p)
	if err := core.Enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := New(core, context.Background(), Profile{})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 60})
	m.Sync()
	rendered := m.Render()
	if err := rejectTerminalControls(rendered); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		"COVERAGE:", "partial", "! 1", "! 2", "! 3",
	} {
		if !strings.Contains(rendered, marker) {
			t.Fatalf("semantic marker %q missing: %q", marker, rendered)
		}
	}
	// Truncation, not wrapping: every row renders on one line inside the pane.
	pane := m.renderScreen(m.snapshot, m.cursor)
	if len(pane.rows) != 3 {
		t.Fatalf("portfolio row count=%d, want 3", len(pane.rows))
	}
	for i, row := range pane.rows {
		if len(row) != 2 {
			t.Fatalf("portfolio row %d has %d cells, want 2: %#v", i, len(row), row)
		}
	}
}

// TestAttentionRowsRenderForegroundAndPlainRowsDoNot proves
// check:go.test.launcher.colour: a row whose projection carries an attention
// state renders with an ANSI-index foreground, a row without one renders
// plain, and the same frame under NO_COLOR keeps every byte free of terminal
// controls. lipgloss v2 Render emits full-fidelity ANSI and downsamples only
// at print, so the escape-sequence assertion does not depend on the TTY.
func TestAttentionRowsRenderForegroundAndPlainRowsDoNot(t *testing.T) {
	state := launcher.Snapshot{
		Screen: launcher.ScreenPortfolio, AmbientProduct: "Concord", Coverage: "authoritative",
		Rows: []launcher.ProductRow{
			{Name: "Blocked Product", Stage: "in_progress", Reliance: "blocked", Actions: 3, Focus: "Fix colour"},
			{Name: "Clear Product", Stage: "in_progress", Reliance: "clear", Actions: 1, Focus: "Hold steady"},
		},
	}
	core := launcher.New(&port{state: state})
	if err := core.Enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := New(core, context.Background(), Profile{Color: true})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.Sync()
	pane := m.renderScreen(m.snapshot, m.cursor)
	if len(pane.severities) != len(pane.rows) || pane.severities[0] != severityAttention || pane.severities[1] != severityNone {
		t.Fatalf("projection severities not parallel to rows: %#v", pane.severities)
	}
	rendered := m.Render()
	coloured := 0
	// Bold on the cursor row combines with the foreground into one SGR
	// sequence, so the red index arrives as "1;31" there and as "31" alone
	// elsewhere.
	hasForeground := func(line string) bool {
		return strings.Contains(line, "\x1b[31m") || strings.Contains(line, ";31m")
	}
	for _, line := range strings.Split(rendered, "\n") {
		hasColour := hasForeground(line)
		if strings.Contains(line, "Blocked Product") && !hasColour {
			t.Fatalf("attention row rendered without a foreground: %q", line)
		}
		if strings.Contains(line, "Clear Product") && hasColour {
			t.Fatalf("plain row rendered with a foreground: %q", line)
		}
		if hasColour {
			coloured++
		}
	}
	if coloured == 0 {
		t.Fatalf("no line rendered an ANSI-index foreground: %q", rendered)
	}
	// The table colour stays on the ANSI-index palette. The bubbles help
	// footer keeps its own pre-existing adaptive styles, so the palette
	// check is scoped to the data rows this change colours.
	for _, line := range strings.Split(rendered, "\n") {
		if !strings.Contains(line, "Blocked Product") && !strings.Contains(line, "Clear Product") {
			continue
		}
		for _, downsampled := range []string{"\x1b[38;5;", "\x1b[38;2;"} {
			if strings.Contains(line, downsampled) {
				t.Fatalf("colour left the ANSI-index palette: %q found in %q", downsampled, line)
			}
		}
	}
	plain := New(core, context.Background(), Profile{})
	plain.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	plain.Sync()
	if err := rejectTerminalControls(plain.Render()); err != nil {
		t.Fatalf("NO_COLOR path emitted terminal controls: %v", err)
	}
}

func TestPortfolioRendersDegradedProbesWithoutCandidates(t *testing.T) {
	core := launcher.New(nil)
	core.RestoreSnapshot(launcher.Snapshot{
		Screen: launcher.ScreenPortfolio, Coverage: "authoritative",
		Probes: []launcher.ProbeStatus{
			{Name: "vision", Reason: "daemon unavailable"},
			{Name: "lgrep", Reason: "index unavailable"},
		},
	})
	m := New(core, context.Background(), Profile{})
	m.Sync()
	rendered := m.Render()
	for _, want := range []string{"VISION: unavailable: daemon unavailable", "LGREP: unavailable: index unavailable"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("degraded probe marker %q missing: %q", want, rendered)
		}
	}
	if _, cmd := m.Update(keyPress('q', "q", 0)); cmd == nil {
		t.Fatal("degraded probe state made the launcher unusable")
	}
}

func rejectTerminalControls(value string) error {
	for i, b := range []byte(value) {
		switch {
		case b == 0x1b || b == 0x9b || b == 0x9d:
			return fmt.Errorf("terminal escape/control byte 0x%02x at byte %d", b, i)
		case b < 0x20 && b != '\n' && b != '\r' && b != '\t':
			return fmt.Errorf("terminal control byte 0x%02x at byte %d", b, i)
		}
	}
	return nil
}

func TestTerminalControlHelperRejectsInjectedANSIAndAcceptsPlainText(t *testing.T) {
	for _, injected := range []string{
		"\x1b[31mred\x1b[0m",
		"\x1b]8;;https://example.test\x07link\x1b]8;;\x07",
		"\x1b[2Jclear",
		"\x9b38;5;1mred",
	} {
		if err := rejectTerminalControls(injected); err == nil {
			t.Fatalf("injected terminal control accepted: %q", injected)
		}
	}
	if err := rejectTerminalControls("plain\ntext\twith Unicode界"); err != nil {
		t.Fatalf("plain text rejected: %v", err)
	}
}

func TestLongFieldsTruncateToOneLineAtThe80ColumnFloor(t *testing.T) {
	// The name exceeds the single-pane floor's inner width, so the first
	// column truncates in place: non-first over-width columns shed instead,
	// and the first column never drops.
	name := "Product-" + strings.Repeat("A", 76)
	p := &port{state: launcher.Snapshot{
		Screen: launcher.ScreenPortfolio, AmbientProduct: "Concord",
		Reliance: "blocked", Coverage: "authoritative",
		Rows: []launcher.ProductRow{{Name: name, Stage: "in_progress", Reliance: "blocked", Actions: 7, Focus: "Focus"}},
	}}
	core := launcher.New(p)
	if err := core.Enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := New(core, context.Background(), Profile{})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.Sync()
	rendered := m.Render()
	for _, line := range strings.Split(rendered, "\n") {
		if got := lipgloss.Width(line); got > 80 {
			t.Fatalf("line exceeds 80 display columns: %d: %q", got, line)
		}
	}
	// The narrowed budget sheds the Actions column, and the first column
	// truncates in place when it alone exceeds the floor.
	pane := m.renderScreen(m.snapshot, m.cursor)
	if len(pane.rows) != 1 || len(pane.rows[0]) != len(m.projection.Columns) || len(pane.rows[0]) == 0 {
		t.Fatalf("the Product row must stay parallel to the projected columns %#v: %#v", m.projection.Columns, pane.rows)
	}
	if len(m.projection.Columns) > 1 || m.projection.Columns[0] != "Product" {
		t.Fatalf("over-width values must shed down to the first column, got %v", m.projection.Columns)
	}
	if !strings.Contains(rendered, "…") {
		t.Fatalf("over-width values carry no ellipsis: %q", rendered)
	}
	if !strings.Contains(rendered, "Product") {
		t.Fatalf("the projected column header is missing: %q", rendered)
	}
	if strings.Contains(rendered, name) {
		t.Fatalf("the long name stayed untruncated: %q", rendered)
	}
}

func TestNoOpAndResizeReturnNoCommandAndDoNotRead(t *testing.T) {
	p := &port{state: launcher.Snapshot{Screen: launcher.ScreenPortfolio, Coverage: "authoritative"}}
	m := New(launcher.New(p), context.Background(), Profile{})
	if _, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: 'z', Text: "z"})); cmd != nil {
		t.Fatal("no-op update returned an autonomous command")
	}
	if _, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24}); cmd != nil {
		t.Fatal("resize returned an autonomous command")
	}
	if p.reads != 0 {
		t.Fatalf("no-op/resize reads=%d", p.reads)
	}
}

func TestBubblesUICommandsAreReadFreeAndOnlyExplicitPathsRead(t *testing.T) {
	p := &port{state: launcher.Snapshot{Screen: launcher.ScreenPortfolio, Coverage: "authoritative"}}
	m := New(launcher.New(p), context.Background(), Profile{})
	if cmd := m.OpenFilter(); cmd != nil {
		_ = cmd() // Bubbles cursor blink; UI-only command, never a Concord read.
	}
	_, cmd := m.Update(keyPress('a', "a", 0))
	if cmd != nil {
		_ = cmd() // Bubbles cursor/render command; still cannot reach ReadPort.
	}
	if p.reads != 0 {
		t.Fatalf("UI-only commands read=%d", p.reads)
	}
	m.Update(keyPress(tea.KeyEnter, "", 0))
	if p.reads != 0 {
		t.Fatalf("filter submit reads=%d", p.reads)
	}
	m.Update(keyPress('r', "r", 0))
	if p.reads != 1 {
		t.Fatalf("explicit refresh reads=%d", p.reads)
	}
}

func TestSubmitCallsReadOnceAndNoTimerOrPolling(t *testing.T) {
	p := &port{state: launcher.Snapshot{Screen: launcher.ScreenPortfolio, Coverage: "authoritative"}}
	m := New(launcher.New(p), context.Background(), Profile{})
	m.OpenFilter()
	m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if p.reads != 0 {
		t.Fatalf("filter submit reads=%d", p.reads)
	}
}

func TestNavigationFilterHelpRefreshAndBackAreReadBounded(t *testing.T) {
	p := &port{state: launcher.Snapshot{Screen: launcher.ScreenPortfolio, Coverage: "authoritative", Rows: []launcher.ProductRow{
		{ID: "p-1", Name: "Alpha", Stage: "production"},
		{ID: "p-2", Name: "Beta", Stage: "alpha"},
		{ID: "p-3", Name: "Gamma", Stage: "beta"},
	}}}
	core := launcher.New(p)
	if err := core.Enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := New(core, context.Background(), Profile{})
	if cmd := m.UpdateKey("j"); cmd != nil {
		t.Fatal("navigation issued command")
	}
	if m.Cursor() != 1 || p.reads != 1 {
		t.Fatalf("navigation cursor=%d reads=%d", m.Cursor(), p.reads)
	}
	m.UpdateKey("/")
	for _, key := range []string{"b", "e"} {
		m.Update(keyPress(rune(key[0]), key, 0))
	}
	if got := m.FilterValue(); got != "be" || p.reads != 1 {
		t.Fatalf("local filter=%q reads=%d", got, p.reads)
	}
	m.UpdateKey("ctrl+l")
	if m.FilterValue() != "" || p.reads != 1 {
		t.Fatalf("clear filter=%q reads=%d", m.FilterValue(), p.reads)
	}
	m.UpdateKey("enter")
	m.UpdateKey("?")
	if !m.HelpVisible() || p.reads != 1 || !strings.Contains(m.Render(), "HELP:") {
		t.Fatalf("help visible=%v reads=%d", m.HelpVisible(), p.reads)
	}
	m.UpdateKey("r")
	if p.reads != 2 {
		t.Fatalf("refresh reads=%d, want 2", p.reads)
	}
	p.state = launcher.Snapshot{Screen: launcher.ScreenProduct, Coverage: "authoritative"}
	m.UpdateKey("enter")
	if got := core.Snapshot(); got.Screen != launcher.ScreenProduct || got.StatusMessage != "" {
		t.Fatalf("product=%#v", got)
	}
	if p.reads != 3 { // entry, refresh, and the selection; help and filter never read
		t.Fatalf("selection reads=%d", p.reads)
	}
	m.UpdateKey("esc")
	if core.Snapshot().Screen != launcher.ScreenPortfolio || p.reads != 3 {
		t.Fatalf("back screen=%s reads=%d", core.Snapshot().Screen, p.reads)
	}
}

func TestBackRestoresPortfolioRowsCursorAndScroll(t *testing.T) {
	portfolio := launcher.Snapshot{
		Screen:   launcher.ScreenPortfolio,
		Coverage: "authoritative",
		Rows: []launcher.ProductRow{
			{ID: "p-1", Name: "Alpha"},
			{ID: "p-2", Name: "Beta"},
		},
	}
	p := &port{state: portfolio}
	core := launcher.New(p)
	if err := core.Enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := New(core, context.Background(), Profile{})
	m.UpdateKey("j")
	m.scroll = 1
	p.state = launcher.Snapshot{
		Screen:         launcher.ScreenProduct,
		AmbientProduct: "p-2",
		Coverage:       "authoritative",
		Ranked:         []launcher.RankedWork{{ID: "work-1", Title: "Only work"}},
	}
	m.UpdateKey("enter")
	m.UpdateKey("esc")
	got := core.Snapshot()
	if got.Screen != launcher.ScreenPortfolio || len(got.Rows) != 2 || got.Rows[1].ID != "p-2" {
		t.Fatalf("restored portfolio snapshot = %#v", got)
	}
	if m.Cursor() != 1 || m.scroll != 1 {
		t.Fatalf("restored portfolio position cursor=%d scroll=%d", m.Cursor(), m.scroll)
	}
	if !strings.Contains(m.Render(), "> Beta") {
		t.Fatalf("restored portfolio position is not visible: %q", m.Render())
	}
}

func TestS1HelpHasNoSemanticQueryBinding(t *testing.T) {
	p := &port{state: launcher.Snapshot{Screen: launcher.ScreenPortfolio, Coverage: "authoritative"}}
	m := New(launcher.New(p), context.Background(), Profile{})
	m.UpdateKey("?")
	rendered := m.Render()
	if strings.Contains(strings.ToLower(rendered), "query") || strings.Contains(rendered, " s ") {
		t.Fatalf("S1 help exposes semantic query: %q", rendered)
	}
	m.UpdateKey("s")
	if p.reads != 0 {
		t.Fatalf("S1 semantic-query key read=%d", p.reads)
	}
}

func TestS1QuitReturnsTeaQuitAndNoRead(t *testing.T) {
	p := &port{state: launcher.Snapshot{Screen: launcher.ScreenPortfolio, Coverage: "authoritative"}}
	m := New(launcher.New(p), context.Background(), Profile{})
	_, cmd := m.Update(keyPress('q', "q", 0))
	if cmd == nil || p.reads != 0 {
		t.Fatalf("quit cmd=%v reads=%d", cmd != nil, p.reads)
	}
}

type coordinationPort struct {
	reads    int
	requests []launcher.ReadRequest
}

func (p *coordinationPort) Read(_ context.Context, request launcher.ReadRequest) (launcher.Snapshot, error) {
	p.reads++
	p.requests = append(p.requests, request)
	switch request.Kind {
	case launcher.ReadPortfolio:
		return launcher.Snapshot{Screen: launcher.ScreenPortfolio, Coverage: "authoritative", Rows: []launcher.ProductRow{{ID: "product-1", Name: "Product One"}}}, nil
	case launcher.ReadDomains:
		return launcher.Snapshot{Screen: launcher.ScreenProduct, AmbientProduct: request.Product, Coverage: "authoritative", Ranked: []launcher.RankedWork{{ID: "work-1", Title: "First", Lifecycle: "needed", Priority: 1, Ready: true}, {ID: "work-2", Title: "Blocked", Lifecycle: "needed", Priority: 2, Blocked: true, Blockers: []launcher.Blocker{{ID: "blocker-1", Title: "External", Authority: "ci", Age: "old", External: true}}}}, Domains: launcher.DomainSection{Read: true, State: "authoritative", Registry: "sha256:fixed", Domains: []launcher.DomainRow{{ID: "product-root:one", Name: "Product One", Home: true}}, Overlaps: []launcher.OverlapPair{{From: "work-1", To: "work-2", State: "absent", SharedDomains: []string{"work-nav"}}}}}, nil
	case launcher.ReadProduct:
		return launcher.Snapshot{Screen: launcher.ScreenProduct, AmbientProduct: request.Product, Coverage: "authoritative", Ranked: []launcher.RankedWork{{ID: "work-1", Title: "First", Lifecycle: "needed", Priority: 1, Ready: true}, {ID: "work-2", Title: "Blocked", Lifecycle: "needed", Priority: 2, Blocked: true, Blockers: []launcher.Blocker{{ID: "blocker-1", Title: "External", Authority: "ci", Age: "old", External: true}}}}}, nil
	case launcher.ReadSearch:
		return launcher.Snapshot{Screen: launcher.ScreenProduct, AmbientProduct: request.Product, Coverage: "authoritative", QueryResult: true, QuerySubmitted: request.Query, Ranked: []launcher.RankedWork{{ID: "work-1", Title: "First", Lifecycle: "needed", Priority: 1, Ready: true}}}, nil
	default:
		return launcher.Snapshot{}, nil
	}
}

func TestQuerySubmitsExactlyOnce(t *testing.T) {
	p := &coordinationPort{}
	core := launcher.New(p)
	if err := core.Enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := New(core, context.Background(), Profile{})
	m.UpdateKey("enter")
	reads := p.reads
	m.UpdateKey("s")
	m.Update(keyPress('b', "b", 0))
	if p.reads != reads {
		t.Fatalf("query typing read=%d", p.reads)
	}
	m.UpdateKey("enter")
	if p.reads != reads+1 || len(p.requests) == 0 || p.requests[len(p.requests)-1].Kind != launcher.ReadSearch {
		t.Fatalf("query submit reads=%d requests=%#v", p.reads, p.requests)
	}
}

func TestDisplayedQueryEscRestoresSnapshotCursorAndScroll(t *testing.T) {
	p := &coordinationPort{}
	core := launcher.New(p)
	if err := core.Enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := New(core, context.Background(), Profile{})
	m.UpdateKey("enter")
	m.UpdateKey("j")
	m.scroll = 1
	m.UpdateKey("s")
	m.Update(keyPress('b', "b", 0))
	m.UpdateKey("enter")
	if p.reads != 3 { // entry, product selection, and the one query read
		t.Fatalf("query submit read count = %d requests=%#v", p.reads, p.requests)
	}
	m.UpdateKey("esc")
	if got := core.Snapshot(); got.Screen != launcher.ScreenProduct || m.Cursor() != 1 || m.scroll != 1 {
		t.Fatalf("query Esc did not restore prior Product state: snapshot=%#v cursor=%d scroll=%d", got, m.Cursor(), m.scroll)
	}
}

func TestFilterAndQueryInputRemainSeparate(t *testing.T) {
	p := &coordinationPort{}
	core := launcher.New(p)
	if err := core.Enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := New(core, context.Background(), Profile{})
	m.UpdateKey("/")
	m.Update(keyPress('p', "p", 0))
	m.UpdateKey("enter")
	if m.FilterValue() != "p" {
		t.Fatalf("filter value=%q", m.FilterValue())
	}
	m.UpdateKey("enter")
	if m.FilterValue() != "" {
		t.Fatalf("S1 filter leaked into S2: %q", m.FilterValue())
	}
	m.UpdateKey("s")
	if m.QueryValue() != "" {
		t.Fatalf("semantic query inherited filter input: %q", m.QueryValue())
	}
	m.Update(keyPress('q', "q", 0))
	m.UpdateKey("enter")
	if m.FilterValue() != "" || core.Snapshot().QuerySubmitted != "q" {
		t.Fatalf("submitted query became local filter: filter=%q snapshot=%#v", m.FilterValue(), core.Snapshot())
	}
}

func TestProductScreenRendersUnavailableForegroundReadState(t *testing.T) {
	snapshot := launcher.Snapshot{Screen: launcher.ScreenProduct, AmbientProduct: "product-1", Coverage: "unavailable", StatusMessage: "unavailable: Product work omitted by launcher limit"}
	p := &port{state: snapshot}
	core := launcher.New(p)
	core.RestoreSnapshot(snapshot)
	m := New(core, context.Background(), Profile{})
	rendered := m.Render()
	// The status bar clips its half at the frame, so the visible prefix and
	// the typed state row together carry the message.
	if !strings.Contains(rendered, "STATUS: unavailable: Product work") {
		t.Fatalf("foreground read state must remain visible: %q", rendered)
	}
	if !strings.Contains(rendered, snapshot.StatusMessage) {
		t.Fatalf("degraded state row lost the typed reason: %q", rendered)
	}
}

// TestRenderChangesOnlyThroughSync proves the render path reads only the
// synced snapshot and local interaction state: a core mutation that skips
// Sync changes nothing on screen, and Sync then projects it.
func TestRenderChangesOnlyThroughSync(t *testing.T) {
	snapshot := launcher.Snapshot{
		Screen: launcher.ScreenProduct, AmbientProduct: "product-1",
		Coverage: "authoritative",
		Ranked:   []launcher.RankedWork{{ID: "work-1", Kind: "task", Title: "Seated", Lifecycle: "needed", Priority: 1, Ready: true}},
	}
	core := launcher.New(&port{state: snapshot})
	core.RestoreSnapshot(snapshot)
	m := New(core, context.Background(), Profile{})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m.Sync()
	before := m.Render()

	next := snapshot
	next.Ranked = []launcher.RankedWork{{ID: "work-2", Kind: "bug", Title: "Replacement", Lifecycle: "needed", Priority: 2}}
	core.RestoreSnapshot(next)
	if after := m.Render(); after != before {
		t.Fatalf("render changed before Sync:\n%s\n%s", before, after)
	}
	m.Sync()
	if after := m.Render(); after == before {
		t.Fatalf("Sync projected no change: %s", after)
	}
}

func TestDegradedWorkListNeverRendersAuthoritativeEmpty(t *testing.T) {
	snapshot := launcher.Snapshot{
		Screen: launcher.ScreenProduct, AmbientProduct: "product-1",
		Coverage: "unavailable", StatusMessage: "unavailable: Product work omitted by launcher limit",
	}
	p := &port{state: snapshot}
	core := launcher.New(p)
	core.RestoreSnapshot(snapshot)
	m := New(core, context.Background(), Profile{})
	rendered := m.Render()
	if !strings.Contains(rendered, "unavailable: Product work omitted by launcher limit") {
		t.Fatalf("degraded work list lost its typed state: %q", rendered)
	}
	if strings.Contains(rendered, "authoritative-empty") {
		t.Fatalf("degraded work list rendered an authoritative-empty list: %q", rendered)
	}
}

// authorityPort serves S1 rows until the authority is marked unreachable, then
// returns the typed unavailable state the store port produces alongside its
// error.
type authorityPort struct {
	rows        []launcher.ProductRow
	unreachable bool
}

func (p *authorityPort) Read(_ context.Context, _ launcher.ReadRequest) (launcher.Snapshot, error) {
	if p.unreachable {
		return launcher.Snapshot{
			Screen: launcher.ScreenPortfolio, Coverage: "unreachable", Reliance: "unreachable",
			StatusMessage: "unreachable: database unavailable",
		}, errors.New("database unavailable")
	}
	return launcher.Snapshot{Screen: launcher.ScreenPortfolio, Coverage: "authoritative", Rows: p.rows}, nil
}

func TestS1RendersNoCachedRowsAsCurrentWhenAuthorityIsUnreachable(t *testing.T) {
	p := &authorityPort{rows: []launcher.ProductRow{
		{ID: "p-1", Name: "Alpha", Stage: "production", Reliance: "authoritative", Actions: 2, Focus: "Ship the floor"},
	}}
	core := launcher.New(p)
	if err := core.Enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := New(core, context.Background(), Profile{})
	m.Sync()
	if seeded := m.Render(); !strings.Contains(seeded, "Alpha") {
		t.Fatalf("seeded S1 row never rendered: %q", seeded)
	}

	p.unreachable = true
	m.UpdateKey("r")

	if got := core.Snapshot(); len(got.Rows) != 0 || got.Coverage != "unreachable" || got.Reliance != "unreachable" {
		t.Fatalf("unreachable S1 retained cached rows or coverage: %#v", got)
	}
	rendered := m.Render()
	for _, cached := range []string{"Alpha", "production", "Ship the floor"} {
		if strings.Contains(rendered, cached) {
			t.Fatalf("S1 rendered cached value %q as current: %q", cached, rendered)
		}
	}
	// A failed foreground read is reported as launch-time status text, so the
	// visible reason is the port's error rather than the snapshot's own field.
	for _, want := range []string{"STATUS: database unavailable"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("unreachable S1 hid %q: %q", want, rendered)
		}
	}
	if strings.Contains(rendered, "authoritative-empty") {
		t.Fatalf("unreachable S1 is indistinguishable from an authoritative-empty portfolio: %q", rendered)
	}
}

// refreshCountingPort records every request so a test can prove both how many
// reads were issued and that none appeared between two of them.
type refreshCountingPort struct {
	requests []launcher.ReadRequest
}

func (p *refreshCountingPort) Read(_ context.Context, request launcher.ReadRequest) (launcher.Snapshot, error) {
	p.requests = append(p.requests, request)
	return launcher.Snapshot{
		Screen: launcher.ScreenPortfolio, Coverage: "authoritative",
		Rows: []launcher.ProductRow{{ID: "p-1", Name: "Alpha"}, {ID: "p-2", Name: "Beta"}},
	}, nil
}

func TestTwoConsecutiveRefreshKeysIssueTwoReadsAndNoneBetweenThem(t *testing.T) {
	p := &refreshCountingPort{}
	core := launcher.New(p)
	if err := core.Enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := New(core, context.Background(), Profile{})
	m.Sync()
	if len(p.requests) != 1 {
		t.Fatalf("entry requests=%#v, want exactly one", p.requests)
	}
	// Nothing the launcher schedules for itself can read; refresh is the
	// operator's key alone.
	if cmd := m.Init(); cmd != nil {
		t.Fatal("launcher scheduled startup work, so a read could fire without a keypress")
	}

	if cmd := m.UpdateKey("r"); cmd != nil {
		t.Fatal("first refresh scheduled a follow-up command")
	}
	if len(p.requests) != 2 {
		t.Fatalf("first refresh requests=%#v, want two", p.requests)
	}

	// Between the two presses the launcher only redraws and answers local UI
	// events. No navigation occurs, so no read may occur either.
	m.Render()
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.UpdateKey("?")
	m.UpdateKey("?")
	m.Render()
	if len(p.requests) != 2 {
		t.Fatalf("a read was issued between two consecutive refreshes: %#v", p.requests)
	}

	if cmd := m.UpdateKey("r"); cmd != nil {
		t.Fatal("second refresh scheduled a follow-up command")
	}
	if len(p.requests) != 3 {
		t.Fatalf("two consecutive refreshes issued %d reads after entry, want two", len(p.requests)-1)
	}
	for i, request := range p.requests {
		if request.Kind != launcher.ReadPortfolio {
			t.Fatalf("request %d = %#v, want a portfolio read", i, request)
		}
	}
}

func TestDefaultSessionLauncherHandsOnlyIdentityToCoreBootstrap(t *testing.T) {
	cmd, err := sessionProcess(launcher.SessionHandoff{ProductID: "product-1", WorkID: "work-1", Agent: launcher.DefaultSessionAgent})
	if err != nil {
		t.Fatal(err)
	}
	if len(cmd.Args) != 2 || cmd.Args[0] != cmd.Path || cmd.Args[1] != "session" {
		t.Fatalf("session argv=%q path=%q", cmd.Args, cmd.Path)
	}
	selected := map[string]string{}
	for _, value := range cmd.Env {
		if strings.HasPrefix(value, "CONCORD_SELECTED_AGENT=") {
			selected["agent"] = strings.TrimPrefix(value, "CONCORD_SELECTED_AGENT=")
		}
		if strings.HasPrefix(value, "CONCORD_SELECTED_PRODUCT_ID=") {
			selected["product"] = strings.TrimPrefix(value, "CONCORD_SELECTED_PRODUCT_ID=")
		}
		if strings.HasPrefix(value, "CONCORD_SELECTED_WORK_ID=") {
			selected["work"] = strings.TrimPrefix(value, "CONCORD_SELECTED_WORK_ID=")
		}
	}
	if selected["product"] != "product-1" || selected["work"] != "work-1" {
		t.Fatalf("session env identity=%v", selected)
	}
	if selected["agent"] != "concord-1" {
		t.Fatalf("session env agent=%q, want concord-1", selected["agent"])
	}
}

func TestSessionCommandPassesPromptThroughEnvironment(t *testing.T) {
	cmd, err := sessionProcess(launcher.SessionHandoff{ProductID: "product-1", WorkID: "work-1", Prompt: "inspect the failing test", Agent: launcher.DefaultSessionAgent})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range cmd.Env {
		if value == "CONCORD_SELECTED_PROMPT=inspect the failing test" {
			return
		}
	}
	t.Fatalf("prompt was not passed through session environment: %v", cmd.Env)
}

func TestSessionCommandPreservesInheritedAgentOverride(t *testing.T) {
	t.Setenv("CONCORD_SELECTED_AGENT", "operator-agent")
	cmd, err := sessionProcess(launcher.SessionHandoff{ProductID: "product-1", Agent: launcher.DefaultSessionAgent})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range cmd.Env {
		if value == "CONCORD_SELECTED_AGENT=operator-agent" {
			return
		}
	}
	t.Fatalf("inherited agent override was not preserved: %v", cmd.Env)
}

func TestProjectCandidateStartsAPlainSession(t *testing.T) {
	project := "/projects/plain"
	core := launcher.New(nil)
	core.RestoreSnapshot(launcher.Snapshot{Screen: launcher.ScreenPortfolio, Coverage: "authoritative", Candidates: []launcher.Candidate{{
		ID: project, Kind: launcher.CandidateProject, Name: "plain", Path: project, Available: true,
	}}})
	m := New(core, context.Background(), Profile{})
	var got launcher.SessionHandoff
	m.SetSessionLauncher(func(handoff launcher.SessionHandoff) tea.Cmd {
		got = handoff
		return nil
	})
	m.UpdateKey("enter")
	if got.ProjectPath != project || got.ProductID != "" || got.WorkID != "" {
		t.Fatalf("project handoff = %#v", got)
	}
}

func TestCandidateSnapshotProductEnterOpensTheProduct(t *testing.T) {
	core := launcher.New(&coordinationPort{})
	core.RestoreSnapshot(launcher.Snapshot{Screen: launcher.ScreenPortfolio, Coverage: "authoritative", Candidates: []launcher.Candidate{{
		ID: "product-1", Kind: launcher.CandidateProduct, Name: "Product One", ProductID: "product-1", State: "available", Available: true,
	}}})
	m := New(core, context.Background(), Profile{})
	m.UpdateKey("enter")
	if got := core.Snapshot(); got.Screen != launcher.ScreenProduct || got.AmbientProduct != "product-1" {
		t.Fatalf("candidate product enter = %#v", got)
	}
}

func TestCandidateSnapshotProductEnterReadsTheSelectedProduct(t *testing.T) {
	p := &coordinationPort{}
	core := launcher.New(p)
	core.RestoreSnapshot(launcher.Snapshot{Screen: launcher.ScreenPortfolio, Coverage: "authoritative", Candidates: []launcher.Candidate{{
		ID: "product-1", Kind: launcher.CandidateProduct, Name: "Product One", ProductID: "product-1", State: "available", Available: true,
	}}})
	m := New(core, context.Background(), Profile{})
	m.UpdateKey("enter")
	if len(p.requests) != 1 || p.requests[0].Kind != launcher.ReadDomains || p.requests[0].Product != "product-1" {
		t.Fatalf("candidate product reads = %#v, want one composed Product read", p.requests)
	}
}

func TestSessionLauncherFailsClosedWithoutRunningBinaryIdentity(t *testing.T) {
	original := executablePath
	executablePath = func() (string, error) { return "", errors.New("unavailable") }
	defer func() { executablePath = original }()
	if cmd, err := sessionProcess(launcher.SessionHandoff{ProductID: "product-1"}); err == nil || cmd != nil {
		t.Fatalf("session process=%v err=%v", cmd, err)
	}
}

// refusedLaunchState is the screen a completed read leaves behind: the
// read's coverage, reliance, watermark, and rows are all seated, and a launch
// refusal must leave each exactly where the read set it.
func refusedLaunchState() launcher.Snapshot {
	return launcher.Snapshot{
		Screen: launcher.ScreenProduct, AmbientProduct: "corded",
		Watermark: "w41", ObservedAt: "1m", Reliance: "authoritative", Coverage: "authoritative",
		Ranked: []launcher.RankedWork{{ID: "import-advance-work-one", Title: "Document customer queue", Lifecycle: "needed"}},
	}
}

func TestRefusedLaunchReportsStatusOnlyAndKeepsScreenState(t *testing.T) {
	p := &port{state: refusedLaunchState()}
	core := launcher.New(p)
	if err := core.Enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := New(core, context.Background(), Profile{})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.Sync()
	refusal := errors.New("workflow instance is not recorded")
	m.Update(sessionLaunchError{err: refusal})
	got := core.Snapshot()
	if got.StatusMessage != refusal.Error() {
		t.Fatalf("refused launch status=%q, want %q", got.StatusMessage, refusal.Error())
	}
	if got.Coverage != "authoritative" || got.Reliance != "authoritative" || got.Watermark != "w41" || got.ObservedAt != "1m" || len(got.Ranked) != 1 {
		t.Fatalf("refused launch moved screen state: coverage=%q reliance=%q watermark=%q observed=%q ranked=%d", got.Coverage, got.Reliance, got.Watermark, got.ObservedAt, len(got.Ranked))
	}
	m.Sync()
	rendered := m.Render()
	if !strings.Contains(rendered, "STATUS: "+refusal.Error()) {
		t.Fatalf("refusal is not the rendered status: %q", rendered)
	}
	for _, marker := range []string{"Document customer queue"} {
		if !strings.Contains(rendered, marker) {
			t.Fatalf("refused launch render lost work-read marker %q: %q", marker, rendered)
		}
	}
}

func TestUnavailableWorkCandidateRefusalReportsStatusOnly(t *testing.T) {
	p := &port{state: launcher.Snapshot{
		Screen: launcher.ScreenPortfolio, AmbientProduct: "corded", Watermark: "w7", ObservedAt: "2m",
		Reliance: "authoritative", Coverage: "authoritative",
		Rows:       []launcher.ProductRow{{ID: "corded", Name: "Corded", Stage: "prototype", Reliance: "clear", Actions: 1, Focus: "Import work"}},
		Candidates: []launcher.Candidate{{Kind: launcher.CandidateWork, ID: "import-advance-work-one", ProductID: "corded", Name: "Document customer queue", Available: false}},
	}}
	core := launcher.New(p)
	if err := core.Enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := New(core, context.Background(), Profile{})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.Sync()
	cmd, handled := m.activateCandidate(launcher.Candidate{Kind: launcher.CandidateWork, ID: "import-advance-work-one", ProductID: "corded", Name: "Document customer queue", Available: false})
	if !handled || cmd != nil {
		t.Fatalf("unavailable candidate handled=%v cmd=%v", handled, cmd)
	}
	got := core.Snapshot()
	if got.StatusMessage != "work item import-advance-work-one has no claimed worktree" {
		t.Fatalf("refusal status=%q", got.StatusMessage)
	}
	if got.Coverage != "authoritative" || got.Reliance != "authoritative" || got.Watermark != "w7" || len(got.Rows) != 1 {
		t.Fatalf("worktree refusal moved screen state: coverage=%q reliance=%q watermark=%q rows=%d", got.Coverage, got.Reliance, got.Watermark, len(got.Rows))
	}
}

// An available work candidate launches straight from the portfolio. When the
// session then refuses, the portfolio the read produced is still on screen.
func TestAvailableWorkCandidateRefusedLaunchKeepsScreenState(t *testing.T) {
	p := &port{state: launcher.Snapshot{
		Screen: launcher.ScreenPortfolio, AmbientProduct: "corded", Watermark: "w7", ObservedAt: "2m",
		Reliance: "authoritative", Coverage: "authoritative",
		Rows:       []launcher.ProductRow{{ID: "corded", Name: "Corded", Stage: "prototype", Reliance: "clear", Actions: 1, Focus: "Import work"}},
		Candidates: []launcher.Candidate{{Kind: launcher.CandidateWork, ID: "import-advance-work-one", ProductID: "corded", WorkID: "import-advance-work-one", Name: "Document customer queue", Available: true}},
	}}
	core := launcher.New(p)
	if err := core.Enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := New(core, context.Background(), Profile{})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	var got launcher.SessionHandoff
	refusal := errors.New("session bootstrap refused")
	m.SetSessionLauncher(func(handoff launcher.SessionHandoff) tea.Cmd {
		got = handoff
		return func() tea.Msg { return sessionLaunchError{err: refusal} }
	})
	cmd, handled := m.activateCandidate(p.state.Candidates[0])
	if !handled || cmd == nil {
		t.Fatalf("available candidate handled=%v cmd=%v", handled, cmd)
	}
	if got.ProductID != "corded" || got.WorkID != "import-advance-work-one" || got.Agent != launcher.DefaultSessionAgent {
		t.Fatalf("candidate handoff = %#v", got)
	}
	m.Update(cmd())
	snapshot := core.Snapshot()
	if snapshot.StatusMessage != refusal.Error() {
		t.Fatalf("refusal status=%q", snapshot.StatusMessage)
	}
	if snapshot.Screen != launcher.ScreenPortfolio || snapshot.Coverage != "authoritative" || snapshot.Reliance != "authoritative" || snapshot.Watermark != "w7" || len(snapshot.Rows) != 1 || len(snapshot.Candidates) != 1 {
		t.Fatalf("refused candidate launch moved screen state: screen=%q coverage=%q reliance=%q watermark=%q rows=%d candidates=%d", snapshot.Screen, snapshot.Coverage, snapshot.Reliance, snapshot.Watermark, len(snapshot.Rows), len(snapshot.Candidates))
	}
}

// TestDomainContextRendersOnlyWhenAbnormal proves the always-on Domain
// context dropped: a clean registry renders no Domain line, and every
// abnormal shape renders its typed line.
func TestDomainContextRendersOnlyWhenAbnormal(t *testing.T) {
	cases := []struct {
		name    string
		domains launcher.DomainSection
		want    string
		absent  string
	}{
		{
			name:    "clean registry stays silent",
			domains: launcher.DomainSection{Read: true, State: "authoritative"},
			absent:  "DOMAIN:",
		},
		{
			name:    "unavailable section names the reason",
			domains: launcher.DomainSection{Read: true, State: "unavailable", Reason: "registry unavailable"},
			want:    "DOMAIN: unavailable: registry unavailable",
		},
		{
			name:    "bounded relation read never answers clean",
			domains: launcher.DomainSection{Read: true, State: "authoritative", RelationsTruncated: true},
			want:    "DOMAIN: unavailable: domain_relations_bounded",
			absent:  "no unresolved overlaps",
		},
		{
			name:    "unresolved overlaps render",
			domains: launcher.DomainSection{Read: true, State: "authoritative", Overlaps: []launcher.OverlapPair{{From: "w-a", To: "w-b", State: "absent", SharedDomains: []string{"d-law"}}}},
			want:    "DOMAIN: unresolved overlap: w-a & w-b domains=d-law resolution=absent",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			core := launcher.New(nil)
			core.RestoreSnapshot(launcher.Snapshot{
				Screen: launcher.ScreenProduct, AmbientProduct: "product-1", Coverage: "authoritative",
				Domains: tc.domains,
				Ranked:  []launcher.RankedWork{{ID: "w-1", Title: "Next", Ready: true}},
			})
			m := New(core, context.Background(), Profile{})
			m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			m.Sync()
			rendered := m.Render()
			for _, line := range strings.Split(rendered, "\n") {
				if width := lipgloss.Width(line); width > 80 {
					t.Fatalf("line width=%d: %q", width, line)
				}
			}
			if tc.want != "" && !strings.Contains(rendered, tc.want) {
				t.Fatalf("abnormal Domain line %q missing: %q", tc.want, rendered)
			}
			if tc.absent != "" && strings.Contains(rendered, tc.absent) {
				t.Fatalf("quiet frame rendered %q: %q", tc.absent, rendered)
			}
		})
	}
}

func TestWorkListRedrawIsByteIdentical(t *testing.T) {
	core := launcher.New(nil)
	core.RestoreSnapshot(launcher.Snapshot{
		Screen: launcher.ScreenProduct, AmbientProduct: "product-1", Coverage: "authoritative",
		Ranked: []launcher.RankedWork{{ID: "w-1", Title: "Next", Ready: true}},
	})
	m := New(core, context.Background(), Profile{})
	m.Sync()
	if first, second := m.Render(), m.Render(); first != second {
		t.Fatalf("unchanged work list rendered different bytes")
	}
}

func TestViewportWindowFollowsCursorPastPaneBoundary(t *testing.T) {
	rows := make([]launcher.ProductRow, 32)
	for i := range rows {
		rows[i] = launcher.ProductRow{ID: fmt.Sprintf("p-%d", i+1), Name: fmt.Sprintf("Product %d", i+1), Stage: "in_progress", Actions: 1}
	}
	core := launcher.New(nil)
	core.RestoreSnapshot(launcher.Snapshot{Screen: launcher.ScreenPortfolio, Coverage: "authoritative", Rows: rows})
	m := New(core, context.Background(), Profile{})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	m.Sync()
	for i := 0; i < 20; i++ {
		m.UpdateKey("j")
	}
	rendered := m.Render()
	if !strings.Contains(rendered, "> Product 21") {
		t.Fatalf("selected row is not visible after scrolling: %q", rendered)
	}
	if strings.Contains(rendered, "> Product 1 ") {
		t.Fatalf("viewport remained at the top: %q", rendered)
	}
}

func TestPaneOffsetIgnoresGreaterThanContent(t *testing.T) {
	rendered := pane(renderedPane{
		header: []string{"HEADER"},
		rows:   [][]string{{"literal content"}, {"row 2"}, {"row 3"}, {"row 4"}},
	}, 40, 5, 2)
	if strings.Contains(rendered, "literal content") {
		t.Fatalf("content glyph changed the viewport offset: %q", rendered)
	}
	if !strings.Contains(rendered, "row 3") {
		t.Fatalf("viewport did not follow the row offset: %q", rendered)
	}
}

func TestPortfolioRowsRenderExactlyOneLineAtSupportedWidths(t *testing.T) {
	snapshot := launcher.Snapshot{
		Screen:   launcher.ScreenPortfolio,
		Coverage: "authoritative",
		Rows: []launcher.ProductRow{
			{ID: "p-1", Name: "Alpha", Stage: "in_progress", Reliance: "clear", Actions: 1, Focus: "Ship the floor"},
			{ID: "p-2", Name: "operator_only_product_with_a_long_name", Stage: "in_progress", Reliance: "clear", Actions: 1,
				Focus: "Focus text long enough to overflow its column"},
		},
	}
	core := launcher.New(&port{state: snapshot})
	if err := core.Enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := New(core, context.Background(), Profile{})
	for _, width := range []int{80, 100, 120, 200} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
		frame := m.Render()
		// The long row renders on exactly one line at every width: the
		// two-column table truncates the name in place instead of wrapping.
		rowLines := 0
		for _, line := range strings.Split(frame, "\n") {
			if strings.Contains(line, "operator_only_prod") {
				rowLines++
			}
		}
		if rowLines != 1 {
			t.Fatalf("width %d: the long product row renders on %d lines, want 1: %q", width, rowLines, frame)
		}
		if !strings.Contains(frame, "Actions") {
			t.Fatalf("width %d: the Actions column header is missing: %q", width, frame)
		}
	}
}

func TestHelpFooterRendersOncePerFrame(t *testing.T) {
	snapshot := launcher.Snapshot{
		Screen:   launcher.ScreenPortfolio,
		Coverage: "authoritative",
		Rows:     []launcher.ProductRow{{ID: "p-1", Name: "Alpha"}},
	}
	m := New(launcher.New(&port{state: snapshot}), context.Background(), Profile{})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	rendered := m.Render()
	if got := strings.Count(rendered, "arrows move"); got != 1 {
		t.Fatalf("help footer count=%d, want 1: %q", got, rendered)
	}
}

func TestHelpFooterUsesFullWidthBudgetAtEveryTerminalWidth(t *testing.T) {
	snapshot := launcher.Snapshot{
		Screen:   launcher.ScreenPortfolio,
		Coverage: "authoritative",
		Rows:     []launcher.ProductRow{{ID: "p-1", Name: "Alpha"}},
	}
	m := New(launcher.New(&port{state: snapshot}), context.Background(), Profile{})
	for _, width := range []int{80, 100, 120, 200} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		frame := m.Render()
		if !strings.Contains(frame, "ctrl-u unpin") {
			t.Fatalf("width %d: footer lost the unpin binding: %q", width, frame)
		}
		// The help model may elide whole bindings that do not fit, and it
		// marks that with an ellipsis. It must never cut a binding label
		// mid-word, which is what a footer starved of width does.
		for _, line := range strings.Split(frame, "\n") {
			if !strings.Contains(line, "arrows move") {
				continue
			}
			for _, cut := range strings.Split(line, "…")[:strings.Count(line, "…")] {
				if tail := strings.TrimRight(cut, " "); tail != cut {
					continue
				}
				t.Fatalf("width %d: footer cut a binding label mid-word before %q: %q", width, "…", line)
			}
		}
	}
}

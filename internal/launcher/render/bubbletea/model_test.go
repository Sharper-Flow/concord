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
	m.Sync()
	first, second := m.Render(), m.Render()
	if first != second {
		t.Fatal("unchanged model rendered different bytes")
	}
	if p.reads != 1 {
		t.Fatalf("render caused reads: %d", p.reads)
	}
	for _, line := range strings.Split(first, "\n") {
		if got := lipgloss.Width(line); got > 80 {
			t.Fatalf("line exceeds 80 display columns: %d: %q", got, line)
		}
	}
	for _, marker := range []string{"Concord", "w42", "2m", "authoritative", "! blocked", "in_progress", "3", "Fix input"} {
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
		Screen: launcher.ScreenPortfolio, AmbientProduct: "Concord", Watermark: "w-stale", ObservedAt: "old",
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
	m.Sync()
	rendered := m.Render()
	if err := rejectTerminalControls(rendered); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		"PRODUCT:", "Concord", "WATERMARK:", "w-stale", "AGE:", "old", "RELIANCE:", "stale", "COVERAGE:", "partial",
		"degraded", "! degraded", "stale", "! stale", "error", "! error", "1", "2", "3",
	} {
		if !strings.Contains(rendered, marker) {
			t.Fatalf("semantic marker %q missing: %q", marker, rendered)
		}
	}
	widths := columnWidths(80)
	for i, value := range []string{"degraded: unavailable dependency", "stale: old watermark", "error: read failed"} {
		for _, chunk := range splitDisplay(value, widths[4]) {
			if !strings.Contains(rendered, chunk) {
				t.Fatalf("semantic value chunk %q missing for field %d", chunk, i)
			}
		}
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

func TestLongFieldsWrapByRendererDisplayWidth(t *testing.T) {
	name := "Product-" + strings.Repeat("A", 52)
	stage := "進行中" + strings.Repeat("e\u0301", 20)
	reliance := "blocked-" + strings.Repeat("!", 38)
	focus := "Focus-" + strings.Repeat("界", 24)
	p := &port{state: launcher.Snapshot{
		Screen: launcher.ScreenPortfolio, AmbientProduct: "Product-" + strings.Repeat("界", 22),
		Watermark: "watermark-" + strings.Repeat("W", 34), ObservedAt: "observed-" + strings.Repeat("o", 34),
		Reliance: "blocked", Coverage: "authoritative",
		Rows: []launcher.ProductRow{{Name: name, Stage: stage, Reliance: reliance, Actions: 7, Focus: focus}},
	}}
	core := launcher.New(p)
	if err := core.Enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := New(core, context.Background(), Profile{})
	m.Sync()
	rendered := m.Render()
	for _, line := range strings.Split(rendered, "\n") {
		if got := lipgloss.Width(line); got > 80 {
			t.Fatalf("wrapped line exceeds 80 display columns: %d: %q", got, line)
		}
	}
	for _, label := range []string{"PRODUCT:", "WATERMARK:", "AGE:", "SCREEN:", "RELIANCE:", "COVERAGE:", "Product", "Stage", "Reliance", "Actions", "Focus"} {
		if !strings.Contains(rendered, label) {
			t.Fatalf("semantic label %q missing: %q", label, rendered)
		}
	}
	widths := columnWidths(80)
	for i, value := range []string{name, stage, "! " + reliance, "7", focus} {
		for _, chunk := range splitDisplay(value, widths[i]) {
			if !strings.Contains(rendered, chunk) {
				t.Fatalf("wrapped value chunk %q missing for field %d: %q", chunk, i, rendered)
			}
		}
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

func TestS1NavigationFilterHelpRefreshAndS2BackAreReadBounded(t *testing.T) {
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
	p.state = launcher.Snapshot{Screen: launcher.ScreenProduct, Section: launcher.SectionRelations, Coverage: "authoritative"}
	m.UpdateKey("enter")
	if got := core.Snapshot(); got.Screen != launcher.ScreenProduct || got.StatusMessage != "" {
		t.Fatalf("S2=%#v", got)
	}
	if p.reads != 4 {
		t.Fatalf("selection reads=%d", p.reads)
	}
	m.UpdateKey("esc")
	if core.Snapshot().Screen != launcher.ScreenPortfolio || p.reads != 4 {
		t.Fatalf("back screen=%s reads=%d", core.Snapshot().Screen, p.reads)
	}
}

func TestS2BackRestoresPortfolioRowsCursorAndScroll(t *testing.T) {
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
		Section:        launcher.SectionRanked,
		Coverage:       "authoritative",
		Rows:           []launcher.ProductRow{{ID: "work-1", Name: "S2 row"}},
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
		return launcher.Snapshot{Screen: launcher.ScreenProduct, AmbientProduct: request.Product, Section: launcher.SectionDomains, Coverage: "authoritative", Ranked: []launcher.RankedWork{{ID: "work-1", Title: "First", Lifecycle: "needed", Priority: 1, Ready: true}, {ID: "work-2", Title: "Blocked", Lifecycle: "needed", Priority: 2, Blocked: true, Blockers: []launcher.Blocker{{ID: "blocker-1", Title: "External", Authority: "ci", Age: "old", External: true}}}}, Relations: launcher.RelationTree{Edges: []launcher.RelationEdge{{Kind: "parent", Source: "work-1", Target: "work-2"}}, Clusters: [][]string{{"work-1", "work-2"}}, Roots: []string{"work-1"}, Depth: 3}, Domains: launcher.DomainSection{Read: true, State: "authoritative", Registry: "sha256:fixed", Domains: []launcher.DomainRow{{ID: "product-root:one", Name: "Product One", Home: true}, {ID: "work-nav", Name: "Work navigation", ParentID: "product-root:one"}}, Relations: []launcher.DomainRelationEdge{{Kind: "depends_on", Source: "work-nav", Target: "product-root:one", State: "active"}}, Overlaps: []launcher.OverlapPair{{From: "work-1", To: "work-2", State: "absent", SharedDomains: []string{"work-nav"}}}}}, nil
	case launcher.ReadProduct:
		return launcher.Snapshot{Screen: launcher.ScreenProduct, AmbientProduct: request.Product, Section: request.Section, Coverage: "authoritative", Ranked: []launcher.RankedWork{{ID: "work-1", Title: "First", Lifecycle: "needed", Priority: 1, Ready: true}, {ID: "work-2", Title: "Blocked", Lifecycle: "needed", Priority: 2, Blocked: true, Blockers: []launcher.Blocker{{ID: "blocker-1", Title: "External", Authority: "ci", Age: "old", External: true}}}}, Relations: launcher.RelationTree{Edges: []launcher.RelationEdge{{Kind: "parent", Source: "work-1", Target: "work-2"}}, Clusters: [][]string{{"work-1", "work-2"}}, Roots: []string{"work-1"}, Depth: 3}}, nil
	case launcher.ReadSearch:
		return launcher.Snapshot{Screen: launcher.ScreenProduct, AmbientProduct: request.Product, Section: launcher.SectionRanked, Coverage: "authoritative", QueryResult: true, QuerySubmitted: request.Query, Ranked: []launcher.RankedWork{{ID: "work-1", Title: "First", Lifecycle: "needed", Priority: 1, Ready: true}}}, nil
	case launcher.ReadWork:
		return launcher.Snapshot{Screen: launcher.ScreenWork, AmbientProduct: request.Product, SelectedWorkID: request.Work, Section: request.Section, Coverage: "authoritative", Detail: launcher.WorkDetail{Item: launcher.RankedWork{ID: request.Work, Title: "First", Lifecycle: "needed", Priority: 1}, History: []string{"created"}}}, nil
	case launcher.ReadKnowledge:
		return launcher.Snapshot{Screen: launcher.ScreenProduct, AmbientProduct: request.Product, Section: launcher.SectionKnowledge, Coverage: "authoritative", Knowledge: launcher.KnowledgeSection{Read: true, State: "authoritative-empty"}}, nil
	default:
		return launcher.Snapshot{}, nil
	}
}

func TestS2S3NavigationRestoresProductSelectionAndScroll(t *testing.T) {
	p := &coordinationPort{}
	core := launcher.New(p)
	if err := core.Enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := New(core, context.Background(), Profile{})
	m.UpdateKey("enter")
	if core.Snapshot().Screen != launcher.ScreenProduct || p.reads != 3 {
		t.Fatalf("S2=%#v reads=%d", core.Snapshot(), p.reads)
	}
	m.UpdateKey("tab") // domain -> blocked
	m.UpdateKey("tab") // blocked -> next
	m.UpdateKey("j")
	m.UpdateKey("enter")
	if core.Snapshot().Screen != launcher.ScreenWork || core.Handoff().WorkID != "work-2" || p.reads != 4 {
		t.Fatalf("S3=%#v reads=%d", core.Snapshot(), p.reads)
	}
	m.UpdateKey("esc")
	if core.Snapshot().Screen != launcher.ScreenProduct || m.Cursor() != 1 || core.Section() != launcher.SectionRanked {
		t.Fatalf("restored product=%#v cursor=%d", core.Snapshot(), m.Cursor())
	}
}

func TestS3BackRestoresProductSnapshotCursorAndScrollForEveryBackKey(t *testing.T) {
	for _, backKey := range []string{"esc", "h", "left", "q"} {
		t.Run(backKey, func(t *testing.T) {
			p := &coordinationPort{}
			core := launcher.New(p)
			if err := core.Enter(context.Background()); err != nil {
				t.Fatal(err)
			}
			m := New(core, context.Background(), Profile{})
			m.UpdateKey("enter")
			m.UpdateKey("tab")
			m.UpdateKey("tab")
			m.UpdateKey("j")
			m.scroll = 1
			product := core.Snapshot()
			m.UpdateKey("enter")
			if core.Snapshot().Screen != launcher.ScreenWork {
				t.Fatalf("work snapshot = %#v", core.Snapshot())
			}
			m.UpdateKey(backKey)
			got := core.Snapshot()
			if got.Screen != launcher.ScreenProduct || got.Section != product.Section || len(got.Ranked) != len(product.Ranked) {
				t.Fatalf("restored product snapshot = %#v, want %#v", got, product)
			}
			if m.Cursor() != 1 || m.scroll != 1 {
				t.Fatalf("restored product position cursor=%d scroll=%d", m.Cursor(), m.scroll)
			}
		})
	}
}

func TestS2PanelFocusAndQuerySubmitsExactlyOnce(t *testing.T) {
	p := &coordinationPort{}
	core := launcher.New(p)
	if err := core.Enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := New(core, context.Background(), Profile{})
	m.UpdateKey("enter")
	reads := p.reads
	m.UpdateKey("tab") // domain -> blocked
	if p.reads != reads {
		t.Fatalf("domain to blocked read=%d", p.reads)
	}
	m.UpdateKey("tab") // blocked -> next
	if p.reads != reads {
		t.Fatalf("blocked to next read=%d", p.reads)
	}
	m.UpdateKey("tab") // next -> domain
	if p.reads != reads {
		t.Fatalf("S2 panel cycling must not read=%d", p.reads)
	}
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
	m.UpdateKey("tab") // domain -> blocked
	m.UpdateKey("tab") // blocked -> next
	m.UpdateKey("j")
	m.scroll = 1
	m.UpdateKey("s")
	m.Update(keyPress('b', "b", 0))
	m.UpdateKey("enter")
	if p.reads != 4 {
		t.Fatalf("query must make exactly one port read: %d", p.reads)
	}
	m.UpdateKey("esc")
	if got := core.Snapshot(); got.Screen != launcher.ScreenProduct || got.Section != launcher.SectionRanked || m.Cursor() != 1 || m.scroll != 1 {
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

func TestS2AndS3RenderUnavailableForegroundReadState(t *testing.T) {
	for _, snapshot := range []launcher.Snapshot{
		{Screen: launcher.ScreenProduct, AmbientProduct: "product-1", Section: launcher.SectionRanked, Coverage: "unavailable", StatusMessage: "unavailable: Product work omitted by launcher limit"},
		{Screen: launcher.ScreenWork, AmbientProduct: "product-1", SelectedWorkID: "work-1", Section: launcher.SectionRelations, Coverage: "unreachable", StatusMessage: "database unavailable"},
	} {
		p := &port{state: snapshot}
		core := launcher.New(p)
		core.RestoreSnapshot(snapshot)
		m := New(core, context.Background(), Profile{})
		rendered := m.Render()
		if !strings.Contains(rendered, "STATUS: "+snapshot.StatusMessage) {
			t.Fatalf("foreground read state must remain visible: %q", rendered)
		}
	}
}

func TestS2DrillDownRendersKindReadinessAndTerminalAt(t *testing.T) {
	snapshot := launcher.Snapshot{
		Screen: launcher.ScreenProduct, AmbientProduct: "product-1", Section: launcher.SectionRanked,
		PanelFocus: launcher.S2PanelBlocked, Coverage: "authoritative",
		Ranked: []launcher.RankedWork{
			{ID: "work-1", Kind: "task", Title: "Live", Lifecycle: "needed", Priority: 1, Ready: true},
			{ID: "work-2", Kind: "bug", Title: "Done", Lifecycle: "completed", Priority: 2, Terminal: true, TerminalAt: "2026-08-05T00:00:00Z"},
		},
	}
	p := &port{state: snapshot}
	core := launcher.New(p)
	core.RestoreSnapshot(snapshot)
	m := New(core, context.Background(), Profile{})
	rendered := m.Render()
	for _, want := range []string{
		"kind=task", "kind=bug",
		"+READY", "-TERMINAL",
		"terminal=2026-08-05T00:00:00Z",
		"lifecycle=completed",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("drill-down line missing %q: %q", want, rendered)
		}
	}
	if again := m.Render(); again != rendered {
		t.Fatalf("drill-down render changed between frames:\n%s\n%s", rendered, again)
	}
}

func TestS2DegradedDrillDownNeverRendersAuthoritativeEmpty(t *testing.T) {
	for _, focus := range []launcher.S2Panel{launcher.S2PanelBlocked, launcher.S2PanelNext} {
		snapshot := launcher.Snapshot{
			Screen: launcher.ScreenProduct, AmbientProduct: "product-1", Section: launcher.SectionRanked,
			PanelFocus: focus, Coverage: "unavailable", StatusMessage: "unavailable: Product work omitted by launcher limit",
		}
		p := &port{state: snapshot}
		core := launcher.New(p)
		core.RestoreSnapshot(snapshot)
		m := New(core, context.Background(), Profile{})
		rendered := m.Render()
		if !strings.Contains(rendered, "unavailable: Product work omitted by launcher limit") {
			t.Fatalf("degraded %s drill-down lost its typed state: %q", focus, rendered)
		}
		if strings.Contains(rendered, "authoritative-empty") {
			t.Fatalf("degraded %s drill-down rendered an authoritative-empty list: %q", focus, rendered)
		}
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
	for _, want := range []string{"RELIANCE: unreachable", "COVERAGE: unreachable", "STATUS: database unavailable"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("unreachable S1 hid %q: %q", want, rendered)
		}
	}
	if strings.Contains(rendered, "PORTFOLIO: authoritative-empty") {
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

func TestLaunchHandoffIsIdentityOnlyAndS1CannotReachWork(t *testing.T) {
	p := &coordinationPort{}
	core := launcher.New(p)
	if err := core.Enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := core.SelectWork(context.Background(), "work-1"); err != nil {
		t.Fatal(err)
	}
	if core.Snapshot().Screen != launcher.ScreenPortfolio || p.reads != 1 {
		t.Fatalf("ambient-less work selection=%#v reads=%d", core.Snapshot(), p.reads)
	}
	m := New(core, context.Background(), Profile{})
	called := launcher.SessionHandoff{}
	m.SetSessionLauncher(func(handoff launcher.SessionHandoff) tea.Cmd { called = handoff; return func() tea.Msg { return nil } })
	m.UpdateKey("enter")
	m.UpdateKey("l")
	if called != (launcher.SessionHandoff{ProductID: "product-1"}) {
		t.Fatalf("S2 handoff=%#v", called)
	}
	// S2 opens on the Domain panel; two tabs reach the ranked work mode.
	m.UpdateKey("tab")
	m.UpdateKey("tab")
	m.UpdateKey("enter")
	m.UpdateKey("l")
	if called.ProductID != "product-1" || called.WorkID != "work-1" {
		t.Fatalf("S3 handoff=%#v", called)
	}
}

func TestDefaultSessionLauncherHandsOnlyIdentityToCoreBootstrap(t *testing.T) {
	cmd, err := sessionProcess(launcher.SessionHandoff{ProductID: "product-1", WorkID: "work-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cmd.Args) != 2 || cmd.Args[0] != cmd.Path || cmd.Args[1] != "session" {
		t.Fatalf("session argv=%q path=%q", cmd.Args, cmd.Path)
	}
	selected := map[string]string{}
	for _, value := range cmd.Env {
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
}

func TestSessionCommandPassesPromptThroughEnvironment(t *testing.T) {
	cmd, err := sessionProcess(launcher.SessionHandoff{ProductID: "product-1", WorkID: "work-1", Prompt: "inspect the failing test"})
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

func TestSessionLauncherFailsClosedWithoutRunningBinaryIdentity(t *testing.T) {
	original := executablePath
	executablePath = func() (string, error) { return "", errors.New("unavailable") }
	defer func() { executablePath = original }()
	if cmd, err := sessionProcess(launcher.SessionHandoff{ProductID: "product-1"}); err == nil || cmd != nil {
		t.Fatalf("session process=%v err=%v", cmd, err)
	}
}

func TestS2DomainSectionRendersHierarchyRelationsAndOverlap(t *testing.T) {
	p := &coordinationPort{}
	core := launcher.New(p)
	if err := core.Enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := New(core, context.Background(), Profile{})
	m.UpdateKey("enter")
	rendered := m.Render()
	for _, want := range []string{"HOME product-root:one Product One law=0 active=0", "DOMAIN work-nav Work navigation parent=product-root:one law=0 active=0", "RELATION depends_on: work-nav -> product-root:one state=active", "OVERLAP work-1 & work-2 domains=work-nav resolution=absent"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("domains render missing %q: %q", want, rendered)
		}
	}
	if strings.Contains(rendered, "COMPONENT ") {
		t.Fatalf("retired component label still rendered: %q", rendered)
	}
	if !strings.Contains(rendered, "CLUSTER 1:") {
		t.Fatalf("work-relation cluster label missing after rename: %q", m.Render())
	}
}

func TestS2AnswerStackAdapterRendersPanelsInContractOrderAndKeepsSummariesStable(t *testing.T) {
	snapshot := launcher.Snapshot{
		Screen: launcher.ScreenProduct, AmbientProduct: "product-1", Coverage: "authoritative",
		PanelFocus: launcher.S2PanelDomain,
		Domains:    launcher.DomainSection{Read: true, State: "authoritative", Overlaps: []launcher.OverlapPair{{From: "w-a", To: "w-b", State: "absent", SharedDomains: []string{"d-law"}}}},
		Ranked:     []launcher.RankedWork{{ID: "w-store", Title: "Stored order", Blocked: true, Blockers: []launcher.Blocker{{ID: "b-store", Authority: "law"}}}, {ID: "w-second", Title: "Not first", Ready: true}},
	}
	core := launcher.New(nil)
	core.RestoreSnapshot(snapshot)
	m := New(core, context.Background(), Profile{})
	m.Sync()
	rendered := m.Render()
	for _, want := range []string{"OVERLAP w-a & w-b domains=d-law resolution=absent", "BLOCKED: w-store Stored order marker=!BLOCKED blockers=b-store[law]", "NEXT: !BLOCKED w-store Stored order"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("initial stack line missing %q: %q", want, rendered)
		}
	}
	if strings.Index(rendered, "DOMAIN:") > strings.Index(rendered, "BLOCKED:") || strings.Index(rendered, "BLOCKED:") > strings.Index(rendered, "NEXT:") {
		t.Fatalf("panel order changed: %q", rendered)
	}
	m.UpdateKey("tab")
	m.UpdateKey("tab")
	rendered = m.Render()
	for _, want := range []string{"DOMAIN: unresolved overlap: w-a & w-b domains=d-law resolution=absent", "BLOCKED: w-store Stored order marker=!BLOCKED blockers=b-store[law]", "1 !BLOCKED w-store Stored order"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("summary missing %q: %q", want, rendered)
		}
	}
}

func TestS2AnswerStackAdapterRedrawIsByteIdentical(t *testing.T) {
	core := launcher.New(nil)
	core.RestoreSnapshot(launcher.Snapshot{
		Screen: launcher.ScreenProduct, AmbientProduct: "product-1", Coverage: "authoritative",
		Domains: launcher.DomainSection{Read: true, State: "authoritative"},
		Ranked:  []launcher.RankedWork{{ID: "w-1", Title: "Next", Ready: true}},
	})
	m := New(core, context.Background(), Profile{})
	m.Sync()
	if first, second := m.Render(), m.Render(); first != second {
		t.Fatalf("unchanged S2 state rendered different bytes")
	}
}

func TestS2AnswerStackSummaryLinesStayWithin80ColumnsAndUnavailableDiffersFromClean(t *testing.T) {
	for _, state := range []launcher.DomainSection{
		{Read: true, State: "authoritative"},
		{Read: true, State: "unavailable", Reason: "registry unavailable"},
	} {
		core := launcher.New(nil)
		core.RestoreSnapshot(launcher.Snapshot{Screen: launcher.ScreenProduct, AmbientProduct: "product-1", Coverage: "authoritative", PanelFocus: launcher.S2PanelNext, Domains: state, Ranked: []launcher.RankedWork{{ID: "w-1", Title: strings.Repeat("x", 200), Ready: true}}})
		m := New(core, context.Background(), Profile{})
		m.Sync()
		rendered := m.Render()
		for _, line := range strings.Split(rendered, "\n") {
			if width := lipgloss.Width(line); width > 80 {
				t.Fatalf("line width=%d: %q", width, line)
			}
		}
		if state.State == "unavailable" && !strings.Contains(rendered, "DOMAIN: unavailable: registry unavailable") {
			t.Fatalf("typed unavailable reason missing: %q", rendered)
		}
		if state.State == "authoritative" && !strings.Contains(rendered, "DOMAIN: no unresolved overlaps") {
			t.Fatalf("evaluated-clean summary missing: %q", rendered)
		}
	}
}

func TestS2TabFocusAndS3TabSectionBehaviour(t *testing.T) {
	core := launcher.New(nil)
	core.RestoreSnapshot(launcher.Snapshot{Screen: launcher.ScreenProduct, Section: launcher.SectionDomains, Domains: launcher.DomainSection{Read: true, State: "authoritative"}})
	m := New(core, context.Background(), Profile{})
	m.Sync()
	for _, want := range []launcher.S2Panel{launcher.S2PanelBlocked, launcher.S2PanelNext, launcher.S2PanelDomain} {
		m.UpdateKey("tab")
		if got := core.PanelFocus(); got != want {
			t.Fatalf("S2 focus=%q, want %q", got, want)
		}
	}
	core.RestoreSnapshot(launcher.Snapshot{Screen: launcher.ScreenWork, Section: launcher.SectionRelations, Detail: launcher.WorkDetail{Knowledge: launcher.KnowledgeSection{Read: true}}})
	m.Sync()
	m.UpdateKey("tab")
	if got := core.Section(); got != launcher.SectionRanked {
		t.Fatalf("S3 Tab changed to %q, want next existing section", got)
	}
}

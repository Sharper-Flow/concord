package bubbletea

import (
	"context"
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
	m.OpenQuery()
	for _, msg := range []tea.KeyPressMsg{keyPress('a', "a", 0), keyPress('b', "b", 0), keyPress(tea.KeyLeft, "", 0), keyPress('X', "X", 0)} {
		m.Update(msg)
	}
	if got := m.QueryValue(); got != "aXb" {
		t.Fatalf("mid-string insertion=%q", got)
	}
	m.Update(keyPress(tea.KeyBackspace, "", 0))
	if got := m.QueryValue(); got != "ab" {
		t.Fatalf("backspace=%q", got)
	}
	m.Update(tea.PasteMsg{Content: " pasted"})
	if got := m.QueryValue(); got != "a pastedb" {
		t.Fatalf("bracketed paste=%q", got)
	}
	m.input.CursorStart()
	m.Update(keyPress(tea.KeyRight, "", 0))
	m.Update(keyPress(tea.KeyDelete, "", 0))
	if got := m.QueryValue(); got != "apastedb" {
		t.Fatalf("delete=%q", got)
	}
	m.Update(keyPress('l', "", tea.ModCtrl))
	if got := m.QueryValue(); got != "" {
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
	if cmd := m.OpenQuery(); cmd != nil {
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
	m.OpenQuery()
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
	m.UpdateKey("enter")
	if got := core.Snapshot(); got.Screen != launcher.ScreenProduct || got.StatusMessage != "not_implemented" {
		t.Fatalf("S2=%#v", got)
	}
	if p.reads != 3 {
		t.Fatalf("selection reads=%d", p.reads)
	}
	m.UpdateKey("esc")
	if core.Snapshot().Screen != launcher.ScreenPortfolio || p.reads != 3 {
		t.Fatalf("back screen=%s reads=%d", core.Snapshot().Screen, p.reads)
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
	case launcher.ReadProduct:
		return launcher.Snapshot{Screen: launcher.ScreenProduct, AmbientProduct: request.Product, Section: request.Section, Coverage: "authoritative", Ranked: []launcher.RankedWork{{ID: "work-1", Title: "First", Lifecycle: "needed", Priority: 1, Ready: true}, {ID: "work-2", Title: "Blocked", Lifecycle: "needed", Priority: 2, Blocked: true, Blockers: []launcher.Blocker{{ID: "blocker-1", Title: "External", Authority: "ci", Age: "old", External: true}}}}, Relations: launcher.RelationTree{Edges: []launcher.RelationEdge{{Kind: "parent", Source: "work-1", Target: "work-2"}}, Depth: 3}}, nil
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
	if core.Snapshot().Screen != launcher.ScreenProduct || p.reads != 2 {
		t.Fatalf("S2=%#v reads=%d", core.Snapshot(), p.reads)
	}
	m.UpdateKey("tab") // relation -> ranked
	m.UpdateKey("j")
	m.UpdateKey("enter")
	if core.Snapshot().Screen != launcher.ScreenWork || core.Handoff().WorkID != "work-2" || p.reads != 3 {
		t.Fatalf("S3=%#v reads=%d", core.Snapshot(), p.reads)
	}
	m.UpdateKey("esc")
	if core.Snapshot().Screen != launcher.ScreenProduct || m.Cursor() != 1 || core.Section() != launcher.SectionRanked {
		t.Fatalf("restored product=%#v cursor=%d", core.Snapshot(), m.Cursor())
	}
}

func TestKnowledgeIsLazyAndQuerySubmitsExactlyOnce(t *testing.T) {
	p := &coordinationPort{}
	core := launcher.New(p)
	if err := core.Enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := New(core, context.Background(), Profile{})
	m.UpdateKey("enter")
	reads := p.reads
	m.UpdateKey("tab")
	if p.reads != reads {
		t.Fatalf("relation to ranked read=%d", p.reads)
	}
	m.UpdateKey("tab")
	if p.reads != reads+1 || !core.Snapshot().Knowledge.Read {
		t.Fatalf("knowledge read=%d snapshot=%#v", p.reads, core.Snapshot())
	}
	m.UpdateKey("tab") // knowledge -> relations
	m.UpdateKey("tab") // relations -> ranked
	m.UpdateKey("s")
	m.Update(keyPress('b', "b", 0))
	if p.reads != reads+1 {
		t.Fatalf("query typing read=%d", p.reads)
	}
	m.UpdateKey("enter")
	if p.reads != reads+2 || len(p.requests) == 0 || p.requests[len(p.requests)-1].Kind != launcher.ReadSearch {
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
	m.UpdateKey("tab")
	m.UpdateKey("j")
	m.scroll = 1
	m.UpdateKey("s")
	m.Update(keyPress('b', "b", 0))
	m.UpdateKey("enter")
	if p.reads != 3 {
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
	m.UpdateKey("tab")
	m.UpdateKey("enter")
	m.UpdateKey("l")
	if called.ProductID != "product-1" || called.WorkID != "work-1" {
		t.Fatalf("S3 handoff=%#v", called)
	}
}

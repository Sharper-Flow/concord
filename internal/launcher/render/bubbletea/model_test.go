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
	if p.reads != 1 {
		t.Fatalf("submitted query reads=%d", p.reads)
	}
	m.Update(keyPress('r', "r", 0))
	if p.reads != 2 {
		t.Fatalf("explicit refresh reads=%d", p.reads)
	}
}

func TestSubmitCallsReadOnceAndNoTimerOrPolling(t *testing.T) {
	p := &port{state: launcher.Snapshot{Screen: launcher.ScreenPortfolio, Coverage: "authoritative"}}
	m := New(launcher.New(p), context.Background(), Profile{})
	m.OpenQuery()
	m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if p.reads != 1 {
		t.Fatalf("submit reads=%d", p.reads)
	}
}

package bubbletea

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/sharper-flow/concord/internal/launcher"
)

func TestFrameHasTerminalGeometryAndStableHeader(t *testing.T) {
	cases := []struct {
		name          string
		width, height int
		snapshot      launcher.Snapshot
		golden        string
	}{
		{
			name:  "80x24",
			width: 80, height: 24,
			snapshot: launcher.Snapshot{
				Screen: launcher.ScreenPortfolio, AmbientProduct: "Concord",
				Watermark: "w42", ObservedAt: "2m", Reliance: "clear", Coverage: "authoritative",
				Rows: fixturePortfolioRows(28),
			},
			golden: "frame-80x24.golden",
		},
		{
			name:  "120x40",
			width: 120, height: 40,
			snapshot: launcher.Snapshot{
				Screen: launcher.ScreenProduct, AmbientProduct: "Concord", Section: launcher.SectionRanked, PanelFocus: launcher.S2PanelNext,
				Coverage: "authoritative", Domains: launcher.DomainSection{
					Read: true, State: "authoritative", Domains: []launcher.DomainRow{{ID: "operator-surface", Name: "Operator surface", Home: true}},
				},
				Ranked: fixtureRankedWorks(45),
			},
			golden: "frame-120x40.golden",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			core := launcher.New(nil)
			core.RestoreSnapshot(tc.snapshot)
			model := New(core, context.Background(), Profile{})
			model.Update(tea.WindowSizeMsg{Width: tc.width, Height: tc.height})
			model.Sync()
			model.UpdateKey("G")
			if !model.View().AltScreen {
				t.Fatal("launcher view does not use the alternate screen")
			}
			frame := model.Render()

			lines := strings.Split(frame, "\n")
			if len(lines) != tc.height {
				t.Fatalf("frame line count=%d, want %d", len(lines), tc.height)
			}
			for i, line := range lines {
				if width := lipgloss.Width(line); width != tc.width {
					t.Fatalf("line %d width=%d, want %d: %q", i, width, tc.width, line)
				}
			}
			if got := strings.Count(frame, "CONCORD LAUNCHER"); got != 1 {
				t.Fatalf("header count=%d, want 1", got)
			}
			if !strings.Contains(frame, "╭") || !strings.Contains(frame, "╰") {
				t.Fatal("frame does not contain a rounded pane")
			}
			if tc.name == "80x24" && (!strings.Contains(frame, "Product") || !strings.Contains(frame, "Stage")) {
				t.Fatal("scrolled portfolio frame lost the column header")
			}
			if tc.name == "120x40" && !strings.Contains(frame, "S2 PRODUCT COORDINATION") {
				t.Fatal("scrolled S2 frame lost its section label")
			}

			path := filepath.Join("testdata", tc.golden)
			if os.Getenv("UPDATE_GOLDEN") == "1" {
				if err := os.WriteFile(path, []byte(frame+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(want) != frame+"\n" {
				t.Fatalf("frame differs from %s", path)
			}
		})
	}
}

func fixturePortfolioRows(count int) []launcher.ProductRow {
	rows := make([]launcher.ProductRow, count)
	for i := range rows {
		rows[i] = launcher.ProductRow{ID: "p-" + fmt.Sprint(i+1), Name: "Product " + fmt.Sprint(i+1), Stage: "in_progress", Reliance: "clear", Actions: 1, Focus: "Focus " + fmt.Sprint(i+1)}
	}
	return rows
}

// focusedDetailSnapshot carries one Product row whose twelve focus fields the
// core computes on every refresh, so the detail pane has typed data to render.
func focusedDetailSnapshot() launcher.Snapshot {
	return launcher.Snapshot{
		Screen: launcher.ScreenPortfolio, AmbientProduct: "Concord",
		Watermark: "w42", ObservedAt: "2m", Reliance: "clear", Coverage: "authoritative",
		Rows: []launcher.ProductRow{{
			ID: "p-1", Name: "Alpha", Stage: "in_progress", Reliance: "clear", Actions: 1,
			Focus:   "Ship the detail pane",
			FocusID: "work-42", FocusWorkKind: "task", FocusLifecycle: "in_progress",
			FocusAttentionKind: "approval_required", FocusBlockedSessionCount: 2,
			FocusOldestBlockedSession: "session-7", FocusPriority: 3,
			FocusWorkflowStepLabel: "execution", FocusProjectCount: 1,
			FocusStageContext: "build", FocusStageOverrideMaturity: "stable",
			FocusStageOverrideAudience: "operator",
		}},
	}
}

// TestFrameJoinsDetailPaneWhenWidthSeatsBothMinima proves the split
// geometry by measurement, not screenshots:
// check:launcher/frame-joins-two-panes joins a second pane at the frame,
// check:launcher/pane-widths-sum-to-frame holds the two pane widths equal to
// the frame width with a shared border and an unchanged frame height, and
// check:launcher/narrow-frame-stays-single-pane keeps one pane below the
// seated minima.
func TestFrameJoinsDetailPaneWhenWidthSeatsBothMinima(t *testing.T) {
	core := launcher.New(nil)
	core.RestoreSnapshot(focusedDetailSnapshot())
	model := New(core, context.Background(), Profile{})
	model.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	frame := model.Render()
	lines := strings.Split(frame, "\n")
	if len(lines) != 24 {
		t.Fatalf("split frame height=%d, want 24", len(lines))
	}
	if got := strings.Count(frame, "╭"); got != 2 {
		t.Fatalf("split frame pane count=%d, want 2: %q", got, frame)
	}
	boundary := primaryPaneWidth(120)
	if boundary != 120-detailPaneWidth {
		t.Fatalf("primary pane width=%d, want frame minus the declared detail budget", boundary)
	}
	top := []rune(lines[2])
	if top[boundary-1] != '╮' || top[boundary] != '╭' {
		t.Fatalf("top border is not split at column %d: %q", boundary, lines[2])
	}
	body := []rune(lines[4])
	if body[boundary-1] != '│' || body[boundary] != '│' {
		t.Fatalf("pane boundary at column %d is not a shared border: %q", boundary, lines[4])
	}
	detailWidth := lipgloss.Width(string(top[boundary:]))
	if boundary+detailWidth != 120 {
		t.Fatalf("pane widths %d + %d do not sum to the frame width 120", boundary, detailWidth)
	}
	for i, line := range lines {
		if got := lipgloss.Width(line); got != 120 {
			t.Fatalf("split frame line %d width=%d, want 120: %q", i, got, line)
		}
	}

	// Below the seated minima the frame stays single-pane and no detail
	// content leaks into it.
	model.Update(tea.WindowSizeMsg{Width: 79, Height: 24})
	narrow := model.Render()
	if got := strings.Count(narrow, "╭"); got != 1 {
		t.Fatalf("narrow frame pane count=%d, want 1: %q", got, narrow)
	}
	if strings.Contains(narrow, "DETAIL") {
		t.Fatalf("narrow frame leaked the detail pane: %q", narrow)
	}
}

// TestFrameSplitHoldsAtUnicodeDisplayWidths proves the split geometry at
// Unicode display widths: a wide-rune work title clips inside its pane by
// display width and neither the pane boundary nor the frame width moves.
func TestFrameSplitHoldsAtUnicodeDisplayWidths(t *testing.T) {
	snapshot := launcher.Snapshot{
		Screen: launcher.ScreenProduct, AmbientProduct: "product-1", Section: launcher.SectionRanked,
		PanelFocus: launcher.S2PanelBlocked, Coverage: "authoritative",
		Ranked: []launcher.RankedWork{
			{ID: "work-1", Kind: "task", Title: strings.Repeat("作業", 40), Lifecycle: "in_progress", Priority: 1, Ready: true},
			{ID: "work-2", Kind: "bug", Title: "plain", Lifecycle: "needed", Priority: 2},
		},
	}
	core := launcher.New(nil)
	core.RestoreSnapshot(snapshot)
	model := New(core, context.Background(), Profile{})
	model.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	frame := model.Render()
	lines := strings.Split(frame, "\n")
	if len(lines) != 24 {
		t.Fatalf("unicode frame height=%d, want 24", len(lines))
	}
	if got := strings.Count(frame, "╭"); got != 2 {
		t.Fatalf("unicode frame pane count=%d, want 2", got)
	}
	boundary := primaryPaneWidth(120)
	top := []rune(lines[2])
	if top[boundary-1] != '╮' || top[boundary] != '╭' {
		t.Fatalf("unicode top border is not split at column %d: %q", boundary, lines[2])
	}
	for i, line := range lines {
		if got := lipgloss.Width(line); got != 120 {
			t.Fatalf("unicode frame line %d width=%d, want 120: %q", i, got, line)
		}
	}
	if !strings.Contains(frame, "WORK: work-1") {
		t.Fatalf("unicode frame lost the selected work detail: %q", frame)
	}
}

func fixtureRankedWorks(count int) []launcher.RankedWork {
	rows := make([]launcher.RankedWork, count)
	for i := range rows {
		rows[i] = launcher.RankedWork{ID: "work-" + fmt.Sprint(i+1), Kind: "task", Title: "Work " + fmt.Sprint(i+1), Lifecycle: "in_progress", Ready: true}
	}
	return rows
}

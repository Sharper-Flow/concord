package bubbletea

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/sharper-flow/concord/internal/launcher"
)

func TestFrameHasTerminalGeometryAndSingleSurface(t *testing.T) {
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
				Screen: launcher.ScreenProduct, AmbientProduct: "Concord", Coverage: "authoritative",
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
			if got := strings.Count(frame, "CONCORD LAUNCHER"); got != 0 {
				t.Fatalf("title line count=%d, want 0", got)
			}
			if !strings.Contains(frame, "╭") || !strings.Contains(frame, "╰") {
				t.Fatal("frame does not contain a rounded pane")
			}
			if got := strings.Count(frame, "╭"); got != 1 {
				t.Fatalf("frame pane count=%d, want the single surface", got)
			}
			if tc.name == "80x24" && (!strings.Contains(frame, "Product") || !strings.Contains(frame, "Actions")) {
				t.Fatal("scrolled portfolio frame lost the Product and Actions column headers")
			}
			if tc.name == "120x40" && strings.Contains(frame, "S2 PRODUCT COORDINATION") {
				t.Fatal("scrolled work list rendered the retired S2 section label")
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

// focusedDetailSnapshot carries one Product row whose focus fields the core
// computes on every refresh, so the frame has typed portfolio data to render.
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

// TestFrameStaysSingleSurfaceAtEveryWidth proves the frame geometry by
// measurement: one bordered table spans the frame at every supported width,
// the title line and detail pane stay retired, and no line exceeds the
// terminal.
func TestFrameStaysSingleSurfaceAtEveryWidth(t *testing.T) {
	core := launcher.New(nil)
	core.RestoreSnapshot(focusedDetailSnapshot())
	for _, width := range []int{80, 100, 113, 114, 120, 200} {
		model := New(core, context.Background(), Profile{})
		model.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		frame := model.Render()
		lines := strings.Split(frame, "\n")
		if len(lines) != 24 {
			t.Fatalf("width %d: frame height=%d, want 24", width, len(lines))
		}
		if got := strings.Count(frame, "╭"); got != 1 {
			t.Fatalf("width %d: frame pane count=%d, want 1: %q", width, got, frame)
		}
		if strings.Contains(frame, "DETAIL") || strings.Contains(frame, "CONCORD LAUNCHER") {
			t.Fatalf("width %d: the retired title line or detail pane rendered: %q", width, frame)
		}
		for i, line := range lines {
			if got := lipgloss.Width(line); got != width {
				t.Fatalf("width %d: line %d width=%d, want %d", width, i, got, width)
			}
		}
		if !strings.Contains(frame, "FOCUS:") || !strings.Contains(frame, "COVERAGE:") {
			t.Fatalf("width %d: the status line lost its halves: %q", width, frame)
		}
	}
}

// TestFrameHoldsAtUnicodeDisplayWidths proves the frame geometry at Unicode
// display widths: a wide-rune work title clips inside its pane by display
// width and the frame width never moves.
func TestFrameHoldsAtUnicodeDisplayWidths(t *testing.T) {
	snapshot := launcher.Snapshot{
		Screen: launcher.ScreenProduct, AmbientProduct: "product-1", Coverage: "authoritative",
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
	if got := strings.Count(frame, "╭"); got != 1 {
		t.Fatalf("unicode frame pane count=%d, want 1", got)
	}
	for i, line := range lines {
		if got := lipgloss.Width(line); got != 120 {
			t.Fatalf("unicode frame line %d width=%d, want 120", i, got)
		}
	}
	if !strings.Contains(frame, "plain") {
		t.Fatalf("unicode frame lost its plain row: %q", frame)
	}
}

// TestColumnBudgetShedsAtCellAccurateThresholds drives the core's column
// budget with the measure this package renders with — lipgloss.Width, the
// library every table here prices with. Near the threshold a rune count
// answers differently: CJK and fullwidth forms occupy two cells per rune,
// while the family emoji occupies two cells for the whole sequence. A base
// character with a combining mark occupies one cell, so each
// fixture separates the cell measure from a rune count in at least one
// direction. The core carries no width logic of its own; this test is the
// near-threshold pin on the measurement the renderer hands it.
func TestColumnBudgetShedsAtCellAccurateThresholds(t *testing.T) {
	cases := []struct {
		name       string
		cell       string
		seat, shed int // widths where the two-column set seats vs sheds
		runeSeat   int // where a rune count would seat, for the contrast
	}{
		// seat = display cells + 7: the gutter, both widest cells, and the
		// inter-column padding of a two-column table.
		{"cjk", "界界界界", 15, 14, 11},
		{"fullwidth", "ＦＵＬＬＷＩＤＴＨ", 25, 24, 16},
		{"family emoji", "👨‍👩‍👧", 9, 8, 12},
		{"emoji", "👍👍👍", 13, 12, 10},
		{"combining marks", "e\u0301e\u0301e\u0301e\u0301", 11, 10, 15},
	}
	headers := []string{"A", "B"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows := [][]string{{tc.cell, "x"}}
			if got := launcher.ColumnBudget(headers, rows, tc.seat, lipgloss.Width); got != 2 {
				t.Fatalf("cell budget at seat width %d = %d, want 2", tc.seat, got)
			}
			if got := launcher.ColumnBudget(headers, rows, tc.shed, lipgloss.Width); got != 1 {
				t.Fatalf("cell budget at shed width %d = %d, want 1", tc.shed, got)
			}
			if got := launcher.ColumnBudget(headers, rows, tc.runeSeat, utf8.RuneCountInString); got != 2 {
				t.Fatalf("rune-count budget at %d = %d, want 2: the fixture no longer separates the measures", tc.runeSeat, got)
			}
		})
	}
}

// TestProjectShedsByTheRendererMeasure applies the renderer's measure
// through the Project entry point the model's Sync uses. The Product row
// "界界界界" spans 8 display cells (4 runes) and the Actions header 7, so
// the two-column set spans 21 cells: it seats at 21 and sheds the Actions
// column at 20.
func TestProjectShedsByTheRendererMeasure(t *testing.T) {
	core := launcher.New(nil)
	core.RestoreSnapshot(launcher.Snapshot{
		Screen: launcher.ScreenPortfolio, AmbientProduct: "Concord",
		Coverage: "authoritative",
		Rows: []launcher.ProductRow{{
			ID: "p-1", Name: "界界界界", Stage: "in_progress",
			Reliance: "clear", Actions: 1, Focus: "界界界界",
		}},
	})
	snapshot := core.Snapshot()
	// The Actions header prices 7 cells, so the two-column set spans
	// 2 + (8+2) + (7+2) = 21 cells.
	narrow := launcher.Project(snapshot, 20, lipgloss.Width)
	if len(narrow.Columns) != 1 || narrow.Columns[0] != "Product" {
		t.Fatalf("width-20 projection kept %d columns, want 1: %v", len(narrow.Columns), narrow.Columns)
	}
	wide := launcher.Project(snapshot, 21, lipgloss.Width)
	if len(wide.Columns) != 2 {
		t.Fatalf("width-21 projection dropped fitting columns: %v", wide.Columns)
	}
}

func fixtureRankedWorks(count int) []launcher.RankedWork {
	rows := make([]launcher.RankedWork, count)
	for i := range rows {
		// UpdatedAt stays unset so the golden's Updated cells are the stable
		// "-" and never age with the wall clock.
		rows[i] = launcher.RankedWork{ID: "work-" + fmt.Sprint(i+1), Kind: "task", Title: "Work " + fmt.Sprint(i+1), Lifecycle: "in_progress", Ready: true}
	}
	return rows
}

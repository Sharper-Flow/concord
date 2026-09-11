package bubbletea

import (
	"context"
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
				Rows: []launcher.ProductRow{{ID: "p-1", Name: "Launcher", Stage: "in_progress", Reliance: "clear", Actions: 3, Focus: "Fix frame"}},
			},
			golden: "frame-80x24.golden",
		},
		{
			name:  "120x40",
			width: 120, height: 40,
			snapshot: launcher.Snapshot{
				Screen: launcher.ScreenProduct, AmbientProduct: "Concord", Section: launcher.SectionDomains,
				Coverage: "authoritative", Domains: launcher.DomainSection{
					Read: true, State: "authoritative", Domains: []launcher.DomainRow{{ID: "operator-surface", Name: "Operator surface", Home: true}},
				},
				Ranked: []launcher.RankedWork{{ID: "work-1", Kind: "task", Title: "Frame launcher", Lifecycle: "in_progress", Ready: true}},
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

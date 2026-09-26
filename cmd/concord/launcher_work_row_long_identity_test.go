package main

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/sharper-flow/concord/internal/launcher"
	"github.com/sharper-flow/concord/internal/launcher/render/bubbletea"
	"github.com/sharper-flow/concord/internal/launcher/storeport"
	"github.com/sharper-flow/concord/internal/store"
)

// TestLauncherWorkRowRendersLongStoreIdentityInFull proves
// check:launcher.work_row_issue_key_and_occupancy across the real store read
// and the rendered Product work list: a store whose work item's confirmed
// Linear link carries the long real-store shape (a 42-character issue key)
// reads through the store port into the Product work list and renders the
// full linked key on the row line at 80, 120, and 200 columns, beside the
// readiness marker, the title, and the host-attested occupancy. No session
// launches: the only read is the port's, the occupancy probe is a stub, and
// the model renders from the snapshot.
func TestLauncherWorkRowRendersLongStoreIdentityInFull(t *testing.T) {
	const (
		longKey = "LONG-LINKED-ISSUE-KEY-405-PLATFORM-FIXTURE"
		longID  = "work-3f9c1b2a4d5e6f708192a3b4c5d6e7f8"
	)
	if len(longKey) != 42 || len(longID) != 37 {
		t.Fatalf("fixture drift: key %d chars, id %d chars, want the long real-store shapes 42/37", len(longKey), len(longID))
	}
	s := openLauncherCorpusStore(t)
	seedLauncherCorpusProduct(t, s, "scope-long", "Scope Long")
	seedLauncherCorpusWork(t, s, longID, "scope-long", "task", "Long identity work", "in_progress", 1, "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z")
	corpusExec(t, s, `INSERT INTO linear_issue_links(work_id,remote_issue_uuid,human_key,url,link_state,created_at,updated_at) VALUES (?,?,?,?,?,?,?)`,
		longID, "uuid-long-key", longKey, "https://linear.app/example/issue/long-key", "confirmed", "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z")
	corpusExec(t, s, `INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,git_facts) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		store.WorktreeSetID(longID), "scope-long-project", "claim-op-1", "work/long-identity", "0000000000000000000000000000000000000000", "/wt/long-identity", "repo-1", "active", "2026-08-01T00:00:00Z", "{}", "session-long")

	port := storeport.New(s)
	port.SessionProbe = func(context.Context, store.WorktreeEntry) bool { return true }
	snapshot, err := port.Read(context.Background(), launcher.ReadRequest{Kind: launcher.ReadProduct, Product: "scope-long", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Ranked) != 1 {
		t.Fatalf("Product read ranked %#v, want the one long-identity row", snapshot.Ranked)
	}
	read := snapshot.Ranked[0]
	if read.ID != longID || read.LinearIssueKey != longKey || read.LinearIssueURL == "" || read.Live != 1 {
		t.Fatalf("store row lost the long identity: %#v", read)
	}

	core := launcher.New(nil)
	core.RestoreSnapshot(snapshot)
	ui := bubbletea.New(core, context.Background(), bubbletea.Profile{})
	for _, width := range []int{80, 120, 200} {
		ui.Update(tea.WindowSizeMsg{Width: width, Height: 40})
		frame := ui.Render()
		// The row line is the only frame line that carries the full key.
		var rowLines []string
		for _, line := range strings.Split(frame, "\n") {
			if strings.Contains(line, longKey) {
				rowLines = append(rowLines, line)
			}
		}
		if len(rowLines) != 1 {
			t.Fatalf("width %d: the linked key renders on %d lines, want the one work row: %q", width, len(rowLines), frame)
		}
		rowLine := rowLines[0]
		for _, want := range []string{"~ACTIVE", "yes"} {
			if !strings.Contains(rowLine, want) {
				t.Fatalf("width %d: store row line lost %q: %q", width, want, rowLine)
			}
		}
		// The mandated cells lead: at 80 the title yields to the full key,
		// and at the wider widths the full title seats beside it.
		if width == 80 {
			if !strings.Contains(rowLine, "…") || strings.Contains(rowLine, "Long identity work") {
				t.Fatalf("width %d: the title did not yield to the full linked key: %q", width, rowLine)
			}
		} else if !strings.Contains(rowLine, "Long identity work") {
			t.Fatalf("width %d: store row line lost its title: %q", width, rowLine)
		}
	}
}

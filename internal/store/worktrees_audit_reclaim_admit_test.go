package store

import (
	"context"
	"testing"
	"time"
)

// The pass's Admit bound runs inside the candidate row's reclaim transaction.
// A declined row rolls back with no reclaim event and no native removal, and
// it is reported unattempted in report_only with every later attempt.
func TestWorktreeAuditReclaimDeclinedRowRollsBack(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	ctx := context.Background()
	paths := map[string]string{}
	for _, workID := range []string{"work-a", "work-b", "work-c"} {
		paths[workID] = auditWork(t, s, git, workID, true)
		completeAuditWork(t, s, workID)
	}
	var calls int
	held := true
	result, err := s.WorktreeAuditReclaim(ctx, WorktreeAuditReclaimRequest{
		ProductID: "product-w", DefaultRef: "origin/main", PrincipalRef: "principal-1", RequestID: "audit-admit", Now: time.Unix(40, 0).UTC(), Runner: git, Limit: 100,
		Admit: func(root string, rows []WorktreeAuditReclaimRow, remaining []WorktreeDrift) bool {
			calls++
			// The row transaction holds the pool's one connection.
			held = held && s.db.Stats().InUse == 1
			if root == "" || len(rows) != calls || len(remaining) != 3-calls {
				t.Errorf("admit call %d: root=%q rows=%d remaining=%d", calls, root, len(rows), len(remaining))
			}
			if last := rows[len(rows)-1]; last.Outcome != WorktreeAuditReclaimed || last.Version == 0 {
				t.Errorf("admit candidate=%+v, want its reclaimed post-state", last)
			}
			return calls < 2
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("admit calls=%d, want the pass to stop at the first decline", calls)
	}
	if !held {
		t.Fatal("admit ran without the row transaction holding the connection")
	}
	if len(result.Rows) != 1 || result.Rows[0].Outcome != WorktreeAuditReclaimed {
		t.Fatalf("rows=%+v, want the one admitted reclaim", result.Rows)
	}
	if _, still := git.worktrees[paths[result.Rows[0].WorkID]]; still {
		t.Fatal("admitted row's native worktree was not removed")
	}
	if len(result.ReportOnly) != 2 {
		t.Fatalf("report_only=%+v, want the declined row and the unattempted one", result.ReportOnly)
	}
	for _, drift := range result.ReportOnly {
		if drift.Class != WorktreeDriftTerminalPresent {
			t.Fatalf("declined drift=%+v, want terminal_present", drift)
		}
		if _, kept := git.worktrees[paths[drift.WorkID]]; !kept {
			t.Fatalf("declined %s lost its native worktree", drift.WorkID)
		}
		if _, kept := git.branches["work/"+drift.WorkID]; !kept {
			t.Fatalf("declined %s lost its branch", drift.WorkID)
		}
		var reclaims int
		if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE kind='work.worktree_reclaimed' AND subject_id=?`, drift.WorkID).Scan(&reclaims); err != nil {
			t.Fatal(err)
		}
		if reclaims != 0 {
			t.Fatalf("declined %s recorded %d reclaim events", drift.WorkID, reclaims)
		}
		var state string
		if err := s.db.QueryRow(`SELECT state FROM worktree_entries WHERE set_id=? AND project_id='project-w'`, worktreeSetPrefix+drift.WorkID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != "active" {
			t.Fatalf("declined %s entry state=%q, want active", drift.WorkID, state)
		}
	}
	// A later pass reclaims the declined rows.
	again, err := s.WorktreeAuditReclaim(ctx, WorktreeAuditReclaimRequest{ProductID: "product-w", DefaultRef: "origin/main", PrincipalRef: "principal-1", RequestID: "audit-admit-2", Now: time.Unix(50, 0).UTC(), Runner: git, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Rows) != 2 || again.Rows[0].Outcome != WorktreeAuditReclaimed || again.Rows[1].Outcome != WorktreeAuditReclaimed {
		t.Fatalf("second pass rows=%+v, want both declined rows reclaimed", again.Rows)
	}
}

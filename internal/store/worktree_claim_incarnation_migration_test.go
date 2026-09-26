package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// A claim a bootstrap reopen revived before the incarnation column existed
// carries one suffixed work.worktree_created event per reopen. The migration
// must count those events, or the claim's next release and reclaim re-derive
// the first incarnation's event ids and refuse as duplicate_event forever.
func TestMigrateV99ToV100BackfillsReopenedClaimIncarnation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const before = 99
	if migrations[before].Version != before+1 || migrations[before].Name != "worktree_claims_carry_incarnation" {
		t.Fatalf("migration %d is %q, want worktree_claims_carry_incarnation", migrations[before].Version, migrations[before].Name)
	}
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "concord-v99.db"), before)

	sha := strings.Repeat("a", 40)
	claims := []struct{ op, work, state string }{
		{"bootstrap-once", "work-once", "verified"},
		{"bootstrap-reopened", "work-reopened", "verified"},
		{"bootstrap-twice", "work-twice", "reclaimed"},
		{"bootstrap-lookalike", "work-lookalike", "verified"},
	}
	for _, c := range claims {
		if _, err := db.ExecContext(ctx, `INSERT INTO worktree_claims(op_id,work_id,project_id,set_id,pinned_branch,pinned_base_sha,pinned_path,state,principal_ref,request_id,observed_at,updated_at) VALUES(?,?,'p',?,?,?,?,?,'operator','r','t','t')`,
			c.op, c.work, "wts:"+c.work, "work/"+c.work, sha, "/worktrees/"+c.work, c.state); err != nil {
			t.Fatal(err)
		}
	}
	events := []struct{ id, kind, subject string }{
		{"bootstrap-once:worktree-created", "work.worktree_created", "work-once"},
		{"bootstrap-reopened:worktree-created", "work.worktree_created", "work-reopened"},
		{"bootstrap-reopened:worktree-created:8", "work.worktree_created", "work-reopened"},
		{"bootstrap-twice:worktree-created", "work.worktree_created", "work-twice"},
		{"bootstrap-twice:worktree-created:5", "work.worktree_created", "work-twice"},
		{"bootstrap-twice:worktree-created:9", "work.worktree_created", "work-twice"},
		// Another claim's op id that starts with this one's must not count.
		{"bootstrap-lookalike-2:worktree-created:3", "work.worktree_created", "work-other"},
		// A different kind under the same prefix must not count.
		{"bootstrap-lookalike:worktree-created:4", "work.note_recorded", "work-lookalike"},
	}
	for _, e := range events {
		if _, err := db.ExecContext(ctx, `INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,'work_item',?,'operator','t',1,'{}')`, e.id, e.kind, e.subject); err != nil {
			t.Fatal(err)
		}
	}

	if err := applyMigration(ctx, db, migrations[before]); err != nil {
		t.Fatal(err)
	}

	want := map[string]int{"bootstrap-once": 0, "bootstrap-reopened": 1, "bootstrap-twice": 2, "bootstrap-lookalike": 0}
	for op, incarnation := range want {
		var got int
		if err := db.QueryRowContext(ctx, `SELECT incarnation FROM worktree_claims WHERE op_id=?`, op).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != incarnation {
			t.Errorf("%s incarnation = %d, want %d", op, got, incarnation)
		}
	}
	if got := occupancyReleasedEventID("work-reopened", "p", "bootstrap-reopened", "ses-test", 1); got != "work-reopened:p:bootstrap-reopened:worktree-occupancy-released:ses-test:i1" {
		t.Errorf("backfilled claim release id = %q", got)
	}
}

package storeport

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/sharper-flow/concord/internal/launcher"
	"github.com/sharper-flow/concord/internal/store"
)

// seedLauncherStoreFixture seeds two Products with Projects, works in both,
// one terminal work, one confirmed Linear link, one intent external_ref
// fallback, and one active occupied worktree entry. The fold guard is active
// only while the fold-only fixture rows are inserted.
func seedLauncherStoreFixture(t *testing.T, s *store.Store) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := s.DatabaseForTesting().ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
			t.Errorf("remove fold guard: %v", err)
		}
	}()
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `
		INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES
		('scope-a','Scope A','prototype','operator_only',1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z'),
		('scope-b','Scope B','prototype','operator_only',1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z');
		INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES
		('proj-a1','Pathed project',1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z'),
		('proj-a2','Pathless project',1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z'),
		('proj-b','Other product project',1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z');
		INSERT INTO product_projects(product_id,project_id,role) VALUES
		('scope-a','proj-a1','primary'),('scope-a','proj-a2','secondary'),('scope-b','proj-b','primary');
		INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,terminal_time) VALUES
		('scope-live','task','Linked live work','in_progress',1,1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z',NULL),
		('scope-done','bug','Finished work','completed',1,1,'2026-08-01T00:00:00Z','2026-08-04T00:00:00Z','2026-08-04T00:00:00Z'),
		('scope-ref','task','Externally referenced work','needed',2,1,'2026-08-02T00:00:00Z','2026-08-02T00:00:00Z',NULL),
		('other-live','task','Other product work','needed',1,1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z',NULL);
		UPDATE work_items SET intent_json='{"external_ref":"GH-42"}' WHERE id='scope-ref';
		INSERT INTO work_projects(work_id,project_id,role) VALUES
		('scope-live','proj-a1','primary'),('scope-done','proj-a1','primary'),
		('scope-ref','proj-a1','primary'),('other-live','proj-b','primary');
		INSERT INTO linear_issue_links(work_id,remote_issue_uuid,human_key,url,link_state,created_at,updated_at) VALUES
		('scope-live','uuid-con-153','CON-153','https://linear.app/example/issue/CON-153','confirmed','2026-08-01T00:00:00Z','2026-08-01T00:00:00Z');
		INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,git_facts) VALUES
		('`+store.WorktreeSetID("scope-live")+`','proj-a1','claim-op-1','work/scope-live','0000000000000000000000000000000000000000','/wt/scope-live','repo-1','active','2026-08-01T00:00:00Z','{}');
		INSERT INTO worktree_occupancy(worktree_id,session_ref,recorded_at,host_pid,host_pid_start,has_process_identity) VALUES
		('`+store.WorktreeSetID("scope-live")+`:proj-a1:claim-op-1','session-1','2026-08-01T00:00:00Z',NULL,NULL,0);
		INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES
		('loc-a1','proj-a1','canonical_path','/src/proj-a1','/src/proj-a1','2026-08-01T00:00:00Z','2026-08-01T00:00:00Z');
	`); err != nil {
		t.Fatal(err)
	}
}

// TestProductReadScopesWorkToProductAndActiveLifeCycle proves
// check:launcher.work_list_product_scoped_active at the store boundary: the
// Product screen lists only that Product's work, active items in the pick
// segment, terminal items in the history tail, and never another Product's
// work. The snapshot sets ActiveWorkOnly so the picker filters the tail.
func TestProductReadScopesWorkToProductAndActiveLifeCycle(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(context.Background(), filepath.Join(dir, "launcher.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedLauncherStoreFixture(t, s)
	port := New(s)
	snapshot, err := port.Read(context.Background(), launcher.ReadRequest{Kind: launcher.ReadProduct, Product: "scope-a", Limit: 100, Section: launcher.SectionRanked})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]launcher.RankedWork{}
	for _, row := range snapshot.Ranked {
		ids[row.ID] = row
	}
	if _, foreign := ids["other-live"]; foreign {
		t.Fatalf("Product screen leaked another Product's work: %#v", snapshot.Ranked)
	}
	live, ok := ids["scope-live"]
	if !ok || live.Terminal || live.Lifecycle != "in_progress" {
		t.Fatalf("active scoped item = %#v (rows %#v)", live, snapshot.Ranked)
	}
	done, ok := ids["scope-done"]
	if !ok || !done.Terminal || done.Lifecycle != "completed" {
		t.Fatalf("terminal tail item = %#v", done)
	}
	if !snapshot.ActiveWorkOnly {
		t.Fatal("Product snapshot must set ActiveWorkOnly so the picker drops terminal rows")
	}
}

// TestProductReadCarriesIssueKeyWorktreeAndLiveOccupancy proves
// check:launcher.work_row_issue_key_and_occupancy at the store boundary: a
// confirmed Linear link resolves to the row's issue key, a linked-but-unconfirmed
// external reference falls back to the intent ref, and the occupancy join fills
// the worktree path and Live only when the host probe attests a live session.
func TestProductReadCarriesIssueKeyWorktreeAndLiveOccupancy(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(context.Background(), filepath.Join(dir, "launcher.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedLauncherStoreFixture(t, s)
	port := New(s)
	port.SessionProbe = func(_ context.Context, entry store.WorktreeEntry) bool {
		return entry.State == "active" && entry.Path != ""
	}
	snapshot, err := port.Read(context.Background(), launcher.ReadRequest{Kind: launcher.ReadProduct, Product: "scope-a", Limit: 100, Section: launcher.SectionRanked})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]launcher.RankedWork{}
	for _, row := range snapshot.Ranked {
		byID[row.ID] = row
	}
	live := byID["scope-live"]
	if live.LinearIssueKey != "CON-153" || live.Worktree != "/wt/scope-live" || live.Live != 1 {
		t.Fatalf("linked occupied row = %#v", live)
	}
	ref := byID["scope-ref"]
	if ref.LinearIssueKey != "GH-42" || ref.Worktree != "" || ref.Live != 0 {
		t.Fatalf("external-ref row = %#v", ref)
	}
}

// TestProjectsOmitProjectsWithoutRecordedPath proves the degrade path of
// check:launcher.new_backlog_resolves_issue_or_project: the Project select
// lists only Projects whose repository path a prior bootstrap or locator
// recorded, and omits a pathless Project rather than offering a launch that
// can only fail.
func TestProjectsOmitProjectsWithoutRecordedPath(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(context.Background(), filepath.Join(dir, "launcher.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedLauncherStoreFixture(t, s)
	port := New(s)
	projects, err := port.Projects(context.Background(), "scope-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 {
		t.Fatalf("projects = %#v, want only the pathed Project", projects)
	}
	if projects[0].ID != "proj-a1" || projects[0].Path != "/src/proj-a1" {
		t.Fatalf("pathed project = %#v", projects[0])
	}
	empty, err := port.Projects(context.Background(), "scope-b")
	if err != nil || len(empty) != 0 {
		t.Fatalf("pathless product projects = %#v err=%v, want empty", empty, err)
	}
}

// TestLauncherPortReadsPerformNoDurableWrite proves
// check:launcher.store_write_free inside internal/launcher: driving every
// launcher read, the Project select, and issue resolution against a seeded
// store leaves the durable operation tables untouched. CD-0108 D4 keeps work
// capture inside the launched session.
func TestLauncherPortReadsPerformNoDurableWrite(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(context.Background(), filepath.Join(dir, "launcher.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedLauncherStoreFixture(t, s)
	counts := func() map[string]int {
		t.Helper()
		out := map[string]int{}
		for _, table := range []string{"domain_events", "agent_approvals", "agent_approval_challenges", "idempotency_records", "durable_operations"} {
			var count int
			if err := s.DatabaseForTesting().QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
				t.Fatalf("count %s: %v", table, err)
			}
			out[table] = count
		}
		return out
	}
	before := counts()
	port := New(s)
	port.SessionProbe = func(context.Context, store.WorktreeEntry) bool { return false }
	ctx := context.Background()
	read := func(kind launcher.ReadKind, product, work string, section launcher.Section) {
		t.Helper()
		if _, err := port.Read(ctx, launcher.ReadRequest{Kind: kind, Product: product, Work: work, Limit: 20, Section: section}); err != nil {
			t.Fatalf("read %s must stay readable even when degraded: %v", kind, err)
		}
	}
	degradedRead := func(kind launcher.ReadKind, product, work string, section launcher.Section) {
		t.Helper()
		// A typed unavailable section is a rendered state, not a write.
		_, _ = port.Read(ctx, launcher.ReadRequest{Kind: kind, Product: product, Work: work, Limit: 20, Section: section})
	}
	read(launcher.ReadPortfolio, "", "", "")
	read(launcher.ReadProduct, "scope-a", "", launcher.SectionRanked)
	read(launcher.ReadDomains, "scope-a", "", launcher.SectionDomains)
	read(launcher.ReadWork, "scope-a", "scope-live", "")
	degradedRead(launcher.ReadKnowledge, "scope-a", "", launcher.SectionKnowledge)
	if _, err := port.Candidates(ctx, 10); err != nil {
		t.Fatalf("candidates: %v", err)
	}
	if _, err := port.Projects(ctx, "scope-a"); err != nil {
		t.Fatalf("projects: %v", err)
	}
	if _, err := port.ResolveIssue(ctx, "CON-153", ""); err != nil {
		t.Fatalf("resolve linked issue: %v", err)
	}
	if _, err := port.ResolveIssue(ctx, "CON-999", ""); err == nil {
		t.Fatal("unlinked issue must refuse, not fabricate a target")
	}
	if after := counts(); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatalf("launcher reads changed durable state: before=%v after=%v", before, after)
	}
}

package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func seedClaimOrigin(t *testing.T, repoRoot string) {
	t.Helper()
	origin := filepath.Join(filepath.Dir(repoRoot), filepath.Base(repoRoot)+"-origin.git")
	gitRun(t, filepath.Dir(repoRoot), "init", "-q", "--bare", "-b", "main", origin)
	gitRun(t, repoRoot, "remote", "add", "origin", origin)
	gitRun(t, repoRoot, "push", "-q", "origin", "main")
	gitRun(t, repoRoot, "fetch", "-q", "origin")
	gitRun(t, repoRoot, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
}

func worktreeDispatchFixture(t *testing.T) (*store.Store, *Service, Authority, string, string) {
	t.Helper()
	ctx := context.Background()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	events := append(agentFixtureProductEvents("wt-dispatch", "product-1", "project-1", "WT Dispatch", "WT Project", "worktree dispatch fixture"),
		store.Event{EventID: "wt-dispatch-work", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 4, Payload: json.RawMessage(`{"work_kind":"task","title":"Worktree Dispatch","priority":1}`)},
		store.Event{EventID: "wt-dispatch-work-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	)
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0, store.VersionRef(store.SubjectWorkItem, "work-1"): 0}}); err != nil {
		t.Fatal(err)
	}
	repoRoot := t.TempDir()
	seedFixtureRepo(t, repoRoot, "# fixture\n", "fixture base")
	baseSHA := gitRun(t, repoRoot, "rev-parse", "HEAD")
	if err := s.AddProjectLocator(ctx, "project-1", store.ProjectLocator{ID: "path-1", Kind: store.LocatorCanonicalPath, Value: repoRoot}, 1); err != nil {
		t.Fatal(err)
	}
	service, _, grant := newAuthorizedService(t, s, "client-1", "human-1", []agentcontract.Capability{"work_transition", "product_read"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	return s, service, grant, repoRoot, baseSHA
}

func seedSecondProjectFixture(t *testing.T, s *store.Store) string {
	t.Helper()
	ctx := context.Background()
	events := []store.Event{
		{EventID: "wt-scope-product-2", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"WT Scope Two","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "wt-scope-project-2", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"display_name":"WT Project Two"}`)},
		{EventID: "wt-scope-membership-2", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-2","project_id":"project-2","role":"primary","reason":"cross scope fixture","expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-2"): 0, store.VersionRef(store.SubjectProject, "project-2"): 0}}); err != nil {
		t.Fatal(err)
	}
	repoRoot := t.TempDir()
	seedFixtureRepo(t, repoRoot, "# fixture two\n", "fixture base two")
	if err := s.AddProjectLocator(ctx, "project-2", store.ProjectLocator{ID: "path-2", Kind: store.LocatorCanonicalPath, Value: repoRoot}, 1); err != nil {
		t.Fatal(err)
	}
	return gitRun(t, repoRoot, "rev-parse", "HEAD")
}

func seedSiblingProjectFixture(t *testing.T, s *store.Store) string {
	t.Helper()
	ctx := context.Background()
	events := []store.Event{
		{EventID: "wt-sibling-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1b", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"display_name":"WT Project One B"}`)},
		{EventID: "wt-sibling-membership", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-1b","role":"secondary","reason":"sibling project fixture","expected_version":2,"resulting_version":3}`)},
		{EventID: "wt-sibling-work-memberships", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"},{"project_id":"project-1b","role":"secondary"}],"expected_version":2,"resulting_version":3}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 2, store.VersionRef(store.SubjectProject, "project-1b"): 0, store.VersionRef(store.SubjectWorkItem, "work-1"): 2}}); err != nil {
		t.Fatal(err)
	}
	repoRoot := t.TempDir()
	seedFixtureRepo(t, repoRoot, "# fixture sibling\n", "fixture base sibling")
	if err := s.AddProjectLocator(ctx, "project-1b", store.ProjectLocator{ID: "path-1b", Kind: store.LocatorCanonicalPath, Value: repoRoot}, 1); err != nil {
		t.Fatal(err)
	}
	return gitRun(t, repoRoot, "rev-parse", "HEAD")
}

func seedFixtureRepo(t *testing.T, dir, readme, message string) {
	t.Helper()
	gitRun(t, dir, "init", "-b", "main")
	gitRun(t, dir, "config", "user.email", "concord@example.invalid")
	gitRun(t, dir, "config", "user.name", "Concord Worktree Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(readme), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "README.md")
	gitRun(t, dir, "commit", "-m", message)
	seedClaimOrigin(t, dir)
}

func seedWorkTransition(t *testing.T, s *store.Store, workID, from, to string, expected int64) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"from": from, "to": to, "reason": "fixture transition to " + to, "evidence_refs": []string{"fixture-evidence"}, "expected_version": expected, "resulting_version": expected + 1})
	event := store.Event{EventID: workID + "-" + to + "-transition", Kind: "work.transitioned", SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: payload}
	if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{event}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, workID): expected}}); err != nil {
		t.Fatal(err)
	}
}

func seedHostSessionPlacement(t *testing.T, s *store.Store, workID, sessionRef, path string) {
	t.Helper()
	execProjection(t, s, []string{
		`DELETE FROM worktree_occupancy WHERE session_ref=?`,
		`INSERT INTO worktree_occupancy(worktree_id,session_ref,recorded_at,has_process_identity) SELECT set_id||':'||project_id||':'||claim_op_id,?,'2026-10-01T00:00:00Z',0 FROM worktree_entries WHERE path=? AND state='active'`,
		`INSERT OR IGNORE INTO session_claim_landings(work_id,session_ref,project_id,claim_op_id) SELECT ?,?,project_id,claim_op_id FROM worktree_entries WHERE path=? AND state='active'`,
	}, [][]any{{sessionRef}, {sessionRef, path}, {workID, sessionRef, path}})
}

// Takeover still consumes departure facts retained by upgraded stores.
func seedRecordedHolderDeparture(t *testing.T, s *store.Store, sessionRef, source string) {
	t.Helper()
	execProjection(t, s, []string{
		`INSERT INTO session_vacate_requests(work_id,session_ref,project_id,source_directory,destination_directory,request_seq,landed) SELECT 'work-1',?,'project-1',?,'/repo',last+1,0 FROM store_sequences WHERE name='effect'`,
		`UPDATE store_sequences SET last=last+1 WHERE name='effect'`,
	}, [][]any{{sessionRef, source}, nil})
}

// Native verification and Review still read entries from upgraded stores.
// The fixture creates its checkout with Git and seeds only that read model.
func seedHostWorktree(t *testing.T, s *store.Store, repoRoot, workID, projectID, baseSHA string) string {
	t.Helper()
	path := filepath.Join(filepath.Dir(s.Path()), "worktrees", projectID, workID)
	branch := "work/" + workID
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repoRoot, "worktree", "add", "-b", branch, path, baseSHA)
	stamp := fixedTime().UTC().Format(time.RFC3339Nano)
	op := "fixture:" + projectID + ":" + workID
	execProjection(t, s, []string{
		`INSERT INTO worktree_claims(op_id,work_id,project_id,set_id,pinned_branch,pinned_base_sha,pinned_path,state,principal_ref,request_id,observed_at,updated_at,repository_id) VALUES(?,?,?,?,?,?,?,'verified','principal/fixture',?,?,?,?)`,
		`INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at) VALUES(?,?,?,?,?,?,?,'active',?)`,
		`UPDATE work_items SET version=version+1 WHERE id=?`,
	}, [][]any{
		{op, workID, projectID, store.WorktreeSetID(workID), branch, baseSHA, path, op, stamp, stamp, repoRoot},
		{store.WorktreeSetID(workID), projectID, op, branch, baseSHA, path, repoRoot, stamp},
		{workID},
	})
	return path
}

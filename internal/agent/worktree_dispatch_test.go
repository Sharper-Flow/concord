package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

// A real git repository exercises the actual ExecGitRunner seam: native
// worktree add, verification probes, and reclamation all run against git
// itself, not a stub.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func worktreeDispatchFixture(t *testing.T) (*store.Store, *Service, Grant, string, string) {
	t.Helper()
	ctx := context.Background()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	events := []store.Event{
		{EventID: "wt-dispatch-product", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"WT Dispatch","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "wt-dispatch-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"WT Project"}`)},
		{EventID: "wt-dispatch-membership", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"worktree dispatch fixture","expected_version":1,"resulting_version":2}`)},
		{EventID: "wt-dispatch-work", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"Worktree Dispatch","priority":1}`)},
		{EventID: "wt-dispatch-work-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0, store.VersionRef(store.SubjectWorkItem, "work-1"): 0}}); err != nil {
		t.Fatal(err)
	}

	repoRoot := t.TempDir()
	gitRun(t, repoRoot, "init", "-b", "main")
	gitRun(t, repoRoot, "config", "user.email", "concord@example.invalid")
	gitRun(t, repoRoot, "config", "user.name", "Concord Worktree Test")
	if err := os.WriteFile(filepath.Join(repoRoot, "README.md"), []byte("# fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repoRoot, "add", "README.md")
	gitRun(t, repoRoot, "commit", "-m", "fixture base")
	baseSHA := gitRun(t, repoRoot, "rev-parse", "HEAD")

	if err := s.AddProjectLocator(ctx, "project-1", store.ProjectLocator{ID: "path-1", Kind: store.LocatorCanonicalPath, Value: repoRoot}, 1); err != nil {
		t.Fatal(err)
	}

	service, _, grant := newAuthorizedService(t, s, "client-1", "human-1", []Capability{"work_transition"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	return s, service, grant, repoRoot, baseSHA
}

func TestWorktreeClaimAndReclaimThroughToolSurface(t *testing.T) {
	ctx := context.Background()
	s, service, grant, repoRoot, baseSHA := worktreeDispatchFixture(t)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	worktreePath := filepath.Join(t.TempDir(), "linked-wt")

	claimInput, _ := json.Marshal(map[string]any{
		"work_id": "work-1", "project_id": "project-1",
		"branch": "work/dispatch-1", "base_sha": baseSHA, "path": worktreePath,
		"expected_version": 2, "idempotency_key": "wt-claim-1",
	})
	request := InvokeRequest{Tool: "concord_work_transition", Operation: "worktree_claim", Input: claimInput}
	response, err := Dispatch(ctx, s, service, request, mutationEnvelope(grant, scopeVersion))
	if err != nil || response.Outcome != OutcomeOK {
		t.Fatalf("claim response=%+v err=%v", response, err)
	}
	entries, err := s.WorktreeEntries(ctx, "work-1")
	if err != nil || len(entries) != 1 || entries[0].State != "active" || entries[0].Branch != "work/dispatch-1" {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
	listing := gitRun(t, repoRoot, "worktree", "list", "--porcelain")
	if !strings.Contains(listing, "linked-wt") {
		t.Fatalf("native worktree missing:\n%s", listing)
	}

	// The same claim retried is an idempotent replay, not a second worktree.
	replay, err := Dispatch(ctx, s, service, request, mutationEnvelope(grant, scopeVersion))
	if err != nil || replay.Outcome != OutcomeOK {
		t.Fatalf("replay response=%+v err=%v", replay, err)
	}
	if got := strings.Count(gitRun(t, repoRoot, "worktree", "list"), "linked-wt"); got != 1 {
		t.Fatalf("expected one linked worktree line, got %d", got)
	}

	// Dirty tree is refused; clean tree reclaims.
	if err := os.WriteFile(filepath.Join(worktreePath, "README.md"), []byte("# dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reclaimInput, _ := json.Marshal(map[string]any{
		"work_id": "work-1", "project_id": "project-1",
		"default_ref":      "main",
		"expected_version": 3, "idempotency_key": "wt-reclaim-1",
	})
	reclaim := InvokeRequest{Tool: "concord_work_transition", Operation: "worktree_reclaim", Input: reclaimInput}
	dirty, err := Dispatch(ctx, s, service, reclaim, mutationEnvelope(grant, scopeVersion))
	if err != nil || dirty.Outcome == OutcomeOK {
		t.Fatalf("dirty reclaim must fail: response=%+v err=%v", dirty, err)
	}
	if err := os.WriteFile(filepath.Join(worktreePath, "README.md"), []byte("# fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, worktreePath, "checkout", "--", ".")

	clean, err := Dispatch(ctx, s, service, reclaim, mutationEnvelope(grant, scopeVersion))
	if err != nil || clean.Outcome != OutcomeOK {
		t.Fatalf("clean reclaim response=%+v err=%v", clean, err)
	}
	entries, err = s.WorktreeEntries(ctx, "work-1")
	if err != nil || len(entries) != 1 || entries[0].State != "reclaimed" {
		t.Fatalf("entries after reclaim=%+v err=%v", entries, err)
	}
	if strings.Contains(gitRun(t, repoRoot, "worktree", "list"), "linked-wt") {
		t.Fatal("native worktree still present after reclaim")
	}
}

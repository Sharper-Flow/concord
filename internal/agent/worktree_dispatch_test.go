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

func worktreeDispatchFixture(t *testing.T) (*store.Store, *Service, Authority, string, string) {
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

	service, _, grant := newAuthorizedService(t, s, "client-1", "human-1", []Capability{"work_transition", "product_read"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
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

// seedWorkTransition appends one work.transitioned event so a fixture work
// item reaches an exact lifecycle without driving the approval flow.
func seedWorkTransition(t *testing.T, s *store.Store, workID, from, to string, expected int64) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"from": from, "to": to, "reason": "fixture transition to " + to, "evidence_refs": []string{"fixture-evidence"}, "expected_version": expected, "resulting_version": expected + 1})
	event := store.Event{EventID: workID + "-" + to + "-transition", Kind: "work.transitioned", SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: payload}
	if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{event}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, workID): expected}}); err != nil {
		t.Fatal(err)
	}
}

// claimLinkedWorktree claims a worktree for work-1 through the tool surface
// from a linked-worktree resolution.
func claimLinkedWorktree(t *testing.T, s *store.Store, service *Service, grant Authority, worktreePath, baseSHA, branch, key string) {
	t.Helper()
	claimInput, _ := json.Marshal(map[string]any{
		"work_id": "work-1", "project_id": "project-1",
		"branch": branch, "base_sha": baseSHA, "path": worktreePath,
		"expected_version": 2, "idempotency_key": key,
	})
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	claim, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "worktree_claim", Input: claimInput}, mutationEnvelope(grant, scopeVersion))
	if err != nil || claim.Outcome != OutcomeOK {
		t.Fatalf("claim response=%+v err=%v", claim, err)
	}
}

// issue #674: worktree_reclaim from the main checkout is conditional. The
// authorization boundary admits the operation and records the main-checkout
// grant; the planner refuses it unless the addressed work item is terminal
// (completed or cancelled), because only terminal work holds no live
// implementation surface. A linked worktree keeps reclaiming for every
// lifecycle (TestWorktreeClaimAndReclaimThroughToolSurface).
func TestWorktreeReclaimFromMainCheckoutRequiresTerminalWork(t *testing.T) {
	dispatchReclaim := func(t *testing.T, s *store.Store, service *Service, grant Authority, key string, expected int64) Envelope {
		t.Helper()
		reclaimInput, _ := json.Marshal(map[string]any{
			"work_id": "work-1", "project_id": "project-1",
			"default_ref": "main", "expected_version": expected, "idempotency_key": key,
		})
		scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
		if err != nil {
			t.Fatal(err)
		}
		response, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "worktree_reclaim", Input: reclaimInput}, mutationEnvelope(grant, scopeVersion))
		if err != nil {
			t.Fatalf("dispatch err=%v", err)
		}
		return response
	}
	mainCheckoutResolver := func(context.Context, *store.Transaction, string, string) (store.ProjectResolution, error) {
		return store.ProjectResolution{ProjectID: "project-1", MainWorktree: true}, nil
	}

	t.Run("non-terminal work refuses", func(t *testing.T) {
		ctx := context.Background()
		s, service, grant, repoRoot, baseSHA := worktreeDispatchFixture(t)
		worktreePath := filepath.Join(t.TempDir(), "linked-wt")
		claimLinkedWorktree(t, s, service, grant, worktreePath, baseSHA, "work/main-inprogress", "claim-inprogress")
		seedWorkTransition(t, s, "work-1", "needed", "in_progress", 3)
		service.ProjectResolver = mainCheckoutResolver

		refused := dispatchReclaim(t, s, service, grant, "main-reclaim-inprogress", 4)
		if refused.Outcome != OutcomeError || refused.Error == nil {
			t.Fatalf("response=%+v, want typed refusal", refused)
		}
		if refused.Error.Kind != "unauthorized" {
			t.Fatalf("error.kind=%q, want unauthorized", refused.Error.Kind)
		}
		if !strings.Contains(refused.Error.Message, "CD-0092 D2") {
			t.Fatalf("error.message=%q, want CD-0092 D2 refusal", refused.Error.Message)
		}
		entries, err := s.WorktreeEntries(ctx, "work-1")
		if err != nil || len(entries) != 1 || entries[0].State != "active" {
			t.Fatalf("entries after refusal=%+v err=%v, want the active claim untouched", entries, err)
		}
		if !strings.Contains(gitRun(t, repoRoot, "worktree", "list"), "linked-wt") {
			t.Fatal("native worktree missing after a refused reclaim")
		}
	})

	t.Run("terminal work reclaims", func(t *testing.T) {
		ctx := context.Background()
		s, service, grant, repoRoot, baseSHA := worktreeDispatchFixture(t)
		worktreePath := filepath.Join(t.TempDir(), "linked-wt")
		claimLinkedWorktree(t, s, service, grant, worktreePath, baseSHA, "work/main-terminal", "claim-terminal")
		seedWorkTransition(t, s, "work-1", "needed", "completed", 3)
		service.ProjectResolver = mainCheckoutResolver

		response := dispatchReclaim(t, s, service, grant, "main-reclaim-terminal", 4)
		if response.Outcome != OutcomeOK {
			t.Fatalf("reclaim response=%+v err=%v", response, response.Error)
		}
		entries, err := s.WorktreeEntries(ctx, "work-1")
		if err != nil || len(entries) != 1 || entries[0].State != "reclaimed" {
			t.Fatalf("entries after reclaim=%+v err=%v", entries, err)
		}
		if strings.Contains(gitRun(t, repoRoot, "worktree", "list"), "linked-wt") {
			t.Fatal("native worktree still present after reclaim")
		}
		if _, statErr := os.Stat(worktreePath); !os.IsNotExist(statErr) {
			t.Fatalf("worktree path still exists after reclaim: %v", statErr)
		}
	})

	t.Run("cancelled work reclaims", func(t *testing.T) {
		ctx := context.Background()
		s, service, grant, _, baseSHA := worktreeDispatchFixture(t)
		worktreePath := filepath.Join(t.TempDir(), "linked-wt")
		claimLinkedWorktree(t, s, service, grant, worktreePath, baseSHA, "work/main-cancelled", "claim-cancelled")
		seedWorkTransition(t, s, "work-1", "needed", "cancelled", 3)
		service.ProjectResolver = mainCheckoutResolver

		response := dispatchReclaim(t, s, service, grant, "main-reclaim-cancelled", 4)
		if response.Outcome != OutcomeOK {
			t.Fatalf("reclaim response=%+v err=%v", response, response.Error)
		}
		entries, err := s.WorktreeEntries(ctx, "work-1")
		if err != nil || len(entries) != 1 || entries[0].State != "reclaimed" {
			t.Fatalf("entries after reclaim=%+v err=%v", entries, err)
		}
	})
}

// TestWorktreeReclaimRefusesOccupiedWorktreeThroughToolSurface pins issue #722
// end to end at the typed boundary: the adapter's observation of the host's
// live sessions reaches the store, and the store refuses the removal that
// would strand one. The observation is an input rather than stored state
// because no event records a session leaving a directory, so a stored answer
// would go stale with nothing to clear it.
func TestWorktreeReclaimRefusesOccupiedWorktreeThroughToolSurface(t *testing.T) {
	ctx := context.Background()
	s, service, grant, repoRoot, baseSHA := worktreeDispatchFixture(t)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	worktreePath := filepath.Join(t.TempDir(), "linked-wt")
	claimLinkedWorktree(t, s, service, grant, worktreePath, baseSHA, "work/dispatch-1", "wt-claim-occupied")

	reclaimWith := func(key string, observed []map[string]any) Envelope {
		t.Helper()
		input, _ := json.Marshal(map[string]any{
			"work_id": "work-1", "project_id": "project-1", "default_ref": "main",
			"expected_version": 3, "idempotency_key": key,
			"observed_session_directories": observed,
		})
		response, dispatchErr := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "worktree_reclaim", Input: input}, mutationEnvelope(grant, scopeVersion))
		if dispatchErr != nil {
			t.Fatalf("dispatch err=%v", dispatchErr)
		}
		return response
	}

	occupied := reclaimWith("wt-reclaim-occupied", []map[string]any{
		{"session_ref": "ses_live", "directory": filepath.Join(worktreePath, "internal")},
	})
	if occupied.Outcome == OutcomeOK {
		t.Fatal("a session inside the worktree must refuse the reclaim")
	}
	// The store refuses with worktree_ownership_conflict, which this surface
	// carries as operation_conflict until the takeover surface mints its own
	// envelope kind. The message is what names the session to the operator.
	if occupied.Error == nil || occupied.Error.Kind != "operation_conflict" {
		t.Fatalf("error=%+v, want operation_conflict", occupied.Error)
	}
	if !strings.Contains(occupied.Error.Message, "ses_live") || !strings.Contains(occupied.Error.Message, worktreePath) {
		t.Fatalf("refusal %q must name the occupying session and the worktree", occupied.Error.Message)
	}
	if !strings.Contains(gitRun(t, repoRoot, "worktree", "list"), "linked-wt") {
		t.Fatal("a refused reclaim must leave the native worktree in place")
	}

	// The same worktree with every live session elsewhere reclaims normally.
	free := reclaimWith("wt-reclaim-free", []map[string]any{
		{"session_ref": "ses_live", "directory": filepath.Join(repoRoot)},
	})
	if free.Outcome != OutcomeOK {
		t.Fatalf("response=%+v, want the reclaim to proceed", free)
	}
	if strings.Contains(gitRun(t, repoRoot, "worktree", "list"), "linked-wt") {
		t.Fatal("native worktree still present after reclaim")
	}
}

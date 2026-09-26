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
	// A Project repository is a clone: validateBootstrapDefaultBranch proves
	// the default branch through origin/HEAD, and a claim resolves its base
	// from the matching remote-tracking ref.
	gitRun(t, repoRoot, "update-ref", "refs/remotes/origin/main", "HEAD")
	gitRun(t, repoRoot, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	baseSHA := gitRun(t, repoRoot, "rev-parse", "HEAD")

	if err := s.AddProjectLocator(ctx, "project-1", store.ProjectLocator{ID: "path-1", Kind: store.LocatorCanonicalPath, Value: repoRoot}, 1); err != nil {
		t.Fatal(err)
	}

	service, _, grant := newAuthorizedService(t, s, "client-1", "human-1", []Capability{"work_transition", "product_read"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	return s, service, grant, repoRoot, baseSHA
}

func TestWorktreeClaimAndReclaimThroughToolSurface(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, repoRoot, baseSHA := worktreeDispatchFixture(t)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	worktreePath := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-1")

	claimInput, _ := json.Marshal(map[string]any{
		"work_id": "work-1", "project_id": "project-1",
		"base_sha":         baseSHA,
		"expected_version": 2, "idempotency_key": "wt-claim-1",
	})
	request := InvokeRequest{Tool: "concord_work_transition", Operation: "worktree_claim", Input: claimInput}
	response, err := Dispatch(ctx, s, service, request, mutationEnvelope(grant, scopeVersion))
	if err != nil || response.Outcome != OutcomeOK {
		t.Fatalf("claim response=%+v err=%v", response, err)
	}
	var claimResult struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(response.Result, &claimResult); err != nil {
		t.Fatal(err)
	}
	if claimResult.Path != worktreePath {
		t.Fatalf("claim result path=%q, want %q", claimResult.Path, worktreePath)
	}
	entries, err := s.WorktreeEntries(ctx, "work-1")
	if err != nil || len(entries) != 1 || entries[0].State != "active" || entries[0].Branch != "work/work-1" {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
	listing := gitRun(t, repoRoot, "worktree", "list", "--porcelain")
	if !strings.Contains(listing, "work-1") {
		t.Fatalf("native worktree missing:\n%s", listing)
	}

	// The same claim retried is an idempotent replay, not a second worktree.
	replay, err := Dispatch(ctx, s, service, request, mutationEnvelope(grant, scopeVersion))
	if err != nil || replay.Outcome != OutcomeOK {
		t.Fatalf("replay response=%+v err=%v", replay, err)
	}
	if got := strings.Count(gitRun(t, repoRoot, "worktree", "list"), worktreePath); got != 1 {
		t.Fatalf("expected one linked worktree line, got %d", got)
	}

	// The claiming session vacates before the reclaim; the removal gate
	// refuses while a recorded occupant remains.
	vacateLinkedWorktree(t, s, service, grant, worktreePath, "wt-vacate-1")

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
	if strings.Contains(gitRun(t, repoRoot, "worktree", "list"), "work-1") {
		t.Fatal("native worktree still present after reclaim")
	}
}

// TestWorktreeClaimRefusesWhenSessionOccupiesAnotherWorktree pins the rule that
// a claim never clears another worktree's occupancy. The clear would commit with
// the claim transaction, while the host relocation that makes it true runs
// afterwards and outside it, so a refused relocation would leave this session in
// a directory the removal gate reads as empty. The claim refuses, and both the
// source occupancy row and the destination durable state stay unchanged.
func TestWorktreeClaimRefusesWhenSessionOccupiesAnotherWorktree(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _, baseSHA := worktreeDispatchFixture(t)
	seed := []store.Event{
		{EventID: "wt-dispatch-transfer-work-2", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"Occupancy Refusal","priority":1}`)},
		{EventID: "wt-dispatch-transfer-work-2-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: seed, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "work-2"): 0}}); err != nil {
		t.Fatal(err)
	}
	claim := func(workID, key string) (Envelope, error) {
		t.Helper()
		scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
		if err != nil {
			t.Fatal(err)
		}
		input, _ := json.Marshal(map[string]any{"work_id": workID, "project_id": "project-1", "base_sha": baseSHA, "expected_version": 2, "idempotency_key": key})
		return Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "worktree_claim", Input: input}, mutationEnvelope(grant, scopeVersion))
	}
	first, err := claim("work-1", "occupancy-refusal-1")
	if err != nil || first.Outcome != OutcomeOK {
		t.Fatalf("first claim response=%+v err=%v", first, err)
	}
	second, err := claim("work-2", "occupancy-refusal-2")
	if err == nil && second.Outcome == OutcomeOK {
		t.Fatalf("claim by an occupying session must refuse: response=%+v", second)
	}

	entries, err := s.WorktreeEntries(ctx, "work-1")
	if err != nil || len(entries) != 1 {
		t.Fatalf("source occupancy must survive the refusal: entries=%+v err=%v", entries, err)
	}
	// Occupancy lives in worktree_occupancy (CD-0178 D3). The entry stays
	// active; the durable row carries the recording session.
	occupant, occErr := readOccupantSession(t, s, entries[0])
	if occErr != nil {
		t.Fatalf("read worktree_occupancy: %v", occErr)
	}
	if occupant != grant.SessionRef {
		t.Fatalf("source occupancy row must hold the recording session: got=%q entries=%+v", occupant, entries)
	}
	entries, err = s.WorktreeEntries(ctx, "work-2")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.State == "active" {
			t.Fatalf("refused claim left an active destination entry: %+v", entry)
		}
	}
	if err := store.RebuildFromLog(ctx, s); err != nil {
		t.Fatal(err)
	}
	entries, err = s.WorktreeEntries(ctx, "work-1")
	if err != nil || len(entries) != 1 {
		t.Fatalf("source occupancy after rebuild=%+v err=%v", entries, err)
	}
	if occupant, _ := readOccupantSession(t, s, entries[0]); occupant != grant.SessionRef {
		t.Fatalf("source occupancy after rebuild entries=%+v", entries)
	}
}

// seedSecondProjectFixture adds Product product-2 with Project project-2 over
// its own repository. The ambient Project stays single-Product, so only the
// claimed Project can make the mutation cross-Product, and the returned base
// commit pins the second repository.
func seedSecondProjectFixture(t *testing.T, s *store.Store) string {
	t.Helper()
	ctx := context.Background()
	events := []store.Event{
		{EventID: "wt-scope-product-2", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"WT Scope Two","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "wt-scope-project-2", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"WT Project Two"}`)},
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

// seedSiblingProjectFixture adds Project project-1b under product-1 over its
// own repository and extends work-1 to hold it beside project-1, so a claim
// may name a Project other than the ambient one within the same Product. The
// returned base commit pins the sibling repository.
func seedSiblingProjectFixture(t *testing.T, s *store.Store) string {
	t.Helper()
	ctx := context.Background()
	events := []store.Event{
		{EventID: "wt-sibling-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1b", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"WT Project One B"}`)},
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
	gitRun(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	gitRun(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
}

// A claim that names a Project outside the envelope's selected Product is a
// cross-Product mutation: the claimed Project joins the plan scope, so the
// scope gate evaluates its Product exactly as for every other mutation. The
// client policy names product-2, so the policy passes and the cross_scope
// capability gate refuses the mutation before any effect.
func TestWorktreeClaimCrossScopeGate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, _, _, _ := worktreeDispatchFixture(t)
	baseSHA := seedSecondProjectFixture(t, s)
	// The client policy names both Products, so the refusal below is the
	// cross_scope capability gate and not the client's own Product policy.
	service, _, crossGrant := newAuthorizedService(t, s, "client-cross", "human-cross", []Capability{"work_transition", "product_read"}, []string{"product-1", "product-2"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{"work_id": "work-1", "project_id": "project-2", "base_sha": baseSHA, "expected_version": 2, "idempotency_key": "cross-scope-claim"})
	response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "worktree_claim", Input: input}, mutationEnvelope(crossGrant, scopeVersion))
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "unauthorized" {
		t.Fatalf("claim response=%+v, want an unauthorized refusal", response)
	}
	if !strings.Contains(response.Error.Message, "cross_scope") {
		t.Fatalf("error.message=%q, want the cross_scope capability gate refusal", response.Error.Message)
	}
	entries, err := s.WorktreeEntries(ctx, "work-1")
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries after refused claim=%+v err=%v, want no durable claim", entries, err)
	}
}

// A multi-Project work item may claim a worktree under its second Project:
// project_id need not equal the ambient Project, and the claim result carries
// the derived destination for the session move. The follow-up dispatch
// attempt resolves, so the claim strands neither the session nor the surface.
func TestWorktreeClaimCrossProjectCarriesDestination(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _, _ := worktreeDispatchFixture(t)
	baseSHA := seedSiblingProjectFixture(t, s)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{"work_id": "work-1", "project_id": "project-1b", "base_sha": baseSHA, "expected_version": 3, "idempotency_key": "cross-project-claim"})
	response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "worktree_claim", Input: input}, mutationEnvelope(grant, scopeVersion))
	if err != nil || response.Outcome != OutcomeOK {
		t.Fatalf("claim response=%+v err=%v", response, err)
	}
	var claimResult struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(response.Result, &claimResult); err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1b", "work-1")
	if claimResult.Path != wantPath {
		t.Fatalf("claim result path=%q, want %q", claimResult.Path, wantPath)
	}
	browse, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_browse", Operation: "scope", Input: json.RawMessage(`{"product_id":"product-1","work_id":"work-1"}`)}, mutationEnvelope(grant, scopeVersion))
	if err != nil || browse.Outcome != OutcomeOK {
		t.Fatalf("follow-up dispatch response=%+v err=%v", browse, err)
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
		"base_sha":         baseSHA,
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

// vacateLinkedWorktree records that the claiming session left its linked
// worktree. The removal gate refuses while a recorded occupant remains, so
// every test that claims and then removes must vacate in between.
func vacateLinkedWorktree(t *testing.T, s *store.Store, service *Service, grant Authority, worktreePath, key string) {
	t.Helper()
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	env.Worktree = worktreePath
	env.Directory = worktreePath
	input, _ := json.Marshal(map[string]any{"idempotency_key": key})
	vacate, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "session_vacate", Input: input}, env)
	if err != nil || vacate.Outcome != OutcomeOK {
		t.Fatalf("vacate response=%+v err=%v", vacate, err)
	}
}

// issue #674: worktree_reclaim from the main checkout is conditional. The
// authorization boundary admits the operation and records the main-checkout
// grant; the planner refuses it unless the addressed work item is terminal
// (completed or cancelled), because only terminal work holds no live
// implementation surface. A linked worktree keeps reclaiming for every
// lifecycle (TestWorktreeClaimAndReclaimThroughToolSurface).
func TestWorktreeReclaimFromMainCheckoutRequiresTerminalWork(t *testing.T) {
	t.Parallel()
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
		worktreePath := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-1")
		claimLinkedWorktree(t, s, service, grant, worktreePath, baseSHA, "work/main-inprogress", "claim-inprogress")
		vacateLinkedWorktree(t, s, service, grant, worktreePath, "vacate-inprogress")
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
		if !strings.Contains(gitRun(t, repoRoot, "worktree", "list"), "work-1") {
			t.Fatal("native worktree missing after a refused reclaim")
		}
	})

	t.Run("terminal work reclaims", func(t *testing.T) {
		ctx := context.Background()
		s, service, grant, repoRoot, baseSHA := worktreeDispatchFixture(t)
		worktreePath := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-1")
		claimLinkedWorktree(t, s, service, grant, worktreePath, baseSHA, "work/main-terminal", "claim-terminal")
		vacateLinkedWorktree(t, s, service, grant, worktreePath, "vacate-terminal")
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
		if strings.Contains(gitRun(t, repoRoot, "worktree", "list"), "work-1") {
			t.Fatal("native worktree still present after reclaim")
		}
		if _, statErr := os.Stat(worktreePath); !os.IsNotExist(statErr) {
			t.Fatalf("worktree path still exists after reclaim: %v", statErr)
		}
	})

	t.Run("cancelled work reclaims", func(t *testing.T) {
		ctx := context.Background()
		s, service, grant, _, baseSHA := worktreeDispatchFixture(t)
		worktreePath := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-1")
		claimLinkedWorktree(t, s, service, grant, worktreePath, baseSHA, "work/main-cancelled", "claim-cancelled")
		vacateLinkedWorktree(t, s, service, grant, worktreePath, "vacate-cancelled")
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

// TestWorktreeReclaimRefusesOccupiedWorktreeThroughToolSurface pins the
// stored occupancy gate end to end at the typed boundary.
func TestWorktreeReclaimRefusesOccupiedWorktreeThroughToolSurface(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, repoRoot, baseSHA := worktreeDispatchFixture(t)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	worktreePath := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-1")
	claimLinkedWorktree(t, s, service, grant, worktreePath, baseSHA, "work/dispatch-1", "wt-claim-occupied")

	reclaimWith := func(key string, observed []map[string]any) Envelope {
		t.Helper()
		input, _ := json.Marshal(map[string]any{
			"work_id": "work-1", "project_id": "project-1", "default_ref": "main",
			"expected_version": 3, "idempotency_key": key,
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
	// carries as unauthorized: the remedy is the occupying session releasing
	// the directory or the operator ending it, not a reconciliation the caller
	// can run. The message is what names the session to the operator.
	if occupied.Error == nil || occupied.Error.Kind != "unauthorized" {
		t.Fatalf("error=%+v, want unauthorized", occupied.Error)
	}
	if !strings.Contains(occupied.Error.Message, grant.SessionRef) || !strings.Contains(occupied.Error.Message, worktreePath) {
		t.Fatalf("refusal %q must name the occupying session and the worktree", occupied.Error.Message)
	}
	if !strings.Contains(gitRun(t, repoRoot, "worktree", "list"), "work-1") {
		t.Fatal("a refused reclaim must leave the native worktree in place")
	}
	vacateLinkedWorktree(t, s, service, grant, worktreePath, "wt-vacate-occupied")

	// The same worktree with every live session elsewhere reclaims normally.
	free := reclaimWith("wt-reclaim-free", []map[string]any{
		{"session_ref": "ses_live", "directory": filepath.Join(repoRoot)},
	})
	if free.Outcome != OutcomeOK {
		t.Fatalf("response=%+v, want the reclaim to proceed", free)
	}
	if strings.Contains(gitRun(t, repoRoot, "worktree", "list"), "work-1") {
		t.Fatal("native worktree still present after reclaim")
	}
}

// The direct reclaim's planner forwards the host session observation to the
// store request: an observation placing the recorded occupant inside the
// worktree keeps the strand-guard refusal, and an observation placing the
// occupant at a readable directory elsewhere reclaims the row. The store owns
// the release semantics; this pins that the agent surface carries the field
// through. CD-0178 D3 removed the host observation input from reclaim; the
// replacement tests live under TestReclaimWorktreeKeepsLiveOccupantRefusal
// and TestDestroyReleasesRecordedStaleOccupancyWithApproval.
func TestReclaimForwardsObservedSessionDirectories(t *testing.T) {
	t.Skip("TestReclaimForwardsObservedSessionDirectories pinned the legacy host-observation release path that CD-0178 D3 removed.")
	t.Parallel()
	ctx := context.Background()
	s, service, grant, repoRoot, baseSHA := worktreeDispatchFixture(t)
	worktreePath := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-1")
	claimLinkedWorktree(t, s, service, grant, worktreePath, baseSHA, "work/reclaim-observation", "reclaim-observation-claim")
	seedWorkTransition(t, s, "work-1", "needed", "completed", 3)
	service.ProjectResolver = func(context.Context, *store.Transaction, string, string) (store.ProjectResolution, error) {
		return store.ProjectResolution{ProjectID: "project-1", MainWorktree: true}, nil
	}
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}

	reclaimWith := func(key string, observed []map[string]any) Envelope {
		t.Helper()
		input, _ := json.Marshal(map[string]any{
			"work_id": "work-1", "project_id": "project-1", "default_ref": "main",
			"expected_version": 4, "idempotency_key": key,
		})
		response, dispatchErr := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "worktree_reclaim", Input: input}, mutationEnvelope(grant, scopeVersion))
		if dispatchErr != nil {
			t.Fatalf("dispatch err=%v", dispatchErr)
		}
		return response
	}

	// The observation places the recorded occupant's own session inside the
	// worktree: the strand-guard refusal stays.
	naming := reclaimWith("reclaim-observation-naming", []map[string]any{
		{"session_ref": grant.SessionRef, "directory": worktreePath},
	})
	if naming.Outcome == OutcomeOK {
		t.Fatal("an observation placing the recorded occupant inside the worktree must keep the refusal")
	}
	if naming.Error == nil || naming.Error.Kind != "unauthorized" {
		t.Fatalf("error=%+v, want unauthorized", naming.Error)
	}
	if !strings.Contains(naming.Error.Message, grant.SessionRef) || !strings.Contains(naming.Error.Message, worktreePath) {
		t.Fatalf("refusal %q must name the occupying session and the worktree", naming.Error.Message)
	}

	// The same recorded occupant is observed at a readable directory outside
	// the worktree: a work_start move has retargeted it, so the reclaim
	// proceeds.
	moved := reclaimWith("reclaim-observation-moved", []map[string]any{
		{"session_ref": grant.SessionRef, "directory": repoRoot},
	})
	if moved.Outcome != OutcomeOK {
		t.Fatalf("response=%+v, want the occupant-observed-elsewhere reclaim to proceed", moved)
	}
	entries, err := s.WorktreeEntries(ctx, "work-1")
	if err != nil || len(entries) != 1 || entries[0].State != "reclaimed" {
		t.Fatalf("entries after reclaim=%+v err=%v", entries, err)
	}
	if strings.Contains(gitRun(t, repoRoot, "worktree", "list"), "work-1") {
		t.Fatal("native worktree still present after reclaim")
	}
}

func TestSessionVacateSucceedsFromLinkedWorktreeMutation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, repoRoot, baseSHA := worktreeDispatchFixture(t)
	worktreePath := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-1")
	claimLinkedWorktree(t, s, service, grant, worktreePath, baseSHA, "work/vacate", "claim-vacate")

	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	env.Worktree = worktreePath
	env.Directory = worktreePath
	request := InvokeRequest{
		Tool:      "concord_work_transition",
		Operation: "session_vacate",
		Input:     json.RawMessage(`{"idempotency_key":"vacate-1"}`),
	}
	response, err := Dispatch(ctx, s, service, request, env)
	if err != nil || response.Outcome != OutcomeOK {
		t.Fatalf("vacate response=%+v error=%+v err=%v", response, response.Error, err)
	}
	var result struct {
		WorkID               string `json:"work_id"`
		ProjectID            string `json:"project_id"`
		SourceDirectory      string `json:"source_directory"`
		DestinationDirectory string `json:"destination_directory"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.WorkID != "work-1" || result.ProjectID != "project-1" {
		t.Fatalf("vacate result=%+v", result)
	}
	if result.SourceDirectory != worktreePath {
		t.Fatalf("source_directory=%q, want %q", result.SourceDirectory, worktreePath)
	}
	if result.DestinationDirectory != repoRoot {
		t.Fatalf("destination_directory=%q, want registered main checkout %q", result.DestinationDirectory, repoRoot)
	}
	var eventCount int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, "work-1", "work.session_vacated").Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("session_vacated event count=%d, want 1", eventCount)
	}
}

// The vacate request digest is caller-constant: the input carries only the
// idempotency key, which mutationDigest strips. When the event id was the
// bare digest, the first vacate ever recorded owned the id for every later
// session, and each one refused as a conflicting replay. A second session
// vacating its own linked worktree must record its own event.
func TestSecondSessionVacateRecordsItsOwnEvent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _, baseSHA := worktreeDispatchFixture(t)

	worktreeA := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-1")
	claimLinkedWorktree(t, s, service, grant, worktreeA, baseSHA, "work/vacate-a", "claim-vacate-a")
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	envA := mutationEnvelope(grant, scopeVersion)
	envA.Worktree = worktreeA
	envA.Directory = worktreeA
	first, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "session_vacate", Input: json.RawMessage(`{"idempotency_key":"vacate-a"}`)}, envA)
	if err != nil || first.Outcome != OutcomeOK {
		t.Fatalf("first vacate response=%+v error=%+v err=%v", first, first.Error, err)
	}

	// A second work item gives the second session its own linked worktree.
	seed := []store.Event{
		{EventID: "wt-dispatch-work-2", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"Second Vacate","priority":1}`)},
		{EventID: "wt-dispatch-work-2-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: seed, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "work-2"): 0}}); err != nil {
		t.Fatal(err)
	}

	grantB := grant
	grantB.SessionRef = "session/vacate-b"
	worktreeB := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-2")
	claimB, _ := json.Marshal(map[string]any{
		"work_id": "work-2", "project_id": "project-1",
		"base_sha":         baseSHA,
		"expected_version": 2, "idempotency_key": "claim-vacate-b",
	})
	scopeVersionB, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	claim, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "worktree_claim", Input: claimB}, mutationEnvelope(grantB, scopeVersionB))
	if err != nil || claim.Outcome != OutcomeOK {
		t.Fatalf("second claim response=%+v err=%v", claim, err)
	}

	envB := mutationEnvelope(grantB, scopeVersionB)
	envB.Worktree = worktreeB
	envB.Directory = worktreeB
	second, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "session_vacate", Input: json.RawMessage(`{"idempotency_key":"vacate-b"}`)}, envB)
	if err != nil || second.Outcome != OutcomeOK {
		t.Fatalf("second vacate response=%+v error=%+v err=%v", second, second.Error, err)
	}
	var eventIDs, sessionRefs []string
	rows, err := s.DatabaseForTesting().QueryContext(ctx, `SELECT event_id, json_extract(payload,'$.session_ref') FROM domain_events WHERE kind='work.session_vacated' ORDER BY occurred_at`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var eventID, sessionRef string
		if err := rows.Scan(&eventID, &sessionRef); err != nil {
			t.Fatal(err)
		}
		eventIDs = append(eventIDs, eventID)
		sessionRefs = append(sessionRefs, sessionRef)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(eventIDs) != 2 {
		t.Fatalf("session_vacated events=%v, want one per session", eventIDs)
	}
	if eventIDs[0] == eventIDs[1] || sessionRefs[0] == sessionRefs[1] {
		t.Fatalf("session_vacated ids=%v sessions=%v, want distinct ids and sessions", eventIDs, sessionRefs)
	}
}

// readOccupantSession reads the recorded session_ref from the durable
// worktree_occupancy projection for the named worktree entry (CD-0178 D3).
// Empty means the worktree carries no occupancy row.
func readOccupantSession(t *testing.T, s *store.Store, entry store.WorktreeEntry) (string, error) {
	t.Helper()
	var sessionRef string
	row := s.DatabaseForTesting().QueryRow(`SELECT session_ref FROM worktree_occupancy WHERE worktree_id=? ORDER BY recorded_at LIMIT 1`, entry.SetID+":"+entry.ProjectID+":"+entry.ClaimOpID)
	if err := row.Scan(&sessionRef); err != nil {
		if err.Error() == "sql: no rows in result set" {
			return "", nil
		}
		return "", err
	}
	return sessionRef, nil
}

// CD-0178 D2: a worktree_claim whose target Project lives in another git
// repository refuses with cross_repository_claim before any worktree exists,
// and the refusal is not retryable — the host refuses the move, so the only
// route is a second coordinator session in the target repository. Two
// CD-0178 D2: a worktree_claim whose target Project lives in another git
// repository refuses with cross_repository_claim before any worktree exists,
// and the refusal is not retryable — the host refuses the move, so the only
// route is a second coordinator session in the target repository. Two
// Projects in one repository keep the within-repository move.
func TestWorktreeClaimRefusesCrossRepositoryBeforeCreation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, repoRoot, baseSHA := worktreeDispatchFixture(t)
	// project-1x joins the work item inside product-1, but its canonical
	// locator resolves to a different git repository than the calling
	// session's directory.
	crossRepo := t.TempDir()
	seedFixtureRepo(t, crossRepo, "# fixture cross repository\n", "fixture base cross")
	events := []store.Event{
		{EventID: "wt-cross-repo-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1x", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"WT Cross Repo"}`)},
		{EventID: "wt-cross-repo-membership", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-1x","role":"secondary","reason":"cross repository fixture","expected_version":2,"resulting_version":3}`)},
		{EventID: "wt-cross-repo-work-memberships", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"},{"project_id":"project-1x","role":"secondary"}],"expected_version":2,"resulting_version":3}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 2, store.VersionRef(store.SubjectProject, "project-1x"): 0, store.VersionRef(store.SubjectWorkItem, "work-1"): 2}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProjectLocator(ctx, "project-1x", store.ProjectLocator{ID: "path-1x", Kind: store.LocatorCanonicalPath, Value: crossRepo}, 1); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{"work_id": "work-1", "project_id": "project-1x", "base_sha": baseSHA, "expected_version": 3, "idempotency_key": "cross-repo-claim"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	// The calling session runs in project-1's repository.
	env.Directory = repoRoot
	response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "worktree_claim", Input: input}, env)
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "cross_repository_claim" {
		t.Fatalf("claim error=%+v, want a cross_repository_claim refusal", response.Error)
	}
	if response.Error.RetrySafe {
		t.Fatalf("cross_repository_claim must not be retryable, got retry_safe=%v", response.Error.RetrySafe)
	}
	if !strings.Contains(response.Error.Message, "one coordinator session per repository") {
		t.Fatalf("error.message=%q, want the one-session-per-repository route", response.Error.Message)
	}
	entries, err := s.WorktreeEntries(ctx, "work-1")
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries after refused claim=%+v err=%v, want no durable claim", entries, err)
	}
	foreignWorktree := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1x", "work-1")
	if _, statErr := os.Stat(foreignWorktree); !os.IsNotExist(statErr) {
		t.Fatalf("refused claim created the native worktree %s: %v", foreignWorktree, statErr)
	}

	// Two Projects in one repository keep the move: the second Project's
	// canonical locator is a checkout of the calling session's own
	// repository, so the git common dir of both locators matches.
	sameRepoTree := filepath.Join(repoRoot, "sibling-checkout")
	if err := os.MkdirAll(sameRepoTree, 0o755); err != nil {
		t.Fatal(err)
	}
	sameEvents := []store.Event{
		{EventID: "wt-same-repo-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1s", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"WT Same Repo"}`)},
		{EventID: "wt-same-repo-membership", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-1s","role":"secondary","reason":"same repository fixture","expected_version":3,"resulting_version":4}`)},
		{EventID: "wt-same-repo-work-memberships", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"},{"project_id":"project-1s","role":"secondary"}],"expected_version":3,"resulting_version":4}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: sameEvents, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 3, store.VersionRef(store.SubjectProject, "project-1s"): 0, store.VersionRef(store.SubjectWorkItem, "work-1"): 3}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProjectLocator(ctx, "project-1s", store.ProjectLocator{ID: "path-1s", Kind: store.LocatorCanonicalPath, Value: sameRepoTree}, 1); err != nil {
		t.Fatal(err)
	}
	sameInput, _ := json.Marshal(map[string]any{"work_id": "work-1", "project_id": "project-1s", "base_sha": baseSHA, "expected_version": 4, "idempotency_key": "same-repo-claim"})
	scopeVersion, _, err = s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env = mutationEnvelope(grant, scopeVersion)
	env.Directory = repoRoot
	response, err = Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "worktree_claim", Input: sameInput}, env)
	if err != nil || response.Outcome != OutcomeOK {
		t.Fatalf("same-repository claim error=%+v outcome=%s err=%v, want the within-repository move", response.Error, response.Outcome, err)
	}
	var claimResult struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(response.Result, &claimResult); err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1s", "work-1")
	if claimResult.Path != wantPath {
		t.Fatalf("claim result path=%q, want %q", claimResult.Path, wantPath)
	}
}

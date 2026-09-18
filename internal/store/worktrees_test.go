package store

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeWorktreeGit models one primary repository plus any further roots a test
// registers: branches with heads, existing linked worktrees keyed by path,
// ancestry, durability, dirtiness, and the default ref. It records every
// invocation so tests can assert what git was asked to do.
type fakeWorktreeGit struct {
	repoRoot      string
	extraRoots    map[string]bool   // further repository roots this fake answers for
	worktreeRepos map[string]string // worktree path -> repository root
	branches      map[string]string // branch -> head sha
	worktrees     map[string]string // path -> branch
	dirty         map[string]bool   // path -> dirty
	content       map[string]string // branch -> tree content id; defaults to the head sha
	defaultRef    string
	headBranch    string         // the ref HEAD resolves to; the fixture default is main
	ahead         map[string]int // branch -> commit count beyond the default ref
	unpushed      map[string]int // branch -> commits unreachable from local remotes
	mergeConflict bool
	failAdd       bool
	calls         [][]string
}

// addRepository registers a further repository root the fake answers for.
// Branch state stays shared across roots; the store's own per-repository slot
// is what the tests exercise, and each root reports itself as its toplevel.
func (g *fakeWorktreeGit) addRepository(root string) {
	g.extraRoots[root] = true
}

// treeOf models the tree a ref resolves to. Content is normally keyed to the
// head sha, so two branches agree only when their heads agree. A squash merge
// is modelled by giving a branch the default ref's content under a different
// head sha, which is exactly the shape commit reachability cannot see.
func (g *fakeWorktreeGit) treeOf(ref string) string {
	name := strings.TrimPrefix(ref, "origin/")
	if tree, ok := g.content[name]; ok {
		return tree
	}
	return g.resolveRef(ref)
}

func newFakeWorktreeGit(repoRoot string) *fakeWorktreeGit {
	return &fakeWorktreeGit{
		repoRoot:      repoRoot,
		extraRoots:    map[string]bool{},
		worktreeRepos: map[string]string{},
		branches:      map[string]string{"main": strings.Repeat("a", 40)},
		worktrees:     map[string]string{},
		dirty:         map[string]bool{},
		content:       map[string]string{},
		ahead:         map[string]int{},
		unpushed:      map[string]int{},
		headBranch:    "main",
		defaultRef:    "origin/main",
	}
}

func (g *fakeWorktreeGit) Run(_ context.Context, dir string, args ...string) ([]byte, error) {
	g.calls = append(g.calls, append([]string{dir}, args...))
	join := strings.Join(args, " ")
	switch {
	case join == "rev-parse --show-toplevel":
		if dir != g.repoRoot && !g.extraRoots[dir] {
			return nil, fmt.Errorf("not the repository root")
		}
		return []byte(dir + "\n"), nil
	case join == "rev-parse --abbrev-ref HEAD":
		branch, ok := g.worktrees[dir]
		if !ok {
			return nil, fmt.Errorf("not a worktree")
		}
		return []byte(branch + "\n"), nil
	case join == "rev-parse HEAD":
		branch, ok := g.worktrees[dir]
		if !ok {
			return nil, fmt.Errorf("not a worktree")
		}
		return []byte(g.branches[branch] + "\n"), nil
	case join == "rev-parse --git-common-dir":
		root, ok := g.worktreeRepos[dir]
		if !ok {
			return nil, fmt.Errorf("not a worktree")
		}
		return []byte(filepath.Join(root, ".git") + "\n"), nil
	case strings.HasPrefix(join, "merge-tree --write-tree"):
		if g.mergeConflict {
			return nil, fmt.Errorf("merge conflict")
		}
		parts := strings.Fields(join)
		into, from := parts[2], parts[3]
		if g.resolveRef(into) == into || g.resolveRef(from) == from {
			return nil, fmt.Errorf("unknown ref")
		}
		// Merging a contained branch adds nothing, so the merged tree is the
		// target's own tree. Otherwise the merge produces a different tree.
		if g.treeOf(from) == g.treeOf(into) {
			return []byte(g.treeOf(into) + "\n"), nil
		}
		return []byte(strings.Repeat("f", 40) + "\n"), nil
	case strings.HasSuffix(join, "^{tree}") && strings.HasPrefix(join, "rev-parse "):
		ref := strings.TrimSuffix(strings.TrimPrefix(join, "rev-parse "), "^{tree}")
		if g.resolveRef(ref) == ref {
			return nil, fmt.Errorf("unknown ref")
		}
		return []byte(g.treeOf(ref) + "\n"), nil
	case strings.HasPrefix(join, "merge-base --is-ancestor"):
		parts := strings.Fields(join)
		base, head := g.resolveRef(parts[2]), g.resolveRef(parts[3])
		if base == "" || head == "" {
			return nil, fmt.Errorf("unknown ref")
		}
		if base == head {
			return nil, nil
		}
		return nil, fmt.Errorf("not an ancestor")
	case strings.HasPrefix(join, "rev-list --count ") && strings.HasSuffix(join, " --not --remotes"):
		branch := strings.TrimSuffix(strings.TrimPrefix(join, "rev-list --count "), " --not --remotes")
		return []byte(strconv.Itoa(g.unpushed[branch]) + "\n"), nil
	case strings.HasPrefix(join, "rev-list --count "):
		refs := strings.TrimPrefix(join, "rev-list --count ")
		dotdot := strings.Index(refs, "..")
		if dotdot < 0 {
			return nil, fmt.Errorf("malformed revision range")
		}
		branch := strings.TrimPrefix(refs[dotdot+2:], "origin/")
		return []byte(strconv.Itoa(g.ahead[branch]) + "\n"), nil
	case strings.HasPrefix(join, "worktree add"):
		if g.failAdd {
			return nil, fmt.Errorf("native add failed")
		}
		parts := strings.Fields(join)
		path := parts[2]
		branch := parts[3]
		base := g.branches[branch]
		if len(parts) == 6 && parts[3] == "-b" {
			branch, base = parts[4], parts[5]
		}
		if _, exists := g.worktrees[path]; exists {
			return nil, fmt.Errorf("worktree already exists")
		}
		g.worktrees[path] = branch
		g.worktreeRepos[path] = dir
		g.branches[branch] = base
		return nil, nil
	case strings.HasPrefix(join, "branch -D "):
		branch := strings.TrimPrefix(join, "branch -D -- ")
		if _, exists := g.branches[branch]; !exists {
			return nil, fmt.Errorf("branch does not exist")
		}
		delete(g.branches, branch)
		return nil, nil
	case strings.HasPrefix(join, "worktree remove"):
		fields := strings.Fields(join)
		path := fields[2]
		if path == "--force" {
			path = fields[3]
		}
		if _, ok := g.worktrees[path]; !ok {
			return nil, fmt.Errorf("no such worktree")
		}
		delete(g.worktrees, path)
		return nil, nil
	case join == "status --porcelain":
		if g.dirty[dir] {
			return []byte("M file\n"), nil
		}
		return nil, nil
	case join == "diff HEAD":
		if g.dirty[dir] {
			return []byte("M file\n"), nil
		}
		return nil, nil
	case join == "symbolic-ref refs/remotes/origin/HEAD" || join == "symbolic-ref --quiet refs/remotes/origin/HEAD":
		if g.defaultRef == "" {
			return nil, fmt.Errorf("no origin HEAD")
		}
		return []byte("refs/remotes/" + g.defaultRef + "\n"), nil
	case strings.HasPrefix(join, "show-ref --verify --quiet refs/heads/"):
		branch := strings.TrimPrefix(join, "show-ref --verify --quiet refs/heads/")
		if _, exists := g.branches[branch]; !exists {
			return nil, exec.Command("false").Run()
		}
		return nil, nil
	case strings.HasPrefix(join, "rev-parse --verify ") && strings.HasSuffix(join, "^{commit}"):
		ref := strings.TrimSuffix(strings.TrimPrefix(join, "rev-parse --verify "), "^{commit}")
		ref = strings.TrimPrefix(ref, "refs/heads/")
		if ref == "HEAD" {
			ref = g.headBranch
		}
		if sha := g.resolveRef(ref); sha != ref {
			return []byte(sha + "\n"), nil
		}
		return nil, fmt.Errorf("unknown ref")
	}
	return nil, fmt.Errorf("unexpected git invocation: %s", join)
}

func (g *fakeWorktreeGit) resolveRef(ref string) string {
	ref = strings.TrimPrefix(ref, "refs/remotes/")
	if sha, ok := g.branches[ref]; ok {
		return sha
	}
	if local := strings.TrimPrefix(ref, "origin/"); local != ref {
		if sha, ok := g.branches[local]; ok {
			return sha
		}
	}
	return ref
}

func (g *fakeWorktreeGit) countCalls(prefix string) int {
	n := 0
	for _, call := range g.calls {
		if strings.HasPrefix(strings.Join(call[1:], " "), prefix) {
			n++
		}
	}
	return n
}

func worktreeFixture(t *testing.T) (*Store, *fakeWorktreeGit, string) {
	t.Helper()
	s := openTemp(t)
	ctx := context.Background()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{locatorProductEvent("product-w"), locatorProjectEvent("project-w"), locatorMembershipEvent("product-w", "project-w")}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product-w"): 0, VersionRef(SubjectProject, "project-w"): 0}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		{EventID: "work-w-create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 2, Payload: jsonRaw(`{"work_kind":"task","title":"Worktree Work","priority":1}`)},
		{EventID: "work-w-membership", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"memberships":[{"project_id":"project-w","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-w"): 0}}); err != nil {
		t.Fatal(err)
	}
	repoRoot := t.TempDir()
	if err := s.AddProjectLocator(ctx, "project-w", ProjectLocator{ID: "path-w", Kind: LocatorCanonicalPath, Value: repoRoot}, 1); err != nil {
		t.Fatal(err)
	}
	git := newFakeWorktreeGit(repoRoot)
	return s, git, repoRoot
}

func jsonRaw(s string) []byte { return []byte(s) }

func baseClaim(git *fakeWorktreeGit) WorktreeClaimRequest {
	return WorktreeClaimRequest{
		OpID: "wt-op-1", WorkID: "work-w", ProjectID: "project-w",
		BaseSHA:      git.branches["main"],
		PrincipalRef: "principal-1", RequestID: "req-1",
		ExpectedVersion: 2, Now: time.Unix(10, 0).UTC(), Runner: git,
	}
}

func claimBranch() string { return "work/work-w" }

func claimPath(s *Store) string {
	return filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-w", "work-w")
}

func TestClaimWorktreeCreatesVerifiesAndFolds(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	req.SessionRef = "ses-claim"
	result, err := s.ClaimWorktree(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Entry.State != worktreeEntryActive || result.Entry.Branch != claimBranch() || result.Entry.ClaimOpID != "wt-op-1" || result.Entry.OccupantSessionRef != "ses-claim" {
		t.Fatalf("entry=%+v", result.Entry)
	}
	if result.Entry.SetID != WorktreeSetID("work-w") {
		t.Fatalf("set id=%q", result.Entry.SetID)
	}
	if git.countCalls("worktree add") != 1 {
		t.Fatalf("expected exactly one native add, got %d", git.countCalls("worktree add"))
	}
	entries, err := s.WorktreeEntries(context.Background(), "work-w")
	if err != nil || len(entries) != 1 || entries[0].State != worktreeEntryActive {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
}

func TestClaimWorktreeReconcilesInterruptedCreateWithoutSecondWorktree(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)

	// Simulate an interruption after the claim row but before Concord could
	// append the verified locator: git created the worktree, the fold never
	// ran.
	if err := s.insertPendingClaim(req, git.repoRoot); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(s.Path()), "worktrees", req.ProjectID, req.WorkID)
	if _, err := git.Run(context.Background(), git.repoRoot, "worktree", "add", path, "-b", claimBranch(), req.BaseSHA); err != nil {
		t.Fatal(err)
	}

	result, err := s.ClaimWorktree(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reconciled {
		t.Fatal("expected reconciled=true")
	}
	if result.Entry.State != worktreeEntryActive {
		t.Fatalf("entry=%+v", result.Entry)
	}
	if git.countCalls("worktree add") != 1 {
		t.Fatalf("reconciliation must not create a second worktree, saw %d adds", git.countCalls("worktree add"))
	}
}

func TestClaimWorktreeRetryFromPendingWithoutNativeCreateProbesFirst(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	if err := s.insertPendingClaim(req, git.repoRoot); err != nil {
		t.Fatal(err)
	}
	// Nothing was created; the retry probes, finds nothing, then creates.
	result, err := s.ClaimWorktree(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Entry.State != worktreeEntryActive {
		t.Fatalf("entry=%+v", result.Entry)
	}
	if git.countCalls("worktree add") != 1 {
		t.Fatalf("expected one add after probe-miss, got %d", git.countCalls("worktree add"))
	}
}

func TestClaimWorktreeReusesOrphanedBranchAtPinnedBase(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	git.branches[claimBranch()] = req.BaseSHA

	result, err := s.ClaimWorktree(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Entry.State != worktreeEntryActive {
		t.Fatalf("entry=%+v", result.Entry)
	}
	if git.countCalls("worktree add") != 1 {
		t.Fatalf("expected one native add, got %d", git.countCalls("worktree add"))
	}
	for _, call := range git.calls {
		if strings.Join(call[1:], " ") == "worktree add "+claimPath(s)+" "+claimBranch() {
			return
		}
	}
	t.Fatalf("worktree add did not reuse the existing branch: %v", git.calls)
}

func TestClaimWorktreeRefusesDivergentExistingBranch(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	git.branches[claimBranch()] = strings.Repeat("b", 40)

	_, err := s.ClaimWorktree(context.Background(), req)
	if failureKind(err) != KindProjectionConflict {
		t.Fatalf("err=%v, want projection conflict", err)
	}
	if git.countCalls("worktree add") != 0 {
		t.Fatalf("divergent branch must not be attached, saw %d adds", git.countCalls("worktree add"))
	}
}

func TestClaimWorktreeRefusesSecondActiveAndIntentMismatch(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	_, err := s.ClaimWorktree(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	second := req
	second.OpID = "wt-op-2"
	if _, err := s.ClaimWorktree(context.Background(), second); err == nil {
		t.Fatal("second active worktree for one Project must be refused")
	}
	mismatched := req
	mismatched.BaseSHA = strings.Repeat("b", 40)
	if _, err := s.ClaimWorktree(context.Background(), mismatched); err == nil {
		t.Fatal("retry with different base intent must be refused")
	}
}

// addFixtureProject creates a Project in product-w, the membership invariant
// the fold demands. The fixture's product holds version 2 after project-w.
func addFixtureProject(t *testing.T, s *Store, id string) {
	t.Helper()
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{
		locatorProjectEvent(id),
		{EventID: "membership-product-w-" + id, Kind: "product_project.added", SubjectType: SubjectProduct, SubjectID: "product-w", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"product_id":"product-w","project_id":"` + id + `","role":"secondary","reason":"fixture","expected_version":2,"resulting_version":3}`)},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProject, id): 0, VersionRef(SubjectProduct, "product-w"): 2}}); err != nil {
		t.Fatal(err)
	}
}

// secondWorktreeProject adds project-w2 on its own repository and gives work-w
// a secondary membership in it, so one work item holds one Project per
// repository (CD-0008 D1). Returns project-w2's canonical path.
func secondWorktreeProject(t *testing.T, s *Store, git *fakeWorktreeGit) string {
	t.Helper()
	addFixtureProject(t, s, "project-w2")
	repoRoot := t.TempDir()
	if err := s.AddProjectLocator(context.Background(), "project-w2", ProjectLocator{ID: "path-w2", Kind: LocatorCanonicalPath, Value: repoRoot}, 1); err != nil {
		t.Fatal(err)
	}
	git.addRepository(repoRoot)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{{EventID: "work-w-memberships-w2", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "operator", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"memberships":[{"project_id":"project-w","role":"primary"},{"project_id":"project-w2","role":"secondary"}],"expected_version":2,"resulting_version":3}`)}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-w"): 2}}); err != nil {
		t.Fatal(err)
	}
	return repoRoot
}

// The branch slot is unique per repository, not globally: two Projects of one
// work item derive the same branch work/<work_id> in two repositories, and
// both claims must hold (CD-0008 D1; CD-0151 D2 scoped to the repository).
func TestStoreTwoProjectClaimsConcurrent(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	secondRoot := secondWorktreeProject(t, s, git)
	ctx := context.Background()
	first := baseClaim(git)
	first.ExpectedVersion = 3
	if _, err := s.ClaimWorktree(ctx, first); err != nil {
		t.Fatalf("first Project claim: %v", err)
	}
	second := first
	second.ProjectID = "project-w2"
	second.OpID = "wt-op-2"
	second.RequestID = "req-2"
	second.ExpectedVersion = 4
	result, err := s.ClaimWorktree(ctx, second)
	if err != nil {
		t.Fatalf("second per-Project claim in another repository refused: %v", err)
	}
	if result.Entry.ProjectID != "project-w2" || result.Entry.Branch != claimBranch() || result.Entry.State != worktreeEntryActive {
		t.Fatalf("entry=%+v", result.Entry)
	}
	if result.Entry.RepositoryID != secondRoot {
		t.Fatalf("second claim repository=%q, want %q", result.Entry.RepositoryID, secondRoot)
	}
	entries, err := s.WorktreeEntries(ctx, "work-w")
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
	for _, entry := range entries {
		if entry.State != worktreeEntryActive {
			t.Fatalf("entry=%+v", entry)
		}
	}
}

// One repository cannot stand behind two Projects: the canonical-path locator
// is globally unique, so the same-repo cross-project claim is refused before
// any worktree identity is derived.
func TestStoreSameRepoCrossProjectRefusal(t *testing.T) {
	t.Parallel()
	s, _, repoRoot := worktreeFixture(t)
	addFixtureProject(t, s, "project-w2")
	err := s.AddProjectLocator(context.Background(), "project-w2", ProjectLocator{ID: "path-w2", Kind: LocatorCanonicalPath, Value: repoRoot}, 1)
	if failureKind(err) != KindMembershipConflict {
		t.Fatalf("err=%v, want membership conflict for the shared repository locator", err)
	}
}

// A claim that loses the race for its repository's branch slot is a typed
// conflict: the store classifies the unique-index refusal instead of
// reporting a retryable storage failure.
func TestStoreClaimViolationTypedConflict(t *testing.T) {
	t.Parallel()
	s, git, repoRoot := worktreeFixture(t)
	stamp := time.Unix(9, 0).UTC().Format(time.RFC3339Nano)
	// The concurrent winner's committed row: it appeared after this claim
	// passed every check it can see, so the fixture writes it directly.
	if _, err := s.db.Exec(`INSERT INTO worktree_claims(op_id,work_id,project_id,set_id,repository_id,pinned_branch,pinned_base_sha,pinned_path,state,principal_ref,request_id,observed_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"wt-ghost", "work-w", "project-ghost", WorktreeSetID("work-w"), repoRoot, claimBranch(), git.branches["main"], filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-ghost", "work-w"), worktreeStatePending, "principal-1", "req-ghost", stamp, stamp); err != nil {
		t.Fatal(err)
	}
	req := baseClaim(git)
	req.OpID = "wt-op-9"
	req.RequestID = "req-9"
	_, err := s.ClaimWorktree(context.Background(), req)
	if failureKind(err) != KindProjectionConflict {
		t.Fatalf("err=%v, want projection conflict for the held branch slot", err)
	}
}

func TestClaimWorktreeVerifiedReplayIsIdempotent(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	first, err := s.ClaimWorktree(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	addsBefore := git.countCalls("worktree add")
	replay, err := s.ClaimWorktree(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Reconciled || replay.Entry.SetID != first.Entry.SetID {
		t.Fatalf("replay=%+v", replay)
	}
	if git.countCalls("worktree add") != addsBefore {
		t.Fatal("verified replay must not touch git again")
	}
}

func TestReclaimWorktreeUsesRemoteDurabilityFacts(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	claimed, err := s.ClaimWorktree(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	reclaim := WorktreeReclaimRequest{WorkID: "work-w", ProjectID: "project-w", DefaultRef: "origin/main", PrincipalRef: "principal-1", RequestID: "req-2", ExpectedVersion: 3, Now: time.Unix(20, 0).UTC(), Runner: git, ObservedSessionDirectories: emptySessionObservation(), ObservedProjectID: "project-w"}

	git.dirty[claimed.Entry.Path] = true
	if _, err := s.ReclaimWorktree(context.Background(), reclaim); err == nil || !strings.Contains(err.Error(), "dirty") {
		t.Fatalf("dirty tree must be refused, got %v", err)
	}
	git.dirty[claimed.Entry.Path] = false

	// A local-only commit is the sole copy and must remain in the worktree.
	git.unpushed[claimBranch()] = 1
	if _, err := s.ReclaimWorktree(context.Background(), reclaim); err == nil || !strings.Contains(err.Error(), "not reachable from remote refs") {
		t.Fatalf("local-only commit must be refused, got %v", err)
	}
	git.unpushed[claimBranch()] = 0
	// The branch can conflict when replayed onto main and still be safe to
	// remove because a remote ref retains its tip.
	git.mergeConflict = true

	entry, err := s.ReclaimWorktree(context.Background(), reclaim)
	if err != nil {
		t.Fatal(err)
	}
	if entry.State != worktreeEntryReclaimed {
		t.Fatalf("entry=%+v", entry)
	}
	if _, still := git.worktrees[claimed.Entry.Path]; still {
		t.Fatal("native worktree was not removed")
	}
	if _, still := git.branches[claimBranch()]; still {
		t.Fatal("reclaimed branch was not deleted")
	}
	if git.countCalls("merge-tree") != 0 {
		t.Fatal("durability gate must not replay the branch onto the default ref")
	}
	replay, err := s.ReclaimWorktree(context.Background(), reclaim)
	if err != nil || replay.State != worktreeEntryReclaimed {
		t.Fatalf("same-generation reclaim replay=%+v err=%v", replay, err)
	}
	// A second claim after reclamation is allowed.
	third := baseClaim(git)
	third.OpID = "wt-op-3"
	third.ExpectedVersion = 4
	if _, err := s.ClaimWorktree(context.Background(), third); err != nil {
		t.Fatalf("re-claim after reclaim failed: %v", err)
	}
	stalePayload := jsonRaw(`{"expected_version":5,"resulting_version":6,"set_id":"` + WorktreeSetID("work-w") + `","project_id":"project-w","claim_op_id":"wt-op-1","git_facts":{}}`)
	stale := Event{EventID: "wt-op-1:stale-replay", Kind: "work.worktree_reclaimed", SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "principal-1", OccurredAt: time.Unix(30, 0).UTC(), PayloadVersion: 1, Payload: stalePayload}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{stale}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-w"): 5}}); err == nil {
		t.Fatal("a reclaim event from an older claim generation must be refused")
	}
	entries, err := s.WorktreeEntries(context.Background(), "work-w")
	if err != nil || len(entries) != 1 || entries[0].ClaimOpID != "wt-op-3" || entries[0].State != worktreeEntryActive {
		t.Fatalf("stale replay changed the later claim: entries=%+v err=%v", entries, err)
	}
	secondReclaim := reclaim
	secondReclaim.RequestID = "req-3"
	secondReclaim.ExpectedVersion = 5
	secondReclaim.Now = time.Unix(40, 0).UTC()
	if _, err := s.ReclaimWorktree(context.Background(), secondReclaim); err != nil {
		t.Fatalf("second generation reclaim failed: %v", err)
	}
	rows, err := s.db.Query(`SELECT event_id FROM domain_events WHERE kind='work.worktree_reclaimed' ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var eventIDs []string
	for rows.Next() {
		var eventID string
		if err := rows.Scan(&eventID); err != nil {
			t.Fatal(err)
		}
		eventIDs = append(eventIDs, eventID)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(eventIDs) != 2 || eventIDs[0] == eventIDs[1] || !strings.Contains(eventIDs[0], "wt-op-1") || !strings.Contains(eventIDs[1], "wt-op-3") {
		t.Fatalf("reclaim events=%v, want one event per claim generation", eventIDs)
	}
}

func TestReclaimWorktreeReplaysVersionOnePayload(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	if _, err := s.ClaimWorktree(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	event := Event{
		EventID: "legacy-worktree-reclaim",
		Kind:    "work.worktree_reclaimed", SubjectType: SubjectWorkItem, SubjectID: "work-w",
		Actor: "principal-1", OccurredAt: time.Unix(20, 0).UTC(), PayloadVersion: 1,
		Payload: jsonRaw(`{"expected_version":3,"resulting_version":4,"set_id":"` + WorktreeSetID("work-w") + `","project_id":"project-w","git_facts":{}}`),
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-w"): 3}}); err != nil {
		t.Fatalf("version-one reclaim replay failed: %v", err)
	}
	entries, err := s.WorktreeEntries(context.Background(), "work-w")
	if err != nil || len(entries) != 1 || entries[0].State != worktreeEntryReclaimed {
		t.Fatalf("entries after version-one replay=%+v err=%v", entries, err)
	}
}

// TestReclaimWorktreeRefusesOccupiedWorktree pins the stored occupancy gate.
func TestReclaimWorktreeRefusesOccupiedWorktree(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	req.SessionRef = "ses_live"
	claimed, err := s.ClaimWorktree(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	reclaim := WorktreeReclaimRequest{
		WorkID: "work-w", ProjectID: "project-w", DefaultRef: "origin/main",
		PrincipalRef: "principal-1", RequestID: "req-occupied", ExpectedVersion: 3,
		Now: time.Unix(20, 0).UTC(), Runner: git,
	}

	_, err = s.ReclaimWorktree(context.Background(), reclaim)
	if err == nil {
		t.Fatal("a recorded occupant must refuse the removal")
	}
	failure, ok := err.(*Failure)
	if !ok || failure.Kind != KindWorktreeOwnershipConflict {
		t.Fatalf("err=%v, want worktree_ownership_conflict", err)
	}
	if !strings.Contains(failure.Detail, "ses_live") || !strings.Contains(failure.Detail, claimed.Entry.Path) {
		t.Fatalf("refusal %q must name the session and the worktree", failure.Detail)
	}
	if _, still := git.worktrees[claimed.Entry.Path]; !still {
		t.Fatal("a refused removal must leave the native worktree in place")
	}
}

// A host observation without Project scope does not affect the authoritative
// Concord occupancy projection.
func TestReclaimWorktreeIgnoresUnscopedOccupancyObservation(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	claimed, err := s.ClaimWorktree(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.ReclaimWorktree(context.Background(), WorktreeReclaimRequest{
		WorkID: "work-w", ProjectID: "project-w", DefaultRef: "origin/main",
		PrincipalRef: "principal-1", RequestID: "req-unscoped", ExpectedVersion: 3,
		Now: time.Unix(20, 0).UTC(), Runner: git,
		ObservedSessionDirectories: &[]SessionDirectory{{SessionRef: "ses-live", Directory: claimed.Entry.Path}},
	})
	if err != nil {
		t.Fatalf("an unscoped observation must not block an unoccupied worktree: %v", err)
	}
	if _, still := git.worktrees[claimed.Entry.Path]; still {
		t.Fatal("an unoccupied worktree must be removed")
	}
	entries, entriesErr := s.WorktreeEntries(context.Background(), "work-w")
	if entriesErr != nil || len(entries) != 1 || entries[0].State != worktreeEntryReclaimed {
		t.Fatalf("entries=%+v err=%v, want the claim reclaimed", entries, entriesErr)
	}
}

// A recorded occupant the host attests is gone releases its occupancy: the
// observation names no live session with the occupant's ref and no live
// directory inside the worktree, so the removal cannot strand anyone. The
// absent observation still refuses, because it attests nothing.
func TestReclaimWorktreeReleasesDeadOccupantByObservation(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	req.SessionRef = "ses_dead"
	claimed, err := s.ClaimWorktree(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	seedWorktreeLifecycle(t, s, "work-w", "completed", 3)

	_, err = s.ReclaimWorktree(context.Background(), WorktreeReclaimRequest{
		WorkID: "work-w", ProjectID: "project-w", DefaultRef: "origin/main",
		PrincipalRef: "principal-1", RequestID: "req-dead-absent", ExpectedVersion: 4,
		Now: time.Unix(20, 0).UTC(), Runner: git,
	})
	failure, ok := err.(*Failure)
	if !ok || failure.Kind != KindWorktreeOwnershipConflict {
		t.Fatalf("an absent observation attests nothing, so the recorded occupant must still refuse: err=%v", err)
	}
	if _, still := git.worktrees[claimed.Entry.Path]; !still {
		t.Fatal("a refused removal must leave the native worktree in place")
	}

	entry, err := s.ReclaimWorktree(context.Background(), WorktreeReclaimRequest{
		WorkID: "work-w", ProjectID: "project-w", DefaultRef: "origin/main",
		PrincipalRef: "principal-1", RequestID: "req-dead-released", ExpectedVersion: 4,
		Now: time.Unix(20, 0).UTC(), Runner: git,
		ObservedSessionDirectories: &[]SessionDirectory{{SessionRef: "ses_other", Directory: filepath.Join(t.TempDir(), "unrelated")}},
	})
	if err != nil {
		t.Fatalf("a host observation naming no live session must release the dead occupant: %v", err)
	}
	if entry.State != worktreeEntryReclaimed {
		t.Fatalf("entry=%+v, want reclaimed", entry)
	}
	if _, still := git.worktrees[claimed.Entry.Path]; still {
		t.Fatal("the released worktree must be removed")
	}
	if _, still := git.branches[claimBranch()]; still {
		t.Fatal("the reclaimed branch was not deleted")
	}
}

// The release path keeps every strand-guard for live state: an observation
// naming the recorded occupant anywhere refuses, and so does an observation
// naming any other live session inside the worktree.
func TestReclaimWorktreeKeepsLiveOccupantRefusal(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	req.SessionRef = "ses_live"
	claimed, err := s.ClaimWorktree(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	seedWorktreeLifecycle(t, s, "work-w", "completed", 3)

	for name, observed := range map[string][]SessionDirectory{
		"occupant live in the worktree": {{SessionRef: "ses_live", Directory: claimed.Entry.Path}},
		"occupant live elsewhere":       {{SessionRef: "ses_live", Directory: filepath.Join(t.TempDir(), "elsewhere")}},
		"other session inside":          {{SessionRef: "ses_other", Directory: filepath.Join(claimed.Entry.Path, "nested")}},
	} {
		_, err := s.ReclaimWorktree(context.Background(), WorktreeReclaimRequest{
			WorkID: "work-w", ProjectID: "project-w", DefaultRef: "origin/main",
			PrincipalRef: "principal-1", RequestID: "req-live-" + name, ExpectedVersion: 4,
			Now: time.Unix(20, 0).UTC(), Runner: git,
			ObservedSessionDirectories: &observed,
		})
		failure, ok := err.(*Failure)
		if !ok || failure.Kind != KindWorktreeOwnershipConflict {
			t.Fatalf("%s: err=%v, want worktree_ownership_conflict", name, err)
		}
		if !strings.Contains(failure.Detail, "ses_live") || !strings.Contains(failure.Detail, claimed.Entry.Path) {
			t.Fatalf("%s: refusal %q must name the occupant and the worktree", name, failure.Detail)
		}
	}
	if _, still := git.worktrees[claimed.Entry.Path]; !still {
		t.Fatal("no refused attempt may remove the worktree")
	}
}

// The destroy tier threads the same observation: a dead occupant releases
// without an operator approval, while the approval route stays available.
func TestDestroyWorktreeReleasesDeadOccupantByObservation(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	req.SessionRef = "ses_dead"
	claimed, err := s.ClaimWorktree(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	seedWorktreeLifecycle(t, s, "work-w", "completed", 3)

	entry, err := s.DestroyWorktree(context.Background(), WorktreeDestroyRequest{
		WorkID: "work-w", ProjectID: "project-w", DefaultRef: "origin/main",
		ExpectedVersion: 4, PrincipalRef: "principal-1", RequestID: "destroy-dead",
		Now: time.Unix(30, 0).UTC(), Runner: git,
		ObservedSessionDirectories: &[]SessionDirectory{{SessionRef: "ses_other", Directory: filepath.Join(t.TempDir(), "unrelated")}},
	})
	if err != nil {
		t.Fatalf("a dead occupant must release on destroy without operator approval: %v", err)
	}
	if entry.State != worktreeEntryReclaimed {
		t.Fatalf("entry=%+v, want reclaimed", entry)
	}
	if _, still := git.worktrees[claimed.Entry.Path]; still {
		t.Fatal("the released worktree must be removed")
	}
}

// TestDestroyRefusesOccupiedWorktreeDespiteApproval pins that the destructive
// tier's operator approval does not reach the occupancy gate. The approval
// covers discarding the clean-tree and durable-branch gates, which protect
// committed and uncommitted work. It does not authorize stranding a session.
func TestDestroyRefusesOccupiedWorktreeDespiteApproval(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	req.SessionRef = "ses_live"
	claimed, err := s.ClaimWorktree(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	seedWorktreeLifecycle(t, s, "work-w", "completed", 3)
	_, err = s.DestroyWorktree(context.Background(), WorktreeDestroyRequest{
		WorkID: "work-w", ProjectID: "project-w", DefaultRef: "origin/main",
		ExpectedVersion: 4, PrincipalRef: "principal-1", RequestID: "destroy-occupied",
		Now: time.Unix(30, 0).UTC(), Runner: git,
		OperatorApprovalRef: "approval:destroy-forced",
		Destructive:         true,
	})
	if err == nil {
		t.Fatal("a destructive destroy must still refuse an occupied worktree")
	}
	failure, ok := err.(*Failure)
	if !ok || failure.Kind != KindWorktreeOwnershipConflict {
		t.Fatalf("err=%v, want worktree_ownership_conflict", err)
	}
	if _, still := git.worktrees[claimed.Entry.Path]; !still {
		t.Fatal("a refused destroy must leave the native worktree in place")
	}
}

func TestDestroyReleasesRecordedStaleOccupancyWithApproval(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	req.SessionRef = "ses_stale"
	claimed, err := s.ClaimWorktree(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	seedWorktreeLifecycle(t, s, "work-w", "completed", 3)
	entry, err := s.DestroyWorktree(context.Background(), WorktreeDestroyRequest{
		WorkID: "work-w", ProjectID: "project-w", DefaultRef: "origin/main",
		ExpectedVersion: 4, PrincipalRef: "principal-1", RequestID: "destroy-stale-occupancy",
		Now: time.Unix(30, 0).UTC(), Runner: git,
		OperatorApprovalRef: "approval:destroy-stale", Destructive: true, ReleaseOccupancy: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.State != worktreeEntryReclaimed || entry.OccupantSessionRef != "" {
		t.Fatalf("entry=%+v, want reclaimed and unoccupied", entry)
	}
	if _, still := git.worktrees[claimed.Entry.Path]; still {
		t.Fatal("native worktree was not removed")
	}
}

// TestReclaimAbsentWorktreeIgnoresOccupancy pins that the occupancy gate never
// blocks stale-claim recovery. When the native worktree is already gone, the
// reclamation only reconciles the projection: there is no directory left to
// remove and no session left to strand.
func TestReclaimAbsentWorktreeIgnoresOccupancy(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	if _, err := s.ClaimWorktree(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	delete(git.worktrees, claimPath(s))
	entry, err := s.ReclaimWorktree(context.Background(), WorktreeReclaimRequest{
		WorkID: "work-w", ProjectID: "project-w", DefaultRef: "origin/main",
		PrincipalRef: "principal-1", RequestID: "req-absent", ExpectedVersion: 3,
		Now: time.Unix(20, 0).UTC(), Runner: git,
		ObservedSessionDirectories: &[]SessionDirectory{{SessionRef: "ses_live", Directory: claimPath(s)}},
		ObservedProjectID:          "project-w",
	})
	if err != nil {
		t.Fatalf("an absent worktree must reconcile, got %v", err)
	}
	if entry.State != worktreeEntryReclaimed {
		t.Fatalf("entry=%+v", entry)
	}
}

// TestReclaimWorktreeAcceptsSquashMergedBranch pins the merged-ness test to
// content rather than commit reachability. A squash merge rewrites the branch's
// commits into one new commit on the default ref, so the branch tip never
// becomes an ancestor of it. Where squash is the only permitted merge method,
// an ancestry probe refuses every branch that actually merged and no worktree
// can ever be reclaimed (issue #628).
func TestReclaimWorktreeAcceptsSquashMergedBranch(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	if _, err := s.ClaimWorktree(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	// The default ref advances to a new commit carrying the branch's content.
	// The branch tip does not move and is not an ancestor of that commit.
	const squashedTree = "squashed-content-tree"
	git.branches["main"] = strings.Repeat("c", 40)
	git.content["main"] = squashedTree
	git.branches[claimBranch()] = strings.Repeat("b", 40)
	git.content[claimBranch()] = squashedTree

	reclaim := WorktreeReclaimRequest{WorkID: "work-w", ProjectID: "project-w", DefaultRef: "origin/main", PrincipalRef: "principal-1", RequestID: "req-2", ExpectedVersion: 3, Now: time.Unix(20, 0).UTC(), Runner: git, ObservedSessionDirectories: emptySessionObservation(), ObservedProjectID: "project-w"}
	entry, err := s.ReclaimWorktree(context.Background(), reclaim)
	if err != nil {
		t.Fatalf("a squash-merged branch must reclaim, got %v", err)
	}
	if entry.State != worktreeEntryReclaimed {
		t.Fatalf("entry=%+v", entry)
	}
	if _, still := git.worktrees[claimPath(s)]; still {
		t.Fatal("native worktree was not removed")
	}
}

func TestWorktreeEntriesRebuildFromLog(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	if _, err := s.ClaimWorktree(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	entries, err := s.WorktreeEntries(context.Background(), "work-w")
	if err != nil || len(entries) != 1 || entries[0].State != worktreeEntryActive || entries[0].ClaimOpID != "wt-op-1" {
		t.Fatalf("entries after rebuild=%+v err=%v", entries, err)
	}
}

// insertPendingClaim writes the durable claim row without running git, the
// state an interrupted operation leaves behind. repoRoot is the Project's
// canonical-path locator value, the repository identity the claim pins.
func (s *Store) insertPendingClaim(req WorktreeClaimRequest, repoRoot string) error {
	_, err := s.db.Exec(`INSERT INTO worktree_claims(op_id,work_id,project_id,set_id,repository_id,pinned_branch,pinned_base_sha,pinned_path,state,principal_ref,request_id,observed_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		req.OpID, req.WorkID, req.ProjectID, WorktreeSetID(req.WorkID), repoRoot, "work/"+req.WorkID, req.BaseSHA, filepath.Join(filepath.Dir(s.Path()), "worktrees", req.ProjectID, req.WorkID), worktreeStatePending, req.PrincipalRef, req.RequestID, req.Now.Format(time.RFC3339Nano), req.Now.Format(time.RFC3339Nano))
	return err
}

// auditWork seeds one extra work item in the worktree fixture and claims it
// at the canonical audit path under the store's worktree root. The fake
// runner registers the worktree without touching the real filesystem, so the
// test controls presence on disk directly with mkdir.
func auditWork(t *testing.T, s *Store, git *fakeWorktreeGit, workID string, onDisk bool) string {
	t.Helper()
	ctx := context.Background()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		{EventID: workID + "-create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 2, Payload: jsonRaw(`{"work_kind":"task","title":"Audit ` + workID + `","priority":1}`)},
		{EventID: workID + "-membership", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"memberships":[{"project_id":"project-w","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 0}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-w", workID)
	req := WorktreeClaimRequest{
		OpID: "wt-" + workID, WorkID: workID, ProjectID: "project-w",
		BaseSHA:      git.branches["main"],
		PrincipalRef: "principal-1", RequestID: "req-" + workID,
		ExpectedVersion: 2, Now: time.Unix(10, 0).UTC(), Runner: git,
	}
	if _, err := s.ClaimWorktree(ctx, req); err != nil {
		t.Fatal(err)
	}
	if onDisk {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func setWorktreeOccupant(t *testing.T, s *Store, workID, sessionRef string) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`UPDATE worktree_entries SET occupant_session_ref=? WHERE set_id=? AND state='active'`, sessionRef, WorktreeSetID(workID)); err != nil {
		t.Fatal(err)
	}
}

func auditRowsByClass(rows []WorktreeDrift) map[string][]WorktreeDrift {
	byClass := map[string][]WorktreeDrift{}
	for _, row := range rows {
		byClass[row.Class] = append(byClass[row.Class], row)
	}
	return byClass
}

func TestWorktreeAuditClassifiesEachDriftClass(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	ctx := context.Background()
	root := filepath.Join(filepath.Dir(s.Path()), "worktrees")

	// Healthy: a verified claim whose worktree exists on disk reports
	// nothing, because the branch holds commits beyond the default ref.
	auditWork(t, s, git, "work-healthy", true)
	git.ahead["work/work-healthy"] = 1
	// Stale claim only: the work moved past needed, so its gone worktree is a
	// claim problem, not a stranded work item.
	stalePath := auditWork(t, s, git, "work-stale", false)
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{{EventID: "work-stale-start", Kind: "work.transitioned", SubjectType: SubjectWorkItem, SubjectID: "work-stale", Actor: "operator", OccurredAt: time.Unix(20, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"from":"needed","to":"in_progress","reason":"started","expected_version":3,"resulting_version":4}`)}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-stale"): 3}}); err != nil {
		t.Fatal(err)
	}
	// Stale claim plus stranded work: still at needed with the worktree gone.
	gonePath := auditWork(t, s, git, "work-gone", false)
	// Orphan: a directory with no claim and no entry.
	orphanPath := filepath.Join(root, "project-w", "work-orphan")
	if err := os.MkdirAll(orphanPath, 0o755); err != nil {
		t.Fatal(err)
	}

	audit, err := s.WorktreeAudit(ctx, WorktreeAuditRequest{ProductID: "product-w", Limit: 100, Runner: git, DefaultRef: "origin/main"})
	if err != nil {
		t.Fatal(err)
	}
	if audit.Root != root {
		t.Fatalf("root=%q want %q", audit.Root, root)
	}
	byClass := auditRowsByClass(audit.Drift)

	orphans := byClass[WorktreeDriftOrphan]
	if len(orphans) != 1 || orphans[0].ProjectID != "project-w" || orphans[0].WorkID != "work-orphan" || orphans[0].Path != orphanPath || orphans[0].RecoveryAction != WorktreeRecoveryRemoveOrphan {
		t.Fatalf("orphan rows=%+v", orphans)
	}
	stale := byClass[WorktreeDriftStaleClaim]
	if len(stale) != 2 {
		t.Fatalf("stale rows=%+v", stale)
	}
	byWork := map[string]WorktreeDrift{}
	for _, row := range stale {
		byWork[row.WorkID] = row
	}
	for _, workID := range []string{"work-stale", "work-gone"} {
		row := byWork[workID]
		if row.ClaimState != worktreeStateVerified || row.RecoveryAction != WorktreeRecoveryReclaim {
			t.Fatalf("stale row for %s=%+v", workID, row)
		}
	}
	if byWork["work-stale"].Path != stalePath || byWork["work-gone"].Path != gonePath {
		t.Fatalf("stale paths=%+v", byWork)
	}
	stranded := byClass[WorktreeDriftStrandedNeeded]
	if len(stranded) != 1 || stranded[0].WorkID != "work-gone" || stranded[0].Path != gonePath || stranded[0].Lifecycle != "needed" || stranded[0].RecoveryAction != WorktreeRecoveryClaim {
		t.Fatalf("stranded rows=%+v", stranded)
	}
	for _, row := range audit.Drift {
		if row.WorkID == "work-healthy" {
			t.Fatalf("healthy worktree reported as drift: %+v", row)
		}
	}
}

// A pending claim is intent mid-creation, not verified fact: its missing
// directory is reconciled by retrying the claim, so the audit must not
// classify it as drift.
func TestWorktreeAuditIgnoresPendingClaims(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	if err := s.insertPendingClaim(req, git.repoRoot); err != nil {
		t.Fatal(err)
	}
	audit, err := s.WorktreeAudit(context.Background(), WorktreeAuditRequest{ProductID: "product-w", Limit: 100, Runner: git, DefaultRef: "origin/main"})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range audit.Drift {
		if row.Class == WorktreeDriftStaleClaim || row.Class == WorktreeDriftStrandedNeeded {
			t.Fatalf("pending claim classified as drift: %+v", row)
		}
	}
}

func TestWorktreeAuditChangesNoDurableState(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	ctx := context.Background()
	auditWork(t, s, git, "work-gone", false)
	if err := os.MkdirAll(filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-w", "work-orphan"), 0o755); err != nil {
		t.Fatal(err)
	}
	var claimsBefore, entriesBefore, eventsBefore int
	if err := s.db.QueryRow(`SELECT count(*) FROM worktree_claims`).Scan(&claimsBefore); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM worktree_entries`).Scan(&entriesBefore); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM domain_events`).Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}

	if _, err := s.WorktreeAudit(ctx, WorktreeAuditRequest{ProductID: "product-w", Limit: 100, Runner: git, DefaultRef: "origin/main"}); err != nil {
		t.Fatal(err)
	}

	var claimsAfter, entriesAfter, eventsAfter int
	if err := s.db.QueryRow(`SELECT count(*) FROM worktree_claims`).Scan(&claimsAfter); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM worktree_entries`).Scan(&entriesAfter); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM domain_events`).Scan(&eventsAfter); err != nil {
		t.Fatal(err)
	}
	if claimsAfter != claimsBefore || entriesAfter != entriesBefore || eventsAfter != eventsBefore {
		t.Fatalf("audit mutated state: claims %d->%d entries %d->%d events %d->%d", claimsBefore, claimsAfter, entriesBefore, entriesAfter, eventsBefore, eventsAfter)
	}
}

func TestWorktreeAuditRequiresProductScopeAndBoundsLimit(t *testing.T) {
	t.Parallel()
	s, _, _ := worktreeFixture(t)
	if _, err := s.WorktreeAudit(context.Background(), WorktreeAuditRequest{}); err == nil {
		t.Fatal("empty Product scope must be refused")
	}
	audit, err := s.WorktreeAudit(context.Background(), WorktreeAuditRequest{ProductID: "product-w", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(audit.Drift) > 1 {
		t.Fatalf("limit not applied: %d rows", len(audit.Drift))
	}
	if audit.Drift == nil {
		t.Fatal("drift must serialize as an empty array, not null")
	}
}

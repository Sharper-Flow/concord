package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeWorktreeGit models one repository: branches with heads, existing linked
// worktrees keyed by path, ancestry, dirtiness, and the default ref. It
// records every invocation so tests can assert what git was asked to do.
type fakeWorktreeGit struct {
	repoRoot   string
	branches   map[string]string // branch -> head sha
	worktrees  map[string]string // path -> branch
	dirty      map[string]bool   // path -> dirty
	content    map[string]string // branch -> tree content id; defaults to the head sha
	defaultRef string
	headBranch string // the ref HEAD resolves to; the fixture default is main
	failAdd    bool
	calls      [][]string
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
		repoRoot:   repoRoot,
		branches:   map[string]string{"main": strings.Repeat("a", 40)},
		worktrees:  map[string]string{},
		dirty:      map[string]bool{},
		content:    map[string]string{},
		headBranch: "main",
	}
}

func (g *fakeWorktreeGit) Run(_ context.Context, dir string, args ...string) ([]byte, error) {
	g.calls = append(g.calls, append([]string{dir}, args...))
	join := strings.Join(args, " ")
	switch {
	case join == "rev-parse --show-toplevel":
		if dir != g.repoRoot {
			return nil, fmt.Errorf("not the repository root")
		}
		return []byte(g.repoRoot + "\n"), nil
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
		if _, ok := g.worktrees[dir]; !ok {
			return nil, fmt.Errorf("not a worktree")
		}
		return []byte(filepath.Join(g.repoRoot, ".git") + "\n"), nil
	case strings.HasPrefix(join, "merge-tree --write-tree"):
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
	case strings.HasPrefix(join, "worktree add"):
		if g.failAdd {
			return nil, fmt.Errorf("native add failed")
		}
		parts := strings.Fields(join)
		path, branch, base := parts[2], parts[4], parts[5]
		if _, exists := g.worktrees[path]; exists {
			return nil, fmt.Errorf("worktree already exists")
		}
		g.worktrees[path] = branch
		g.branches[branch] = base
		return nil, nil
	case strings.HasPrefix(join, "worktree remove"):
		path := strings.Fields(join)[2]
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
	case join == "symbolic-ref refs/remotes/origin/HEAD":
		if g.defaultRef == "" {
			return nil, fmt.Errorf("no origin HEAD")
		}
		return []byte("refs/remotes/" + g.defaultRef + "\n"), nil
	case strings.HasPrefix(join, "rev-parse --verify ") && strings.HasSuffix(join, "^{commit}"):
		ref := strings.TrimSuffix(strings.TrimPrefix(join, "rev-parse --verify "), "^{commit}")
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
		Branch: "work/w-1", BaseSHA: git.branches["main"],
		Path:         filepath.Join(git.repoRoot, "..", "w-1"),
		PrincipalRef: "principal-1", RequestID: "req-1",
		ExpectedVersion: 2, Now: time.Unix(10, 0).UTC(), Runner: git,
	}
}

func TestClaimWorktreeCreatesVerifiesAndFolds(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	result, err := s.ClaimWorktree(context.Background(), baseClaim(git))
	if err != nil {
		t.Fatal(err)
	}
	if result.Entry.State != worktreeEntryActive || result.Entry.Branch != "work/w-1" || result.Entry.ClaimOpID != "wt-op-1" {
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
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)

	// Simulate an interruption after the claim row but before Concord could
	// append the verified locator: git created the worktree, the fold never
	// ran.
	if err := s.insertPendingClaim(req); err != nil {
		t.Fatal(err)
	}
	if _, err := git.Run(context.Background(), git.repoRoot, "worktree", "add", req.Path, "-b", req.Branch, req.BaseSHA); err != nil {
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
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	if err := s.insertPendingClaim(req); err != nil {
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

func TestClaimWorktreeRefusesSecondActiveAndIntentMismatch(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	if _, err := s.ClaimWorktree(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	second := req
	second.OpID = "wt-op-2"
	if _, err := s.ClaimWorktree(context.Background(), second); err == nil {
		t.Fatal("second active worktree for one Project must be refused")
	}
	mismatched := req
	mismatched.Branch = "work/different"
	if _, err := s.ClaimWorktree(context.Background(), mismatched); err == nil {
		t.Fatal("retry with different intent must be refused")
	}
}

func TestClaimWorktreeVerifiedReplayIsIdempotent(t *testing.T) {
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

func TestReclaimWorktreeDerivesFromGitFacts(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	if _, err := s.ClaimWorktree(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	reclaim := WorktreeReclaimRequest{WorkID: "work-w", ProjectID: "project-w", DefaultRef: "origin/main", PrincipalRef: "principal-1", RequestID: "req-2", ExpectedVersion: 3, Now: time.Unix(20, 0).UTC(), Runner: git}

	git.dirty[req.Path] = true
	if _, err := s.ReclaimWorktree(context.Background(), reclaim); err == nil || !strings.Contains(err.Error(), "dirty") {
		t.Fatalf("dirty tree must be refused, got %v", err)
	}
	git.dirty[req.Path] = false

	// Unmerged: the branch head moves beyond the default ref's history.
	git.branches["work/w-1"] = strings.Repeat("b", 40)
	if _, err := s.ReclaimWorktree(context.Background(), reclaim); err == nil || !strings.Contains(err.Error(), "not merged") {
		t.Fatalf("unmerged head must be refused, got %v", err)
	}
	git.branches["work/w-1"] = req.BaseSHA

	entry, err := s.ReclaimWorktree(context.Background(), reclaim)
	if err != nil {
		t.Fatal(err)
	}
	if entry.State != worktreeEntryReclaimed {
		t.Fatalf("entry=%+v", entry)
	}
	if _, still := git.worktrees[req.Path]; still {
		t.Fatal("native worktree was not removed")
	}
	// A second claim after reclamation is allowed.
	third := baseClaim(git)
	third.OpID = "wt-op-3"
	third.ExpectedVersion = 4
	if _, err := s.ClaimWorktree(context.Background(), third); err != nil {
		t.Fatalf("re-claim after reclaim failed: %v", err)
	}
}

// TestReclaimWorktreeAcceptsSquashMergedBranch pins the merged-ness test to
// content rather than commit reachability. A squash merge rewrites the branch's
// commits into one new commit on the default ref, so the branch tip never
// becomes an ancestor of it. Where squash is the only permitted merge method,
// an ancestry probe refuses every branch that actually merged and no worktree
// can ever be reclaimed (issue #628).
func TestReclaimWorktreeAcceptsSquashMergedBranch(t *testing.T) {
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
	git.branches["work/w-1"] = strings.Repeat("b", 40)
	git.content["work/w-1"] = squashedTree

	reclaim := WorktreeReclaimRequest{WorkID: "work-w", ProjectID: "project-w", DefaultRef: "origin/main", PrincipalRef: "principal-1", RequestID: "req-2", ExpectedVersion: 3, Now: time.Unix(20, 0).UTC(), Runner: git}
	entry, err := s.ReclaimWorktree(context.Background(), reclaim)
	if err != nil {
		t.Fatalf("a squash-merged branch must reclaim, got %v", err)
	}
	if entry.State != worktreeEntryReclaimed {
		t.Fatalf("entry=%+v", entry)
	}
	if _, still := git.worktrees[req.Path]; still {
		t.Fatal("native worktree was not removed")
	}
}

func TestWorktreeEntriesRebuildFromLog(t *testing.T) {
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
// state an interrupted operation leaves behind.
func (s *Store) insertPendingClaim(req WorktreeClaimRequest) error {
	_, err := s.db.Exec(`INSERT INTO worktree_claims(op_id,work_id,project_id,set_id,pinned_branch,pinned_base_sha,pinned_path,state,principal_ref,request_id,observed_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		req.OpID, req.WorkID, req.ProjectID, WorktreeSetID(req.WorkID), req.Branch, req.BaseSHA, req.Path, worktreeStatePending, req.PrincipalRef, req.RequestID, req.Now.Format(time.RFC3339Nano), req.Now.Format(time.RFC3339Nano))
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
		Branch: "work/" + workID, BaseSHA: git.branches["main"],
		Path:         path,
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

func auditRowsByClass(rows []WorktreeDrift) map[string][]WorktreeDrift {
	byClass := map[string][]WorktreeDrift{}
	for _, row := range rows {
		byClass[row.Class] = append(byClass[row.Class], row)
	}
	return byClass
}

func TestWorktreeAuditClassifiesEachDriftClass(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	ctx := context.Background()
	root := filepath.Join(filepath.Dir(s.Path()), "worktrees")

	// Healthy: a verified claim whose worktree exists on disk reports nothing.
	auditWork(t, s, git, "work-healthy", true)
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

	audit, err := s.WorktreeAudit(ctx, "product-w", 100)
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
	s, git, _ := worktreeFixture(t)
	path := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-w", "work-w")
	req := baseClaim(git)
	req.Path = path
	if err := s.insertPendingClaim(req); err != nil {
		t.Fatal(err)
	}
	audit, err := s.WorktreeAudit(context.Background(), "product-w", 100)
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

	if _, err := s.WorktreeAudit(ctx, "product-w", 100); err != nil {
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
	s, _, _ := worktreeFixture(t)
	if _, err := s.WorktreeAudit(context.Background(), "", 0); err == nil {
		t.Fatal("empty Product scope must be refused")
	}
	audit, err := s.WorktreeAudit(context.Background(), "product-w", 1)
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

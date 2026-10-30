package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

type fakeWorktreeGit struct {
	repoRoot          string
	extraRoots        map[string]bool
	worktreeRepos     map[string]string
	branches          map[string]string
	worktrees         map[string]string
	dirty             map[string]bool
	content           map[string]string
	defaultRef        string
	headBranch        string
	ahead             map[string]int
	unpushed          map[string]int
	mergeBaseFail     bool
	defaultCommits    []string
	commitPatchIDs    map[string]string
	mergeConflict     bool
	failFetch         bool
	addedRecordShards map[string][]string
	recordShardKinds  map[string]string
	calls             [][]string
}

func newFakeWorktreeGit(repoRoot string) *fakeWorktreeGit {
	return &fakeWorktreeGit{repoRoot: repoRoot, extraRoots: map[string]bool{}, worktreeRepos: map[string]string{}, branches: map[string]string{"main": strings.Repeat("a", 40)}, worktrees: map[string]string{}, dirty: map[string]bool{}, content: map[string]string{}, ahead: map[string]int{}, unpushed: map[string]int{}, commitPatchIDs: map[string]string{}, addedRecordShards: map[string][]string{}, recordShardKinds: map[string]string{}, headBranch: "main", defaultRef: "origin/main"}
}

func (g *fakeWorktreeGit) addRepository(root string) { g.extraRoots[root] = true }

func (g *fakeWorktreeGit) treeOf(ref string) string {
	if tree, ok := g.content[strings.TrimPrefix(ref, "origin/")]; ok {
		return tree
	}
	if tree, ok := g.content[g.revisionBranch(ref)]; ok {
		return tree
	}
	return g.resolveRef(ref)
}

func (g *fakeWorktreeGit) revisionBranch(rev string) string {
	name := strings.TrimPrefix(strings.TrimPrefix(rev, "refs/remotes/"), "origin/")
	if _, ok := g.branches[name]; ok {
		return name
	}
	var zero []string
	for branch, tip := range g.branches {
		if tip != rev {
			continue
		}
		if g.unpushed[branch] > 0 || g.ahead[branch] > 0 {
			return branch
		}
		zero = append(zero, branch)
	}
	if len(zero) > 0 {
		sort.Strings(zero)
		return zero[0]
	}
	return name
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
	case join == "rev-parse HEAD" || join == "rev-parse --verify HEAD^{commit}":
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
		if g.treeOf(from) == g.treeOf(into) {
			return []byte(g.treeOf(into) + "\n"), nil
		}
		return []byte(strings.Repeat("f", 40) + "\n"), nil
	case strings.HasSuffix(join, "^{tree}") && strings.HasPrefix(join, "rev-parse "):
		ref := strings.TrimSuffix(args[len(args)-1], "^{tree}")
		if _, known := g.branches[g.revisionBranch(ref)]; !known {
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
	case strings.HasPrefix(join, "merge-base "):
		if g.mergeBaseFail {
			return nil, fmt.Errorf("unrelated histories")
		}
		parts := strings.Fields(join)
		if len(parts) != 3 {
			return nil, fmt.Errorf("malformed merge-base")
		}
		for _, ref := range parts[1:3] {
			if _, ok := g.branches[g.revisionBranch(ref)]; !ok {
				return nil, errors.New("unknown ref " + ref)
			}
		}
		return []byte("merge-base\n"), nil
	case strings.HasPrefix(join, "rev-list --no-merges "):
		parts := strings.Fields(join)
		if len(parts) != 3 || !strings.Contains(parts[2], "..") {
			return nil, fmt.Errorf("malformed revision range")
		}
		if len(g.defaultCommits) == 0 {
			return nil, nil
		}
		return []byte(strings.Join(g.defaultCommits, "\n") + "\n"), nil
	case strings.HasPrefix(join, "rev-list --count ") && strings.HasSuffix(join, " --not --remotes"):
		branch := g.revisionBranch(strings.TrimSuffix(strings.TrimPrefix(join, "rev-list --count "), " --not --remotes"))
		return []byte(strconv.Itoa(g.unpushed[branch]) + "\n"), nil
	case strings.HasPrefix(join, "rev-list --count "):
		refs := strings.TrimPrefix(join, "rev-list --count ")
		dotdot := strings.Index(refs, "..")
		if dotdot < 0 {
			return nil, fmt.Errorf("malformed revision range")
		}
		return []byte(strconv.Itoa(g.ahead[g.revisionBranch(refs[dotdot+2:])]) + "\n"), nil
	case join == "status --porcelain" || join == "status --porcelain=v1 -z --untracked-files=all" || join == "diff HEAD":
		if g.dirty[dir] {
			return []byte("M file\n"), nil
		}
		return nil, nil
	case strings.HasPrefix(join, "diff --name-only --diff-filter=A "):
		parts := strings.Fields(join)
		if len(parts) < 6 || parts[5] != "--" {
			return nil, fmt.Errorf("malformed record diff")
		}
		return []byte(strings.Join(g.addedRecordShards[g.revisionBranch(parts[4])], "\n")), nil
	case strings.HasPrefix(join, "show "):
		ref := strings.TrimPrefix(join, "show ")
		rev, path, split := strings.Cut(ref, ":")
		if !split {
			return nil, fmt.Errorf("unmodelled blob %s", ref)
		}
		kind, ok := g.recordShardKinds[g.revisionBranch(rev)+":"+path]
		if !ok {
			return nil, fmt.Errorf("unmodelled blob %s", ref)
		}
		return []byte(`{"id":"probe","kind":"` + kind + `"}` + "\n"), nil
	case strings.HasPrefix(join, "diff "):
		parts := strings.Fields(join)
		if len(parts) != 3 {
			return nil, fmt.Errorf("malformed diff")
		}
		if g.treeOf(parts[1]) == g.treeOf(parts[2]) {
			return nil, nil
		}
		return []byte(g.netDiffPatchID(parts[1], parts[2]) + "\n"), nil
	case join == "symbolic-ref refs/remotes/origin/HEAD" || join == "symbolic-ref --quiet refs/remotes/origin/HEAD":
		if g.defaultRef == "" {
			return nil, fmt.Errorf("no origin HEAD")
		}
		return []byte("refs/remotes/" + g.defaultRef + "\n"), nil
	case join == "symbolic-ref --quiet HEAD":
		if g.headBranch == "" {
			return nil, exec.Command("false").Run()
		}
		return []byte("refs/heads/" + g.headBranch + "\n"), nil
	case strings.HasPrefix(join, "symbolic-ref --quiet refs/heads/"):
		return nil, exec.Command("false").Run()
	case strings.HasPrefix(join, "fetch --no-tags --no-recurse-submodules --refmap= origin +refs/heads/"):
		if g.failFetch {
			return nil, fmt.Errorf("unreachable origin")
		}
		return nil, nil
	case strings.HasPrefix(join, "show-ref --verify --quiet refs/heads/"):
		if _, exists := g.branches[strings.TrimPrefix(join, "show-ref --verify --quiet refs/heads/")]; !exists {
			return nil, exec.Command("false").Run()
		}
		return nil, nil
	case strings.HasPrefix(join, "rev-parse --verify ") && strings.HasSuffix(join, "^{commit}"):
		ref := strings.TrimPrefix(strings.TrimSuffix(strings.TrimPrefix(join, "rev-parse --verify "), "^{commit}"), "refs/heads/")
		if ref == "HEAD" {
			ref = g.headBranch
		}
		if sha := g.resolveRef(ref); sha != ref {
			return []byte(sha + "\n"), nil
		}
		return nil, fmt.Errorf("unknown ref")
	case strings.HasPrefix(join, "rev-parse --verify refs/heads/"):
		if sha, ok := g.branches[strings.TrimPrefix(join, "rev-parse --verify refs/heads/")]; ok {
			return []byte(sha + "\n"), nil
		}
		return nil, fmt.Errorf("unknown ref")
	case slices.Contains(args, "--name-only"):
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected git invocation: %s", join)
}

func (g *fakeWorktreeGit) RunStdin(_ context.Context, dir string, stdin []byte, args ...string) ([]byte, error) {
	g.calls = append(g.calls, append([]string{dir}, args...))
	switch strings.Join(args, " ") {
	case "patch-id --stable":
		return stdin, nil
	case "diff-tree --patch --stdin":
		var out []byte
		for _, sha := range strings.Fields(string(stdin)) {
			pid, ok := g.commitPatchIDs[sha]
			if !ok {
				return nil, errors.New("unmodelled commit " + sha)
			}
			out = append(out, []byte(pid+"\n")...)
		}
		return out, nil
	}
	return nil, fmt.Errorf("unexpected stdin git invocation: %s", strings.Join(args, " "))
}

func (g *fakeWorktreeGit) netDiffPatchID(from, to string) string {
	return "pid:" + g.treeOf(from) + ".." + g.treeOf(to)
}
func (g *fakeWorktreeGit) squashMergeIntoDefault(branch string) {
	sha := strings.Repeat("d", 39) + strconv.Itoa(len(g.defaultCommits))
	g.defaultCommits = append(g.defaultCommits, sha)
	g.commitPatchIDs[sha] = g.netDiffPatchID("merge-base", branch)
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
func (g *fakeWorktreeGit) divergeBranch(branch string, ahead, unpushed int) {
	g.ahead[branch] = ahead
	g.unpushed[branch] = unpushed
	g.branches[branch] = distinctTip(branch)
}
func distinctTip(branch string) string {
	sum := fnv.New32a()
	_, _ = sum.Write([]byte(branch))
	return fmt.Sprintf("d%039x", sum.Sum32())
}
func (g *fakeWorktreeGit) addBranchRecordShard(branch, path, kind string) {
	if kind == "" {
		delete(g.addedRecordShards, branch)
		return
	}
	g.addedRecordShards[branch] = append(g.addedRecordShards[branch], path)
	g.recordShardKinds[branch+":"+path] = kind
	g.branches[branch] = distinctTip(branch)
}

func worktreeFixture(t *testing.T) (*Store, *fakeWorktreeGit, string) {
	t.Helper()
	s := openTemp(t)
	ctx := context.Background()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{locatorProductEvent("product-w"), locatorProjectEvent("project-w"), locatorMembershipEvent("product-w", "project-w")}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product-w"): 0, VersionRef(SubjectProject, "project-w"): 0}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		{EventID: "work-w-create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion("work.created"), Payload: jsonRaw(`{"work_kind":"task","title":"Worktree Work","priority":1}`)},
		{EventID: "work-w-membership", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"memberships":[{"project_id":"project-w","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-w"): 0}}); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if err := s.AddProjectLocator(ctx, "project-w", ProjectLocator{ID: "path-w", Kind: LocatorCanonicalPath, Value: repo}, 1); err != nil {
		t.Fatal(err)
	}
	return s, newFakeWorktreeGit(repo), repo
}

func jsonRaw(s string) []byte { return []byte(s) }
func claimBranch() string     { return "work/work-w" }
func claimPath(s *Store) string {
	return filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-w", "work-w")
}

// recordHostWorktree seeds retained locators; it never calls a production ownership route.
func recordHostWorktree(t *testing.T, s *Store, git *fakeWorktreeGit, workID, projectID, claimID string) WorktreeEntry {
	t.Helper()
	repo := git.repoRoot
	path := filepath.Join(filepath.Dir(s.Path()), "worktrees", projectID, workID)
	branch := "work/" + workID
	entry := WorktreeEntry{SetID: WorktreeSetID(workID), ProjectID: projectID, ClaimOpID: claimID, Branch: branch, BaseSHA: git.branches["main"], Path: path, RepositoryID: repo, State: worktreeEntryActive, VerifiedAt: time.Unix(10, 0).UTC().Format(time.RFC3339Nano), GitFacts: json.RawMessage(`{}`)}
	git.worktrees[path], git.worktreeRepos[path], git.branches[branch] = branch, repo, entry.BaseSHA
	insertHostWorktreeRows(t, s, workID, entry)
	return entry
}

func insertHostWorktreeRows(t *testing.T, s *Store, workID string, e WorktreeEntry) {
	t.Helper()
	if e.SetID == "" {
		e.SetID = WorktreeSetID(workID)
	}
	if e.State == "" {
		e.State = worktreeEntryActive
	}
	if e.VerifiedAt == "" {
		e.VerifiedAt = time.Unix(10, 0).UTC().Format(time.RFC3339Nano)
	}
	if len(e.GitFacts) == 0 {
		e.GitFacts = json.RawMessage(`{}`)
	}
	if err := s.Transact(t.Context(), func(tr *Transaction) error {
		scope, err := beginWrite(t.Context(), tr.tx)
		if err != nil {
			return err
		}
		if _, err := tr.tx.ExecContext(t.Context(), `INSERT INTO worktree_claims(op_id,work_id,project_id,set_id,repository_id,pinned_branch,pinned_base_sha,pinned_path,state,principal_ref,request_id,observed_at,updated_at,member_id) VALUES(?,?,?,?,?,?,?,?,'verified','principal-1','host-fixture',?,?,?)`, e.ClaimOpID, workID, e.ProjectID, e.SetID, e.RepositoryID, e.Branch, e.BaseSHA, e.Path, e.VerifiedAt, e.VerifiedAt, e.MemberID); err != nil {
			return err
		}
		if _, err := tr.tx.ExecContext(t.Context(), `INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,git_facts,member_id) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, e.SetID, e.ProjectID, e.ClaimOpID, e.Branch, e.BaseSHA, e.Path, e.RepositoryID, e.State, e.VerifiedAt, string(e.GitFacts), e.MemberID); err != nil {
			return err
		}
		if _, err := tr.tx.ExecContext(t.Context(), `UPDATE work_items SET version=version+1 WHERE id=?`, workID); err != nil {
			return err
		}
		return scope.close(t.Context())
	}); err != nil {
		t.Fatal(err)
	}
}

func createHostWorktreeFixture(t *testing.T, s *Store, workID, projectID, claimID, base string) WorktreeEntry {
	t.Helper()
	repo, err := s.ProjectCanonicalPath(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if base == "" {
		base = runTestGit(t, repo, "rev-parse", "HEAD")
	}
	path := filepath.Join(filepath.Dir(s.Path()), "worktrees", projectID, workID)
	branch := "work/" + workID
	gitRunStore(t, repo, "worktree", "add", path, "-b", branch, base)
	entry := WorktreeEntry{ProjectID: projectID, ClaimOpID: claimID, Branch: branch, BaseSHA: base, Path: path, RepositoryID: repo}
	insertHostWorktreeRows(t, s, workID, entry)
	entry.SetID, entry.State = WorktreeSetID(workID), worktreeEntryActive
	return entry
}

func replaceHostWorktreeGeneration(t *testing.T, s *Store, path, claimID string) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1);
		UPDATE worktree_claims SET op_id=? WHERE op_id=(SELECT claim_op_id FROM worktree_entries WHERE path=?);
		UPDATE worktree_occupancy SET worktree_id=(SELECT set_id||':'||project_id||':'||? FROM worktree_entries WHERE path=?) WHERE worktree_id IN (SELECT set_id||':'||project_id||':'||claim_op_id FROM worktree_entries WHERE path=?);
		UPDATE worktree_entries SET claim_op_id=? WHERE path=?;
		DELETE FROM write_guard`, claimID, path, claimID, path, path, claimID, path); err != nil {
		t.Fatal(err)
	}
}

func setWorktreeOccupant(t *testing.T, s *Store, workID, sessionRef string) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); INSERT INTO worktree_occupancy(worktree_id,session_ref,recorded_at,host_pid,host_pid_start,has_process_identity) SELECT set_id||':'||project_id||':'||claim_op_id,?,?,NULL,NULL,0 FROM worktree_entries WHERE set_id=? AND state='active'; DELETE FROM write_guard`, sessionRef, time.Now().UTC().Format(time.RFC3339Nano), WorktreeSetID(workID)); err != nil {
		t.Fatal(err)
	}
}

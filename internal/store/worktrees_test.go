package store

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeWorktreeOriginURL is the remote URL the fixture repositories report
// for remote.origin.url: the bounded preflight fetches that URL directly, so
// the fake's fetch case matches on it.
const fakeWorktreeOriginURL = "https://freshness.invalid/repository.git"

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
	mergeBaseFail bool           // merge-base fails: the histories share no base
	// defaultCommits are the non-merge commit shas the default ref holds
	// since its merge base with the audited branch, and commitPatchIDs maps
	// each to the patch text diff-tree would emit for it. Together they
	// model the commit a squash merge left on the default ref (CD-0181).
	defaultCommits []string
	commitPatchIDs map[string]string
	mergeConflict  bool
	failAdd        bool
	// failFetch models an unreachable origin: the bounded preflight fetch
	// fails the way a transport failure does.
	failFetch bool
	// partialAdd models git leaving the tree directory and the requested new
	// branch behind before it reports a failed `worktree add`.
	partialAdd bool
	// addedRecordShards models the endpoint diff the unpublished-lesson
	// probe runs: branch -> record shard paths the branch tree adds beyond
	// the default tree, and recordShardKinds maps "<branch>:<path>" to the
	// committed record kind.
	addedRecordShards map[string][]string
	recordShardKinds  map[string]string
	calls             [][]string
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
	if tree, ok := g.content[g.revisionBranch(ref)]; ok {
		return tree
	}
	return g.resolveRef(ref)
}

// revisionBranch maps a revision the store's gates read onto the branch whose
// modeled state the query is about. CD-0212 D1 feeds every gate the observed
// tip SHA, not the branch name, so a tip resolves back through the branches
// that hold it. When several share one tip, the one with the non-zero
// modeled count is the one the fixture means: a branch with commits beyond a
// ref cannot share its tip with that ref in real git. The zero-state
// candidates answer zero either way, so a stable order keeps the model
// deterministic.
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

func newFakeWorktreeGit(repoRoot string) *fakeWorktreeGit {
	return &fakeWorktreeGit{
		repoRoot:          repoRoot,
		extraRoots:        map[string]bool{},
		worktreeRepos:     map[string]string{},
		branches:          map[string]string{"main": strings.Repeat("a", 40)},
		worktrees:         map[string]string{},
		dirty:             map[string]bool{},
		content:           map[string]string{},
		ahead:             map[string]int{},
		unpushed:          map[string]int{},
		commitPatchIDs:    map[string]string{},
		addedRecordShards: map[string][]string{},
		recordShardKinds:  map[string]string{},
		headBranch:        "main",
		defaultRef:        "origin/main",
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
		branch := g.revisionBranch(refs[dotdot+2:])
		return []byte(strconv.Itoa(g.ahead[branch]) + "\n"), nil
	case strings.HasPrefix(join, "worktree add"):
		if g.failAdd {
			return nil, fmt.Errorf("native add failed")
		}
		parts := strings.Fields(join)
		if g.partialAdd {
			if len(parts) == 6 && parts[3] == "-b" {
				g.branches[parts[4]] = parts[5]
			}
			if err := os.MkdirAll(parts[2], 0o755); err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("native add failed after creating state")
		}
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
	case strings.HasPrefix(join, "update-ref --no-deref -d refs/heads/"):
		// The pinned argv no-deref deletion (CD-0212 D4): one command whose
		// old-value argument refuses a ref that moved, exactly the
		// compare-and-delete git runs, deleting the named ref itself.
		fields := strings.Fields(join)
		if len(fields) != 5 {
			return nil, fmt.Errorf("malformed pinned delete: %s", join)
		}
		branch := strings.TrimPrefix(fields[3], "refs/heads/")
		sha, exists := g.branches[branch]
		if !exists {
			return nil, fmt.Errorf("cannot lock ref '%s': unable to resolve reference '%s'", fields[3], fields[3])
		}
		if sha != fields[4] {
			return nil, fmt.Errorf("cannot lock ref '%s': is at %s but expected %s", fields[3], sha, fields[4])
		}
		delete(g.branches, branch)
		return nil, nil
	case strings.HasPrefix(join, "update-ref --no-deref refs/heads/"):
		// The create-only argv restoration (CD-0212 D4): the zero old-value
		// refuses to overwrite a ref a concurrent actor already rebuilt.
		fields := strings.Fields(join)
		if len(fields) != 5 || strings.Trim(fields[4], "0") != "" {
			return nil, fmt.Errorf("malformed create-only restoration: %s", join)
		}
		branch := strings.TrimPrefix(fields[1], "refs/heads/")
		if _, exists := g.branches[branch]; exists {
			return nil, fmt.Errorf("cannot lock ref '%s': reference already exists", fields[1])
		}
		g.branches[branch] = fields[2]
		return nil, nil
	case join == "worktree list --porcelain":
		// The porcelain worktree list the checked-out protection reads: the
		// querying repository's own main checkout plus its linked worktrees
		// with their checked-out branches (CD-0212 D4). The fake shares
		// branch state across registered roots, so the listing is scoped to
		// the root that asked, exactly as git scopes it to one repository.
		// An entry whose branch never received a SHA reports git's all-zero
		// unborn HEAD, so the complete-inventory parse stays honest.
		var out strings.Builder
		porcelainHead := func(branch string) string {
			sha := g.resolveRef(branch)
			if len(sha) == 40 || len(sha) == 64 {
				return sha
			}
			return strings.Repeat("0", 40)
		}
		if dir == g.repoRoot || g.extraRoots[dir] {
			fmt.Fprintf(&out, "worktree %s\nHEAD %s\nbranch refs/heads/%s\n\n", dir, porcelainHead(g.headBranch), g.headBranch)
		}
		paths := make([]string, 0, len(g.worktrees))
		for path, root := range g.worktreeRepos {
			if root == dir {
				paths = append(paths, path)
			}
		}
		sort.Strings(paths)
		for _, path := range paths {
			branch := g.worktrees[path]
			fmt.Fprintf(&out, "worktree %s\nHEAD %s\nbranch refs/heads/%s\n\n", path, porcelainHead(branch), branch)
		}
		return []byte(out.String()), nil
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
	case strings.HasPrefix(join, "diff --name-only --diff-filter=A "):
		// diff --name-only --diff-filter=A <from> <to> -- <path>: the fake
		// names the record shards the branch tree adds beyond the default
		// tree, exactly the endpoint diff the unpublished-lesson probe runs.
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
		// diff <from> <to>: the fake's patch text encodes the pair of trees,
		// and its patch-id is that text, so equal diffs carry equal ids. An
		// empty tree pair produces an empty diff, as git does.
		parts := strings.Fields(join)
		if len(parts) != 3 {
			return nil, fmt.Errorf("malformed diff")
		}
		from, to := parts[1], parts[2]
		if g.treeOf(from) == g.treeOf(to) {
			return nil, nil
		}
		return []byte(g.netDiffPatchID(from, to) + "\n"), nil
	case join == "symbolic-ref refs/remotes/origin/HEAD" || join == "symbolic-ref --quiet refs/remotes/origin/HEAD":
		if g.defaultRef == "" {
			return nil, fmt.Errorf("no origin HEAD")
		}
		return []byte("refs/remotes/" + g.defaultRef + "\n"), nil
	case join == "symbolic-ref --quiet HEAD":
		// The repository's own checked-out HEAD: the fake models a branch
		// checkout, like the repositories the real surface runs against.
		if g.headBranch == "" {
			return nil, exec.Command("false").Run()
		}
		return []byte("refs/heads/" + g.headBranch + "\n"), nil
	case strings.HasPrefix(join, "symbolic-ref --quiet refs/heads/"):
		// Every branch the fake holds is a direct ref, so the symbolic probe
		// answers "not a symbolic ref" with git's own exit status 1, for a
		// missing ref exactly as for a direct one.
		return nil, exec.Command("false").Run()
	case strings.HasPrefix(join, "fetch --no-tags --no-recurse-submodules --refmap= origin +refs/heads/"):
		if g.failFetch {
			return nil, fmt.Errorf("unreachable origin")
		}
		// The bounded preflight refresh: the fake's default ref already
		// names the fetched head, so the fetch only confirms the shared
		// cache the fake models as current.
		return nil, nil
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
	case strings.HasPrefix(join, "rev-parse --verify refs/heads/"):
		// The exact-path resolution the ref boundary pins by (CD-0212 D4):
		// a full refs/heads path admits no tag shadowing, and the fake holds
		// only direct branches.
		branch := strings.TrimPrefix(join, "rev-parse --verify refs/heads/")
		if sha, ok := g.branches[branch]; ok {
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

// RunStdin answers the stdin plumbing the squash containment probes: patch-id
// over a diff or a diff-tree stream. The fake's patch text is its own
// patch-id, so patch-id echoes its input.
func (g *fakeWorktreeGit) RunStdin(_ context.Context, dir string, stdin []byte, args ...string) ([]byte, error) {
	g.calls = append(g.calls, append([]string{dir}, args...))
	join := strings.Join(args, " ")
	switch join {
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
	return nil, fmt.Errorf("unexpected stdin git invocation: %s", join)
}

// netDiffPatchID models the patch text diff(from, to) produces.
func (g *fakeWorktreeGit) netDiffPatchID(from, to string) string {
	return "pid:" + g.treeOf(from) + ".." + g.treeOf(to)
}

// squashMergeIntoDefault records the commit the default ref holds whose patch
// carries branch's net diff from its merge base: the shape a squash merge
// leaves behind once the remote head branch is deleted (CD-0181).
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

// divergeBranch models a branch holding commits beyond the default ref:
// ahead of them, unpushed of those unreachable from local remotes. The tip
// moves to a fresh sha, because a branch with commits beyond a ref cannot
// share that ref's tip in real git, and the store's gates query revisions
// by observed tip (CD-0212 D1), so the fake's tips must identify the branch
// whose state was modeled.
func (g *fakeWorktreeGit) divergeBranch(branch string, ahead, unpushed int) {
	g.ahead[branch] = ahead
	g.unpushed[branch] = unpushed
	g.branches[branch] = distinctTip(branch)
}

// distinctTip derives a stable pseudo-sha unlike every fixture base.
func distinctTip(branch string) string {
	sum := fnv.New32a()
	_, _ = sum.Write([]byte(branch))
	return fmt.Sprintf("d%039x", sum.Sum32())
}

// addBranchRecordShard models one record shard the branch tree adds beyond
// the default tree, with the record kind its committed JSON carries. An
// empty kind removes the branch's modelled shards, the shape a merge leaves.
func (g *fakeWorktreeGit) addBranchRecordShard(branch, path, kind string) {
	if kind == "" {
		delete(g.addedRecordShards, branch)
		return
	}
	g.addedRecordShards[branch] = append(g.addedRecordShards[branch], path)
	g.recordShardKinds[branch+":"+path] = kind
	// A branch whose tree adds a record holds a commit, so its tip cannot
	// stay the default ref's tip (CD-0212 D1 tip-keyed gates).
	if tip := distinctTip(branch); g.branches[branch] != tip {
		g.branches[branch] = tip
	}
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
	if result.Entry.State != worktreeEntryActive || result.Entry.Branch != claimBranch() || result.Entry.ClaimOpID != "wt-op-1" {
		t.Fatalf("entry=%+v", result.Entry)
	}
	if worktreeOccupancyByEntry(t, s, result.Entry.SetID, result.Entry.ProjectID, result.Entry.ClaimOpID) != "ses-claim" {
		t.Fatalf("occupancy row missing for ses-claim, entry=%+v", result.Entry)
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

func TestClaimWorktreeRefusesNonMemberProjectBeforeDurableWrite(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	// The second Project is fully valid on its own: Product, membership, and a
	// canonical-path locator over a registered repository root. The only thing
	// missing is work-w's membership, so the refusal below names membership
	// alone and not a missing locator or an unreachable repository.
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{locatorProductEvent("product-w2"), locatorProjectEvent("project-w2"), locatorMembershipEvent("product-w2", "project-w2")}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product-w2"): 0, VersionRef(SubjectProject, "project-w2"): 0}}); err != nil {
		t.Fatal(err)
	}
	repo2 := t.TempDir()
	git.addRepository(repo2)
	if err := s.AddProjectLocator(context.Background(), "project-w2", ProjectLocator{ID: "path-w2", Kind: LocatorCanonicalPath, Value: repo2}, 1); err != nil {
		t.Fatal(err)
	}
	req := baseClaim(git)
	req.ProjectID = "project-w2"
	_, err := s.ClaimWorktree(context.Background(), req)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindUnknownScope {
		t.Fatalf("claim error=%v, want unknown scope", err)
	}
	var claims int
	if err := s.db.QueryRow(`SELECT count(*) FROM worktree_claims`).Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if claims != 0 {
		t.Fatalf("claims=%d, want no durable claim", claims)
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
	reclaim := WorktreeReclaimRequest{WorkID: "work-w", ProjectID: "project-w", DefaultRef: "origin/main", PrincipalRef: "principal-1", RequestID: "req-2", ExpectedVersion: 3, Now: time.Unix(20, 0).UTC(), Runner: git}

	git.dirty[claimed.Entry.Path] = true
	if _, err := s.ReclaimWorktree(context.Background(), reclaim); err == nil || !strings.Contains(err.Error(), "dirty") {
		t.Fatalf("dirty tree must be refused, got %v", err)
	}
	git.dirty[claimed.Entry.Path] = false

	// A local-only commit is the sole copy and must remain in the worktree.
	git.divergeBranch(claimBranch(), 1, 1)
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
	writeProtectingHostLease(t, s)
	req := baseClaim(git)
	req.SessionRef = "ses_live"
	// The legacy row must predate nothing: recording it now keeps the
	// protecting lease ahead of it in start-time order.
	req.Now = time.Now().UTC()
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

// The release path keeps every strand-guard for live state (CD-0178 D3, as
// amended by CD-0179): a legacy occupancy row with no process identity stays
// while a live lease predates it, so the recorded row keeps the worktree
// claim live and the reclaim refuses.
func TestReclaimWorktreeKeepsLiveOccupantRefusal(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	writeProtectingHostLease(t, s)
	req := baseClaim(git)
	req.SessionRef = "ses_live"
	req.Now = time.Now().UTC()
	claimed, err := s.ClaimWorktree(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	seedWorktreeLifecycle(t, s)

	cases := []string{"recorded legacy occupant"}
	for _, name := range cases {
		_, err := s.ReclaimWorktree(context.Background(), WorktreeReclaimRequest{
			WorkID: "work-w", ProjectID: "project-w", DefaultRef: "origin/main",
			PrincipalRef: "principal-1", RequestID: "req-live-" + name, ExpectedVersion: 4,
			Now: time.Unix(20, 0).UTC(), Runner: git,
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

// TestDestroyRefusesOccupiedWorktreeDespiteApproval pins that the destructive
// tier's operator approval does not reach the occupancy gate. The approval
// covers discarding the clean-tree and durable-branch gates, which protect
// committed and uncommitted work. It does not authorize stranding a session.
func TestDestroyRefusesOccupiedWorktreeDespiteApproval(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	writeProtectingHostLease(t, s)
	req := baseClaim(git)
	req.SessionRef = "ses_live"
	req.Now = time.Now().UTC()
	claimed, err := s.ClaimWorktree(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	seedWorktreeLifecycle(t, s)
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
	seedWorktreeLifecycle(t, s)
	entry, err := s.DestroyWorktree(context.Background(), WorktreeDestroyRequest{
		WorkID: "work-w", ProjectID: "project-w", DefaultRef: "origin/main",
		ExpectedVersion: 4, PrincipalRef: "principal-1", RequestID: "destroy-stale-occupancy",
		Now: time.Unix(30, 0).UTC(), Runner: git,
		OperatorApprovalRef: "approval:destroy-stale", Destructive: true, ReleaseOccupancy: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.State != worktreeEntryReclaimed {
		t.Fatalf("entry=%+v, want reclaimed", entry)
	}
	if worktreeOccupancyByEntry(t, s, entry.SetID, entry.ProjectID, entry.ClaimOpID) != "" {
		t.Fatalf("operator-approved destructive reclaim must release every occupancy row: entry=%+v", entry)
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

	reclaim := WorktreeReclaimRequest{WorkID: "work-w", ProjectID: "project-w", DefaultRef: "origin/main", PrincipalRef: "principal-1", RequestID: "req-2", ExpectedVersion: 3, Now: time.Unix(20, 0).UTC(), Runner: git}
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

// TestReclaimWorktreeAcceptsSquashMergedBranchWithDeletedRemote pins the
// CD-0181 containment. The remote head branch was deleted after the squash
// merge, so every branch commit is unreachable from local remote-tracking
// refs, and the default ref holds the branch's net diff as one commit. The
// branch is durable and reclaims without a push that can never happen.
func TestReclaimWorktreeAcceptsSquashMergedBranchWithDeletedRemote(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	if _, err := s.ClaimWorktree(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	git.divergeBranch(claimBranch(), 2, 2)
	git.squashMergeIntoDefault(claimBranch())

	reclaim := WorktreeReclaimRequest{WorkID: "work-w", ProjectID: "project-w", DefaultRef: "origin/main", PrincipalRef: "principal-1", RequestID: "req-squash", ExpectedVersion: 3, Now: time.Unix(20, 0).UTC(), Runner: git}
	entry, err := s.ReclaimWorktree(context.Background(), reclaim)
	if err != nil {
		t.Fatalf("a squash-contained branch must reclaim, got %v", err)
	}
	if entry.State != worktreeEntryReclaimed {
		t.Fatalf("entry=%+v", entry)
	}
	if _, still := git.worktrees[claimPath(s)]; still {
		t.Fatal("native worktree was not removed")
	}
}

// TestReclaimWorktreeResolvesDefaultRefForSquashContainment pins the gate's
// default-ref resolution: a caller that names no default ref still gets the
// squash containment, resolved from origin/HEAD.
func TestReclaimWorktreeResolvesDefaultRefForSquashContainment(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	if _, err := s.ClaimWorktree(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	git.divergeBranch(claimBranch(), 2, 2)
	git.squashMergeIntoDefault(claimBranch())

	reclaim := WorktreeReclaimRequest{WorkID: "work-w", ProjectID: "project-w", PrincipalRef: "principal-1", RequestID: "req-squash-implicit", ExpectedVersion: 3, Now: time.Unix(20, 0).UTC(), Runner: git}
	if _, err := s.ReclaimWorktree(context.Background(), reclaim); err != nil {
		t.Fatalf("containment must resolve the default ref from origin/HEAD, got %v", err)
	}
}

// TestReclaimWorktreeRefusesEmptyNetDiff pins the refused edge: a branch
// whose tree equals its merge base has no net diff, so no patch-id can
// establish containment, and the count refusal stands.
func TestReclaimWorktreeRefusesEmptyNetDiff(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	if _, err := s.ClaimWorktree(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	git.divergeBranch(claimBranch(), 2, 2)
	git.content["merge-base"] = "shared-tree"
	git.content[claimBranch()] = "shared-tree"

	reclaim := WorktreeReclaimRequest{WorkID: "work-w", ProjectID: "project-w", DefaultRef: "origin/main", PrincipalRef: "principal-1", RequestID: "req-empty-diff", ExpectedVersion: 3, Now: time.Unix(20, 0).UTC(), Runner: git}
	_, err := s.ReclaimWorktree(context.Background(), reclaim)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation {
		t.Fatalf("empty net diff must refuse typed, got %v", err)
	}
	if !strings.Contains(failure.Detail, "not reachable from remote refs") {
		t.Fatalf("refusal detail=%q", failure.Detail)
	}
	if _, kept := git.worktrees[claimPath(s)]; !kept {
		t.Fatal("refused worktree must remain")
	}
}

// TestReclaimWorktreeRefusesCommitsBeyondTheMergedPatch pins the refused
// edge: the default ref holds the patch the branch once squash-merged, but
// the branch has advanced past it, so its net diff no longer matches and the
// later commits stay protected.
func TestReclaimWorktreeRefusesCommitsBeyondTheMergedPatch(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	if _, err := s.ClaimWorktree(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	git.divergeBranch(claimBranch(), 2, 2)
	git.content[claimBranch()] = "squash-tree"
	git.squashMergeIntoDefault(claimBranch())
	git.content[claimBranch()] = "later-tree"

	reclaim := WorktreeReclaimRequest{WorkID: "work-w", ProjectID: "project-w", DefaultRef: "origin/main", PrincipalRef: "principal-1", RequestID: "req-beyond", ExpectedVersion: 3, Now: time.Unix(20, 0).UTC(), Runner: git}
	_, err := s.ReclaimWorktree(context.Background(), reclaim)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation {
		t.Fatalf("commits beyond the merged patch must refuse typed, got %v", err)
	}
	if _, kept := git.worktrees[claimPath(s)]; !kept {
		t.Fatal("refused worktree must remain")
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
	// The helper inserts a legacy worktree_occupancy row (no process
	// identity) recorded now, so a live host lease that predates the row
	// keeps it in place (CD-0179) and the reclaim/destroy path refuses
	// without operator approval. The fold-only guard requires fold_guard=1
	// around any direct INSERT, even in tests.
	if _, err := s.DatabaseForTesting().Exec(`
		INSERT INTO fold_guard(active) VALUES(1);
		INSERT INTO worktree_occupancy (worktree_id, session_ref, recorded_at, host_pid, host_pid_start, has_process_identity)
		SELECT set_id || ':' || project_id || ':' || claim_op_id, ?, ?, NULL, NULL, 0
		  FROM worktree_entries WHERE set_id=? AND state='active';
		DELETE FROM fold_guard`, sessionRef, time.Now().UTC().Format(time.RFC3339Nano), WorktreeSetID(workID)); err != nil {
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

// The audit read pages its classification: each page continues at the offset
// the previous page returned, the last page carries no continuation, and a
// cursor that does not name a position in the current classification refuses
// typed instead of guessing (CD-0185).
func TestWorktreeAuditCursorPagesAndRejectsNonPosition(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	ctx := context.Background()
	root := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-w")
	for i := 0; i < 7; i++ {
		if err := os.MkdirAll(filepath.Join(root, fmt.Sprintf("work-orphan-%02d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	full, err := s.WorktreeAudit(ctx, WorktreeAuditRequest{ProductID: "product-w", Limit: 100, Runner: git, DefaultRef: "origin/main"})
	if err != nil {
		t.Fatal(err)
	}
	cursor := ""
	walked := 0
	pages := 0
	for {
		page, pageErr := s.WorktreeAudit(ctx, WorktreeAuditRequest{ProductID: "product-w", Limit: 3, Runner: git, DefaultRef: "origin/main", Cursor: cursor})
		if pageErr != nil {
			t.Fatal(pageErr)
		}
		pages++
		if len(page.Drift) == 0 {
			t.Fatalf("page %d is empty before the classification is exhausted", pages)
		}
		walked += len(page.Drift)
		if page.NextCursor == "" {
			break
		}
		if len(page.Drift) != 3 {
			t.Fatalf("page %d holds %d rows, want a full page before the last", pages, len(page.Drift))
		}
		cursor = page.NextCursor
		if pages > 10 {
			t.Fatalf("paging did not terminate")
		}
	}
	if walked != len(full.Drift) {
		t.Fatalf("walked=%d rows over %d pages, full classification=%d", walked, pages, len(full.Drift))
	}
	for _, bad := range []string{"not-a-number", "-1", strconv.Itoa(len(full.Drift) + 1)} {
		_, pageErr := s.WorktreeAudit(ctx, WorktreeAuditRequest{ProductID: "product-w", Limit: 3, Runner: git, DefaultRef: "origin/main", Cursor: bad})
		assertFailureKind(t, pageErr, KindInvalidCursor)
	}
}

func TestWorktreeAuditClassifiesEachDriftClass(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	ctx := context.Background()
	root := filepath.Join(filepath.Dir(s.Path()), "worktrees")

	// Healthy: a verified claim whose worktree exists on disk reports
	// nothing, because the branch holds commits beyond the default ref.
	auditWork(t, s, git, "work-healthy", true)
	git.divergeBranch("work/work-healthy", 1, 0)
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

// A claim whose durable half fails after this operation ran `git worktree
// add` must not leave the created worktree and branch unowned: the tree was
// created outside the transaction, so the rollback alone strands it. The
// compensation removes both only when the tree is clean and holds no commit
// beyond the pinned base; a removal that cannot complete reports the failure
// effect-possible instead of claiming no effect.
func TestClaimWorktreeRollbackCompensatesCreatedWorktree(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	request := baseClaim(git)
	request.ExpectedVersion = 99 // stale version fails the durable fold, not the git half
	_, err := s.ClaimWorktree(context.Background(), request)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindVersionConflict {
		t.Fatalf("stale claim failure=%v, want version_conflict", err)
	}
	if failure.EffectPossible {
		t.Fatalf("compensated claim reported effect possible: %+v", failure)
	}
	if _, held := git.worktrees[claimPath(s)]; held {
		t.Fatal("compensation left the created worktree in place")
	}
	if _, held := git.branches[claimBranch()]; held {
		t.Fatal("compensation left the created branch in place")
	}
	if git.countCalls("worktree remove") != 1 || git.countCalls("branch -D -- "+claimBranch()) != 1 {
		t.Fatalf("compensation calls=%v, want one worktree remove and one branch delete", git.calls)
	}
}

// A dirty tree may hold work the claim never recorded, so compensation must
// refuse the removal and mark the failure effect-possible: the created
// worktree then outlives the rolled-back claim row.
func TestClaimWorktreeRollbackKeepsDirtyWorktreeAndReportsPossibleEffect(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	git.dirty[claimPath(s)] = true
	request := baseClaim(git)
	request.ExpectedVersion = 99
	_, err := s.ClaimWorktree(context.Background(), request)
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("stale claim failure=%v, want a typed failure", err)
	}
	if !failure.EffectPossible {
		t.Fatalf("dirty-tree compensation reported no effect: %+v", failure)
	}
	if _, held := git.worktrees[claimPath(s)]; !held {
		t.Fatal("compensation removed a dirty worktree")
	}
	if git.countCalls("worktree remove") != 0 {
		t.Fatal("compensation attempted to remove a dirty worktree")
	}
}

// A branch holding commits beyond the pinned base is unrecorded work. The
// removal must refuse and the failure must report the possible effect.
func TestClaimWorktreeRollbackKeepsCommittedWorktreeAndReportsPossibleEffect(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	// The rollback probe reads the branch by name (the claim surface, not
	// the reclaim gates), so the modeled commits need no distinct tip.
	git.ahead[claimBranch()] = 1
	request := baseClaim(git)
	request.ExpectedVersion = 99
	_, err := s.ClaimWorktree(context.Background(), request)
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("stale claim failure=%v, want a typed failure", err)
	}
	if !failure.EffectPossible {
		t.Fatalf("committed-worktree compensation reported no effect: %+v", failure)
	}
	if _, held := git.worktrees[claimPath(s)]; !held {
		t.Fatal("compensation removed a worktree holding commits")
	}
}

// A clean, commit-free worktree that pre-existed the claim is native state
// this operation never created: its phase-3 failure returns the cause
// unchanged and touches nothing, because the first probe adopted the tree and
// the operation ran no `git worktree add` of its own.
func TestClaimWorktreeKeepsPreExistingWorktreeWhenItCreatedNothing(t *testing.T) {
	t.Parallel()
	s, git, repoRoot := worktreeFixture(t)
	base := git.branches["main"]
	git.branches[claimBranch()] = base
	git.worktrees[claimPath(s)] = claimBranch()
	git.worktreeRepos[claimPath(s)] = repoRoot
	request := baseClaim(git)
	request.ExpectedVersion = 99 // stale version fails the durable fold, not the git half
	_, err := s.ClaimWorktree(context.Background(), request)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindVersionConflict {
		t.Fatalf("stale claim failure=%v, want version_conflict", err)
	}
	if failure.EffectPossible {
		t.Fatalf("pre-existing tree reported effect possible: %+v", failure)
	}
	if _, held := git.worktrees[claimPath(s)]; !held {
		t.Fatal("compensation removed a worktree this operation did not create")
	}
	if git.countCalls("worktree add") != 0 {
		t.Fatalf("claim ran worktree add for a pre-existing tree: %v", git.calls)
	}
	if git.countCalls("worktree remove") != 0 {
		t.Fatal("compensation ran worktree remove for a pre-existing tree")
	}
}

// The caller that owns the commit can only compensate what the claim reports.
// PrepareWorktreeClaimNative reports the tree and branch this operation
// created, so a caller whose transaction fails after the native half — a
// failed Commit, or the agent mutation envelope's post-effect writes —
// compensates from the reported facts instead of leaving the creation
// stranded.
func TestClaimWorktreeRawTxReportsCreatedNativeState(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	ctx := context.Background()
	request := baseClaim(git)
	native, created, prepErr := s.PrepareWorktreeClaimNative(ctx, request)
	if prepErr != nil {
		t.Fatal(prepErr)
	}
	if created == nil || created.Path != claimPath(s) || created.Branch != claimBranch() || created.Base != request.BaseSHA || created.RepoRoot == "" || !created.CreatedBranch {
		t.Fatalf("created report=%+v, want this operation's tree and branch", created)
	}
	_ = native
	cause := errors.New("cannot commit claim")
	if compErr := CompensateWorktreeClaimCreation(ctx, git, *created, cause); compErr != cause {
		var failure *Failure
		if errors.As(compErr, &failure) && failure.EffectPossible {
			t.Fatalf("compensation could not prove removal: %v", compErr)
		}
		t.Fatalf("compensation error=%v, want the cause returned unchanged", compErr)
	}
	if _, held := git.worktrees[claimPath(s)]; held {
		t.Fatal("compensation left the created worktree in place")
	}
	if _, held := git.branches[claimBranch()]; held {
		t.Fatal("compensation left the created branch in place")
	}
}

// An adopted branch pre-existed the claim, so compensation removes the
// worktree this operation created but never the branch it only adopted.
func TestClaimWorktreeRollbackRemovesWorktreeButKeepsAdoptedBranch(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	base := git.branches["main"]
	git.branches[claimBranch()] = base
	request := baseClaim(git)
	request.ExpectedVersion = 99
	_, err := s.ClaimWorktree(context.Background(), request)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindVersionConflict {
		t.Fatalf("stale claim failure=%v, want version_conflict", err)
	}
	if _, held := git.worktrees[claimPath(s)]; held {
		t.Fatal("compensation left the created worktree in place")
	}
	if _, held := git.branches[claimBranch()]; !held {
		t.Fatal("compensation deleted the adopted branch")
	}
}

// A failed `git worktree add` can leave the tree directory or the new branch
// behind. The rolled-back claim cannot see that state, so the failure must
// report the effect possible; a failure that left nothing reports no effect.
func TestClaimWorktreeAddFailureReportsPartialNativeState(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		partial    bool
		wantEffect bool
	}{
		{name: "partial state left behind", partial: true, wantEffect: true},
		{name: "nothing left behind", partial: false, wantEffect: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, git, _ := worktreeFixture(t)
			git.partialAdd = tc.partial
			git.failAdd = !tc.partial
			_, err := s.ClaimWorktree(context.Background(), baseClaim(git))
			var failure *Failure
			if !errors.As(err, &failure) || failure.Kind != KindGitUnreachable {
				t.Fatalf("claim failure=%v, want git_unreachable", err)
			}
			if failure.EffectPossible != tc.wantEffect {
				t.Fatalf("effect possible=%v, want %v", failure.EffectPossible, tc.wantEffect)
			}
		})
	}
}

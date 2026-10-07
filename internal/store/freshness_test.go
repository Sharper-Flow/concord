package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// runFreshnessGit runs one fixture git command with a synthetic committer.
func runFreshnessGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repo}, args...)...)
	command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// initFreshnessOriginRepo builds a working repository whose origin is a real
// local bare remote, so the bounded preflight fetch exercises real Git. The
// returned advance function commits and pushes one new default-branch commit
// from a throwaway clone and returns its SHA, leaving the working
// repository's remote-tracking cache stale until a fetch runs.
func initFreshnessOriginRepo(t *testing.T) (string, func(string) string) {
	t.Helper()
	parent := t.TempDir()
	origin := filepath.Join(parent, "origin.git")
	repo := filepath.Join(parent, "repo")
	runFreshnessGit(t, parent, "init", "-q", "--bare", "-b", "main", "origin.git")
	runFreshnessGit(t, parent, "init", "-q", "-b", "main", "repo")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("freshness\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runFreshnessGit(t, repo, "add", "README.md")
	runFreshnessGit(t, repo, "commit", "-q", "-m", "base")
	runFreshnessGit(t, repo, "remote", "add", "origin", "https://freshness.invalid/repository.git")
	runFreshnessGit(t, repo, "config", "url."+origin+".insteadOf", "https://freshness.invalid/repository.git")
	runFreshnessGit(t, repo, "push", "-q", "origin", "main")
	runFreshnessGit(t, repo, "fetch", "-q", "origin")
	runFreshnessGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	advances := 0
	advance := func(message string) string {
		advances++
		seed := filepath.Join(parent, "seed-"+strconv.Itoa(advances))
		runFreshnessGit(t, parent, "clone", "-q", origin, "seed-"+strconv.Itoa(advances))
		if err := os.WriteFile(filepath.Join(seed, "commit-"+strconv.Itoa(advances)), []byte(message+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runFreshnessGit(t, seed, "add", ".")
		runFreshnessGit(t, seed, "commit", "-q", "-m", message)
		runFreshnessGit(t, seed, "push", "-q", "origin", "main")
		head := runFreshnessGit(t, seed, "rev-parse", "HEAD")
		if err := os.RemoveAll(seed); err != nil {
			t.Fatal(err)
		}
		return head
	}
	return repo, advance
}

// goOfflineOrigin swaps the insteadOf mapping onto a removed local path, so
// every fetch fails the same way an unreachable remote does, with no network
// dependency.
func goOfflineOrigin(t *testing.T, repo string) {
	t.Helper()
	mapped := runFreshnessGit(t, repo, "config", "--get-regexp", `^url\..*\.insteadOf$`)
	fields := strings.Fields(mapped)
	if len(fields) != 2 {
		t.Fatalf("fixture origin mapping missing: %q", mapped)
	}
	runFreshnessGit(t, repo, "config", "--unset", fields[0])
	runFreshnessGit(t, repo, "config", "url."+filepath.Join(t.TempDir(), "gone.git")+".insteadOf", fields[1])
}

// The capture's default-based base must be the fetched default head, not the
// stale local tracking cache. This test first exposed the defect this work
// repairs: the created worktree sat on the last head another tool fetched.
func TestWorkBootstrapCaptureCreatesFromRefreshedDefaultHead(t *testing.T) {
	repo, advanceDefault := initFreshnessOriginRepo(t)
	s := openTemp(t)
	seedBootstrapStoreAuthority(t, s, repo)
	stale := runFreshnessGit(t, repo, "rev-parse", "HEAD")
	advanced := advanceDefault("second commit on origin")
	if cached := runFreshnessGit(t, repo, "rev-parse", "refs/remotes/origin/main"); cached != stale {
		t.Fatalf("tracking cache=%s want the stale head %s", cached, stale)
	}
	result, err := s.BootstrapWorktree(context.Background(), bootstrapStoreRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Entry.BaseSHA != advanced {
		t.Fatalf("capture base=%s want the fetched default head %s", result.Entry.BaseSHA, advanced)
	}
	if head := runFreshnessGit(t, result.Entry.Path, "rev-parse", "HEAD"); head != advanced {
		t.Fatalf("worktree head=%s want %s", head, advanced)
	}
}

// A refresh failure refuses the capture before any work item, claim, branch,
// or worktree exists.
func TestWorkBootstrapCaptureRefusesWhenRefreshFails(t *testing.T) {
	repo, _ := initFreshnessOriginRepo(t)
	goOfflineOrigin(t, repo)
	s := openTemp(t)
	seedBootstrapStoreAuthority(t, s, repo)
	_, err := s.BootstrapWorktree(context.Background(), bootstrapStoreRequest(), nil)
	if err == nil {
		t.Fatal("capture succeeded without the origin refresh")
	}
	assertFailureKind(t, err, KindGitUnreachable)
	for _, table := range []struct{ table string }{{"work_items"}, {"bootstrap_operations"}, {"worktree_claims"}} {
		var count int
		if err := s.db.QueryRow(`SELECT count(*) FROM ` + table.table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s holds %d rows after the refresh refusal", table.table, count)
		}
	}
	if branches := runFreshnessGit(t, repo, "branch", "--format=%(refname:short)"); strings.Contains(branches, "work/") {
		t.Fatalf("refresh refusal left a work branch behind: %q", branches)
	}
	if listing := runFreshnessGit(t, repo, "worktree", "list", "--porcelain"); strings.Count(listing, "\nworktree ")+boolToInt(strings.HasPrefix(listing, "worktree ")) != 1 {
		t.Fatalf("refresh refusal left a linked worktree behind:\n%s", listing)
	}
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// A caller-pinned ref stays an exact pin, and every first creation still owes
// the shared preflight: the capture with the origin online resolves the pin
// exactly while the fetch runs, and the same capture with the origin offline
// refuses typed before any effect.
func TestWorkBootstrapCaptureExplicitRefPreflightsAndPinsBase(t *testing.T) {
	repo, advanceDefault := initFreshnessOriginRepo(t)
	s := openTemp(t)
	seedBootstrapStoreAuthority(t, s, repo)
	pinned := runFreshnessGit(t, repo, "rev-parse", "HEAD")
	advanceDefault("origin moved past the pinned commit")
	req := bootstrapStoreRequest()
	req.Ref = pinned
	req.IdempotencyKey = "bootstrap-store-pinned"
	result, err := s.BootstrapWorktree(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("explicit-ref capture refused with the origin online: %v", err)
	}
	if result.Entry.BaseSHA != pinned {
		t.Fatalf("capture base=%s want the pinned %s", result.Entry.BaseSHA, pinned)
	}
	goOfflineOrigin(t, repo)
	offlineReq := bootstrapStoreRequest()
	offlineReq.Ref = pinned
	offlineReq.IdempotencyKey = "bootstrap-store-pinned-offline"
	_, err = s.BootstrapWorktree(context.Background(), offlineReq, nil)
	if err == nil {
		t.Fatal("explicit-ref capture succeeded without the origin refresh")
	}
	assertFailureKind(t, err, KindGitUnreachable)
	for _, table := range []string{"work_items", "worktree_claims"} {
		var count int
		if err := s.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("%s holds %d rows after the offline refusal, want only the online capture", table, count)
		}
	}
}

// An exact replay keeps its stored base and succeeds with the origin offline.
func TestWorkBootstrapReplayKeepsPinnedBaseWithoutFetch(t *testing.T) {
	repo, _ := initFreshnessOriginRepo(t)
	s := openTemp(t)
	seedBootstrapStoreAuthority(t, s, repo)
	first, err := s.BootstrapWorktree(context.Background(), bootstrapStoreRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	goOfflineOrigin(t, repo)
	replay, err := s.BootstrapWorktree(context.Background(), bootstrapStoreRequest(), nil)
	if err != nil {
		t.Fatalf("exact replay refused offline: %v", err)
	}
	if !replay.Replayed || replay.WorkID != first.WorkID || replay.Entry.BaseSHA != first.Entry.BaseSHA {
		t.Fatalf("replay=%+v first=%+v", replay, first)
	}
}

// The missing-worktree bootstrap of an existing live item refreshes before it
// resolves its creation base.
func TestWorkBootstrapExistingWorktreeRefreshesDefaultBase(t *testing.T) {
	repo, advanceDefault := initFreshnessOriginRepo(t)
	s := openTemp(t)
	seedBootstrapStoreAuthority(t, s, repo)
	if err := seedLiveWorkItem(t, s, "work-fresh-existing", "project-bootstrap"); err != nil {
		t.Fatal(err)
	}
	advanced := advanceDefault("origin moved before the first worktree")
	result, err := s.BootstrapExistingWorktree(context.Background(), ExistingBootstrapRequest{
		ProductID: "product-bootstrap", ProjectID: "project-bootstrap", WorkID: "work-fresh-existing",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Entry.BaseSHA != advanced {
		t.Fatalf("existing bootstrap base=%s want the fetched default head %s", result.Entry.BaseSHA, advanced)
	}
}

// SampleWorktreeFreshness reports the exact lag behind the refreshed default
// branch and leaves the worktree's files, index, and branch tip untouched.
func TestSampleWorktreeFreshnessReportsExactLagWithoutWorktreeChanges(t *testing.T) {
	repo, advanceDefault := initFreshnessOriginRepo(t)
	s := openTemp(t)
	seedBootstrapStoreAuthority(t, s, repo)
	capture, err := s.BootstrapWorktree(context.Background(), bootstrapStoreRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	second := advanceDefault("origin commit two")
	third := advanceDefault("origin commit three")
	behind := int64(2)
	freshness := SampleWorktreeFreshness(context.Background(), capture.Entry, ExecGitRunner{})
	if freshness.Status != FreshnessStatusOK || freshness.BehindCount == nil || *freshness.BehindCount != behind {
		t.Fatalf("freshness=%+v want ok with behind=%d", freshness, behind)
	}
	if freshness.DefaultRef != "origin/main" || freshness.DefaultSHA != third {
		t.Fatalf("default ref=%q sha=%s want origin/main at %s", freshness.DefaultRef, freshness.DefaultSHA, third)
	}
	if head := runFreshnessGit(t, capture.Entry.Path, "rev-parse", "HEAD"); freshness.HeadSHA != head {
		t.Fatalf("sampled head=%s want the worktree HEAD %s", freshness.HeadSHA, head)
	}
	if status := runFreshnessGit(t, capture.Entry.Path, "status", "--porcelain"); status != "" {
		t.Fatalf("freshness sampling dirtied the worktree: %q", status)
	}
	if tip := runFreshnessGit(t, repo, "rev-parse", "refs/heads/"+capture.Entry.Branch); tip != capture.Entry.BaseSHA || tip == second {
		t.Fatalf("branch tip=%s want the untouched base %s", tip, capture.Entry.BaseSHA)
	}
}

// A fetch failure degrades the sample to the typed unknown with no count and
// never blocks the caller.
func TestSampleWorktreeFreshnessUnknownOnFetchFailure(t *testing.T) {
	repo, _ := initFreshnessOriginRepo(t)
	s := openTemp(t)
	seedBootstrapStoreAuthority(t, s, repo)
	capture, err := s.BootstrapWorktree(context.Background(), bootstrapStoreRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	goOfflineOrigin(t, repo)
	freshness := SampleWorktreeFreshness(context.Background(), capture.Entry, ExecGitRunner{})
	if freshness.Status != FreshnessStatusUnknown || freshness.BehindCount != nil || freshness.Reason != FreshnessReasonFetchFailed {
		t.Fatalf("freshness=%+v want unknown fetch_failed with no count", freshness)
	}
}

// A fetch that outlives the fixed deadline reports the typed timeout.
func TestSampleWorktreeFreshnessTimeoutReason(t *testing.T) {
	original := freshnessPreflightTimeout
	freshnessPreflightTimeout = 20 * time.Millisecond
	t.Cleanup(func() { freshnessPreflightTimeout = original })
	entry := WorktreeEntry{Path: initFreshnessOriginRepoPath(t)}
	freshness := SampleWorktreeFreshness(context.Background(), entry, newBlockingFetchRunner())
	if freshness.Status != FreshnessStatusUnknown || freshness.Reason != FreshnessReasonTimeout || freshness.BehindCount != nil {
		t.Fatalf("freshness=%+v want unknown timeout with no count", freshness)
	}
}

type blockingFetchRunner struct{ GitRunner }

func newBlockingFetchRunner() blockingFetchRunner {
	return blockingFetchRunner{GitRunner: ExecGitRunner{}}
}

func (blockingFetchRunner) RunNoninteractive(ctx context.Context, _ string, _ ...string) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func initFreshnessOriginRepoPath(t *testing.T) string {
	t.Helper()
	repo, _ := initFreshnessOriginRepo(t)
	return repo
}

// seedLiveWorkItem folds one live work item into the store, the state a
// resume that bootstraps a missing worktree starts from.
func seedLiveWorkItem(t *testing.T, s *Store, workID, projectID string) error {
	t.Helper()
	return ApplyOperation(context.Background(), s, Operation{Events: []Event{
		{EventID: workID + "-create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"Freshness Work","priority":1}`)},
		{EventID: workID + "-membership", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(4, 0).UTC(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"` + projectID + `","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 0}})
}

// The first ordinary direct claim runs the shared preflight before it creates
// any branch or worktree: a fetch failure refuses typed with no effect, and a
// healthy refresh records the bounded fetch of the registered default branch.
func TestClaimWorktreeRefreshesBeforeFirstCreation(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	git.failFetch = true
	_, err := s.ClaimWorktree(context.Background(), baseClaim(git))
	if err == nil {
		t.Fatal("claim succeeded without the origin refresh")
	}
	assertFailureKind(t, err, KindGitUnreachable)
	if _, held := git.worktrees[claimPath(s)]; held {
		t.Fatal("refresh refusal created the worktree")
	}
	if _, held := git.branches[claimBranch()]; held {
		t.Fatal("refresh refusal created the branch")
	}
	var claims int
	if err := s.db.QueryRow(`SELECT count(*) FROM worktree_claims`).Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if claims != 0 {
		t.Fatalf("refresh refusal left %d claim rows", claims)
	}
	if got := git.countCalls("fetch --no-tags --no-recurse-submodules --refmap= origin +refs/heads/main:refs/remotes/origin/main"); got != 1 {
		t.Fatalf("refused attempt ran %d bounded default-branch fetches, want 1", got)
	}
	git.failFetch = false
	if _, err := s.ClaimWorktree(context.Background(), baseClaim(git)); err != nil {
		t.Fatal(err)
	}
	if got := git.countCalls("fetch --no-tags --no-recurse-submodules --refmap= origin +refs/heads/main:refs/remotes/origin/main"); got != 2 {
		t.Fatalf("retry after refusal ran %d fetches total, want one per fresh attempt", got)
	}
}

// A retry whose durable claim row already exists is an exact replay or
// recovery: it keeps its stored base and owes no new fetch.
func TestClaimWorktreeReplayOwesNoFetch(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	first, err := s.ClaimWorktree(context.Background(), baseClaim(git))
	if err != nil {
		t.Fatal(err)
	}
	git.failFetch = true
	replay, err := s.ClaimWorktree(context.Background(), baseClaim(git))
	if err != nil {
		t.Fatalf("replay refused with the origin offline: %v", err)
	}
	if !replay.Reconciled || replay.Entry.ClaimOpID != first.Entry.ClaimOpID {
		t.Fatalf("replay=%+v first=%+v", replay, first)
	}
	if got := git.countCalls("fetch --no-tags"); got != 1 {
		t.Fatalf("replay ran %d fetches, want only the first creation's", got)
	}
}

// exerciseCrossProjectClaim runs the cross-Project direct claim fixture: two
// repositories, a capture in the first, and the first claim of the same work
// in the second after that Project's origin advanced. It returns the base the
// cross-Project claim pinned.
func exerciseCrossProjectClaim(t *testing.T) string {
	t.Helper()
	repoA, _ := initFreshnessOriginRepo(t)
	repoB, advanceB := initFreshnessOriginRepo(t)
	s := openTemp(t)
	seedBootstrapStoreAuthority(t, s, repoA)
	seedBootstrapProject(t, s, "project-fresh-b", repoB)
	first, err := s.BootstrapWorktree(context.Background(), bootstrapStoreRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	memberships, marshalErr := json.Marshal(workMembershipsPayload{Memberships: []workMembershipPayload{
		{ProjectID: "project-bootstrap", Role: "primary"},
		{ProjectID: "project-fresh-b", Role: "secondary"},
	}, ExpectedVersion: first.WorkVersion, ResultingVersion: first.WorkVersion + 1})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{{
		EventID: first.WorkID + "-memberships-b", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: first.WorkID,
		Actor: "operator", OccurredAt: time.Unix(5, 0).UTC(), PayloadVersion: 1, Payload: memberships,
	}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, first.WorkID): first.WorkVersion}}); err != nil {
		t.Fatal(err)
	}
	advanced := advanceB("origin B moved before the cross-project claim")
	result, err := s.BootstrapExistingWorktree(context.Background(), ExistingBootstrapRequest{
		ProductID: "product-bootstrap", ProjectID: "project-fresh-b", WorkID: first.WorkID,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Entry.BaseSHA != advanced {
		t.Fatalf("cross-project claim base=%s want the fetched default head %s", result.Entry.BaseSHA, advanced)
	}
	return result.Entry.BaseSHA
}

// The cross-Project claim route owns exactly one bounded preflight for its
// fresh creation: the one that resolves its base before the claim pins it.
func TestCrossProjectClaimRefreshesDefaultBase(t *testing.T) {
	if base := exerciseCrossProjectClaim(t); !worktreeSHAPattern.MatchString(base) {
		t.Fatalf("cross-project claim base=%q is not a commit SHA", base)
	}
}

// The whole cross-Project creation runs a bounded fetch budget of exactly
// four invocations: two fixture initializations, the capture's preflight,
// and the second Project's single first-claim preflight. A second preflight
// doubles the operation's remote-contact budget for no new fact.
func TestCrossProjectCreationRunsExactlyOnePreflight(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	log := filepath.Join(bin, "fetches")
	script := fmt.Sprintf("#!/bin/sh\ncase \"$3\" in fetch) printf 'fetch\\n' >> %q ;; esac\nexec %q \"$@\"\n", log, realGit)
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	exerciseCrossProjectClaim(t)
	data, readErr := os.ReadFile(log)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if count := strings.Count(string(data), "fetch\n"); count != 4 {
		t.Fatalf("cross-Project creation counted %d git fetch invocations, want 2 initialization + 1 capture preflight + 1 claim preflight", count)
	}
}

// The bounded preflight's fetch must ignore the repository's configured
// remote fetch mappings: a configured mapping that names a local branch as
// its destination would move that branch, and the preflight may only update
// the selected remote-tracking ref.
func TestFreshnessPreflightLeavesConfiguredMappingsUnapplied(t *testing.T) {
	repo, advanceDefault := initFreshnessOriginRepo(t)
	parked := runFreshnessGit(t, repo, "rev-parse", "HEAD")
	runFreshnessGit(t, repo, "branch", "parked", parked)
	runFreshnessGit(t, repo, "config", "--add", "remote.origin.fetch", "+refs/heads/main:refs/heads/parked")
	advanceDefault("advance origin")
	preflight, err := startCreationPreflight(context.Background(), ExecGitRunner{}, repo)
	preflight.close()
	if err != nil {
		t.Fatal(err)
	}
	if moved := runFreshnessGit(t, repo, "rev-parse", "refs/heads/parked"); moved != parked {
		t.Fatalf("preflight moved local branch parked from %s to %s", parked, moved)
	}
}

// The bounded preflight's fetch must not recurse into submodules: a healthy
// Project default-branch refresh cannot depend on a submodule origin being
// reachable.
func TestFreshnessPreflightIgnoresSubmoduleOrigins(t *testing.T) {
	repo, _ := initFreshnessOriginRepo(t)
	subSource, _ := initFreshnessOriginRepo(t)
	runFreshnessGit(t, repo, "-c", "protocol.file.allow=always", "submodule", "add", subSource, "sub")
	runFreshnessGit(t, repo, "commit", "-qm", "add submodule")
	runFreshnessGit(t, repo, "push", "-q", "origin", "main")
	runFreshnessGit(t, filepath.Join(repo, "sub"), "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing-sub-origin.git"))
	runFreshnessGit(t, repo, "config", "fetch.recurseSubmodules", "true")
	preflight, err := startCreationPreflight(context.Background(), ExecGitRunner{}, repo)
	preflight.close()
	if err != nil {
		t.Fatalf("healthy Project default fetch failed because the preflight contacted the unreachable submodule origin: %v", err)
	}
}

// The bounded preflight refreshes exactly the origin default branch: a
// configured fetch.prune and fetch.pruneTags stay unapplied, so the refresh
// cannot prune the Project's local tags as a side effect.
func TestFreshnessPreflightKeepsLocalTagsUnderConfiguredPrune(t *testing.T) {
	repo, _ := initFreshnessOriginRepo(t)
	runFreshnessGit(t, repo, "tag", "local-only")
	runFreshnessGit(t, repo, "tag", "remote-only")
	runFreshnessGit(t, repo, "push", "-q", "origin", "refs/tags/remote-only")
	runFreshnessGit(t, repo, "tag", "-d", "remote-only")
	runFreshnessGit(t, repo, "config", "fetch.prune", "true")
	runFreshnessGit(t, repo, "config", "fetch.pruneTags", "true")
	preflight, err := startCreationPreflight(context.Background(), ExecGitRunner{}, repo)
	preflight.close()
	if err != nil {
		t.Fatal(err)
	}
	if tags := runFreshnessGit(t, repo, "tag", "--list"); tags != "local-only" {
		t.Fatalf("preflight applied the configured prune: tags=%q want local-only", tags)
	}
}

// writeDeadlineProbeShim installs a fake git on PATH whose descendant sleep
// inherits the command's output pipes and outlives every budget the tests
// set: the shape a wedged probe takes on a host.
func writeDeadlineProbeShim(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nsleep 0.5 &\nwait\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// Every freshness subprocess must end inside the fixed deadline, including
// the local probes: the runner kills the probe's process group and bounds
// the pipe drainage, so a descendant holding a probe's output pipes cannot
// stretch a 20ms budget to the descendant's own lifetime.
func TestSampleWorktreeFreshnessDeadlineBoundsProbeDescendants(t *testing.T) {
	original := freshnessPreflightTimeout
	freshnessPreflightTimeout = 20 * time.Millisecond
	t.Cleanup(func() { freshnessPreflightTimeout = original })
	writeDeadlineProbeShim(t)
	start := time.Now()
	freshness := SampleWorktreeFreshness(context.Background(), WorktreeEntry{Path: t.TempDir()}, ExecGitRunner{})
	elapsed := time.Since(start)
	if freshness.Status != FreshnessStatusUnknown || freshness.Reason != FreshnessReasonTimeout || freshness.BehindCount != nil {
		t.Fatalf("freshness=%+v want unknown timeout with no count", freshness)
	}
	if elapsed > 150*time.Millisecond {
		t.Fatalf("20ms sample deadline returned after %s: a probe descendant held the output pipes", elapsed)
	}
}

// The creation refresh's default-ref read runs under the same fixed deadline
// with the same bounded runner: a wedged probe refuses the creation with the
// typed deadline failure instead of hanging the caller.
func TestWorkBootstrapRefreshDeadlineBoundsProbeDescendants(t *testing.T) {
	original := freshnessPreflightTimeout
	freshnessPreflightTimeout = 20 * time.Millisecond
	t.Cleanup(func() { freshnessPreflightTimeout = original })
	writeDeadlineProbeShim(t)
	start := time.Now()
	preflight, err := startCreationPreflight(context.Background(), ExecGitRunner{}, t.TempDir())
	preflight.close()
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected the deadline to refuse the creation refresh")
	}
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindGitUnreachable {
		t.Fatalf("err=%v want a typed git-unreachable refusal", err)
	}
	if elapsed > 150*time.Millisecond {
		t.Fatalf("20ms creation deadline returned after %s: a probe descendant held the output pipes", elapsed)
	}
}

// RunNoninteractive must end inside the caller's deadline even when a
// descendant of the git process holds the output pipes open: the runner
// kills the process group and bounds the pipe drainage, so a wedged child
// cannot stretch a 20ms budget to the descendant's own lifetime.
func TestRunNoninteractiveBoundsDescendantHeldPipes(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nsleep 0.5 &\nwait\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := (ExecGitRunner{}).RunNoninteractive(ctx, t.TempDir(), "fetch", "origin", "main")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected the deadline to cancel the fetch")
	}
	if elapsed > 150*time.Millisecond {
		t.Fatalf("20ms deadline returned after %s: the runner waited for a descendant-held output pipe: %v", elapsed, err)
	}
}

// A caller-supplied ref stays an exact pin even when that ref is HEAD: the
// capture pins the repository's current HEAD, not the refreshed origin
// default head, which only an omitted ref selects.
func TestWorkBootstrapCaptureExplicitHEADStaysExactPin(t *testing.T) {
	repo, advanceDefault := initFreshnessOriginRepo(t)
	s := openTemp(t)
	seedBootstrapStoreAuthority(t, s, repo)
	head := runFreshnessGit(t, repo, "rev-parse", "HEAD")
	advanceDefault("origin moved past explicit HEAD")
	req := bootstrapStoreRequest()
	req.Ref = "HEAD"
	result, err := s.BootstrapWorktree(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Entry.BaseSHA != head {
		t.Fatalf("explicit HEAD capture base=%s want the repository HEAD %s", result.Entry.BaseSHA, head)
	}
}

// The sample counts between the two sampled commit SHAs, not between mutable
// refs, and one default-ref read drives both the fetch and the sample. A
// nested default branch flows through the same refspec and sample.
func TestSampleWorktreeFreshnessCountsSampledSHAs(t *testing.T) {
	headSHA := strings.Repeat("1", 40)
	defaultSHA := strings.Repeat("2", 40)
	runner := &samplingFreshnessRunner{
		responses: map[string][]byte{
			"rev-parse --git-dir":                                            []byte(".git\n"),
			"symbolic-ref --quiet refs/remotes/origin/HEAD":                  []byte("refs/remotes/origin/release/stable\n"),
			"rev-parse --verify HEAD^{commit}":                               []byte(headSHA + "\n"),
			"rev-parse --verify refs/remotes/origin/release/stable^{commit}": []byte(defaultSHA + "\n"),
			"rev-list --count " + headSHA + ".." + defaultSHA:                []byte("7\n"),
		},
	}
	freshness := SampleWorktreeFreshness(context.Background(), WorktreeEntry{Path: "/repo"}, runner)
	if freshness.Status != FreshnessStatusOK {
		t.Fatalf("freshness=%+v want ok", freshness)
	}
	if freshness.DefaultRef != "origin/release/stable" || freshness.DefaultSHA != defaultSHA || freshness.HeadSHA != headSHA {
		t.Fatalf("freshness=%+v want the nested default sampled at %s", freshness, defaultSHA)
	}
	if freshness.BehindCount == nil || *freshness.BehindCount != 7 {
		t.Fatalf("behind=%v want 7", freshness.BehindCount)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.fetchRefspecs) != 1 || runner.fetchRefspecs[0] != "+refs/heads/release/stable:refs/remotes/origin/release/stable" {
		t.Fatalf("fetchRefspecs=%v want the nested default branch refspec", runner.fetchRefspecs)
	}
	var counted []string
	for _, call := range runner.calls {
		if strings.Join(call, " ") == "rev-list --count "+headSHA+".."+defaultSHA {
			counted = call
		}
	}
	if counted == nil {
		t.Fatalf("no rev-list ran against the sampled SHAs: %v", runner.calls)
	}
}

// The fixed deadline covers every local probe of the sample, not only the
// fetch: a probe that outlives the budget reads as the typed timeout, not an
// unbounded wait.
func TestSampleWorktreeFreshnessTimeoutCoversProbes(t *testing.T) {
	original := freshnessPreflightTimeout
	freshnessPreflightTimeout = 20 * time.Millisecond
	t.Cleanup(func() { freshnessPreflightTimeout = original })
	runner := &blockingProbeRunner{calls: map[string][]byte{
		"rev-parse --git-dir":                           []byte(".git\n"),
		"symbolic-ref --quiet refs/remotes/origin/HEAD": []byte("refs/remotes/origin/main\n"),
	}}
	freshness := SampleWorktreeFreshness(context.Background(), WorktreeEntry{Path: "/repo"}, runner)
	if freshness.Status != FreshnessStatusUnknown || freshness.Reason != FreshnessReasonTimeout || freshness.BehindCount != nil {
		t.Fatalf("freshness=%+v want unknown timeout with no count", freshness)
	}
}

// samplingFreshnessRunner answers canned output per joined argv, records the
// noninteractive fetch refspecs, and fails any unmodelled command.
type samplingFreshnessRunner struct {
	responses     map[string][]byte
	calls         [][]string
	fetchRefspecs []string
	mu            sync.Mutex
}

func (r *samplingFreshnessRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, args)
	if out, ok := r.responses[strings.Join(args, " ")]; ok {
		return out, nil
	}
	return nil, fmt.Errorf("unmodelled git invocation: %s", strings.Join(args, " "))
}

func (r *samplingFreshnessRunner) RunNoninteractive(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, args)
	if args[0] == "fetch" {
		r.fetchRefspecs = append(r.fetchRefspecs, args[len(args)-1])
		return nil, nil
	}
	return nil, fmt.Errorf("unmodelled noninteractive git invocation: %s", strings.Join(args, " "))
}

// blockingProbeRunner answers the discovery probes and blocks every later
// command until its context is done, the shape a wedged local probe takes.
type blockingProbeRunner struct{ calls map[string][]byte }

func (r *blockingProbeRunner) Run(ctx context.Context, _ string, args ...string) ([]byte, error) {
	if out, ok := r.calls[strings.Join(args, " ")]; ok {
		return out, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (r *blockingProbeRunner) RunNoninteractive(ctx context.Context, _ string, args ...string) ([]byte, error) {
	if args[0] == "fetch" {
		return nil, nil
	}
	return r.Run(ctx, "", args...)
}

// shortenFreshnessBudget shrinks the fixed preflight deadline for one test,
// so a deadline regression fails in milliseconds instead of seconds.
func shortenFreshnessBudget(t *testing.T) {
	t.Helper()
	original := freshnessPreflightTimeout
	freshnessPreflightTimeout = 100 * time.Millisecond
	t.Cleanup(func() { freshnessPreflightTimeout = original })
}

// freshnessProbeDeadlineSlack bounds how far past the shrunken budget a
// bounded route may return before the test reads the wait as unbounded. The
// budget leaves a local fetch room to finish, so the blocked base probe is
// deterministically the call the deadline kills.
const freshnessProbeDeadlineSlack = 300 * time.Millisecond

// slowBaseProbeRunner answers every probe through the wrapped runner except
// the creation base's rev-parse, which blocks until its context ends: the
// shape a wedged local base probe takes on a host.
type slowBaseProbeRunner struct{ GitRunner }

func (r slowBaseProbeRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if strings.Join(args, " ") == "rev-parse --verify refs/remotes/origin/main^{commit}" {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return r.GitRunner.Run(ctx, dir, args...)
}

// assertCreationProbeRefusal asserts the shared-deadline refusal: typed, inside
// the shrunken budget's slack, and free of native and durable effects.
func assertCreationProbeRefusal(t *testing.T, err error, elapsed time.Duration) {
	t.Helper()
	if err == nil {
		t.Fatal("expected the preflight deadline to refuse the creation")
	}
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindGitUnreachable {
		t.Fatalf("err=%v want a typed git-unreachable refusal", err)
	}
	if elapsed > freshnessProbeDeadlineSlack {
		t.Fatalf("creation base probe escaped the shrunken preflight deadline: elapsed=%s error=%v", elapsed, err)
	}
}

// The first direct claim's base-resolution probes share the preflight's fixed
// deadline: a base-SHA probe that outlives the budget refuses the claim inside
// the budget instead of waiting out the caller's context.
func TestClaimCreationBaseProbeSharesPreflightDeadline(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	shortenFreshnessBudget(t)
	req := baseClaim(git)
	req.Runner = slowBaseProbeRunner{git}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	_, _, err := s.PrepareWorktreeClaimNative(ctx, req)
	assertCreationProbeRefusal(t, err, time.Since(start))
	if _, held := git.worktrees[claimPath(s)]; held {
		t.Fatal("the refused claim created the worktree")
	}
	if _, held := git.branches[claimBranch()]; held {
		t.Fatal("the refused claim created the branch")
	}
	var claims int
	if err := s.db.QueryRow(`SELECT count(*) FROM worktree_claims`).Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if claims != 0 {
		t.Fatalf("the refused claim left %d claim rows", claims)
	}
}

// The capture's base-resolution probes share the preflight deadline the same
// way: a base probe wedged past the budget refuses the capture inside the
// budget with no work item, claim, branch, or worktree.
func TestCaptureCreationBaseProbeSharesPreflightDeadline(t *testing.T) {
	repo, _ := initFreshnessOriginRepo(t)
	s := openTemp(t)
	seedBootstrapStoreAuthority(t, s, repo)
	shortenFreshnessBudget(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	_, err := s.bootstrapWorktree(ctx, bootstrapStoreRequest(), nil, slowBaseProbeRunner{ExecGitRunner{}})
	assertCreationProbeRefusal(t, err, time.Since(start))
	for _, table := range []string{"work_items", "worktree_claims"} {
		var count int
		if err := s.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s holds %d rows after the probe refusal", table, count)
		}
	}
	if listing := runFreshnessGit(t, repo, "worktree", "list", "--porcelain"); strings.Count(listing, "\nworktree ")+boolToInt(strings.HasPrefix(listing, "worktree ")) != 1 {
		t.Fatalf("the probe refusal left a linked worktree behind:\n%s", listing)
	}
}

// The cross-Project claim's base-resolution probes share the preflight
// deadline too: a base probe wedged past the budget refuses the claim inside
// the budget with no claim row, branch, or worktree in the second Project.
func TestCrossProjectClaimBaseProbeSharesPreflightDeadline(t *testing.T) {
	repoA, _ := initFreshnessOriginRepo(t)
	repoB, _ := initFreshnessOriginRepo(t)
	s := openTemp(t)
	seedBootstrapStoreAuthority(t, s, repoA)
	seedBootstrapProject(t, s, "project-fresh-b", repoB)
	first, err := s.BootstrapWorktree(context.Background(), bootstrapStoreRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	memberships, marshalErr := json.Marshal(workMembershipsPayload{Memberships: []workMembershipPayload{
		{ProjectID: "project-bootstrap", Role: "primary"},
		{ProjectID: "project-fresh-b", Role: "secondary"},
	}, ExpectedVersion: first.WorkVersion, ResultingVersion: first.WorkVersion + 1})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{{
		EventID: first.WorkID + "-memberships-b", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: first.WorkID,
		Actor: "operator", OccurredAt: time.Unix(5, 0).UTC(), PayloadVersion: 1, Payload: memberships,
	}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, first.WorkID): first.WorkVersion}}); err != nil {
		t.Fatal(err)
	}
	shortenFreshnessBudget(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	_, err = s.claimCrossProjectWorktree(ctx, ExistingBootstrapRequest{
		ProductID: "product-bootstrap", ProjectID: "project-fresh-b", WorkID: first.WorkID,
	}, nil, slowBaseProbeRunner{ExecGitRunner{}})
	assertCreationProbeRefusal(t, err, time.Since(start))
	var claims int
	if err := s.db.QueryRow(`SELECT count(*) FROM worktree_claims WHERE project_id='project-fresh-b'`).Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if claims != 0 {
		t.Fatalf("the refused cross-Project claim left %d claim rows", claims)
	}
	if branches := runFreshnessGit(t, repoB, "branch", "--format=%(refname:short)"); strings.Contains(branches, "work/") {
		t.Fatalf("the refused cross-Project claim left a branch behind: %q", branches)
	}
}

// The preflight's fetch must never move a local branch tip through a
// symbolic ref: a tracking ref that points at a local branch stays a
// remote-tracking update only, and the parked branch keeps its tip.
func TestFreshnessPreflightNeverMovesLocalTipThroughSymref(t *testing.T) {
	repo, advanceDefault := initFreshnessOriginRepo(t)
	before := runFreshnessGit(t, repo, "rev-parse", "HEAD")
	runFreshnessGit(t, repo, "branch", "parked", before)
	runFreshnessGit(t, repo, "symbolic-ref", "refs/remotes/origin/main", "refs/heads/parked")
	advanceDefault("remote advanced")
	preflight, err := startCreationPreflight(context.Background(), ExecGitRunner{}, repo)
	preflight.close()
	after := runFreshnessGit(t, repo, "rev-parse", "refs/heads/parked")
	if after != before {
		t.Fatalf("freshness moved local branch tip: before=%s after=%s refresh_error=%v", before, after, err)
	}
}

// RunNoninteractive refuses every credential prompt the supported transports
// can open: terminal prompts off, and askpass programs that answer empty so
// authentication fails instead of blocking.
func TestExecGitRunnerNoninteractiveEnv(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\nprintf '%s|%s|%s|%s\\n' \"$GIT_TERMINAL_PROMPT\" \"$SSH_ASKPASS_REQUIRE\" \"$SSH_ASKPASS\" \"$GIT_ASKPASS\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := ExecGitRunner{}.RunNoninteractive(context.Background(), t.TempDir(), "fetch", "origin", "main")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "0|force|/bin/true|/bin/true" {
		t.Fatalf("noninteractive env=%q want refused prompts with empty-output askpass", got)
	}
}

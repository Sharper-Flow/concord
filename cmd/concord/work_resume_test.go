package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

func resumeCLI(t *testing.T, s *store.Store, directory, workID string) (int, workResumeOutput, string) {
	t.Helper()
	raw, err := json.Marshal(workResumeInput{ProductID: "product-wl", ProjectID: "project-wl", WorkID: workID})
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(directory)
	var out, errOut bytes.Buffer
	code := runWorkResume(raw, s, &out, &errOut)
	var parsed workResumeOutput
	_ = json.Unmarshal(out.Bytes(), &parsed)
	return code, parsed, errOut.String()
}

func transitionWorkItem(t *testing.T, s *store.Store, workID string, from, to string, version int64) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"from": from, "to": to, "reason": "fixture transition", "expected_version": version, "resulting_version": version + 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{{EventID: "resume-transition-" + to + "-" + workID, Kind: "work.transitioned", SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(10, 0).UTC(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkResumeDerivesActiveEntryFromDefaultCheckout(t *testing.T) {
	repo := initLocatorRepo(t)
	s := mustOpenStore(t, filepath.Join(t.TempDir(), "concord.db"))
	seedLocatorAuthority(t, s, repo)
	origin, err := s.BootstrapWorktree(context.Background(), bootstrapRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	code, output, stderr := resumeCLI(t, s, repo, origin.WorkID)
	if code != 0 {
		t.Fatalf("resume code=%d stderr=%q", code, stderr)
	}
	if output.SchemaVersion != "1.0" || output.WorkID != origin.WorkID || output.ProductID != "product-wl" || output.ProjectID != "project-wl" {
		t.Fatalf("resume output=%+v", output)
	}
	if output.Worktree.Path != origin.Entry.Path || output.Worktree.State != "active" || output.Worktree.Branch != origin.Entry.Branch {
		t.Fatalf("resume worktree=%+v want entry=%+v", output.Worktree, origin.Entry)
	}
}

// locatorOriginPath reads the local bare repository the fixture's insteadOf
// mapping hides behind the well-formed remote URL. Git reports the config
// key's section and name lowercased, so the suffix parse is case-insensitive.
func locatorOriginPath(t *testing.T, repo string) string {
	t.Helper()
	mapped := gitOutput(t, repo, "config", "--get-regexp", `^url\..*\.insteadOf$`)
	fields := strings.Fields(mapped)
	if len(fields) != 2 || !strings.HasPrefix(strings.ToLower(fields[0]), "url.") || !strings.HasSuffix(strings.ToLower(fields[0]), ".insteadof") {
		t.Fatalf("fixture origin mapping missing: %q", mapped)
	}
	return fields[0][len("url.") : len(fields[0])-len(".insteadof")]
}

// pushLocatorOriginCommit advances the real origin by one commit from a
// throwaway clone and returns its SHA, leaving the working repository's
// remote-tracking cache stale until a fetch runs. The clone carries its own
// synthetic identity, so the commit never depends on the host's Git config.
func pushLocatorOriginCommit(t *testing.T, repo, message string) string {
	t.Helper()
	origin := locatorOriginPath(t, repo)
	seedParent := t.TempDir()
	seed := filepath.Join(seedParent, "seed")
	gitOutput(t, seedParent, "clone", "-q", origin, "seed")
	gitOutput(t, seed, "config", "user.name", "t")
	gitOutput(t, seed, "config", "user.email", "t@t")
	if err := os.WriteFile(filepath.Join(seed, message), []byte(message+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOutput(t, seed, "add", ".")
	gitOutput(t, seed, "commit", "-q", "-m", message)
	gitOutput(t, seed, "push", "-q", "origin", "main")
	head := strings.TrimSpace(gitOutput(t, seed, "rev-parse", "HEAD"))
	return head
}

// TestPushLocatorOriginCommitNeedsNoHostGitIdentity runs the origin-advance
// fixture with every host identity source removed, the way a clean CI runner
// presents Git, and proves the pushed commit reaches the origin.
func TestPushLocatorOriginCommitNeedsNoHostGitIdentity(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, name := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "EMAIL"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	repo := initLocatorRepo(t)
	pushed := pushLocatorOriginCommit(t, repo, "isolated-identity")
	if got := strings.TrimSpace(gitOutput(t, locatorOriginPath(t, repo), "rev-parse", "refs/heads/main")); got != pushed {
		t.Fatalf("origin main = %q, want pushed commit %q", got, pushed)
	}
}

// goOfflineLocatorOrigin swaps the insteadOf mapping onto a removed local
// path, so every fetch fails the same way an unreachable remote does, with
// no network dependency.
func goOfflineLocatorOrigin(t *testing.T, repo string) {
	t.Helper()
	mapped := gitOutput(t, repo, "config", "--get-regexp", `^url\..*\.insteadOf$`)
	fields := strings.Fields(mapped)
	if len(fields) != 2 {
		t.Fatalf("fixture origin mapping missing: %q", mapped)
	}
	gitOutput(t, repo, "config", "--unset", fields[0])
	gitOutput(t, repo, "config", "url."+filepath.Join(t.TempDir(), "gone.git")+".insteadOf", fields[1])
}

// validateBranchFreshnessContract proves one emitted freshness sample against
// the owning branch-freshness contract projection.
func validateBranchFreshnessContract(t *testing.T, freshness *store.BranchFreshness) {
	t.Helper()
	if freshness == nil {
		t.Fatal("resume carried no branch_freshness section")
	}
	raw, err := json.Marshal(freshness)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateBranchFreshnessObject(decoded); err != nil {
		t.Fatalf("emitted branch_freshness diverges from the owning contract: %v", err)
	}
}

// The resume reports the exact lag between the worktree's branch and the
// refreshed origin default branch, so the agent rebases deliberately.
func TestWorkResumeReportsExactBranchLag(t *testing.T) {
	repo := initLocatorRepo(t)
	s := mustOpenStore(t, filepath.Join(t.TempDir(), "concord.db"))
	seedLocatorAuthority(t, s, repo)
	origin, err := s.BootstrapWorktree(context.Background(), bootstrapRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	pushLocatorOriginCommit(t, repo, "origin commit one")
	second := pushLocatorOriginCommit(t, repo, "origin commit two")
	code, output, stderr := resumeCLI(t, s, repo, origin.WorkID)
	if code != 0 {
		t.Fatalf("resume code=%d stderr=%q", code, stderr)
	}
	freshness := output.BranchFreshness
	validateBranchFreshnessContract(t, freshness)
	if freshness == nil || freshness.Status != "ok" || freshness.BehindCount == nil || *freshness.BehindCount != 2 {
		t.Fatalf("branch freshness=%+v want ok with behind=2", freshness)
	}
	if freshness.DefaultRef != "origin/main" || freshness.DefaultSHA != second {
		t.Fatalf("branch freshness default=%q %s want origin/main at %s", freshness.DefaultRef, freshness.DefaultSHA, second)
	}
	if head := gitOutput(t, origin.Entry.Path, "rev-parse", "HEAD"); freshness.HeadSHA != strings.TrimSpace(head) {
		t.Fatalf("branch freshness head=%s want the worktree HEAD %s", freshness.HeadSHA, head)
	}
}

// A failed refresh degrades the freshness section to the typed unknown with
// no count, and the resume itself still succeeds.
func TestWorkResumeFreshnessDegradesOfflineWithoutBlocking(t *testing.T) {
	repo := initLocatorRepo(t)
	s := mustOpenStore(t, filepath.Join(t.TempDir(), "concord.db"))
	seedLocatorAuthority(t, s, repo)
	origin, err := s.BootstrapWorktree(context.Background(), bootstrapRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	goOfflineLocatorOrigin(t, repo)
	code, output, stderr := resumeCLI(t, s, repo, origin.WorkID)
	if code != 0 {
		t.Fatalf("resume code=%d stderr=%q", code, stderr)
	}
	freshness := output.BranchFreshness
	validateBranchFreshnessContract(t, freshness)
	if freshness == nil || freshness.Status != "unknown" || freshness.BehindCount != nil || freshness.Reason != "fetch_failed" {
		t.Fatalf("branch freshness=%+v want unknown fetch_failed with no count", freshness)
	}
}

func TestWorkResumeRefusesTerminalAndUnknownButBootstrapsUnclaimedWork(t *testing.T) {
	repo := initLocatorRepo(t)
	s := mustOpenStore(t, filepath.Join(t.TempDir(), "concord.db"))
	seedLocatorAuthority(t, s, repo)

	if code, _, stderr := resumeCLI(t, s, repo, "work-missing"); code == 0 || !strings.Contains(stderr, "does not exist") {
		t.Fatalf("unknown work code=%d stderr=%q", code, stderr)
	}
	// work-wl exists from the fixture but holds no worktree claim. Start now
	// bootstraps its first canonical worktree under the original work identity.
	if code, output, stderr := resumeCLI(t, s, repo, "work-wl"); code != 0 || output.WorkID != "work-wl" || output.Worktree.State != "active" {
		t.Fatalf("unclaimed work code=%d output=%+v stderr=%q", code, output, stderr)
	}

	origin, err := s.BootstrapWorktree(context.Background(), bootstrapRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	transitionWorkItem(t, s, origin.WorkID, "needed", "cancelled", origin.WorkVersion)
	if code, _, stderr := resumeCLI(t, s, repo, origin.WorkID); code == 0 || !strings.Contains(stderr, "terminal work item") {
		t.Fatalf("terminal work code=%d stderr=%q", code, stderr)
	}
}

func TestWorkResumeBootstrapsExistingIdentityAndRecoversAfterNativeCreate(t *testing.T) {
	repo := initLocatorRepo(t)
	s := mustOpenStore(t, filepath.Join(t.TempDir(), "concord.db"))
	seedLocatorAuthority(t, s, repo)
	ctx := context.Background()
	var beforeVersion, beforeEvents int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, "work-wl").Scan(&beforeVersion); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=?`, "work-wl").Scan(&beforeEvents); err != nil {
		t.Fatal(err)
	}
	req := store.ExistingBootstrapRequest{ProductID: "product-wl", ProjectID: "project-wl", WorkID: "work-wl"}
	if _, err := s.BootstrapExistingWorktree(ctx, req, func(phase string) error {
		if phase == "after_native_create" {
			return errors.New("injected native-create interruption")
		}
		return nil
	}); err == nil {
		t.Fatal("interrupted existing bootstrap was accepted")
	}
	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT state FROM bootstrap_operations WHERE work_id=?`, "work-wl").Scan(&state); err != nil || state != "native_ready" {
		t.Fatalf("existing bootstrap state=%q err=%v", state, err)
	}
	var afterVersion, afterEvents int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, "work-wl").Scan(&afterVersion); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=?`, "work-wl").Scan(&afterEvents); err != nil {
		t.Fatal(err)
	}
	if afterVersion != beforeVersion || afterEvents != beforeEvents {
		t.Fatalf("interrupted existing bootstrap changed work identity: version %d/%d events %d/%d", beforeVersion, afterVersion, beforeEvents, afterEvents)
	}
	completed, err := s.BootstrapExistingWorktree(ctx, req, nil)
	if err != nil || completed.WorkID != "work-wl" || completed.WorkVersion != beforeVersion+1 || completed.Entry.State != "active" {
		t.Fatalf("recovered existing bootstrap=%+v err=%v", completed, err)
	}
	reused, err := s.BootstrapExistingWorktree(ctx, req, nil)
	if err != nil || !reused.Replayed || reused.Entry.Path != completed.Entry.Path {
		t.Fatalf("active existing bootstrap reuse=%+v err=%v", reused, err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=?`, "work-wl").Scan(&afterEvents); err != nil {
		t.Fatal(err)
	}
	if afterEvents != beforeEvents+1 {
		t.Fatalf("existing bootstrap event count=%d want %d", afterEvents, beforeEvents+1)
	}
}

func TestWorkResumeReclaimsCompletedBootstrapAndStartsAgain(t *testing.T) {
	repo := initLocatorRepo(t)
	s := mustOpenStore(t, filepath.Join(t.TempDir(), "concord.db"))
	seedLocatorAuthority(t, s, repo)
	ctx := context.Background()
	req := store.ExistingBootstrapRequest{ProductID: "product-wl", ProjectID: "project-wl", WorkID: "work-wl"}
	first, err := s.BootstrapExistingWorktree(ctx, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReclaimWorktree(ctx, store.WorktreeReclaimRequest{
		WorkID: first.WorkID, ProjectID: first.ProjectID, DefaultRef: "origin/main",
		PrincipalRef: "principal/operator", RequestID: "reclaim-work-wl",
		ExpectedVersion: first.WorkVersion, Runner: store.ExecGitRunner{},
	}); err != nil {
		t.Fatal(err)
	}
	second, err := s.BootstrapExistingWorktree(ctx, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.WorkID != first.WorkID || second.OperationID != first.OperationID || second.WorkVersion != first.WorkVersion+2 || second.Entry.State != "active" {
		t.Fatalf("restarted bootstrap=%+v first=%+v", second, first)
	}
	var activeClaims, activeEntries int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worktree_claims WHERE work_id=? AND state IN ('pending','verified')`, first.WorkID).Scan(&activeClaims); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worktree_entries WHERE set_id=? AND state='active'`, store.WorktreeSetID(first.WorkID)).Scan(&activeEntries); err != nil {
		t.Fatal(err)
	}
	if activeClaims != 1 || activeEntries != 1 {
		t.Fatalf("active claims=%d entries=%d", activeClaims, activeEntries)
	}
}

func TestWorkResumeReclaimsBootstrapAndAdoptsAdvancedCanonicalBranch(t *testing.T) {
	repo := initLocatorRepo(t)
	s := mustOpenStore(t, filepath.Join(t.TempDir(), "concord.db"))
	seedLocatorAuthority(t, s, repo)
	ctx := context.Background()
	req := store.ExistingBootstrapRequest{ProductID: "product-wl", ProjectID: "project-wl", WorkID: "work-wl"}
	first, err := s.BootstrapExistingWorktree(ctx, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	baseTree := strings.TrimSpace(gitOutput(t, repo, "rev-parse", first.Entry.BaseSHA+"^{tree}"))
	commit := exec.Command("git", "-C", repo, "commit-tree", baseTree, "-p", first.Entry.BaseSHA, "-m", "advanced canonical branch")
	commit.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	advancedOutput, err := commit.CombinedOutput()
	if err != nil {
		t.Fatalf("advance canonical branch: %v\n%s", err, advancedOutput)
	}
	advancedSHA := strings.TrimSpace(string(advancedOutput))
	for _, ref := range []string{"refs/heads/" + first.Entry.Branch, "refs/remotes/origin/main"} {
		if output, err := exec.Command("git", "-C", repo, "update-ref", ref, advancedSHA).CombinedOutput(); err != nil {
			t.Fatalf("advance %s: %v\n%s", ref, err, output)
		}
	}
	if _, err := s.ReclaimWorktree(ctx, store.WorktreeReclaimRequest{
		WorkID: first.WorkID, ProjectID: first.ProjectID, DefaultRef: "origin/main",
		PrincipalRef: "principal/operator", RequestID: "reclaim-advanced-work-wl",
		ExpectedVersion: first.WorkVersion, Runner: store.ExecGitRunner{},
	}); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", repo, "update-ref", "refs/heads/"+first.Entry.Branch, advancedSHA).CombinedOutput(); err != nil {
		t.Fatalf("advance canonical branch: %v\n%s", err, output)
	}
	var beforeEvents int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=?`, first.WorkID).Scan(&beforeEvents); err != nil {
		t.Fatal(err)
	}

	second, err := s.BootstrapExistingWorktree(ctx, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.WorkID != first.WorkID || second.OperationID != first.OperationID || second.Entry.State != "active" {
		t.Fatalf("restarted bootstrap=%+v first=%+v", second, first)
	}
	if second.Entry.BaseSHA != advancedSHA {
		t.Fatalf("restarted base=%s want advanced branch head %s", second.Entry.BaseSHA, advancedSHA)
	}
	if head := strings.TrimSpace(gitOutput(t, second.Entry.Path, "rev-parse", "HEAD")); head != advancedSHA {
		t.Fatalf("restarted worktree head=%s want %s", head, advancedSHA)
	}
	var afterEvents int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=?`, first.WorkID).Scan(&afterEvents); err != nil {
		t.Fatal(err)
	}
	if afterEvents != beforeEvents+1 {
		t.Fatalf("restart events=%d want %d", afterEvents, beforeEvents+1)
	}
}

func TestWorkResumeConcurrentExistingStartsConvergeOnOneClaim(t *testing.T) {
	repo := initLocatorRepo(t)
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	s := mustOpenStore(t, dbPath)
	seedLocatorAuthority(t, s, repo)
	req := store.ExistingBootstrapRequest{ProductID: "product-wl", ProjectID: "project-wl", WorkID: "work-wl"}
	results := make([]store.BootstrapResult, 2)
	errors := make([]error, 2)
	var group sync.WaitGroup
	for index := range results {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			local, openErr := store.Open(context.Background(), dbPath)
			if openErr != nil {
				errors[index] = openErr
				return
			}
			defer local.Close()
			results[index], errors[index] = local.BootstrapExistingWorktree(context.Background(), req, nil)
		}(index)
	}
	group.Wait()
	if results[0].WorkID != "work-wl" && results[1].WorkID != "work-wl" {
		t.Fatalf("concurrent existing bootstrap results=%+v errors=%v", results, errors)
	}
	var operations, claims, entries int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM bootstrap_operations WHERE work_id=?`, "work-wl").Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worktree_claims WHERE work_id=? AND state IN ('pending','verified')`, "work-wl").Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worktree_entries WHERE set_id=? AND state='active'`, store.WorktreeSetID("work-wl")).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if operations != 1 || claims != 1 || entries != 1 {
		t.Fatalf("operations=%d claims=%d entries=%d", operations, claims, entries)
	}
}

func TestWorkResumeAppliesTheBootstrapOriginGate(t *testing.T) {
	repo := initLocatorRepo(t)
	s := mustOpenStore(t, filepath.Join(t.TempDir(), "concord.db"))
	seedLocatorAuthority(t, s, repo)
	first, err := s.BootstrapWorktree(context.Background(), func() store.BootstrapRequest {
		request := bootstrapRequest()
		request.IdempotencyKey = "resume-origin-first"
		return request
	}(), nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.BootstrapWorktree(context.Background(), func() store.BootstrapRequest {
		request := bootstrapRequest()
		request.IdempotencyKey = "resume-origin-second"
		return request
	}(), nil)
	if err != nil {
		t.Fatal(err)
	}

	// A live, clean origin worktree is admitted (CD-0110 D1 as amended for
	// issue #896): a session may leave one item's worktree for another.
	code, output, stderr := resumeCLI(t, s, first.Entry.Path, second.WorkID)
	if code != 0 {
		t.Fatalf("live clean origin resume code=%d stderr=%q", code, stderr)
	}
	if output.Worktree.Path != second.Entry.Path {
		t.Fatalf("live origin resume target=%+v", output)
	}

	if err := os.WriteFile(filepath.Join(first.Entry.Path, "dirty.txt"), []byte("keep here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(filepath.Join(first.Entry.Path, "dirty.txt")) }()
	if code, _, stderr := resumeCLI(t, s, first.Entry.Path, second.WorkID); code == 0 || !strings.Contains(stderr, "dirty worktree") {
		t.Fatalf("dirty origin resume code=%d stderr=%q", code, stderr)
	}
}

// TestWorkResumeSameTargetSkipsTheOriginGate proves the convergent
// same-target move: a session that already runs in the item's own worktree
// resumes even when that worktree is dirty, because it chains from no origin.
func TestWorkResumeSameTargetSkipsTheOriginGate(t *testing.T) {
	repo := initLocatorRepo(t)
	s := mustOpenStore(t, filepath.Join(t.TempDir(), "concord.db"))
	seedLocatorAuthority(t, s, repo)
	origin, err := s.BootstrapWorktree(context.Background(), bootstrapRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(origin.Entry.Path, "dirty.txt"), []byte("in-progress\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(filepath.Join(origin.Entry.Path, "dirty.txt")) }()
	code, output, stderr := resumeCLI(t, s, origin.Entry.Path, origin.WorkID)
	if code != 0 {
		t.Fatalf("dirty same-target resume code=%d stderr=%q", code, stderr)
	}
	if output.Worktree.Path != origin.Entry.Path {
		t.Fatalf("same-target resume worktree=%+v want %s", output.Worktree, origin.Entry.Path)
	}
}

func TestSessionPrepareAcceptsEmptyTask(t *testing.T) {
	repo := initLocatorRepo(t)
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	s := mustOpenStore(t, dbPath)
	seedLocatorAuthority(t, s, repo)
	origin, err := s.BootstrapWorktree(context.Background(), bootstrapRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(dbOverrideEnv, dbPath)
	t.Chdir(origin.Entry.Path)
	var out, errOut bytes.Buffer
	code := runSessionPrepare(commandSessionPrepareInput(t, origin.WorkID, ""), s, &out, &errOut,
		func(string) error { return nil },
		hostCommandAt(defaultHostResolution()),
		func(context.Context, string, hostCommandResolution, string, string, string) (string, error) {
			return "agent", nil
		},
		func(context.Context, string, string, string) ([]byte, error) {
			return []byte(`{"watermark":"test"}`), nil
		})
	if code != 0 {
		t.Fatalf("empty task session-prepare code=%d stderr=%q", code, errOut.String())
	}
	var prepared sessionPrepareOutput
	if err := json.Unmarshal(out.Bytes(), &prepared); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prepared.Prompt, "Task:") {
		t.Fatalf("empty task prompt carries a task line: %q", prepared.Prompt)
	}
}

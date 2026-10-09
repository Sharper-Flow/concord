package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

func bootstrapRequest() store.BootstrapRequest {
	return store.BootstrapRequest{
		ProductID: "product-wl", ProjectID: "project-wl", Title: "Bootstrap work",
		ValueStatement: "A worktree is ready", Kind: "task", Task: "run the task",
		IdempotencyKey: "bootstrap-key", Priority: 1,
	}
}

func commandSessionPrepareInput(t *testing.T, workID, task string) []byte {
	t.Helper()
	raw, err := json.Marshal(sessionPrepareInput{ProductID: "product-wl", WorkID: workID, Task: task, Agent: "concord-1"})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func gitOutput(t *testing.T, repo string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

func TestWorkBootstrapExactReplayAndPendingRecovery(t *testing.T) {
	repo := initLocatorRepo(t)
	dbDir := t.TempDir()
	s, err := store.Open(context.Background(), filepath.Join(dbDir, "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedLocatorAuthority(t, s, repo)
	req := bootstrapRequest()

	failed, err := s.BootstrapWorktree(context.Background(), req, func(phase string) error {
		if phase == "after_prepare" {
			return errors.New("injected phase failure")
		}
		return nil
	})
	if err == nil || failed.WorkID != "" {
		t.Fatalf("phase failure result=%+v err=%v", failed, err)
	}
	operationID, workID, _, err := store.CanonicalBootstrapIdentity(req)
	if err != nil {
		t.Fatal(err)
	}
	location, err := s.LocateWorktree(context.Background(), req.ProjectID, workID, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(location.Path); !os.IsNotExist(err) {
		t.Fatalf("phase failure created native worktree: %v", err)
	}
	var journalState string
	if err := s.DatabaseForTesting().QueryRow("SELECT state FROM bootstrap_operations WHERE operation_id=?", operationID).Scan(&journalState); err != nil || journalState != "pending" {
		t.Fatalf("journal state=%q err=%v", journalState, err)
	}

	first, err := s.BootstrapWorktree(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.WorkID != workID || first.Entry.Path != location.Path || first.Entry.State != "active" {
		t.Fatalf("bootstrap result=%+v location=%+v", first, location)
	}
	if current := strings.TrimSpace(gitOutput(t, repo, "branch", "--show-current")); current != "main" {
		t.Fatalf("main checkout branch=%q", current)
	}
	if count := strings.Count(gitOutput(t, repo, "worktree", "list", "--porcelain"), "worktree "); count != 2 {
		t.Fatalf("native worktree count=%d", count)
	}

	req2 := req
	req2.IdempotencyKey = "bootstrap-key-2"
	if _, err := s.BootstrapWorktree(context.Background(), req2, func(phase string) error {
		if phase == "after_native_create" {
			return errors.New("injected finalize failure")
		}
		return nil
	}); err == nil {
		t.Fatal("injected finalize failure was ignored")
	}
	_, workID2, _, err := store.CanonicalBootstrapIdentity(req2)
	if err != nil {
		t.Fatal(err)
	}
	location2, err := s.LocateWorktree(context.Background(), req2.ProjectID, workID2, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(location2.Path); err != nil {
		t.Fatalf("injected finalize failure removed native worktree: %v", err)
	}
	var nativeState string
	if err := s.DatabaseForTesting().QueryRow("SELECT state FROM bootstrap_operations WHERE work_id=?", workID2).Scan(&nativeState); err != nil || nativeState != "native_ready" {
		t.Fatalf("native-ready journal state=%q err=%v", nativeState, err)
	}
	second, err := s.BootstrapWorktree(context.Background(), req2, nil)
	if err != nil || !second.Replayed || second.Entry.Path != location2.Path {
		t.Fatalf("pending native replay=%+v err=%v", second, err)
	}
	replay, err := s.BootstrapWorktree(context.Background(), req, nil)
	if err != nil || !replay.Replayed || replay.Entry.Path != first.Entry.Path {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var cliOutput workBootstrapOutput
	var cliErr bytes.Buffer
	var cliJSON bytes.Buffer
	t.Chdir(repo)
	if code := runWorkBootstrap(raw, s, &cliJSON, &cliErr); code != 0 || json.Unmarshal(cliJSON.Bytes(), &cliOutput) != nil {
		t.Fatalf("CLI bootstrap output code=%d stdout=%q stderr=%q", code, cliJSON.String(), cliErr.String())
	}
	if cliOutput.SchemaVersion != "1.0" || cliOutput.OperationID != first.OperationID || cliOutput.WorkVersion != first.WorkVersion || cliOutput.Worktree.State != "active" {
		t.Fatalf("CLI bootstrap output=%+v", cliOutput)
	}
	if _, err := s.BootstrapWorktree(context.Background(), store.BootstrapRequest{
		ProductID: req.ProductID, ProjectID: req.ProjectID, Title: req.Title,
		ValueStatement: req.ValueStatement, Kind: req.Kind, Task: "different task",
		IdempotencyKey: req.IdempotencyKey, Priority: req.Priority,
	}, nil); err == nil {
		t.Fatal("mismatched idempotency request accepted")
	}
}

func TestWorkBootstrapRefusesExistingUnattributedBranch(t *testing.T) {
	repo := initLocatorRepo(t)
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedLocatorAuthority(t, s, repo)
	req := bootstrapRequest()
	req.IdempotencyKey = "bootstrap-branch-created"
	_, workID, _, err := store.CanonicalBootstrapIdentity(req)
	if err != nil {
		t.Fatal(err)
	}
	location, err := s.LocateWorktree(context.Background(), req.ProjectID, workID, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", repo, "branch", location.Branch, location.BaseSHA).CombinedOutput(); err != nil {
		t.Fatalf("create branch: %v\n%s", err, output)
	}
	if _, err := s.BootstrapWorktree(context.Background(), req, nil); err == nil {
		t.Fatal("pre-existing branch was adopted")
	}
	var journals int
	if err := s.DatabaseForTesting().QueryRow("SELECT count(*) FROM bootstrap_operations WHERE idempotency_key=?", req.IdempotencyKey).Scan(&journals); err != nil || journals != 0 {
		t.Fatalf("refused branch journal count=%d err=%v", journals, err)
	}
	if current := strings.TrimSpace(gitOutput(t, repo, "branch", "--show-current")); current != "main" {
		t.Fatalf("main checkout branch=%q", current)
	}
}

func TestWorkBootstrapRefusesNonDefaultMainCheckoutBeforeJournal(t *testing.T) {
	repo := initLocatorRepo(t)
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedLocatorAuthority(t, s, repo)
	if output, err := exec.Command("git", "-C", repo, "switch", "-q", "-c", "feature").CombinedOutput(); err != nil {
		t.Fatalf("switch feature branch: %v\n%s", err, output)
	}
	req := bootstrapRequest()
	req.IdempotencyKey = "bootstrap-non-default"
	if _, err := s.BootstrapWorktree(context.Background(), req, nil); err == nil {
		t.Fatal("non-default main checkout was accepted")
	}
	var journals int
	if err := s.DatabaseForTesting().QueryRow("SELECT count(*) FROM bootstrap_operations").Scan(&journals); err != nil || journals != 0 {
		t.Fatalf("non-default checkout journal count=%d err=%v", journals, err)
	}
}

func TestWorkBootstrapRefusesPlantedCanonicalWorktreeWithCommits(t *testing.T) {
	repo := initLocatorRepo(t)
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedLocatorAuthority(t, s, repo)
	req := bootstrapRequest()
	req.IdempotencyKey = "bootstrap-planted"
	_, workID, _, err := store.CanonicalBootstrapIdentity(req)
	if err != nil {
		t.Fatal(err)
	}
	location, err := s.LocateWorktree(context.Background(), req.ProjectID, workID, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", repo, "worktree", "add", location.Path, "-b", location.Branch, location.BaseSHA).CombinedOutput(); err != nil {
		t.Fatalf("create planted worktree: %v\n%s", err, output)
	}
	if err := os.WriteFile(filepath.Join(location.Path, "planted.txt"), []byte("not bootstrap provenance\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "planted.txt"}, {"commit", "-q", "-m", "planted"}} {
		command := exec.Command("git", append([]string{"-C", location.Path}, args...)...)
		command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	if _, err := s.BootstrapWorktree(context.Background(), req, nil); err == nil {
		t.Fatal("planted canonical worktree was adopted")
	}
	var journals int
	if err := s.DatabaseForTesting().QueryRow("SELECT count(*) FROM bootstrap_operations").Scan(&journals); err != nil || journals != 0 {
		t.Fatalf("planted worktree journal count=%d err=%v", journals, err)
	}
}

func TestWorkBootstrapRequiresRequestedProjectMainCheckout(t *testing.T) {
	repoA := initLocatorRepo(t)
	repoB := initLocatorRepo(t)
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedLocatorAuthority(t, s, repoA)
	if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: productProjectMembershipEvents("other", "product-other", "project-other", "other"), ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-other"): 0, store.VersionRef(store.SubjectProject, "project-other"): 0}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProjectLocator(context.Background(), "project-other", store.ProjectLocator{ID: "other-path", Kind: store.LocatorCanonicalPath, Value: repoB}, 1); err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(workBootstrapInput{ProductID: "product-other", ProjectID: "project-other", Title: "Mismatch", ValueStatement: "Refuse mismatch", Kind: "task", Task: "run", IdempotencyKey: "mismatch-key"})
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repoA)
	var out, errOut bytes.Buffer
	if code := runWorkBootstrap(input, s, &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "requested Project") {
		t.Fatalf("two-Project mismatch code=%d stderr=%q", code, errOut.String())
	}
	var journalCount int
	if err := s.DatabaseForTesting().QueryRow("SELECT count(*) FROM bootstrap_operations").Scan(&journalCount); err != nil || journalCount != 0 {
		t.Fatalf("mismatch wrote journal count=%d err=%v", journalCount, err)
	}

	req := bootstrapRequest()
	result, err := s.BootstrapWorktree(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(result.Entry.Path)
	req.IdempotencyKey = "live-origin-admitted"
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	out, errOut = bytes.Buffer{}, bytes.Buffer{}
	if code := runWorkBootstrap(raw, s, &out, &errOut); code != 0 {
		t.Fatalf("linked-worktree invocation code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestWorkBootstrapChainsFromCleanTerminalWorktreeAtDefaultBranch(t *testing.T) {
	repo := initLocatorRepo(t)
	s := mustOpenStore(t, filepath.Join(t.TempDir(), "concord.db"))
	seedLocatorAuthority(t, s, repo)
	originRequest := bootstrapRequest()
	originRequest.IdempotencyKey = "bootstrap-terminal-origin"
	origin, err := s.BootstrapWorktree(context.Background(), originRequest, nil)
	if err != nil {
		t.Fatal(err)
	}
	defaultSHA := strings.TrimSpace(gitOutput(t, repo, "rev-parse", "origin/main"))
	if err := os.WriteFile(filepath.Join(origin.Entry.Path, "origin.txt"), []byte("terminal origin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "-C", origin.Entry.Path, "add", "origin.txt")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, output)
	}
	command = exec.Command("git", "-C", origin.Entry.Path, "commit", "-q", "-m", "origin change")
	command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, output)
	}
	originSHA := strings.TrimSpace(gitOutput(t, origin.Entry.Path, "rev-parse", "HEAD"))
	if originSHA == defaultSHA {
		t.Fatal("terminal origin did not advance beyond the default branch")
	}
	terminalPayload, err := json.Marshal(map[string]any{"from": "needed", "to": "cancelled", "reason": "fixture terminal", "expected_version": origin.WorkVersion, "resulting_version": origin.WorkVersion + 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{{EventID: "terminal-origin-complete", Kind: "work.transitioned", SubjectType: store.SubjectWorkItem, SubjectID: origin.WorkID, Actor: "operator", OccurredAt: time.Unix(10, 0).UTC(), PayloadVersion: 1, Payload: terminalPayload}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, origin.WorkID): origin.WorkVersion}}); err != nil {
		t.Fatal(err)
	}

	request := bootstrapRequest()
	request.IdempotencyKey = "bootstrap-chained"
	raw, err := json.Marshal(workBootstrapInput{ProductID: request.ProductID, ProjectID: request.ProjectID, Title: request.Title, ValueStatement: request.ValueStatement, Kind: request.Kind, Task: request.Task, IdempotencyKey: request.IdempotencyKey})
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(origin.Entry.Path)
	var out, errOut bytes.Buffer
	if err := os.WriteFile("dirty.txt", []byte("keep here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := runWorkBootstrap(raw, s, &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "dirty worktree") {
		t.Fatalf("dirty chained bootstrap code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if err := os.Remove("dirty.txt"); err != nil {
		t.Fatal(err)
	}
	out, errOut = bytes.Buffer{}, bytes.Buffer{}
	if code := runWorkBootstrap(raw, s, &out, &errOut); code != 0 {
		t.Fatalf("chained bootstrap code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var chained workBootstrapOutput
	if err := json.Unmarshal(out.Bytes(), &chained); err != nil {
		t.Fatal(err)
	}
	if chained.WorkID == origin.WorkID || chained.Worktree.BaseSHA != defaultSHA {
		t.Fatalf("chained work=%+v default=%s origin=%s", chained, defaultSHA, originSHA)
	}
	if _, err := os.Stat(origin.Entry.Path); err != nil {
		t.Fatalf("origin worktree was removed: %v", err)
	}
}

func TestWorkBootstrapConcurrentExactReplayHasOneNativeResult(t *testing.T) {
	repo := initLocatorRepo(t)
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	seedLocatorAuthority(t, s, repo)
	req := bootstrapRequest()
	req.IdempotencyKey = "bootstrap-concurrent"
	var wg sync.WaitGroup
	results := make([]store.BootstrapResult, 2)
	errors := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			local, openErr := store.Open(context.Background(), dbPath)
			if openErr != nil {
				errors[i] = openErr
				return
			}
			defer local.Close()
			results[i], errors[i] = local.BootstrapWorktree(context.Background(), req, nil)
		}(i)
	}
	wg.Wait()
	defer s.Close()
	operationID, workID, _, err := store.CanonicalBootstrapIdentity(req)
	if err != nil {
		t.Fatal(err)
	}
	completed := 0
	for i := range results {
		if errors[i] == nil && results[i].WorkID == workID {
			completed++
		}
	}
	if completed == 0 {
		t.Fatalf("concurrent bootstrap errors=%v results=%+v", errors, results)
	}
	if _, err := s.BootstrapWorktree(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	var works, events, claims, entries int
	if err := s.DatabaseForTesting().QueryRow("SELECT count(*) FROM work_items WHERE id=?", workID).Scan(&works); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow("SELECT count(*) FROM domain_events WHERE event_id=?", operationID+":worktree-created").Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow("SELECT count(*) FROM worktree_claims WHERE op_id=?", operationID).Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow("SELECT count(*) FROM worktree_entries WHERE set_id=?", store.WorktreeSetID(workID)).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if works != 1 || events != 1 || claims != 1 || entries != 1 {
		t.Fatalf("works=%d events=%d claims=%d entries=%d", works, events, claims, entries)
	}
	location, err := s.LocateWorktree(context.Background(), req.ProjectID, workID, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(gitOutput(t, repo, "worktree", "list", "--porcelain"), "worktree "); count != 2 {
		t.Fatalf("native worktree count=%d", count)
	}
	if _, err := os.Stat(location.Path); err != nil {
		t.Fatalf("native worktree path=%s: %v", location.Path, err)
	}
	branchCount := 0
	for _, branch := range strings.Split(strings.TrimSpace(gitOutput(t, repo, "branch", "--format=%(refname:short)")), "\n") {
		if branch == location.Branch {
			branchCount++
		}
	}
	if branchCount != 1 {
		t.Fatalf("native branch count=%d", branchCount)
	}
}

func TestSessionPrepareRefusesWrongDirectoryBeforeIdentity(t *testing.T) {
	repo := initLocatorRepo(t)
	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "concord.db")
	t.Setenv(dbOverrideEnv, dbPath)
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	seedLocatorAuthority(t, s, repo)
	result, err := s.BootstrapWorktree(context.Background(), bootstrapRequest(), nil)
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	t.Chdir(repo)
	identityCalls := 0
	var out, errOut bytes.Buffer
	code := runSessionPrepare(commandSessionPrepareInput(t, result.WorkID, "run the task"), mustOpenStore(t, dbPath), &out, &errOut,
		func(string) error { return nil },
		hostCommandAt(defaultHostResolution()),
		func(context.Context, string, hostCommandResolution, string, string, string) (string, error) {
			identityCalls++
			return "agent", nil
		},
		func(context.Context, string, string, string) ([]byte, error) {
			return json.RawMessage(`{"watermark":"test"}`), nil
		})
	if code == 0 || identityCalls != 0 || !strings.Contains(errOut.String(), "claimed worktree") {
		t.Fatalf("wrong-directory code=%d identity_calls=%d stderr=%q", code, identityCalls, errOut.String())
	}
}

func TestSessionPrepareRunsLaneIdentityBeforeOrchestratorAndBoot(t *testing.T) {
	repo := initLocatorRepo(t)
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	s := mustOpenStore(t, dbPath)
	seedLocatorAuthority(t, s, repo)
	result, err := s.BootstrapWorktree(context.Background(), bootstrapRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(dbOverrideEnv, dbPath)
	t.Chdir(result.Entry.Path)
	var before int
	if err := s.DatabaseForTesting().QueryRow("SELECT count(*) FROM domain_events").Scan(&before); err != nil {
		t.Fatal(err)
	}
	laneCalls, identityCalls, bootCalls := 0, 0, 0
	var out, errOut bytes.Buffer
	code := runSessionPrepare(commandSessionPrepareInput(t, result.WorkID, "use UTF-8 ✓"), s, &out, &errOut,
		func(string) error { laneCalls++; return nil },
		hostCommandAt(defaultHostResolution()),
		func(ctx context.Context, dir string, host hostCommandResolution, productID, workID, agent string) (string, error) {
			identityCalls++
			if agent != "concord-1" {
				return "", errors.New("session-prepare must pass the active agent through to identity verification")
			}
			_, err := s.RecordOrchestratorIdentityAssertion(ctx, "prepare-success-identity", s.Now(), store.OrchestratorIdentityAssertion{
				Type: "orchestrator", Version: "1", RulesetDigest: "sha256:" + strings.Repeat("a", 64),
				Sources:   []store.OrchestratorArtifactSource{{Kind: "orchestrator_definition", Path: "/tmp/orchestrator.md", SHA256: strings.Repeat("b", 64)}},
				ProductID: productID, WorkID: workID, PrincipalRef: "principal/orchestrator", ClientRef: "client/session", AgentRef: "agent/orchestrator", SessionRef: "session/prepare",
			})
			return "orchestrator", err
		},
		func(context.Context, string, string, string) ([]byte, error) {
			bootCalls++
			return []byte(`{"watermark":"test"}`), nil
		})
	if code != 0 || laneCalls != 1 || identityCalls != 1 || bootCalls != 1 || !strings.Contains(out.String(), "use UTF-8 ✓") {
		t.Fatalf("prepare code=%d lane=%d identity=%d boot=%d stdout=%q stderr=%q", code, laneCalls, identityCalls, bootCalls, out.String(), errOut.String())
	}
	var prepared struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(out.Bytes(), &prepared); err != nil {
		t.Fatalf("unmarshal prepare output: %v", err)
	}
	if prepared.Title != "Bootstrap work" {
		t.Fatalf("prepare title=%q", prepared.Title)
	}
	var after int
	if err := s.DatabaseForTesting().QueryRow("SELECT count(*) FROM domain_events").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Fatalf("success event count before=%d after=%d", before, after)
	}

	before = after
	laneCalls, identityCalls, bootCalls = 0, 0, 0
	out, errOut = bytes.Buffer{}, bytes.Buffer{}
	code = runSessionPrepare(commandSessionPrepareInput(t, result.WorkID, "run"), s, &out, &errOut,
		func(string) error { laneCalls++; return errors.New("lane definition is missing") },
		hostCommandAt(defaultHostResolution()),
		func(context.Context, string, hostCommandResolution, string, string, string) (string, error) {
			identityCalls++
			return "orchestrator", nil
		},
		func(context.Context, string, string, string) ([]byte, error) {
			bootCalls++
			return []byte(`{"watermark":"test"}`), nil
		})
	if code == 0 || laneCalls != 1 || identityCalls != 0 || bootCalls != 0 {
		t.Fatalf("missing lane code=%d lane=%d identity=%d boot=%d stderr=%q", code, laneCalls, identityCalls, bootCalls, errOut.String())
	}
	if err := s.DatabaseForTesting().QueryRow("SELECT count(*) FROM domain_events").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("missing lane wrote event before=%d after=%d", before, after)
	}
}

// TestSessionPrepareReportsAKilledRegistryProbeAsRetryable covers a host
// registry probe that the kernel or the probe timeout kills. The host
// process never printed a document, so the registry is unestablished, but
// nothing about the configuration refuses: the same request passes once
// the host answers. session-prepare reports the ordinary failure status,
// which the adapter maps to retry_same_request, and never the refusal
// status, which the adapter maps to contact_operator.
func TestSessionPrepareReportsAKilledRegistryProbeAsRetryable(t *testing.T) {
	repo := initLocatorRepo(t)
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	s := mustOpenStore(t, dbPath)
	seedLocatorAuthority(t, s, repo)
	result, err := s.BootstrapWorktree(context.Background(), bootstrapRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(dbOverrideEnv, dbPath)
	installFakeCommand(t, t.TempDir(), "opencode", "#!/bin/sh\nkill -9 $$\n")
	t.Chdir(result.Entry.Path)
	identityCalls := 0
	var out, errOut bytes.Buffer
	code := runSessionPrepare(commandSessionPrepareInput(t, result.WorkID, "run"), s, &out, &errOut,
		func(string) error { return nil },
		hostSessionHostCommand,
		func(context.Context, string, hostCommandResolution, string, string, string) (string, error) {
			identityCalls++
			return "concord-1", nil
		},
		func(context.Context, string, string, string) ([]byte, error) { return []byte(`{}`), nil })
	if code != 1 || identityCalls != 0 {
		t.Fatalf("killed probe code=%d identity_calls=%d stderr=%q, want the retryable failure status 1", code, identityCalls, errOut.String())
	}
	if !strings.Contains(errOut.String(), "killed") {
		t.Fatalf("diagnostic=%q, want it to name the killed probe", errOut.String())
	}
}

// TestTimedOutRegistryProbeTypedAndExit1 covers a host that answers slower
// than the probe deadline. probeHostConfig returns the typed
// interrupted-probe error naming the deadline, and session-prepare reports
// the ordinary failure status, which the adapter maps to retry_same_request,
// instead of the refusal status. The short deadline comes from the parent
// context: probeHostConfig applies whichever deadline expires first.
func TestTimedOutRegistryProbeTypedAndExit1(t *testing.T) {
	repo := initLocatorRepo(t)
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	s := mustOpenStore(t, dbPath)
	seedLocatorAuthority(t, s, repo)
	result, err := s.BootstrapWorktree(context.Background(), bootstrapRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(dbOverrideEnv, dbPath)
	installFakeCommand(t, t.TempDir(), "opencode", "#!/bin/sh\nwhile true; do :; done\n")
	t.Chdir(result.Entry.Path)
	probeCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, probeErr := probeHostConfig(probeCtx, hostProbeArgv(defaultHostCommand), result.Entry.Path)
	var interrupted *hostProbeInterruptedError
	if !errors.As(probeErr, &interrupted) || !interrupted.DeadlinePassed {
		t.Fatalf("timed-out probe error=%v, want the typed interrupted-probe error naming the deadline", probeErr)
	}
	identityCalls := 0
	var out, errOut bytes.Buffer
	code := runSessionPrepare(commandSessionPrepareInput(t, result.WorkID, "run"), s, &out, &errOut,
		func(string) error { return nil },
		func(ctx context.Context, dir string) (hostCommandResolution, error) {
			timedCtx, timedCancel := context.WithTimeout(ctx, 50*time.Millisecond)
			defer timedCancel()
			return hostSessionHostCommand(timedCtx, dir)
		},
		func(context.Context, string, hostCommandResolution, string, string, string) (string, error) {
			identityCalls++
			return "concord-1", nil
		},
		func(context.Context, string, string, string) ([]byte, error) { return []byte(`{}`), nil })
	if code != 1 || identityCalls != 0 {
		t.Fatalf("timed-out probe code=%d identity_calls=%d stderr=%q, want the retryable failure status 1", code, identityCalls, errOut.String())
	}
	if !strings.Contains(errOut.String(), "deadline") {
		t.Fatalf("diagnostic=%q, want it to name the probe deadline", errOut.String())
	}
}

// TestDeterministicHostRegistryRefusalsExit2 covers the probe outcomes that
// refuse: a host that exits nonzero on its own, and a host executable the
// PATH does not carry. Neither condition clears on a replay, so
// session-prepare keeps the refusal status; only an interrupted probe is
// retryable.
func TestDeterministicHostRegistryRefusalsExit2(t *testing.T) {
	newFixture := func(t *testing.T) (workID string, worktree string, dbPath string) {
		repo := initLocatorRepo(t)
		path := filepath.Join(t.TempDir(), "concord.db")
		s := mustOpenStore(t, path)
		seedLocatorAuthority(t, s, repo)
		result, err := s.BootstrapWorktree(context.Background(), bootstrapRequest(), nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv(dbOverrideEnv, path)
		return result.WorkID, result.Entry.Path, path
	}
	run := func(t *testing.T, dbPath, workID string) (int, string) {
		var out, errOut bytes.Buffer
		code := runSessionPrepare(commandSessionPrepareInput(t, workID, "run"), mustOpenStore(t, dbPath), &out, &errOut,
			func(string) error { return nil },
			hostSessionHostCommand,
			func(context.Context, string, hostCommandResolution, string, string, string) (string, error) {
				t.Fatal("the identity step ran after a refused probe")
				return "", nil
			},
			func(context.Context, string, string, string) ([]byte, error) { return []byte(`{}`), nil })
		return code, errOut.String()
	}
	t.Run("a host that exits nonzero on its own refuses", func(t *testing.T) {
		workID, worktree, dbPath := newFixture(t)
		installFakeCommand(t, t.TempDir(), "opencode", "#!/bin/sh\nexit 3\n")
		t.Chdir(worktree)
		code, diagnostic := run(t, dbPath, workID)
		if code != sessionPrepareRefusalExit {
			t.Fatalf("nonzero-exit probe code=%d stderr=%q, want the refusal status %d", code, diagnostic, sessionPrepareRefusalExit)
		}
		if !strings.Contains(diagnostic, "exit status 3") {
			t.Fatalf("diagnostic=%q, want it to name the exit status", diagnostic)
		}
	})
	t.Run("a missing host executable refuses", func(t *testing.T) {
		workID, worktree, dbPath := newFixture(t)
		// A PATH without opencode but with git, which the Project
		// resolution shells out to before the probe runs.
		gitPath, err := exec.LookPath("git")
		if err != nil {
			t.Fatal(err)
		}
		binDir := t.TempDir()
		if err := os.Symlink(gitPath, filepath.Join(binDir, "git")); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", binDir)
		t.Chdir(worktree)
		code, diagnostic := run(t, dbPath, workID)
		if code != sessionPrepareRefusalExit {
			t.Fatalf("missing-executable probe code=%d stderr=%q, want the refusal status %d", code, diagnostic, sessionPrepareRefusalExit)
		}
		if !strings.Contains(diagnostic, "executable file not found") {
			t.Fatalf("diagnostic=%q, want it to name the missing executable", diagnostic)
		}
	})
}

func mustOpenStore(t *testing.T, path string) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestSessionRecordAcceptsTheAdapterRetargetPayload crosses the seam the
// adapter's own tests stub out. adapter/opencode/concord.ts records a completed
// retarget with the session identity and no model, because CD-0098 retargets
// this session rather than spawning a child that could report one. The store
// must accept exactly that payload, or work_start can never leave outcome
// partial.

// TestSessionPrepareVerifiesTheConfiguredCommandDocument covers CD-0189 on
// the session-prepare side of the shared probe. The bare probe reads the
// host_command option, the second probe runs through the configured command
// in the claimed worktree, and the registration check reads that command's
// own document: a wrapper whose registry does not register the handle
// refuses even though the bare host's document registers it, and a wrapper
// whose document carries the handle and the identical host_command admits
// the preparation.
func TestSessionPrepareVerifiesTheConfiguredCommandDocument(t *testing.T) {
	newFixture := func(t *testing.T) (workID string, worktree string, dbPath string) {
		repo := initLocatorRepo(t)
		path := filepath.Join(t.TempDir(), "concord.db")
		s := mustOpenStore(t, path)
		seedLocatorAuthority(t, s, repo)
		result, err := s.BootstrapWorktree(context.Background(), bootstrapRequest(), nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv(dbOverrideEnv, path)
		return result.WorkID, result.Entry.Path, path
	}
	installFakes := func(t *testing.T, recordDir string) {
		t.Helper()
		installFakeHost(t, recordDir)
		installFakeWrapper(t, recordDir)
	}
	t.Run("a wrapper document that does not register the handle refuses", func(t *testing.T) {
		workID, worktree, dbPath := newFixture(t)
		bare := `{"agent":{"concord-1":{"mode":"primary"}},"plugin":[["file:///tools/concord-plugin.ts",{"host_command":["fake-wrapper"]}]]}`
		wrapper := `{"agent":{"someone-else":{"mode":"primary"}},"plugin":[["file:///tools/concord-plugin.ts",{"host_command":["fake-wrapper"]}]]}`
		if err := os.WriteFile(filepath.Join(worktree, "opencode.registry.json"), []byte(bare), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(worktree, "wrapper.registry.json"), []byte(wrapper), 0o644); err != nil {
			t.Fatal(err)
		}
		recordDir := t.TempDir()
		installFakes(t, recordDir)
		t.Chdir(worktree)
		var out, errOut bytes.Buffer
		code := runSessionPrepare(commandSessionPrepareInput(t, workID, "run"), mustOpenStore(t, dbPath), &out, &errOut,
			func(string) error { return nil },
			hostSessionHostCommand,
			func(_ context.Context, dir string, host hostCommandResolution, productID, workID, agent string) (string, error) {
				// The production callback verifies the resolution's registry
				// document; the stub runs that check directly so the test
				// observes exactly what the configured command's document
				// decides.
				if err := verifyHostRegistersHandle(host, agent); err != nil {
					return "", err
				}
				t.Fatal("the configured command's document registered the handle the bare document's refusal should have caught")
				return "", nil
			},
			func(context.Context, string, string, string) ([]byte, error) { return nil, nil })
		if code != sessionPrepareRefusalExit {
			t.Fatalf("prepare code=%d stderr=%q", code, errOut.String())
		}
		if !strings.Contains(errOut.String(), "concord-1") || !strings.Contains(errOut.String(), "not registered") {
			t.Fatalf("diagnostic=%q, want the wrapper registry's refusal", errOut.String())
		}
		if count := hostRecord(t, recordDir, "wrapper-probe-count"); count != "1" {
			t.Fatalf("configured probes=%s, want the refusal right after the second probe", count)
		}
	})
	t.Run("a wrapper document that registers the handle admits the preparation", func(t *testing.T) {
		workID, worktree, dbPath := newFixture(t)
		registry := `{"agent":{"concord-1":{"mode":"primary"}},"plugin":[["file:///tools/concord-plugin.ts",{"host_command":["fake-wrapper"]}]]}`
		if err := os.WriteFile(filepath.Join(worktree, "opencode.registry.json"), []byte(registry), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(worktree, "wrapper.registry.json"), []byte(registry), 0o644); err != nil {
			t.Fatal(err)
		}
		recordDir := t.TempDir()
		installFakes(t, recordDir)
		t.Chdir(worktree)
		var out, errOut bytes.Buffer
		code := runSessionPrepare(commandSessionPrepareInput(t, workID, "run"), mustOpenStore(t, dbPath), &out, &errOut,
			func(string) error { return nil },
			hostSessionHostCommand,
			func(_ context.Context, dir string, host hostCommandResolution, productID, workID, agent string) (string, error) {
				if !slices.Equal(host.Command, []string{"fake-wrapper"}) {
					t.Fatalf("orchestrator received command %q, want the configured argv", host.Command)
				}
				if err := verifyHostRegistersHandle(host, agent); err != nil {
					return "", err
				}
				return agent, nil
			},
			func(context.Context, string, string, string) ([]byte, error) { return []byte(`{}`), nil })
		if code != 0 {
			t.Fatalf("prepare code=%d stderr=%q", code, errOut.String())
		}
	})
}

func TestWorkBootstrapCaptureIdentitySurvivesTheSessionMove(t *testing.T) {
	repo := initLocatorRepo(t)
	s := mustOpenStore(t, filepath.Join(t.TempDir(), "concord.db"))
	seedLocatorAuthority(t, s, repo)
	input, err := json.Marshal(workBootstrapInput{ProductID: "product-wl", ProjectID: "project-wl", Title: "Bootstrap work", ValueStatement: "A worktree is ready", Kind: "task", Task: "run the task", IdempotencyKey: "bootstrap-move-key"})
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	var out, errOut bytes.Buffer
	if code := runWorkBootstrap(input, s, &out, &errOut); code != 0 {
		t.Fatalf("trunk capture code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var captured workBootstrapOutput
	if err := json.Unmarshal(out.Bytes(), &captured); err != nil {
		t.Fatal(err)
	}

	// The session moved into the claimed worktree. The identical capture
	// replayed from there stays the original work item and worktree: the
	// directory-derived default branch is resolution, not identity.
	t.Chdir(captured.Worktree.Path)
	out, errOut = bytes.Buffer{}, bytes.Buffer{}
	if code := runWorkBootstrap(input, s, &out, &errOut); code != 0 {
		t.Fatalf("moved replay code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var replayed workBootstrapOutput
	if err := json.Unmarshal(out.Bytes(), &replayed); err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.WorkID != captured.WorkID || replayed.OperationID != captured.OperationID || replayed.Worktree.Path != captured.Worktree.Path {
		t.Fatalf("moved replay=%+v captured=%+v", replayed, captured)
	}
	var operations int
	if err := s.DatabaseForTesting().QueryRow("SELECT count(*) FROM bootstrap_operations").Scan(&operations); err != nil || operations != 1 {
		t.Fatalf("bootstrap operations=%d err=%v", operations, err)
	}
}

func TestWorkBootstrapDeterministicRefusalsExit2(t *testing.T) {
	repo := initLocatorRepo(t)
	s := mustOpenStore(t, filepath.Join(t.TempDir(), "concord.db"))
	seedLocatorAuthority(t, s, repo)
	var out, errOut bytes.Buffer
	if code := runWorkBootstrap([]byte("[]"), s, &out, &errOut); code != workBootstrapRefusalExit {
		t.Fatalf("invalid input code=%d stderr=%q", code, errOut.String())
	}
	sessionless, err := json.Marshal(workBootstrapInput{ProductID: "product-wl", ProjectID: "project-wl", Title: "T", ValueStatement: "V", Kind: "task", Task: "run", IdempotencyKey: "sessionless", SessionRef: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	if code := runWorkBootstrap(sessionless, s, &out, &errOut); code != workBootstrapRefusalExit {
		t.Fatalf("session_ref without host_pid code=%d stderr=%q", code, errOut.String())
	}

	t.Chdir(repo)
	capture, err := json.Marshal(workBootstrapInput{ProductID: "product-wl", ProjectID: "project-wl", Title: "Bootstrap work", ValueStatement: "A worktree is ready", Kind: "task", Task: "run the task", IdempotencyKey: "refusal-key"})
	if err != nil {
		t.Fatal(err)
	}
	if code := runWorkBootstrap(capture, s, &out, &errOut); code != 0 {
		t.Fatalf("capture code=%d stderr=%q", code, errOut.String())
	}
	var captured workBootstrapOutput
	if err := json.Unmarshal(out.Bytes(), &captured); err != nil {
		t.Fatal(err)
	}
	conflict, err := json.Marshal(workBootstrapInput{ProductID: "product-wl", ProjectID: "project-wl", Title: "A different capture", ValueStatement: "A worktree is ready", Kind: "task", Task: "run the task", IdempotencyKey: "refusal-key"})
	if err != nil {
		t.Fatal(err)
	}
	out, errOut = bytes.Buffer{}, bytes.Buffer{}
	if code := runWorkBootstrap(conflict, s, &out, &errOut); code != workBootstrapRefusalExit || !strings.Contains(errOut.String(), "bound to different input") {
		t.Fatalf("idempotency conflict code=%d stderr=%q", code, errOut.String())
	}
	mismatch, err := json.Marshal(workBootstrapInput{ProductID: "product-wl", ProjectID: "project-other", Title: "Bootstrap work", ValueStatement: "A worktree is ready", Kind: "task", Task: "run the task", IdempotencyKey: "refusal-mismatch"})
	if err != nil {
		t.Fatal(err)
	}
	if code := runWorkBootstrap(mismatch, s, &out, &errOut); code != workBootstrapRefusalExit || !strings.Contains(errOut.String(), "requested Project") {
		t.Fatalf("project mismatch code=%d stderr=%q", code, errOut.String())
	}

	t.Chdir(captured.Worktree.Path)
	if err := os.WriteFile("dirty.txt", []byte("dirty the origin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirty, err := json.Marshal(workBootstrapInput{ProductID: "product-wl", ProjectID: "project-wl", Title: "Chained", ValueStatement: "Chained capture", Kind: "task", Task: "run", IdempotencyKey: "refusal-dirty"})
	if err != nil {
		t.Fatal(err)
	}
	if code := runWorkBootstrap(dirty, s, &out, &errOut); code != workBootstrapRefusalExit || !strings.Contains(errOut.String(), "dirty worktree") {
		t.Fatalf("dirty origin code=%d stderr=%q", code, errOut.String())
	}
	if err := os.Remove("dirty.txt"); err != nil {
		t.Fatal(err)
	}
	if code := storeFailureExit(&store.Failure{Kind: store.KindUnavailable, RetrySafe: true}); code != 1 {
		t.Fatalf("retryable read failure code=%d, want 1", code)
	}
	if code := storeFailureExit(&store.Failure{Kind: store.KindUnavailable, RetrySafe: false}); code != workBootstrapRefusalExit {
		t.Fatalf("unsafe read failure code=%d, want %d", code, workBootstrapRefusalExit)
	}
}

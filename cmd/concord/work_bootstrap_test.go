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

func bootstrapRequest() store.BootstrapRequest {
	return store.BootstrapRequest{
		ProductID: "product-wl", ProjectID: "project-wl", Title: "Bootstrap work",
		ValueStatement: "A worktree is ready", Kind: "task", Task: "run the task",
		IdempotencyKey: "bootstrap-key", Priority: 1,
	}
}

// launchOwner is the live host process identity both session-prepare and
// session-record must present.
type launchOwner struct {
	pid   int64
	start string
}

func ownerIdentity(t *testing.T) launchOwner {
	t.Helper()
	stat, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		t.Fatal(err)
	}
	closeParen := strings.LastIndex(string(stat), ")")
	if closeParen < 0 {
		t.Fatal("process stat has no command boundary")
	}
	fields := strings.Fields(string(stat)[closeParen+1:])
	if len(fields) < 20 {
		t.Fatal("process stat lacks a start identity")
	}
	return launchOwner{pid: int64(os.Getpid()), start: fields[19]}
}

func commandSessionPrepareInput(t *testing.T, workID, task string) []byte {
	t.Helper()
	owner := ownerIdentity(t)
	raw, err := json.Marshal(sessionPrepareInput{ProductID: "product-wl", WorkID: workID, Task: task, OwnerPID: owner.pid, OwnerStart: owner.start})
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
	if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{
		{EventID: "other-product", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-other", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"display_name":"other","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "other-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-other", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"display_name":"other"}`)},
		{EventID: "other-membership", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-other", Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"product_id":"product-other","project_id":"project-other","role":"primary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-other"): 0, store.VersionRef(store.SubjectProject, "project-other"): 0}}); err != nil {
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
	if code := runWorkBootstrap(input, s, &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "requested Project main checkout") {
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
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	out, errOut = bytes.Buffer{}, bytes.Buffer{}
	if code := runWorkBootstrap(raw, s, &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "requested Project main checkout") {
		t.Fatalf("linked-worktree invocation code=%d stderr=%q", code, errOut.String())
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
		func(context.Context, string, string, string) (string, error) { identityCalls++; return "agent", nil },
		func(context.Context, string, string, string) ([]byte, error) {
			return json.RawMessage(`{"watermark":"test"}`), nil
		})
	if code == 0 || identityCalls != 0 || !strings.Contains(errOut.String(), "claimed worktree") {
		t.Fatalf("wrong-directory code=%d identity_calls=%d stderr=%q", code, identityCalls, errOut.String())
	}
}

func TestBootstrapRollbackRequiresTheRecordedLinkedWorktree(t *testing.T) {
	repo := initLocatorRepo(t)
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	s := mustOpenStore(t, dbPath)
	seedLocatorAuthority(t, s, repo)
	result, err := s.BootstrapWorktree(context.Background(), bootstrapRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]string{"product_id": result.ProductID, "work_id": result.WorkID, "operation_id": result.OperationID, "directory": result.Entry.Path, "reason": "session preparation failed"})
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	var out, errOut bytes.Buffer
	if code := runBootstrapRollback(raw, s, &out, &errOut); code == 0 {
		t.Fatal("rollback accepted the default checkout")
	}
	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT state FROM bootstrap_operations WHERE operation_id=?`, result.OperationID).Scan(&state); err != nil || state != "completed" {
		t.Fatalf("wrong-directory state=%s err=%v", state, err)
	}
	t.Chdir(result.Entry.Path)
	out.Reset()
	errOut.Reset()
	if code := runBootstrapRollback(raw, s, &out, &errOut); code != 0 {
		t.Fatalf("rollback code=%d stderr=%q", code, errOut.String())
	}
	t.Chdir(repo)
	if err := s.DatabaseForTesting().QueryRow(`SELECT state FROM bootstrap_operations WHERE operation_id=?`, result.OperationID).Scan(&state); err != nil || state != "rolled_back" {
		t.Fatalf("linked-worktree state=%s err=%v", state, err)
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
		func(ctx context.Context, dir, productID, workID string) (string, error) {
			identityCalls++
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
		func(context.Context, string, string, string) (string, error) {
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
func TestSessionRecordAcceptsTheAdapterRetargetPayload(t *testing.T) {
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
	var out, errOut bytes.Buffer
	code := runSessionPrepare(commandSessionPrepareInput(t, result.WorkID, "run the task"), s, &out, &errOut,
		func(string) error { return nil },
		func(context.Context, string, string, string) (string, error) { return "orchestrator", nil },
		func(context.Context, string, string, string) ([]byte, error) {
			return json.RawMessage(`{"watermark":"test"}`), nil
		})
	if code != 0 {
		t.Fatalf("session-prepare code=%d stderr=%q", code, errOut.String())
	}
	var prepared sessionPrepareOutput
	if err := json.Unmarshal(out.Bytes(), &prepared); err != nil {
		t.Fatal(err)
	}
	owner := ownerIdentity(t)
	record, err := json.Marshal(map[string]any{
		"operation_id":   prepared.OperationID,
		"attempt_id":     prepared.AttemptID,
		"product_id":     result.ProductID,
		"work_id":        result.WorkID,
		"agent":          prepared.Agent,
		"directory":      result.Entry.Path,
		"session_id":     "ses-adapter-retarget",
		"state":          "completed",
		"failure_reason": "",
		"owner_pid":      owner.pid,
		"owner_start":    owner.start,
	})
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	if code := runSessionRecord(record, s, &out, &errOut); code != 0 {
		t.Fatalf("session-record refused the adapter payload: code=%d stderr=%q", code, errOut.String())
	}
	var launchState, sessionID string
	if err := s.DatabaseForTesting().QueryRow(`SELECT launch_state,COALESCE(launch_session_id,'') FROM bootstrap_operations WHERE operation_id=?`, prepared.OperationID).Scan(&launchState, &sessionID); err != nil {
		t.Fatal(err)
	}
	if launchState != "completed" || sessionID != "ses-adapter-retarget" {
		t.Fatalf("launch_state=%q session=%q", launchState, sessionID)
	}
}

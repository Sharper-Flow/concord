package main

// This file is self-contained by design: the native oracle harness stages a
// package with only the recipe-declared test files, so every helper below
// carries a br-prefixed unique name instead of reaching other test files.
// The two fixtures drive the real work-bootstrap and work-resume commands
// from a preserved ad-hoc linked worktree and pin the caller-aware remedy.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

func brGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

// brRun runs git in dir with a fixed commit identity.
func brRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(append(cmd.Env, os.Environ()...), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
}

// brRepo clones a working repository from a seeded bare origin and presents
// it under a supported https remote URL rewritten to the local bare, so
// bootstrap and resume run against a real fetched remote without reusing
// another fixture's repository construction.
func brRepo(t *testing.T) string {
	t.Helper()
	parent := t.TempDir()
	seed := filepath.Join(parent, "seed")
	repo := filepath.Join(parent, "repo")
	const supported = "https://remedy.invalid/repository.git"
	brRun(t, parent, "init", "-q", "-b", "main", "seed")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("remedy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	brRun(t, seed, "add", ".")
	brRun(t, seed, "commit", "-q", "-m", "base")
	brRun(t, parent, "clone", "-q", "--bare", "seed", "origin.git")
	brRun(t, parent, "clone", "-q", filepath.Join(parent, "origin.git"), "repo")
	brRun(t, repo, "remote", "set-url", "origin", supported)
	brRun(t, repo, "config", "url."+filepath.Join(parent, "origin.git")+".insteadOf", supported)
	brRun(t, repo, "remote", "set-head", "origin", "-a")
	return repo
}

func brSeedAuthority(t *testing.T, s *store.Store, repo string, workID string) {
	t.Helper()
	ctx := context.Background()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	_, err := s.CreateProductWithProject(ctx, store.ProductCreation{
		ProductID: "product-wl", DisplayName: "Remedy Product",
		StageMaturity: "prototype", StageAudienceCommitment: "operator_only",
		ProjectID: "project-wl", ProjectDisplayName: "Remedy Project",
		Role: "primary", Reason: "fixture",
	})
	must(err)
	must(store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{
		{EventID: "br-work-create-" + workID, Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(4, 0).UTC(), PayloadVersion: store.CurrentEventPayloadVersion("work.created"), Payload: raw(`{"work_kind":"task","title":"Remedy Work","priority":1}`)},
		{EventID: "br-work-membership-" + workID, Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(5, 0).UTC(), PayloadVersion: 1, Payload: raw(`{"memberships":[{"project_id":"project-wl","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, workID): 0}}))
	must(s.AddProjectLocator(ctx, "project-wl", store.ProjectLocator{ID: "path-wl-" + workID, Kind: store.LocatorCanonicalPath, Value: repo}, 1))
}

func brCounts(t *testing.T, s *store.Store) map[string]int {
	t.Helper()
	counts := make(map[string]int)
	for _, table := range []string{"effect_receipts", "idempotency_records", "durable_operations", "worktree_claims", "worktree_entries", "bootstrap_operations"} {
		var count int
		if err := s.DatabaseForTesting().QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		counts[table] = count
	}
	return counts
}

func brPreservedOrigin(t *testing.T, repo string) string {
	t.Helper()
	origin := filepath.Join(t.TempDir(), "preserved-origin")
	brGit(t, repo, "worktree", "add", "--detach", origin, "HEAD")
	return origin
}

func TestBootstrapRemedyNamesCaptureCheckout(t *testing.T) {
	repo := brRepo(t)
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "authority.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	brSeedAuthority(t, s, repo, "work-wl")
	origin := brPreservedOrigin(t, repo)
	raw, err := json.Marshal(store.BootstrapRequest{
		ProductID: "product-wl", ProjectID: "project-wl", Title: "Capture work",
		ValueStatement: "A worktree is ready", Kind: "task", Task: "run the task",
		IdempotencyKey: "br-capture-key", Priority: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	before := brCounts(t, s)
	t.Chdir(origin)
	var out, diagnostic bytes.Buffer
	code := runWorkBootstrap(raw, s, &out, &diagnostic)
	if code != workBootstrapRefusalExit || out.Len() != 0 {
		t.Fatalf("unclaimed origin unexpectedly admitted: code=%d out=%s diagnostic=%s", code, &out, &diagnostic)
	}
	if after := brCounts(t, s); !reflect.DeepEqual(before, after) {
		t.Fatalf("refused capture changed authority: before=%v after=%v", before, after)
	}
	if !strings.Contains(diagnostic.String(), "capture") || !strings.Contains(diagnostic.String(), "default checkout") || !strings.Contains(diagnostic.String(), repo) {
		t.Errorf("no-active-item refusal does not name its actual capture checkout: %s", &diagnostic)
	}
	if !strings.Contains(brGit(t, repo, "worktree", "list", "--porcelain"), origin) {
		t.Fatal("refused capture removed the preserved origin")
	}
	t.Chdir(repo)
	out.Reset()
	diagnostic.Reset()
	if code := runWorkBootstrap(raw, s, &out, &diagnostic); code != 0 {
		t.Fatalf("capture from the actual default checkout is not callable: code=%d diagnostic=%s", code, &diagnostic)
	}
}

func TestBootstrapRemedyNamesActiveResumePath(t *testing.T) {
	repo := brRepo(t)
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "authority.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	brSeedAuthority(t, s, repo, "work-wl")
	active, err := s.BootstrapWorktree(context.Background(), store.BootstrapRequest{
		ProductID: "product-wl", ProjectID: "project-wl", Title: "Resume work",
		ValueStatement: "A worktree is ready", Kind: "task", Task: "run the task",
		IdempotencyKey: "br-resume-key", Priority: 1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	origin := brPreservedOrigin(t, repo)
	resume := func(directory string) (int, workResumeOutput, string) {
		raw, err := json.Marshal(workResumeInput{ProductID: "product-wl", ProjectID: "project-wl", WorkID: active.WorkID})
		if err != nil {
			t.Fatal(err)
		}
		t.Chdir(directory)
		var out, diagnostic bytes.Buffer
		code := runWorkResume(raw, s, &out, &diagnostic)
		var parsed workResumeOutput
		_ = json.Unmarshal(out.Bytes(), &parsed)
		return code, parsed, diagnostic.String()
	}
	before := brCounts(t, s)
	code, _, diagnostic := resume(origin)
	if code != workResumeRefusalExit {
		t.Fatalf("unclaimed origin unexpectedly admitted: code=%d diagnostic=%s", code, diagnostic)
	}
	if after := brCounts(t, s); !reflect.DeepEqual(before, after) {
		t.Fatalf("refused resume changed authority: before=%v after=%v", before, after)
	}
	if !strings.Contains(diagnostic, active.Entry.Path) || !strings.Contains(diagnostic, active.WorkID) || !strings.Contains(diagnostic, "work_start") {
		t.Errorf("active-item refusal does not name its actual resume identity and path: %s", diagnostic)
	}
	if !strings.Contains(brGit(t, repo, "worktree", "list", "--porcelain"), origin) {
		t.Fatal("refused resume removed the preserved origin")
	}
	code, resumed, diagnostic := resume(active.Entry.Path)
	if code != 0 || resumed.Worktree.Path != active.Entry.Path {
		t.Fatalf("resume from the actual active worktree is not callable: code=%d output=%+v diagnostic=%s", code, resumed, diagnostic)
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// seedLocatorAuthority registers a Product, Project, membership, work item,
// and canonical_path locator so worktree-locate has authority to read.
func jsonRaw(s string) []byte { return []byte(s) }

func seedLocatorAuthority(t *testing.T, s *store.Store, repo string) {
	t.Helper()
	ctx := context.Background()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{
		{EventID: "wl-product", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-wl", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"display_name":"wl","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "wl-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-wl", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"display_name":"wl"}`)},
		{EventID: "wl-membership", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-wl", Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"product_id":"product-wl","project_id":"project-wl","role":"primary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-wl"): 0, store.VersionRef(store.SubjectProject, "project-wl"): 0}}))
	must(store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{
		{EventID: "wl-work-create", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-wl", Actor: "operator", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 2, Payload: jsonRaw(`{"work_kind":"task","title":"Locator Work","priority":1}`)},
		{EventID: "wl-work-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-wl", Actor: "operator", OccurredAt: time.Unix(4, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"memberships":[{"project_id":"project-wl","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "work-wl"): 0}}))
	must(s.AddProjectLocator(ctx, "project-wl", store.ProjectLocator{ID: "path-wl", Kind: store.LocatorCanonicalPath, Value: repo}, 1))
}

func initLocatorRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main", ".")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("locator\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "base")
	run("update-ref", "refs/remotes/origin/main", "HEAD")
	run("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	return repo
}

// The acceptance criterion of #316: whatever owns the locator produces input
// that worktree_claim accepts on the first attempt, proven by executing the
// claim — not by inspection. The located intent drives one real claim against
// a real git repository through the real git runner.
func TestWorktreeLocateOutputIsAcceptedByClaimOnFirstAttempt(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	repo := initLocatorRepo(t)
	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "concord.db")
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedLocatorAuthority(t, s, repo)

	var out, errOut bytes.Buffer
	code := runWorktreeLocate([]byte(`{"project_id":"project-wl","work_id":"work-wl"}`), s, &out, &errOut)
	if code != 0 {
		t.Fatalf("worktree-locate exit=%d stderr=%q", code, errOut.String())
	}
	var located struct {
		Branch  string `json:"branch"`
		BaseSHA string `json:"base_sha"`
		Path    string `json:"path"`
		Repo    string `json:"repo"`
		Ref     string `json:"ref"`
	}
	if err := json.Unmarshal(out.Bytes(), &located); err != nil {
		t.Fatalf("located output: %v\n%s", err, out.String())
	}
	if located.Branch != "work/work-wl" || located.Ref != "HEAD" {
		t.Fatalf("branch=%q ref=%q", located.Branch, located.Ref)
	}
	if !strings.HasPrefix(located.Path, filepath.Join(dbDir, "worktrees", "project-wl", "work-wl")) {
		t.Fatalf("path=%q, want data-root/worktrees/project-wl/work-wl under %s", located.Path, dbDir)
	}

	result, err := s.ClaimWorktree(context.Background(), store.WorktreeClaimRequest{
		OpID: "wl-claim-1", WorkID: "work-wl", ProjectID: "project-wl",
		Branch: located.Branch, BaseSHA: located.BaseSHA, Path: located.Path,
		PrincipalRef: "principal-wl", RequestID: "wl-req-1",
		ExpectedVersion: 2, Now: time.Unix(20, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("first-attempt claim refused the located intent: %v", err)
	}
	if result.Entry.State == "" || result.Entry.Branch != "work/work-wl" {
		t.Fatalf("entry=%+v", result.Entry)
	}
}

func TestWorktreeLocateRefusesUnlocatableAuthorityAndBadRefs(t *testing.T) {
	repo := initLocatorRepo(t)
	for _, ref := range []string{"-help", "main branch", "main\x00suffix"} {
		if _, err := store.ResolveCommitSHA(context.Background(), repo, ref); err == nil {
			t.Fatalf("hostile ref %q was accepted", ref)
		}
	}
	if sha, err := store.ResolveCommitSHA(context.Background(), repo, "refs/heads/main"); err != nil || len(sha) != 40 {
		t.Fatalf("valid symbolic ref: sha=%q err=%v", sha, err)
	}
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var out, errOut bytes.Buffer
	if code := runWorktreeLocate([]byte(`{"project_id":"ghost","work_id":"work-wl"}`), s, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "no canonical_path locator") {
		t.Fatalf("unregistered project: code=%d stderr=%q", code, errOut.String())
	}

	seedLocatorAuthority(t, s, repo)
	out, errOut = bytes.Buffer{}, bytes.Buffer{}
	if code := runWorktreeLocate([]byte(`{"project_id":"project-wl","work_id":"work wl"}`), s, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "claim's own validation") {
		t.Fatalf("branch-hostile work id: code=%d stderr=%q", code, errOut.String())
	}
	out, errOut = bytes.Buffer{}, bytes.Buffer{}
	if code := runWorktreeLocate([]byte(`{"project_id":"project-wl","work_id":"work-wl","ref":"refs/heads/nope"}`), s, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "cannot resolve") {
		t.Fatalf("unknown ref: code=%d stderr=%q", code, errOut.String())
	}
}

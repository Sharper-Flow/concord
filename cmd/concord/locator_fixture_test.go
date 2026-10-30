package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

func jsonRaw(s string) []byte { return []byte(s) }

func seedLocatorAuthority(t *testing.T, s *store.Store, repo string) {
	t.Helper()
	ctx := context.Background()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	seedProductProject(t, s, "product-wl", "project-wl", "wl")
	must(store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{
		{EventID: "wl-work-create", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-wl", Actor: "operator", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: store.CurrentEventPayloadVersion("work.created"), Payload: jsonRaw(`{"work_kind":"task","title":"Locator Work","priority":1}`)},
		{EventID: "wl-work-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-wl", Actor: "operator", OccurredAt: time.Unix(4, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"memberships":[{"project_id":"project-wl","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "work-wl"): 0}}))
	must(s.AddProjectLocator(ctx, "project-wl", store.ProjectLocator{ID: "path-wl", Kind: store.LocatorCanonicalPath, Value: repo}, 1))
}

// The origin is local, so repository probes never need network access.
func initLocatorRepo(t *testing.T) string {
	t.Helper()
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	run := func(dir string, args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(parent, "init", "-q", "--bare", "-b", "main", "origin.git")
	run(parent, "init", "-q", "-b", "main", "repo")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("locator\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(repo, "add", ".")
	run(repo, "commit", "-q", "-m", "base")
	run(repo, "remote", "add", "origin", "https://locator.invalid/repository.git")
	run(repo, "config", "url."+filepath.Join(parent, "origin.git")+".insteadOf", "https://locator.invalid/repository.git")
	run(repo, "push", "-q", "origin", "main")
	run(repo, "fetch", "-q", "origin")
	run(repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	return repo
}

func seedProductProject(t *testing.T, s *store.Store, productID, projectID, displayName string) {
	t.Helper()
	if _, err := s.CreateProductWithProject(context.Background(), store.ProductCreation{
		ProductID: productID, DisplayName: displayName,
		StageMaturity: "prototype", StageAudienceCommitment: "operator_only",
		ProjectID: projectID, ProjectDisplayName: displayName,
		Role: "primary", Reason: "fixture",
	}); err != nil {
		t.Fatal(err)
	}
}

type cliWorktreeFixture struct {
	WorkID      string
	WorkVersion int64
	Entry       store.WorktreeEntry
}

func seedCLIHostWorktree(t *testing.T, repo, workID, projectID string) store.WorktreeEntry {
	t.Helper()
	path := filepath.Join(t.TempDir(), "checkout")
	branch := "fixture/" + workID
	base := strings.TrimSpace(gitOutput(t, repo, "rev-parse", "HEAD"))
	gitOutput(t, repo, "worktree", "add", "-b", branch, path, base)
	return store.WorktreeEntry{Path: path, Branch: branch, BaseSHA: base, State: "active", ProjectID: projectID}
}

func seedCLISessionWorktree(t *testing.T, s *store.Store, repo string) cliWorktreeFixture {
	t.Helper()
	ref, err := store.DefaultWorkflowRefForKind("task")
	if err != nil {
		t.Fatal(err)
	}
	definition, err := store.LiveWorkflowDefinitionForRef(ref)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{
			WorkID: "work-wl", Definition: definition, Now: time.Unix(5, 0).UTC(),
			Actor: store.WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord", AgentRef: "agent/concord", SessionRef: "session/fixture", ActorClass: store.ActorOperator},
		})
	}); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-wl'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return cliWorktreeFixture{WorkID: "work-wl", WorkVersion: version, Entry: seedCLIHostWorktree(t, repo, "work-wl", "project-wl")}
}

func gitOutput(t *testing.T, repo string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

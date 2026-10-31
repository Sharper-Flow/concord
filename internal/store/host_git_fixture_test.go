package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func initBootstrapStoreRepo(t *testing.T) string {
	t.Helper()
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	runTestGit(t, parent, "init", "-q", "--bare", "-b", "main", "origin.git")
	runTestGit(t, parent, "init", "-q", "-b", "main", "repo")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("fixture\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "add", "README.md")
	runTestGit(t, repo, "commit", "-q", "-m", "base")
	runTestGit(t, repo, "remote", "add", "origin", "https://bootstrap.invalid/repository.git")
	runTestGit(t, repo, "config", "url."+filepath.Join(parent, "origin.git")+".insteadOf", "https://bootstrap.invalid/repository.git")
	runTestGit(t, repo, "push", "-q", "origin", "main")
	runTestGit(t, repo, "fetch", "-q", "origin")
	runTestGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	return repo
}

func runTestGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	command, err := newTestGitCommand(repo, args...)
	if err != nil {
		t.Fatal(err)
	}
	command.Env = append(command.Env, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newTestGitCommand(repo string, args ...string) (*exec.Cmd, error) {
	if !filepath.IsAbs(repo) {
		return nil, fmt.Errorf("test git requires an absolute fixture directory: %q", repo)
	}
	command := exec.Command("git", args...)
	command.Dir = repo
	command.Env = os.Environ()
	return command, nil
}

func seedBootstrapStoreAuthority(t *testing.T, s *Store, repo string) {
	t.Helper()
	if _, err := s.CreateProductWithProject(t.Context(), ProductCreation{ProductID: "product-bootstrap", DisplayName: "bootstrap", StageMaturity: "prototype", StageAudienceCommitment: "operator_only", ProjectID: "project-bootstrap", ProjectDisplayName: "bootstrap", Role: "primary", Reason: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProjectLocator(t.Context(), "project-bootstrap", ProjectLocator{ID: "bootstrap-path", Kind: LocatorCanonicalPath, Value: repo}, 1); err != nil {
		t.Fatal(err)
	}
}

func seedBootstrapProject(t *testing.T, s *Store, projectID, repo string) {
	t.Helper()
	if _, err := s.CreateProjectForProduct(t.Context(), ProjectCreation{ProjectID: projectID, DisplayName: projectID, ProductID: "product-bootstrap", Role: "secondary", Reason: "fixture", ExpectedProductVersion: 2}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProjectLocator(t.Context(), projectID, ProjectLocator{ID: "bootstrap-path-" + projectID, Kind: LocatorCanonicalPath, Value: repo}, 1); err != nil {
		t.Fatal(err)
	}
}

func replaceBootstrapMemberships(t *testing.T, s *Store, workID string, version int64, projectID string) {
	t.Helper()
	payload, err := json.Marshal(workMembershipsPayload{Memberships: []workMembershipPayload{{ProjectID: projectID, Role: "primary"}}, ExpectedVersion: version, ResultingVersion: version + 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{{EventID: "bootstrap-memberships-" + projectID, Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
}

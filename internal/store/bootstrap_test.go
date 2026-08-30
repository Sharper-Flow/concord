package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type branchProbeFailureRunner struct{}

func (branchProbeFailureRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "show-ref" {
		return nil, errors.New("injected branch probe failure")
	}
	return (ExecGitRunner{}).Run(ctx, dir, args...)
}

func initBootstrapStoreRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runBootstrapGit(t, repo, "init", "-q", "-b", "main", ".")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("bootstrap\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runBootstrapGit(t, repo, "add", "README.md")
	runBootstrapGit(t, repo, "commit", "-q", "-m", "base")
	runBootstrapGit(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	runBootstrapGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	return repo
}

func runBootstrapGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repo}, args...)...)
	command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func seedBootstrapStoreAuthority(t *testing.T, s *Store, repo string) {
	t.Helper()
	ctx := context.Background()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		{EventID: "bootstrap-product", Kind: "product.created", SubjectType: SubjectProduct, SubjectID: "product-bootstrap", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"bootstrap","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "bootstrap-project", Kind: "project.created", SubjectType: SubjectProject, SubjectID: "project-bootstrap", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"bootstrap"}`)},
		{EventID: "bootstrap-membership", Kind: "product_project.added", SubjectType: SubjectProduct, SubjectID: "product-bootstrap", Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-bootstrap","project_id":"project-bootstrap","role":"primary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product-bootstrap"): 0, VersionRef(SubjectProject, "project-bootstrap"): 0}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProjectLocator(ctx, "project-bootstrap", ProjectLocator{ID: "bootstrap-path", Kind: LocatorCanonicalPath, Value: repo}, 1); err != nil {
		t.Fatal(err)
	}
}

func bootstrapStoreRequest() BootstrapRequest {
	return BootstrapRequest{ProductID: "product-bootstrap", ProjectID: "project-bootstrap", Title: "Bootstrap", ValueStatement: "Work starts in one worktree", Kind: "task", Task: "run", IdempotencyKey: "bootstrap-store", Priority: 1, Urgency: "standard", Ref: "HEAD"}
}

func TestValidateBootstrapTaskUsesUTF8ByteLimit(t *testing.T) {
	base := BootstrapRequest{ProductID: "product", ProjectID: "project", Title: "title", ValueStatement: "value", Kind: "task", IdempotencyKey: "key"}
	base.Task = strings.Repeat("✓", 2730)
	if len(base.Task) != 8190 {
		t.Fatalf("task bytes=%d", len(base.Task))
	}
	if err := validateBootstrapRequest(base); err != nil {
		t.Fatalf("8190-byte UTF-8 task refused: %v", err)
	}
	base.Task += "✓"
	if len(base.Task) != 8193 {
		t.Fatalf("boundary task bytes=%d", len(base.Task))
	}
	if err := validateBootstrapRequest(base); err == nil {
		t.Fatal("8193-byte UTF-8 task accepted")
	}
}

func TestPrepareBootstrapReplayUsesTransactionPinnedLocation(t *testing.T) {
	repo := initBootstrapStoreRepo(t)
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedBootstrapStoreAuthority(t, s, repo)
	req := bootstrapStoreRequest()
	req.Ref = "moving"
	runBootstrapGit(t, repo, "branch", "moving", "HEAD")
	operationID, workID, digest, err := CanonicalBootstrapIdentity(req)
	if err != nil {
		t.Fatal(err)
	}
	firstLocation, err := s.LocateWorktree(context.Background(), req.ProjectID, workID, req.Ref)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.prepareBootstrap(context.Background(), req, operationID, workID, digest, firstLocation, ExecGitRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("bootstrap moved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runBootstrapGit(t, repo, "add", "README.md")
	runBootstrapGit(t, repo, "commit", "-q", "-m", "move ref")
	runBootstrapGit(t, repo, "branch", "-f", "moving", "HEAD")
	secondLocation, err := s.LocateWorktree(context.Background(), req.ProjectID, workID, req.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if firstLocation.BaseSHA == secondLocation.BaseSHA {
		t.Fatal("moving ref did not change")
	}
	second, err := s.prepareBootstrap(context.Background(), req, operationID, workID, digest, secondLocation, ExecGitRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Result.Replayed || second.Location != first.Location || second.Location.BaseSHA != firstLocation.BaseSHA {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
}

func TestBootstrapBranchProbeFailureCreatesNoReplayAuthority(t *testing.T) {
	repo := initBootstrapStoreRepo(t)
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedBootstrapStoreAuthority(t, s, repo)
	req := bootstrapStoreRequest()
	req.IdempotencyKey = "bootstrap-probe-failure"
	_, workID, _, err := CanonicalBootstrapIdentity(req)
	if err != nil {
		t.Fatal(err)
	}
	location, err := s.LocateWorktree(context.Background(), req.ProjectID, workID, req.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.bootstrapWorktree(context.Background(), req, nil, branchProbeFailureRunner{}); err == nil {
		t.Fatal("branch probe failure was accepted")
	}
	var journals int
	if err := s.db.QueryRow("SELECT count(*) FROM bootstrap_operations").Scan(&journals); err != nil || journals != 0 {
		t.Fatalf("branch probe failure journal count=%d err=%v", journals, err)
	}
	runBootstrapGit(t, repo, "branch", location.Branch, location.BaseSHA)
	if _, err := s.BootstrapWorktree(context.Background(), req, nil); err == nil {
		t.Fatal("later request adopted the branch hidden by the failed probe")
	}
	if err := s.db.QueryRow("SELECT count(*) FROM bootstrap_operations").Scan(&journals); err != nil || journals != 0 {
		t.Fatalf("planted branch journal count=%d err=%v", journals, err)
	}
}

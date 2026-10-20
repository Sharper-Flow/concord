package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The repository's own CI declaration is the shard source; this pins the
// derivation to it rather than to a copied package list.
func TestWorkflowShardedPackagesReadsTheRepositoryCIDeclaration(t *testing.T) {
	ci, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(workflowCIWorkflowPath)))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := workflowShardedPackages(ci), map[string]bool{"internal/store": true, "cmd/concord": true}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sharded packages=%v, want %v", got, want)
	}
}

func TestWorkflowShardedPackagesMarksEveryDeclaredShardInvocation(t *testing.T) {
	ci := strings.Join([]string{
		`        run: go test -timeout=15m -run "$(python3 scripts/go-test-shard.py ./internal/alpha 1 2)" ./internal/alpha`,
		`        run: go test -run "$(python3 scripts/go-test-shard.py ./internal/beta 1 2)" ./internal/other`,
		`        run: go test -run "$(python3 scripts/go-test-shard.py ./internal/gamma 0 2)" ./internal/gamma`,
		`        run: go test -run "$(python3 scripts/go-test-shard.py ./vendor/x 1 2)" ./vendor/x`,
		`        run: go test -run "$(python3 scripts/go-test-shard.py ./internal/delta 1 2)" ./internal/delta -v`,
		`        # python3 scripts/go-test-shard.py ./internal/epsilon <i> <n>`,
		`        run: go test ./internal/zeta`,
	}, "\n")
	if got, want := workflowShardedPackages([]byte(ci)), map[string]bool{"internal/alpha": true, "internal/beta": true, "internal/delta": true}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sharded packages=%v, want %v", got, want)
	}
}

func TestReadWorkflowShardedPackagesReadsTheProjectCommit(t *testing.T) {
	ctx := t.Context()
	repo := t.TempDir()
	workflow := filepath.Join(repo, filepath.FromSlash(workflowCIWorkflowPath))
	if err := os.MkdirAll(filepath.Dir(workflow), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "init", "-q")
	runTestGit(t, repo, "add", "-A")
	runTestGit(t, repo, "commit", "-q", "-m", "without CI")
	without := runTestGit(t, repo, "rev-parse", "HEAD")
	shardRun := `run: go test -run "$(python3 scripts/go-test-shard.py ./internal/alpha 1 2)" ./internal/alpha` + "\n"
	if err := os.WriteFile(workflow, []byte(shardRun), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "add", "-A")
	runTestGit(t, repo, "commit", "-q", "-m", "with CI")
	with := runTestGit(t, repo, "rev-parse", "HEAD")
	if err := os.WriteFile(workflow, []byte(strings.ReplaceAll(shardRun, "alpha", "dirty")), 0o600); err != nil {
		t.Fatal(err)
	}

	sharded, err := readWorkflowShardedPackages(ctx, ExecGitRunner{}, repo, with)
	if err != nil || !reflect.DeepEqual(sharded, map[string]bool{"internal/alpha": true}) {
		t.Fatalf("committed declaration=%v, %v", sharded, err)
	}
	if sharded, err := readWorkflowShardedPackages(ctx, ExecGitRunner{}, repo, without); err != nil || len(sharded) != 0 {
		t.Fatalf("commit without CI=%v, %v", sharded, err)
	}
	if _, err := readWorkflowShardedPackages(ctx, ExecGitRunner{}, repo, strings.Repeat("f", 40)); err == nil {
		t.Fatal("an absent commit resolved a shard declaration")
	}
	if _, err := readWorkflowShardedPackages(ctx, ExecGitRunner{}, repo, "HEAD"); err == nil {
		t.Fatal("a symbolic ref resolved a shard declaration")
	}
	for name, failing := range map[string]string{"unreachable repository": "ls-tree", "unreadable workflow blob": "cat-file"} {
		if _, err := readWorkflowShardedPackages(ctx, failingShardGit{fail: failing}, repo, with); err == nil {
			t.Fatalf("an %s resolved an empty shard declaration", name)
		}
	}
}

// failingShardGit runs real git but fails the one subcommand it names.
type failingShardGit struct{ fail string }

func (g failingShardGit) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == g.fail {
		return nil, errors.New(g.fail + " failed")
	}
	return ExecGitRunner{}.Run(ctx, dir, args...)
}

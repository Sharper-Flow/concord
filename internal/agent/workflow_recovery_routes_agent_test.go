package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

func recoveryRegistry(t *testing.T, s *store.Store) (hash, root string) {
	t.Helper()
	if err := s.DatabaseForTesting().QueryRow(`SELECT content_hash,root_domain_id FROM domain_registries WHERE product_id='product-1'`).Scan(&hash, &root); err != nil {
		t.Fatal(err)
	}
	return hash, root
}

func recoveryDomainRepository(t *testing.T, s *store.Store) string {
	t.Helper()
	ctx := context.Background()
	home, err := pm1fixture.SeedCommittedProductDomain(ctx, s, "product-1", "project-1", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	toolingPath := filepath.Join(home.RepoPath, ".concord", "tooling.v1.json")
	if err := os.WriteFile(toolingPath, []byte(`{"schema_version":"1.0","project":"fixture","tools":[{"id":"go-vet","invocation":"go vet ./...","tier":"fast"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", ".concord/tooling.v1.json"}, {"commit", "--quiet", "-m", "declare fixture verification tooling"}} {
		if output, err := exec.Command("git", append([]string{"-C", home.RepoPath}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("fixture tooling: %v: %s", err, output)
		}
	}
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"remote", "add", "origin", "https://example.invalid/fixture.git"}, {"update-ref", "refs/remotes/origin/main", "HEAD"}, {"symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main"}} {
		if output, err := exec.Command("git", append([]string{"-C", home.RepoPath}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("fixture repository: %v: %s", err, output)
		}
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE project_locators SET locator_value=?1, normalized_value=?1 WHERE project_id='project-1' AND kind='canonical_path'; DELETE FROM write_guard`, home.RepoPath); err != nil {
		t.Fatalf("point the fixture locator at the repository: %v", err)
	}
	tooling, err := store.ResolveWorkProjectTooling(ctx, s, "work-1")
	if err != nil || tooling == nil || !store.ProjectToolingInvocationDeclared(tooling, []string{"go", "vet", "./..."}) {
		t.Fatalf("resolve the fixture's default-ref tooling: %+v, %v", tooling, err)
	}
	return home.RepoPath
}

// stepOf reads the workflow instance's current step.
func stepOf(t *testing.T, s *store.Store) string {
	t.Helper()
	var step string
	if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id='work-1'`).Scan(&step); err != nil {
		t.Fatal(err)
	}
	return step
}

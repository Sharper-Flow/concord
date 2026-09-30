package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// resolveContinuityClaim claims one worktree through the standard worktree
// fixture and returns the store plus the claimed path.
func resolveContinuityClaim(t *testing.T) (*Store, *fakeWorktreeGit, string) {
	t.Helper()
	s, git, _ := worktreeFixture(t)
	result, err := s.ClaimWorktree(context.Background(), baseClaim(git))
	if err != nil {
		t.Fatalf("ClaimWorktree() error = %v", err)
	}
	return s, git, result.Entry.Path
}

func TestResolveContinuityWorkByDirectoryResolvesActiveClaim(t *testing.T) {
	t.Parallel()
	s, _, path := resolveContinuityClaim(t)
	resolution, err := s.ResolveContinuityWorkByDirectory(context.Background(), path)
	if err != nil {
		t.Fatalf("ResolveContinuityWorkByDirectory() error = %v", err)
	}
	if resolution.WorkID != "work-w" || resolution.ProjectID != "project-w" || resolution.ProductID != "product-w" {
		t.Fatalf("resolution = %+v, want work-w/project-w/product-w", resolution)
	}
}

func TestResolveContinuityWorkByDirectoryNormalizesDirectoryForms(t *testing.T) {
	t.Parallel()
	s, _, path := resolveContinuityClaim(t)
	// The fake git runner never materializes the worktree, but a real claimed
	// worktree exists on disk and a host may report it through a symlink.
	// Materialize it so the symlink form resolves the way production does.
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "linked-worktree")
	if err := os.Symlink(path, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	ctx := context.Background()
	for _, directory := range []string{filepath.Join(path, "nested", "dir", "..", ".."), link} {
		resolution, err := s.ResolveContinuityWorkByDirectory(ctx, directory)
		if err != nil {
			t.Fatalf("ResolveContinuityWorkByDirectory(%q) error = %v", directory, err)
		}
		if resolution.WorkID != "work-w" {
			t.Fatalf("ResolveContinuityWorkByDirectory(%q) = %+v, want the claimed work item", directory, resolution)
		}
	}
}

func TestResolveContinuityWorkByDirectoryReclaimedClaimIsAbsence(t *testing.T) {
	t.Parallel()
	s, git, path := resolveContinuityClaim(t)
	if _, err := s.ReclaimWorktree(context.Background(), WorktreeReclaimRequest{
		WorkID: "work-w", ProjectID: "project-w", DefaultRef: "origin/main",
		PrincipalRef: "principal-1", RequestID: "req-reclaim", ExpectedVersion: 3,
		Now: time.Unix(20, 0).UTC(), Runner: git,
	}); err != nil {
		t.Fatalf("ReclaimWorktree() error = %v", err)
	}
	resolution, err := s.ResolveContinuityWorkByDirectory(context.Background(), path)
	if err != nil {
		t.Fatalf("ResolveContinuityWorkByDirectory() error = %v", err)
	}
	if resolution.WorkID != "" {
		t.Fatalf("reclaimed claim resolved %+v, want absence", resolution)
	}
}

func TestResolveContinuityWorkByDirectoryUnknownPathIsAbsence(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	for _, directory := range []string{"", t.TempDir(), filepath.Join(t.TempDir(), "missing", "leaf")} {
		resolution, err := s.ResolveContinuityWorkByDirectory(ctx, directory)
		if err != nil {
			t.Fatalf("ResolveContinuityWorkByDirectory(%q) error = %v", directory, err)
		}
		if resolution.WorkID != "" {
			t.Fatalf("ResolveContinuityWorkByDirectory(%q) = %+v, want absence", directory, resolution)
		}
	}
}

package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

// gatedWorktreeGit blocks one native git verb until the test releases it, so
// a test can observe the store while that verb runs.
type gatedWorktreeGit struct {
	*fakeWorktreeGit
	verb    string
	entered chan struct{}
	release chan struct{}
}

func newGatedWorktreeGit(inner *fakeWorktreeGit, verb string) *gatedWorktreeGit {
	return &gatedWorktreeGit{fakeWorktreeGit: inner, verb: verb, entered: make(chan struct{}), release: make(chan struct{})}
}

func (g *gatedWorktreeGit) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if strings.HasPrefix(strings.Join(args, " "), g.verb) {
		close(g.entered)
		<-g.release
	}
	return g.fakeWorktreeGit.Run(ctx, dir, args...)
}

// beginWriteWithin opens a second connection to the store file, as another
// Concord process would, and reports whether it takes the write lock within
// the given busy timeout.
func beginWriteWithin(t *testing.T, path string, timeout time.Duration) error {
	t.Helper()
	dsn := "file:" + path + "?_txlock=immediate&_pragma=" + url.QueryEscape(fmt.Sprintf("busy_timeout(%d)", timeout.Milliseconds()))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	return tx.Rollback()
}

// A write transaction must not stay open across a native git mutation
// (CD-0195 D2). While reclaim runs git worktree remove, another process must
// still take the write lock.
func TestReclaimWorktreeHoldsNoWriteLockDuringNativeRemove(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	if _, err := s.ClaimWorktree(context.Background(), baseClaim(git)); err != nil {
		t.Fatal(err)
	}
	gated := newGatedWorktreeGit(git, "worktree remove")
	done := make(chan error, 1)
	go func() {
		_, err := s.ReclaimWorktree(context.Background(), WorktreeReclaimRequest{
			WorkID: "work-w", ProjectID: "project-w", DefaultRef: "origin/main", PrincipalRef: "principal-1", RequestID: "reclaim-1", ExpectedVersion: 3,
			Now: time.Unix(20, 0).UTC(), Runner: gated})
		done <- err
	}()
	select {
	case <-gated.entered:
	case err := <-done:
		t.Fatalf("reclaim returned before git worktree remove: %v", err)
	}
	writeErr := beginWriteWithin(t, s.Path(), 250*time.Millisecond)
	close(gated.release)
	if err := <-done; err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if writeErr != nil {
		t.Fatalf("a concurrent writer could not begin while reclaim ran git worktree remove: %v", writeErr)
	}
}

// While claim runs git worktree add, another process must still take the
// write lock.
func TestClaimWorktreeHoldsNoWriteLockDuringNativeAdd(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	gated := newGatedWorktreeGit(git, "worktree add")
	claim := baseClaim(git)
	claim.Runner = gated
	done := make(chan error, 1)
	go func() {
		_, err := s.ClaimWorktree(context.Background(), claim)
		done <- err
	}()
	select {
	case <-gated.entered:
	case err := <-done:
		t.Fatalf("claim returned before git worktree add: %v", err)
	}
	writeErr := beginWriteWithin(t, s.Path(), 250*time.Millisecond)
	close(gated.release)
	if err := <-done; err != nil {
		t.Fatalf("claim: %v", err)
	}
	if writeErr != nil {
		t.Fatalf("a concurrent writer could not begin while claim ran git worktree add: %v", writeErr)
	}
}

package store

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

// routeVacate is the declared route the occupancy gate names: vacate the
// occupying session, then reclaim the worktree. It is ordered, and it mirrors
// the agent envelope's RecoveryAction.RequiredRefs carrier.
var routeVacate = []string{"session_vacate", "worktree_reclaim"}

// The live-occupant refusal names the declared route instead of operator
// prose. The occupant may be the caller's own session, and vacate-then-
// reclaim is the route the workflow already admits for a clean merged
// worktree, so the refusal carries those operations, in order.
func TestOccupancyRefusalNamesSessionVacate(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	req.SessionRef = "ses_live"
	claimed, err := s.ClaimWorktree(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	seedWorktreeLifecycle(t, s, "work-w", "completed", 3)

	_, err = s.ReclaimWorktree(context.Background(), WorktreeReclaimRequest{
		WorkID: "work-w", ProjectID: "project-w", DefaultRef: "origin/main",
		PrincipalRef: "principal-1", RequestID: "req-occupant-route", ExpectedVersion: 4,
		Now: time.Unix(20, 0).UTC(), Runner: git,
	})
	failure, ok := err.(*Failure)
	if !ok || failure.Kind != KindWorktreeOwnershipConflict {
		t.Fatalf("err=%v, want worktree_ownership_conflict", err)
	}
	if failure.RecoveryAction != RecoveryUseDeclaredRoute {
		t.Fatalf("recovery=%q, want %q", failure.RecoveryAction, RecoveryUseDeclaredRoute)
	}
	if !slices.Equal(failure.RecoveryRefs, routeVacate) {
		t.Fatalf("refs=%v, want %v in execution order", failure.RecoveryRefs, routeVacate)
	}
	if !strings.Contains(failure.Detail, "ses_live") || !strings.Contains(failure.Detail, claimed.Entry.Path) {
		t.Fatalf("refusal %q must still name the occupant and the worktree", failure.Detail)
	}
	if _, still := git.worktrees[claimed.Entry.Path]; !still {
		t.Fatal("no refused attempt may remove the worktree")
	}
}

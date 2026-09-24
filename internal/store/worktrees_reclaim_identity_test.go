package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestReclaimWorktreeConvergesRecordedReclaimForLaterOperation(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	claim := baseClaim(git)
	if _, err := s.ClaimWorktree(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	eventID := reclaimedEventID("work-w", "project-w", claim.OpID, 0)
	payload := fmt.Sprintf(`{"expected_version":3,"resulting_version":4,"set_id":%q,"project_id":"project-w","claim_op_id":%q,"request_id":"first-reclaim","git_facts":{"already_absent":true}}`, WorktreeSetID("work-w"), claim.OpID)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,1,?)`, eventID, "work.worktree_reclaimed", string(SubjectWorkItem), "work-w", "principal-1", time.Unix(20, 0).UTC().Format(time.RFC3339Nano), payload); err != nil {
		t.Fatal(err)
	}

	_, err := s.ReclaimWorktree(context.Background(), WorktreeReclaimRequest{
		WorkID: "work-w", ProjectID: "project-w", DefaultRef: "origin/main", PrincipalRef: "principal-1", RequestID: "later-reclaim", ExpectedVersion: 3,
		Now: time.Unix(30, 0).UTC(), Runner: git, ObservedSessionDirectories: emptySessionObservation(), ObservedProjectID: "project-w",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, kept := git.worktrees[claimPath(s)]; kept {
		t.Fatal("a later reclaim must remove the native worktree")
	}
}

func TestReclaimWorktreeRecreatedDirectoryCanBeReclaimedByLaterOperation(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	claim := baseClaim(git)
	if _, err := s.ClaimWorktree(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	reclaim := WorktreeReclaimRequest{
		WorkID: "work-w", ProjectID: "project-w", DefaultRef: "origin/main", PrincipalRef: "principal-1", RequestID: "first-reclaim", ExpectedVersion: 3,
		Now: time.Unix(20, 0).UTC(), Runner: git, ObservedSessionDirectories: emptySessionObservation(), ObservedProjectID: "project-w",
	}
	if _, err := s.ReclaimWorktree(context.Background(), reclaim); err != nil {
		t.Fatal(err)
	}

	path := claimPath(s)
	git.branches[claimBranch()] = git.branches["main"]
	git.worktrees[path] = claimBranch()
	git.worktreeRepos[path] = filepath.Clean(git.repoRoot)
	removes := git.countCalls("worktree remove")
	later := reclaim
	later.RequestID = "later-reclaim"
	later.ExpectedVersion = 4
	_, err := s.ReclaimWorktree(context.Background(), later)
	if err != nil {
		t.Fatal(err)
	}
	if git.countCalls("worktree remove") != removes+1 {
		t.Fatal("a later reclaim must remove the recreated directory")
	}

	if _, err := s.ReclaimWorktree(context.Background(), reclaim); err != nil {
		t.Fatalf("same reclaim retry=%v, want an idempotent result", err)
	}
	if git.countCalls("worktree remove") != removes+1 {
		t.Fatal("an idempotent retry must not remove the directory again")
	}
}

// The event identity derivation scopes to the claim incarnation: the first
// incarnation keeps the legacy identity byte for byte, and a reopened
// incarnation derives its own.
func TestClaimIncarnationEventIDScoping(t *testing.T) {
	t.Parallel()
	if got := reclaimedEventID("work-w", "project-w", "wt-op-1", 0); got != "work-w:project-w:wt-op-1:worktree-reclaimed" {
		t.Fatalf("first incarnation reclaim identity=%q", got)
	}
	if got := reclaimedEventID("work-w", "project-w", "wt-op-1", 2); got != "work-w:project-w:wt-op-1:worktree-reclaimed:i2" {
		t.Fatalf("reopened incarnation reclaim identity=%q", got)
	}
	if got := occupancyReleasedEventID("work-w", "project-w", "wt-op-1", 0); got != "work-w:project-w:wt-op-1:worktree-occupancy-released" {
		t.Fatalf("first incarnation release identity=%q", got)
	}
	if got := occupancyReleasedEventID("work-w", "project-w", "wt-op-1", 3); got != "work-w:project-w:wt-op-1:worktree-occupancy-released:i3" {
		t.Fatalf("reopened incarnation release identity=%q", got)
	}
}

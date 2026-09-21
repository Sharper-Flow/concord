package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// insertRawReclaimEvent reproduces the backfill shape the convergence repair
// targets: a reclaim event row that entered the log without its fold, so the
// worktree projection never saw it. The row itself is valid log history; the
// insert bypasses only the fold, exactly as a raw backfill does.
func insertRawReclaimEvent(t *testing.T, s *Store, workID, eventID, kind, actor, payload string, at time.Time) {
	t.Helper()
	_, err := s.DatabaseForTesting().Exec(
		`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,1,?)`,
		eventID, kind, string(SubjectWorkItem), workID, actor,
		at.UTC().Format(time.RFC3339Nano), payload)
	if err != nil {
		t.Fatal(err)
	}
}

func reclaimedFixtureEventID(workID, claimOpID string) string {
	return fmt.Sprintf("%s:%s:%s:worktree-reclaimed", workID, "project-w", claimOpID)
}

func reclaimedFixturePayload(workID string, expected, resulting int64, claimOpID, facts string) string {
	return fmt.Sprintf(`{"expected_version":%d,"resulting_version":%d,"set_id":%q,"project_id":"project-w","claim_op_id":%q,"git_facts":%s}`,
		expected, resulting, WorktreeSetID(workID), claimOpID, facts)
}

func convergeWorkVersion(t *testing.T, s *Store, workID string) int64 {
	t.Helper()
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func convergeEntryState(t *testing.T, s *Store, workID, projectID string) string {
	t.Helper()
	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT state FROM worktree_entries WHERE set_id=? AND project_id=?`, WorktreeSetID(workID), projectID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func convergeClaimState(t *testing.T, s *Store, claimOpID string) string {
	t.Helper()
	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT state FROM worktree_claims WHERE op_id=?`, claimOpID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

// A reclaim colliding with an already-recorded reclaimed event for the same
// claim generation converges on the stored event: the log row is the durable
// reclaim record, its projection repair runs, and the native removal
// proceeds. The stored fold's version advance is already public history, so
// the repair does not advance the work item's version again.
func TestReclaimWorktreeConvergesOnUnfoldedReclaimEvent(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	ctx := context.Background()
	path := auditWork(t, s, git, "work-unfolded", true)
	completeAuditWork(t, s, "work-unfolded", 3)
	insertRawReclaimEvent(t, s, "work-unfolded",
		reclaimedFixtureEventID("work-unfolded", "wt-work-unfolded"),
		"work.worktree_reclaimed", "principal:operator",
		reclaimedFixturePayload("work-unfolded", 3, 4, "wt-work-unfolded", `{"already_absent":true}`),
		time.Unix(35, 0).UTC())

	req := WorktreeReclaimRequest{
		WorkID: "work-unfolded", ProjectID: "project-w", PrincipalRef: "principal-1", RequestID: "converge-terminal",
		ExpectedVersion: 4, Now: time.Unix(40, 0).UTC(), Runner: git, RequireTerminal: true,
		ObservedSessionDirectories: emptySessionObservation(), ObservedProjectID: "project-w",
	}
	entry, err := s.ReclaimWorktree(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if entry.State != worktreeEntryReclaimed {
		t.Fatalf("entry=%+v", entry)
	}
	if got := convergeEntryState(t, s, "work-unfolded", "project-w"); got != worktreeEntryReclaimed {
		t.Fatalf("folded entry state=%q", got)
	}
	if got := convergeWorkVersion(t, s, "work-unfolded"); got != 4 {
		t.Fatalf("convergence must not advance the version, got %d", got)
	}
	if got := convergeClaimState(t, s, "wt-work-unfolded"); got != worktreeStateReclaimed {
		t.Fatalf("claim state=%q", got)
	}
	if _, kept := git.worktrees[path]; kept {
		t.Fatal("native removal did not proceed after convergence")
	}

	// The converged reclaim is idempotent: the folded entry short-circuits
	// the tier gates before any further event or git work.
	again, err := s.ReclaimWorktree(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if again.State != worktreeEntryReclaimed {
		t.Fatalf("second reclaim=%+v", again)
	}
	if git.countCalls("worktree remove") != 1 {
		t.Fatalf("native remove calls=%d", git.countCalls("worktree remove"))
	}
}

// When the stored fold's version advance is still pending — the work item's
// version sits at the stored expected version — convergence runs the fold's
// own version rule, so the advance lands with the projection repair.
func TestReclaimWorktreeConvergenceAdvancesPendingStoredVersion(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	ctx := context.Background()
	path := auditWork(t, s, git, "work-pending", true)
	insertRawReclaimEvent(t, s, "work-pending",
		reclaimedFixtureEventID("work-pending", "wt-work-pending"),
		"work.worktree_reclaimed", "principal:operator",
		reclaimedFixturePayload("work-pending", 3, 4, "wt-work-pending", `{"already_absent":true}`),
		time.Unix(35, 0).UTC())

	_, err := s.ReclaimWorktree(ctx, WorktreeReclaimRequest{
		WorkID: "work-pending", ProjectID: "project-w", PrincipalRef: "principal-1", RequestID: "converge-unstarted",
		ExpectedVersion: 3, Now: time.Unix(40, 0).UTC(), Runner: git, RequireUnstarted: true, DefaultRef: "origin/main",
		ObservedSessionDirectories: emptySessionObservation(), ObservedProjectID: "project-w",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := convergeWorkVersion(t, s, "work-pending"); got != 4 {
		t.Fatalf("pending stored advance must land, got version %d", got)
	}
	if got := convergeEntryState(t, s, "work-pending", "project-w"); got != worktreeEntryReclaimed {
		t.Fatalf("folded entry state=%q", got)
	}
	var lifecycle string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle FROM work_items WHERE id='work-pending'`).Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "needed" {
		t.Fatalf("convergence must not change the lifecycle, got %q", lifecycle)
	}
	if _, kept := git.worktrees[path]; kept {
		t.Fatal("native removal did not proceed after convergence")
	}
}

// The convergence interpretation is scoped to the reclaiming claim's
// generation: a recorded event holding the derived event_id but naming a
// different claim generation keeps the divergent-reuse refusal.
func TestReclaimWorktreeKeepsRefusingDifferentClaimGeneration(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	ctx := context.Background()
	path := auditWork(t, s, git, "work-othergen", true)
	completeAuditWork(t, s, "work-othergen", 3)
	insertRawReclaimEvent(t, s, "work-othergen",
		reclaimedFixtureEventID("work-othergen", "wt-work-othergen"),
		"work.worktree_reclaimed", "principal:operator",
		reclaimedFixturePayload("work-othergen", 3, 4, "wt-a-different-generation", `{"already_absent":true}`),
		time.Unix(35, 0).UTC())

	_, err := s.ReclaimWorktree(ctx, WorktreeReclaimRequest{
		WorkID: "work-othergen", ProjectID: "project-w", PrincipalRef: "principal-1", RequestID: "converge-othergen",
		ExpectedVersion: 4, Now: time.Unix(40, 0).UTC(), Runner: git, RequireTerminal: true,
		ObservedSessionDirectories: emptySessionObservation(), ObservedProjectID: "project-w",
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindIdempotencyConflict {
		t.Fatalf("want idempotency_conflict, got %+v", err)
	}
	if _, kept := git.worktrees[path]; !kept {
		t.Fatal("refused reclaim must keep the worktree")
	}
}

// The convergence interpretation is scoped to kind work.worktree_reclaimed:
// a collision whose stored row carries another kind is still the
// divergent-reuse refusal.
func TestReclaimWorktreeKeepsRefusingDifferentKind(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	ctx := context.Background()
	path := auditWork(t, s, git, "work-otherkind", true)
	completeAuditWork(t, s, "work-otherkind", 3)
	insertRawReclaimEvent(t, s, "work-otherkind",
		reclaimedFixtureEventID("work-otherkind", "wt-work-otherkind"),
		"work.worktree_occupancy_released", "principal:operator",
		`{"set_id":"wts:work-otherkind","project_id":"project-w","claim_op_id":"wt-work-otherkind","occupant_session_ref":"ses-1"}`,
		time.Unix(35, 0).UTC())

	_, err := s.ReclaimWorktree(ctx, WorktreeReclaimRequest{
		WorkID: "work-otherkind", ProjectID: "project-w", PrincipalRef: "principal-1", RequestID: "converge-otherkind",
		ExpectedVersion: 4, Now: time.Unix(40, 0).UTC(), Runner: git, RequireTerminal: true,
		ObservedSessionDirectories: emptySessionObservation(), ObservedProjectID: "project-w",
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindIdempotencyConflict {
		t.Fatalf("want idempotency_conflict, got %+v", err)
	}
	if _, kept := git.worktrees[path]; !kept {
		t.Fatal("refused reclaim must keep the worktree")
	}
}

// The audit pass converges the same deadlock and reports the work item's
// true post-reclaim version: the no-advance repair leaves the version at its
// current value rather than promising the bump that did not happen.
func TestWorktreeAuditReclaimConvergesUnfoldedReclaimEvent(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	ctx := context.Background()
	path := auditWork(t, s, git, "work-audit-unfolded", true)
	completeAuditWork(t, s, "work-audit-unfolded", 3)
	insertRawReclaimEvent(t, s, "work-audit-unfolded",
		reclaimedFixtureEventID("work-audit-unfolded", "wt-work-audit-unfolded"),
		"work.worktree_reclaimed", "principal:operator",
		reclaimedFixturePayload("work-audit-unfolded", 3, 4, "wt-work-audit-unfolded", `{"already_absent":true}`),
		time.Unix(35, 0).UTC())

	result, err := s.WorktreeAuditReclaim(ctx, WorktreeAuditReclaimRequest{
		ProductID: "product-w", DefaultRef: "origin/main", PrincipalRef: "principal-1",
		RequestID: "audit-converge", Now: time.Unix(40, 0).UTC(), Runner: git, Limit: 100,
		ObservedSessionDirectories: emptySessionObservation(), ObservedProjectID: "project-w",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("rows=%+v", result.Rows)
	}
	row := result.Rows[0]
	if row.WorkID != "work-audit-unfolded" || row.Outcome != WorktreeAuditReclaimed {
		t.Fatalf("row=%+v", row)
	}
	if row.Version != 4 {
		t.Fatalf("audit row version=%d, want the unadvanced 4", row.Version)
	}
	if _, kept := git.worktrees[path]; kept {
		t.Fatal("native worktree was not removed")
	}
}

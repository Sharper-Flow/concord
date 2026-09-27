package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/hostlease"
)

// CD-0179 pins the occupancy law this file exercises: a verified landing or
// a session_vacate releases every other occupancy row of that session in any
// work item; a claim records the host process identity at creation; a legacy
// row releases when every live host lease started after the row's
// recorded_at; and an unreadable lease set releases nothing.

func TestClaimRecordsHostProcessIdentity(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	req.SessionRef = "ses-claim"
	req.HostPID = os.Getpid()
	if _, err := s.ClaimWorktree(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	var pid int64
	var pidStart uint64
	var hasIdentity int
	err := s.db.QueryRow(`SELECT host_pid, host_pid_start, has_process_identity FROM worktree_occupancy WHERE worktree_id=?`,
		worktreeOccupancyID(WorktreeSetID("work-w"), "project-w", "wt-op-1")).Scan(&pid, &pidStart, &hasIdentity)
	if err != nil {
		t.Fatal(err)
	}
	start, err := hostlease.ProcessStart(os.Getpid())
	if err != nil {
		t.Fatalf("the test process is not observable: %v", err)
	}
	if pid != int64(os.Getpid()) || pidStart != start || hasIdentity != 1 {
		t.Fatalf("row pid=%d start=%d identity=%d, want the test process identity the core derived", pid, pidStart, hasIdentity)
	}
}

func TestSessionVacateReleasesEveryWorkItemRow(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	req.SessionRef = "ses-x"
	if _, err := s.ClaimWorktree(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	// The same session holds a stale row on another work item's worktree.
	auditWork(t, s, git, "work-b", true)
	setWorktreeOccupant(t, s, "work-b", "ses-x")

	if worktreeOccupancyByEntry(t, s, WorktreeSetID("work-b"), "project-w", "wt-work-b") != "ses-x" {
		t.Fatal("the second work item's row must exist before the vacate")
	}
	vacatePayload := jsonRaw(`{"work_id":"work-w","project_id":"project-w","session_ref":"ses-x","source_directory":"` + claimPath(s) + `"}`)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{{
		EventID: "vacate-cross", Kind: "work.session_vacated", SubjectType: SubjectWorkItem, SubjectID: "work-w",
		Actor: "ses-x", OccurredAt: time.Unix(50, 0).UTC(), PayloadVersion: 1, Payload: vacatePayload,
	}}}); err != nil {
		t.Fatal(err)
	}
	if got := worktreeOccupancyByEntry(t, s, WorktreeSetID("work-w"), "project-w", "wt-op-1"); got != "" {
		t.Fatalf("vacated worktree still records %q", got)
	}
	if got := worktreeOccupancyByEntry(t, s, WorktreeSetID("work-b"), "project-w", "wt-work-b"); got != "" {
		t.Fatalf("the vacate left a stale row on another work item: %q", got)
	}
}

func TestClaimLandingReleasesEveryWorkItemRow(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	// The session holds a stale row on another work item's worktree.
	auditWork(t, s, git, "work-b", true)
	setWorktreeOccupant(t, s, "work-b", "ses-x")
	// The resumed-session shape: the destination records no occupant yet.
	landingReq := baseClaim(git)
	landingReq.OpID = "wt-landing"
	landingReq.Now = time.Unix(20, 0).UTC()
	if _, err := s.ClaimWorktree(context.Background(), landingReq); err != nil {
		t.Fatal(err)
	}
	landing, err := s.RecordWorktreeClaimLanding(context.Background(), WorktreeClaimLandingRequest{
		WorkID: "work-w", SessionRef: "ses-x", LandedDirectory: claimPath(s), HostPID: os.Getpid(), Now: time.Unix(25, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, source := range landing.ReleasedSources {
		if strings.HasSuffix(source, "work-b") {
			found = true
		}
	}
	if !found {
		t.Fatalf("released sources %v must name the other work item's worktree", landing.ReleasedSources)
	}
	if got := worktreeOccupancyByEntry(t, s, WorktreeSetID("work-b"), "project-w", "wt-work-b"); got != "" {
		t.Fatalf("the landing left a stale row on another work item: %q", got)
	}
	if got := worktreeOccupancyByEntry(t, s, WorktreeSetID("work-w"), "project-w", "wt-landing"); got != "ses-x" {
		t.Fatalf("landed row records %q, want ses-x", got)
	}
}

func TestLegacyRowReleasesOnEndedRecordingProcess(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	// A live lease exists, and every live lease started after the row was
	// recorded: the recording process has ended, so the row releases.
	writeProtectingHostLease(t, s)
	req := baseClaim(git)
	req.SessionRef = "ses-old"
	req.Now = time.Now().UTC()
	if _, err := s.ClaimWorktree(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	previous := hostLeaseWallStart
	hostLeaseWallStart = func(int) (time.Time, error) { return time.Now().Add(time.Hour), nil }
	t.Cleanup(func() { hostLeaseWallStart = previous })

	seedWorktreeLifecycle(t, s, "work-w", "completed", 3)
	if _, err := s.ReclaimWorktree(context.Background(), WorktreeReclaimRequest{
		WorkID: "work-w", ProjectID: "project-w", DefaultRef: "origin/main",
		PrincipalRef: "principal-1", RequestID: "req-legacy-release", ExpectedVersion: 4,
		Now: time.Unix(40, 0).UTC(), Runner: git,
	}); err != nil {
		t.Fatalf("the ended recording process must release the legacy row: %v", err)
	}
	var released int
	if err := s.db.QueryRow(`SELECT count(*) FROM domain_events WHERE kind='work.worktree_occupancy_released' AND json_extract(payload,'$.session_ref')='ses-old'`).Scan(&released); err != nil {
		t.Fatal(err)
	}
	if released != 1 {
		t.Fatalf("released events=%d, want the recorded lease-proof release", released)
	}
}

func TestLegacyRowStaysOnUnreadableLeaseSet(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	req.SessionRef = "ses-old"
	req.Now = time.Now().UTC()
	if _, err := s.ClaimWorktree(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	// A hosts directory the reader cannot interpret is an unreadable lease
	// set, and an unreadable lease set releases nothing.
	if err := os.MkdirAll(filepath.Join(filepath.Dir(s.Path()), "hosts", "stray"), 0o700); err != nil {
		t.Fatal(err)
	}
	seedWorktreeLifecycle(t, s, "work-w", "completed", 3)
	_, err := s.ReclaimWorktree(context.Background(), WorktreeReclaimRequest{
		WorkID: "work-w", ProjectID: "project-w", DefaultRef: "origin/main",
		PrincipalRef: "principal-1", RequestID: "req-legacy-unreadable", ExpectedVersion: 4,
		Now: time.Unix(40, 0).UTC(), Runner: git,
	})
	failure, ok := err.(*Failure)
	if !ok || failure.Kind != KindWorktreeOwnershipConflict {
		t.Fatalf("err=%v, want worktree_ownership_conflict", err)
	}
	if got := worktreeOccupancyByEntry(t, s, WorktreeSetID("work-w"), "project-w", "wt-op-1"); got != "ses-old" {
		t.Fatalf("unreadable lease set released the row: %q", got)
	}
}

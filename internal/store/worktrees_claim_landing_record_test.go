package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// The landing record transfers the session's occupancy inside one
// transaction: the destination row must be active and occupied by this
// session, the other active rows of the same work item clear, one durable
// event names the session, work item, source paths, and landed path, and the
// same landing replays idempotently. A destination the session does not
// occupy refuses before any effect.
func TestClaimLandingTransfersOccupancyInOneTransaction(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	siblingProjectFixture(t, s, git)
	ctx := context.Background()

	reqA := baseClaim(git)
	reqA.ExpectedVersion = 3
	reqA.SessionRef = "ses-1"
	claimedA, err := s.ClaimWorktree(ctx, reqA)
	if err != nil {
		t.Fatal(err)
	}
	reqB := siblingClaim(git, "wt-op-2", 4)
	reqB.SessionRef = "ses-1"
	claimedB, err := s.ClaimWorktree(ctx, reqB)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Unix(30, 0).UTC()
	landing, err := s.RecordWorktreeClaimLanding(ctx, WorktreeClaimLandingRequest{WorkID: "work-w", SessionRef: "ses-1", LandedDirectory: claimedB.Entry.Path, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if landing.ProjectID != "project-b" || landing.AlreadyRecorded {
		t.Fatalf("landing=%+v, want the project-b destination freshly recorded", landing)
	}
	if !slices.Equal(landing.ReleasedSources, []string{claimedA.Entry.Path}) {
		t.Fatalf("released sources=%v, want %v", landing.ReleasedSources, []string{claimedA.Entry.Path})
	}

	byProject := worktreeEntriesByProject(t, s, "work-w")
	if byProject["project-b"].OccupantSessionRef != "ses-1" {
		t.Fatalf("destination entry=%+v, want ses-1 to hold the claimed worktree", byProject["project-b"])
	}
	if byProject["project-w"].OccupantSessionRef != "" {
		t.Fatalf("source entry=%+v, want the sibling occupancy cleared by the landing", byProject["project-w"])
	}

	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM domain_events WHERE kind='work.session_claim_landed' AND subject_id='work-w'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("landing event count=%d, want exactly one", count)
	}
	var payload []byte
	if err := s.db.QueryRow(`SELECT payload FROM domain_events WHERE kind='work.session_claim_landed'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var recorded struct {
		WorkID            string   `json:"work_id"`
		ProjectID         string   `json:"project_id"`
		SessionRef        string   `json:"session_ref"`
		SourceDirectories []string `json:"source_directories"`
		LandedDirectory   string   `json:"landed_directory"`
	}
	if err := json.Unmarshal(payload, &recorded); err != nil {
		t.Fatal(err)
	}
	if recorded.WorkID != "work-w" || recorded.ProjectID != "project-b" || recorded.SessionRef != "ses-1" || recorded.LandedDirectory != claimedB.Entry.Path {
		t.Fatalf("recorded landing=%+v, want the session, work item, and landed path named", recorded)
	}
	if !slices.Equal(recorded.SourceDirectories, []string{claimedA.Entry.Path}) {
		t.Fatalf("recorded sources=%v, want %v", recorded.SourceDirectories, []string{claimedA.Entry.Path})
	}

	replay, err := s.RecordWorktreeClaimLanding(ctx, WorktreeClaimLandingRequest{WorkID: "work-w", SessionRef: "ses-1", LandedDirectory: claimedB.Entry.Path, Now: now})
	if err != nil || !replay.AlreadyRecorded {
		t.Fatalf("replay landing=%+v err=%v, want an idempotent replay", replay, err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM domain_events WHERE kind='work.session_claim_landed'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("landing event count after replay=%d, want still one", count)
	}

	// The vacated source path is no longer occupied by the session, so a
	// landing there refuses before effect.
	_, err = s.RecordWorktreeClaimLanding(ctx, WorktreeClaimLandingRequest{WorkID: "work-w", SessionRef: "ses-1", LandedDirectory: claimedA.Entry.Path, Now: now})
	failure, ok := err.(*Failure)
	if !ok || failure.Kind != KindWorktreeOwnershipConflict {
		t.Fatalf("unoccupied destination err=%v, want %s", err, KindWorktreeOwnershipConflict)
	}

	// A path with no active row of this work item refuses as absent.
	_, err = s.RecordWorktreeClaimLanding(ctx, WorktreeClaimLandingRequest{WorkID: "work-w", SessionRef: "ses-1", LandedDirectory: filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-w", "work-elsewhere"), Now: now})
	failure, ok = err.(*Failure)
	if !ok || failure.Kind != KindProjectionNotFound {
		t.Fatalf("absent destination err=%v, want %s", err, KindProjectionNotFound)
	}

	// Another session's occupancy refuses the landing.
	if _, err := s.db.Exec(`UPDATE worktree_entries SET occupant_session_ref='ses-9' WHERE set_id=? AND project_id='project-b'`, WorktreeSetID("work-w")); err != nil {
		t.Fatal(err)
	}
	_, err = s.RecordWorktreeClaimLanding(ctx, WorktreeClaimLandingRequest{WorkID: "work-w", SessionRef: "ses-1", LandedDirectory: claimedB.Entry.Path, Now: now})
	failure, ok = err.(*Failure)
	if !ok || failure.Kind != KindWorktreeOwnershipConflict {
		t.Fatalf("foreign occupant err=%v, want %s", err, KindWorktreeOwnershipConflict)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM domain_events WHERE kind='work.session_claim_landed'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("landing event count after refusals=%d, want no refused landing to record", count)
	}
}

// The landing's replay guard reads state, not a derived event id: the claimed
// path per Project is deterministic, so an event id naming the path would own
// the replay for the work item's whole life. After a reclaim and a new claim
// at the same derived path, a real second landing must record its own
// transfer and clear the sibling row the session occupies.
func TestClaimLandingAfterReclaimRecordsSecondTransferAtSamePath(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	siblingProjectFixture(t, s, git)
	ctx := context.Background()

	reqA := baseClaim(git)
	reqA.ExpectedVersion = 3
	reqA.SessionRef = "ses-1"
	claimedA, err := s.ClaimWorktree(ctx, reqA)
	if err != nil {
		t.Fatal(err)
	}
	reqB := siblingClaim(git, "wt-op-2", 4)
	reqB.SessionRef = "ses-1"
	claimedB, err := s.ClaimWorktree(ctx, reqB)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Unix(30, 0).UTC()
	landing, err := s.RecordWorktreeClaimLanding(ctx, WorktreeClaimLandingRequest{WorkID: "work-w", SessionRef: "ses-1", LandedDirectory: claimedB.Entry.Path, Now: now})
	if err != nil || landing.AlreadyRecorded {
		t.Fatalf("first landing=%+v err=%v, want a fresh transfer", landing, err)
	}

	reclaim := func(projectID, requestID string, expectedVersion int64) {
		t.Helper()
		_, err := s.ReclaimWorktree(ctx, WorktreeReclaimRequest{
			WorkID: "work-w", ProjectID: projectID, DefaultRef: "origin/main", PrincipalRef: "principal-1", RequestID: requestID, ExpectedVersion: expectedVersion,
			Now: now, Runner: git, ObservedSessionDirectories: emptySessionObservation(), ObservedProjectID: projectID,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	reclaim("project-w", "reclaim-w", 5)
	// The fake shares branch state across repositories, and the reclaim just
	// deleted the branch name project-b's repository still holds.
	git.branches[claimBranch()] = reqB.BaseSHA
	reclaim("project-b", "reclaim-b", 6)

	reqA2 := reqA
	reqA2.OpID = "wt-op-3"
	reqA2.RequestID = "req-3"
	reqA2.ExpectedVersion = 7
	if _, err := s.ClaimWorktree(ctx, reqA2); err != nil {
		t.Fatal(err)
	}
	reqB2 := siblingClaim(git, "wt-op-4", 8)
	reqB2.SessionRef = "ses-1"
	claimedB2, err := s.ClaimWorktree(ctx, reqB2)
	if err != nil {
		t.Fatal(err)
	}
	if claimedB2.Entry.Path != claimedB.Entry.Path {
		t.Fatalf("re-claimed path=%s, want the same derived path %s", claimedB2.Entry.Path, claimedB.Entry.Path)
	}

	second, err := s.RecordWorktreeClaimLanding(ctx, WorktreeClaimLandingRequest{WorkID: "work-w", SessionRef: "ses-1", LandedDirectory: claimedB.Entry.Path, Now: now})
	if err != nil || second.AlreadyRecorded {
		t.Fatalf("second landing=%+v err=%v, want a fresh transfer at the same derived path", second, err)
	}
	if !slices.Equal(second.ReleasedSources, []string{claimedA.Entry.Path}) {
		t.Fatalf("released sources=%v, want %v", second.ReleasedSources, []string{claimedA.Entry.Path})
	}
	byProject := worktreeEntriesByProject(t, s, "work-w")
	if byProject["project-w"].OccupantSessionRef != "" {
		t.Fatalf("source entry=%+v, want the second landing to clear the sibling occupancy", byProject["project-w"])
	}
	if byProject["project-b"].OccupantSessionRef != "ses-1" {
		t.Fatalf("destination entry=%+v, want ses-1 to hold the re-claimed worktree", byProject["project-b"])
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM domain_events WHERE kind='work.session_claim_landed' AND subject_id='work-w'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("landing event count=%d, want one event per transfer", count)
	}

	replay, err := s.RecordWorktreeClaimLanding(ctx, WorktreeClaimLandingRequest{WorkID: "work-w", SessionRef: "ses-1", LandedDirectory: claimedB.Entry.Path, Now: now})
	if err != nil || !replay.AlreadyRecorded {
		t.Fatalf("replay landing=%+v err=%v, want an idempotent replay", replay, err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM domain_events WHERE kind='work.session_claim_landed'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("landing event count after replay=%d, want still two", count)
	}
}

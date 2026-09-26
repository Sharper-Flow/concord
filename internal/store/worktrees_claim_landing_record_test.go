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
// session — or record no occupant, the shape a resumed session lands in — the
// other active rows of the same work item clear, one durable event names the
// session, work item, source paths, and landed path, and the same landing
// replays idempotently. A second session's landing in the same worktree adds
// its own occupancy row and never refuses (CD-0178 D3).
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
	landing, err := s.RecordWorktreeClaimLanding(ctx, WorktreeClaimLandingRequest{WorkID: "work-w", SessionRef: "ses-1", LandedDirectory: claimedB.Entry.Path, Now: now, HostPID: 1})
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
	if worktreeOccupancyByEntry(t, s, WorktreeSetID("work-w"), "project-b", byProject["project-b"].ClaimOpID) != "ses-1" {
		t.Fatalf("destination entry=%+v, want ses-1 to hold the claimed worktree", byProject["project-b"])
	}
	if worktreeOccupancyByEntry(t, s, WorktreeSetID("work-w"), "project-w", byProject["project-w"].ClaimOpID) != "" {
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

	replay, err := s.RecordWorktreeClaimLanding(ctx, WorktreeClaimLandingRequest{WorkID: "work-w", SessionRef: "ses-1", LandedDirectory: claimedB.Entry.Path, Now: now, HostPID: 1})
	if err != nil || !replay.AlreadyRecorded {
		t.Fatalf("replay landing=%+v err=%v, want an idempotent replay", replay, err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM domain_events WHERE kind='work.session_claim_landed'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("landing event count after replay=%d, want still one", count)
	}

	// A landing back at the vacated source path is the same verified
	// transfer in the other direction (CD-0178 D3): the landing adds the
	// session's row there and releases its row at the path it left.
	backTransfer, err := s.RecordWorktreeClaimLanding(ctx, WorktreeClaimLandingRequest{WorkID: "work-w", SessionRef: "ses-1", LandedDirectory: claimedA.Entry.Path, Now: now, HostPID: 1})
	if err != nil || backTransfer.AlreadyRecorded {
		t.Fatalf("transfer back=%+v err=%v, want a fresh transfer", backTransfer, err)
	}
	if !slices.Equal(backTransfer.ReleasedSources, []string{claimedB.Entry.Path}) {
		t.Fatalf("released sources=%v, want %v", backTransfer.ReleasedSources, []string{claimedB.Entry.Path})
	}
	if worktreeOccupancyByEntry(t, s, WorktreeSetID("work-w"), "project-w", claimedA.Entry.ClaimOpID) != "ses-1" {
		t.Fatal("the source entry must hold the session again after the transfer back")
	}
	if worktreeOccupancyByEntry(t, s, WorktreeSetID("work-w"), "project-b", claimedB.Entry.ClaimOpID) != "" {
		t.Fatal("the previous destination must be empty after the transfer back")
	}

	// A path with no active row of this work item refuses as absent.
	_, err = s.RecordWorktreeClaimLanding(ctx, WorktreeClaimLandingRequest{WorkID: "work-w", SessionRef: "ses-1", LandedDirectory: filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-w", "work-elsewhere"), Now: now, HostPID: 1})
	failure, ok := err.(*Failure)
	if !ok || failure.Kind != KindProjectionNotFound {
		t.Fatalf("absent destination err=%v, want %s", err, KindProjectionNotFound)
	}

	// Another session's row never refuses a landing (CD-0178 D3): ses-2's
	// landing into the worktree the inserted ses-9 row holds adds its own
	// row, and ses-9's row stays until its own release rule consumes it.
	if _, err := s.db.Exec(`
		INSERT INTO fold_guard(active) VALUES(1);
		INSERT INTO worktree_occupancy (worktree_id, session_ref, recorded_at, host_pid, host_pid_start, has_process_identity)
		SELECT set_id || ':' || project_id || ':' || claim_op_id, 'ses-9', '1970-01-01T00:00:00Z', NULL, NULL, 0
		  FROM worktree_entries WHERE set_id=? AND project_id='project-b' AND state='active';
		DELETE FROM fold_guard`, WorktreeSetID("work-w")); err != nil {
		t.Fatal(err)
	}
	secondLanding, err := s.RecordWorktreeClaimLanding(ctx, WorktreeClaimLandingRequest{WorkID: "work-w", SessionRef: "ses-2", LandedDirectory: claimedB.Entry.Path, Now: now, HostPID: 1})
	if err != nil || secondLanding.AlreadyRecorded {
		t.Fatalf("landing beside another session's row=%+v err=%v, want a fresh record", secondLanding, err)
	}
	var rows int
	if err := s.db.QueryRow(`SELECT count(*) FROM worktree_occupancy WHERE session_ref IN ('ses-2','ses-9') AND worktree_id IN (SELECT set_id || ':' || project_id || ':' || claim_op_id FROM worktree_entries WHERE set_id=? AND project_id='project-b')`, WorktreeSetID("work-w")).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("occupancy rows after the second session's landing=%d, want ses-2 and ses-9 both recorded", rows)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM domain_events WHERE kind='work.session_claim_landed'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("landing event count=%d, want one per transfer and the add-a-row landing", count)
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
	landing, err := s.RecordWorktreeClaimLanding(ctx, WorktreeClaimLandingRequest{WorkID: "work-w", SessionRef: "ses-1", LandedDirectory: claimedB.Entry.Path, Now: now, HostPID: 1})
	if err != nil || landing.AlreadyRecorded {
		t.Fatalf("first landing=%+v err=%v, want a fresh transfer", landing, err)
	}

	reclaim := func(projectID, requestID string, expectedVersion int64) {
		t.Helper()
		_, err := s.ReclaimWorktree(ctx, WorktreeReclaimRequest{
			WorkID: "work-w", ProjectID: projectID, DefaultRef: "origin/main", PrincipalRef: "principal-1", RequestID: requestID, ExpectedVersion: expectedVersion,
			Now: now, Runner: git,
			ReleaseOccupancy: true, OperatorApprovalRef: "approval:claim-landing-test",
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

	second, err := s.RecordWorktreeClaimLanding(ctx, WorktreeClaimLandingRequest{WorkID: "work-w", SessionRef: "ses-1", LandedDirectory: claimedB.Entry.Path, Now: now, HostPID: 1})
	if err != nil || second.AlreadyRecorded {
		t.Fatalf("second landing=%+v err=%v, want a fresh transfer at the same derived path", second, err)
	}
	if !slices.Equal(second.ReleasedSources, []string{claimedA.Entry.Path}) {
		t.Fatalf("released sources=%v, want %v", second.ReleasedSources, []string{claimedA.Entry.Path})
	}
	byProject := worktreeEntriesByProject(t, s, "work-w")
	if worktreeOccupancyByEntry(t, s, WorktreeSetID("work-w"), "project-w", byProject["project-w"].ClaimOpID) != "" {
		t.Fatalf("source entry=%+v, want the second landing to clear the sibling occupancy", byProject["project-w"])
	}
	if worktreeOccupancyByEntry(t, s, WorktreeSetID("work-w"), "project-b", byProject["project-b"].ClaimOpID) != "ses-1" {
		t.Fatalf("destination entry=%+v, want ses-1 to hold the re-claimed worktree", byProject["project-b"])
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM domain_events WHERE kind='work.session_claim_landed' AND subject_id='work-w'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("landing event count=%d, want one event per transfer", count)
	}

	replay, err := s.RecordWorktreeClaimLanding(ctx, WorktreeClaimLandingRequest{WorkID: "work-w", SessionRef: "ses-1", LandedDirectory: claimedB.Entry.Path, Now: now, HostPID: 1})
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

// A session that resumes an existing work item claims no worktree: the read
// that derives its active worktree records nothing (CD-0104 D1), so its
// landing is the one shape that reaches an unoccupied destination row. Once
// the host move has read back, the record admits that shape, names the
// session as the occupant, and replays idempotently. Another session's
// landing into the recorded worktree adds its own row (CD-0178 D3).
func TestClaimLandingRecordsResumedSessionInUnoccupiedWorktree(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	ctx := context.Background()
	path := auditWork(t, s, git, "work-resumed", true)

	now := time.Unix(30, 0).UTC()
	landing, err := s.RecordWorktreeClaimLanding(ctx, WorktreeClaimLandingRequest{WorkID: "work-resumed", SessionRef: "ses-resumed", LandedDirectory: path, Now: now, HostPID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if landing.AlreadyRecorded || landing.ProjectID != "project-w" || len(landing.ReleasedSources) != 0 {
		t.Fatalf("landing=%+v, want the resumed session freshly recorded with no released sources", landing)
	}
	byProject := worktreeEntriesByProject(t, s, "work-resumed")
	if worktreeOccupancyByEntry(t, s, WorktreeSetID("work-resumed"), "project-w", byProject["project-w"].ClaimOpID) != "ses-resumed" {
		t.Fatalf("destination entry=%+v, want ses-resumed recorded as the occupant", byProject["project-w"])
	}

	replay, err := s.RecordWorktreeClaimLanding(ctx, WorktreeClaimLandingRequest{WorkID: "work-resumed", SessionRef: "ses-resumed", LandedDirectory: path, Now: now, HostPID: 1})
	if err != nil || !replay.AlreadyRecorded {
		t.Fatalf("replay landing=%+v err=%v, want an idempotent replay", replay, err)
	}

	// Another session's landing into the recorded row adds its own row:
	// CD-0104 D5 admits two sessions in one worktree, and CD-0178 D3
	// refuses no landing because another session holds a row.
	second, err := s.RecordWorktreeClaimLanding(ctx, WorktreeClaimLandingRequest{WorkID: "work-resumed", SessionRef: "ses-other", LandedDirectory: path, Now: now, HostPID: 1})
	if err != nil || second.AlreadyRecorded {
		t.Fatalf("another session's landing=%+v err=%v, want its own row recorded", second, err)
	}
	if len(second.ReleasedSources) != 0 {
		t.Fatalf("released sources=%v, want no sources for a session that holds no other worktree", second.ReleasedSources)
	}
	byProject = worktreeEntriesByProject(t, s, "work-resumed")
	var occupants int
	if err := s.db.QueryRow(`SELECT count(*) FROM worktree_occupancy WHERE worktree_id=?`, worktreeOccupancyID(WorktreeSetID("work-resumed"), "project-w", byProject["project-w"].ClaimOpID)).Scan(&occupants); err != nil {
		t.Fatal(err)
	}
	if occupants != 2 {
		t.Fatalf("occupancy rows=%d, want the resumed session and the second session both recorded", occupants)
	}
}

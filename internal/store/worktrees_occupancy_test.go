package store

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/hostlease"
)

// Release reads only the durable worktree_occupancy rows and their host
// process identity (CD-0178 D3). The kernel proves whether a process is alive.

// worktreeOccupancyByEntry is a test helper that returns the recorded session_ref
// of a worktree's occupancy row, or empty when no row exists.
func worktreeOccupancyByEntry(t *testing.T, s *Store, setID, projectID, claimOpID string) string {
	t.Helper()
	var sessionRef string
	err := s.db.QueryRow(`SELECT session_ref FROM worktree_occupancy WHERE worktree_id=? ORDER BY recorded_at LIMIT 1`, worktreeOccupancyID(setID, projectID, claimOpID)).Scan(&sessionRef)
	if err != nil && err.Error() != "sql: no rows in result set" {
		t.Fatalf("query worktree_occupancy: %v", err)
	}
	return sessionRef
}

// insertWorktreeOccupantWithIdentity is a test helper that inserts one
// occupancy row with host process identity on the work item's active
// worktree. pidStart is recorded verbatim, so a caller passes
// hostlease.ProcessStart(pid) for a live row and a wrong value for a row
// whose host process the kernel no longer proves at that start.
func insertWorktreeOccupantWithIdentity(t *testing.T, s *Store, workID, sessionRef string, pid int, pidStart uint64) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`
		INSERT INTO fold_guard(active) VALUES(1);
		INSERT INTO worktree_occupancy (worktree_id, session_ref, recorded_at, host_pid, host_pid_start, has_process_identity)
		SELECT set_id || ':' || project_id || ':' || claim_op_id, ?, '1970-01-01T00:00:00Z', ?, ?, 1
		  FROM worktree_entries WHERE set_id=? AND state='active';
		DELETE FROM fold_guard`, sessionRef, pid, pidStart, WorktreeSetID(workID)); err != nil {
		t.Fatal(err)
	}
}

// A row with host process identity releases only when the kernel no longer
// proves its recorded process at its recorded start time (CD-0178 D3). A row
// the kernel proves live blocks the reclaim and survives it; a row whose
// recorded start no longer matches the kernel releases with a recorded event
// and no operator approval.
func TestReclaimWorktreeReleasesOccupancyOnlyByProcessEnd(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	pid := os.Getpid()
	pidStart, err := hostlease.ProcessStart(pid)
	if err != nil {
		t.Fatalf("cannot read the test process start: %v", err)
	}

	claim := baseClaim(git)
	claim.SessionRef = "ses-claim"
	claimed, err := s.ClaimWorktree(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	seedWorktreeLifecycle(t, s, "work-w", "completed", 3)
	// The claim's own legacy row is joined by one row the kernel proves
	// live and one row whose recorded start no longer matches the kernel.
	insertWorktreeOccupantWithIdentity(t, s, "work-w", "ses-live", pid, pidStart)
	insertWorktreeOccupantWithIdentity(t, s, "work-w", "ses-ended", pid, pidStart-1)

	_, err = s.ReclaimWorktree(context.Background(), WorktreeReclaimRequest{
		WorkID: "work-w", ProjectID: "project-w", DefaultRef: "origin/main",
		PrincipalRef: "principal-1", RequestID: "req-live-and-ended", ExpectedVersion: 4,
		Now: time.Unix(20, 0).UTC(), Runner: git,
	})
	failure, ok := err.(*Failure)
	if !ok || failure.Kind != KindWorktreeOwnershipConflict {
		t.Fatalf("live occupancy err=%v, want %s", err, KindWorktreeOwnershipConflict)
	}
	if !strings.Contains(failure.Detail, "ses-live") || strings.Contains(failure.Detail, "ses-ended") {
		t.Fatalf("refusal %q must name the live row and not the released one", failure.Detail)
	}
	if _, still := git.worktrees[claimed.Entry.Path]; !still {
		t.Fatal("a refused reclaim must leave the native worktree in place")
	}
	if worktreeOccupancyByEntry(t, s, claimed.Entry.SetID, claimed.Entry.ProjectID, claimed.Entry.ClaimOpID) == "" {
		t.Fatal("a refused reclaim must leave every occupancy row in place")
	}

	// Vacate the live sessions, then the reclaim releases the ended row by
	// process liveness alone and takes the worktree.
	vacate := func(eventID, sessionRef string) {
		t.Helper()
		payload := jsonRaw(`{"work_id":"work-w","project_id":"project-w","session_ref":"` + sessionRef + `","source_directory":"` + claimed.Entry.Path + `"}`)
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{{
			EventID: eventID, Kind: "work.session_vacated", SubjectType: SubjectWorkItem, SubjectID: "work-w",
			Actor: sessionRef, OccurredAt: time.Unix(22, 0).UTC(), PayloadVersion: 1, Payload: payload,
		}}}); err != nil {
			t.Fatal(err)
		}
	}
	vacate("vacate-live", "ses-live")
	vacate("vacate-claim", "ses-claim")
	entry, err := s.ReclaimWorktree(context.Background(), WorktreeReclaimRequest{
		WorkID: "work-w", ProjectID: "project-w", DefaultRef: "origin/main",
		PrincipalRef: "principal-1", RequestID: "req-after-process-end", ExpectedVersion: 4,
		Now: time.Unix(25, 0).UTC(), Runner: git,
	})
	if err != nil {
		t.Fatalf("reclaim after the live row left: %v", err)
	}
	if entry.State != worktreeEntryReclaimed {
		t.Fatalf("entry=%+v, want reclaimed", entry)
	}
	var released int
	if err := s.db.QueryRow(`SELECT count(*) FROM domain_events WHERE kind='work.worktree_occupancy_released' AND json_extract(payload,'$.session_ref')='ses-ended'`).Scan(&released); err != nil {
		t.Fatal(err)
	}
	if released != 1 {
		t.Fatalf("released events for ses-ended=%d, want the recorded process-end release", released)
	}
	if _, still := git.worktrees[claimed.Entry.Path]; still {
		t.Fatal("native worktree was not removed after the process-end release")
	}
}

// A legacy row has no process identity, so liveness can never release it
// (CD-0178 D3): the audit pass refuses the worktree typed, the row survives,
// and only session_vacate — or an operator-approved removal — releases it.
func TestAuditReclaimRejectsLegacyOccupancyWithoutApproval(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	ctx := context.Background()
	legacyPath := auditWork(t, s, git, "work-legacy", true)
	completeAuditWork(t, s, "work-legacy", 3)
	setWorktreeOccupant(t, s, "work-legacy", "ses-legacy")

	result, err := s.WorktreeAuditReclaim(ctx, WorktreeAuditReclaimRequest{ProductID: "product-w", DefaultRef: "origin/main", PrincipalRef: "principal-1", RequestID: "audit-reclaim-legacy", Now: time.Unix(40, 0).UTC(), Runner: git, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 || result.Rows[0].Outcome != WorktreeAuditRefused || result.Rows[0].RefusalKind != string(KindWorktreeOwnershipConflict) {
		t.Fatalf("legacy occupancy must be refused typed, got %+v", result.Rows)
	}
	if _, kept := git.worktrees[legacyPath]; !kept {
		t.Fatal("a legacy occupancy row must keep the worktree in place")
	}
	if worktreeOccupancyByEntry(t, s, WorktreeSetID("work-legacy"), "project-w", "wt-work-legacy") != "ses-legacy" {
		t.Fatal("the liveness pass must leave the legacy row in place")
	}

	// session_vacate is the one non-destructive release for a legacy row.
	vacatePayload := jsonRaw(`{"work_id":"work-legacy","project_id":"project-w","session_ref":"ses-legacy","source_directory":"` + legacyPath + `"}`)
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{{
		EventID: "vacate-legacy", Kind: "work.session_vacated", SubjectType: SubjectWorkItem, SubjectID: "work-legacy",
		Actor: "ses-legacy", OccurredAt: time.Unix(42, 0).UTC(), PayloadVersion: 1, Payload: vacatePayload,
	}}}); err != nil {
		t.Fatal(err)
	}
	if worktreeOccupancyByEntry(t, s, WorktreeSetID("work-legacy"), "project-w", "wt-work-legacy") != "" {
		t.Fatal("session_vacate must release the legacy row")
	}
	result, err = s.WorktreeAuditReclaim(ctx, WorktreeAuditReclaimRequest{ProductID: "product-w", DefaultRef: "origin/main", PrincipalRef: "principal-1", RequestID: "audit-reclaim-legacy-2", Now: time.Unix(45, 0).UTC(), Runner: git, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 || result.Rows[0].Outcome != WorktreeAuditReclaimed {
		t.Fatalf("after the legacy row released the reclaim must proceed, got %+v", result.Rows)
	}
	if _, kept := git.worktrees[legacyPath]; kept {
		t.Fatal("the vacated worktree was not removed")
	}
}

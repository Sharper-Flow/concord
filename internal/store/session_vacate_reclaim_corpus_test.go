package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The last unbound continuity scenario (CD-0090 corpus
// adapter-continuity.v1.json): a terminal work item's session runs in its
// linked worktree, session_vacate resolves the registered main checkout the
// core derives, the vacate-landing readback records the verified landing and
// releases the session's occupancy rows, and the reclaim then passes the
// occupancy gate. This is a synthetic core test through the production
// destination resolution, landing, and reclaim owners; it runs no host or
// consumer session operation. The corpus entry and its assertions are
// consumed exactly: a renamed, duplicated, or rewritten scenario unbinds
// this owner instead of silently passing.

const vacateReclaimScenarioID = "adapter-continuity-session-vacate-then-reclaim-passes-occupancy-gate"

var vacateReclaimImplementedAssertions = []string{
	"destination_is_core_derived",
	"landing_is_read_back",
	"reclaim_occupancy_gate_passes",
}

func vacateReclaimCorpusAssertions(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", ".concord", "scenarios", "adapter-continuity.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Scenarios []struct {
			ID   string `json:"id"`
			Then []struct {
				Assertion string `json:"assertion"`
			} `json:"then"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	var assertions []string
	for _, scenario := range corpus.Scenarios {
		if scenario.ID == vacateReclaimScenarioID {
			for _, entry := range scenario.Then {
				assertions = append(assertions, entry.Assertion)
			}
		}
	}
	slices.Sort(assertions)
	implemented := slices.Clone(vacateReclaimImplementedAssertions)
	slices.Sort(implemented)
	if !slices.Equal(assertions, implemented) {
		t.Fatalf("corpus scenario %s asserts %v, owner implements %v; the binding is stale", vacateReclaimScenarioID, assertions, implemented)
	}
	return assertions
}

func TestSessionVacateReclaimOccupancyGateCorpus(t *testing.T) {
	vacateReclaimCorpusAssertions(t)
	t.Run(vacateReclaimScenarioID, func(t *testing.T) {
		s, git, mainCheckout := worktreeFixture(t)
		ctx := context.Background()
		claim := baseClaim(git)
		claimed, err := s.ClaimWorktree(ctx, claim)
		if err != nil {
			t.Fatal(err)
		}
		completeAuditWork(t, s, "work-w")
		// The session stands in its linked worktree: one identity-less
		// occupancy row recorded after the protecting host lease, so the
		// lease-set proof keeps the row and the occupancy gate refuses
		// instead of releasing it (CD-0179).
		writeProtectingHostLease(t, s)
		setWorktreeOccupant(t, s, "work-w", "ses-x")
		if got := worktreeOccupancyByEntry(t, s, claimed.Entry.SetID, claimed.Entry.ProjectID, claimed.Entry.ClaimOpID); got != "ses-x" {
			t.Fatalf("occupancy row=%q, want ses-x before the vacate", got)
		}

		// The occupancy gate refuses the reclaim while the session stands in
		// the worktree, and the refusal names the stranded session.
		refused, err := s.ReclaimWorktree(ctx, WorktreeReclaimRequest{
			WorkID: "work-w", ProjectID: "project-w", DefaultRef: "origin/main",
			PrincipalRef: "principal-1", RequestID: "req-gate-refused", ExpectedVersion: 4,
			Now: time.Unix(20, 0).UTC(), Runner: git,
		})
		failure, ok := err.(*Failure)
		if !ok || failure.Kind != KindWorktreeOwnershipConflict {
			t.Fatalf("reclaim before the vacate err=%v, want %s", err, KindWorktreeOwnershipConflict)
		}
		if !strings.Contains(failure.Detail, "ses-x") {
			t.Fatalf("occupancy refusal %q must name the stranded session", failure.Detail)
		}
		if refused.State != "" {
			t.Fatalf("refused reclaim returned %+v", refused)
		}
		if _, still := git.worktrees[claimed.Entry.Path]; !still {
			t.Fatal("a refused reclaim must leave the native worktree in place")
		}

		// The vacate destination is the registered main checkout the core
		// resolves from the Project locator, never a caller-supplied path.
		var target SessionVacateTarget
		if err := s.Transact(ctx, func(transaction *Transaction) error {
			var resolveErr error
			target, resolveErr = ResolveSessionVacateTargetTx(ctx, transaction, "project-w", claimed.Entry.Path, "ses-x")
			return resolveErr
		}); err != nil {
			t.Fatal(err)
		}
		if target.WorkID != "work-w" || target.SourceDirectory != claimed.Entry.Path || target.DestinationDirectory != mainCheckout || target.OccupantSessionRef != "ses-x" {
			t.Fatalf("core-derived vacate target=%+v, want source %s and destination %s", target, claimed.Entry.Path, mainCheckout)
		}
		if err := s.Transact(ctx, func(transaction *Transaction) error {
			_, resolveErr := ResolveSessionVacateTargetTx(ctx, transaction, "project-w", filepath.Join(mainCheckout, "not-a-worktree"), "ses-x")
			return resolveErr
		}); failureKind(err) != KindProjectionNotFound {
			t.Fatalf("resolution off the linked worktree err=%v, want projection_not_found", err)
		}

		// The committed relocation request carries the core-derived
		// destination; a landing anywhere else refuses and releases nothing.
		request, err := json.Marshal(sessionVacatedPayload{WorkID: "work-w", ProjectID: "project-w", SessionRef: "ses-x", SourceDirectory: target.SourceDirectory, DestinationDirectory: target.DestinationDirectory})
		if err != nil {
			t.Fatal(err)
		}
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{{
			EventID: "vacate-reclaim-corpus", Kind: "work.session_vacated", SubjectType: SubjectWorkItem, SubjectID: "work-w",
			Actor: "ses-x", OccurredAt: time.Unix(21, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion("work.session_vacated"), Payload: request,
		}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.RecordSessionVacateLanding(ctx, SessionVacateLandingRequest{
			WorkID: "work-w", SessionRef: "ses-x", LandedDirectory: filepath.Join(mainCheckout, "elsewhere"), HostPID: os.Getpid(), Now: time.Unix(22, 0).UTC(),
		}); failureKind(err) != KindProjectionNotFound {
			t.Fatalf("landing off the committed destination err=%v, want projection_not_found", err)
		}
		if got := worktreeOccupancyByEntry(t, s, claimed.Entry.SetID, claimed.Entry.ProjectID, claimed.Entry.ClaimOpID); got != "ses-x" {
			t.Fatalf("a refused landing released the row: %q", got)
		}

		// The honest landing readback: the directory the host read back is
		// the registered main checkout, and the landing releases the
		// session's occupancy rows in the same transaction.
		landing, err := s.RecordSessionVacateLanding(ctx, SessionVacateLandingRequest{
			WorkID: "work-w", SessionRef: "ses-x", LandedDirectory: mainCheckout, HostPID: os.Getpid(), Now: time.Unix(23, 0).UTC(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if landing.AlreadyRecorded || landing.DestinationDirectory != mainCheckout || landing.LandedDirectory != mainCheckout || !slices.Contains(landing.ReleasedSources, claimed.Entry.Path) {
			t.Fatalf("landing result=%+v, want the verified landing releasing %s", landing, claimed.Entry.Path)
		}
		if got := worktreeOccupancyByEntry(t, s, claimed.Entry.SetID, claimed.Entry.ProjectID, claimed.Entry.ClaimOpID); got != "" {
			t.Fatalf("the verified landing left the occupancy row: %q", got)
		}
		var landings int
		if err := s.db.QueryRow(`SELECT COALESCE(vacate_landing_count,0) FROM session_relocation_counts WHERE work_id='work-w' AND session_ref='ses-x'`).Scan(&landings); err != nil || landings != 1 {
			t.Fatalf("recorded landings=%d err=%v, want exactly the one verified landing", landings, err)
		}

		// With the session landed, the reclaim passes the occupancy gate and
		// removes the vacated worktree.
		entry, err := s.ReclaimWorktree(ctx, WorktreeReclaimRequest{
			WorkID: "work-w", ProjectID: "project-w", DefaultRef: "origin/main",
			PrincipalRef: "principal-1", RequestID: "req-gate-passes", ExpectedVersion: 4,
			Now: time.Unix(25, 0).UTC(), Runner: git,
		})
		if err != nil {
			t.Fatalf("reclaim after the verified landing: %v", err)
		}
		if entry.State != worktreeEntryReclaimed {
			t.Fatalf("entry=%+v, want reclaimed", entry)
		}
		if _, kept := git.worktrees[claimed.Entry.Path]; kept {
			t.Fatal("the vacated worktree was not removed after the gate passed")
		}
	})
}

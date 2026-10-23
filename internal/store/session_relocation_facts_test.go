package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// hideDomainEvents makes the event log unreachable, so a reader that still
// consults it fails instead of answering.
func hideDomainEvents(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`ALTER TABLE domain_events RENAME TO domain_events_hidden`); err != nil {
		t.Fatal(err)
	}
}

func restoreDomainEvents(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`ALTER TABLE domain_events_hidden RENAME TO domain_events`); err != nil {
		t.Fatal(err)
	}
}

// inRelocationTx runs fn in one transaction that writes nothing.
func inRelocationTx(t *testing.T, s *Store, fn func(*Transaction, *sql.Tx) error) error {
	t.Helper()
	return s.Transact(context.Background(), func(transaction *Transaction) error {
		tx, err := transactionSQL(transaction, "relocation_test")
		if err != nil {
			return err
		}
		return fn(transaction, tx)
	})
}

// A pending relocation request is answered from current state: the request
// count, the pending source request, the replay target, and the landing
// state all hold with the event log unreachable. A second request on the
// same work item reopens the request its landing completed.
func TestSessionRelocationRequestReadersIgnoreTheLog(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	req.SessionRef = "ses-x"
	if _, err := s.ClaimWorktree(ctx, req); err != nil {
		t.Fatal(err)
	}
	source := claimPath(s)
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{vacateRequestEvent("relocation-request-1", source)}}); err != nil {
		t.Fatal(err)
	}

	hideDomainEvents(t, s)
	if err := inRelocationTx(t, s, func(transaction *Transaction, tx *sql.Tx) error {
		count, err := CountWorkSessionVacatesTx(ctx, transaction, "work-w", "ses-x")
		if err != nil || count != 1 {
			t.Fatalf("request count=%d err=%v, want 1", count, err)
		}
		pending, err := PendingSessionVacateSourceRequestTx(ctx, transaction, "project-w", source, "ses-x")
		if err != nil || pending.WorkID != "work-w" || pending.DestinationDirectory != "/data/repo-main" {
			t.Fatalf("pending request=%+v err=%v", pending, err)
		}
		target, err := ResolveSessionVacateReplayTargetTx(ctx, transaction, "project-w", "/data/repo-main", "ses-x")
		if err != nil || target.WorkID != "work-w" || target.SourceDirectory != source {
			t.Fatalf("replay target=%+v err=%v", target, err)
		}
		landed, blocker, err := sessionVacateLandingStateTx(ctx, tx, "work-w", "ses-x")
		if err != nil || landed || !strings.Contains(blocker, "waits for its verified landing") {
			t.Fatalf("landing state landed=%v blocker=%q err=%v, want pending", landed, blocker, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	restoreDomainEvents(t, s)

	if _, err := s.RecordSessionVacateLanding(ctx, SessionVacateLandingRequest{
		WorkID: "work-w", SessionRef: "ses-x", LandedDirectory: "/data/repo-main", HostPID: os.Getpid(), Now: time.Unix(60, 0).UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	hideDomainEvents(t, s)
	if err := inRelocationTx(t, s, func(transaction *Transaction, tx *sql.Tx) error {
		if _, err := PendingSessionVacateSourceRequestTx(ctx, transaction, "project-w", source, "ses-x"); failureKind(err) != KindProjectionNotFound {
			t.Fatalf("err=%v, want projection_not_found once the landing completed the request", err)
		}
		if _, err := ResolveSessionVacateReplayTargetTx(ctx, transaction, "project-w", "/data/repo-main", "ses-x"); err != nil {
			t.Fatalf("completed replay err=%v, want the committed target", err)
		}
		landings, err := countSessionVacateLandingsTx(ctx, tx, "work-w", "ses-x")
		if err != nil || landings != 1 {
			t.Fatalf("landing count=%d err=%v, want 1", landings, err)
		}
		landed, blocker, err := sessionVacateLandingStateTx(ctx, tx, "work-w", "ses-x")
		if err != nil || !landed {
			t.Fatalf("landing state landed=%v blocker=%q err=%v, want landed", landed, blocker, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	restoreDomainEvents(t, s)

	if err := ApplyOperation(ctx, s, Operation{Events: []Event{vacateRequestEvent("relocation-request-2", source)}}); err != nil {
		t.Fatal(err)
	}

	hideDomainEvents(t, s)
	if err := inRelocationTx(t, s, func(transaction *Transaction, tx *sql.Tx) error {
		count, err := CountWorkSessionVacatesTx(ctx, transaction, "work-w", "ses-x")
		if err != nil || count != 2 {
			t.Fatalf("request count=%d err=%v, want 2", count, err)
		}
		if _, err := PendingSessionVacateSourceRequestTx(ctx, transaction, "project-w", source, "ses-x"); err != nil {
			t.Fatalf("second request err=%v, want it pending: the earlier landing does not complete it", err)
		}
		landed, _, err := sessionVacateLandingStateTx(ctx, tx, "work-w", "ses-x")
		if err != nil || landed {
			t.Fatalf("landing state landed=%v err=%v, want the second request pending", landed, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A verified placement and the claim-landing ordinals come from current
// state: after a vacate and a re-claim, the old claim generation's landing
// still counts toward the work item and session, the re-claim holds its own
// landing, and the placement names the re-claimed Project, all with the
// event log unreachable.
func TestSessionClaimLandingReadersIgnoreTheLog(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	ctx := context.Background()
	bindSessionToProjectWorktree(t, f.store, f.targetTree, f.targetSession)
	var firstClaim string
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT claim_op_id FROM worktree_entries WHERE path=? AND state='active'`, filepath.Clean(f.targetTree)).Scan(&firstClaim); err != nil {
		t.Fatal(err)
	}
	destination := projectMainCheckout(t, f.targetTree)
	request, err := json.Marshal(sessionVacatedPayload{WorkID: f.workID, ProjectID: f.targetProject, SessionRef: f.targetSession, SourceDirectory: filepath.Clean(f.targetTree), DestinationDirectory: filepath.Clean(destination)})
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, f.store, Operation{Events: []Event{{
		EventID: "relocation-vacate-target", Kind: "work.session_vacated", SubjectType: SubjectWorkItem, SubjectID: f.workID, Actor: f.targetSession, OccurredAt: time.Unix(50, 0).UTC(), PayloadVersion: 2, Payload: request,
	}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.RecordSessionVacateLanding(ctx, SessionVacateLandingRequest{WorkID: f.workID, SessionRef: f.targetSession, LandedDirectory: filepath.Clean(destination), HostPID: os.Getpid(), Now: time.Unix(51, 0).UTC()}); err != nil {
		t.Fatal(err)
	}
	baseOut, err := (ExecGitRunner{}).Run(ctx, destination, "rev-parse", "origin/main")
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, f.store, f.workID)
	if _, err := f.store.ReclaimWorktree(ctx, WorktreeReclaimRequest{
		WorkID: f.workID, ProjectID: f.targetProject, DefaultRef: "origin/main", PrincipalRef: "principal-2",
		RequestID: "relocation-reclaim", ExpectedVersion: version, Now: time.Unix(52, 0).UTC(), Runner: ExecGitRunner{},
	}); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := f.store.ClaimWorktree(ctx, WorktreeClaimRequest{
		OpID: "relocation-op-2", WorkID: f.workID, ProjectID: f.targetProject,
		BaseSHA: strings.TrimSpace(string(baseOut)), PrincipalRef: "principal-2", RequestID: "relocation-req-2",
		SessionRef: f.targetSession, ExpectedVersion: version + 1, Now: time.Unix(52, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Before the re-claim lands, its occupancy row proves no placement.
	hideDomainEvents(t, f.store)
	if err := inRelocationTx(t, f.store, func(_ *Transaction, tx *sql.Tx) error {
		placement, err := verifiedSessionPlacementTx(ctx, tx, f.workID, f.targetSession)
		if err != nil || placement != "" {
			t.Fatalf("placement=%q err=%v, want none before the re-claim lands", placement, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	restoreDomainEvents(t, f.store)

	if _, err := f.store.RecordWorktreeClaimLanding(ctx, WorktreeClaimLandingRequest{WorkID: f.workID, SessionRef: f.targetSession, LandedDirectory: reclaimed.Entry.Path, HostPID: os.Getpid(), Now: time.Unix(53, 0).UTC()}); err != nil {
		t.Fatal(err)
	}

	hideDomainEvents(t, f.store)
	if err := inRelocationTx(t, f.store, func(_ *Transaction, tx *sql.Tx) error {
		placement, err := verifiedSessionPlacementTx(ctx, tx, f.workID, f.targetSession)
		if err != nil || placement != f.targetProject {
			t.Fatalf("placement=%q err=%v, want %s", placement, err, f.targetProject)
		}
		total, err := countSessionClaimLandingsTx(ctx, tx, f.workID, f.targetSession)
		if err != nil || total != 2 {
			t.Fatalf("claim landings=%d err=%v, want 2", total, err)
		}
		for _, claim := range []string{firstClaim, reclaimed.Entry.ClaimOpID} {
			count, err := countSessionClaimLandingsForClaimTx(ctx, tx, f.workID, f.targetSession, claim)
			if err != nil || count != 1 {
				t.Fatalf("claim %s landings=%d err=%v, want 1", claim, count, err)
			}
		}
		landed, blocker, err := sessionVacateLandingStateTx(ctx, tx, f.workID, f.targetSession)
		if err != nil || landed || !strings.Contains(blocker, "occupancy") {
			t.Fatalf("landing state landed=%v blocker=%q err=%v, want refused by the re-claim's rows", landed, blocker, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

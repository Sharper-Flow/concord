package store

import (
	"context"
	"strings"
	"testing"
)

// The authorization fold binds the attempt before any worker report arrives.
// This fixture keeps that production state rather than synthesizing dispatch
// evidence for a worker that has not finished.
func seedRemovalInFlightAttempt(t *testing.T) (*Store, WorkRemovalRequest, string) {
	t.Helper()
	s := openTemp(t)
	const workID = "removal-in-flight-work"
	const attemptID = "attempt-removal-in-flight"
	seed := seedDispatchFixture(t, s, workID)
	claimed := dispatchSessionWorktree(t, s, workID)
	request := cd781DispatchRequest(t, s, workID, readWorkVersion(t, s, workID), attemptID, seed.ownerActor, claimed, "removal-in-flight")
	if _, err := invokeWorkflowActionForCD0059(context.Background(), t, s, request); err != nil {
		t.Fatal(err)
	}
	assertWorkerAttemptState(t, s, workID, attemptID, "in_flight")
	req := removalTestRequest()
	req.WorkID = workID
	req.ExpectedVersion = readWorkVersion(t, s, workID)
	return s, req, attemptID
}

func TestWorkRemovalRefusesInFlightAttempt(t *testing.T) {
	t.Parallel()
	s, req, attemptID := seedRemovalInFlightAttempt(t)
	ctx := context.Background()
	// Isolate the worker gate from the fixture's separate worktree gate.
	productRowExec(t, s, `DELETE FROM worktree_entries WHERE set_id=?`, WorktreeSetID(req.WorkID))
	for _, operation := range []struct {
		name string
		run  func(context.Context, WorkRemovalRequest) (WorkRemovalReceipt, error)
	}{
		{"prepare", s.PrepareWorkRemoval},
		{"shelve", func(ctx context.Context, req WorkRemovalRequest) (WorkRemovalReceipt, error) {
			req.Reason = "shelved"
			return s.RemoveWork(ctx, req)
		}},
		{"cancel", func(ctx context.Context, req WorkRemovalRequest) (WorkRemovalReceipt, error) {
			req.Reason = "cancelled"
			return s.RemoveWork(ctx, req)
		}},
		{"retry", func(ctx context.Context, req WorkRemovalRequest) (WorkRemovalReceipt, error) {
			req.Reason = "shelved"
			return s.RemoveWork(ctx, req)
		}},
	} {
		t.Run(operation.name, func(t *testing.T) {
			_, err := operation.run(ctx, req)
			if !hasFailureKind(err, KindResourceClaimHeld) || !strings.Contains(err.Error(), "live or unknown worker attempt") {
				t.Fatalf("removal did not refuse the in-flight attempt: %v", err)
			}
		})
	}
	assertWorkerAttemptState(t, s, req.WorkID, attemptID, "in_flight")
	if version := readWorkVersion(t, s, req.WorkID); version != req.ExpectedVersion {
		t.Fatalf("refused removal changed work version: %d", version)
	}
	var receipts int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM work_removal_operations WHERE work_id=?`, req.WorkID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if receipts != 0 {
		t.Fatalf("refused removal recorded %d receipts", receipts)
	}
}

func TestWorkRemovalAllowsCompletedAttempt(t *testing.T) {
	t.Parallel()
	s, req, attemptID := seedRemovalInFlightAttempt(t)
	digest := cd781DispatchPacketDigest(t, s, req.WorkID, attemptID)
	if err := seedWorkerEvidenceForAttempt(t, s, req.WorkID, attemptID, "removal-completed", digest); err != nil {
		t.Fatal(err)
	}
	assertWorkerAttemptState(t, s, req.WorkID, attemptID, "completed")
	liveness, err := s.ReadWorkLiveness(context.Background(), req.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	if liveness.Attempts != 1 || strings.Join(liveness.Evidence, " ") != "host liveness has no verified active or waiting evidence" {
		t.Fatalf("completed attempt is still reported as open: %+v", liveness)
	}
	productRowExec(t, s, `DELETE FROM worktree_entries WHERE set_id=?`, WorktreeSetID(req.WorkID))
	req.ExpectedVersion = readWorkVersion(t, s, req.WorkID)
	req.Reason = "shelved"
	receipt, err := s.RemoveWork(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State != "committed" {
		t.Fatalf("completed worker prevented removal: %+v", receipt)
	}
}

func TestWorkLivenessIncludesInFlightAttempt(t *testing.T) {
	t.Parallel()
	s, req, _ := seedRemovalInFlightAttempt(t)
	liveness, err := s.ReadWorkLiveness(context.Background(), req.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	if liveness.State != "unknown" || liveness.Attempts != 1 || strings.Join(liveness.Evidence, " ") != "1 open worker attempt(s) have unknown host state" {
		t.Fatalf("liveness does not account for the in-flight attempt: %+v", liveness)
	}
}

func TestProductRowsLivenessIncludesInFlightAttempt(t *testing.T) {
	t.Parallel()
	s, req, _ := seedRemovalInFlightAttempt(t)
	result, err := s.QueryProductRows(context.Background(), ProductRowRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 || result.Rows[0].Focus == nil || result.Rows[0].Focus.WorkID != req.WorkID {
		t.Fatalf("unexpected Product-row focus: %+v", result.Rows)
	}
	liveness := result.Rows[0].Focus.Liveness
	if liveness == nil || liveness.State != "unknown" || liveness.Attempts != 1 || strings.Join(liveness.Evidence, " ") != "1 open worker attempt(s) have unknown host state" {
		t.Fatalf("Product-row liveness does not account for the in-flight attempt: %+v", liveness)
	}
}

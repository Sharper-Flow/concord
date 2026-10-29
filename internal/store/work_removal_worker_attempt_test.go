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
			if !hasFailureKind(err, KindNotTerminal) || !strings.Contains(err.Error(), "live or unknown worker attempt") {
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
	liveness, err := deriveWorkLivenessQ(context.Background(), s.db, req.WorkID)
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
	liveness, err := deriveWorkLivenessQ(context.Background(), s.db, req.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	if liveness.State != "unknown" || liveness.Attempts != 1 || strings.Join(liveness.Evidence, " ") != "1 open worker attempt(s) have unknown host state" {
		t.Fatalf("liveness does not account for the in-flight attempt: %+v", liveness)
	}
}

func TestSnapshotLivenessIncludesInFlightAttempt(t *testing.T) {
	t.Parallel()
	s, req, _ := seedRemovalInFlightAttempt(t)
	result, err := s.QueryQ2(context.Background(), Q2Request{Product: "product"})
	if err != nil {
		t.Fatal(err)
	}
	var item *WorkItem
	for i := range result.Items {
		if result.Items[i].ID == req.WorkID {
			item = &result.Items[i]
		}
	}
	if item == nil {
		t.Fatalf("snapshot lost the in-flight work: %+v", result.Items)
	}
	liveness := item.Liveness
	if liveness == nil || liveness.State != "unknown" || liveness.Attempts != 1 || strings.Join(liveness.Evidence, " ") != "1 open worker attempt(s) have unknown host state" {
		t.Fatalf("snapshot liveness does not account for the in-flight attempt: %+v", liveness)
	}
}

// productRowExec runs one raw statement under the write guard, so a direct
// table write cannot desync the workflow projection between statements.
func productRowExec(t *testing.T, s *Store, statement string, args ...any) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT INTO write_guard(active) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := s.DatabaseForTesting().ExecContext(ctx, `DELETE FROM write_guard`); err != nil {
			t.Errorf("remove write guard: %v", err)
		}
	}()
	if _, err := s.DatabaseForTesting().ExecContext(ctx, statement, args...); err != nil {
		t.Fatal(err)
	}
}

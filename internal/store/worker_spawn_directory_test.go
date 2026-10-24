package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkerCompletionRequiresObservedDirectory(t *testing.T) {
	for _, route := range []string{"generic", "authenticated"} {
		for _, explicitEmpty := range []bool{false, true} {
			name := "omitted"
			if explicitEmpty {
				name = "empty"
			}
			t.Run(route+"/"+name, func(t *testing.T) {
				s := openTemp(t)
				workID, attemptID := "work-directory-required", "attempt-directory-required"
				insertWorkerWorktreeEntry(t, s, workID, t.TempDir())
				if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent(workID, attemptID, BuiltinLaneDefinitions()[0], nil)}}); err != nil {
					t.Fatal(err)
				}
				event := workerCompletionWithDirectory(workID, "completion-directory-required", attemptID, "")
				if explicitEmpty {
					var payload map[string]any
					if err := json.Unmarshal(event.Payload, &payload); err != nil {
						t.Fatal(err)
					}
					payload["worker_directory"] = ""
					event.Payload = mustJSONValue(payload)
				}
				before := countRows(t, s, "domain_events")
				var err error
				if route == "generic" {
					err = ApplyOperation(context.Background(), s, Operation{Events: []Event{event}})
				} else {
					err = s.Transact(context.Background(), func(tx *Transaction) error {
						_, err := ApplyWorkerEvidenceOperationTx(context.Background(), tx, event)
						return err
					})
				}
				var failure *Failure
				if !errors.As(err, &failure) || failure.Kind != KindUnauthorizedDispatch {
					t.Fatalf("completion failure = %v, want unauthorized_dispatch", err)
				}
				if got := countRows(t, s, "domain_events"); got != before {
					t.Fatalf("missing-directory completion added %d events", got-before)
				}
				var lifecycle string
				if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state FROM worker_attempts WHERE attempt_id=?`, attemptID).Scan(&lifecycle); err != nil {
					t.Fatal(err)
				}
				if lifecycle != "dispatched" {
					t.Fatalf("attempt lifecycle = %q, want dispatched", lifecycle)
				}
			})
		}
	}
}

func TestWorkerCompletionDirectoryMustMatchActiveClaim(t *testing.T) {
	t.Run("mismatch refuses without a terminal event", func(t *testing.T) {
		s := openTemp(t)
		root := t.TempDir()
		claimed := filepath.Join(root, "claimed")
		worker := filepath.Join(root, "worker")
		for _, path := range []string{claimed, worker} {
			if err := os.Mkdir(path, 0o755); err != nil {
				t.Fatalf("create directory %s: %v", path, err)
			}
		}
		workID := "work-worker-directory-mismatch"
		attemptID := "attempt-worker-directory-mismatch"
		insertWorkerWorktreeEntry(t, s, workID, claimed)
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent(workID, attemptID, BuiltinLaneDefinitions()[0], nil)}}); err != nil {
			t.Fatal(err)
		}

		eventCount := countRows(t, s, "domain_events")
		err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerCompletionWithDirectory(workID, "completion-worker-directory-mismatch", attemptID, worker)}})
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindUnauthorizedDispatch {
			t.Fatalf("completion failure = %v, want unauthorized_dispatch", err)
		}
		claimedCanonical, err := canonicalWorkerWorktreePath(claimed)
		if err != nil {
			t.Fatal(err)
		}
		workerCanonical, err := canonicalWorkerWorktreePath(worker)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(failure.Detail, workerWorktreeIdentity(claimedCanonical)) || !strings.Contains(failure.Detail, workerWorktreeIdentity(workerCanonical)) {
			t.Fatalf("completion failure does not name both directory identities: %q", failure.Detail)
		}
		if got := countRows(t, s, "domain_events"); got != eventCount {
			t.Fatalf("rejected completion added %d events, want no event", got-eventCount)
		}
		var lifecycle string
		if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state FROM worker_attempts WHERE attempt_id=?`, attemptID).Scan(&lifecycle); err != nil {
			t.Fatal(err)
		}
		if lifecycle != "dispatched" {
			t.Fatalf("attempt lifecycle = %q, want dispatched", lifecycle)
		}
	})

	t.Run("matching symlink completes", func(t *testing.T) {
		s := openTemp(t)
		root := t.TempDir()
		claimed := filepath.Join(root, "claimed")
		alias := filepath.Join(root, "alias")
		if err := os.Mkdir(claimed, 0o755); err != nil {
			t.Fatalf("create claimed directory: %v", err)
		}
		if err := os.Symlink(claimed, alias); err != nil {
			t.Fatalf("create directory alias: %v", err)
		}
		workID := "work-worker-directory-match"
		attemptID := "attempt-worker-directory-match"
		insertWorkerWorktreeEntry(t, s, workID, claimed)
		authorizeWorkerFixtureDispatch(t, s, workID, attemptID)
		lane := BuiltinLaneDefinitions()[0]
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent(workID, attemptID, lane, nil)}}); err != nil {
			t.Fatal(err)
		}
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerCompletionWithDirectory(workID, "completion-worker-directory-match", attemptID, alias)}}); err != nil {
			t.Fatalf("matching completion refused: %v", err)
		}
		var lifecycle string
		if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state FROM worker_attempts WHERE attempt_id=?`, attemptID).Scan(&lifecycle); err != nil {
			t.Fatal(err)
		}
		if lifecycle != "completed" {
			t.Fatalf("attempt lifecycle = %q, want completed", lifecycle)
		}
	})
}

func workerCompletionWithDirectory(workID, eventID, attemptID, directory string) Event {
	payload, err := json.Marshal(WorkerCompletedPayload{
		AttemptID: attemptID, ReadbackModel: preferredModelForLane(BuiltinLaneDefinitions()[0]),
		ReportSchemaVersion: WorkerReportSchemaVersion, WorkerDirectory: directory,
		EvidenceOrigin: WorkerEvidenceReported, Evidence: laneCoveringEvidence(BuiltinLaneDefinitions()[0]),
	})
	if err != nil {
		panic(err)
	}
	return Event{EventID: eventID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion(WorkerCompleted), Payload: payload}
}

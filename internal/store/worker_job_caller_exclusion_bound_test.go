package store

// Boundary and regression tests for the shared packet-bounds guard on the
// dispatch-time caller exclusions (worker_packet5_worker_job): at most
// WorkerJobCallerExclusionsMax paths of at most
// WorkerJobCallerExclusionPathMax Unicode characters each. The computation
// owner refuses the dispatch with the accept-or-split recovery; the
// continuity ready projection omits the whole set and keeps the read
// available. The observed-delivery route is the only writer that can carry a
// path past the character bound: recorded basis paths are refused above 512
// bytes at recording, and Unicode characters never exceed bytes.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// The guard holds the packet's declared bounds exactly: an empty set passes,
// the count bound holds at its edge with the exact count diagnostic and the
// accept-or-split recovery, and the per-path bound is measured in Unicode
// characters, never bytes.
func TestValidateWorkerJobPacketExclusionsBounds(t *testing.T) {
	const jobID = "job:exclusion-bounds"
	const recovery = "accept or split an open correction job through accept_worker_result or record_worker_job, then dispatch again"
	t.Run("empty set passes", func(t *testing.T) {
		if err := validateWorkerJobPacketExclusions(jobID, nil); err != nil {
			t.Fatalf("empty exclusions refused: %v", err)
		}
		if err := validateWorkerJobPacketExclusions(jobID, []string{}); err != nil {
			t.Fatalf("empty exclusions refused: %v", err)
		}
	})
	t.Run("count bound edge", func(t *testing.T) {
		paths := make([]string, WorkerJobCallerExclusionsMax)
		for i := range paths {
			paths[i] = fmt.Sprintf("internal/store/path%d.go", i)
		}
		if err := validateWorkerJobPacketExclusions(jobID, paths); err != nil {
			t.Fatalf("%d paths refused: %v", len(paths), err)
		}
		paths = append(paths, "internal/store/one-too-many.go")
		err := validateWorkerJobPacketExclusions(jobID, paths)
		var failure *Failure
		if !errors.As(err, &failure) {
			t.Fatalf("%d paths error = %v, want a Failure", len(paths), err)
		}
		want := fmt.Sprintf("worker job %s's caller exclusions would carry %d paths; the packet declares at most %d", jobID, WorkerJobCallerExclusionsMax+1, WorkerJobCallerExclusionsMax)
		if failure.Detail != want {
			t.Fatalf("count diagnostic = %q, want %q", failure.Detail, want)
		}
		if failure.RecoveryAction != recovery {
			t.Fatalf("count recovery = %q, want %q", failure.RecoveryAction, recovery)
		}
	})
	t.Run("character bound edge counts Unicode characters", func(t *testing.T) {
		// 512 two-byte characters: 1024 bytes, exactly at the declared
		// character maximum.
		atBound := strings.Repeat("é", WorkerJobCallerExclusionPathMax)
		if utf8.RuneCountInString(atBound) != WorkerJobCallerExclusionPathMax || len(atBound) <= WorkerJobCallerExclusionPathMax {
			t.Fatalf("fixture must be a multi-byte string at the character bound")
		}
		if err := validateWorkerJobPacketExclusions(jobID, []string{atBound}); err != nil {
			t.Fatalf("%d-character (%d-byte) path refused: %v", utf8.RuneCountInString(atBound), len(atBound), err)
		}
		over := strings.Repeat("é", WorkerJobCallerExclusionPathMax+1)
		err := validateWorkerJobPacketExclusions(jobID, []string{"internal/store/short.go", over})
		var failure *Failure
		if !errors.As(err, &failure) {
			t.Fatalf("over-length path error = %v, want a Failure", err)
		}
		want := fmt.Sprintf("worker job %s's caller exclusions carry a path above the packet's %d-character bound", jobID, WorkerJobCallerExclusionPathMax)
		if failure.Detail != want {
			t.Fatalf("length diagnostic = %q, want %q", failure.Detail, want)
		}
		if failure.RecoveryAction != recovery {
			t.Fatalf("length recovery = %q, want %q", failure.RecoveryAction, recovery)
		}
	})
}

// callerExclusionCharacterFixture records one correction job whose sealed
// observation carries the given measured path and one plain ready job whose
// dispatch would carry the correction's exclusions.
func callerExclusionCharacterFixture(t *testing.T, workID, observedPath string) *Store {
	t.Helper()
	fixture := sizeDispatchFixture(t, workID)
	t.Cleanup(func() { fixture.store.Close() })
	s := workerJobSizeStore(fixture)
	route := workflowReturnRouteFixture{store: fixture.store, owner: fixture.owner}
	correction := &WorkerJobBinding{JobID: "job:over-length-correction", Revision: 1}
	recordEditingJobForTest(t, route, workID, workerJobSizeEditingFields(t, workID, route), correction)
	recordSizeDispatchAuthorization(t, s, workID, "attempt:over-length", *correction)
	if err := appendJobBoundWorkerDispatch(t, s, workID, "attempt:over-length", correction); err != nil {
		t.Fatalf("record the implementation dispatch: %v", err)
	}
	lane := reviewGateLane(t, "implementation")
	base := testJobSizeBaseCommit()
	completed := reportBindingCompletion(t, s, workID, "attempt:over-length", lane, correction, func(payload *WorkerCompletedPayload) {
		payload.ObservedDelivery = &WorkerObservedDelivery{Commit: strings.Repeat("7", 40), State: "clean", Base: base, Paths: []WorkerObservedPath{{Path: observedPath, Added: 1}}}
		payload.ProductionSize = &WorkerProductionSize{Base: base, Tree: strings.Repeat("7", 40), Count: 1}
	})
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{completed}}); err != nil {
		t.Fatalf("seal the correction's observed paths: %v", err)
	}
	other := &WorkerJobBinding{JobID: "job:plain-ready", Revision: 1}
	otherFields := workerJobSizeEditingFields(t, workID, route)
	otherFields["path_scope"] = []string{"adapter/opencode"}
	otherFields["measurement_basis"].(map[string]any)["paths"] = []map[string]any{{"path": "adapter/opencode/packet.ts", "class": "production"}}
	recordEditingJobForTest(t, route, workID, otherFields, other)
	return s
}

// The exclusions computation owner refuses a set carrying a path above the
// packet's character bound with the accept-or-split recovery, exactly like
// the count bound, instead of emitting a packet the closed schema rejects.
func TestWorkerJobCallerExclusionsRefuseAboveTheCharacterBound(t *testing.T) {
	const workID = "worker-job-size-exclusion-character-bound"
	over := strings.Repeat("é", 513)
	if utf8.RuneCountInString(over) != 513 || len(over) <= 513 {
		t.Fatalf("fixture must be a multi-byte path above the character bound")
	}
	s := callerExclusionCharacterFixture(t, workID, over)
	_, err := workerJobCallerExclusionsTx(context.Background(), s.db, workID, "job:plain-ready")
	var failure *Failure
	if !errors.As(err, &failure) || !strings.Contains(failure.Detail, "carry a path above the packet's 512-character bound") || !strings.Contains(failure.RecoveryAction, "accept or split an open correction job through accept_worker_result or record_worker_job, then dispatch again") {
		t.Fatalf("over-length exclusions error = %v, want the character-bound refusal", err)
	}
}

// The continuity ready projection omits a job's whole exclusions set when it
// leaves the packet's declared bounds and keeps the read available; the
// dispatch refuses with its recovery.
func TestWorkerJobContinuityOmitsExclusionsAboveTheCharacterBound(t *testing.T) {
	const workID = "worker-job-size-exclusion-character-continuity"
	over := strings.Repeat("é", 513)
	s := callerExclusionCharacterFixture(t, workID, over)
	if got := continuityReadyJobForTest(t, s, workID, "job:plain-ready").CallerExclusions; got != nil {
		t.Fatalf("continuity published caller_exclusions %q whose set leaves the packet bound, want none", got)
	}
}

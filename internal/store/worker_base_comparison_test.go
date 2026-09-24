package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// baseComparisonFixture is one well-formed comparison between branch and base
// results, exactly as an adapter would forward it after admitting
// agent-lane-report.v1.
func baseComparisonFixture() *WorkerBaseComparison {
	return &WorkerBaseComparison{Checks: []WorkerBaseComparisonCheck{
		{Command: "go test ./internal/store/...", BranchResult: "pass", BaseResult: "pass"},
		{Command: "go vet ./...", BranchResult: "pass", BaseResult: "fail"},
	}}
}

// baseComparisonCompleteEvent builds a reported completion whose evidence
// covers the lane and whose optional base_comparison is the given value.
func baseComparisonCompleteEvent(workID, eventID, attemptID string, lane LaneDefinition, comparison *WorkerBaseComparison) Event {
	evidence := laneCoveringEvidence(lane)
	payload := WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion, Evidence: evidence, EvidenceOrigin: WorkerEvidenceReported, BaseComparison: comparison}
	return Event{EventID: eventID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 2, Payload: mustJSONValue(payload)}
}

// A completion may carry the optional base_comparison object; it folds like
// any reported completion and the reported comparison stays durable in the
// worker-completed payload, byte-preserved as reported.
func TestWorkerCompletionCarriesBaseComparisonDurably(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	lane := BuiltinLaneDefinitions()[1]
	attemptID := "base-comparison-attempt"
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent("base-comparison", attemptID, lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	complete := baseComparisonCompleteEvent("base-comparison", "base-comparison-complete", attemptID, lane, baseComparisonFixture())
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{complete}}); err != nil {
		t.Fatalf("completion carrying base_comparison was refused: %v", err)
	}
	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state FROM worker_attempts WHERE attempt_id=?`, attemptID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "completed" {
		t.Fatalf("lifecycle_state = %q, want completed", state)
	}
	var stored []byte
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE event_id=?`, "base-comparison-complete").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var durable WorkerCompletedPayload
	if err := json.Unmarshal(stored, &durable); err != nil {
		t.Fatal(err)
	}
	if durable.BaseComparison == nil || len(durable.BaseComparison.Checks) != 2 {
		t.Fatalf("durable base_comparison = %+v, want 2 recorded checks", durable.BaseComparison)
	}
	if durable.BaseComparison.Checks[1].Command != "go vet ./..." || durable.BaseComparison.Checks[1].BranchResult != "pass" || durable.BaseComparison.Checks[1].BaseResult != "fail" {
		t.Fatalf("durable check = %+v, want the reported pair recorded as reported", durable.BaseComparison.Checks[1])
	}
}

// The store admits every size the report schema admits: an empty checks array
// records that the worker compared no checks, and 64 checks with 512-byte
// commands is the largest comparison the schema allows.
func TestWorkerCompletionAdmitsBaseComparisonSchemaBounds(t *testing.T) {
	t.Parallel()
	lane := BuiltinLaneDefinitions()[1]
	largest := &WorkerBaseComparison{Checks: make([]WorkerBaseComparisonCheck, 64)}
	for index := range largest.Checks {
		largest.Checks[index] = WorkerBaseComparisonCheck{Command: strings.Repeat("x", 512), BranchResult: "fail", BaseResult: "not_run"}
	}
	tests := []struct {
		name       string
		comparison *WorkerBaseComparison
	}{
		{name: "empty checks array", comparison: &WorkerBaseComparison{Checks: []WorkerBaseComparisonCheck{}}},
		{name: "64 checks with 512-byte commands", comparison: largest},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			s := openTemp(t)
			attemptID := "base-comparison-bound-attempt"
			if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent("base-comparison-bound", attemptID, lane, nil)}}); err != nil {
				t.Fatal(err)
			}
			if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{baseComparisonCompleteEvent("base-comparison-bound", "base-comparison-bound-complete", attemptID, lane, testCase.comparison)}}); err != nil {
				t.Fatalf("completion at the schema bound was refused: %v", err)
			}
			var stored []byte
			if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE event_id=?`, "base-comparison-bound-complete").Scan(&stored); err != nil {
				t.Fatal(err)
			}
			var durable WorkerCompletedPayload
			if err := json.Unmarshal(stored, &durable); err != nil {
				t.Fatal(err)
			}
			if durable.BaseComparison == nil || durable.BaseComparison.Checks == nil || len(durable.BaseComparison.Checks) != len(testCase.comparison.Checks) {
				t.Fatalf("durable base_comparison = %+v, want %d recorded checks", durable.BaseComparison, len(testCase.comparison.Checks))
			}
		})
	}
}

// The optional object is closed in both directions at the store boundary too:
// an oversized check list, an out-of-range command, a result outside the
// closed set, and a missing checks array are refused, and a refusal leaves
// the dispatched attempt untouched.
func TestWorkerCompletionBaseComparisonShapeIsClosed(t *testing.T) {
	t.Parallel()
	lane := BuiltinLaneDefinitions()[1]
	tooMany := &WorkerBaseComparison{Checks: make([]WorkerBaseComparisonCheck, 65)}
	for index := range tooMany.Checks {
		tooMany.Checks[index] = WorkerBaseComparisonCheck{Command: "go test ./...", BranchResult: "pass", BaseResult: "pass"}
	}
	longCommand := strings.Repeat("x", 513)
	tests := []struct {
		name       string
		comparison *WorkerBaseComparison
		wantDetail string
	}{
		{name: "more than 64 checks", comparison: tooMany, wantDetail: "at most 64 checks"},
		{name: "checks array absent", comparison: &WorkerBaseComparison{}, wantDetail: "a checks array"},
		{name: "empty command", comparison: &WorkerBaseComparison{Checks: []WorkerBaseComparisonCheck{{Command: "", BranchResult: "pass", BaseResult: "pass"}}}, wantDetail: "between 1 and 512 bytes"},
		{name: "oversized command", comparison: &WorkerBaseComparison{Checks: []WorkerBaseComparisonCheck{{Command: longCommand, BranchResult: "pass", BaseResult: "pass"}}}, wantDetail: "between 1 and 512 bytes"},
		{name: "branch result outside the vocabulary", comparison: &WorkerBaseComparison{Checks: []WorkerBaseComparisonCheck{{Command: "go test ./...", BranchResult: "skipped", BaseResult: "pass"}}}, wantDetail: "pass, fail, or not_run"},
		{name: "base result outside the vocabulary", comparison: &WorkerBaseComparison{Checks: []WorkerBaseComparisonCheck{{Command: "go test ./...", BranchResult: "pass", BaseResult: ""}}}, wantDetail: "pass, fail, or not_run"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			s := openTemp(t)
			attemptID := "base-comparison-shape-attempt"
			if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent("base-comparison-shape", attemptID, lane, nil)}}); err != nil {
				t.Fatal(err)
			}
			err := ApplyOperation(context.Background(), s, Operation{Events: []Event{baseComparisonCompleteEvent("base-comparison-shape", "base-comparison-shape-complete", attemptID, lane, testCase.comparison)}})
			if !hasFailureKind(err, KindInvalidPayload) {
				t.Fatalf("shape error = %v, want %s", err, KindInvalidPayload)
			}
			if detail := failureDetail(t, err); !strings.Contains(detail, testCase.wantDetail) {
				t.Fatalf("failure detail = %q, want it to name %q", detail, testCase.wantDetail)
			}
			var state string
			if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state FROM worker_attempts WHERE attempt_id=?`, attemptID).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if state != "dispatched" {
				t.Fatalf("lifecycle_state = %q, want dispatched", state)
			}
		})
	}
}

// A completion without the optional object folds exactly as before, and a
// stored v1 completion upcasts to a payload that carries no comparison — no
// shape change reaches payloads that predate the field.
func TestWorkerCompletionWithoutBaseComparisonIsUnchanged(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	lane := BuiltinLaneDefinitions()[0]
	attemptID := "no-comparison-attempt"
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent("no-comparison", attemptID, lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	complete := workerCompleteEventV2("no-comparison", "no-comparison-complete", attemptID, preferredModelForLane(lane), WorkerEvidenceReported, laneCoveringEvidence(lane))
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{complete}}); err != nil {
		t.Fatalf("completion without base_comparison was refused: %v", err)
	}
	var stored []byte
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE event_id=?`, "no-comparison-complete").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), "base_comparison") {
		t.Fatalf("payload without a comparison carries the field anyway: %s", stored)
	}
	legacy := workerCompleteEvent("no-comparison-legacy", "no-comparison-legacy-complete", attemptID, preferredModelForLane(lane))
	upcast, err := upcastWorkerCompletedV1(legacy)
	if err != nil {
		t.Fatalf("upcastWorkerCompletedV1 error = %v", err)
	}
	var upcastPayload WorkerCompletedPayload
	if err := json.Unmarshal(upcast.Payload, &upcastPayload); err != nil {
		t.Fatal(err)
	}
	if upcastPayload.BaseComparison != nil {
		t.Fatalf("upcast payload carries base_comparison %+v, want none", upcastPayload.BaseComparison)
	}
}

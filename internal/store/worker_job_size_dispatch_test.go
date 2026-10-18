package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// sizeDispatchFixture seeds one work item on the family's current head with
// the qualified oracle subject the shared recording helpers need.
func sizeDispatchFixture(t *testing.T, workID string) workflowReturnRouteFixture {
	t.Helper()
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	bootstrapOracleFixtureSubject(t, workerJobSizeStore(fixture), workID)
	return fixture
}

// sizeDispatchLane resolves one builtin lane by capability class.
func sizeDispatchLane(t *testing.T, capabilityClass string) LaneDefinition {
	t.Helper()
	return reviewGateLane(t, capabilityClass)
}

// recordSizeDispatchAttempt records one implementation-lane dispatch of the
// named job — its dispatch_worker authorization, the worker.dispatched
// evidence through the authoritative lane-actor route, and the rejection
// that disposes the attempt — so the job sits in correction: an
// implementation attempt rejected since its last satisfaction.
func recordSizeDispatchAttempt(t *testing.T, fixture workflowReturnRouteFixture, workID, attemptID string, job WorkerJobBinding) {
	t.Helper()
	s := workerJobSizeStore(fixture)
	recordSizeDispatchAuthorization(t, s, workID, attemptID, job)
	if err := appendJobBoundWorkerDispatch(t, s, workID, attemptID, &job); err != nil {
		t.Fatalf("record implementation dispatch %s: %v", attemptID, err)
	}
	version := readWorkVersion(t, s, workID)
	rejection := map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"action_id": "reject_worker_result", "step_id": "repair", "attempt_id": attemptID, "worker_attempt_id": attemptID,
		"diagnosis": "the delivered subject does not satisfy the approved predicate",
		"strategy":  "retry the bounded job from the recorded base",
	}
	raw, err := json.Marshal(rejection)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterFold(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?, 'workflow.action_completed', 'work_item', ?, ?, ?, 2, ?)`,
		workID+":size-rejection:"+attemptID, workID, "principal/operator", time.Unix(22, 0).UTC().Format(time.RFC3339Nano), string(raw)); err != nil {
		t.Fatalf("record the rejection of %s: %v", attemptID, err)
	}
	if err := leaveFold(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// recordSizeDispatchAuthorization records the dispatch_worker completion the
// worker.dispatched evidence must match, under the recorded-history route
// the review-subject fixture uses for folded accept completions.
func recordSizeDispatchAuthorization(t *testing.T, s *Store, workID, attemptID string, job WorkerJobBinding) {
	t.Helper()
	version := readWorkVersion(t, s, workID)
	authorization := map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"action_id": "dispatch_worker", "step_id": "repair", "attempt_epoch": 1, "worker_attempt_id": attemptID,
		"worker_job": map[string]any{"job_id": job.JobID, "revision": job.Revision, "digest": job.Digest},
	}
	raw, err := json.Marshal(authorization)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterFold(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?, 'workflow.action_started', 'work_item', ?, ?, ?, 2, ?), (?, 'workflow.action_completed', 'work_item', ?, ?, ?, 2, ?)`,
		workID+":size-started:"+attemptID, workID, "principal/operator", time.Unix(20, 0).UTC().Format(time.RFC3339Nano),
		mustJSONValue(map[string]any{"work_id": workID, "action_id": "dispatch_worker", "step_id": "repair", "attempt_epoch": 1}),
		workID+":size-authorization:"+attemptID, workID, "principal/operator", time.Unix(20, 0).UTC().Format(time.RFC3339Nano), string(raw)); err != nil {
		t.Fatalf("record the dispatch authorization of %s: %v", attemptID, err)
	}
	if err := leaveFold(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// recordEditingJobForTest records one ready editing job from the caller's
// fields and fills the binding from the recorded revision.
func recordEditingJobForTest(t *testing.T, fixture workflowReturnRouteFixture, workID string, fields map[string]any, job *WorkerJobBinding) {
	t.Helper()
	s := workerJobSizeStore(fixture)
	fields["job_id"] = job.JobID
	if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, fields); err != nil {
		t.Fatalf("record worker-job %s: %v", job.JobID, err)
	}
	views, err := readWorkerJobRevisions(context.Background(), s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range views {
		if view.Binding.JobID == job.JobID {
			*job = view.Binding
		}
	}
}

// sizeDispatchPacketJob builds the packet worker_job content a coordinator
// sends: the recorded revision's projected content with the caller's
// caller_exclusions overlay applied.
func sizeDispatchPacketJob(t *testing.T, s *Store, workID string, job WorkerJobBinding, exclusions []string) map[string]any {
	t.Helper()
	content := recordedPacketJobForTest(t, s, workID, job)
	if exclusions != nil {
		content["caller_exclusions"] = exclusions
	}
	return content
}

// The recorded size members project onto every admitted lane's packet, and
// the dispatch admission holds the claimed worker_job to the exact projected
// content: no exclusions on a non-implementation lane, and no drift.
func TestWorkerJobSizePacketProjectsSizeContent(t *testing.T) {
	const workID = "worker-job-size-packet-projection"
	fixture := sizeDispatchFixture(t, workID)
	s := workerJobSizeStore(fixture)
	definition := mustBuiltinDefinition(t, "workflow.break_fix").Definition
	job := &WorkerJobBinding{JobID: "job:projected", Revision: 1}
	recordEditingJobForTest(t, fixture, workID, workerJobSizeEditingFields(t, workID, fixture), job)
	views, err := readWorkerJobRevisions(context.Background(), s.db, workID)
	if err != nil || len(views) != 1 {
		t.Fatalf("recorded views = %#v, error = %v", views, err)
	}
	view := views[0]
	if view.ProductionSizeMax == nil || view.MeasurementBasis == nil || view.DirectCallerAllowance == nil {
		t.Fatalf("the recorded view carries no size members: %#v", view)
	}
	projected := packetJobFromView(view)
	if projected.ProductionSizeMax == nil || projected.MeasurementBasis == nil || projected.DirectCallerAllowance == nil || projected.CallerExclusions != nil {
		t.Fatalf("the packet projection carries the wrong size content: %#v", projected)
	}
	t.Run("implementation lane admits the exact projection", func(t *testing.T) {
		lane := sizeDispatchLane(t, "implementation")
		packet := joinPacketFor(t, s, workID, "repair", "attempt-projected", lane.ID, lane.Version, lane.Digest)
		packet["inputs"].(map[string]any)["worker_job"] = sizeDispatchPacketJob(t, s, workID, *job, nil)
		if _, err := validateWorkerPacketJob(context.Background(), s.db, definition, workID, lane, mustJSONValue(packet)); err != nil {
			t.Fatalf("implementation packet refused: %v", err)
		}
	})
	t.Run("review lane admits the same content without exclusions", func(t *testing.T) {
		lane := sizeDispatchLane(t, "review")
		packet := joinPacketFor(t, s, workID, "repair", "attempt-projected-review", lane.ID, lane.Version, lane.Digest)
		packet["inputs"].(map[string]any)["worker_job"] = sizeDispatchPacketJob(t, s, workID, *job, nil)
		if _, err := validateWorkerPacketJob(context.Background(), s.db, definition, workID, lane, mustJSONValue(packet)); err != nil {
			t.Fatalf("review packet refused: %v", err)
		}
	})
	t.Run("review lane refuses caller_exclusions", func(t *testing.T) {
		lane := sizeDispatchLane(t, "review")
		packet := joinPacketFor(t, s, workID, "repair", "attempt-projected-review-excl", lane.ID, lane.Version, lane.Digest)
		packet["inputs"].(map[string]any)["worker_job"] = sizeDispatchPacketJob(t, s, workID, *job, []string{"internal/store/worker_jobs.go"})
		_, err := validateWorkerPacketJob(context.Background(), s.db, definition, workID, lane, mustJSONValue(packet))
		if err == nil || !strings.Contains(err.Error(), "caller_exclusions") {
			t.Fatalf("review exclusions error = %v, want the implementation-only refusal", err)
		}
	})
	t.Run("drifted size content refuses", func(t *testing.T) {
		lane := sizeDispatchLane(t, "implementation")
		packet := joinPacketFor(t, s, workID, "repair", "attempt-projected-drift", lane.ID, lane.Version, lane.Digest)
		content := sizeDispatchPacketJob(t, s, workID, *job, nil)
		content["production_size_max"] = 999999
		packet["inputs"].(map[string]any)["worker_job"] = content
		_, err := validateWorkerPacketJob(context.Background(), s.db, definition, workID, lane, mustJSONValue(packet))
		if err == nil || !strings.Contains(err.Error(), "content does not match the recorded revision") {
			t.Fatalf("drifted content error = %v, want the equality refusal", err)
		}
	})
	t.Run("read-only job projects no members", func(t *testing.T) {
		readOnly := &WorkerJobBinding{JobID: "job:projected-read-only", Revision: 1}
		recordEditingJobForTest(t, fixture, workID, workerJobSizeReadOnlyFields(t, workID, fixture), readOnly)
		lane := sizeDispatchLane(t, "implementation")
		packet := joinPacketFor(t, s, workID, "repair", "attempt-projected-readonly", lane.ID, lane.Version, lane.Digest)
		packet["inputs"].(map[string]any)["worker_job"] = sizeDispatchPacketJob(t, s, workID, *readOnly, nil)
		binding, err := validateWorkerPacketJob(context.Background(), s.db, definition, workID, lane, mustJSONValue(packet))
		if err != nil || binding == nil {
			t.Fatalf("read-only packet refused: %v", err)
		}
	})
}

// The caller exclusions are the measured paths of the work's other jobs that
// are in correction, computed by the core and bound to the implementation
// packet exactly: a missing, extra, or different set refuses.
func TestWorkerJobSizeCallerExclusionsBindThePacket(t *testing.T) {
	const workID = "worker-job-size-caller-exclusions"
	fixture := sizeDispatchFixture(t, workID)
	s := workerJobSizeStore(fixture)
	definition := mustBuiltinDefinition(t, "workflow.break_fix").Definition
	blocked := &WorkerJobBinding{JobID: "job:blocked", Revision: 1}
	recordEditingJobForTest(t, fixture, workID, workerJobSizeEditingFields(t, workID, fixture), blocked)
	recordSizeDispatchAttempt(t, fixture, workID, "attempt:blocked", *blocked)
	other := &WorkerJobBinding{JobID: "job:other", Revision: 1}
	recordEditingJobForTest(t, fixture, workID, workerJobSizeEditingFields(t, workID, fixture), other)
	lane := sizeDispatchLane(t, "implementation")
	exclusions := []string{"internal/store/worker_jobs.go"}
	t.Run("implementation packet carries the computed exclusions", func(t *testing.T) {
		packet := joinPacketFor(t, s, workID, "repair", "attempt-exclusions", lane.ID, lane.Version, lane.Digest)
		packet["inputs"].(map[string]any)["worker_job"] = sizeDispatchPacketJob(t, s, workID, *other, exclusions)
		if _, err := validateWorkerPacketJob(context.Background(), s.db, definition, workID, lane, mustJSONValue(packet)); err != nil {
			t.Fatalf("exclusions-bearing packet refused: %v", err)
		}
	})
	for _, refusal := range []struct {
		name      string
		exclusion []string
	}{
		{"missing exclusions", nil},
		{"foreign exclusions", []string{"adapter/opencode/packet.ts"}},
		{"extra exclusions", []string{"internal/store/worker_jobs.go", "adapter/opencode/packet.ts"}},
	} {
		t.Run(refusal.name+" refuse", func(t *testing.T) {
			packet := joinPacketFor(t, s, workID, "repair", "attempt-exclusions-"+strings.ReplaceAll(refusal.name, " ", "-"), lane.ID, lane.Version, lane.Digest)
			packet["inputs"].(map[string]any)["worker_job"] = sizeDispatchPacketJob(t, s, workID, *other, refusal.exclusion)
			_, err := validateWorkerPacketJob(context.Background(), s.db, definition, workID, lane, mustJSONValue(packet))
			if err == nil || !strings.Contains(err.Error(), "content does not match the recorded revision") {
				t.Fatalf("exclusions %q error = %v, want the equality refusal", refusal.name, err)
			}
		})
	}
	t.Run("review lane carries no exclusions", func(t *testing.T) {
		reviewLane := sizeDispatchLane(t, "review")
		packet := joinPacketFor(t, s, workID, "repair", "attempt-exclusions-review", reviewLane.ID, reviewLane.Version, reviewLane.Digest)
		packet["inputs"].(map[string]any)["worker_job"] = sizeDispatchPacketJob(t, s, workID, *other, nil)
		if _, err := validateWorkerPacketJob(context.Background(), s.db, definition, workID, reviewLane, mustJSONValue(packet)); err != nil {
			t.Fatalf("review packet without exclusions refused: %v", err)
		}
	})
}

// The retained v33 pin keeps its packet3 closure and admits an uncapped
// implementation job unchanged, while the size-capable heads anchor
// dispatch_worker at the packet4 closure.
func TestWorkerJobSizeHistoricalPacketUnchanged(t *testing.T) {
	t.Parallel()
	v34 := mustCompileWorkflowComposition(implementationCompositionV34())
	if got := dispatchPacketSchemaRef(t, v34); got != "worker_packet4" {
		t.Fatalf("implementation v34 dispatch packet schema ref = %q, want worker_packet4", got)
	}
	v31 := mustCompileWorkflowComposition(breakFixCompositionV31())
	if got := dispatchPacketSchemaRef(t, v31); got != "worker_packet4" {
		t.Fatalf("break_fix v31 dispatch packet schema ref = %q, want worker_packet4", got)
	}
	v33 := mustCompileWorkflowComposition(implementationCompositionV33())
	if got := dispatchPacketSchemaRef(t, v33); got != "worker_packet3" {
		t.Fatalf("implementation v33 dispatch packet schema ref = %q, want the retained worker_packet3", got)
	}
	const workID = "worker-job-size-historical-packet"
	fixture := seedHistoricalWorkflowReturnRouteFixture(t, workID, "workflow.implementation", 33, "execution")
	s := workerJobSizeStore(fixture)
	definition, lookupErr := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 33)
	if !lookupErr {
		t.Fatal("workflow.implementation v33 is not registered")
	}
	job := &WorkerJobBinding{JobID: "job:historical", Revision: 1}
	historicalFields := map[string]any{
		"job_id": job.JobID, "objective": "bounded historical job", "stopping_condition": "the recorded checks pass",
		"path_scope": []string{"internal/store"}, "checks": []string{"go test ./internal/store/"},
		"ready": true, "readiness_evidence": []string{"evidence:coordinator-ready"},
	}
	if oracleCapablePinForWork(t, s, workID) {
		bootstrapOracleFixtureSubject(t, s, workID)
		historicalFields["acceptance_oracle"] = acceptanceOracleFieldsForTest(t, s, workID)
	}
	if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, historicalFields); err != nil {
		t.Fatalf("record historical job: %v", err)
	}
	views, err := readWorkerJobRevisions(context.Background(), s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range views {
		if view.Binding.JobID == job.JobID {
			*job = view.Binding
		}
	}
	lane := sizeDispatchLane(t, "implementation")
	packet := joinPacketFor(t, s, workID, "execution", "attempt-historical", lane.ID, lane.Version, lane.Digest)
	packet["inputs"].(map[string]any)["worker_job"] = sizeDispatchPacketJob(t, s, workID, *job, nil)
	if _, err := validateWorkerPacketJob(context.Background(), s.db, definition.Definition, workID, lane, mustJSONValue(packet)); err != nil {
		t.Fatalf("historical v33 implementation dispatch refused: %v", err)
	}
}

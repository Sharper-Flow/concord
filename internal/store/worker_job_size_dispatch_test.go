package store

import (
	"context"
	"strings"
	"testing"
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

package store

import (
	"context"
	"strings"
	"testing"
)

func TestWorkerJobVerificationDispatchRequiresChecks(t *testing.T) {
	for _, tc := range []struct {
		name   string
		checks []string
		want   string
	}{
		{name: "recorded_commands", checks: []string{"go test ./internal/store/ -run TestWorkerJob"}},
		{name: "empty_checks", checks: []string{}, want: "verification worker job requires nonempty checks"},
		{name: "omitted_checks", want: "verification worker job requires nonempty checks"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workID := "worker-job-verify-" + tc.name
			fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
			s := fixture.store
			defer s.Close()
			fields := map[string]any{
				"job_id": "job:verify", "objective": "Run the recorded verification commands",
				"stopping_condition":   "Each command exits zero",
				"reserved_integration": "The parent records whole-work integration separately",
				"ready":                true, "readiness_evidence": []string{"evidence:verification-ready"},
			}
			if tc.checks != nil {
				fields["checks"] = tc.checks
			}
			if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, fields); err != nil {
				t.Fatal(err)
			}
			ready, err := s.ReadyWorkerJobRevisions(context.Background(), workID)
			if err != nil || len(ready) != 1 {
				t.Fatalf("ready jobs = %#v, error = %v, want one recorded revision", ready, err)
			}
			lane := reviewGateLane(t, "verification")
			attemptID := "attempt:" + workID
			packet := joinPacketFor(t, s, workID, "repair", attemptID, lane.ID, lane.Version, lane.Digest)
			packet["inputs"].(map[string]any)["worker_job"] = packetJobFromView(ready[0])
			version := readWorkVersion(t, s, workID)
			err = dispatchJobPacketForTest(t, s, workID, attemptID, fixture.owner, "dispatch-"+workID, packet)
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("verification dispatch error = %v, want %q", err, tc.want)
				}
				if got := readWorkVersion(t, s, workID); got != version || dispatchStartedCount(t, s, workID) != 0 {
					t.Fatalf("refused dispatch changed state: version=%d, want %d", got, version)
				}
				return
			}
			if err != nil {
				t.Fatalf("verification dispatch with recorded checks refused: %v", err)
			}
			found, binding, err := dispatchCompletionJobForAttempt(context.Background(), s.DatabaseForTesting(), workID, attemptID)
			if err != nil || !found || binding == nil || *binding != ready[0].Binding {
				t.Fatalf("dispatch binding = %#v, found=%v, error=%v, want %#v", binding, found, err, ready[0].Binding)
			}
			if got := packet["inputs"].(map[string]any)["task"]; got != "deliver the checked change" {
				t.Fatalf("parent task = %v, want the separate approved premise", got)
			}
		})
	}
}

func TestWorkerJobCapabilityClassUsesClosedRegistry(t *testing.T) {
	for _, class := range []string{"implementation", "design", "research", "review", "verification"} {
		t.Run(class, func(t *testing.T) {
			_ = reviewGateLane(t, class)
			if !workerJobCapabilityClass(class) {
				t.Fatalf("registered capability class %q cannot execute a worker job", class)
			}
		})
	}
	for _, class := range []string{"", "verify", "implement", "Verification", "unregistered"} {
		if workerJobCapabilityClass(class) {
			t.Errorf("unregistered capability class %q can execute a worker job", class)
		}
	}
}

func TestWorkerJobAllRegisteredClassesBindReadyRevision(t *testing.T) {
	for _, lane := range BuiltinLaneDefinitions() {
		if lane.CapabilityClass == "research" {
			continue // Research dispatches before the delivery-bearing job phase.
		}
		t.Run(lane.CapabilityClass, func(t *testing.T) {
			workID := "worker-job-class-" + lane.ID
			stepID := "repair"
			fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", stepID)
			s := fixture.store
			defer s.Close()
			job := &WorkerJobBinding{JobID: "job:" + lane.ID, Revision: 1}
			recordWorkerJobRevisionForTest(t, s, workID, fixture.owner, job)
			definition := mustBuiltinDefinition(t, "workflow.break_fix").Definition
			attemptID := "attempt:" + workID
			packet := joinPacketFor(t, s, workID, stepID, attemptID, lane.ID, lane.Version, lane.Digest)
			if err := dispatchJobPacketForTest(t, s, workID, attemptID, fixture.owner, "unbound-"+workID, packet); err == nil || !strings.Contains(err.Error(), "carries no inputs.worker_job") {
				t.Fatalf("unbound %s dispatch error = %v, want missing-job refusal", lane.ID, err)
			}
			recorded := recordedPacketJobForTest(t, s, workID, *job)
			packet["inputs"].(map[string]any)["worker_job"] = recorded
			for _, mutation := range []struct {
				name, field string
				value       any
				want        string
			}{
				{"fabricated", "job_id", "job:unrecorded", "does not name a recorded worker-job revision"},
				{"digest", "digest", "sha256:" + strings.Repeat("e", 64), "digest does not match the recorded revision"},
				{"content", "objective", "An unrecorded objective", "content does not match the recorded revision"},
			} {
				t.Run(mutation.name, func(t *testing.T) {
					original := recorded[mutation.field]
					recorded[mutation.field] = mutation.value
					defer func() { recorded[mutation.field] = original }()
					_, err := validateWorkerPacketJob(context.Background(), s.DatabaseForTesting(), definition, workID, lane, mustJSONValue(packet))
					if err == nil || !strings.Contains(err.Error(), mutation.want) {
						t.Fatalf("%s job error = %v, want %q", lane.ID, err, mutation.want)
					}
				})
			}
			if err := dispatchJobPacketForTest(t, s, workID, attemptID, fixture.owner, "bound-"+workID, packet); err != nil {
				t.Fatalf("bound %s dispatch: %v", lane.ID, err)
			}
			found, bound, err := dispatchCompletionJobForAttempt(context.Background(), s.DatabaseForTesting(), workID, attemptID)
			if err != nil || !found || !sameWorkerJob(bound, job) {
				t.Fatalf("%s dispatch bound %#v, found=%v, error=%v, want %#v", lane.ID, bound, found, err, job)
			}
			var laneID, capabilityClass string
			if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT json_extract(payload,'$.worker_lane_id'),json_extract(payload,'$.worker_capability_class') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.worker_attempt_id')=?`, workID, WorkflowActionCompleted, attemptID).Scan(&laneID, &capabilityClass); err != nil {
				t.Fatal(err)
			}
			if laneID != lane.ID || capabilityClass != lane.CapabilityClass {
				t.Fatalf("dispatch lane = %s/%s, want %s/%s beside the exact attempt/job binding", laneID, capabilityClass, lane.ID, lane.CapabilityClass)
			}
			if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, map[string]any{
				"job_id": job.JobID, "objective": "Await readiness evidence",
				"stopping_condition": "The checks pass", "checks": []string{"go test ./internal/store/"}, "ready": false,
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := validateWorkerPacketJob(context.Background(), s.DatabaseForTesting(), definition, workID, lane, mustJSONValue(packet)); err == nil || !strings.Contains(err.Error(), "is not ready") {
				t.Fatalf("superseded %s job error = %v, want readiness refusal", lane.ID, err)
			}
			views, err := s.WorkerJobRevisions(context.Background(), workID)
			if err != nil || len(views) != 2 {
				t.Fatalf("revisions = %#v, error=%v, want two immutable revisions", views, err)
			}
			packet["inputs"].(map[string]any)["worker_job"] = packetJobFromView(views[1])
			if _, err := validateWorkerPacketJob(context.Background(), s.DatabaseForTesting(), definition, workID, lane, mustJSONValue(packet)); err == nil || !strings.Contains(err.Error(), "is not ready") {
				t.Fatalf("unready %s job error = %v, want readiness refusal", lane.ID, err)
			}
		})
	}
}

func TestWorkerJobLegacyPinsNeverRequireOrSynthesizeJobs(t *testing.T) {
	for _, pin := range []struct {
		ref     string
		version int64
	}{
		{"workflow.implementation", 23},
		{"workflow.break_fix", 20},
	} {
		t.Run(pin.ref, func(t *testing.T) {
			registered, ok := BuiltinWorkflowRegistry().Lookup(pin.ref, pin.version)
			if !ok {
				t.Fatalf("historical pin %s@%d is missing", pin.ref, pin.version)
			}
			for _, lane := range BuiltinLaneDefinitions() {
				// Legacy admission needs no job projection read. Parent prose
				// cannot become a recorded job, even for a verification lane.
				packet := map[string]any{"inputs": map[string]any{"task": "Run all parent integration checks"}}
				bound, err := validateWorkerPacketJob(context.Background(), nil, registered.Definition, "legacy-work", lane, mustJSONValue(packet))
				if err != nil || bound != nil {
					t.Fatalf("legacy %s dispatch job = %#v, error = %v, want no binding", lane.ID, bound, err)
				}
				packet["inputs"].(map[string]any)["worker_job"] = map[string]any{"job_id": "job:forbidden"}
				if _, err := validateWorkerPacketJob(context.Background(), nil, registered.Definition, "legacy-work", lane, mustJSONValue(packet)); err == nil || !strings.Contains(err.Error(), "does not admit") {
					t.Fatalf("legacy %s bound-job error = %v, want refusal", lane.ID, err)
				}
			}
		})
	}
}

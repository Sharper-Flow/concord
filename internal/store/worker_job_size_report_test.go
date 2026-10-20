package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The completed report's size members bind the dispatched job's recorded
// measurement (CD-0225 D2, CD-0227 D2): a capped implementation job's report
// carries production_size with the job's recorded base and the observed
// delivered tree — a missing member is malformed output — and every other
// report carries neither production_size nor direct_caller_repairs.

// reportBindingCompletion authors the completed report event of one
// implementation attempt, with the caller's mutation applied to the payload
// before the append. The reported directory is the fixture's active claim,
// which the live fold requires.
func reportBindingCompletion(t *testing.T, s *Store, workID, attemptID string, lane LaneDefinition, job *WorkerJobBinding, mutate func(payload *WorkerCompletedPayload)) Event {
	t.Helper()
	payload := WorkerCompletedPayload{
		AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion,
		EvidenceOrigin: WorkerEvidenceReported, WorkerJob: job, WorkerDirectory: workerFixtureDirectory(t, s, workID),
	}
	if len(lane.RequiredReportBlocks) > 0 {
		payload.Review = &WorkerReviewBlock{Verdict: "ship", Findings: []WorkerReviewFinding{}}
	}
	payload.Evidence = reportedLaneEvidenceForTest(lane, payload.Review)
	if mutate != nil {
		mutate(&payload)
	}
	return Event{EventID: "complete-" + attemptID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(21, 0).UTC(), PayloadVersion: WorkerEvidenceEventPayloadVersion(WorkerCompleted), Payload: mustJSONValue(payload)}
}

// reviewLaneClaimRepo materializes the fixture's claimed worktree as one
// real git repository with a committed seed file on the claim's own branch,
// so the shared accepted-delivery helper's edit and commit run against the
// claim the fixture already holds instead of a bare directory.
func reviewLaneClaimRepo(t *testing.T, s *Store, workID string) {
	t.Helper()
	entry, err := activeWorktreeEntryForProject(context.Background(), s.DatabaseForTesting(), "verify_worktree", workID, "project")
	if err != nil {
		t.Fatal(err)
	}
	runTestGit(t, entry.Path, "init", "-b", entry.Branch)
	if err := os.WriteFile(filepath.Join(entry.Path, "seed.txt"), []byte("review lane fixture seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, entry.Path, "add", "seed.txt")
	runTestGit(t, entry.Path, "commit", "-m", "review lane fixture seed")
}

func TestWorkerCompletedReportSizeFoldBinding(t *testing.T) {
	const workID = "worker-report-size-binding"
	fixture := sizeDispatchFixture(t, workID)
	s := workerJobSizeStore(fixture)
	job := &WorkerJobBinding{JobID: "job:report-size", Revision: 1}
	recordEditingJobForTest(t, fixture, workID, workerJobSizeEditingFields(t, workID, fixture), job)
	recordSizeDispatchAuthorization(t, s, workID, "attempt:report-size", *job)
	if err := appendJobBoundWorkerDispatch(t, s, workID, "attempt:report-size", job); err != nil {
		t.Fatalf("record implementation dispatch: %v", err)
	}
	lane := reviewGateLane(t, "implementation")
	base := testJobSizeBaseCommit()
	size := func(tree string) *WorkerProductionSize {
		return &WorkerProductionSize{Base: base, Tree: tree, Count: 12}
	}
	t.Run("a bound production size admits", func(t *testing.T) {
		completed := reportBindingCompletion(t, s, workID, "attempt:report-size", lane, job, func(payload *WorkerCompletedPayload) {
			payload.ObservedDelivery = &WorkerObservedDelivery{Commit: strings.Repeat("3", 40), State: "clean"}
			payload.ProductionSize = size(strings.Repeat("3", 40))
		})
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{completed}}); err != nil {
			t.Fatalf("bound size report refused: %v", err)
		}
	})
	t.Run("members on a review lane refuse", func(t *testing.T) {
		// A separate work keeps the review gate's accepted delivery and its
		// held attempt out of this fixture's step: the review gate owes one
		// accepted implementation delivery, and a second capped job stays
		// open for the review attempt to bind.
		reviewWork := workID + "-review"
		reviewFixture := sizeDispatchFixture(t, reviewWork)
		reviewStore := workerJobSizeStore(reviewFixture)
		reviewLaneClaimRepo(t, reviewStore, reviewWork)
		seedAcceptedImplementationDeliveryAt(t, reviewStore, reviewWork, reviewFixture.owner, currentStep(t, reviewStore, reviewWork))
		gateJob := &WorkerJobBinding{JobID: "job:report-review", Revision: 1}
		gateFields := workerJobSizeEditingFields(t, reviewWork, reviewFixture)
		gateFields["path_scope"] = []string{"adapter/opencode"}
		gateFields["measurement_basis"] = map[string]any{"base_commit": base, "paths": []map[string]any{{"path": "adapter/opencode/packet.ts", "class": "production"}}}
		recordEditingJobForTest(t, reviewFixture, reviewWork, gateFields, gateJob)
		reviewLane := reviewGateLane(t, "review")
		attemptID := "attempt:report-review"
		recordSizeDispatchAuthorizationClass(t, reviewStore, reviewWork, attemptID, *gateJob, reviewLane.CapabilityClass)
		if err := appendLaneBoundWorkerDispatch(t, reviewStore, reviewWork, attemptID, reviewLane, gateJob); err != nil {
			t.Fatalf("record review dispatch: %v", err)
		}
		completed := reportBindingCompletion(t, reviewStore, reviewWork, attemptID, reviewLane, gateJob, func(payload *WorkerCompletedPayload) {
			payload.ProductionSize = size(base)
		})
		err := ApplyOperation(context.Background(), reviewStore, Operation{Events: []Event{completed}})
		if err == nil || !strings.Contains(err.Error(), "neither member") {
			t.Fatalf("review-lane member error = %v, want the reserved-members refusal", err)
		}
	})
	for _, refusal := range []struct {
		name   string
		mutate func(payload *WorkerCompletedPayload)
		want   string
	}{
		{"missing production size", func(payload *WorkerCompletedPayload) {
			payload.ObservedDelivery = &WorkerObservedDelivery{Commit: strings.Repeat("3", 40), State: "clean"}
		}, "missing production_size is malformed output"},
		{"wrong base", func(payload *WorkerCompletedPayload) {
			payload.ObservedDelivery = &WorkerObservedDelivery{Commit: strings.Repeat("3", 40), State: "clean"}
			payload.ProductionSize = &WorkerProductionSize{Base: strings.Repeat("b", 40), Tree: strings.Repeat("3", 40), Count: 12}
		}, "does not equal the dispatched job's recorded base_commit"},
		{"wrong tree", func(payload *WorkerCompletedPayload) {
			payload.ObservedDelivery = &WorkerObservedDelivery{Commit: strings.Repeat("3", 40), State: "clean"}
			payload.ProductionSize = size(strings.Repeat("c", 40))
		}, "must equal the observed delivery commit"},
		// Without a sealed observation the tree has nothing to bind; the
		// base still binds, and acceptance refuses the unmeasured delta.
		{"wrong base without an observed delivery", func(payload *WorkerCompletedPayload) {
			payload.ProductionSize = &WorkerProductionSize{Base: strings.Repeat("b", 40), Tree: strings.Repeat("3", 40), Count: 12}
		}, "does not equal the dispatched job's recorded base_commit"},
		{"missing production size without an observed delivery", func(payload *WorkerCompletedPayload) {}, "missing production_size is malformed output"},
	} {
		t.Run(refusal.name+" refuses", func(t *testing.T) {
			attemptID := "attempt:report-size-" + strings.ReplaceAll(refusal.name, " ", "-")
			recordSizeDispatchAuthorization(t, s, workID, attemptID, *job)
			if err := appendJobBoundWorkerDispatch(t, s, workID, attemptID, job); err != nil {
				t.Fatalf("record implementation dispatch %s: %v", attemptID, err)
			}
			completed := reportBindingCompletion(t, s, workID, attemptID, lane, job, refusal.mutate)
			err := ApplyOperation(context.Background(), s, Operation{Events: []Event{completed}})
			if err == nil || !strings.Contains(err.Error(), refusal.want) {
				t.Fatalf("%s error = %v, want %q", refusal.name, err, refusal.want)
			}
		})
	}
	t.Run("members on an uncapped job refuse", func(t *testing.T) {
		uncapped := &WorkerJobBinding{JobID: "job:report-uncapped", Revision: 1}
		recordEditingJobForTest(t, fixture, workID, workerJobSizeReadOnlyFields(t, workID, fixture), uncapped)
		recordSizeDispatchAuthorization(t, s, workID, "attempt:report-uncapped", *uncapped)
		if err := appendJobBoundWorkerDispatch(t, s, workID, "attempt:report-uncapped", uncapped); err != nil {
			t.Fatalf("record uncapped dispatch: %v", err)
		}
		completed := reportBindingCompletion(t, s, workID, "attempt:report-uncapped", lane, uncapped, func(payload *WorkerCompletedPayload) {
			payload.ProductionSize = size(base)
		})
		err := ApplyOperation(context.Background(), s, Operation{Events: []Event{completed}})
		if err == nil || !strings.Contains(err.Error(), "neither member") {
			t.Fatalf("uncapped-job member error = %v, want the reserved-members refusal", err)
		}
	})
}

// TestWorkerCompletedReportSizeVersionBoundary: the report's size members
// are reserved for the size payload version; bytes that carry them on a
// below-boundary source are fabricated content no store ever recorded.
func TestWorkerCompletedReportSizeVersionBoundary(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	workID, attemptID := "work-report-size-version", "attempt-report-size-version"
	authorizeWorkerFixtureDispatch(t, s, workID, attemptID)
	lane := BuiltinLaneDefinitions()[0]
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent(workID, attemptID, lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	payload := WorkerCompletedPayload{
		AttemptID: attemptID, ReadbackModel: "openai/gpt-5.6-luna", ReportSchemaVersion: WorkerReportSchemaVersion,
		EvidenceOrigin: WorkerEvidenceLegacyUnavailable, WorkerDirectory: workerFixtureDirectory(t, s, workID),
		ProductionSize: &WorkerProductionSize{Base: testJobSizeBaseCommit(), Tree: strings.Repeat("3", 40), Count: 3},
	}
	legacy := Event{EventID: "complete-" + attemptID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: workerCompletedJobSizeVersion - 1, Payload: mustJSONValue(payload)}
	err := ApplyOperation(context.Background(), s, Operation{Events: []Event{legacy}})
	if err == nil || !strings.Contains(err.Error(), "reserved for payload version >= 8") {
		t.Fatalf("production_size below the boundary error = %v, want the fabricated-bytes refusal", err)
	}
}

// The report's size members mirror the closed shape the published report
// schema declares: bounded counts, raw commits, bounded repair lists with
// the three check signals each carrying its observed reference.
func TestWorkerCompletedReportSizeShape(t *testing.T) {
	t.Parallel()
	event := Event{}
	validRepair := WorkerDirectCallerRepair{
		Owner:  WorkerCallerMechanism{Path: "internal/store/worker_lanes.go", EntryPoint: "foldWorkerCompleted"},
		Caller: WorkerCallerMechanism{Path: "adapter/opencode/dispatch.ts", EntryPoint: "admitWorkerResult"},
		Edge:   "the caller invokes the changed owner entry point", Exposing: "the owner-only request fixture",
		ChangedPaths: []string{"internal/store/worker_lanes.go", "adapter/opencode/dispatch.ts"},
		Checks: WorkerCallerRepairChecks{
			BaselinePass:  WorkerCallerRepairCheck{Observed: "go test ./internal/agent -run Report: pass on the baseline"},
			OwnerOnlyFail: WorkerCallerRepairCheck{Observed: "go test ./internal/agent -run Report: compile failure with the owner change alone", InferredCause: "the caller passes the removed parameter"},
			CombinedPass:  WorkerCallerRepairCheck{Observed: "go test ./internal/agent -run Report: pass with the combined change"},
		},
	}
	base := func() WorkerCompletedPayload {
		return WorkerCompletedPayload{AttemptID: "attempt-shape", ReadbackModel: "openai/gpt-5.6-luna", ReportSchemaVersion: WorkerReportSchemaVersion, EvidenceOrigin: WorkerEvidenceLegacyUnavailable}
	}
	if err := validateWorkerCompletedPayload(event, base()); err != nil {
		t.Fatalf("member-free payload refused: %v", err)
	}
	ok := base()
	ok.ProductionSize = &WorkerProductionSize{Base: testJobSizeBaseCommit(), Tree: strings.Repeat("3", 40), Count: 2147483647}
	ok.DirectCallerRepairs = []WorkerDirectCallerRepair{validRepair}
	if err := validateWorkerCompletedPayload(event, ok); err != nil {
		t.Fatalf("bounded size members refused: %v", err)
	}
	for _, refusal := range []struct {
		name   string
		mutate func(payload *WorkerCompletedPayload)
		want   string
	}{
		{"base is not a raw oid", func(payload *WorkerCompletedPayload) { payload.ProductionSize.Base = "HEAD" }, "raw commit OID"},
		{"count above the bound", func(payload *WorkerCompletedPayload) { payload.ProductionSize.Count = 2147483648 }, "between 0 and"},
		{"negative count", func(payload *WorkerCompletedPayload) { payload.ProductionSize.Count = -1 }, "between 0 and"},
		{"too many repairs", func(payload *WorkerCompletedPayload) {
			payload.DirectCallerRepairs = make([]WorkerDirectCallerRepair, WorkerCallerRepairsMax+1)
			for index := range payload.DirectCallerRepairs {
				payload.DirectCallerRepairs[index] = validRepair
			}
		}, fmt.Sprintf("1 and %d repairs", WorkerCallerRepairsMax)},
		{"an explicit empty repair list", func(payload *WorkerCompletedPayload) {
			payload.DirectCallerRepairs = []WorkerDirectCallerRepair{}
		}, fmt.Sprintf("1 and %d repairs", WorkerCallerRepairsMax)},
		{"a caller path escaping the repository", func(payload *WorkerCompletedPayload) {
			repair := validRepair
			repair.Caller.Path = "../outside.go"
			payload.DirectCallerRepairs = []WorkerDirectCallerRepair{repair}
		}, "caller path"},
		{"an absolute changed path", func(payload *WorkerCompletedPayload) {
			repair := validRepair
			repair.ChangedPaths = []string{"/etc/passwd"}
			payload.DirectCallerRepairs = []WorkerDirectCallerRepair{repair}
		}, "changed paths"},
		{"a changed path named twice", func(payload *WorkerCompletedPayload) {
			repair := validRepair
			repair.ChangedPaths = []string{"internal/store/worker_lanes.go", "internal/store/worker_lanes.go"}
			payload.DirectCallerRepairs = []WorkerDirectCallerRepair{repair}
		}, "twice"},
		{"an explicit empty generator_ref", func(payload *WorkerCompletedPayload) {
			repair := validRepair
			repair.GeneratorRef = []string{}
			payload.DirectCallerRepairs = []WorkerDirectCallerRepair{repair}
		}, "generator_ref must name between 1 and 8"},
		{"too many changed paths", func(payload *WorkerCompletedPayload) {
			repair := validRepair
			repair.ChangedPaths = make([]string, WorkerCallerRepairPathsMax+1)
			for index := range repair.ChangedPaths {
				repair.ChangedPaths[index] = fmt.Sprintf("internal/store/path_%d.go", index)
			}
			payload.DirectCallerRepairs = []WorkerDirectCallerRepair{repair}
		}, fmt.Sprintf("1 and %d changed paths", WorkerCallerRepairPathsMax)},
		{"check without an observed reference", func(payload *WorkerCompletedPayload) {
			repair := validRepair
			repair.Checks.BaselinePass.Observed = ""
			payload.DirectCallerRepairs = []WorkerDirectCallerRepair{repair}
		}, "observed"},
		{"owner without an entry point", func(payload *WorkerCompletedPayload) {
			repair := validRepair
			repair.Owner.EntryPoint = ""
			payload.DirectCallerRepairs = []WorkerDirectCallerRepair{repair}
		}, "entry_point"},
		{"generator_ref outside changed paths", func(payload *WorkerCompletedPayload) {
			repair := validRepair
			repair.GeneratorRef = []string{"scripts/not-named.go"}
			payload.DirectCallerRepairs = []WorkerDirectCallerRepair{repair}
		}, "outside its changed paths"},
	} {
		t.Run(refusal.name+" refuses", func(t *testing.T) {
			t.Parallel()
			payload := base()
			payload.ProductionSize = &WorkerProductionSize{Base: testJobSizeBaseCommit(), Tree: strings.Repeat("3", 40), Count: 1}
			refusal.mutate(&payload)
			err := validateWorkerCompletedPayload(event, payload)
			if err == nil || !strings.Contains(err.Error(), refusal.want) {
				t.Fatalf("%s error = %v, want %q", refusal.name, err, refusal.want)
			}
		})
	}
	t.Run("a generated changed path with its owning generator_ref admits", func(t *testing.T) {
		t.Parallel()
		payload := base()
		payload.ProductionSize = &WorkerProductionSize{Base: testJobSizeBaseCommit(), Tree: strings.Repeat("3", 40), Count: 1}
		repair := validRepair
		repair.ChangedPaths = append(slices.Clone(validRepair.ChangedPaths),
			"contracts/agent-lanes.v1.json", "internal/store/generated_agent_lanes.go")
		repair.GeneratorRef = []string{"contracts/agent-lanes.v1.json"}
		payload.DirectCallerRepairs = []WorkerDirectCallerRepair{repair}
		if err := validateWorkerCompletedPayload(event, payload); err != nil {
			t.Fatalf("owned generated changed path refused: %v", err)
		}
	})
}

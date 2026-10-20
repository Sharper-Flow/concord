package store

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"
)

// The size-capable acceptance gate (CD-0225 D1-D2, CD-0227 D2): one
// measurement over the sealed observation's per-path counts, a report that
// must agree with it, a cap that refuses with the typed overrun and the split
// route, supplemental classifications that only add classes, and caller
// repairs that contain every changed path outside the job's scope.

func TestWorkerJobProductionSize(t *testing.T) {
	t.Parallel()
	base := testJobSizeBaseCommit()
	basis := &WorkerJobMeasurementBasis{BaseCommit: base, Paths: []WorkerJobMeasuredPath{
		{Path: "internal/store/a.go", Class: "production"},
		{Path: "internal/store/a_test.go", Class: "test"},
		{Path: "docs/a.md", Class: "documentation"},
		{Path: "contracts/a.json", Class: "generated", GeneratorRef: "scripts/gen.py"},
	}}
	observed := func(paths ...WorkerObservedPath) *WorkerObservedDelivery {
		return &WorkerObservedDelivery{Commit: strings.Repeat("3", 40), State: "clean", Base: base, Paths: paths}
	}
	for _, tc := range []struct {
		name         string
		basis        *WorkerJobMeasurementBasis
		supplemental []WorkerJobMeasuredPath
		observation  *WorkerObservedDelivery
		foreign      map[string]bool
		want         int64
		unavailable  string
	}{
		{name: "production counts added plus deleted", basis: basis, observation: observed(WorkerObservedPath{Path: "internal/store/a.go", Added: 7, Deleted: 3}), want: 10},
		{name: "test, documentation, and generated paths do not count", basis: basis, observation: observed(
			WorkerObservedPath{Path: "internal/store/a.go", Added: 1},
			WorkerObservedPath{Path: "internal/store/a_test.go", Added: 50},
			WorkerObservedPath{Path: "docs/a.md", Added: 50},
			WorkerObservedPath{Path: "contracts/a.json", Added: 50, Deleted: 50},
		), want: 1},
		{name: "an unclassified path defaults to production", basis: basis, observation: observed(WorkerObservedPath{Path: "internal/store/b.go", Added: 4, Deleted: 1}), want: 5},
		{name: "a supplemental class excludes an unclassified path", basis: basis, supplemental: []WorkerJobMeasuredPath{{Path: "internal/store/b_test.go", Class: "test"}},
			observation: observed(WorkerObservedPath{Path: "internal/store/b_test.go", Added: 40}), want: 0},
		{name: "a supplemental class never overrides the inventory", basis: basis, supplemental: []WorkerJobMeasuredPath{{Path: "internal/store/a.go", Class: "test"}},
			observation: observed(WorkerObservedPath{Path: "internal/store/a.go", Added: 9}), want: 9},
		{name: "a rename counts its deletion and its addition", basis: basis, observation: observed(
			WorkerObservedPath{Path: "internal/store/a.go", Deleted: 4},
			WorkerObservedPath{Path: "internal/store/renamed.go", Added: 4},
		), want: 8},
		{name: "a binary test path is not counted", basis: basis, observation: observed(WorkerObservedPath{Path: "internal/store/a_test.go", Binary: true}), want: 0},
		{name: "a foreign accepted path never enters the count", basis: basis, foreign: map[string]bool{"adapter/opencode/accepted.ts": true},
			observation: observed(WorkerObservedPath{Path: "adapter/opencode/accepted.ts", Added: 90}, WorkerObservedPath{Path: "internal/store/a.go", Added: 2}), want: 2},
		{name: "a binary production path is unavailable", basis: basis, observation: observed(WorkerObservedPath{Path: "internal/store/icon.png", Binary: true}), unavailable: "is binary"},
		{name: "count overflow is unavailable", basis: basis, observation: observed(WorkerObservedPath{Path: "internal/store/a.go", Added: math.MaxInt64, Deleted: 1}), unavailable: "exceeds the measurement range"},
		{name: "cumulative overflow is unavailable", basis: basis, observation: observed(WorkerObservedPath{Path: "internal/store/a.go", Added: math.MaxInt64}, WorkerObservedPath{Path: "internal/store/b.go", Added: 1}), unavailable: "exceeds the measurement range"},
		{name: "a dirty observation is unavailable", basis: basis, observation: &WorkerObservedDelivery{Commit: strings.Repeat("3", 40), State: "dirty", Base: base}, unavailable: "was dirty"},
		{name: "an absent observation is unavailable", basis: basis, unavailable: "no sealed delivery observation"},
		{name: "a changed base is unavailable", basis: basis, observation: &WorkerObservedDelivery{Commit: strings.Repeat("3", 40), State: "clean", Base: strings.Repeat("e", 40)}, unavailable: "not the job's recorded base_commit"},
		{name: "no basis is unavailable", observation: observed(), unavailable: "no measurement basis"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			count, unavailable := workerJobProductionSize(tc.basis, tc.supplemental, tc.observation, tc.foreign)
			if tc.unavailable != "" {
				if count != 0 || !strings.Contains(unavailable, tc.unavailable) {
					t.Fatalf("measurement = %d, %q, want unavailable %q and never zero lines", count, unavailable, tc.unavailable)
				}
				return
			}
			if unavailable != "" || count != tc.want {
				t.Fatalf("measurement = %d, %q, want %d", count, unavailable, tc.want)
			}
		})
	}
}

// sizeAcceptance is one completed capped implementation attempt awaiting
// disposition: the editing job is recorded with the caller's cap, the
// attempt is dispatched, and its completion carries the caller's sealed
// observation and report.
type sizeAcceptance struct {
	fixture   workflowReturnRouteFixture
	s         *Store
	workID    string
	attemptID string
	job       WorkerJobBinding
	tree      string
}

func seedSizeAcceptance(t *testing.T, workID string, mutate func(payload *WorkerCompletedPayload)) sizeAcceptance {
	t.Helper()
	fixture := sizeDispatchFixture(t, workID)
	t.Cleanup(func() { fixture.store.Close() })
	s := workerJobSizeStore(fixture)
	fields := workerJobSizeEditingFields(t, workID, fixture)
	fields["production_size_max"] = 10
	job := &WorkerJobBinding{JobID: "job:size-accept", Revision: 1}
	recordEditingJobForTest(t, fixture, workID, fields, job)
	attemptID := "attempt:" + workID
	recordSizeDispatchAuthorization(t, s, workID, attemptID, *job)
	if err := appendJobBoundWorkerDispatch(t, s, workID, attemptID, job); err != nil {
		t.Fatalf("record implementation dispatch: %v", err)
	}
	tree := strings.Repeat("3", 40)
	completed := reportBindingCompletion(t, s, workID, attemptID, reviewGateLane(t, "implementation"), job, func(payload *WorkerCompletedPayload) {
		payload.ObservedDelivery = &WorkerObservedDelivery{Commit: tree, State: "clean", Base: testJobSizeBaseCommit()}
		payload.ProductionSize = &WorkerProductionSize{Base: testJobSizeBaseCommit(), Tree: tree}
		mutate(payload)
	})
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{completed}}); err != nil {
		t.Fatalf("record the completed report: %v", err)
	}
	return sizeAcceptance{fixture: fixture, s: s, workID: workID, attemptID: attemptID, job: *job, tree: tree}
}

func (a sizeAcceptance) accept(t *testing.T, extra map[string]any) error {
	t.Helper()
	fields := map[string]any{"attempt_id": a.attemptID, "attempt_epoch": 1}
	for member, value := range extra {
		fields[member] = value
	}
	return runVerdictActionAs(t, a.s, a.workID, "accept_worker_result", mustJSONValue(fields), 0, reviewGateAcceptor(a.workID))
}

// acceptedSupplemental reads the supplemental classifications the recorded
// acceptance carries; accepted is false when no acceptance was recorded.
func (a sizeAcceptance) acceptedSupplemental(t *testing.T) (supplemental []WorkerJobMeasuredPath, accepted bool) {
	t.Helper()
	rows, err := a.s.DatabaseForTesting().Query(`SELECT payload FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='accept_worker_result'`, a.workID, WorkflowActionCompleted)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var payload workflowActionCompletedPayload
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			t.Fatal(err)
		}
		supplemental, accepted = payload.SupplementalClassifications, true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return supplemental, accepted
}

func sizedPaths(paths ...WorkerObservedPath) func(payload *WorkerCompletedPayload) {
	return func(payload *WorkerCompletedPayload) {
		payload.ObservedDelivery.Paths = paths
		for _, path := range paths {
			payload.ProductionSize.Count += path.Added + path.Deleted
		}
	}
}

func requireNoAcceptance(t *testing.T, a sizeAcceptance) {
	t.Helper()
	if _, accepted := a.acceptedSupplemental(t); accepted {
		t.Fatal("a refused acceptance recorded its completion")
	}
	var lifecycle string
	if err := a.s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state FROM worker_attempts WHERE attempt_id=?`, a.attemptID).Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "completed" {
		t.Fatalf("refused attempt lifecycle = %q, want completed and awaiting disposition", lifecycle)
	}
}

func TestWorkerJobSizeAcceptanceCap(t *testing.T) {
	t.Run("a count at the cap accepts", func(t *testing.T) {
		a := seedSizeAcceptance(t, "size-accept-at-cap", sizedPaths(WorkerObservedPath{Path: "internal/store/worker_jobs.go", Added: 6, Deleted: 4}))
		if err := a.accept(t, nil); err != nil {
			t.Fatalf("in-cap acceptance refused: %v", err)
		}
		if supplemental, accepted := a.acceptedSupplemental(t); !accepted || supplemental != nil {
			t.Fatalf("acceptance = %v, %#v, want one acceptance without supplemental classifications", accepted, supplemental)
		}
	})
	t.Run("a count above the cap refuses with the typed overrun and the split route", func(t *testing.T) {
		a := seedSizeAcceptance(t, "size-accept-overrun", sizedPaths(WorkerObservedPath{Path: "internal/store/worker_jobs.go", Added: 7, Deleted: 4}))
		err := a.accept(t, nil)
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || failure.SizeOverrun == nil {
			t.Fatalf("overrun acceptance error = %v, want %s with size_overrun", err, KindInvalidOperation)
		}
		want := SizeOverrun{JobID: a.job.JobID, Revision: a.job.Revision, Cap: 10, Observed: 11, Subject: a.tree}
		if *failure.SizeOverrun != want {
			t.Fatalf("size_overrun = %#v, want %#v", *failure.SizeOverrun, want)
		}
		if !slices.Contains(failure.RecoveryRefs, "record_worker_job") {
			t.Fatalf("overrun recovery refs = %q, want the record_worker_job split route", failure.RecoveryRefs)
		}
		requireNoAcceptance(t, a)
	})
	t.Run("a report that disagrees with the observation refuses", func(t *testing.T) {
		a := seedSizeAcceptance(t, "size-accept-disagree", func(payload *WorkerCompletedPayload) {
			sizedPaths(WorkerObservedPath{Path: "internal/store/worker_jobs.go", Added: 3})(payload)
			payload.ProductionSize.Count = 2
		})
		var failure *Failure
		if err := a.accept(t, nil); !errors.As(err, &failure) || failure.Kind != KindMissingEvidence || !strings.Contains(failure.Detail, "does not agree with the observed count 3") {
			t.Fatalf("disagreeing report acceptance error = %v, want the agreement refusal", err)
		}
		requireNoAcceptance(t, a)
	})
	for _, unavailable := range []struct {
		name, workID, want string
		mutate             func(payload *WorkerCompletedPayload)
	}{
		{"an absent observation", "size-accept-no-observation", "no sealed delivery observation", func(payload *WorkerCompletedPayload) { payload.ObservedDelivery = nil }},
		{"a dirty delivered tree", "size-accept-dirty", "was dirty", func(payload *WorkerCompletedPayload) { payload.ObservedDelivery.State = "dirty" }},
		{"a binary production path", "size-accept-binary", "is binary", sizedPaths(WorkerObservedPath{Path: "internal/store/icon.png", Binary: true})},
	} {
		t.Run(unavailable.name+" establishes no size", func(t *testing.T) {
			a := seedSizeAcceptance(t, unavailable.workID, unavailable.mutate)
			var failure *Failure
			if err := a.accept(t, nil); !errors.As(err, &failure) || failure.Kind != KindMissingEvidence || !strings.Contains(failure.Detail, unavailable.want) {
				t.Fatalf("unmeasurable acceptance error = %v, want the unavailable-evidence refusal %q", err, unavailable.want)
			}
			requireNoAcceptance(t, a)
		})
	}
}

func TestWorkerJobSizeAcceptanceSupplementalClassifications(t *testing.T) {
	// The uninventoried test path counts as production by default, which
	// overruns the cap; its classification at acceptance excludes it.
	paths := sizedPaths(WorkerObservedPath{Path: "internal/store/worker_jobs.go", Added: 5}, WorkerObservedPath{Path: "internal/store/worker_jobs_test.go", Added: 30})
	t.Run("a binary path excluded by classification establishes the final size", func(t *testing.T) {
		a := seedSizeAcceptance(t, "size-accept-supplemental-binary", sizedPaths(
			WorkerObservedPath{Path: "internal/store/worker_jobs.go", Added: 5},
			WorkerObservedPath{Path: "internal/store/fixture.bin", Binary: true}))
		classification := []WorkerJobMeasuredPath{{Path: "internal/store/fixture.bin", Class: "test"}}
		if err := a.accept(t, map[string]any{"supplemental_classifications": classification}); err != nil {
			t.Fatalf("a binary test fixture made the final production count unavailable: %v", err)
		}
		var count int64
		if err := a.s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.worker_production_size.count') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='accept_worker_result'`, a.workID, WorkflowActionCompleted).Scan(&count); err != nil || count != 5 {
			t.Fatalf("final accepted count = %d (%v), want 5 without the binary test fixture", count, err)
		}
	})
	t.Run("an unclassified path defaults to production", func(t *testing.T) {
		a := seedSizeAcceptance(t, "size-accept-default-production", paths)
		var failure *Failure
		if err := a.accept(t, nil); !errors.As(err, &failure) || failure.SizeOverrun == nil || failure.SizeOverrun.Observed != 35 {
			t.Fatalf("unclassified acceptance error = %v, want the overrun counting the test path as production", err)
		}
	})
	t.Run("a recorded classification excludes the path and records with the acceptance", func(t *testing.T) {
		a := seedSizeAcceptance(t, "size-accept-supplemental", paths)
		classification := []WorkerJobMeasuredPath{{Path: "internal/store/worker_jobs_test.go", Class: "test"}}
		if err := a.accept(t, map[string]any{"supplemental_classifications": classification}); err != nil {
			t.Fatalf("classified acceptance refused: %v", err)
		}
		if supplemental, accepted := a.acceptedSupplemental(t); !accepted || !slices.Equal(supplemental, classification) {
			t.Fatalf("recorded classification = %#v, want %#v on the acceptance", supplemental, classification)
		}
		var count int64
		var base, tree string
		if err := a.s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.worker_production_size.count'), json_extract(payload,'$.worker_production_size.base'), json_extract(payload,'$.worker_production_size.tree') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='accept_worker_result'`, a.workID, WorkflowActionCompleted).Scan(&count, &base, &tree); err != nil {
			t.Fatal(err)
		}
		if count != 5 || base != testJobSizeBaseCommit() || tree != a.tree {
			t.Fatalf("accepted size = %d at %s..%s, want 5 at the report's base/tree", count, base, tree)
		}
		var reported int64
		if err := a.s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.production_size.count') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.attempt_id')=?`, a.workID, WorkerCompleted, a.attemptID).Scan(&reported); err != nil {
			t.Fatal(err)
		}
		if reported != 35 {
			t.Fatalf("worker report was rewritten to %d, want its original inventory/default count 35", reported)
		}
		views, err := readWorkerJobRevisions(context.Background(), a.s.db, a.workID)
		if err != nil {
			t.Fatal(err)
		}
		for _, view := range views {
			if view.Binding.JobID == a.job.JobID && view.Binding.Digest != a.job.Digest {
				t.Fatalf("job digest changed from %s to %s", a.job.Digest, view.Binding.Digest)
			}
		}
	})
	t.Run("the executor cannot approve a non-production classification", func(t *testing.T) {
		a := seedSizeAcceptance(t, "size-accept-self-classify", paths)
		executor := workflowActorForRef(t, a.s, crossAttemptDispatchedLaneActor(t, a.s, a.workID, a.attemptID))
		fields := map[string]any{"attempt_id": a.attemptID, "attempt_epoch": 1,
			"supplemental_classifications": []WorkerJobMeasuredPath{{Path: "internal/store/worker_jobs_test.go", Class: "test"}}}
		var failure *Failure
		err := runVerdictActionAs(t, a.s, a.workID, "accept_worker_result", mustJSONValue(fields), 0, executor)
		if !errors.As(err, &failure) || failure.Kind != KindUnauthorized {
			t.Fatalf("executor's classification acceptance = %v, want unauthorized", err)
		}
		requireNoAcceptance(t, a)
	})
	t.Run("a worker cannot pre-apply the coordinator's exclusion", func(t *testing.T) {
		a := seedSizeAcceptance(t, "size-accept-premature-exclusion", func(payload *WorkerCompletedPayload) {
			paths(payload)
			payload.ProductionSize.Count = 5
		})
		err := a.accept(t, map[string]any{"supplemental_classifications": []WorkerJobMeasuredPath{{Path: "internal/store/worker_jobs_test.go", Class: "test"}}})
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindMissingEvidence || !strings.Contains(failure.Detail, "observed count 35 under the recorded inventory and production default") {
			t.Fatalf("pre-applied exclusion acceptance = %v, want the inventory/default disagreement refusal", err)
		}
		requireNoAcceptance(t, a)
	})
	for _, refusal := range []struct {
		name, workID, want string
		classification     WorkerJobMeasuredPath
	}{
		{"an inventoried path cannot be reclassified", "size-accept-reclassify", "already classified by the job's inventory", WorkerJobMeasuredPath{Path: "internal/store/worker_jobs.go", Class: "test"}},
		{"an unobserved path cannot be classified", "size-accept-unobserved", "not a path of this job's observed delta", WorkerJobMeasuredPath{Path: "internal/store/other_test.go", Class: "test"}},
		{"a generated class needs its generator", "size-accept-generator", "without its owning generator_ref", WorkerJobMeasuredPath{Path: "internal/store/worker_jobs_test.go", Class: "generated"}},
	} {
		t.Run(refusal.name, func(t *testing.T) {
			a := seedSizeAcceptance(t, refusal.workID, paths)
			err := a.accept(t, map[string]any{"supplemental_classifications": []WorkerJobMeasuredPath{refusal.classification}})
			if err == nil || !strings.Contains(err.Error(), refusal.want) {
				t.Fatalf("classification error = %v, want %q", err, refusal.want)
			}
			requireNoAcceptance(t, a)
		})
	}
}

func TestWorkerJobSizeAcceptanceCallerRepairContainment(t *testing.T) {
	const callerPath = "adapter/opencode/dispatch.ts"
	repair := func(owner WorkerCallerMechanism) WorkerDirectCallerRepair {
		return WorkerDirectCallerRepair{
			Owner:  owner,
			Caller: WorkerCallerMechanism{Path: callerPath, EntryPoint: "admitWorkerResult"},
			Edge:   "the caller invokes the changed owner entry point", Exposing: "the owner-only request fixture",
			ChangedPaths: []string{callerPath},
			Checks: WorkerCallerRepairChecks{
				BaselinePass:  WorkerCallerRepairCheck{Observed: "go test ./internal/agent -run Report: pass on the baseline"},
				OwnerOnlyFail: WorkerCallerRepairCheck{Observed: "go test ./internal/agent -run Report: compile failure with the owner change alone", InferredCause: "the caller passes the removed parameter"},
				CombinedPass:  WorkerCallerRepairCheck{Observed: "go test ./internal/agent -run Report: pass with the combined change"},
			},
		}
	}
	mechanism := WorkerCallerMechanism{Path: "internal/store/worker_jobs.go", EntryPoint: "foldWorkerJobRecorded"}
	withCaller := func(repairs ...WorkerDirectCallerRepair) func(payload *WorkerCompletedPayload) {
		return func(payload *WorkerCompletedPayload) {
			sizedPaths(WorkerObservedPath{Path: "internal/store/worker_jobs.go", Added: 2}, WorkerObservedPath{Path: callerPath, Added: 1, Deleted: 1})(payload)
			if len(repairs) > 0 {
				payload.DirectCallerRepairs = repairs
			}
		}
	}
	t.Run("an evidenced repair of an allowance mechanism accepts", func(t *testing.T) {
		a := seedSizeAcceptance(t, "size-accept-caller-repair", withCaller(repair(mechanism)))
		if err := a.accept(t, nil); err != nil {
			t.Fatalf("contained caller repair refused: %v", err)
		}
	})
	t.Run("a changed path outside the scope without a repair refuses", func(t *testing.T) {
		a := seedSizeAcceptance(t, "size-accept-caller-unrepaired", withCaller())
		if err := a.accept(t, nil); err == nil || !strings.Contains(err.Error(), "changed path "+callerPath+" outside its path_scope without an evidenced direct-caller repair") {
			t.Fatalf("unrepaired caller acceptance error = %v, want the containment refusal", err)
		}
		requireNoAcceptance(t, a)
	})
	t.Run("a repair of an owner outside the allowance refuses", func(t *testing.T) {
		a := seedSizeAcceptance(t, "size-accept-caller-owner", withCaller(repair(WorkerCallerMechanism{Path: "internal/store/worker_jobs.go", EntryPoint: "readWorkerJobRevisions"})))
		if err := a.accept(t, nil); err == nil || !strings.Contains(err.Error(), "which is not a mechanism of its direct-caller allowance") {
			t.Fatalf("foreign-owner repair acceptance error = %v, want the allowance refusal", err)
		}
		requireNoAcceptance(t, a)
	})
	t.Run("a repaired path another work item claims refuses", func(t *testing.T) {
		a := seedSizeAcceptance(t, "size-accept-caller-claimed", withCaller(repair(mechanism)))
		seedHeldPathClaimForTest(t, a.s, "size-accept-caller-claim-holder", "path:adapter/opencode")
		if err := a.accept(t, nil); err == nil || !strings.Contains(err.Error(), "holds through claim path:adapter/opencode") {
			t.Fatalf("claimed caller acceptance error = %v, want the foreign-claim refusal", err)
		}
		requireNoAcceptance(t, a)
	})
	t.Run("a path claim in another Project does not own this caller", func(t *testing.T) {
		a := seedSizeAcceptance(t, "size-accept-caller-other-project", withCaller(repair(mechanism)))
		const holder = "size-caller-other-project-holder"
		seedHeldPathClaimForTest(t, a.s, holder, "path:adapter/opencode")
		if err := ApplyOperation(context.Background(), a.s, Operation{
			Events: []Event{
				productCreatedEvent("other-product", "create-caller-other-product"),
				projectCreatedEvent("other-project", "create-caller-other-project"),
				operationEvent("caller-other-product-project", "product_project.added", SubjectProduct, "other-product", map[string]any{
					"product_id": "other-product", "project_id": "other-project", "role": "primary", "reason": "separate caller Project", "expected_version": 1, "resulting_version": 2,
				}),
			},
			ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProject, "other-project"): 0, VersionRef(SubjectProduct, "other-product"): 0},
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := a.s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE work_projects SET project_id='other-project' WHERE work_id=?; DELETE FROM fold_guard`, holder); err != nil {
			t.Fatal(err)
		}
		if err := a.accept(t, nil); err != nil {
			t.Fatalf("another Project's claim blocked a contained repair: %v", err)
		}
	})
	t.Run("multiple uncontained paths refuse in stable path order", func(t *testing.T) {
		a := seedSizeAcceptance(t, "size-accept-caller-stable-refusal", sizedPaths(WorkerObservedPath{Path: "z.go", Added: 1}, WorkerObservedPath{Path: "a.go", Added: 1}))
		for range 16 {
			err := a.accept(t, nil)
			var failure *Failure
			if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || !strings.Contains(failure.Detail, "changed path a.go outside its path_scope") {
				t.Fatalf("uncontained paths refusal = %v, want the first path in sorted order", err)
			}
		}
		requireNoAcceptance(t, a)
	})
}

// seedHeldPathClaimForTest gives a second work item a held path claim, the
// projection row a work.resource_claimed fold writes.
func seedHeldPathClaimForTest(t *testing.T, s *Store, holderWorkID, resourceKey string) {
	t.Helper()
	seedWork(t, s, holderWorkID)
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterFold(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO resource_claims(resource_key,holder_work_id,holder_agent,holder_session,reason,state,claimed_at) VALUES(?,?,'agent:test','session:test','the holder edits the caller','held','2026-01-01T00:00:00Z')`, resourceKey, holderWorkID); err != nil {
		t.Fatal(err)
	}
	if err := leaveFold(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// A correction's delivered subject may carry another job's work accepted
// after this job's first implementation dispatch; those paths never enter
// this job's count or its containment (CD-0225 D1).
func TestWorkerJobSizeAcceptanceExcludesForeignAcceptedPaths(t *testing.T) {
	t.Run("a foreign text path", func(t *testing.T) {
		// The report claims the whole delta; only the final count drops the
		// other job's accepted path.
		acceptForeignCorrection(t, "size-accept-foreign", WorkerObservedPath{Path: "adapter/opencode/other.ts", Added: 90}, "production", 92)
	})
	t.Run("a foreign binary path", func(t *testing.T) {
		// The other job inventories its binary fixture as a test path. For
		// the correction it is an unclassified binary: it has no text lines
		// on either side, and the foreign subtraction keeps it out of the
		// final measured set, so it makes no size unavailable.
		acceptForeignCorrection(t, "size-accept-foreign-binary", WorkerObservedPath{Path: "adapter/opencode/other.ts", Binary: true}, "test", 2)
	})
}

func acceptForeignCorrection(t *testing.T, workID string, foreignPath WorkerObservedPath, foreignClass string, reported int64) {
	t.Helper()
	fixture := sizeDispatchFixture(t, workID)
	t.Cleanup(func() { fixture.store.Close() })
	s := workerJobSizeStore(fixture)
	lane := reviewGateLane(t, "implementation")
	base := testJobSizeBaseCommit()
	fields := workerJobSizeEditingFields(t, workID, fixture)
	fields["production_size_max"] = 10
	job := &WorkerJobBinding{JobID: "job:size-correction", Revision: 1}
	recordEditingJobForTest(t, fixture, workID, fields, job)
	recordSizeDispatchAttempt(t, fixture, workID, "attempt:first", *job)

	other := workerJobSizeEditingFields(t, workID, fixture)
	other["path_scope"] = []string{"adapter/opencode"}
	other["measurement_basis"].(map[string]any)["paths"] = []map[string]any{{"path": "adapter/opencode/other.ts", "class": foreignClass}}
	other["direct_caller_allowance"].(map[string]any)["mechanisms"] = []map[string]any{{"project_id": "project", "path": "adapter/opencode/other.ts", "entry_point": "other"}}
	otherJob := &WorkerJobBinding{JobID: "job:size-other", Revision: 1}
	recordEditingJobForTest(t, fixture, workID, other, otherJob)
	complete := func(attemptID string, binding *WorkerJobBinding, tree string, count int64, paths ...WorkerObservedPath) {
		t.Helper()
		recordSizeDispatchAuthorization(t, s, workID, attemptID, *binding)
		if err := appendJobBoundWorkerDispatch(t, s, workID, attemptID, binding); err != nil {
			t.Fatalf("record implementation dispatch %s: %v", attemptID, err)
		}
		completed := reportBindingCompletion(t, s, workID, attemptID, lane, binding, func(payload *WorkerCompletedPayload) {
			payload.ObservedDelivery = &WorkerObservedDelivery{Commit: tree, State: "clean", Base: base, Paths: paths}
			payload.ProductionSize = &WorkerProductionSize{Base: base, Tree: tree, Count: count}
		})
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{completed}}); err != nil {
			t.Fatalf("record the completed report of %s: %v", attemptID, err)
		}
	}
	acceptAttempt := func(attemptID string) error {
		return runVerdictActionAs(t, s, workID, "accept_worker_result", mustJSONValue(map[string]any{"attempt_id": attemptID, "attempt_epoch": 1}), 0, reviewGateAcceptor(workID))
	}
	complete("attempt:other", otherJob, strings.Repeat("4", 40), foreignPath.Added+foreignPath.Deleted, foreignPath)
	if err := acceptAttempt("attempt:other"); err != nil {
		t.Fatalf("accept the other job's work: %v", err)
	}
	complete("attempt:correction", job, strings.Repeat("5", 40), reported,
		WorkerObservedPath{Path: "internal/store/worker_jobs.go", Added: 2}, foreignPath)
	if err := acceptAttempt("attempt:correction"); err != nil {
		t.Fatalf("the correction's acceptance counted or contained the other job's accepted path: %v", err)
	}
	var final int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.worker_production_size.count') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='accept_worker_result' AND json_extract(payload,'$.worker_attempt_id')='attempt:correction'`, workID, WorkflowActionCompleted).Scan(&final); err != nil || final != 2 {
		t.Fatalf("final accepted count = %d (%v), want 2 without the other job's accepted path", final, err)
	}
}

func TestWorkerJobSizeAcceptanceRetainsDispatchCallerExclusions(t *testing.T) {
	const workID = "size-accept-dispatch-exclusions"
	const caller = "adapter/opencode/caller.ts"
	f, dir, base := sizeJourneyFixture(t, workID)
	route := workflowReturnRouteFixture{store: f.store, owner: f.owner}
	blockedFields := workerJobSizeEditingFields(t, workID, route)
	blockedFields["path_scope"] = []string{"adapter/opencode"}
	blockedFields["measurement_basis"].(map[string]any)["base_commit"] = base
	blockedFields["measurement_basis"].(map[string]any)["paths"] = []map[string]any{{"path": caller, "class": "production"}}
	blocked := &WorkerJobBinding{JobID: "job:caller-correction", Revision: 1}
	recordEditingJobForTest(t, route, workID, blockedFields, blocked)
	recordSizeDispatchAttempt(t, route, workID, "attempt:caller-correction", *blocked)
	fields := workerJobSizeEditingFields(t, workID, route)
	fields["measurement_basis"].(map[string]any)["base_commit"] = base
	job := &WorkerJobBinding{JobID: "job:caller-owner", Revision: 1}
	recordEditingJobForTest(t, route, workID, fields, job)
	const attemptID = "attempt:caller-owner"
	if err := sizeJourneyDispatch(t, f, dir, attemptID, *job, []string{caller}); err != nil {
		t.Fatalf("dispatch with the caller exclusions: %v", err)
	}
	if err := appendJobBoundWorkerDispatch(t, f.store, workID, attemptID, job); err != nil {
		t.Fatal(err)
	}
	completed := reportBindingCompletion(t, f.store, workID, attemptID, reviewGateLane(t, "implementation"), job, func(payload *WorkerCompletedPayload) {
		// The caller repair delivers a real changed tree: a clean delivery
		// still at its dispatch subject is the no-op result the acceptance
		// refuses before any exclusion check.
		tree := strings.Repeat("6", 40)
		payload.ObservedDelivery = &WorkerObservedDelivery{Commit: tree, Base: base, State: "clean", Paths: []WorkerObservedPath{{Path: caller, Added: 1}}}
		payload.ProductionSize = &WorkerProductionSize{Base: base, Tree: tree, Count: 1}
		payload.DirectCallerRepairs = []WorkerDirectCallerRepair{{
			Owner:  WorkerCallerMechanism{Path: "internal/store/worker_jobs.go", EntryPoint: "foldWorkerJobRecorded"},
			Caller: WorkerCallerMechanism{Path: caller, EntryPoint: "callOwner"}, Edge: "calls the changed owner", Exposing: "the direct call fixture",
			ChangedPaths: []string{caller}, Checks: WorkerCallerRepairChecks{
				BaselinePass: WorkerCallerRepairCheck{Observed: "baseline check passes"}, OwnerOnlyFail: WorkerCallerRepairCheck{Observed: "owner-only compile check fails"}, CombinedPass: WorkerCallerRepairCheck{Observed: "combined check passes"},
			},
		}}
	})
	if err := ApplyOperation(context.Background(), f.store, Operation{Events: []Event{completed}}); err != nil {
		t.Fatal(err)
	}
	err := runVerdictActionAs(t, f.store, workID, "accept_worker_result", mustJSONValue(map[string]any{"attempt_id": attemptID, "attempt_epoch": latestStepStartEpoch(t, f.store, workID, "repair")}), 0, reviewGateAcceptor(workID))
	if err == nil || !strings.Contains(err.Error(), "which its dispatch excluded as another correction's measured path") {
		t.Fatalf("excluded caller repair acceptance = %v, want the dispatch-bound exclusion refusal", err)
	}
	requireNoAcceptance(t, sizeAcceptance{s: f.store, workID: workID, attemptID: attemptID})
}

func TestWorkerJobSizeCompletionPayloadVersionBoundary(t *testing.T) {
	for _, member := range []string{"worker_caller_exclusions", "supplemental_classifications", "worker_production_size"} {
		t.Run(member, func(t *testing.T) {
			const workID = "size-completion-boundary"
			f := sizeDispatchFixture(t, workID)
			defer f.store.Close()
			version := readWorkVersion(t, f.store, workID)
			fields := map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1,
				"step_id": "repair", "attempt_epoch": 1, "worker_job": WorkerJobBinding{JobID: "job:boundary", Revision: 1, Digest: "sha256:" + strings.Repeat("a", 64)}}
			switch member {
			case "worker_caller_exclusions":
				fields["action_id"] = "dispatch_worker"
				fields[member] = []string{"caller.go"}
			case "supplemental_classifications":
				fields["action_id"] = "accept_worker_result"
				fields[member] = []WorkerJobMeasuredPath{{Path: "caller_test.go", Class: "test"}}
			default:
				fields["action_id"] = "accept_worker_result"
				fields[member] = WorkerProductionSize{Base: testJobSizeBaseCommit(), Tree: strings.Repeat("3", 40), Count: 1}
			}
			actor, err := WorkflowActorRef(f.owner)
			if err != nil {
				t.Fatal(err)
			}
			event := workflowEventWithActor("size-completion-boundary", WorkflowActionCompleted, workID, actor, fields)
			event.PayloadVersion = 5
			err = applyWorkflowTestOperation(context.Background(), f.store, Operation{Events: []Event{event}})
			var failure *Failure
			if !errors.As(err, &failure) || failure.Kind != KindInvalidPayload || !strings.Contains(failure.Detail, "reserved for payload version >= 6") {
				t.Fatalf("v5 completion carrying %s = %v, want source-version refusal", member, err)
			}
			if got := readWorkVersion(t, f.store, workID); got != version {
				t.Fatalf("refused completion advanced version from %d to %d", version, got)
			}
		})
	}
}

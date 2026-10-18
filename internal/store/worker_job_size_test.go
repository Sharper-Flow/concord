package store

import (
	"context"
	"strings"
	"testing"
)

// testJobSizeBaseCommit is the fixture base commit every measurement basis in
// this file pins: a raw 40-hex oid, the shape worktreeSHAPattern admits.
func testJobSizeBaseCommit() string {
	return strings.Repeat("a", 40)
}

// The gate reads the declared record_worker_job member: the size-capable
// heads declare production_size_max, the released predecessors do not.
func TestWorkflowJobSizeGateFollowsTheDeclaredMember(t *testing.T) {
	t.Parallel()
	active := []WorkflowDefinition{
		mustCompileWorkflowComposition(implementationCompositionV34()),
		mustCompileWorkflowComposition(breakFixCompositionV31()),
	}
	for _, definition := range active {
		if !workflowJobSizeActive(definition) {
			t.Fatalf("%s v%d does not activate the size gate", definition.Ref, definition.Version)
		}
	}
	inactive := []WorkflowDefinition{
		mustCompileWorkflowComposition(implementationCompositionV33()),
		mustCompileWorkflowComposition(breakFixCompositionV30()),
		mustCompileWorkflowComposition(researchCompositionV20()),
	}
	for _, definition := range inactive {
		if workflowJobSizeActive(definition) {
			t.Fatalf("%s v%d activated the size gate", definition.Ref, definition.Version)
		}
	}
}

// A payload without the members serializes to exactly the pre-change digest
// bytes, and carrying the members moves the digest: absence replays as
// absence, presence binds content.
func TestWorkerJobSizeMembersPreserveHistoricalDigest(t *testing.T) {
	t.Parallel()
	payload := WorkerJobRecordedPayload{
		JobID: "job:size-digest", Revision: 1, ContractVersion: 1,
		Objective: "measure the bounded delta", StoppingCondition: "the counted delta stays within the cap",
		ProjectScope: "project",
		PathScope:    []string{"internal/store"}, PredicateIDs: []string{}, Checks: []string{"go test ./internal/store/"},
		Prerequisites: []WorkerJobPrerequisite{}, UnresolvedRefs: []string{}, ReservedIntegration: "reserved",
		Readiness: &WorkerJobReadiness{Ready: true, Evidence: []string{"evidence:coordinator-ready"}},
	}
	if payload.ProductionSizeMax != nil || payload.MeasurementBasis != nil || payload.DirectCallerAllowance != nil {
		t.Fatal("a member-less payload carries size members")
	}
	const want = "sha256:fa864008ca9452c750ac0442f0e5613129d19b3df7237d6c18cfe5c73fc20667"
	if got := DeriveWorkerJobDigest(payload); got != want {
		t.Fatalf("member-less digest = %s, want the pinned pre-change derivation %s", got, want)
	}
	sizeCap := int64(400)
	payload.ProductionSizeMax = &sizeCap
	payload.MeasurementBasis = &WorkerJobMeasurementBasis{BaseCommit: testJobSizeBaseCommit(), Paths: []WorkerJobMeasuredPath{{Path: "internal/store/worker_jobs.go", Class: "production"}}}
	payload.DirectCallerAllowance = &WorkerJobCallerAllowance{ProjectID: "project", Mechanisms: []OracleMechanism{{ProjectID: "project", Path: "internal/store/worker_jobs.go", EntryPoint: "foldWorkerJobRecorded"}}, DomainIDs: []string{"root"}}
	if got := DeriveWorkerJobDigest(payload); got == want {
		t.Fatal("a size-carrying payload kept the member-less digest")
	}
}

// workerJobSizeFoldPayload authors one full size-carrying payload under the
// fixture's contract, with the caller's mutation applied before the digest
// is derived, so each case lands on the fold's own refusals.
func workerJobSizeFoldPayload(jobID string, mutate func(payload *WorkerJobRecordedPayload)) WorkerJobRecordedPayload {
	sizeCap := int64(400)
	payload := WorkerJobRecordedPayload{
		JobID: jobID, Revision: 1, ContractVersion: 1, ProjectScope: "project",
		Objective: "measure the bounded delta", StoppingCondition: "the counted delta stays within the cap",
		PathScope: []string{"internal/store"}, PredicateIDs: []string{}, Checks: []string{},
		Prerequisites: []WorkerJobPrerequisite{}, UnresolvedRefs: []string{},
		Readiness:             &WorkerJobReadiness{Ready: false},
		ProductionSizeMax:     &sizeCap,
		MeasurementBasis:      &WorkerJobMeasurementBasis{BaseCommit: testJobSizeBaseCommit(), Paths: []WorkerJobMeasuredPath{{Path: "internal/store/worker_jobs.go", Class: "production"}}},
		DirectCallerAllowance: &WorkerJobCallerAllowance{ProjectID: "project", Mechanisms: []OracleMechanism{{ProjectID: "project", Path: "internal/store/worker_jobs.go", EntryPoint: "foldWorkerJobRecorded"}}, DomainIDs: []string{"root"}},
	}
	if mutate != nil {
		mutate(&payload)
	}
	payload.Digest = DeriveWorkerJobDigest(payload)
	return payload
}

// The fold re-proves the size members' closed shape from the retained
// payload alone: the trio is present together or absent together, the cap is
// bounded, and the allowance binds the job's own Project scope.
func TestWorkerJobSizeFoldShapeValidation(t *testing.T) {
	const workID = "worker-job-size-fold-shape"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	t.Run("editing job folds", func(t *testing.T) {
		if err := applyWorkerJobEventForTest(t, s, workID, workerJobSizeFoldPayload("job:fold-editing", nil)); err != nil {
			t.Fatalf("editing payload refused: %v", err)
		}
		views, err := readWorkerJobRevisions(context.Background(), s.db, workID)
		if err != nil || len(views) != 1 || views[0].Binding.JobID != "job:fold-editing" {
			t.Fatalf("recorded views = %#v, error = %v", views, err)
		}
	})
	t.Run("read-only job folds", func(t *testing.T) {
		payload := workerJobSizeFoldPayload("job:fold-read-only", func(payload *WorkerJobRecordedPayload) {
			payload.ProductionSizeMax, payload.MeasurementBasis, payload.DirectCallerAllowance = nil, nil, nil
		})
		if err := applyWorkerJobEventForTest(t, s, workID, payload); err != nil {
			t.Fatalf("read-only payload refused: %v", err)
		}
	})
	for _, refusal := range []struct {
		name   string
		mutate func(payload *WorkerJobRecordedPayload)
		want   string
	}{
		{"partial presence", func(payload *WorkerJobRecordedPayload) { payload.DirectCallerAllowance = nil }, "present together or absent together"},
		{"cap below the floor", func(payload *WorkerJobRecordedPayload) { *payload.ProductionSizeMax = 0 }, "outside 1 to"},
		{"cap above the ceiling", func(payload *WorkerJobRecordedPayload) { *payload.ProductionSizeMax = 2147483648 }, "outside 1 to"},
		{"allowance project mismatch", func(payload *WorkerJobRecordedPayload) { payload.DirectCallerAllowance.ProjectID = "project-other" }, "does not equal the recorded project_scope"},
		{"generated without generator_ref", func(payload *WorkerJobRecordedPayload) {
			payload.MeasurementBasis.Paths[0].Class = "generated"
		}, "classifies generated without its owning generator_ref"},
		{"generated ref outside the classification", func(payload *WorkerJobRecordedPayload) {
			payload.MeasurementBasis.Paths[0].GeneratorRef = "scripts/generate-agent-contracts.py"
		}, "generator_ref outside the generated classification"},
		{"base commit is not a raw oid", func(payload *WorkerJobRecordedPayload) { payload.MeasurementBasis.BaseCommit = "HEAD" }, "must be a raw git commit oid"},
	} {
		t.Run(refusal.name, func(t *testing.T) {
			payload := workerJobSizeFoldPayload("job:fold-"+strings.ReplaceAll(refusal.name, " ", "-"), refusal.mutate)
			err := applyWorkerJobEventForTest(t, s, workID, payload)
			if err == nil || !strings.Contains(err.Error(), refusal.want) {
				t.Fatalf("fold refusal %q error = %v, want text %q", refusal.name, err, refusal.want)
			}
		})
	}
}

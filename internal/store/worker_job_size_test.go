package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// testJobSizeBaseCommit is the fixture base commit every measurement basis in
// this file pins: a raw 40-hex oid, the shape worktreeSHAPattern admits.
func testJobSizeBaseCommit() string {
	return strings.Repeat("a", 40)
}

// workerJobSizeStore unwraps the fixture's store.
func workerJobSizeStore(fixture workflowReturnRouteFixture) *Store { return fixture.store }

// workerJobSizeFixture seeds one work item on the family's current registered
// head — the size-capable pin this slice registers — with the oracle subject
// the shared oracle fixture needs. The generic return-route helper pins the
// legacy approval policy, whose catalog does not declare the size members.
func workerJobSizeFixture(t *testing.T, workID string) workflowReturnRouteFixture {
	t.Helper()
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	fixture := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, registered, "repair",
		[]string{"verification"}, []string{"verification", "review", "artifact"})
	bootstrapOracleFixtureSubject(t, workerJobSizeStore(fixture), workID)
	return fixture
}

// workerJobSizeEditingFields authors one editing job: the oracle the
// oracle-capable pin requires beside all three size members.
func workerJobSizeEditingFields(t *testing.T, workID string, fixture workflowReturnRouteFixture) map[string]any {
	t.Helper()
	return map[string]any{
		"job_id": "job:size", "objective": "measure the bounded delta",
		"stopping_condition":   "the counted delta stays within the cap",
		"path_scope":           []string{"internal/store"},
		"checks":               []string{"go test ./internal/store/"},
		"reserved_integration": "reserved",
		"ready":                true, "readiness_evidence": []string{"evidence:coordinator-ready"},
		"acceptance_oracle":   acceptanceOracleFieldsForTest(t, workerJobSizeStore(fixture), workID),
		"production_size_max": 400,
		"measurement_basis": map[string]any{
			"base_commit": testJobSizeBaseCommit(),
			"paths":       []map[string]any{{"path": "internal/store/worker_jobs.go", "class": "production"}},
		},
		"direct_caller_allowance": map[string]any{
			"project_id":   "project",
			"mechanisms":   []map[string]any{{"project_id": "project", "path": "internal/store/worker_jobs.go", "entry_point": "foldWorkerJobRecorded"}},
			"domain_ids":   []string{"root"},
			"law_bindings": []map[string]any{},
		},
	}
}

// workerJobSizeReadOnlyFields authors one read-only job: no size members.
func workerJobSizeReadOnlyFields(t *testing.T, workID string, fixture workflowReturnRouteFixture) map[string]any {
	t.Helper()
	fields := workerJobSizeEditingFields(t, workID, fixture)
	for _, member := range []string{"production_size_max", "measurement_basis", "direct_caller_allowance"} {
		delete(fields, member)
	}
	fields["objective"] = "read the recorded state"
	return fields
}

// seedAllowanceAuthorityFixture pins one law revision the allowance can bind
// and seeds one more current Domain outside the contract's affected scope, so
// the join refusals name real mismatches.
func seedAllowanceAuthorityFixture(t *testing.T, fixture workflowReturnRouteFixture, workID string) {
	t.Helper()
	lawHash := "sha256:" + strings.Repeat("a", 64)
	if _, err := workerJobSizeStore(fixture).DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		INSERT INTO workflow_contract_law_revisions(work_id,contract_version,law_id,content_hash) VALUES(?,1,'spec:allowance',?);
		DELETE FROM fold_guard`, workID, lawHash); err != nil {
		t.Fatalf("seed pinned law revision: %v", err)
	}
	var registryHash string
	if err := workerJobSizeStore(fixture).DatabaseForTesting().QueryRow(`SELECT content_hash FROM domain_registries WHERE product_id='product'`).Scan(&registryHash); err != nil {
		t.Fatalf("read registry hash: %v", err)
	}
	if _, err := workerJobSizeStore(fixture).DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,status,registry_content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','product','edge','Edge','outside the affected scope','current',?,'test');
		DELETE FROM fold_guard`, registryHash); err != nil {
		t.Fatalf("seed outside-scope Domain: %v", err)
	}
}

// The gate reads the declared record_worker_job member: the size-capable
// heads declare production_size_max, the released predecessors do not.
func TestWorkflowJobSizeGateFollowsTheDeclaredMember(t *testing.T) {
	t.Parallel()
	active := []WorkflowDefinition{
		mustCompileWorkflowComposition(implementationCompositionV36()),
		mustCompileWorkflowComposition(breakFixCompositionV33()),
	}
	for _, definition := range active {
		if !workflowJobSizeActive(definition) {
			t.Fatalf("%s v%d does not activate the size gate", definition.Ref, definition.Version)
		}
	}
	inactive := []WorkflowDefinition{
		mustCompileWorkflowComposition(implementationCompositionV35()),
		mustCompileWorkflowComposition(breakFixCompositionV32()),
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

// A size-capable pin records an editing job with all three members and a
// read-only job with none.
func TestWorkerJobSizeAuthoringAdmitsEditingAndReadOnlyJobs(t *testing.T) {
	const workID = "worker-job-size-authoring"
	fixture := workerJobSizeFixture(t, workID)
	editing := workerJobSizeEditingFields(t, workID, fixture)
	editing["job_id"] = "job:editing"
	if err := recordWorkerJobActionForTest(t, workerJobSizeStore(fixture), workID, fixture.owner, editing); err != nil {
		t.Fatalf("editing job refused: %v", err)
	}
	readOnly := workerJobSizeReadOnlyFields(t, workID, fixture)
	readOnly["job_id"] = "job:read-only"
	if err := recordWorkerJobActionForTest(t, workerJobSizeStore(fixture), workID, fixture.owner, readOnly); err != nil {
		t.Fatalf("read-only job refused: %v", err)
	}
	views, err := readWorkerJobRevisions(context.Background(), workerJobSizeStore(fixture).db, workID)
	if err != nil || len(views) != 2 {
		t.Fatalf("recorded views = %#v, error = %v", views, err)
	}
}

// A pin without the declared members refuses every size member through the
// pinned-definition preflight: an undeclared member is not one the pinned
// definition admits.
func TestWorkerJobSizeGateInactivePinRefusesMembers(t *testing.T) {
	const workID = "worker-job-size-inactive-pin"
	fixture := seedHistoricalWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", 30, "repair")
	bootstrapOracleFixtureSubject(t, workerJobSizeStore(fixture), workID)
	fields := workerJobSizeEditingFields(t, workID, fixture)
	err := recordWorkerJobActionForTest(t, workerJobSizeStore(fixture), workID, fixture.owner, fields)
	if err == nil || !strings.Contains(err.Error(), `"production_size_max" is not declared for action "record_worker_job"`) {
		t.Fatalf("inactive-pin size members error = %v, want the pinned-definition refusal", err)
	}
}

// Partial presence refuses: an editing job carries all three members, a
// read-only job none.
func TestWorkerJobSizePartialPresenceRefuses(t *testing.T) {
	const workID = "worker-job-size-partial"
	fixture := workerJobSizeFixture(t, workID)
	for _, partial := range []struct {
		name   string
		mutate func(fields map[string]any)
	}{
		{"cap-only", func(fields map[string]any) {
			delete(fields, "measurement_basis")
			delete(fields, "direct_caller_allowance")
		}},
		{"basis-only", func(fields map[string]any) {
			delete(fields, "production_size_max")
			delete(fields, "direct_caller_allowance")
		}},
		{"cap-and-basis", func(fields map[string]any) { delete(fields, "direct_caller_allowance") }},
		{"cap-and-allowance", func(fields map[string]any) { delete(fields, "measurement_basis") }},
	} {
		t.Run(partial.name, func(t *testing.T) {
			fields := workerJobSizeEditingFields(t, workID, fixture)
			fields["job_id"] = "job:partial-" + partial.name
			partial.mutate(fields)
			err := recordWorkerJobActionForTest(t, workerJobSizeStore(fixture), workID, fixture.owner, fields)
			if err == nil || !strings.Contains(err.Error(), "present together or absent together") {
				t.Fatalf("partial presence %q error = %v, want the closed-trio refusal", partial.name, err)
			}
		})
	}
}

// Every inventory path is contained by path_scope: equal to an entry or under
// an entry with a trailing slash.
func TestWorkerJobSizeUncontainedInventoryPathRefuses(t *testing.T) {
	const workID = "worker-job-size-containment"
	fixture := workerJobSizeFixture(t, workID)
	fields := workerJobSizeEditingFields(t, workID, fixture)
	fields["job_id"] = "job:uncontained"
	fields["measurement_basis"].(map[string]any)["paths"] = []map[string]any{
		{"path": "adapter/opencode/packet.ts", "class": "production"},
	}
	err := recordWorkerJobActionForTest(t, workerJobSizeStore(fixture), workID, fixture.owner, fields)
	if err == nil || !strings.Contains(err.Error(), "outside the job's declared path_scope") {
		t.Fatalf("uncontained inventory path error = %v, want the containment refusal", err)
	}
}

// An inventory path that is not a normalized relative path refuses on the
// author route with an author recovery, not a worker-event repair.
func TestWorkerJobSizeUnnormalizedInventoryPathRefusesAtAuthor(t *testing.T) {
	const workID = "worker-job-size-unnormalized"
	fixture := workerJobSizeFixture(t, workID)
	fields := workerJobSizeEditingFields(t, workID, fixture)
	fields["job_id"] = "job:unnormalized"
	fields["measurement_basis"].(map[string]any)["paths"] = []map[string]any{
		{"path": "internal/store/../store/worker_jobs.go", "class": "production"},
	}
	err := recordWorkerJobActionForTest(t, workerJobSizeStore(fixture), workID, fixture.owner, fields)
	var failure *Failure
	if !errors.As(err, &failure) || !strings.Contains(failure.Detail, "must be normalized relative paths") || failure.RecoveryAction != "supply a normalized relative repository path" {
		t.Fatalf("unnormalized inventory path error = %v, want the author-route normalization refusal", err)
	}
}

// The generated classification requires its owning generator reference, and
// only the generated classification carries one.
func TestWorkerJobSizeGeneratedClassRequiresGeneratorRef(t *testing.T) {
	const workID = "worker-job-size-generated"
	fixture := workerJobSizeFixture(t, workID)
	t.Run("generated without generator_ref refuses", func(t *testing.T) {
		fields := workerJobSizeEditingFields(t, workID, fixture)
		fields["job_id"] = "job:generated-naked"
		fields["measurement_basis"].(map[string]any)["paths"] = []map[string]any{
			{"path": "internal/store/worker_jobs.go", "class": "generated"},
		}
		err := recordWorkerJobActionForTest(t, workerJobSizeStore(fixture), workID, fixture.owner, fields)
		if err == nil || !strings.Contains(err.Error(), "classifies generated without its owning generator_ref") {
			t.Fatalf("naked generated path error = %v, want the generator refusal", err)
		}
	})
	t.Run("generated with generator_ref admits", func(t *testing.T) {
		fields := workerJobSizeEditingFields(t, workID, fixture)
		fields["job_id"] = "job:generated-owned"
		fields["measurement_basis"].(map[string]any)["paths"] = []map[string]any{
			{"path": "internal/store/worker_jobs.go", "class": "generated", "generator_ref": "scripts/generate-agent-contracts.py"},
		}
		if err := recordWorkerJobActionForTest(t, workerJobSizeStore(fixture), workID, fixture.owner, fields); err != nil {
			t.Fatalf("owned generated path refused: %v", err)
		}
	})
	t.Run("non-generated class with generator_ref refuses", func(t *testing.T) {
		fields := workerJobSizeEditingFields(t, workID, fixture)
		fields["job_id"] = "job:generated-misattributed"
		fields["measurement_basis"].(map[string]any)["paths"] = []map[string]any{
			{"path": "internal/store/worker_jobs.go", "class": "production", "generator_ref": "scripts/generate-agent-contracts.py"},
		}
		err := recordWorkerJobActionForTest(t, workerJobSizeStore(fixture), workID, fixture.owner, fields)
		if err == nil || !strings.Contains(err.Error(), "generator_ref outside the generated classification") {
			t.Fatalf("misattributed generator_ref error = %v, want the classification refusal", err)
		}
	})
}

// The allowance joins the job's Project scope and the active contract's
// Domain and law boundaries exactly as the oracle joins them.
func TestWorkerJobSizeAllowanceJoins(t *testing.T) {
	const workID = "worker-job-size-allowance-joins"
	fixture := workerJobSizeFixture(t, workID)
	seedAllowanceAuthorityFixture(t, fixture, workID)
	base := func() map[string]any {
		fields := workerJobSizeEditingFields(t, workID, fixture)
		fields["job_id"] = "job:allowance-join"
		fields["direct_caller_allowance"].(map[string]any)["law_bindings"] = []map[string]any{
			{"source": map[string]any{"kind": "knowledge", "source_id": "source:allowance", "law_id": "spec:allowance", "content_hash": "sha256:" + strings.Repeat("a", 64)}, "clause": "the caller repair preserves approved behavior"},
		}
		return fields
	}
	if err := recordWorkerJobActionForTest(t, workerJobSizeStore(fixture), workID, fixture.owner, base()); err != nil {
		t.Fatalf("fully joined allowance refused: %v", err)
	}
	for _, refusal := range []struct {
		name     string
		mutation func(fields map[string]any)
		wantText string
	}{
		{"project mismatch", func(fields map[string]any) {
			fields["direct_caller_allowance"].(map[string]any)["project_id"] = "project-other"
		}, "does not equal the recorded project_scope"},
		{"mechanism in another Project", func(fields map[string]any) {
			fields["direct_caller_allowance"].(map[string]any)["mechanisms"].([]map[string]any)[0]["project_id"] = "project-other"
		}, "is not the allowance's permitted Project"},
		{"domain outside the contract", func(fields map[string]any) {
			fields["direct_caller_allowance"].(map[string]any)["domain_ids"] = []string{"edge"}
		}, "direct-caller allowance Domain edge is outside the approved affected Domain scope"},
		{"unpinned law", func(fields map[string]any) {
			fields["direct_caller_allowance"].(map[string]any)["law_bindings"].([]map[string]any)[0]["source"].(map[string]any)["law_id"] = "spec:never-pinned"
		}, "did not pin"},
	} {
		t.Run(refusal.name, func(t *testing.T) {
			fields := base()
			fields["job_id"] = "job:allowance-" + strings.ReplaceAll(refusal.name, " ", "-")
			refusal.mutation(fields)
			err := recordWorkerJobActionForTest(t, workerJobSizeStore(fixture), workID, fixture.owner, fields)
			if err == nil || !strings.Contains(err.Error(), refusal.wantText) {
				t.Fatalf("allowance refusal %q error = %v, want text %q", refusal.name, err, refusal.wantText)
			}
		})
	}
}

// A later revision of a capped job cannot raise the cap, cannot reclassify a
// production path away, cannot go uncapped, and admits an equal or lower cap.
func TestWorkerJobSizeCapInheritance(t *testing.T) {
	const workID = "worker-job-size-cap-inheritance"
	const jobID = "job:inheritance"
	fixture := workerJobSizeFixture(t, workID)
	twoPaths := []map[string]any{
		{"path": "internal/store/worker_jobs.go", "class": "production"},
		{"path": "internal/store/worker_oracle.go", "class": "test"},
	}
	first := workerJobSizeEditingFields(t, workID, fixture)
	first["job_id"] = jobID
	first["production_size_max"] = 400
	first["measurement_basis"].(map[string]any)["paths"] = twoPaths
	if err := recordWorkerJobActionForTest(t, workerJobSizeStore(fixture), workID, fixture.owner, first); err != nil {
		t.Fatalf("first capped revision refused: %v", err)
	}
	next := func(sizeCap int64, mutate func(fields map[string]any)) error {
		fields := workerJobSizeEditingFields(t, workID, fixture)
		fields["job_id"] = jobID
		fields["production_size_max"] = sizeCap
		fields["measurement_basis"].(map[string]any)["paths"] = twoPaths
		if mutate != nil {
			mutate(fields)
		}
		return recordWorkerJobActionForTest(t, workerJobSizeStore(fixture), workID, fixture.owner, fields)
	}
	if err := next(500, nil); err == nil || !strings.Contains(err.Error(), "raises the job's lowest recorded cap 400") {
		t.Fatalf("cap raise error = %v, want the raise refusal", err)
	}
	if err := next(400, func(fields map[string]any) {
		fields["measurement_basis"].(map[string]any)["paths"] = []map[string]any{
			{"path": "internal/store/worker_jobs.go", "class": "test"},
			{"path": "internal/store/worker_oracle.go", "class": "test"},
		}
	}); err == nil || !strings.Contains(err.Error(), "reclassifies measured path internal/store/worker_jobs.go away from an earlier revision's production classification") {
		t.Fatalf("production reclassification error = %v, want the reclassification refusal", err)
	}
	uncapped := workerJobSizeReadOnlyFields(t, workID, fixture)
	uncapped["job_id"] = jobID
	if err := recordWorkerJobActionForTest(t, workerJobSizeStore(fixture), workID, fixture.owner, uncapped); err == nil || !strings.Contains(err.Error(), "capped job must stay capped") {
		t.Fatalf("uncapped successor error = %v, want the must-stay-capped refusal", err)
	}
	if err := next(300, nil); err != nil {
		t.Fatalf("lower cap refused: %v", err)
	}
	if err := next(400, nil); err == nil || !strings.Contains(err.Error(), "raises the job's lowest recorded cap 300") {
		t.Fatalf("raise back above the lowest cap error = %v, want the raise refusal", err)
	}
	if err := next(300, nil); err != nil {
		t.Fatalf("equal cap refused: %v", err)
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
		DirectCallerAllowance: &WorkerJobCallerAllowance{ProjectID: "project", Mechanisms: []OracleMechanism{{ProjectID: "project", Path: "internal/store/worker_jobs.go", EntryPoint: "foldWorkerJobRecorded"}}, DomainIDs: []string{"root"}, LawBindings: []OracleLawBinding{}},
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
		{"absent law_bindings", func(payload *WorkerJobRecordedPayload) { payload.DirectCallerAllowance.LawBindings = nil }, "law_bindings must be present"},
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

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
)

// The architecture job-binding tests: a new pin whose closed record_worker_job
// declaration requires architecture_binding records the design-and-slice
// identity on the immutable job content, derives its Project scope, file
// scope, and check controls from that exact slice, and carries the binding
// through the digest, the event, the view, and the packet. Capability comes
// from the pinned closed required declaration alone: neither design presence,
// a loose name, nor a version grants it, an old pin keeps its binding-free
// shape under an explicit member, and a stale prospective job loses readiness
// without history moving. No readiness is fabricated from the binding.

const (
	// jobBindingPinRef names the synthetic architecture-job-capable pin. Its
	// closed record_worker_job payload requires architecture_binding, the
	// declared capability source this store surface admits.
	jobBindingPinRef = "workflow.test_job_binding"
	// jobBindingLooseRef names the synthetic pin whose record_worker_job
	// names architecture_binding without the closed required object
	// declaration the capability source demands.
	jobBindingLooseRef = "workflow.test_job_binding_loose"
)

// jobBindingSliceFiles is the edit slice's exact file set, and so the exact
// derived scope the recorded job must carry as a set.
var jobBindingSliceFiles = []string{"internal/store/worker_job_architecture.go", "internal/store/worker_jobs.go"}

// jobBindingDefinition clones the released implementation head and, for the
// capable pin, adds architecture_binding to the closed record_worker_job
// declaration: the field the pinned declaration requires, not a policy or a
// discriminator. The ref stays outside the released range so the synthetic
// head can never publish. The declared fields are copied first, so the
// registration never aliases the shared builtin policy slice.
func jobBindingDefinition(t *testing.T, ref string, withBindingField, required bool) RegisteredDefinition {
	t.Helper()
	base, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 30)
	if !ok {
		t.Fatal("the released implementation head is not registered")
	}
	shape := workflowFixtureShape(base.Definition, 30)
	shape.Ref = ref
	if withBindingField {
		for i := range shape.ActionDefinitions {
			if shape.ActionDefinitions[i].ID != "record_worker_job" {
				continue
			}
			fields := make([]WorkflowPayloadField, 0, len(shape.ActionDefinitions[i].Payload.Fields)+1)
			fields = append(fields, shape.ActionDefinitions[i].Payload.Fields...)
			fields = append(fields, WorkflowPayloadField{Name: "architecture_binding", ValueType: PayloadObject, Required: required})
			shape.ActionDefinitions[i].Payload.Fields = fields
		}
	}
	registered, err := BuiltinWorkflowRegistry().Register(shape)
	if err != nil {
		t.Fatal(err)
	}
	return registered
}

// jobBindingArchitectureFixture returns one closed, valid plan whose edit
// slice owns the binding surface: the owner's Project, the slice's exact file
// set, and one declared test control the acceptance-oracle fixture controls
// resolve.
func jobBindingArchitectureFixture(ownerProject string) WorkflowDesignArchitecture {
	return WorkflowDesignArchitecture{
		Owners: []WorkflowDesignArchitectureOwner{{
			OwnerID:   "job-binding-owner",
			ProjectID: ownerProject,
			Path:      "internal/store/worker_jobs.go",
			Symbol:    "foldWorkerJobRecorded",
		}},
		Invariants: []WorkflowDesignArchitectureInvariant{{
			InvariantID: "bound-jobs",
			OwnerID:     "job-binding-owner",
			Statement:   "every editing job binds one design revision and one slice",
		}},
		DataFlow: []WorkflowDesignArchitectureDataFlow{{
			FlowID:       "record-then-dispatch",
			FromOwnerID:  "job-binding-owner",
			ToOwnerID:    "job-binding-owner",
			Value:        "a bound revision dispatches only under its recorded digest",
			InvariantIDs: []string{"bound-jobs"},
		}},
		FailureBehavior: []WorkflowDesignArchitectureFailureBehavior{{
			FailureID: "unbound-recording-refusal",
			OwnerID:   "job-binding-owner",
			Condition: "a recording omits the binding member",
			Result:    "the recording refuses whole and records nothing",
			Preserves: []string{"bound-jobs"},
		}},
		References: []WorkflowDesignArchitectureReference{{
			ReferenceID: "jobs-reference",
			Source: WorkContextReadingSource{
				Kind:      WorkContextSourceRepositoryFile,
				ProjectID: ownerProject,
				Path:      "internal/store/worker_jobs.go",
				CommitOID: strings.Repeat("a", 40),
			},
			StartLine: 14,
			EndLine:   120,
		}},
		Files: []WorkflowDesignArchitectureFile{
			{ProjectID: ownerProject, Path: "internal/store/worker_jobs.go", Role: DesignArchitectureFileRoleSource},
			{ProjectID: ownerProject, Path: "internal/store/worker_job_architecture.go", Role: DesignArchitectureFileRoleSource},
		},
		UnchangedFiles: []WorkflowDesignArchitectureUnchangedFile{
			{ProjectID: ownerProject, Path: "internal/store/worker_oracle.go"},
		},
		Slices: []WorkflowDesignArchitectureSlice{
			{
				SliceID:         "binding-edit",
				Kind:            DesignArchitectureSliceKindEdit,
				Ordinal:         0,
				OwnerID:         "job-binding-owner",
				Files:           jobBindingSliceFiles,
				InvariantIDs:    []string{"bound-jobs"},
				ReferenceIDs:    []string{"jobs-reference"},
				Tests:           []string{"control:fixture"},
				EndState:        "the binding behavior tests pass",
				MaxChangedLines: 450,
			},
			{
				SliceID:         "binding-integration",
				Kind:            DesignArchitectureSliceKindIntegration,
				Ordinal:         1,
				OwnerID:         "job-binding-owner",
				Files:           []string{},
				InvariantIDs:    []string{"bound-jobs"},
				ReferenceIDs:    []string{"jobs-reference"},
				Tests:           []string{"control:fixture"},
				EndState:        "the whole work integrates at the anchor",
				MaxChangedLines: 1,
			},
		},
		Capacity: &WorkflowDesignArchitectureCapacity{
			DesignRecord: 10240, WorkerJob: 10240, WorkContext: 4096, LawContext: 1024,
			ProposalRecord: 512, WorkRecord: 512, TaskPremise: 4096, OutcomePredicates: 1024,
		},
	}
}

type jobBindingFixture struct {
	store    *Store
	owner    WorkflowActor
	ownerRef string
	revision int64
	digest   string
}

// seedJobBindingFixtureWithDefinition walks the given synthetic pin to its
// record_worker_job step with the contract approved and the evidence bound,
// then records the architecture design at the reached version, so the current
// canonical design identity is available to bind. A non-nil hook reshapes the
// seeded plan before it records.
func seedJobBindingFixtureWithDefinition(t *testing.T, workID string, registered RegisteredDefinition, withDesign bool, mutatePlan func(*WorkflowDesignArchitecture)) jobBindingFixture {
	t.Helper()
	ctx := context.Background()
	step := jobBindingStepForTest(t, registered.Definition)
	route := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, registered, step, []string{"verification"}, []string{"verification", "review", "artifact"})
	fixture := jobBindingFixture{store: route.store, owner: route.owner}
	ownerRef, err := WorkflowActorRef(route.owner)
	if err != nil {
		t.Fatal(err)
	}
	fixture.ownerRef = ownerRef
	if withDesign {
		plan := jobBindingArchitectureFixture("project")
		if mutatePlan != nil {
			mutatePlan(&plan)
		}
		version := readWorkVersion(t, route.store, workID)
		if err := applyProjectionEvents(t, route.store, workID, version, projectionDesignEvent(t, workID, "job-binding-design-"+workID, version, "job-binding", &plan)); err != nil {
			t.Fatalf("seed the architecture design: %v", err)
		}
		current, absent, err := readCurrentWorkflowDesign(ctx, route.store.DatabaseForTesting(), workID)
		if err != nil || absent || current == nil || current.Digest == "" {
			t.Fatalf("seeded design carries no identity: absent=%t %v", absent, err)
		}
		if current.Revision == nil {
			t.Fatal("seeded architecture design carries no revision")
		}
		fixture.revision = *current.Revision
		fixture.digest = current.Digest
	}
	return fixture
}

func seedJobBindingFixture(t *testing.T, workID, ref string, required, withDesign bool, mutatePlan func(*WorkflowDesignArchitecture)) jobBindingFixture {
	t.Helper()
	return seedJobBindingFixtureWithDefinition(t, workID, jobBindingDefinition(t, ref, true, required), withDesign, mutatePlan)
}

// jobBindingStepForTest reports the pinned definition's record_worker_job
// step, so the fixture records where the pin declares the action.
func jobBindingStepForTest(t *testing.T, definition WorkflowDefinition) string {
	t.Helper()
	for _, step := range definition.StepGraph.Steps {
		for _, action := range step.Actions {
			if action == "record_worker_job" {
				return step.ID
			}
		}
	}
	t.Fatal("the pinned definition declares no record_worker_job step")
	return ""
}

// jobBindingMember builds the architecture_binding member the capable
// pin's recording carries.
func jobBindingMember(revision int64, digest, sliceID string) map[string]any {
	return map[string]any{"design_revision": revision, "design_digest": digest, "slice_id": sliceID}
}

// recordJobBindingRevisionForTest records one worker-job revision through the
// record_worker_job action with the caller's authored fields, including any
// architecture_binding member. On an oracle-capable pin the recording carries
// the fixture oracle the definition requires.
func recordJobBindingRevisionForTest(t *testing.T, fixture jobBindingFixture, workID string, fields map[string]any) error {
	t.Helper()
	return recordWorkerJobFieldsForTest(t, fixture.store, workID, fixture.owner, fields)
}

// jobBindingRecordingFields authors one recording whose scope and checks the
// caller controls, ready with the fixture's bound evidence.
func jobBindingRecordingFields(jobID string, scope, checks []string, binding map[string]any, withMember bool) map[string]any {
	fields := map[string]any{
		"job_id": jobID, "objective": "bind the immutable job to its design slice " + jobID,
		"stopping_condition":   "the slice's declared test controls pass with no unresolved reference",
		"path_scope":           scope,
		"checks":               checks,
		"ready":                true,
		"readiness_evidence":   []string{"evidence:return-route-verification"},
		"reserved_integration": "integration evidence binds at the parent effect step",
	}
	if withMember {
		fields["architecture_binding"] = binding
	}
	return fields
}

// applyJobBindingMemberEventForTest appends one raw worker.job_recorded event
// whose payload is the typed recording's exact members plus one extra caller
// member, so a member the fold must judge rides the bytes a replayed log
// carries. The typed digest is derived first, so today's derivation is the
// authority the extra member may or may not enter.
func applyJobBindingMemberEventForTest(t *testing.T, s *Store, workID, label string, extraKey string, extraValue any) error {
	t.Helper()
	payload := WorkerJobRecordedPayload{
		JobID: "job:fold-" + label, Revision: 1, ContractVersion: 1,
		Objective: "fold-boundary recording " + label, StoppingCondition: "the slice's declared test controls pass",
		PathScope: []string{"internal/store/worker_jobs.go"}, PredicateIDs: []string{}, Checks: []string{"control:fixture"},
		Prerequisites: []WorkerJobPrerequisite{}, UnresolvedRefs: []string{},
	}
	payload.Digest = DeriveWorkerJobDigest(payload)
	values := map[string]any{}
	if err := json.Unmarshal(mustJSONValue(payload), &values); err != nil {
		t.Fatal(err)
	}
	if extraKey != "" {
		values[extraKey] = extraValue
	}
	version := readWorkVersion(t, s, workID)
	values["work_id"] = workID
	values["expected_version"] = version
	values["resulting_version"] = version + 1
	event := Event{EventID: "job-binding-raw-" + workID + "-" + label, Kind: WorkerJobRecorded, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(21, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(values)}
	return applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: workVersion(workID, version)})
}

// requireJobBindingRefusal asserts a typed refusal without naming a failure
// kind the store does not pin.
func requireJobBindingRefusal(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("the store accepted a recording it must refuse")
	}
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("refusal is not typed: %v", err)
	}
}

// jobBindingRawEvent reads the work's latest worker.job_recorded event from
// the log: the durable surface the binding must live on.
func jobBindingRawEvent(t *testing.T, s *Store, workID string) map[string]any {
	t.Helper()
	var payload string
	if err := s.DatabaseForTesting().QueryRow(`SELECT CAST(payload AS TEXT) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, workID, WorkerJobRecorded).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("worker.job_recorded payload is not JSON: %v", err)
	}
	return decoded
}

// requireJobBindingMember asserts the payload map carries exactly the typed
// design revision, design digest, and slice id triple.
func requireJobBindingMember(t *testing.T, payload map[string]any, revision int64, digest, sliceID string) {
	t.Helper()
	member, present := payload["architecture_binding"]
	if !present {
		t.Fatalf("the recorded job carries no architecture_binding member: %v", payload)
	}
	raw, err := json.Marshal(member)
	if err != nil {
		t.Fatal(err)
	}
	var bound struct {
		DesignRevision int64  `json:"design_revision"`
		DesignDigest   string `json:"design_digest"`
		SliceID        string `json:"slice_id"`
	}
	if err := json.Unmarshal(raw, &bound); err != nil {
		t.Fatalf("architecture_binding is not the typed design-and-slice triple: %v", err)
	}
	if bound.DesignRevision != revision || bound.DesignDigest != digest || bound.SliceID != sliceID {
		t.Fatalf("recorded architecture_binding = {design_revision:%d design_digest:%q slice_id:%q}, want the exact current identity {design_revision:%d design_digest:%q slice_id:%q}", bound.DesignRevision, bound.DesignDigest, bound.SliceID, revision, digest, sliceID)
	}
}

// jobBindingViewMap marshals the recorded view, so a member the view must
// carry is read from its durable JSON shape rather than a field the red test
// may not name yet.
func jobBindingViewMap(t *testing.T, view WorkerJobRevisionView) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(mustJSONValue(view), &decoded); err != nil {
		t.Fatalf("the worker-job view is not JSON: %v", err)
	}
	return decoded
}

// moveJobBindingDesignForTest records one more architecture-bearing design
// through the design writer, so the current identity moves past a recorded
// binding. It returns the moved identity.
func moveJobBindingDesignForTest(t *testing.T, s *Store, workID string) (int64, string) {
	t.Helper()
	moved := jobBindingArchitectureFixture("project")
	moved.Invariants[0].Statement = "the revised plan the binding must match instead"
	version := readWorkVersion(t, s, workID)
	if err := applyProjectionEvents(t, s, workID, version, projectionDesignEvent(t, workID, "job-binding-design-move-"+workID, version, "moved", &moved)); err != nil {
		t.Fatal(err)
	}
	current, _, err := readCurrentWorkflowDesign(context.Background(), s.DatabaseForTesting(), workID)
	if err != nil || current == nil || current.Digest == "" || current.Revision == nil {
		t.Fatalf("moved design carries no identity: %v", err)
	}
	return *current.Revision, current.Digest
}

type jobBindingState struct {
	version   int64
	events    int64
	jobs      int64
	digest    string
	state     string
	satisfied string
}

// readJobBindingState is the durable surface a refused or stale binding must
// leave untouched.
func readJobBindingState(t *testing.T, s *Store, workID, jobID string) jobBindingState {
	t.Helper()
	state := jobBindingState{}
	if err := s.DatabaseForTesting().QueryRow(`SELECT (SELECT version FROM work_items WHERE id=?),(SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id=?),(SELECT count(*) FROM worker_job_revisions WHERE work_id=?),COALESCE((SELECT digest FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=1),''),COALESCE((SELECT state FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=1),''),COALESCE((SELECT COALESCE(satisfied_result_ref,'') FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=1),'')`, workID, workID, workID, workID, jobID, workID, jobID, workID, jobID).Scan(&state.version, &state.events, &state.jobs, &state.digest, &state.state, &state.satisfied); err != nil {
		t.Fatal(err)
	}
	return state
}

// sortedPaths is the normalized-set comparison for scope: membership and
// cardinality, never member order.
func sortedPaths(paths []string) []string {
	sorted := append([]string{}, paths...)
	sort.Strings(sorted)
	return sorted
}

func TestArchitectureJobBinding(t *testing.T) {
	ctx := context.Background()

	t.Run("a capable pin records the exact binding into content, digest, event, view, and packet", func(t *testing.T) {
		const workID = "job-binding-exact"
		fixture := seedJobBindingFixture(t, workID, jobBindingPinRef, true, true, nil)
		fields := jobBindingRecordingFields("job:exact", []string{jobBindingSliceFiles[1], jobBindingSliceFiles[0]}, []string{"control:fixture"}, jobBindingMember(fixture.revision, fixture.digest, "binding-edit"), true)
		if err := recordJobBindingRevisionForTest(t, fixture, workID, fields); err != nil {
			t.Fatalf("the exact current binding and slice must record: %v", err)
		}
		views, err := fixture.store.WorkerJobRevisions(ctx, workID)
		if err != nil {
			t.Fatal(err)
		}
		if len(views) != 1 {
			t.Fatalf("recorded revisions = %d, want the one bound revision", len(views))
		}
		view := views[0]
		// The binding alone enters the content digest: the derivation over the
		// exact logged payload with ONLY the architecture_binding member
		// removed cannot reach the recorded digest. Every other member,
		// including the readiness evidence, round-trips unchanged.
		logged := jobBindingRawEvent(t, fixture.store, workID)
		delete(logged, "architecture_binding")
		var unbound WorkerJobRecordedPayload
		if err := json.Unmarshal(mustJSONValue(logged), &unbound); err != nil {
			t.Fatalf("the logged payload minus its binding is not one typed recording: %v", err)
		}
		if unbound.Digest != view.Binding.Digest {
			t.Fatalf("logged digest %q does not equal the recorded digest %q; the roundtrip lost content", unbound.Digest, view.Binding.Digest)
		}
		if view.Binding.Digest == DeriveWorkerJobDigest(unbound) {
			t.Fatal("the recorded digest equals the derivation with only the binding removed; the binding did not enter the content digest")
		}
		// The binding enters the event.
		requireJobBindingMember(t, jobBindingRawEvent(t, fixture.store, workID), fixture.revision, fixture.digest, "binding-edit")
		// The binding enters the view's durable JSON shape.
		requireJobBindingMember(t, jobBindingViewMap(t, view), fixture.revision, fixture.digest, "binding-edit")
		// The binding enters the packet projection.
		packet := recordedPacketJobForTest(t, fixture.store, workID, view.Binding)
		requireJobBindingMember(t, packet, fixture.revision, fixture.digest, "binding-edit")
		// The derived scope is the slice files as a normalized set, not the
		// caller's member order.
		if got, want := sortedPaths(view.PathScope), sortedPaths(jobBindingSliceFiles); strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("recorded path_scope = %v, want the slice file set %v", got, want)
		}
		// The derived Project scope is the slice owner's Project.
		if view.ProjectScope != "project" {
			t.Fatalf("recorded project_scope = %q, want the slice owner's Project %q", view.ProjectScope, "project")
		}
		// The recorded checks carry the slice's declared test controls.
		sawControl := false
		for _, check := range view.Checks {
			if check == "control:fixture" {
				sawControl = true
			}
		}
		if !sawControl {
			t.Fatalf("recorded checks = %v, want the slice's declared test control control:fixture", view.Checks)
		}
	})

	t.Run("the slice owner's Project derives the scope over the work's primary", func(t *testing.T) {
		const workID = "job-binding-secondary"
		fixture := seedJobBindingFixture(t, workID, jobBindingPinRef, true, false, nil)
		// A second member Project joins the work, and the design's owner
		// names it, so the derived scope can only come from the slice.
		version := readWorkVersion(t, fixture.store, workID)
		events := []Event{
			projectCreatedEvent("project-b", "create-project-b-"+workID),
			operationEvent("product-project-b-"+workID, "product_project.added", SubjectProduct, "product", map[string]any{
				"product_id": "product", "project_id": "project-b", "role": "secondary", "reason": "test",
				"expected_version": 2, "resulting_version": 3,
			}),
			operationEvent("membership-b-"+workID, "work_project.added", SubjectWorkItem, workID, map[string]any{
				"work_id": workID, "project_id": "project-b", "role": "secondary", "reason": "test",
				"expected_version": version, "resulting_version": version + 1,
			}),
		}
		if err := applyWorkflowTestOperation(ctx, fixture.store, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version, VersionRef(SubjectProject, "project-b"): 0, VersionRef(SubjectProduct, "product"): 2}}); err != nil {
			t.Fatalf("seed the secondary member Project: %v", err)
		}
		plan := jobBindingArchitectureFixture("project-b")
		designVersion := readWorkVersion(t, fixture.store, workID)
		if err := applyProjectionEvents(t, fixture.store, workID, designVersion, projectionDesignEvent(t, workID, "job-binding-design-"+workID, designVersion, "job-binding", &plan)); err != nil {
			t.Fatalf("seed the secondary-owner design: %v", err)
		}
		current, _, err := readCurrentWorkflowDesign(ctx, fixture.store.DatabaseForTesting(), workID)
		if err != nil || current == nil || current.Revision == nil || current.Digest == "" {
			t.Fatalf("seeded design carries no identity: %v", err)
		}
		fixture.revision, fixture.digest = *current.Revision, current.Digest
		fields := jobBindingRecordingFields("job:secondary", jobBindingSliceFiles, []string{"control:fixture"}, jobBindingMember(fixture.revision, fixture.digest, "binding-edit"), true)
		if err := recordJobBindingRevisionForTest(t, fixture, workID, fields); err != nil {
			t.Fatalf("the secondary-owner binding must record: %v", err)
		}
		views, err := fixture.store.WorkerJobRevisions(ctx, workID)
		if err != nil {
			t.Fatal(err)
		}
		if len(views) != 1 || views[0].ProjectScope != "project-b" {
			t.Fatalf("recorded project_scope = %#v, want the slice owner's secondary Project project-b", views)
		}
	})

	t.Run("recording scope must equal the slice files as a normalized set", func(t *testing.T) {
		for _, testCase := range []struct {
			name  string
			scope []string
		}{
			{"narrower", jobBindingSliceFiles[:1]},
			{"wider", append(append([]string{}, jobBindingSliceFiles...), "internal/store/worker_oracle.go")},
			{"omitted", []string{}},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				workID := "job-binding-scope-" + testCase.name
				fixture := seedJobBindingFixture(t, workID, jobBindingPinRef, true, true, nil)
				fields := jobBindingRecordingFields("job:scope", testCase.scope, []string{"control:fixture"}, jobBindingMember(fixture.revision, fixture.digest, "binding-edit"), true)
				err := recordJobBindingRevisionForTest(t, fixture, workID, fields)
				requireJobBindingRefusal(t, err)
			})
		}
	})

	t.Run("job checks must carry the slice's declared test controls", func(t *testing.T) {
		const workID = "job-binding-controls"
		fixture := seedJobBindingFixture(t, workID, jobBindingPinRef, true, true, nil)
		fields := jobBindingRecordingFields("job:controls", jobBindingSliceFiles, []string{"go test ./internal/store/"}, jobBindingMember(fixture.revision, fixture.digest, "binding-edit"), true)
		err := recordJobBindingRevisionForTest(t, fixture, workID, fields)
		requireJobBindingRefusal(t, err)
	})

	t.Run("the binding must name the current design identity and a real slice", func(t *testing.T) {
		t.Run("an unknown slice refuses", func(t *testing.T) {
			const workID = "job-binding-unknown-slice"
			fixture := seedJobBindingFixture(t, workID, jobBindingPinRef, true, true, nil)
			fields := jobBindingRecordingFields("job:unknown-slice", jobBindingSliceFiles, []string{"control:fixture"}, jobBindingMember(fixture.revision, fixture.digest, "no-such-slice"), true)
			requireJobBindingRefusal(t, recordJobBindingRevisionForTest(t, fixture, workID, fields))
		})
		t.Run("a stale design identity refuses", func(t *testing.T) {
			const workID = "job-binding-stale"
			fixture := seedJobBindingFixture(t, workID, jobBindingPinRef, true, true, nil)
			movedRevision, movedDigest := moveJobBindingDesignForTest(t, fixture.store, workID)
			if movedDigest == fixture.digest || movedRevision != fixture.revision+1 {
				t.Fatal("the moved design kept the predecessor identity; the fixture cannot present a stale identity")
			}
			fields := jobBindingRecordingFields("job:stale", jobBindingSliceFiles, []string{"control:fixture"}, jobBindingMember(fixture.revision, fixture.digest, "binding-edit"), true)
			requireJobBindingRefusal(t, recordJobBindingRevisionForTest(t, fixture, workID, fields))
		})
		t.Run("an invented identity over an absent design refuses", func(t *testing.T) {
			const workID = "job-binding-absent-design"
			fixture := seedJobBindingFixture(t, workID, jobBindingPinRef, true, false, nil)
			fields := jobBindingRecordingFields("job:absent", jobBindingSliceFiles, []string{"control:fixture"}, jobBindingMember(1, "sha256:"+strings.Repeat("e", 64), "binding-edit"), true)
			requireJobBindingRefusal(t, recordJobBindingRevisionForTest(t, fixture, workID, fields))
		})
	})

	// Record time cannot infer editing intent: the lane capability joins at
	// dispatch. A job may bind the integration slice under its empty file
	// set, and a recording that claims the edit slice's scope against it
	// refuses through the scope equality alone.
	t.Run("a job may bind the integration slice under its empty file set", func(t *testing.T) {
		const workID = "job-binding-integration-unscoped"
		fixture := seedJobBindingFixture(t, workID, jobBindingPinRef, true, true, nil)
		fields := jobBindingRecordingFields("job:integration", []string{}, []string{"control:fixture"}, jobBindingMember(fixture.revision, fixture.digest, "binding-integration"), true)
		if err := recordJobBindingRevisionForTest(t, fixture, workID, fields); err != nil {
			t.Fatalf("the integration-slice binding under its empty file set must record: %v", err)
		}
		views, err := fixture.store.WorkerJobRevisions(ctx, workID)
		if err != nil {
			t.Fatal(err)
		}
		if len(views) != 1 {
			t.Fatalf("recorded revisions = %d, want the one integration-bound revision", len(views))
		}
		requireJobBindingMember(t, jobBindingRawEvent(t, fixture.store, workID), fixture.revision, fixture.digest, "binding-integration")
	})
	t.Run("a recording claiming edit scope against the integration slice refuses by scope", func(t *testing.T) {
		const workID = "job-binding-integration-scoped"
		fixture := seedJobBindingFixture(t, workID, jobBindingPinRef, true, true, nil)
		fields := jobBindingRecordingFields("job:integration", jobBindingSliceFiles, []string{"control:fixture"}, jobBindingMember(fixture.revision, fixture.digest, "binding-integration"), true)
		requireJobBindingRefusal(t, recordJobBindingRevisionForTest(t, fixture, workID, fields))
	})

	t.Run("a capable pin requires the binding", func(t *testing.T) {
		const workID = "job-binding-required"
		fixture := seedJobBindingFixture(t, workID, jobBindingPinRef, true, true, nil)
		fields := jobBindingRecordingFields("job:unbound", jobBindingSliceFiles, []string{"control:fixture"}, nil, false)
		requireJobBindingRefusal(t, recordJobBindingRevisionForTest(t, fixture, workID, fields))
	})

	t.Run("the fold requires the binding a capable pin declares", func(t *testing.T) {
		const workID = "job-binding-fold-required"
		fixture := seedJobBindingFixture(t, workID, jobBindingPinRef, true, true, nil)
		err := applyJobBindingMemberEventForTest(t, fixture.store, workID, "unbound", "", nil)
		requireJobBindingRefusal(t, err)
	})

	t.Run("a loose declaration grants no capability", func(t *testing.T) {
		const workID = "job-binding-loose"
		fixture := seedJobBindingFixture(t, workID, jobBindingLooseRef, false, true, nil)
		fields := jobBindingRecordingFields("job:loose", jobBindingSliceFiles, []string{"control:fixture"}, jobBindingMember(fixture.revision, fixture.digest, "binding-edit"), true)
		requireJobBindingRefusal(t, recordJobBindingRevisionForTest(t, fixture, workID, fields))
	})

	t.Run("an old pin keeps its binding-free shape", func(t *testing.T) {
		const workID = "job-binding-old-pin"
		registered, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 30)
		if !ok {
			t.Fatal("the released implementation head is not registered")
		}
		fixture := seedJobBindingFixtureWithDefinition(t, workID, registered, false, nil)
		payload := WorkerJobRecordedPayload{
			JobID: "job:legacy", Revision: 1, ContractVersion: 1,
			Objective: "legacy shape recording", StoppingCondition: "the recorded checks pass",
			PathScope: []string{"internal/store/worker_jobs.go"}, PredicateIDs: []string{}, Checks: []string{"control:fixture"},
			Prerequisites: []WorkerJobPrerequisite{}, UnresolvedRefs: []string{},
		}
		payload.Digest = DeriveWorkerJobDigest(payload)
		if err := applyWorkerJobEventForTest(t, fixture.store, workID, payload); err != nil {
			t.Fatalf("the old pin's binding-free recording must hold: %v", err)
		}
		views, err := fixture.store.WorkerJobRevisions(ctx, workID)
		if err != nil {
			t.Fatal(err)
		}
		if len(views) != 1 {
			t.Fatalf("recorded revisions = %d, want the one legacy revision", len(views))
		}
		view := views[0]
		if view.Binding.Digest != payload.Digest {
			t.Fatalf("recorded digest = %q, want the member-free derivation %q", view.Binding.Digest, payload.Digest)
		}
		if _, present := jobBindingViewMap(t, view)["architecture_binding"]; present {
			t.Fatal("the old pin's view carries an architecture_binding member the pin never declared")
		}
		packet := recordedPacketJobForTest(t, fixture.store, workID, view.Binding)
		if _, present := packet["architecture_binding"]; present {
			t.Fatal("the old pin's packet carries an architecture_binding member the pin never declared")
		}
	})

	t.Run("an old pin refuses a supplied binding member, including explicit null", func(t *testing.T) {
		const workID = "job-binding-old-refusal"
		registered, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 30)
		if !ok {
			t.Fatal("the released implementation head is not registered")
		}
		fixture := seedJobBindingFixtureWithDefinition(t, workID, registered, false, nil)
		fields := jobBindingRecordingFields("job:legacy-member", jobBindingSliceFiles, []string{"control:fixture"}, nil, false)
		fields["architecture_binding"] = nil
		requireJobBindingRefusal(t, recordJobBindingRevisionForTest(t, fixture, workID, fields))
		requireJobBindingRefusal(t, applyJobBindingMemberEventForTest(t, fixture.store, workID, "null-member", "architecture_binding", nil))
		// The typed decode matches members case-insensitively, so an alias
		// spelling of the member is a supplied member all the same: it must
		// refuse on an old pin, not decode away to absence.
		alias := WorkerJobRecordedPayload{
			JobID: "job:legacy-alias", Revision: 1, ContractVersion: 1,
			Objective: "legacy alias member", StoppingCondition: "the recorded checks pass",
			PathScope: []string{"internal/store/worker_jobs.go"}, PredicateIDs: []string{}, Checks: []string{"control:fixture"},
			Prerequisites: []WorkerJobPrerequisite{}, UnresolvedRefs: []string{},
		}
		requireJobBindingRefusal(t, applyJobBindingEnvelopeEventForTest(t, fixture.store, workID, alias, `"Architecture_Binding":null`))
	})

	t.Run("a stale prospective job becomes unready without mutating history", func(t *testing.T) {
		const workID = "job-binding-stale-unready"
		fixture := seedJobBindingFixture(t, workID, jobBindingPinRef, true, true, nil)
		fields := jobBindingRecordingFields("job:prospective", jobBindingSliceFiles, []string{"control:fixture"}, jobBindingMember(fixture.revision, fixture.digest, "binding-edit"), true)
		if err := recordJobBindingRevisionForTest(t, fixture, workID, fields); err != nil {
			t.Fatalf("the exact binding must record before the design moves: %v", err)
		}
		before := readJobBindingState(t, fixture.store, workID, "job:prospective")
		views, err := fixture.store.ReadyWorkerJobRevisions(ctx, workID)
		if err != nil {
			t.Fatal(err)
		}
		if len(views) != 1 {
			t.Fatalf("ready revisions before the move = %d, want the one bound revision", len(views))
		}
		movedRevision, movedDigest := moveJobBindingDesignForTest(t, fixture.store, workID)
		if movedDigest == fixture.digest || movedRevision != fixture.revision+1 {
			t.Fatal("the moved design kept the recorded digest; the staleness assertion cannot distinguish identities")
		}
		ready, err := fixture.store.ReadyWorkerJobRevisions(ctx, workID)
		if err != nil {
			t.Fatal(err)
		}
		if len(ready) != 0 {
			t.Fatalf("ready revisions after the design moved = %#v, want none: the stale binding is unready", ready)
		}
		after := readJobBindingState(t, fixture.store, workID, "job:prospective")
		if after.digest != before.digest || after.state != before.state || after.satisfied != before.satisfied || after.jobs != before.jobs {
			t.Fatalf("the design move mutated the recorded job: before %+v after %+v", before, after)
		}
		if after.events != before.events+1 {
			t.Fatalf("the design move changed the event count by %d, want exactly the one design event", after.events-before.events)
		}
	})

	t.Run("no readiness is fabricated from the binding", func(t *testing.T) {
		const workID = "job-binding-no-fabricated-readiness"
		fixture := seedJobBindingFixture(t, workID, jobBindingPinRef, true, true, nil)
		fields := jobBindingRecordingFields("job:unasserted", jobBindingSliceFiles, []string{"control:fixture"}, jobBindingMember(fixture.revision, fixture.digest, "binding-edit"), true)
		fields["ready"] = false
		delete(fields, "readiness_evidence")
		if err := recordJobBindingRevisionForTest(t, fixture, workID, fields); err != nil {
			t.Fatalf("an unasserted binding may record: %v", err)
		}
		views, err := fixture.store.WorkerJobRevisions(ctx, workID)
		if err != nil {
			t.Fatal(err)
		}
		if len(views) != 1 || views[0].Ready {
			t.Fatalf("ready = %#v, want the validly bound job to stay unready without asserted readiness", views)
		}
		// The coordinator readiness assertion keeps its own evidence rule.
		evidenceless := jobBindingRecordingFields("job:evidenceless", jobBindingSliceFiles, []string{"control:fixture"}, jobBindingMember(fixture.revision, fixture.digest, "binding-edit"), true)
		evidenceless["ready"] = true
		delete(evidenceless, "readiness_evidence")
		requireJobBindingRefusal(t, recordJobBindingRevisionForTest(t, fixture, workID, evidenceless))
	})

	t.Run("declared slice tests must resolve exact acceptance-oracle controls", func(t *testing.T) {
		const workID = "job-binding-unjoined-control"
		fixture := seedJobBindingFixture(t, workID, jobBindingPinRef, true, true, func(plan *WorkflowDesignArchitecture) {
			plan.Slices[0].Tests = []string{"control:fixture", "control:unjoined"}
		})
		fields := jobBindingRecordingFields("job:unjoined", jobBindingSliceFiles, []string{"control:fixture", "control:unjoined"}, jobBindingMember(fixture.revision, fixture.digest, "binding-edit"), true)
		requireJobBindingRefusal(t, recordJobBindingRevisionForTest(t, fixture, workID, fields))
	})

	// The binding's slice id answers to the declared architecture identity
	// grammar, not a narrower job grammar: a slash id the design declares
	// binds and resolves end to end.
	t.Run("a slice id with the architecture grammar's slash records and resolves", func(t *testing.T) {
		const workID = "job-binding-slash-slice"
		fixture := seedJobBindingFixture(t, workID, jobBindingPinRef, true, true, func(plan *WorkflowDesignArchitecture) {
			plan.Slices[0].SliceID = "binding/edit"
		})
		fields := jobBindingRecordingFields("job:slash", jobBindingSliceFiles, []string{"control:fixture"}, jobBindingMember(fixture.revision, fixture.digest, "binding/edit"), true)
		if err := recordJobBindingRevisionForTest(t, fixture, workID, fields); err != nil {
			t.Fatalf("the declared slash slice id must record: %v", err)
		}
		requireJobBindingMember(t, jobBindingRawEvent(t, fixture.store, workID), fixture.revision, fixture.digest, "binding/edit")
	})

	// The recorded envelope answers to the typed payload's exact JSON names:
	// a case-insensitive typed decode folds an alias key into the same field
	// the canonical SQL readback reads, so one recorded revision could carry
	// two identities. Alias keys refuse on the binding-bearing surface.
	t.Run("the fold holds the recorded envelope to the typed member names", func(t *testing.T) {
		bindingPayload := func(t *testing.T, fixture jobBindingFixture, workID, jobID string) WorkerJobRecordedPayload {
			t.Helper()
			bootstrapOracleFixtureSubject(t, fixture.store, workID)
			oracle, err := decodeAcceptanceOracle(mustJSONValue(acceptanceOracleFieldsForTest(t, fixture.store, workID)))
			if err != nil {
				t.Fatal(err)
			}
			payload := jobBindingEnvelopePayload(fixture, jobID, true)
			payload.AcceptanceOracle = oracle
			return payload
		}
		t.Run("an identity alias with the same value refuses", func(t *testing.T) {
			const workID = "job-binding-envelope-same"
			fixture := seedJobBindingFixture(t, workID, jobBindingPinRef, true, true, nil)
			payload := bindingPayload(t, fixture, workID, "job:envelope-typed")
			err := applyJobBindingEnvelopeEventForTest(t, fixture.store, workID, payload, `"JOB_ID":"`+payload.JobID+`"`)
			requireJobBindingRefusal(t, err)
		})
		t.Run("an alias value cannot shadow the canonical identity", func(t *testing.T) {
			const workID = "job-binding-envelope-shadow"
			fixture := seedJobBindingFixture(t, workID, jobBindingPinRef, true, true, nil)
			// The typed decode keeps the exact-match canonical member, so the
			// digest covers the canonical identity and the spliced alias
			// value cannot substitute a second one.
			payload := bindingPayload(t, fixture, workID, "job:envelope-typed")
			err := applyJobBindingEnvelopeEventForTest(t, fixture.store, workID, payload, `"JOB_ID":"job:envelope-shadow"`)
			var failure *Failure
			if !errors.As(err, &failure) || failure.Kind != KindInvalidPayload || !strings.Contains(failure.Detail, "digest does not match the derived content digest") {
				t.Fatalf("the shadowing alias event = %v, want the digest refusal that proves the canonical identity held", err)
			}
		})
		t.Run("an architecture binding alias refuses", func(t *testing.T) {
			const workID = "job-binding-envelope-binding-alias"
			fixture := seedJobBindingFixture(t, workID, jobBindingPinRef, true, true, nil)
			payload := bindingPayload(t, fixture, workID, "job:envelope-binding")
			err := applyJobBindingEnvelopeEventForTest(t, fixture.store, workID, payload, `"Architecture_Binding":`+string(mustJSONValue(jobBindingMember(fixture.revision, fixture.digest, "binding-edit"))))
			requireJobBindingRefusal(t, err)
		})
	})

	// The binding member's own shape refuses everywhere it rides: the action
	// route splices raw member bytes into the admitted payload, the raw fold
	// carries them on a replay-shaped event, and the packet claims them
	// through the direct dispatch admission.
	t.Run("the action route refuses a malformed binding member", func(t *testing.T) {
		for _, testCase := range []struct {
			name   string
			member string
		}{
			{"unknown member", `{"design_revision":1,"design_digest":"sha256:` + strings.Repeat("a", 64) + `","slice_id":"binding-edit","approved":true}`},
			{"explicit null", `null`},
			{"alias key", `{"Design_Revision":1,"design_digest":"sha256:` + strings.Repeat("a", 64) + `","slice_id":"binding-edit"}`},
			{"duplicate canonical key", `{"design_revision":1,"design_revision":2,"design_digest":"sha256:` + strings.Repeat("a", 64) + `","slice_id":"binding-edit"}`},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				const workID = "job-binding-action-shape"
				fixture := seedJobBindingFixture(t, workID, jobBindingPinRef, true, true, nil)
				payload := jobBindingSplicedActionPayload(t, fixture, workID, testCase.member)
				requireJobBindingRefusal(t, runVerdictActionAs(t, fixture.store, workID, "record_worker_job", payload, 0, fixture.owner))
			})
		}
	})

	t.Run("the raw fold refuses a malformed or unverifiable binding member", func(t *testing.T) {
		for _, testCase := range []struct {
			name        string
			extraKey    string
			extraValue  any
			bindingName string
		}{
			{"substituted digest", "architecture_binding", jobBindingMember(1, "sha256:"+strings.Repeat("b", 64), "binding-edit"), "raw-substituted"},
			{"unknown member", "architecture_binding", jobBindingRawMember(t, map[string]any{"design_revision": 1, "design_digest": "sha256:" + strings.Repeat("b", 64), "slice_id": "binding-edit", "approved": true}), "raw-unknown"},
			{"explicit null", "architecture_binding", nil, "raw-null"},
			{"alias key", "architecture_binding", jobBindingRawMember(t, map[string]any{"Design_Revision": 1, "design_digest": "sha256:" + strings.Repeat("b", 64), "slice_id": "binding-edit"}), "raw-alias"},
			{"duplicate canonical key", "architecture_binding", json.RawMessage(`{"design_revision":1,"design_revision":2,"design_digest":"sha256:` + strings.Repeat("b", 64) + `","slice_id":"binding-edit"}`), "raw-duplicate"},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				const workID = "job-binding-fold-shape"
				fixture := seedJobBindingFixture(t, workID, jobBindingPinRef, true, true, nil)
				before := readJobBindingState(t, fixture.store, workID, "job:fold-"+testCase.bindingName)
				err := applyJobBindingMemberEventForTest(t, fixture.store, workID, testCase.bindingName, testCase.extraKey, testCase.extraValue)
				requireJobBindingRefusal(t, err)
				if after := readJobBindingState(t, fixture.store, workID, "job:fold-"+testCase.bindingName); after != before {
					t.Fatalf("the refused raw event moved state: before %+v after %+v", before, after)
				}
			})
		}
	})

	t.Run("rebuild retains the recorded binding without re-deriving it", func(t *testing.T) {
		const workID = "job-binding-rebuild"
		fixture := seedJobBindingFixture(t, workID, jobBindingPinRef, true, true, nil)
		fields := jobBindingRecordingFields("job:retained", jobBindingSliceFiles, []string{"control:fixture"}, jobBindingMember(fixture.revision, fixture.digest, "binding-edit"), true)
		if err := recordJobBindingRevisionForTest(t, fixture, workID, fields); err != nil {
			t.Fatalf("the exact binding must record before the rebuild: %v", err)
		}
		moveJobBindingDesignForTest(t, fixture.store, workID)
		if err := RebuildFromLog(ctx, fixture.store); err != nil {
			t.Fatalf("the rebuild must retain the recorded binding: %v", err)
		}
		requireJobBindingMember(t, jobBindingRawEvent(t, fixture.store, workID), fixture.revision, fixture.digest, "binding-edit")
		views, err := fixture.store.WorkerJobRevisions(ctx, workID)
		if err != nil {
			t.Fatal(err)
		}
		if len(views) != 1 {
			t.Fatalf("rebuilt revisions = %d, want the one bound revision", len(views))
		}
		requireJobBindingMember(t, jobBindingViewMap(t, views[0]), fixture.revision, fixture.digest, "binding-edit")
		ready, err := fixture.store.ReadyWorkerJobRevisions(ctx, workID)
		if err != nil {
			t.Fatal(err)
		}
		if len(ready) != 0 {
			t.Fatalf("ready revisions after the rebuild = %#v, want none: replay keeps the recorded authority and the stale job unready", ready)
		}
	})

	t.Run("packet admission carries the binding and rejects tampered members", func(t *testing.T) {
		const workID = "job-binding-packet"
		fixture := seedJobBindingFixture(t, workID, jobBindingPinRef, true, true, nil)
		fields := jobBindingRecordingFields("job:packet", jobBindingSliceFiles, []string{"control:fixture"}, jobBindingMember(fixture.revision, fixture.digest, "binding-edit"), true)
		if err := recordJobBindingRevisionForTest(t, fixture, workID, fields); err != nil {
			t.Fatalf("the exact binding must record before the packet: %v", err)
		}
		views, err := fixture.store.WorkerJobRevisions(ctx, workID)
		if err != nil {
			t.Fatal(err)
		}
		binding := views[0].Binding
		definition := jobBindingDispatchCapableDefinition(t)
		step := jobBindingStepForTest(t, definition)
		lane := reviewGateLane(t, "implementation")
		packetRaw := func(workerJob map[string]any) json.RawMessage {
			packet := dispatchWorkerPacket(t, fixture.store, workID, step, "attempt:"+workID)
			packet["schema_version"] = WorkerPacketSchemaVersion
			packet["inputs"].(map[string]any)["worker_job"] = workerJob
			raw, err := json.Marshal(packet)
			if err != nil {
				t.Fatal(err)
			}
			return raw
		}
		exact := recordedPacketJobForTest(t, fixture.store, workID, binding)
		exact["architecture_binding"] = jobBindingMember(fixture.revision, fixture.digest, "binding-edit")
		if _, err := validateWorkerPacketJob(ctx, fixture.store.DatabaseForTesting(), definition, workID, lane, packetRaw(exact)); err != nil {
			t.Fatalf("packet admission must carry the exact recorded binding: %v", err)
		}
		memberless := recordedPacketJobForTest(t, fixture.store, workID, binding)
		delete(memberless, "architecture_binding")
		_, memberlessErr := validateWorkerPacketJob(ctx, fixture.store.DatabaseForTesting(), definition, workID, lane, packetRaw(memberless))
		requireJobBindingRefusal(t, memberlessErr)
		tampered := recordedPacketJobForTest(t, fixture.store, workID, binding)
		tampered["architecture_binding"] = jobBindingMember(fixture.revision, fixture.digest, "binding-integration")
		_, err = validateWorkerPacketJob(ctx, fixture.store.DatabaseForTesting(), definition, workID, lane, packetRaw(tampered))
		requireJobBindingRefusal(t, err)
		aliased := recordedPacketJobForTest(t, fixture.store, workID, binding)
		aliased["Architecture_Binding"] = jobBindingMember(fixture.revision, fixture.digest, "binding-edit")
		_, err = validateWorkerPacketJob(ctx, fixture.store.DatabaseForTesting(), definition, workID, lane, packetRaw(aliased))
		requireJobBindingRefusal(t, err)
		// The nested binding member answers to its own exact typed names: an
		// alias key spliced after the canonical member coalesces in the
		// typed decode and would transport a tampered canonical identity.
		nestedAlias := recordedPacketJobForTest(t, fixture.store, workID, binding)
		delete(nestedAlias, "architecture_binding")
		nestedRaw := string(mustJSONValue(nestedAlias))
		nestedRaw = nestedRaw[:len(nestedRaw)-1] + `,"architecture_binding":{"design_revision":` + fmt.Sprint(fixture.revision) + `,"design_digest":"` + fixture.digest + `","slice_id":"other-slice","SLICE_ID":"binding-edit"}}`
		packet := dispatchWorkerPacket(t, fixture.store, workID, step, "attempt:"+workID)
		packet["schema_version"] = WorkerPacketSchemaVersion
		packet["inputs"].(map[string]any)["worker_job"] = json.RawMessage(nestedRaw)
		nestedPacket, marshalErr := json.Marshal(packet)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		_, err = validateWorkerPacketJob(ctx, fixture.store.DatabaseForTesting(), definition, workID, lane, nestedPacket)
		requireJobBindingRefusal(t, err)
		duplicated := recordedPacketJobForTest(t, fixture.store, workID, binding)
		duplicatedRaw := string(mustJSONValue(duplicated))
		duplicatedRaw = duplicatedRaw[:len(duplicatedRaw)-1] + `,"architecture_binding":{"design_revision":` + fmt.Sprint(fixture.revision) + `,"design_digest":"` + fixture.digest + `","slice_id":"binding-edit"}}`
		duplicatedPacket := dispatchWorkerPacket(t, fixture.store, workID, step, "attempt:"+workID)
		duplicatedPacket["schema_version"] = WorkerPacketSchemaVersion
		duplicatedPacket["inputs"].(map[string]any)["worker_job"] = json.RawMessage(duplicatedRaw)
		duplicatedBytes, marshalErr := json.Marshal(duplicatedPacket)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		_, err = validateWorkerPacketJob(ctx, fixture.store.DatabaseForTesting(), definition, workID, lane, duplicatedBytes)
		requireJobBindingRefusal(t, err)
	})

	t.Run("a binding-capable definition gains no dispatch job capability before activation", func(t *testing.T) {
		const workID = "job-binding-dispatch-capability"
		fixture := seedJobBindingFixture(t, workID, jobBindingPinRef, true, true, nil)
		fields := jobBindingRecordingFields("job:preactivation", jobBindingSliceFiles, []string{"control:fixture"}, jobBindingMember(fixture.revision, fixture.digest, "binding-edit"), true)
		if err := recordJobBindingRevisionForTest(t, fixture, workID, fields); err != nil {
			t.Fatalf("the binding must record on the capable pin: %v", err)
		}
		views, err := fixture.store.WorkerJobRevisions(ctx, workID)
		if err != nil {
			t.Fatal(err)
		}
		registered, ok := BuiltinWorkflowRegistry().Lookup(jobBindingPinRef, 30)
		if !ok {
			t.Fatal("the capable synthetic pin is not registered")
		}
		packet := dispatchWorkerPacket(t, fixture.store, workID, jobBindingStepForTest(t, registered.Definition), "attempt:"+workID)
		packet["schema_version"] = WorkerPacketSchemaVersion
		workerJob := recordedPacketJobForTest(t, fixture.store, workID, views[0].Binding)
		workerJob["architecture_binding"] = jobBindingMember(fixture.revision, fixture.digest, "binding-edit")
		packet["inputs"].(map[string]any)["worker_job"] = workerJob
		raw, err := json.Marshal(packet)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := validateWorkerPacketJob(ctx, fixture.store.DatabaseForTesting(), registered.Definition, workID, reviewGateLane(t, "implementation"), raw); err == nil || !strings.Contains(err.Error(), "which the pinned workflow definition does not admit") {
			t.Fatalf("pre-activation dispatch admission = %v, want the CD-0205 version gate refusal on the synthetic pin", err)
		}
	})

	t.Run("an integration-bound job's supplemental controls stay independent of the native set", func(t *testing.T) {
		const workID = "job-binding-integration-native"
		fixture := seedJobBindingFixture(t, workID, jobBindingPinRef, true, true, func(plan *WorkflowDesignArchitecture) {
			plan.Slices[1].Tests = []string{"control:native-integration-parent-order"}
		})
		fields := jobBindingRecordingFields("job:integration-native", []string{}, []string{"control:fixture"}, jobBindingMember(fixture.revision, fixture.digest, "binding-integration"), true)
		if err := recordJobBindingRevisionForTest(t, fixture, workID, fields); err != nil {
			t.Fatalf("the integration binding with its supplemental control must record: %v", err)
		}
		requireJobBindingMember(t, jobBindingRawEvent(t, fixture.store, workID), fixture.revision, fixture.digest, "binding-integration")
	})

	t.Run("a member-free legacy packet keeps its base alias shape", func(t *testing.T) {
		const workID = "job-binding-legacy-packet"
		registered, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 30)
		if !ok {
			t.Fatal("the released implementation head is not registered")
		}
		fixture := seedJobBindingFixtureWithDefinition(t, workID, registered, false, nil)
		payload := WorkerJobRecordedPayload{
			JobID: "job:legacy-packet", Revision: 1, ContractVersion: 1,
			Objective: "legacy packet shape", StoppingCondition: "the recorded checks pass",
			PathScope: []string{"internal/store/worker_jobs.go"}, PredicateIDs: []string{}, Checks: []string{"control:fixture"},
			Prerequisites: []WorkerJobPrerequisite{}, UnresolvedRefs: []string{},
			Readiness: &WorkerJobReadiness{Ready: true, Evidence: []string{"evidence:return-route-verification"}},
		}
		payload.Digest = DeriveWorkerJobDigest(payload)
		if err := applyWorkerJobEventForTest(t, fixture.store, workID, payload); err != nil {
			t.Fatalf("the legacy member-free recording must hold: %v", err)
		}
		views, err := fixture.store.WorkerJobRevisions(ctx, workID)
		if err != nil {
			t.Fatal(err)
		}
		definition, step, lane := registered.Definition, jobBindingStepForTest(t, registered.Definition), reviewGateLane(t, "implementation")
		// The exact new packet shape recognition stays off for a member-free
		// legacy packet: its base alias identity is admitted as before.
		claimed := recordedPacketJobForTest(t, fixture.store, workID, views[0].Binding)
		claimedRaw := string(mustJSONValue(claimed))
		claimedRaw = claimedRaw[:len(claimedRaw)-1] + `,"JOB_ID":"` + views[0].Binding.JobID + `"}`
		packet := dispatchWorkerPacket(t, fixture.store, workID, step, "attempt:"+workID)
		packet["schema_version"] = WorkerPacketSchemaVersion
		packet["inputs"].(map[string]any)["worker_job"] = json.RawMessage(claimedRaw)
		aliasedPacket, marshalErr := json.Marshal(packet)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if _, err := validateWorkerPacketJob(ctx, fixture.store.DatabaseForTesting(), definition, workID, lane, aliasedPacket); err != nil {
			t.Fatalf("the member-free legacy packet with its base alias identity must stay admitted: %v", err)
		}
		// Any supplied architecture_binding spelling refuses on the old pin.
		bearing := recordedPacketJobForTest(t, fixture.store, workID, views[0].Binding)
		bearingRaw := string(mustJSONValue(bearing))
		bearingRaw = bearingRaw[:len(bearingRaw)-1] + `,"Architecture_Binding":null}`
		bearingPacket := dispatchWorkerPacket(t, fixture.store, workID, step, "attempt:"+workID+":bearing")
		bearingPacket["schema_version"] = WorkerPacketSchemaVersion
		bearingPacket["inputs"].(map[string]any)["worker_job"] = json.RawMessage(bearingRaw)
		bearingBytes, marshalErr := json.Marshal(bearingPacket)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		_, err = validateWorkerPacketJob(ctx, fixture.store.DatabaseForTesting(), definition, workID, lane, bearingBytes)
		requireJobBindingRefusal(t, err)
	})

	t.Run("dispatch admission refuses a stale bound revision without history moving", func(t *testing.T) {
		const workID = "job-binding-stale-ready"
		fixture := seedJobBindingFixture(t, workID, jobBindingPinRef, true, true, nil)
		fields := jobBindingRecordingFields("job:stale-ready", jobBindingSliceFiles, []string{"control:fixture"}, jobBindingMember(fixture.revision, fixture.digest, "binding-edit"), true)
		if err := recordJobBindingRevisionForTest(t, fixture, workID, fields); err != nil {
			t.Fatalf("the exact binding must record before the design moves: %v", err)
		}
		views, err := fixture.store.WorkerJobRevisions(ctx, workID)
		if err != nil {
			t.Fatal(err)
		}
		binding := views[0].Binding
		moveJobBindingDesignForTest(t, fixture.store, workID)
		before := readJobBindingState(t, fixture.store, workID, "job:stale-ready")
		requireJobBindingRefusal(t, requireWorkerJobRevisionStateReadyTx(ctx, fixture.store.DatabaseForTesting(), workID, binding))
		if after := readJobBindingState(t, fixture.store, workID, "job:stale-ready"); after != before {
			t.Fatalf("the stale dispatch refusal moved state: before %+v after %+v", before, after)
		}
	})

	t.Run("native readiness stays conjunctive beside the binding", func(t *testing.T) {
		const workID = "job-binding-native-conjunctive"
		fixture := seedJobBindingFixture(t, workID, jobBindingPinRef, true, true, nil)
		fields := jobBindingRecordingFields("job:native", jobBindingSliceFiles, []string{"control:fixture"}, jobBindingMember(fixture.revision, fixture.digest, "binding-edit"), true)
		if err := recordJobBindingRevisionForTest(t, fixture, workID, fields); err != nil {
			t.Fatalf("the bound ready recording with its prepared fixture oracle must hold: %v", err)
		}
		views, err := fixture.store.WorkerJobRevisions(ctx, workID)
		if err != nil {
			t.Fatal(err)
		}
		binding := views[0].Binding
		// The recorded preparation binds the subject it was qualified
		// against: moving the qualified candidate leaves the state-ready
		// admission intact and strands the native preparation exactly there.
		git, entry := oracleFixtureClaimedWorktree(t, fixture.store, workID)
		advanceOracleFixtureSubject(t, oracleRepairFixture{store: fixture.store, git: git, entry: entry}, workID, "2")
		if err := requireWorkerJobRevisionStateReadyTx(ctx, fixture.store.DatabaseForTesting(), workID, binding); err != nil {
			t.Fatalf("state-ready admission = %v, want the asserted bound revision ready before the native boundary", err)
		}
		err = requireWorkerJobRevisionReadyTx(ctx, fixture.store.DatabaseForTesting(), workID, binding)
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindMissingEvidence || !strings.Contains(failure.Detail, "job preparation is absent, foreign, or stale for its bundle and candidate") {
			t.Fatalf("full readiness = %v, want the exact stale-native-preparation refusal at the state-ready boundary", err)
		}
	})
}

// jobBindingRawMember marshals one binding member value as raw JSON bytes, so
// alias and duplicate key shapes survive into the event payload.
func jobBindingRawMember(t *testing.T, member any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(member)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// jobBindingEnvelopePayload builds one typed recording whose identity the
// caller controls; withBinding adds the slice-bound readiness-bearing content
// the fold's live authority validates.
func jobBindingEnvelopePayload(fixture jobBindingFixture, jobID string, withBinding bool) WorkerJobRecordedPayload {
	payload := WorkerJobRecordedPayload{
		JobID: jobID, Revision: 1, ContractVersion: 1,
		Objective: "envelope identity recording " + jobID, StoppingCondition: "the slice's declared test controls pass",
		ProjectScope: "project", PathScope: jobBindingSliceFiles, PredicateIDs: []string{}, Checks: []string{"control:fixture"},
		Prerequisites: []WorkerJobPrerequisite{}, UnresolvedRefs: []string{},
		Readiness: &WorkerJobReadiness{Ready: true, Evidence: []string{"evidence:return-route-verification"}},
	}
	if withBinding {
		payload.ArchitectureBinding = &architectureJobBinding{DesignRevision: fixture.revision, DesignDigest: fixture.digest, SliceID: "binding-edit"}
	}
	return payload
}

// applyJobBindingEnvelopeEventForTest appends one raw worker.job_recorded
// event whose digest covers the exact typed content, with one raw alias
// member spliced after the canonical typed members, so the case-insensitive
// typed decode and the canonical SQL readback read different identities from
// one recorded revision.
func applyJobBindingEnvelopeEventForTest(t *testing.T, s *Store, workID string, payload WorkerJobRecordedPayload, aliasRaw string) error {
	t.Helper()
	payload.Digest = DeriveWorkerJobDigest(payload)
	values := map[string]any{}
	if err := json.Unmarshal(mustJSONValue(payload), &values); err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, workID)
	values["work_id"] = workID
	values["expected_version"] = version
	values["resulting_version"] = version + 1
	raw := string(mustJSONValue(values))
	index := strings.LastIndex(raw, "}")
	if index < 1 {
		t.Fatal("the recording payload has no envelope to splice into")
	}
	event := Event{EventID: "job-binding-envelope-" + workID + "-" + payload.JobID, Kind: WorkerJobRecorded, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(22, 0).UTC(), PayloadVersion: 1, Payload: json.RawMessage(raw[:index] + "," + aliasRaw + "}")}
	return applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: workVersion(workID, version)})
}

// jobBindingSplicedActionPayload builds the record_worker_job action payload
// with the caller's raw architecture_binding bytes spliced in, so alias and
// duplicate member keys ride the exact bytes the action admission reads.
func jobBindingSplicedActionPayload(t *testing.T, fixture jobBindingFixture, workID string, bindingRaw string) json.RawMessage {
	t.Helper()
	fields := jobBindingRecordingFields("job:shape", jobBindingSliceFiles, []string{"control:fixture"}, nil, false)
	if oracleCapablePinForWork(t, fixture.store, workID) {
		bootstrapOracleFixtureSubject(t, fixture.store, workID)
		fields["acceptance_oracle"] = acceptanceOracleFieldsForTest(t, fixture.store, workID)
	}
	raw := string(mustJSONValue(fields))
	index := strings.LastIndex(raw, "}")
	if index < 1 {
		t.Fatal("the recording payload has no envelope to splice into")
	}
	return json.RawMessage(raw[:index] + `,"architecture_binding":` + bindingRaw + "}")
}

// jobBindingDispatchCapableDefinition rebrands the capable clone as the
// released implementation head, so the direct packet admission reaches its
// binding validation instead of the CD-0205 version gate. The rebrand never
// registers: the builtin registry keeps its released digests.
func jobBindingDispatchCapableDefinition(t *testing.T) WorkflowDefinition {
	t.Helper()
	registered := jobBindingDefinition(t, jobBindingPinRef, true, true)
	definition := registered.Definition
	definition.Ref = "workflow.implementation"
	definition.Version = 30
	return definition
}

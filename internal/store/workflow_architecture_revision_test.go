package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The bounded design-revision tests: revise_design appends the shared design
// record family through the one design writer, holds its step, and refuses
// everything the approved envelope, a stale revision, a live worker attempt,
// or the slices bound by dispatched work forbids. The route is unpublished on
// live heads: the fixture declares the hold action explicitly on a synthetic
// definition, the way a future architecture-capable pin will.
//
// The runtime tests prove what the route sources today. The job-side
// architecture-binding producer and the accepted-prefix join land in later
// slices; until then the binding fixtures seed the approved
// worker.job_recorded architecture_binding member shape at the log level and
// prove the reader join, not a substitute authority. Recording a job plans
// work; dispatching it starts the slice: only a binding whose exact recorded
// (job_id, revision) pair names a worker.dispatched event bounds the
// revision, so a recorded but never-dispatched job leaves its slice in the
// revisable suffix and its author records a new immutable job revision for
// the revised design. The integration-closure join — protecting a completed
// workerless integration point — lands with that integration event's producer
// and is not claimed here.

// revisionReason is the bounded revision motive every call carries.
const revisionReason = "repartition the unstarted suffix within the approved envelope"

// revisionFixtureDefinition clones the typed-design implementation pin and
// declares revise_design explicitly: hold mode, no approval, closed payload.
// The fixture ref stays outside the released range so the synthetic head can
// never publish.
func revisionFixtureDefinition(t *testing.T) RegisteredDefinition {
	t.Helper()
	base, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 6)
	if !ok {
		t.Fatal("typed design workflow is not registered")
	}
	shape := workflowFixtureShape(base.Definition, 6)
	shape.Ref = "workflow.test_design_revision"
	hasDispatch := false
	for _, action := range shape.ActionDefinitions {
		if action.ID == "dispatch_worker" {
			hasDispatch = true
		}
	}
	if !hasDispatch {
		shape.ActionDefinitions = append(shape.ActionDefinitions, currentActionDefinition("dispatch_worker", true))
		shape.AvailableActions = append(shape.AvailableActions, "dispatch_worker")
	}
	shape.ActionDefinitions = append(shape.ActionDefinitions, WorkflowActionDefinition{
		ID: "revise_design", Consequence: ActionInternalSQLite, Approval: ActionApprovalNone, ExecutionMode: ActionHold,
		Payload: WorkflowPayloadDefinition{Closed: true, Fields: []WorkflowPayloadField{
			{Name: "expected_design_revision", ValueType: PayloadInteger, Required: true, Minimum: workflowInt(1), Maximum: workflowInt(2147483647)},
			{Name: "reason", ValueType: PayloadString, Required: true, NonBlank: true, MinLength: workflowInt(1), MaxLength: workflowInt(2048)},
			{Name: "evidence_refs", ValueType: PayloadStringList, Required: true, MinItems: workflowInt(1), MaxItems: workflowInt(32)},
			{Name: "design_record", ValueType: PayloadObject, Required: true},
		}},
	})
	shape.AvailableActions = append(shape.AvailableActions, "revise_design")
	for i := range shape.StepGraph.Steps {
		switch shape.StepGraph.Steps[i].ID {
		case "planning":
			shape.StepGraph.Steps[i].Actions = append(shape.StepGraph.Steps[i].Actions, "revise_design")
		case "execution":
			shape.StepGraph.Steps[i].Actions = append(shape.StepGraph.Steps[i].Actions, "revise_design", "dispatch_worker")
		}
	}
	registered, err := BuiltinWorkflowRegistry().Register(shape)
	if err != nil {
		t.Fatal(err)
	}
	return registered
}

type revisionFixture struct {
	store    *Store
	owner    WorkflowActor
	ownerRef string
	version  int64
	step     string
	plan     WorkflowDesignArchitecture
	digest   string
}

// seedArchitectureRevisionFixture walks the synthetic definition to the named
// step: proposal, discovery, an optional typed design (a nil plan records the
// legacy architecture-absent design), the design step, an optional contract
// approval. With the approval the instance lands at execution; without it the
// instance stays at planning with no active contract.
func seedArchitectureRevisionFixture(t *testing.T, workID string, plan *WorkflowDesignArchitecture, withApproval, withDesign bool) revisionFixture {
	t.Helper()
	ctx := context.Background()
	s := openTemp(t)
	t.Cleanup(func() { s.Close() })
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	definition := revisionFixtureDefinition(t)
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	events := []Event{
		workflowEventWithActor("revision-owner-"+workID, WorkflowActorRecorded, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef, "agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": "agent"}),
		workflowEvent("revision-definition-"+workID, WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "ref": definition.Definition.Ref, "version": definition.Definition.Version, "digest": definition.Digest, "work_kind": string(definition.Definition.WorkKind)}),
		workflowActionCompletedFixture("revision-proposal-"+workID, workID, ownerRef, 4, "proposal", "record_proposal"),
		workflowActionCompletedFixture("revision-discovery-"+workID, workID, ownerRef, 5, "discovery", "record_discovery"),
	}
	var version int64
	if withDesign {
		events = append(events, projectionDesignEvent(t, workID, "revision-design-"+workID, 6, "first", plan))
		events = append(events, workflowActionCompletedFixture("revision-design-step-"+workID, workID, ownerRef, 7, "design", "record_design"))
		version = 8
	} else {
		events = append(events, workflowActionCompletedFixture("revision-design-step-"+workID, workID, ownerRef, 6, "design", "record_design"))
		version = 7
	}
	if withApproval {
		events = append(events, workflowEventWithActor("revision-contract-"+workID, WorkflowContractApproved, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "contract_version": 1, "premise": "deliver the checked change", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:revision", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"}, "required_evidence": []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{}, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite"}))
		version++
		events = append(events, workflowActionCompletedFixture("revision-approved-"+workID, workID, ownerRef, version, "planning", "approve_contract"))
		version++
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: events, ExpectedVersions: workVersion(workID, 2)}); err != nil {
		t.Fatal(err)
	}
	fixture := revisionFixture{store: s, owner: owner, ownerRef: ownerRef, version: version, step: "planning"}
	if withApproval {
		fixture.step = "execution"
	}
	if plan != nil {
		fixture.plan = *plan
		current, _, err := readCurrentWorkflowDesign(ctx, s.DatabaseForTesting(), workID)
		if err != nil || current == nil || current.Digest == "" {
			t.Fatalf("seeded design carries no digest: %v", err)
		}
		fixture.digest = current.Digest
	}
	return fixture
}

// revisionReplacementRaw builds one revise_design design_record member: the
// replacement typed content, with the caller's architecture.
func revisionReplacementRaw(t *testing.T, choice string, plan *WorkflowDesignArchitecture) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"approach": "Revised approach", "decisions": projectionDesignContent(choice).Decisions, "touched_refs": projectionDesignContent(choice).TouchedRefs, "architecture": plan})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// revisionActionPayload builds the revise_design action payload with the
// bounded reason constant and the valid evidence set.
func revisionActionPayload(t *testing.T, expectedRevision int64, designRaw json.RawMessage) json.RawMessage {
	return revisionActionPayloadEvidence(t, expectedRevision, revisionReason, designRaw, revisionEvidence)
}

// revisionEvidence is the bounded evidence set a valid revision call carries.
var revisionEvidence = []string{"verification:revision-join", "review:revision-envelope"}

// revisionActionPayloadEvidence builds the payload with the caller's
// evidence-references input, so a test can present a closed payload that
// omits, empties, duplicates, or overfills it.
func revisionActionPayloadEvidence(t *testing.T, expectedRevision int64, reason string, designRaw json.RawMessage, evidence []string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"expected_design_revision": expectedRevision, "reason": reason, "design_record": designRaw, "evidence_refs": evidence})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// runReviseDesignAction executes the action through the full action path.
func runReviseDesignAction(t *testing.T, fixture revisionFixture, workID string, payload json.RawMessage, operation string) error {
	t.Helper()
	ctx := context.Background()
	version := readRevisionWorkState(t, fixture.store, workID).version
	tx, err := fixture.store.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := applyWorkflowActionRawTx(ctx, tx, newFoldScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: "revise_design", Payload: payload, Actor: fixture.owner,
		AcceptedInputsDigest: testDigest("revision-" + operation), IdempotencyIdentity: operation, OperationID: operation,
		PrincipalRef: fixture.owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operation,
		RequestID: "request:" + operation, ContractDigest: testManifestDigest, Now: time.Unix(30, 0).UTC(),
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// revisionWorkState is the durable state a revision must respect.
type revisionWorkState struct {
	step         string
	version      int64
	revision     int64
	digest       string
	contracts    int64
	designRows   int64
	openAttempts int64
	startedCount int64
}

func readRevisionWorkState(t *testing.T, s *Store, workID string) revisionWorkState {
	t.Helper()
	state := revisionWorkState{revision: -1}
	if err := s.DatabaseForTesting().QueryRow(`SELECT current_step,(SELECT version FROM work_items WHERE id=workflow_instances.work_id) FROM workflow_instances WHERE work_id=?`, workID).Scan(&state.step, &state.version); err != nil {
		t.Fatal(err)
	}
	err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(revision,-1),COALESCE(digest,'') FROM workflow_design_records WHERE work_id=? ORDER BY work_version DESC LIMIT 1`, workID).Scan(&state.revision, &state.digest)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	for _, row := range []struct {
		query string
		want  *int64
	}{
		{`SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, &state.contracts},
		{`SELECT count(*) FROM workflow_design_records WHERE work_id=?`, &state.designRows},
		{`SELECT count(*) FROM worker_attempts WHERE work_id=? AND lifecycle_state IN ('in_flight','dispatched')`, &state.openAttempts},
		{`SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind='workflow.action_started'`, &state.startedCount},
	} {
		if err := s.DatabaseForTesting().QueryRow(row.query, workID).Scan(row.want); err != nil {
			t.Fatal(err)
		}
	}
	return state
}

func requireRevisionRefusal(t *testing.T, err error, want FailureKind, wantText string) {
	t.Helper()
	if err == nil {
		t.Fatal("the store accepted a revision it must refuse")
	}
	assertFailureKind(t, err, want)
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("refusal is not typed: %v", err)
	}
	if !strings.Contains(failure.Detail, wantText) {
		t.Fatalf("refusal detail = %q, want it to name %q", failure.Detail, wantText)
	}
}

// seedRecordedJobBindingFixture inserts one recorded-binding log row: a
// worker.job_recorded payload carrying the approved architecture_binding
// member whose fold-side producer lands with the job-binding slice. The row
// is recorded-log evidence only — it carries no worker attempt, dispatches
// nothing, and never starts a slice by itself.
func seedRecordedJobBindingFixture(t *testing.T, s *Store, workID, eventID, jobID string, jobRevision, designRevision int64, designDigest, sliceID string) {
	t.Helper()
	payload := fmt.Sprintf(`{"job_id":%q,"revision":%d,"contract_version":1,"objective":"bounded bound-slice job","stopping_condition":"the bound checks pass","path_scope":["internal/store"],"predicate_ids":[],"checks":[],"prerequisites":[],"unresolved_refs":[],"digest":"sha256:%s","architecture_binding":{"design_revision":%d,"design_digest":%q,"slice_id":%q}}`, jobID, jobRevision, strings.Repeat("b", 64), designRevision, designDigest, sliceID)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,1,?)`, "revision-job-"+eventID, WorkerJobRecorded, string(SubjectWorkItem), workID, "worker:test", time.Unix(21, 0).UTC().Format(time.RFC3339Nano), payload); err != nil {
		t.Fatal(err)
	}
}

// seedLogOnlyJobBindingFixture records one revision-1 binding row under the
// fixture's derived job identity.
func seedLogOnlyJobBindingFixture(t *testing.T, s *Store, workID, eventID string, designRevision int64, designDigest, sliceID string) {
	t.Helper()
	seedRecordedJobBindingFixture(t, s, workID, eventID, "job:revision-bound-"+eventID, 1, designRevision, designDigest, sliceID)
}

// seedDispatchedJobRow inserts the dispatched evidence for one recorded job
// revision: a worker.dispatched event whose worker_job binding names the
// exact (job_id, revision) pair the recording carries under its digest.
func seedDispatchedJobRow(t *testing.T, s *Store, workID, eventID, jobID string, jobRevision int64) {
	t.Helper()
	lane := BuiltinLaneDefinitions()[0]
	dispatch := map[string]any{
		"attempt_id": "attempt:revision-" + eventID, "lane_id": lane.ID, "lane_version": lane.Version, "lane_digest": lane.Digest,
		"capability_class": lane.CapabilityClass, "readback_model": preferredModelForLane(lane),
		"packet_schema_version": WorkerPacketSchemaVersion, "report_schema_version": WorkerReportSchemaVersion,
		"packet_digest": "sha256:" + strings.Repeat("d", 64),
		"worker_job":    map[string]any{"job_id": jobID, "revision": jobRevision, "digest": "sha256:" + strings.Repeat("b", 64)},
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,6,?)`, "revision-dispatch-"+eventID, WorkerDispatched, string(SubjectWorkItem), workID, "worker:test", time.Unix(22, 0).UTC().Format(time.RFC3339Nano), mustJSONValue(dispatch)); err != nil {
		t.Fatal(err)
	}
}

// seedDispatchedJobBindingFixture records one revision-1 binding row and adds
// the dispatched evidence that starts its slice. The rows are recorded-log
// evidence and prove the reader join; the dispatch fold itself refuses any
// dispatched binding no recorded revision backs.
func seedDispatchedJobBindingFixture(t *testing.T, s *Store, workID, eventID string, designRevision int64, designDigest, sliceID string) {
	t.Helper()
	jobID := "job:revision-bound-" + eventID
	seedRecordedJobBindingFixture(t, s, workID, eventID, jobID, 1, designRevision, designDigest, sliceID)
	seedDispatchedJobRow(t, s, workID, eventID, jobID, 1)
}

// seedWorkerCompletedFixture adds the terminal worker.completed evidence for
// one dispatched attempt: the job ran and reported, so its slice is a
// completed obligation.
func seedWorkerCompletedFixture(t *testing.T, s *Store, workID, eventID string) {
	t.Helper()
	completed := map[string]any{
		"attempt_id": "attempt:revision-" + eventID, "readback_model": preferredModelForLane(BuiltinLaneDefinitions()[0]),
		"report_schema_version": WorkerReportSchemaVersion, "evidence_origin": WorkerEvidenceLegacyUnavailable,
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,7,?)`, "revision-completed-"+eventID, WorkerCompleted, string(SubjectWorkItem), workID, "worker:test", time.Unix(23, 0).UTC().Format(time.RFC3339Nano), mustJSONValue(completed)); err != nil {
		t.Fatal(err)
	}
}

// seedReadyWorkerJobFixture records one ready job revision through the real
// worker.job_recorded fold, so the existing job-readiness producer owns every
// derived value.
func seedReadyWorkerJobFixture(t *testing.T, s *Store, workID, jobID string, revision, version int64, actorRef string) {
	t.Helper()
	expected, next := version, version+1
	job := WorkerJobRecordedPayload{
		WorkflowVersionFields: WorkflowVersionFields{WorkID: workID, ExpectedVersion: &expected, ResultingVersion: &next},
		JobID:                 jobID, Revision: revision, ContractVersion: 1,
		Objective: "bounded prospective job", StoppingCondition: "the prospective checks pass",
		PathScope: []string{"internal/store"}, PredicateIDs: []string{}, Checks: []string{},
		Prerequisites: []WorkerJobPrerequisite{}, UnresolvedRefs: []string{},
		Readiness: &WorkerJobReadiness{Ready: true, Evidence: []string{"verification:prospective"}},
	}
	job.Digest = DeriveWorkerJobDigest(job)
	event := workflowEventWithActor(fmt.Sprintf("revision-ready-%s-%d", jobID, revision), WorkerJobRecorded, workID, actorRef, map[string]any{})
	event.Payload, _ = json.Marshal(job)
	event.PayloadVersion = 1
	if err := applyWorkflowTestOperationDirect(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("cannot record the prospective job revision: %v", err)
	}
}

// revisionJobReady evaluates the existing job-readiness producer against one
// recorded job revision row.
func revisionJobReady(t *testing.T, s *Store, workID, jobID string, revision int64) bool {
	t.Helper()
	var ready int
	if err := s.DatabaseForTesting().QueryRow(`SELECT CASE WHEN `+workerJobReadyPredicate+` THEN 1 ELSE 0 END FROM worker_job_revisions j WHERE j.work_id=? AND j.job_id=? AND j.revision=?`, workID, jobID, revision).Scan(&ready); err != nil {
		t.Fatal(err)
	}
	return ready == 1
}

func revisionAppendedSlice() WorkflowDesignArchitectureSlice {
	return WorkflowDesignArchitectureSlice{
		SliceID: "suffix-slice", Kind: DesignArchitectureSliceKindEdit, Ordinal: 1,
		OwnerID: "store-arch-owner", Files: []string{"internal/store/workflow_architecture_test.go"},
		InvariantIDs: []string{"closed-plan"}, ReferenceIDs: []string{"design-reference"},
		Tests: []string{"control:design-architecture-value"}, EndState: "the appended suffix slice passes",
		MaxChangedLines: 100,
	}
}

func TestArchitectureRevision(t *testing.T) {
	ctx := context.Background()

	t.Run("revision appends the design family and holds the step", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedArchitectureRevisionFixture(t, "revision-hold", &plan, true, true)
		const workID = "revision-hold"
		before := readRevisionWorkState(t, fixture.store, workID)
		if before.step != "execution" || before.revision != 1 || before.contracts != 1 || before.designRows != 1 || before.openAttempts != 0 {
			t.Fatalf("seeded state = %+v, want execution at revision 1 with one contract and no attempts", before)
		}
		replacement := plan
		payload := revisionActionPayload(t, 1, revisionReplacementRaw(t, "second", &replacement))
		if err := runReviseDesignAction(t, fixture, workID, payload, "revision-hold-op"); err != nil {
			t.Fatalf("revision refused: %v", err)
		}
		after := readRevisionWorkState(t, fixture.store, workID)
		if after.revision != 2 || after.designRows != 2 {
			t.Fatalf("revised state = %+v, want design revision 2 over two retained rows", after)
		}
		if after.digest == before.digest {
			t.Fatal("the replacement kept the predecessor digest; the revision never re-derived the content digest")
		}
		// The hold: same step, same contract, same attempt surface, no step
		// start, so the retry window the step owns is untouched.
		if after.step != "execution" || after.contracts != 1 || after.openAttempts != 0 || after.startedCount != 0 {
			t.Fatalf("revised state = %+v, want the step, contract, attempt wall, and step starts unchanged", after)
		}
		var completions int
		if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind='workflow.action_completed' AND json_extract(payload,'$.action_id')='revise_design'`, workID).Scan(&completions); err != nil {
			t.Fatal(err)
		}
		if completions != 1 {
			t.Fatalf("revise_design completions = %d, want 1", completions)
		}
		// The historical revision stays readable and the shared writer kept
		// the payload version the architecture-bearing family uses.
		var payloadVersion int
		if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT payload_version FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind='workflow.design_recorded' ORDER BY seq DESC LIMIT 1`, workID).Scan(&payloadVersion); err != nil {
			t.Fatal(err)
		}
		if payloadVersion != 2 {
			t.Fatalf("revision design payload version = %d, want 2", payloadVersion)
		}
	})

	t.Run("a stale expected revision refuses with zero effects", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedArchitectureRevisionFixture(t, "revision-stale", &plan, true, true)
		const workID = "revision-stale"
		before := readRevisionWorkState(t, fixture.store, workID)
		payload := revisionActionPayload(t, 2, revisionReplacementRaw(t, "stale", &plan))
		err := runReviseDesignAction(t, fixture, workID, payload, "revision-stale-op")
		requireRevisionRefusal(t, err, KindStaleContext, "no longer current")
		if after := readRevisionWorkState(t, fixture.store, workID); after != before {
			t.Fatalf("stale refusal moved state: before %+v after %+v", before, after)
		}
	})

	t.Run("revision requires the approved contract", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedArchitectureRevisionFixture(t, "revision-nocontract", &plan, false, true)
		const workID = "revision-nocontract"
		if state := readRevisionWorkState(t, fixture.store, workID); state.step != "planning" || state.contracts != 0 {
			t.Fatalf("seeded state = %+v, want planning without a contract", state)
		}
		payload := revisionActionPayload(t, 1, revisionReplacementRaw(t, "unapproved", &plan))
		err := runReviseDesignAction(t, fixture, workID, payload, "revision-nocontract-op")
		requireRevisionRefusal(t, err, KindInvariantViolation, "approved workflow contract is missing")
	})

	t.Run("revision requires an existing typed design", func(t *testing.T) {
		legacy := projectionArchitecture()
		fixture := seedArchitectureRevisionFixture(t, "revision-nodesign", nil, true, false)
		const workID = "revision-nodesign"
		payload := revisionActionPayload(t, 1, revisionReplacementRaw(t, "legacy", &legacy))
		err := runReviseDesignAction(t, fixture, workID, payload, "revision-nodesign-op")
		requireRevisionRefusal(t, err, KindInvalidPayload, "requires an existing typed design")
	})

	t.Run("revision requires the recorded architecture", func(t *testing.T) {
		legacy := projectionArchitecture()
		fixture := seedArchitectureRevisionFixture(t, "revision-noarch", nil, true, true)
		const workID = "revision-noarch"
		if state := readRevisionWorkState(t, fixture.store, workID); state.revision != -1 {
			t.Fatalf("legacy design revision = %d, want no derived revision", state.revision)
		}
		payload := revisionActionPayload(t, 1, revisionReplacementRaw(t, "legacy", &legacy))
		err := runReviseDesignAction(t, fixture, workID, payload, "revision-noarch-op")
		requireRevisionRefusal(t, err, KindInvalidPayload, "requires the recorded architecture")
	})

	t.Run("revision refuses while a worker attempt is not terminal", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedArchitectureRevisionFixture(t, "revision-attempt", &plan, true, true)
		const workID = "revision-attempt"
		state := readRevisionWorkState(t, fixture.store, workID)
		dispatch := workflowEventWithActor("revision-dispatch-"+workID, WorkflowActionCompleted, workID, fixture.ownerRef, map[string]any{
			"work_id": workID, "expected_version": state.version, "resulting_version": state.version + 1,
			"step_id": "execution", "action_id": "dispatch_worker", "attempt_epoch": 1,
			"result_evidence_refs": []string{}, "changed_refs": []string{workID}, "actor_ref": fixture.ownerRef,
			"worker_attempt_id": "attempt:" + workID + ":1", "worker_lane_id": "implement", "worker_lane_version": 1,
			"worker_lane_digest": "sha256:" + strings.Repeat("c", 64), "worker_capability_class": "implementation",
		})
		dispatch.PayloadVersion = 4
		if err := applyWorkflowTestOperation(ctx, fixture.store, Operation{Events: []Event{dispatch}, ExpectedVersions: workVersion(workID, state.version)}); err != nil {
			t.Fatalf("cannot seed the in-flight attempt: %v", err)
		}
		before := readRevisionWorkState(t, fixture.store, workID)
		if before.openAttempts != 1 {
			t.Fatalf("seeded attempts = %d, want one in-flight attempt", before.openAttempts)
		}
		payload := revisionActionPayload(t, 1, revisionReplacementRaw(t, "live", &plan))
		err := runReviseDesignAction(t, fixture, workID, payload, "revision-attempt-op")
		requireRevisionRefusal(t, err, KindIllegalLifecycleTransition, "terminal")
		if after := readRevisionWorkState(t, fixture.store, workID); after != before {
			t.Fatalf("live-attempt refusal moved state: before %+v after %+v", before, after)
		}
	})

	t.Run("the approved envelope is immutable", func(t *testing.T) {
		cases := []struct {
			name   string
			change func(plan *WorkflowDesignArchitecture)
		}{
			{"an invariant statement", func(plan *WorkflowDesignArchitecture) {
				plan.Invariants[0].Statement = "a silently changed invariant"
			}},
			{"an owner identity", func(plan *WorkflowDesignArchitecture) {
				plan.Owners[0].Symbol = "aDifferentOwner"
			}},
			{"a file role", func(plan *WorkflowDesignArchitecture) {
				plan.Files[0].Role = DesignArchitectureFileRoleTest
			}},
			{"the unchanged-file set", func(plan *WorkflowDesignArchitecture) {
				plan.UnchangedFiles = nil
			}},
		}
		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				plan := projectionArchitecture()
				fixture := seedArchitectureRevisionFixture(t, "revision-envelope", &plan, true, true)
				const workID = "revision-envelope"
				before := readRevisionWorkState(t, fixture.store, workID)
				replacement := plan
				testCase.change(&replacement)
				payload := revisionActionPayload(t, 1, revisionReplacementRaw(t, "envelope", &replacement))
				err := runReviseDesignAction(t, fixture, workID, payload, "revision-envelope-op")
				requireRevisionRefusal(t, err, KindInvalidPayload, "approved envelope")
				if after := readRevisionWorkState(t, fixture.store, workID); after != before {
					t.Fatalf("envelope refusal moved state: before %+v after %+v", before, after)
				}
			})
		}
	})

	t.Run("capacity reallocation keeps the same total", func(t *testing.T) {
		t.Run("the same total revises", func(t *testing.T) {
			plan := projectionArchitecture()
			fixture := seedArchitectureRevisionFixture(t, "revision-capacity", &plan, true, true)
			const workID = "revision-capacity"
			replacement := plan
			replacement.Capacity.TaskPremise -= 256
			replacement.Capacity.OutcomePredicates += 256
			payload := revisionActionPayload(t, 1, revisionReplacementRaw(t, "realloc", &replacement))
			if err := runReviseDesignAction(t, fixture, workID, payload, "revision-capacity-op"); err != nil {
				t.Fatalf("same-total reallocation refused: %v", err)
			}
			if state := readRevisionWorkState(t, fixture.store, workID); state.revision != 2 {
				t.Fatalf("reallocation revision = %d, want 2", state.revision)
			}
		})
		t.Run("a changed total refuses", func(t *testing.T) {
			plan := projectionArchitecture()
			fixture := seedArchitectureRevisionFixture(t, "revision-captop", &plan, true, true)
			const workID = "revision-captop"
			before := readRevisionWorkState(t, fixture.store, workID)
			replacement := plan
			replacement.Capacity.TaskPremise -= 256
			payload := revisionActionPayload(t, 1, revisionReplacementRaw(t, "shrink", &replacement))
			err := runReviseDesignAction(t, fixture, workID, payload, "revision-captop-op")
			requireRevisionRefusal(t, err, KindInvalidPayload, "capacity")
			if after := readRevisionWorkState(t, fixture.store, workID); after != before {
				t.Fatalf("capacity refusal moved state: before %+v after %+v", before, after)
			}
		})
	})

	// This group pins the started-set rule: a slice bound by dispatched work
	// is immutable, new slices may only enter the unstarted suffix above the
	// highest dispatched ordinal, and a binding whose design revision lies
	// beyond the current one is history a later recording owns.
	t.Run("slices bound by dispatched jobs are immutable", func(t *testing.T) {
		mutateBoundSlice := func(change func(slice *WorkflowDesignArchitectureSlice)) func(plan *WorkflowDesignArchitecture) {
			return func(plan *WorkflowDesignArchitecture) {
				for i := range plan.Slices {
					if plan.Slices[i].SliceID == "value-slice" {
						change(&plan.Slices[i])
					}
				}
			}
		}
		cases := []struct {
			name   string
			change func(plan *WorkflowDesignArchitecture)
			text   string
		}{
			{"the bound end state", mutateBoundSlice(func(slice *WorkflowDesignArchitectureSlice) { slice.EndState = "a rewritten obligation" }), "bound by dispatched work"},
			{"the bound budget", mutateBoundSlice(func(slice *WorkflowDesignArchitectureSlice) { slice.MaxChangedLines = 500 }), "bound by dispatched work"},
			{"the bound kind", mutateBoundSlice(func(slice *WorkflowDesignArchitectureSlice) { slice.Kind = DesignArchitectureSliceKindIntegration }), "bound by dispatched work"},
			{"the bound files", mutateBoundSlice(func(slice *WorkflowDesignArchitectureSlice) {
				slice.Files = []string{"internal/store/workflow_architecture_test.go"}
			}), "bound by dispatched work"},
		}
		for _, testCase := range cases {
			t.Run(testCase.name+" refuses", func(t *testing.T) {
				plan := projectionArchitecture()
				fixture := seedArchitectureRevisionFixture(t, "revision-bound", &plan, true, true)
				const workID = "revision-bound"
				seedDispatchedJobBindingFixture(t, fixture.store, workID, "bound-1", 1, fixture.digest, "value-slice")
				before := readRevisionWorkState(t, fixture.store, workID)
				replacement := plan
				testCase.change(&replacement)
				payload := revisionActionPayload(t, 1, revisionReplacementRaw(t, "bound", &replacement))
				err := runReviseDesignAction(t, fixture, workID, payload, "revision-bound-op")
				requireRevisionRefusal(t, err, KindInvalidPayload, testCase.text)
				if after := readRevisionWorkState(t, fixture.store, workID); after != before {
					t.Fatalf("bound-slice refusal moved state: before %+v after %+v", before, after)
				}
			})
		}
		t.Run("a preserved bound slice admits a suffix append", func(t *testing.T) {
			plan := projectionArchitecture()
			fixture := seedArchitectureRevisionFixture(t, "revision-append", &plan, true, true)
			const workID = "revision-append"
			seedDispatchedJobBindingFixture(t, fixture.store, workID, "bound-append", 1, fixture.digest, "value-slice")
			replacement := plan
			replacement.Slices = append(replacement.Slices, revisionAppendedSlice())
			payload := revisionActionPayload(t, 1, revisionReplacementRaw(t, "append", &replacement))
			if err := runReviseDesignAction(t, fixture, workID, payload, "revision-append-op"); err != nil {
				t.Fatalf("suffix append refused: %v", err)
			}
			if state := readRevisionWorkState(t, fixture.store, workID); state.revision != 2 {
				t.Fatalf("append revision = %d, want 2", state.revision)
			}
		})
		t.Run("a new slice outside the unstarted suffix refuses", func(t *testing.T) {
			plan := projectionArchitecture()
			fixture := seedArchitectureRevisionFixture(t, "revision-suffix", &plan, true, true)
			const workID = "revision-suffix"
			seedDispatchedJobBindingFixture(t, fixture.store, workID, "bound-suffix", 1, fixture.digest, "value-slice")
			replacement := plan
			appended := revisionAppendedSlice()
			appended.Ordinal = 0
			appended.SliceID = "suffix-slice-early"
			replacement.Slices = append(replacement.Slices, appended)
			payload := revisionActionPayload(t, 1, revisionReplacementRaw(t, "early", &replacement))
			err := runReviseDesignAction(t, fixture, workID, payload, "revision-suffix-op")
			requireRevisionRefusal(t, err, KindInvalidPayload, "unstarted suffix")
		})
		t.Run("a binding naming an unknown slice refuses", func(t *testing.T) {
			plan := projectionArchitecture()
			fixture := seedArchitectureRevisionFixture(t, "revision-unknown", &plan, true, true)
			const workID = "revision-unknown"
			seedDispatchedJobBindingFixture(t, fixture.store, workID, "bound-unknown", 1, fixture.digest, "slice:never-planned")
			payload := revisionActionPayload(t, 1, revisionReplacementRaw(t, "unknown", &plan))
			err := runReviseDesignAction(t, fixture, workID, payload, "revision-unknown-op")
			requireRevisionRefusal(t, err, KindInvariantViolation, "unknown slice")
		})
		t.Run("a binding beyond the current revision does not bound it", func(t *testing.T) {
			plan := projectionArchitecture()
			fixture := seedArchitectureRevisionFixture(t, "revision-future", &plan, true, true)
			const workID = "revision-future"
			seedDispatchedJobBindingFixture(t, fixture.store, workID, "bound-future", 2, fixture.digest, "value-slice")
			replacement := plan
			for i := range replacement.Slices {
				if replacement.Slices[i].SliceID == "value-slice" {
					replacement.Slices[i].EndState = "rewritten while the binding points beyond the current revision"
				}
			}
			payload := revisionActionPayload(t, 1, revisionReplacementRaw(t, "future", &replacement))
			if err := runReviseDesignAction(t, fixture, workID, payload, "revision-future-op"); err != nil {
				t.Fatalf("a binding beyond the current revision must not bound it: %v", err)
			}
		})
	})

	// The unstarted suffix stays revisable: a recorded job that was never
	// dispatched starts no slice, and the revision repartitions and reorders
	// that suffix within the immutable envelope.
	t.Run("an undispatched recorded job does not start its slice", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedArchitectureRevisionFixture(t, "revision-undispatched", &plan, true, true)
		const workID = "revision-undispatched"
		probe := fixture.store.DatabaseForTesting()
		var attempts, dispatches int
		if err := probe.QueryRow(`SELECT count(*) FROM worker_attempts WHERE work_id=?`, workID).Scan(&attempts); err != nil {
			t.Fatal(err)
		}
		if err := probe.QueryRow(`SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind='workflow.action_completed' AND json_extract(payload,'$.action_id')='dispatch_worker'`, workID).Scan(&dispatches); err != nil {
			t.Fatal(err)
		}
		if attempts != 0 || dispatches != 0 {
			t.Fatalf("undispatched proof: worker attempts=%d dispatch completions=%d, want 0 and 0", attempts, dispatches)
		}
		seedLogOnlyJobBindingFixture(t, fixture.store, workID, "undispatched-1", 1, fixture.digest, "value-slice")
		replacement := plan
		for i := range replacement.Slices {
			if replacement.Slices[i].SliceID == "value-slice" {
				replacement.Slices[i].EndState = "the value tests pass with a re-planned end state"
				replacement.Slices[i].MaxChangedLines = 300
			}
		}
		payload := revisionActionPayload(t, 1, revisionReplacementRaw(t, "unstarted", &replacement))
		if err := runReviseDesignAction(t, fixture, workID, payload, "revision-undispatched-op"); err != nil {
			t.Fatalf("a recorded but never-dispatched job must not start its slice; the unstarted suffix stays revisable: %v", err)
		}
		if state := readRevisionWorkState(t, fixture.store, workID); state.revision != 2 {
			t.Fatalf("revision = %d, want the first-order design revision 2", state.revision)
		}
	})

	// A dispatched obligation is started work even while it is unresolved,
	// and a completed job is a finished obligation: neither can be renamed,
	// weakened, reclassified, or split away to reset its budget.
	t.Run("a dispatched unresolved job freezes its slice", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedArchitectureRevisionFixture(t, "revision-dispatched", &plan, true, true)
		const workID = "revision-dispatched"
		seedDispatchedJobBindingFixture(t, fixture.store, workID, "dispatched-1", 1, fixture.digest, "value-slice")
		before := readRevisionWorkState(t, fixture.store, workID)
		replacement := plan
		for i := range replacement.Slices {
			if replacement.Slices[i].SliceID == "value-slice" {
				replacement.Slices[i].EndState = "a renamed dispatched obligation"
			}
		}
		payload := revisionActionPayload(t, 1, revisionReplacementRaw(t, "dispatched", &replacement))
		err := runReviseDesignAction(t, fixture, workID, payload, "revision-dispatched-op")
		requireRevisionRefusal(t, err, KindInvalidPayload, "bound by dispatched work")
		if after := readRevisionWorkState(t, fixture.store, workID); after != before {
			t.Fatalf("dispatched refusal moved state: before %+v after %+v", before, after)
		}
	})

	t.Run("a completed job freezes its slice", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedArchitectureRevisionFixture(t, "revision-completed", &plan, true, true)
		const workID = "revision-completed"
		seedDispatchedJobBindingFixture(t, fixture.store, workID, "completed-1", 1, fixture.digest, "value-slice")
		seedWorkerCompletedFixture(t, fixture.store, workID, "completed-1")
		before := readRevisionWorkState(t, fixture.store, workID)
		replacement := plan
		for i := range replacement.Slices {
			if replacement.Slices[i].SliceID == "value-slice" {
				replacement.Slices[i].MaxChangedLines = 900
			}
		}
		payload := revisionActionPayload(t, 1, revisionReplacementRaw(t, "completed", &replacement))
		err := runReviseDesignAction(t, fixture, workID, payload, "revision-completed-op")
		requireRevisionRefusal(t, err, KindInvalidPayload, "bound by dispatched work")
		if after := readRevisionWorkState(t, fixture.store, workID); after != before {
			t.Fatalf("completed refusal moved state: before %+v after %+v", before, after)
		}
	})

	// The dispatch join is the exact recorded (job_id, revision) pair: a new
	// revision of the same job is a different prospective obligation, and an
	// undispatched one freezes nothing, not even a slice it names above the
	// dispatched floor.
	t.Run("a dispatch bounds only the exact recorded revision it dispatched", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedArchitectureRevisionFixture(t, "revision-revjoin", &plan, true, true)
		const workID = "revision-revjoin"
		shared := "job:revision-revjoin"
		seedRecordedJobBindingFixture(t, fixture.store, workID, "revjoin-1", shared, 1, 1, fixture.digest, "value-slice")
		seedDispatchedJobRow(t, fixture.store, workID, "revjoin-1", shared, 1)
		seedRecordedJobBindingFixture(t, fixture.store, workID, "revjoin-2", shared, 2, 1, fixture.digest, "suffix-slice")
		replacement := plan
		appended := revisionAppendedSlice()
		appended.EndState = "a replanned suffix the undispatched revision-2 binding names"
		replacement.Slices = append(replacement.Slices, appended)
		payload := revisionActionPayload(t, 1, revisionReplacementRaw(t, "revjoin", &replacement))
		if err := runReviseDesignAction(t, fixture, workID, payload, "revision-revjoin-op"); err != nil {
			t.Fatalf("an undispatched revision-2 binding must not freeze the suffix slice: %v", err)
		}
	})

	// The revision call supplies its evidence references: a valid bounded set
	// revises, and an input that omits, empties, duplicates, or overfills the
	// set refuses with zero effects.
	t.Run("revision evidence references are bounded and required", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedArchitectureRevisionFixture(t, "revision-evidence", &plan, true, true)
		const workID = "revision-evidence"
		payload := revisionActionPayload(t, 1, revisionReplacementRaw(t, "evidence", &plan))
		if err := runReviseDesignAction(t, fixture, workID, payload, "revision-evidence-valid-op"); err != nil {
			t.Fatalf("a bounded evidence set must revise: %v", err)
		}
		if state := readRevisionWorkState(t, fixture.store, workID); state.revision != 2 {
			t.Fatalf("evidence revision = %d, want 2", state.revision)
		}
		overbound := make([]string, 33)
		for i := range overbound {
			overbound[i] = fmt.Sprintf("verification:item-%d", i)
		}
		cases := []struct {
			name     string
			evidence []string
		}{
			{"omitted", nil},
			{"empty", []string{}},
			{"duplicated", []string{"verification:once", "verification:once"}},
			{"overbound", overbound},
		}
		for _, testCase := range cases {
			t.Run(testCase.name+" evidence refuses", func(t *testing.T) {
				plan := projectionArchitecture()
				fixture := seedArchitectureRevisionFixture(t, "revision-evidence-refusal", &plan, true, true)
				const workID = "revision-evidence-refusal"
				before := readRevisionWorkState(t, fixture.store, workID)
				payload := revisionActionPayloadEvidence(t, 1, revisionReason, revisionReplacementRaw(t, "evidence-invalid", &plan), testCase.evidence)
				err := runReviseDesignAction(t, fixture, workID, payload, "revision-evidence-refusal-op")
				requireRevisionRefusal(t, err, KindInvalidPayload, "evidence")
				if after := readRevisionWorkState(t, fixture.store, workID); after != before {
					t.Fatalf("evidence refusal moved state: before %+v after %+v", before, after)
				}
			})
		}
	})

	// The constructor owns the evidence bounds standalone: the declared
	// payload gate is one wall, never the only one, so a direct call with a
	// valid set assembles the revision events and every malformed set refuses
	// before any effect.
	t.Run("the revision constructor enforces the evidence bounds itself", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedArchitectureRevisionFixture(t, "revision-evidence-direct", &plan, true, true)
		const workID = "revision-evidence-direct"
		ctx := context.Background()
		overbound := make([]string, 33)
		for i := range overbound {
			overbound[i] = fmt.Sprintf("verification:item-%d", i)
		}
		call := func(evidence []string) error {
			expected := readRevisionWorkState(t, fixture.store, workID).version
			tx, err := fixture.store.DatabaseForTesting().BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			fields := map[string]json.RawMessage{
				"expected_design_revision": json.RawMessage(`1`),
				"reason":                   json.RawMessage(`"` + revisionReason + `"`),
				"design_record":            revisionReplacementRaw(t, "direct", &plan),
				"evidence_refs":            mustJSONValue(evidence),
			}
			_, err = workflowReviseDesignEvents(ctx, tx, WorkflowActionExecutionRequest{WorkID: workID}, fixture.ownerRef, fields, "revision-evidence-direct-event", expected)
			return err
		}
		if err := call(revisionEvidence); err != nil {
			t.Fatalf("a bounded evidence set must assemble the revision events: %v", err)
		}
		for index, evidence := range [][]string{nil, {}, {"verification:once", "verification:once"}, overbound} {
			if err := call(evidence); err == nil {
				t.Fatalf("the constructor accepted malformed evidence set %d", index)
			}
		}
	})

	// Old prospective jobs become unready through the author's next immutable
	// job revision, not through a history rewrite: the revision leaves the
	// recorded revision's row whole, and the new revision of the job takes
	// the readiness the existing producer grants.
	t.Run("a revision leaves prospective job readiness to a new immutable job revision", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedArchitectureRevisionFixture(t, "revision-prospective", &plan, true, true)
		const workID = "revision-prospective"
		jobID := "job:revision-prospective"
		version := readRevisionWorkState(t, fixture.store, workID).version
		seedReadyWorkerJobFixture(t, fixture.store, workID, jobID, 1, version, fixture.ownerRef)
		if !revisionJobReady(t, fixture.store, workID, jobID, 1) {
			t.Fatal("the recorded prospective job revision is not ready before the design revision")
		}
		replacement := plan
		for i := range replacement.Slices {
			if replacement.Slices[i].SliceID == "value-slice" {
				replacement.Slices[i].EndState = "the value tests pass with the replanned end state"
			}
		}
		payload := revisionActionPayload(t, 1, revisionReplacementRaw(t, "prospective", &replacement))
		if err := runReviseDesignAction(t, fixture, workID, payload, "revision-prospective-op"); err != nil {
			t.Fatalf("an undispatched prospective job must not block the revision: %v", err)
		}
		var state, readiness string
		if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT state,COALESCE(readiness,'') FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=1`, workID, jobID).Scan(&state, &readiness); err != nil {
			t.Fatal(err)
		}
		if state != "recorded" || readiness == "" {
			t.Fatalf("prospective job row after revision = state %q readiness %q, want the recorded history kept whole", state, readiness)
		}
		version = readRevisionWorkState(t, fixture.store, workID).version
		seedReadyWorkerJobFixture(t, fixture.store, workID, jobID, 2, version, fixture.ownerRef)
		if revisionJobReady(t, fixture.store, workID, jobID, 1) {
			t.Fatal("the superseded prospective revision must lose readiness to the new immutable job revision")
		}
		if !revisionJobReady(t, fixture.store, workID, jobID, 2) {
			t.Fatal("the new immutable job revision must be the ready one")
		}
	})

	t.Run("the pure envelope comparison is order independent", func(t *testing.T) {
		predecessor := projectionArchitecture()
		reordered := predecessor
		reordered.Owners = []WorkflowDesignArchitectureOwner{predecessor.Owners[0]}
		reordered.Invariants = []WorkflowDesignArchitectureInvariant{predecessor.Invariants[0]}
		reordered.Files = []WorkflowDesignArchitectureFile{predecessor.Files[1], predecessor.Files[0]}
		if err := workflowArchitectureEnvelopeEqual(predecessor, reordered); err != nil {
			t.Fatalf("reordered envelope members refused: %v", err)
		}
		reordered.UnchangedFiles = nil
		if err := workflowArchitectureEnvelopeEqual(predecessor, reordered); err == nil {
			t.Fatal("a dropped unchanged-file member passed the envelope comparison")
		}
		none := map[string]bool{}
		if err := workflowArchitectureRevisionSlices(predecessor, reordered, none); err != nil {
			t.Fatalf("the unstarted suffix must stay revisable with no bound work: %v", err)
		}
		if !reflect.DeepEqual(predecessor.Slices, projectionArchitecture().Slices) {
			t.Fatal("the pure comparison mutated its inputs")
		}
	})
}

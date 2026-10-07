package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// The artifact-staleness fold and its admission halves (CD-0201 D5/D6),
// exercised over real folded history: the staleness causes, the production
// frontier at the declared route target, the ok-verdict refusal, the premise
// refusal, and the CON-846 revised-contract shape on a quarantined released
// pin whose correction the route table now admits.

// stalenessFixture pins a work item to a registered definition version and
// walks it to step, so the fold reads a real instance row.
func stalenessFixture(t *testing.T, s *Store, workID, ref string, version int64, step string) {
	t.Helper()
	entry, ok := BuiltinWorkflowRegistry().Lookup(ref, version)
	if !ok {
		t.Fatalf("%s version %d is not registered", ref, version)
	}
	seedStepWork(t, s, workID)
	initializeStepWorkflow(t, s, workID, entry.Definition)
	if step != "" {
		actorRef, err := WorkflowActorRef(stepFixtureActor())
		if err != nil {
			t.Fatal(err)
		}
		if err := advanceWorkflowTestInstanceToStep(context.Background(), s, workID, step, actorRef); err != nil {
			t.Fatal(err)
		}
	}
}

// stalenessSeedContract approves contract version 1 with one check predicate,
// so the verdict folds read a real active contract.
func stalenessSeedContract(t *testing.T, s *Store, workID string) {
	t.Helper()
	actorRef, err := WorkflowActorRef(stepFixtureActor())
	if err != nil {
		t.Fatal(err)
	}
	// The walk that moved the instance recorded the actor through the real
	// fold; workflow_actors is fold-only, so the contract row reuses it.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,1,?,'internal_sqlite','[]','[]','2026-01-01T00:00:00Z',?,'[]','[]',0,'prototype_internal'); INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES(?,1,'predicate:primary',0,'check','{"kind":"check"}'); DELETE FROM fold_guard`, workID, "The premise the test contract states.", actorRef, workID); err != nil {
		t.Fatal(err)
	}
}

// stalenessEvent appends one domain event row the fold reads, in insertion
// order, so the causal frontiers the tests build are real event sequences.
func stalenessEvent(t *testing.T, s *Store, workID, kind, payload string) {
	t.Helper()
	var nextSeq int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(MAX(seq),0)+1 FROM domain_events`).Scan(&nextSeq); err != nil {
		t.Fatal(err)
	}
	actorRef, err := WorkflowActorRef(stepFixtureActor())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,1,?)`,
		workID+":"+kind+":"+fmt.Sprint(nextSeq), kind, string(SubjectWorkItem), workID, actorRef, "2026-01-02T00:00:00Z", payload); err != nil {
		t.Fatal(err)
	}
}

func stalenessActionPayload(step, action string) string {
	encoded, _ := json.Marshal(map[string]any{"step_id": step, "action_id": action})
	return string(encoded)
}

// stalenessCapabilityDispatch records the attempt's capability dispatch —
// the worker.dispatched event that carries the capability class — which the
// production frontier requires beside the dispatch_worker completion at the
// producing step.
func stalenessCapabilityDispatch(t *testing.T, s *Store, workID, attemptID string) {
	t.Helper()
	stalenessEvent(t, s, workID, string(WorkerDispatched), `{"attempt_id":"`+attemptID+`","capability_class":"implementation"}`)
}

// stalenessSeedWorkerDelivery records the completed worker attempt and its
// dispatched-then-accepted delivery the correction prerequisites read: a
// worker_attempts row (fold-guarded), the worker.dispatched event, and the
// accepted completion that binds it.
func stalenessSeedWorkerDelivery(t *testing.T, s *Store, workID, attemptID string) {
	stalenessSeedWorkerDeliveryAt(t, s, workID, attemptID, "execute")
}

// stalenessSeedWorkerDeliveryAt seeds the completed worker attempt and its
// dispatched-then-accepted delivery at the named producing step.
func stalenessSeedWorkerDeliveryAt(t *testing.T, s *Store, workID, attemptID, step string) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO worker_attempts(work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,dispatched_at,completed_at) VALUES(?,?,'lane:test',1,'sha256:0000000000000000000000000000000000000000000000000000000000000000','implementation','model:test','1.0','1.0','completed','2026-01-02T00:00:00Z','2026-01-02T00:00:01Z'); DELETE FROM fold_guard`, workID, attemptID); err != nil {
		t.Fatal(err)
	}
	stalenessCapabilityDispatch(t, s, workID, attemptID)
	stalenessEvent(t, s, workID, string(WorkflowActionCompleted), `{"step_id":"`+step+`","action_id":"dispatch_worker","worker_attempt_id":"`+attemptID+`"}`)
	stalenessEvent(t, s, workID, string(WorkflowActionCompleted), `{"step_id":"`+step+`","action_id":"accept_worker_result","worker_attempt_id":"`+attemptID+`"}`)
}

func stalenessVerdictPayload(kind string) string {
	encoded, _ := json.Marshal(map[string]any{"predicate_id": "predicate:primary", "verdict_kind": kind, "contract_version": 1})
	return string(encoded)
}

func TestUnfencedCheckpointEpochUsesCausalPrefix(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	const workID = "unfenced-checkpoint-prefix"
	definition := mustBuiltinDefinition(t, "workflow.architecture_spike").Definition
	stalenessFixture(t, s, workID, definition.Ref, definition.Version, "")
	step := workflowStep(definition, "decision_record")
	assertEpoch := func(beforeSeq, want int64) {
		t.Helper()
		got, err := workflowCheckpointAttemptEpoch(context.Background(), s.db, definition, step, workID, step.ID, beforeSeq)
		if err != nil || got != want {
			t.Fatalf("checkpoint epoch before %d = %d, %v; want %d", beforeSeq, got, err, want)
		}
	}
	assertEpoch(0, 1)
	stalenessEvent(t, s, workID, WorkflowActionCheckpointed, `{"step_id":"decision_record","attempt_epoch":1}`)
	var firstSeq int64
	if err := s.db.QueryRow(`SELECT MAX(seq) FROM domain_events`).Scan(&firstSeq); err != nil {
		t.Fatal(err)
	}
	assertEpoch(firstSeq, 1)
	assertEpoch(0, 2)
	stalenessEvent(t, s, workID, WorkflowActionStarted, `{"step_id":"decision_record","action_id":"dispatch_worker","attempt_epoch":1}`)
	assertEpoch(0, 2)
	stalenessEvent(t, s, workID, WorkflowActionCheckpointed, `{"step_id":"review","attempt_epoch":41}`)
	stalenessEvent(t, s, "another-work", WorkflowActionCheckpointed, `{"step_id":"decision_record","attempt_epoch":51}`)
	assertEpoch(0, 2)
	stalenessEvent(t, s, workID, WorkflowActionCheckpointed, `{"step_id":"decision_record","attempt_epoch":2}`)
	var secondSeq int64
	if err := s.db.QueryRow(`SELECT MAX(seq) FROM domain_events`).Scan(&secondSeq); err != nil {
		t.Fatal(err)
	}
	assertEpoch(firstSeq, 1)
	assertEpoch(secondSeq, 2)
	assertEpoch(0, 3)
	stalenessEvent(t, s, workID, WorkflowActionStarted, `{"step_id":"decision_record","action_id":"dispatch_worker","attempt_epoch":61}`)
	assertEpoch(secondSeq, 2)
	assertEpoch(0, 61)
	stalenessEvent(t, s, workID, WorkflowActionCheckpointed, `{"step_id":"decision_record","attempt_epoch":61}`)
	assertEpoch(0, 62)
	if _, err := workflowCheckpointAttemptEpoch(context.Background(), s.db, definition, workflowStep(definition, "poc_optional"), workID, "poc_optional", 0); !hasFailureKind(err, KindIllegalLifecycleTransition) {
		t.Fatalf("fenced checkpoint without a start = %v, want invalid_transition", err)
	}
}

// foldStalenessState opens the read transaction the fold requires and returns
// the folded admission state at the work item's current step.
func foldStalenessState(t *testing.T, s *Store, workID string, definition WorkflowDefinition) WorkflowAdmissionState {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var currentStep string
	if err := tx.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&currentStep); err != nil {
		t.Fatal(err)
	}
	state, _, _, foldErr := loadWorkflowAdmissionStateTx(ctx, tx, workID, definition, currentStep, "staleness_test")
	if foldErr != nil {
		t.Fatal(foldErr)
	}
	return state
}

func TestDirectProducerArtifactOpensDeclaredCorrection(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	const workID = "laneless-artifact-correction"
	entry, ok := BuiltinWorkflowRegistry().Lookup("workflow.research", 1)
	if !ok {
		t.Fatal("released research v1 is missing")
	}
	stalenessFixture(t, s, workID, entry.Definition.Ref, entry.Definition.Version, "conclude")
	stalenessSeedContract(t, s, workID)
	stalenessEvent(t, s, workID, WorkflowActionCompleted, stalenessActionPayload("investigate", "record_finding"))
	stalenessEvent(t, s, workID, WorkflowVerdictRecorded, stalenessVerdictPayload("outcome_mismatch"))
	state, err := workflowCorrectionVerdictState(context.Background(), s.db, workID, entry.Definition, "conclude", "direct_production_test")
	if err != nil {
		t.Fatal(err)
	}
	if state == nil || !state.accepted {
		t.Fatal("the declared direct producer has no correction ground without a worker")
	}
}

// TestBadVerdictStalesArtifactUntilReproduced holds the causal core: a bad
// verdict sets the bit, neither a fenced start, a dispatch, nor an evidence
// binding clears it, and only an accepted worker delivery at the declared
// route target does.
func TestBadVerdictStalesArtifactUntilReproduced(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	const workID = "stale-bad-verdict"
	definition := mustBuiltinDefinition(t, "workflow.generic_one_off").Definition
	if target := workflowUnhealthyVerdictRouteTarget(definition, "verify"); target != "execute" {
		t.Fatalf("generic verify route target = %q, want execute", target)
	}
	stalenessFixture(t, s, workID, "workflow.generic_one_off", definition.Version, "verify")
	stalenessSeedContract(t, s, workID)
	// Production first: an accepted delivery at execute predates the bad
	// verdict, so the verdict alone decides staleness.
	stalenessCapabilityDispatch(t, s, workID, "attempt:stale-1")
	stalenessEvent(t, s, workID, string(WorkflowActionCompleted), stalenessActionPayload("execute", "dispatch_worker"))
	stalenessEvent(t, s, workID, string(WorkflowActionCompleted), `{"step_id":"execute","action_id":"accept_worker_result","worker_attempt_id":"attempt:stale-1"}`)
	stalenessEvent(t, s, workID, string(WorkflowVerdictRecorded), stalenessVerdictPayload("bad"))
	if state := foldStalenessState(t, s, workID, definition); !state.ArtifactStale {
		t.Fatal("bad verdict at verify did not stale the artifact")
	}
	// A fenced start at the target is start-only history: it never clears.
	stalenessEvent(t, s, workID, string(WorkflowActionCompleted), stalenessActionPayload("execute", "start_action"))
	if state := foldStalenessState(t, s, workID, definition); !state.ArtifactStale {
		t.Fatal("fenced start cleared artifact staleness")
	}
	// A dispatch alone is not production either.
	stalenessEvent(t, s, workID, string(WorkflowActionCompleted), `{"step_id":"execute","action_id":"dispatch_worker","worker_attempt_id":"attempt:stale-2"}`)
	if state := foldStalenessState(t, s, workID, definition); !state.ArtifactStale {
		t.Fatal("dispatch without an accepted delivery cleared artifact staleness")
	}
	// Rebidding evidence at an unrelated step does not clear it.
	stalenessEvent(t, s, workID, string(WorkflowEvidenceBound), `{"evidence_ref":"evidence:stale"}`)
	if state := foldStalenessState(t, s, workID, definition); !state.ArtifactStale {
		t.Fatal("evidence binding cleared artifact staleness")
	}
	// The accepted delivery at the target is the fresh production.
	stalenessCapabilityDispatch(t, s, workID, "attempt:stale-2")
	stalenessEvent(t, s, workID, string(WorkflowActionCompleted), `{"step_id":"execute","action_id":"accept_worker_result","worker_attempt_id":"attempt:stale-2"}`)
	if state := foldStalenessState(t, s, workID, definition); state.ArtifactStale {
		t.Fatal("accepted delivery at the route target did not clear artifact staleness")
	}
}

// TestSupersessionStalesArtifactUntilReproduced holds the successor-contract
// cause: an approved supersession at the evaluator changes the objective and
// stales the artifact until it is re-produced.
func TestSupersessionStalesArtifactUntilReproduced(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	const workID = "stale-supersession"
	definition := mustBuiltinDefinition(t, "workflow.generic_one_off").Definition
	stalenessFixture(t, s, workID, "workflow.generic_one_off", definition.Version, "verify")
	stalenessSeedContract(t, s, workID)
	stalenessEvent(t, s, workID, string(WorkflowActionCompleted), stalenessActionPayload("execute", "record_delivery"))
	if state := foldStalenessState(t, s, workID, definition); state.ArtifactStale {
		t.Fatal("production without a staleness cause folded stale")
	}
	stalenessEvent(t, s, workID, string(WorkflowContractSuperseded), `{"new_contract_version":2}`)
	if state := foldStalenessState(t, s, workID, definition); !state.ArtifactStale {
		t.Fatal("successor contract did not stale the artifact")
	}
	stalenessEvent(t, s, workID, string(WorkflowActionCompleted), stalenessActionPayload("execute", "record_delivery"))
	if state := foldStalenessState(t, s, workID, definition); state.ArtifactStale {
		t.Fatal("fresh delivery at the route target did not clear supersession staleness")
	}
}

// TestResearchAndSpikeProducersClearStaleness derives the production frontier
// from the owning family mechanisms: research's investigate step produces
// through its recording actions, and the spike's decision_record through
// record_decision — no worker dispatch exists on either step.
func TestResearchAndSpikeProducersClearStaleness(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	research := mustBuiltinDefinition(t, "workflow.research").Definition
	if actions := workflowStepArtifactActions(research, "investigate"); len(actions) == 0 {
		t.Fatal("research investigate derives no artifact actions")
	}
	const researchWork = "stale-research"
	stalenessFixture(t, s, researchWork, "workflow.research", research.Version, "conclude")
	stalenessSeedContract(t, s, researchWork)
	stalenessEvent(t, s, researchWork, string(WorkflowVerdictRecorded), stalenessVerdictPayload("bad"))
	if state := foldStalenessState(t, s, researchWork, research); !state.ArtifactStale {
		t.Fatal("bad verdict at conclude did not stale the research artifact")
	}
	stalenessEvent(t, s, researchWork, string(WorkflowActionCompleted), stalenessActionPayload("investigate", "record_finding"))
	if state := foldStalenessState(t, s, researchWork, research); state.ArtifactStale {
		t.Fatal("record_finding at investigate did not clear research staleness")
	}

	spikeStore := openTemp(t)
	defer spikeStore.Close()
	spike := mustBuiltinDefinition(t, "workflow.architecture_spike").Definition
	if actions := workflowStepArtifactActions(spike, "decision_record"); len(actions) != 1 || actions[0] != "record_decision" {
		t.Fatalf("spike decision_record artifact actions = %v, want [record_decision]", actions)
	}
	const spikeWork = "stale-spike"
	stalenessFixture(t, spikeStore, spikeWork, "workflow.architecture_spike", spike.Version, "review")
	stalenessSeedContract(t, spikeStore, spikeWork)
	stalenessEvent(t, spikeStore, spikeWork, string(WorkflowVerdictRecorded), stalenessVerdictPayload("bad"))
	if state := foldStalenessState(t, spikeStore, spikeWork, spike); !state.ArtifactStale {
		t.Fatal("bad verdict at review did not stale the spike artifact")
	}
	stalenessEvent(t, spikeStore, spikeWork, string(WorkflowActionCompleted), stalenessActionPayload("decision_record", "record_decision"))
	if state := foldStalenessState(t, spikeStore, spikeWork, spike); state.ArtifactStale {
		t.Fatal("record_decision at decision_record did not clear spike staleness")
	}
}

// TestStaleArtifactRefusesOkVerdictAndPremise holds both admission halves:
// while stale, record_verdict refuses a single ok and a batch containing ok,
// admits the non-ok verdict, and confirm_premise refuses with the declared
// route as the remedy — the generic shape whose verify step carries no
// failure edge, where the old fold let the premise step forward.
func TestStaleArtifactRefusesOkVerdictAndPremise(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	const workID = "stale-refusals"
	definition := mustBuiltinDefinition(t, "workflow.generic_one_off").Definition
	stalenessFixture(t, s, workID, "workflow.generic_one_off", definition.Version, "verify")
	stalenessSeedContract(t, s, workID)
	stalenessEvent(t, s, workID, string(WorkflowVerdictRecorded), stalenessVerdictPayload("bad"))
	state := foldStalenessState(t, s, workID, definition)
	if !state.ArtifactStale {
		t.Fatal("fixture did not fold stale")
	}
	confirm := workflowAdmit(definition, state, "confirm_premise")
	if confirm.Admitted || confirm.Failure == nil || !strings.Contains(confirm.Failure.RecoveryAction, "request_correction") {
		t.Fatalf("confirm_premise while stale = %+v, want the declared-route refusal", confirm)
	}
	// The guard refuses the single ok and a batch containing one ok, and
	// admits the non-ok verdict, through the folded state it consumes.
	guard := &workflowActionGuardContext{
		ctx: context.Background(), entry: mustBuiltinDefinition(t, "workflow.generic_one_off"),
		currentStep: "verify", admissionState: &state,
		request: WorkflowActionExecutionRequest{WorkID: workID, ActionID: "record_verdict", Payload: json.RawMessage(`{"predicate_id":"predicate:primary","verdict_kind":"ok","contract_version":1}`)},
	}
	if err := guardRecordVerdictArtifactFreshness(guard); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("single ok verdict while stale = %v, want the staleness refusal", err)
	}
	batch := WorkflowActionExecutionRequest{WorkID: workID, ActionID: "record_verdict", Payload: json.RawMessage(`{"contract_version":1,"verdicts":[{"predicate_id":"predicate:primary","verdict_kind":"bad"},{"predicate_id":"predicate:other","verdict_kind":"ok"}]}`)}
	guard.request = batch
	if err := guardRecordVerdictArtifactFreshness(guard); err == nil {
		t.Fatal("batch containing an ok verdict while stale admitted")
	}
	nonOK := WorkflowActionExecutionRequest{WorkID: workID, ActionID: "record_verdict", Payload: json.RawMessage(`{"predicate_id":"predicate:primary","verdict_kind":"bad","contract_version":1}`)}
	guard.request = nonOK
	if err := guardRecordVerdictArtifactFreshness(guard); err != nil {
		t.Fatalf("non-ok verdict while stale refused: %v", err)
	}
	// After re-production the same ok verdict records: the refusal is the
	// staleness alone, not the verdict shape.
	stalenessEvent(t, s, workID, string(WorkflowActionCompleted), stalenessActionPayload("execute", "record_delivery"))
	fresh := foldStalenessState(t, s, workID, definition)
	if fresh.ArtifactStale {
		t.Fatal("reproduction did not clear staleness")
	}
	guard.admissionState = &fresh
	guard.request = WorkflowActionExecutionRequest{WorkID: workID, ActionID: "record_verdict", Payload: json.RawMessage(`{"predicate_id":"predicate:primary","verdict_kind":"ok","contract_version":1}`)}
	if err := guardRecordVerdictArtifactFreshness(guard); err != nil {
		t.Fatalf("ok verdict after reproduction refused: %v", err)
	}
}

// TestRevisedContractOnReleasedPinNamesTheRoute is the CON-846 shape: a
// released generic_one_off pin whose verify step holds an unhealthy verdict
// under an approved successor contract resolves its correction through the
// quarantined route table, where the deleted work-kind whitelist refused it.
func TestRevisedContractOnReleasedPinNamesTheRoute(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	const workID = "stale-con861"
	// A released table-less version: the quarantine owns its routes.
	entry, ok := BuiltinWorkflowRegistry().Lookup("workflow.generic_one_off", 13)
	if !ok {
		t.Fatal("workflow.generic_one_off v13 is not registered")
	}
	definition := entry.Definition
	if len(definition.RecoveryRoutes) != 0 {
		t.Fatal("generic v13 declares a table; the fixture needs the quarantined shape")
	}
	stalenessFixture(t, s, workID, "workflow.generic_one_off", 13, "verify")
	stalenessSeedContract(t, s, workID)
	stalenessSeedWorkerDelivery(t, s, workID, "attempt:con861")
	stalenessEvent(t, s, workID, string(WorkflowVerdictRecorded), stalenessVerdictPayload("bad"))
	stalenessEvent(t, s, workID, string(WorkflowContractSuperseded), `{"new_contract_version":2}`)
	state := foldStalenessState(t, s, workID, definition)
	if !state.ArtifactStale {
		t.Fatal("revised contract with a bad verdict did not fold stale")
	}
	if !state.CorrectionRequestRecovery || state.CorrectionRequestContext == nil {
		t.Fatalf("correction request on the released pin = recovered %v context %v, want the declared route admitted", state.CorrectionRequestRecovery, state.CorrectionRequestContext)
	}
	if !strings.Contains(state.CorrectionRequestContext.Strategy, "execute") {
		t.Fatalf("correction strategy = %q, want the route target execute", state.CorrectionRequestContext.Strategy)
	}
	decision := workflowAdmit(definition, state, "request_correction")
	if !decision.Admitted {
		t.Fatalf("request_correction on the released pin refused: %v", decision.Failure)
	}
	// The fold's return target is the same declared producer step.
	if target := workflowCorrectionReturnTarget(definition, "verify"); target != "execute" {
		t.Fatalf("correction return target = %q, want execute", target)
	}
}

// TestCompleteStepReturnReadsTheRouteTable pins the CD-0172 return against the
// table: implementation release returns to execution and break_fix complete to
// repair, and no other family resolves a complete-step route.
func TestCompleteStepReturnReadsTheRouteTable(t *testing.T) {
	t.Parallel()
	implementation := mustBuiltinDefinition(t, "workflow.implementation").Definition
	if target := workflowDisprovedPremiseAtCompleteRouteTarget(implementation, "release"); target != "execution" {
		t.Fatalf("implementation release complete-step target = %q, want execution", target)
	}
	breakFix := mustBuiltinDefinition(t, "workflow.break_fix").Definition
	if target := workflowDisprovedPremiseAtCompleteRouteTarget(breakFix, "complete"); target != "repair" {
		t.Fatalf("break_fix complete-step target = %q, want repair", target)
	}
	generic := mustBuiltinDefinition(t, "workflow.generic_one_off").Definition
	if target := workflowDisprovedPremiseAtCompleteRouteTarget(generic, "complete"); target != "" {
		t.Fatalf("generic complete-step target = %q, want none: the route is not broadened to other families", target)
	}
}

// TestStaleOkVerdictReopensDeclaredRoute closes the liveness edge the
// exhaustive exit check found: an ok verdict the staleness frontier predates
// cannot close verification, so the declared route carries the continuation
// at a premise step with no failure edge.
func TestStaleOkVerdictNeedsUnhealthyVerdictForDeclaredRoute(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	const workID = "stale-ok-reopen"
	definition := mustBuiltinDefinition(t, "workflow.generic_one_off").Definition
	stalenessFixture(t, s, workID, "workflow.generic_one_off", definition.Version, "verify")
	stalenessSeedContract(t, s, workID)
	stalenessEvent(t, s, workID, string(WorkflowActionCompleted), stalenessActionPayload("execute", "record_delivery"))
	stalenessSeedWorkerDelivery(t, s, workID, "attempt:stale-ok")
	stalenessEvent(t, s, workID, string(WorkflowVerdictRecorded), stalenessVerdictPayload("ok"))
	if state := foldStalenessState(t, s, workID, definition); state.ArtifactStale {
		t.Fatal("ok verdict without a staleness cause folded stale")
	}
	stalenessEvent(t, s, workID, string(WorkflowContractSuperseded), `{"new_contract_version":2}`)
	state := foldStalenessState(t, s, workID, definition)
	if !state.ArtifactStale {
		t.Fatal("successor contract after the ok verdict did not stale the artifact")
	}
	if confirm := workflowAdmit(definition, state, "confirm_premise"); confirm.Admitted {
		t.Fatal("confirm_premise admitted past a staled ok verdict")
	}
	if state.CorrectionRequestRecovery {
		t.Fatal("an old ok verdict opened correction without an unhealthy verdict")
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class)
		SELECT work_id,2,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class FROM workflow_contracts WHERE work_id=? AND contract_version=1;
		INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload)
		SELECT work_id,2,predicate_id,ordinal,outcome_kind,outcome_payload FROM workflow_contract_predicates WHERE work_id=? AND contract_version=1;
		UPDATE workflow_contracts SET superseded_by=2 WHERE work_id=? AND contract_version=1;
		DELETE FROM fold_guard`, workID, workID, workID); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"predicate_id": "predicate:primary", "verdict_kind": "outcome_mismatch", "contract_version": 2})
	stalenessEvent(t, s, workID, WorkflowVerdictRecorded, string(payload))
	state = foldStalenessState(t, s, workID, definition)
	if !state.CorrectionRequestRecovery || state.CorrectionRequestContext == nil {
		t.Fatalf("fresh unhealthy verdict left no declared route: recovered %v context %v", state.CorrectionRequestRecovery, state.CorrectionRequestContext)
	}
}

// TestCorrectionRoutesDeriveFromNoWorkKind guards the deletion directly: no
// work-kind list or step-order heuristic may decide a recovery target.
func TestCorrectionRoutesDeriveFromNoWorkKind(t *testing.T) {
	t.Parallel()
	expectations := map[string][2]string{
		"workflow.implementation":     {"acceptance", "execution"},
		"workflow.break_fix":          {"verify", "repair"},
		"workflow.generic_one_off":    {"verify", "execute"},
		"workflow.static_analysis":    {"review", "analyze"},
		"workflow.ops_runbook":        {"health", "execute"},
		"workflow.architecture_spike": {"review", "decision_record"},
		"workflow.research":           {"conclude", "investigate"},
	}
	for ref, want := range expectations {
		definition := mustBuiltinDefinition(t, ref).Definition
		if target := workflowUnhealthyVerdictRouteTarget(definition, want[0]); target != want[1] {
			t.Errorf("%s %s route target = %q, want %q", ref, want[0], target, want[1])
		}
	}
	var source []byte
	for _, name := range []string{"workflow_correction.go", "workflow_admission.go", "workflow_action_guards.go", "workflow_step.go", "workflow.go"} {
		row, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		source = append(source, row...)
	}
	if strings.Contains(string(source), "WorkKindImplementation ||") || strings.Contains(string(source), "nearest external-effect") {
		t.Fatal("a work-kind whitelist or nearest-external-effect heuristic survives in the recovery owners")
	}
}

// TestProductionBindsToDispatchOriginAndCapability pins the positive
// production authority: an accepted result counts only when its attempt was
// dispatched at the declared route target and carries its capability
// dispatch. Evidence-only acceptance, an accept whose dispatch originated at
// another step, and an accept with no capability dispatch never re-produce
// the artifact.
func TestProductionBindsToDispatchOriginAndCapability(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	const workID = "stale-production-authority"
	definition := mustBuiltinDefinition(t, "workflow.generic_one_off").Definition
	stalenessFixture(t, s, workID, "workflow.generic_one_off", definition.Version, "verify")
	stalenessSeedContract(t, s, workID)
	stalenessEvent(t, s, workID, string(WorkflowVerdictRecorded), stalenessVerdictPayload("bad"))
	if state := foldStalenessState(t, s, workID, definition); !state.ArtifactStale {
		t.Fatal("fixture did not fold stale")
	}
	// Evidence-only acceptance at the target, with its dispatch origin in
	// place, is not production.
	stalenessCapabilityDispatch(t, s, workID, "attempt:evidence-only")
	stalenessEvent(t, s, workID, string(WorkflowActionCompleted), `{"step_id":"execute","action_id":"dispatch_worker","worker_attempt_id":"attempt:evidence-only"}`)
	stalenessEvent(t, s, workID, string(WorkflowActionCompleted), `{"step_id":"execute","action_id":"accept_worker_evidence","worker_attempt_id":"attempt:evidence-only"}`)
	if state := foldStalenessState(t, s, workID, definition); !state.ArtifactStale {
		t.Fatal("evidence-only acceptance cleared artifact staleness")
	}
	// An accepted result whose dispatch originated at a step the route does
	// not name is not production at the target.
	stalenessCapabilityDispatch(t, s, workID, "attempt:off-target")
	stalenessEvent(t, s, workID, string(WorkflowActionCompleted), `{"step_id":"verify","action_id":"dispatch_worker","worker_attempt_id":"attempt:off-target"}`)
	stalenessEvent(t, s, workID, string(WorkflowActionCompleted), `{"step_id":"verify","action_id":"accept_worker_result","worker_attempt_id":"attempt:off-target"}`)
	if state := foldStalenessState(t, s, workID, definition); !state.ArtifactStale {
		t.Fatal("off-target accepted delivery cleared artifact staleness")
	}
	// An accepted result at the target with no capability dispatch behind
	// its attempt is not production.
	stalenessEvent(t, s, workID, string(WorkflowActionCompleted), `{"step_id":"execute","action_id":"dispatch_worker","worker_attempt_id":"attempt:no-capability"}`)
	stalenessEvent(t, s, workID, string(WorkflowActionCompleted), `{"step_id":"execute","action_id":"accept_worker_result","worker_attempt_id":"attempt:no-capability"}`)
	if state := foldStalenessState(t, s, workID, definition); !state.ArtifactStale {
		t.Fatal("accept without a capability dispatch cleared artifact staleness")
	}
	// The positively bound delivery — dispatch origin at the target, the
	// capability dispatch, the accepted result — produces.
	stalenessCapabilityDispatch(t, s, workID, "attempt:fresh")
	stalenessEvent(t, s, workID, string(WorkflowActionCompleted), `{"step_id":"execute","action_id":"dispatch_worker","worker_attempt_id":"attempt:fresh"}`)
	stalenessEvent(t, s, workID, string(WorkflowActionCompleted), `{"step_id":"execute","action_id":"accept_worker_result","worker_attempt_id":"attempt:fresh"}`)
	if state := foldStalenessState(t, s, workID, definition); state.ArtifactStale {
		t.Fatal("positively bound delivery at the target did not clear artifact staleness")
	}
}

// TestNonProducingCompletionsNeverClearStaleness pins the positive action
// classification at the producer: a health reading or a candidate revision —
// advance-moded completions the blocklist era counted — never re-produce the
// artifact; only the producer's own recording actions do.
func TestNonProducingCompletionsNeverClearStaleness(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	const workID = "stale-nonproducing"
	definition := mustBuiltinDefinition(t, "workflow.generic_one_off").Definition
	stalenessFixture(t, s, workID, "workflow.generic_one_off", definition.Version, "verify")
	stalenessSeedContract(t, s, workID)
	stalenessEvent(t, s, workID, string(WorkflowVerdictRecorded), stalenessVerdictPayload("bad"))
	// A health reading completed at the target step is not production.
	stalenessEvent(t, s, workID, string(WorkflowActionCompleted), stalenessActionPayload("execute", "record_health"))
	if state := foldStalenessState(t, s, workID, definition); !state.ArtifactStale {
		t.Fatal("health reading at the target cleared artifact staleness")
	}
	// The blocklist era also counted every advance action of the producer;
	// a checkpoint-style completion stays outside the positive set.
	stalenessEvent(t, s, workID, string(WorkflowActionCompleted), stalenessActionPayload("execute", "link_successor"))
	if state := foldStalenessState(t, s, workID, definition); !state.ArtifactStale {
		t.Fatal("successor link at the target cleared artifact staleness")
	}
	stalenessEvent(t, s, workID, string(WorkflowActionCompleted), stalenessActionPayload("execute", "record_delivery"))
	if state := foldStalenessState(t, s, workID, definition); state.ArtifactStale {
		t.Fatal("delivery record at the target did not clear artifact staleness")
	}

	// Research: revising candidates is not recording findings.
	research := openTemp(t)
	defer research.Close()
	researchDefinition := mustBuiltinDefinition(t, "workflow.research").Definition
	const researchWork = "stale-nonproducing-research"
	stalenessFixture(t, research, researchWork, "workflow.research", researchDefinition.Version, "conclude")
	stalenessSeedContract(t, research, researchWork)
	stalenessEvent(t, research, researchWork, string(WorkflowVerdictRecorded), stalenessVerdictPayload("bad"))
	if actions := workflowStepArtifactActions(researchDefinition, "investigate"); len(actions) != 1 || actions[0] != "record_finding" {
		t.Fatalf("research investigate artifact actions = %v, want [record_finding]", actions)
	}
	stalenessEvent(t, research, researchWork, string(WorkflowActionCompleted), stalenessActionPayload("investigate", "revise_candidates"))
	if state := foldStalenessState(t, research, researchWork, researchDefinition); !state.ArtifactStale {
		t.Fatal("candidate revision at investigate cleared research staleness")
	}
	stalenessEvent(t, research, researchWork, string(WorkflowActionCompleted), stalenessActionPayload("investigate", "record_finding"))
	if state := foldStalenessState(t, research, researchWork, researchDefinition); state.ArtifactStale {
		t.Fatal("record_finding at investigate did not clear research staleness")
	}
}

// TestSupersessionWithoutSuccessorStalesUntilReproduced pins the no-successor
// reset: the superseded contract is a staleness cause exactly like a
// successor, and only fresh production at the route target clears it.
func TestSupersessionWithoutSuccessorStalesUntilReproduced(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	const workID = "stale-supersede-no-successor"
	definition := mustBuiltinDefinition(t, "workflow.generic_one_off").Definition
	stalenessFixture(t, s, workID, "workflow.generic_one_off", definition.Version, "verify")
	stalenessSeedContract(t, s, workID)
	stalenessEvent(t, s, workID, string(WorkflowActionCompleted), stalenessActionPayload("execute", "record_delivery"))
	stalenessEvent(t, s, workID, string(WorkflowContractSuperseded), `{}`)
	if state := foldStalenessState(t, s, workID, definition); !state.ArtifactStale {
		t.Fatal("supersession without a successor did not stale the artifact")
	}
	stalenessEvent(t, s, workID, string(WorkflowActionCompleted), stalenessActionPayload("execute", "record_delivery"))
	if state := foldStalenessState(t, s, workID, definition); state.ArtifactStale {
		t.Fatal("fresh production did not clear no-successor supersession staleness")
	}
}

// TestTerminalUnhealthyVerdictReturnsThroughRouteTable restores the terminal
// implementation shape through the single route owner: at release — the
// terminal step past the acceptance verdict — an unhealthy acceptance
// verdict folds the corrective return from release's own declared route,
// admits request_correction off-step, and names execution as the return
// target. The work-kind whitelist once carried this route (CD-0143) and the
// nearest-verdict heuristic once derived it (CD-0204); the declared table
// owns it now (CD-0201 D1): no graph walk may choose the target.
func TestTerminalUnhealthyVerdictReturnsThroughRouteTable(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	const workID = "stale-terminal-release"
	definition := mustBuiltinDefinition(t, "workflow.implementation").Definition
	if target := workflowUnhealthyVerdictRouteTarget(definition, "release"); target != "execution" {
		t.Fatalf("implementation release declares its own unhealthy route %q; the terminal shape needs the declared route to execution", target)
	}
	stalenessFixture(t, s, workID, "workflow.implementation", definition.Version, "release")
	stalenessSeedContract(t, s, workID)
	stalenessSeedWorkerDeliveryAt(t, s, workID, "attempt:terminal", "execution")
	stalenessEvent(t, s, workID, string(WorkflowVerdictRecorded), stalenessVerdictPayload("bad"))
	state := foldStalenessState(t, s, workID, definition)
	if !state.CorrectionRequestRecovery || state.CorrectionRequestContext == nil {
		t.Fatalf("terminal unhealthy verdict left no declared route: recovered %v context %v", state.CorrectionRequestRecovery, state.CorrectionRequestContext)
	}
	if !strings.Contains(state.CorrectionRequestContext.Strategy, "execution") {
		t.Fatalf("terminal correction strategy = %q, want the served route target execution", state.CorrectionRequestContext.Strategy)
	}
	decision := workflowAdmit(definition, state, "request_correction")
	if !decision.Admitted {
		t.Fatalf("request_correction at the terminal step refused: %v", decision.Failure)
	}
	if target := workflowCorrectionReturnTarget(definition, "release"); target != "execution" {
		t.Fatalf("terminal correction return target = %q, want execution", target)
	}
}

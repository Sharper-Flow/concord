package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// The lane-step dispatch join (#892) tests: definition composition derives
// the steps that carry the worker-action set from the generated join, and
// dispatch-time validation refuses a lane whose capability class the current
// step kind does not admit.

func joinPacketFor(t *testing.T, s *Store, workID, stepID, attemptID, laneID string, laneVersion int64, laneDigest string) map[string]any {
	t.Helper()
	task, binding := recordedPacketInputs(t, s, workID, laneID)
	inputs := map[string]any{
		"task":        task,
		"binding":     binding,
		"constraints": []string{"do-not-modify-product-truth"},
	}
	for member, value := range recordedPacketRecords(t, s, workID) {
		inputs[member] = value
	}
	packet := map[string]any{
		"schema_version": WorkerPacketSchemaVersion,
		"attempt_id":     attemptID,
		"lane_id":        laneID,
		"lane_version":   laneVersion,
		"lane_digest":    laneDigest,
		"work_id":        workID,
		"step_id":        stepID,
		"inputs":         inputs,
	}
	return packet
}

// implementLaneIdentity answers the registered implement lane's version and
// digest for fixtures built outside a *testing.T scope.
func implementLaneIdentity() (int64, string) {
	for _, lane := range BuiltinLaneDefinitions() {
		if lane.ID == "implement" {
			return lane.Version, lane.Digest
		}
	}
	panic("lane implement is not registered")
}

func registeredLaneIdentity(t *testing.T, laneID string) (int64, string) {
	t.Helper()
	for _, lane := range BuiltinLaneDefinitions() {
		if lane.ID == laneID {
			return lane.Version, lane.Digest
		}
	}
	t.Fatalf("lane %s is not registered", laneID)
	return 0, ""
}

// seedJoinFixture seeds a break_fix instance pinned to the shipped definition
// and parked on the given step, so the dispatch action is registered exactly
// where the join composes it.
func seedJoinFixture(t *testing.T, s *Store, workID, stepID string) WorkflowActor {
	t.Helper()
	ctx := context.Background()
	actor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	seedWork(t, s, workID)
	entry, ok := BuiltinWorkflowRegistry().Lookup("workflow.break_fix", 3)
	if !ok {
		t.Fatal("workflow.break_fix v3 is not registered")
	}
	events := []Event{
		workflowEvent("join-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": actorRef, "principal_ref": actor.PrincipalRef, "client_ref": actor.ClientRef, "agent_ref": actor.AgentRef, "session_ref": actor.SessionRef, "actor_class": "agent"}),
		workflowEvent("join-definition-"+workID, WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "ref": "workflow.break_fix", "version": 3, "digest": entry.Digest, "work_kind": "break_fix"}),
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_instances SET current_step=? WHERE work_id=?`, stepID, workID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := leaveFold(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return actor
}

func dispatchJoinAttempt(ctx context.Context, t *testing.T, s *Store, workID string, expectedVersion int64, actor WorkflowActor, packet map[string]any) (WorkflowActionExecutionResult, error) {
	t.Helper()
	attemptID := packet["attempt_id"].(string)
	fieldsPayload, err := json.Marshal(map[string]any{"attempt_id": attemptID, "worker_packet": packet})
	if err != nil {
		t.Fatal(err)
	}
	return invokeWorkflowActionForCD0059(ctx, t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: expectedVersion, ActionID: "dispatch_worker",
		Payload: fieldsPayload, Actor: actor, AcceptedInputsDigest: cd0059TestDigest(t, "join-inputs"), ContractDigest: testManifestDigest,
		SessionWorktree: dispatchSessionWorktree(t, s, workID),
		Tool:            "concord_work_transition", IdempotencyKey: "join-key-" + attemptID, RequestID: "join-req-" + attemptID, IdempotencyIdentity: "join-" + attemptID, OperationID: "join-op-" + attemptID, PrincipalRef: actor.PrincipalRef,
		Now: time.Now().UTC(),
	})
}

func TestJoinComposesWorkerActionsOnAdmittedStepKinds(t *testing.T) {
	t.Parallel()
	breakFix, ok := BuiltinWorkflowRegistry().Lookup("workflow.break_fix", 3)
	if !ok {
		t.Fatal("workflow.break_fix v3 is not registered")
	}
	stepActions := map[string][]string{}
	for _, step := range breakFix.Definition.StepGraph.Steps {
		stepActions[step.ID] = step.Actions
	}
	carries := func(stepID string) bool {
		for _, action := range stepActions[stepID] {
			if action == "dispatch_worker" {
				return true
			}
		}
		return false
	}
	// Read-only and external-effect steps all carry the pair; the
	// human_checkpoint steps and the terminal step never do.
	for _, admitted := range []string{"reproduce", "diagnose", "repair"} {
		if !carries(admitted) {
			t.Errorf("break_fix step %s does not carry dispatch_worker", admitted)
		}
	}
	for _, refused := range []string{"planning", "verify", "complete"} {
		if carries(refused) {
			t.Errorf("break_fix step %s carries dispatch_worker", refused)
		}
	}
	// The research family, excluded entirely before the join, now carries
	// the pair on its read steps.
	research, ok := BuiltinWorkflowRegistry().Lookup("workflow.research", 3)
	if !ok {
		t.Fatal("workflow.research v3 is not registered")
	}
	researchCarries := map[string]bool{}
	for _, step := range research.Definition.StepGraph.Steps {
		for _, action := range step.Actions {
			if action == "dispatch_worker" {
				researchCarries[step.ID] = true
			}
		}
	}
	if !researchCarries["investigate"] || !researchCarries["findings"] {
		t.Errorf("research read steps do not carry dispatch_worker: %v", researchCarries)
	}
	if researchCarries["frame"] || researchCarries["conclude"] || researchCarries["complete"] {
		t.Errorf("research checkpoint or terminal steps carry dispatch_worker: %v", researchCarries)
	}
}

func TestCurrentJoinComposesTheWorkerFailureRecordWithTheDispatchPair(t *testing.T) {
	t.Parallel()
	for _, definition := range BuiltinWorkflowDefinitions() {
		if !containsString(definition.AvailableActions, "record_worker_failure") {
			t.Errorf("%s current definition does not declare record_worker_failure", definition.Ref)
		}
		for _, step := range definition.StepGraph.Steps {
			dispatch := containsString(step.Actions, "dispatch_worker")
			accept := containsString(step.Actions, "accept_worker_result")
			recordFailure := containsString(step.Actions, "record_worker_failure")
			acceptEvidence := containsString(step.Actions, "accept_worker_evidence")
			// CD-0187: a confirmation step carries the checkpoint pair and
			// never the advancing accept or the failure record; every other
			// step carries the worker trio or none of it.
			if step.Kind == WorkflowStepHumanCheckpoint {
				if !dispatch || !acceptEvidence || accept || recordFailure {
					t.Errorf("%s checkpoint step %s worker actions: dispatch=%t accept_evidence=%t accept=%t record_failure=%t", definition.Ref, step.ID, dispatch, acceptEvidence, accept, recordFailure)
				}
				continue
			}
			if dispatch != accept || dispatch != recordFailure {
				t.Errorf("%s step %s worker actions: dispatch=%t accept=%t record_failure=%t", definition.Ref, step.ID, dispatch, accept, recordFailure)
			}
			if acceptEvidence {
				t.Errorf("%s step %s carries accept_worker_evidence outside a checkpoint step", definition.Ref, step.ID)
			}
		}
	}

	prior, ok := BuiltinWorkflowRegistry().Lookup("workflow.break_fix", 4)
	if !ok {
		t.Fatal("workflow.break_fix v4 is not registered")
	}
	if containsString(prior.Definition.AvailableActions, "record_worker_failure") {
		t.Fatal("workflow.break_fix v4 changed after record_worker_failure shipped in v5")
	}
}

func TestJoinAdmitsResearchLaneAtReadStep(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	workID := "work-join-admit"
	actor := seedJoinFixture(t, s, workID, "reproduce")
	version := readWorkVersion(t, s, workID)
	laneVersion, laneDigest := registeredLaneIdentity(t, "research")
	packet := joinPacketFor(t, s, workID, "reproduce", "attempt-join-admit", "research", laneVersion, laneDigest)
	result, err := dispatchJoinAttempt(context.Background(), t, s, workID, version, actor, packet)
	if err != nil {
		t.Fatalf("research lane dispatch at reproduce refused: %v", err)
	}
	if result.ResultingVersion <= version {
		t.Fatalf("dispatch did not advance the version: %d", result.ResultingVersion)
	}
}

func TestJoinAdmitsReviewLaneAtEffectStep(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	// CD-0140: review inspects produced work, which exists only after an
	// external-effect step has run; the repair step is the break_fix family's
	// external_effect step.
	workID := "work-join-admit-review"
	actor := seedJoinFixture(t, s, workID, "repair")
	version := readWorkVersion(t, s, workID)
	laneVersion, laneDigest := registeredLaneIdentity(t, "review")
	packet := joinPacketFor(t, s, workID, "repair", "attempt-join-admit-review", "review", laneVersion, laneDigest)
	result, err := dispatchJoinAttempt(context.Background(), t, s, workID, version, actor, packet)
	if err != nil {
		t.Fatalf("review lane dispatch at an external_effect step refused: %v", err)
	}
	if result.ResultingVersion <= version {
		t.Fatalf("dispatch did not advance the version: %d", result.ResultingVersion)
	}
}

func TestJoinRefusesLaneAtUnadmittedStepKind(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	workID := "work-join-refuse"
	actor := seedJoinFixture(t, s, workID, "reproduce")
	version := readWorkVersion(t, s, workID)

	implVersion, implDigest := registeredLaneIdentity(t, "implement")
	implPacket := joinPacketFor(t, s, workID, "reproduce", "attempt-join-refuse-impl", "implement", implVersion, implDigest)
	_, err := dispatchJoinAttempt(context.Background(), t, s, workID, version, actor, implPacket)
	if err == nil {
		t.Fatal("implement lane dispatch at an internal_sqlite step was admitted")
	}
	failure, ok := err.(*Failure)
	if !ok || failure.Kind != KindUnauthorizedDispatch {
		t.Fatalf("refusal = %v, want unauthorized_dispatch", err)
	}
	if !strings.Contains(failure.Detail, "not dispatchable") {
		t.Fatalf("refusal message = %q", failure.Detail)
	}

	// The external_effect repair step admits implement but refuses the
	// read-only research lane.
	repairWorkID := "work-join-refuse-repair"
	repairActor := seedJoinFixture(t, s, repairWorkID, "repair")
	repairVersion := readWorkVersion(t, s, repairWorkID)
	researchVersion, researchDigest := registeredLaneIdentity(t, "research")
	researchPacket := joinPacketFor(t, s, repairWorkID, "repair", "attempt-join-refuse-research", "research", researchVersion, researchDigest)
	_, err = dispatchJoinAttempt(context.Background(), t, s, repairWorkID, repairVersion, repairActor, researchPacket)
	if err == nil {
		t.Fatal("research lane dispatch at an external_effect step was admitted")
	}
	failure, ok = err.(*Failure)
	if !ok || failure.Kind != KindUnauthorizedDispatch {
		t.Fatalf("refusal = %v, want unauthorized_dispatch", err)
	}
	// The refused class alone leaves the caller to guess again. The refusal
	// carries the classes this step kind does admit, so one read replaces the
	// guess.
	for _, admitted := range []string{"design", "implementation", "review", "verification"} {
		if !strings.Contains(failure.Detail, admitted) {
			t.Fatalf("refusal %q omits admitted class %q", failure.Detail, admitted)
		}
	}
}

func TestLaneStepDispatchClassesInvertsTheJoin(t *testing.T) {
	t.Parallel()
	// Each step kind exposes exactly the classes whose generated bindings
	// name it, sorted by name.
	got := LaneStepDispatchClasses(WorkflowStepExternalEffect)
	want := []string{"design", "implementation", "review", "verification"}
	if !slices.Equal(got, want) {
		t.Fatalf("external_effect admits %v, want %v", got, want)
	}
	got = LaneStepDispatchClasses(WorkflowStepInternalSQLite)
	want = []string{"research", "review"}
	if !slices.Equal(got, want) {
		t.Fatalf("internal_sqlite admits %v, want %v", got, want)
	}
	got = LaneStepDispatchClasses(WorkflowStepCrossAuthority)
	want = []string{"research", "review"}
	if !slices.Equal(got, want) {
		t.Fatalf("cross_authority admits %v, want %v", got, want)
	}
	got = LaneStepDispatchClasses(WorkflowStepHumanCheckpoint)
	want = []string{"review"}
	if !slices.Equal(got, want) {
		t.Fatalf("human_checkpoint admits %v, want %v", got, want)
	}
	// The inverse read agrees with the forward read for every class and kind.
	for class := range laneStepDispatchKinds {
		for _, kind := range []WorkflowStepKind{WorkflowStepInternalSQLite, WorkflowStepCrossAuthority, WorkflowStepExternalEffect, WorkflowStepHumanCheckpoint} {
			allowed := LaneStepDispatchAllowed(class, kind)
			contained := slices.Contains(LaneStepDispatchClasses(kind), class)
			if allowed != contained {
				t.Errorf("class %s at %s: allowed=%t, inverse read=%t", class, kind, allowed, contained)
			}
		}
	}
}

// checkpointReviewReviewer records a distinct reviewer actor on the seeded
// item, so the verdict the round trip records carries an evaluator that
// executed no worker work.
func checkpointReviewReviewer(t *testing.T, s *Store, workID string) WorkflowActor {
	t.Helper()
	reviewer := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/checkpoint-reviewer", SessionRef: "session/" + workID + "-reviewer", ActorClass: ActorAgent}
	reviewerRef, err := WorkflowActorRef(reviewer)
	if err != nil {
		t.Fatal(err)
	}
	version := verdictItemVersion(t, s, workID)
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{
		workflowEvent("checkpoint-reviewer-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "actor_ref": reviewerRef, "principal_ref": reviewer.PrincipalRef, "client_ref": reviewer.ClientRef, "agent_ref": reviewer.AgentRef, "session_ref": reviewer.SessionRef, "actor_class": "agent"}),
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
	return reviewer
}

// TestAcceptanceReviewRoundTrip drives the CD-0187 route on the current
// break-fix definition: a contract that requires review evidence refuses the
// confirmation, the review lane dispatches at the confirmation step, its
// report binds as review evidence without advancing the step, and the
// operator's own gate then passes.
func TestAcceptanceReviewRoundTrip(t *testing.T) {
	const workID = "join-checkpoint-review"
	fixture := seedWorkflowReturnRouteFixtureRequiring(t, workID, "workflow.break_fix", "verify", []string{"verification", "review"}, []string{"verification", "artifact"})
	s := fixture.store

	// The verdict is in, and the confirmation still refuses: the
	// contract-required review kind is unbound, the wedge CON-517 recorded.
	reviewer := checkpointReviewReviewer(t, s, workID)
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:return-route","verdict_kind":"ok","evaluation_evidence":["evidence:return-route-verification"]}`), 0, reviewer); err != nil {
		t.Fatalf("record_verdict refused: %v", err)
	}
	err := runIssue933OperatorAction(t, s, workID, "confirm_premise", json.RawMessage(`{"contract_version":1}`), fixture.owner, fixture.operator)
	if err == nil {
		t.Fatal("confirm_premise was admitted without the required review evidence")
	}
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindMissingEvidence || !strings.Contains(failure.Detail, "missing review") {
		t.Fatalf("confirm_premise refusal = %v, want a missing-review refusal", err)
	}

	// The amended join admits the review lane at the confirmation step.
	lane := reviewGateLane(t, "review")
	laneVersion, laneDigest := registeredLaneIdentity(t, "review")
	attemptID := "attempt:" + workID + ":checkpoint-review"
	packet := joinPacketFor(t, s, workID, "verify", attemptID, "review", laneVersion, laneDigest)
	if _, err := dispatchJoinAttempt(context.Background(), t, s, workID, verdictItemVersion(t, s, workID), fixture.owner, packet); err != nil {
		t.Fatalf("review dispatch at the verify checkpoint refused: %v", err)
	}

	// The lane dispatch authorizes the attempt against the window the
	// action opened, carrying the packet digest that action recorded, and
	// the review report lands.
	var packetDigest string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.worker_packet_digest') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionCompleted).Scan(&packetDigest); err != nil {
		t.Fatal(err)
	}
	laneDispatch := Event{EventID: "join-checkpoint-dispatch-" + workID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(30, 0).UTC(), PayloadVersion: 2, Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion, PacketDigest: packetDigest})}
	if err := s.Transact(context.Background(), func(transaction *Transaction) error {
		prepared, err := PrepareLaneActorDispatch(context.Background(), transaction, laneDispatch, fixture.owner.PrincipalRef, fixture.owner.ClientRef)
		if err != nil {
			return err
		}
		_, err = AppendLaneActorDispatchTx(context.Background(), transaction, prepared)
		return err
	}); err != nil {
		t.Fatalf("lane actor dispatch: %v", err)
	}
	completed := workerCompleteEventForLane(workID, "join-checkpoint-completed-"+workID, attemptID, lane, time.Unix(40, 0).UTC())
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{completed}}); err != nil {
		t.Fatal(err)
	}

	// The unsettled attempt keeps the confirmation closed: the report is
	// not evidence until the checkpoint accept binds it.
	err = runIssue933OperatorAction(t, s, workID, "confirm_premise", json.RawMessage(`{"contract_version":1}`), fixture.owner, fixture.operator)
	if err == nil || !failureAs(err, &failure) || !strings.Contains(failure.Detail, "missing review") {
		t.Fatalf("confirm_premise over the unbound report = %v, want the missing-review refusal", err)
	}

	// The checkpoint accept binds the report and holds the step.
	var epoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionStarted).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	if err := runVerdictActionAs(t, s, workID, "accept_worker_evidence", json.RawMessage(`{"attempt_id":"`+attemptID+`","attempt_epoch":`+fmt.Sprint(epoch)+`}`), 0, fixture.owner); err != nil {
		t.Fatalf("accept_worker_evidence refused: %v", err)
	}
	if step := currentStep(t, s, workID); step != "verify" {
		t.Fatalf("step after accept_worker_evidence = %q, want verify", step)
	}
	var bound int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.evidence_kind')='review' AND json_extract(payload,'$.immutable_subject_ref')=?`, workID, WorkflowEvidenceBound, attemptID).Scan(&bound); err != nil {
		t.Fatal(err)
	}
	if bound != 1 {
		t.Fatalf("review evidence bindings for the accepted report = %d, want 1", bound)
	}

	// The operator's own gate passes on the bound review evidence.
	if err := runIssue933OperatorAction(t, s, workID, "confirm_premise", json.RawMessage(`{"contract_version":1}`), fixture.owner, fixture.operator); err != nil {
		t.Fatalf("confirm_premise refused after the bound review: %v", err)
	}
	if step := currentStep(t, s, workID); step != "complete" {
		t.Fatalf("step after premise confirmation = %q, want complete", step)
	}
}

// TestRepinReachesTheCheckpointReviewDefinition carries an instance pinned to
// the pre-CD-0187 break-fix definition forward through the existing repin
// route, and the amended verify step then admits the review lane.
func TestRepinReachesTheCheckpointReviewDefinition(t *testing.T) {
	const workID = "join-checkpoint-repin"
	fixture := seedHistoricalWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", 14, "verify")
	s := fixture.store
	current, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	if current.Definition.Version != 23 {
		t.Fatalf("current break-fix version = %d, want 23", current.Definition.Version)
	}
	if err := s.Transact(context.Background(), func(transaction *Transaction) error {
		return RepinWorkflowTx(context.Background(), transaction, WorkflowRepinRequest{WorkID: workID, EventID: workID + "-repin", Definition: current, Actor: fixture.owner, Now: time.Unix(50, 0).UTC()})
	}); err != nil {
		t.Fatalf("repin to the amended definition refused: %v", err)
	}
	if step := currentStep(t, s, workID); step != "verify" {
		t.Fatalf("step after repin = %q, want verify", step)
	}
	laneVersion, laneDigest := registeredLaneIdentity(t, "review")
	packet := joinPacketFor(t, s, workID, "verify", "attempt:"+workID+":repin-review", "review", laneVersion, laneDigest)
	if _, err := dispatchJoinAttempt(context.Background(), t, s, workID, verdictItemVersion(t, s, workID), fixture.owner, packet); err != nil {
		t.Fatalf("review dispatch after repin refused: %v", err)
	}
}

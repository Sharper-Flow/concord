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
	view, err := readWorkContextView(context.Background(), s.DatabaseForTesting(), workID)
	if err != nil {
		t.Fatal(err)
	}
	if view != nil {
		inputs["work_context"] = view
	}
	if testLaneCapabilityClass(laneID) == "review" {
		snapshot, err := ReadWorkflowContinuity(t.Context(), s, ContinuityRequest{Work: workID})
		if err != nil {
			t.Fatal(err)
		}
		if reviewInput := reviewInputForPacketFixture(t, s, snapshot); reviewInput != nil {
			inputs["review_input"] = reviewInput
		}
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

// seedJoinFixture seeds an instance of one live family pinned to its current
// definition and parked on the given step, so the dispatch action is
// registered exactly where the join composes it.
func seedJoinFixture(t *testing.T, s *Store, workID, ref, stepID string) WorkflowActor {
	t.Helper()
	ctx := context.Background()
	actor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	seedWork(t, s, workID)
	entry := mustBuiltinDefinition(t, ref)
	events := []Event{
		workflowEvent("join-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": actorRef, "principal_ref": actor.PrincipalRef, "client_ref": actor.ClientRef, "agent_ref": actor.AgentRef, "session_ref": actor.SessionRef, "actor_class": "agent"}),
		workflowEvent("join-definition-"+workID, WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "ref": ref, "version": entry.Definition.Version, "digest": entry.Digest, "work_kind": string(entry.Definition.WorkKind)}),
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterWrite(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_instances SET current_step=? WHERE work_id=?`, stepID, workID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := leaveWrite(ctx, tx); err != nil {
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

// TestCurrentJoinComposesTheWorkerFailureRecordWithTheDispatchPair pins the
// worker actions each live family's current definition composes. Every live
// family derives the failure record from the attempt's own terminal event
// (CD-0198 D5), so no step declares the callable record. A step that
// dispatches also takes the result exactly once: a checkpoint takes it as
// accepted evidence or as Main's accepted result, every other step only as
// the accepted result (CD-0242 D5). Only a step of an admitted kind
// dispatches.
func TestCurrentJoinComposesTheWorkerFailureRecordWithTheDispatchPair(t *testing.T) {
	t.Parallel()
	for _, ref := range []string{"workflow.implementation", "workflow.break_fix", "workflow.research"} {
		definition := mustBuiltinDefinition(t, ref).Definition
		if !workflowDerivedWorkerFailureActive(definition) {
			t.Errorf("%s current definition does not derive the failure record", ref)
		}
		if slices.Contains(definition.AvailableActions, "record_worker_failure") {
			t.Errorf("%s still advertises record_worker_failure; CD-0198 D5 derives it", ref)
		}
		for _, step := range definition.StepGraph.Steps {
			dispatch := slices.Contains(step.Actions, "dispatch_worker")
			accept := slices.Contains(step.Actions, "accept_worker_result")
			acceptEvidence := slices.Contains(step.Actions, "accept_worker_evidence")
			if slices.Contains(step.Actions, "record_worker_failure") {
				t.Errorf("%s step %s still declares record_worker_failure; CD-0198 D5 derives it", ref, step.ID)
			}
			if dispatch && len(LaneStepDispatchClasses(step.Kind)) == 0 {
				t.Errorf("%s step %s dispatches at kind %s, which admits no lane class", ref, step.ID, step.Kind)
			}
			if dispatch != (accept || acceptEvidence) || (accept && acceptEvidence) {
				t.Errorf("%s step %s worker actions: dispatch=%t accept=%t accept_evidence=%t", ref, step.ID, dispatch, accept, acceptEvidence)
			}
			if acceptEvidence && step.Kind != WorkflowStepHumanCheckpoint {
				t.Errorf("%s step %s carries accept_worker_evidence outside a checkpoint step", ref, step.ID)
			}
		}
	}
}

func TestJoinAdmitsResearchLaneAtReadStep(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	workID := "work-join-admit"
	actor := seedJoinFixture(t, s, workID, "workflow.implementation", "design")
	version := readWorkVersion(t, s, workID)
	laneVersion, laneDigest := registeredLaneIdentity(t, "research")
	packet := joinPacketFor(t, s, workID, "design", "attempt-join-admit", "research", laneVersion, laneDigest)
	result, err := dispatchJoinAttempt(context.Background(), t, s, workID, version, actor, packet)
	if err != nil {
		t.Fatalf("research lane dispatch at the internal_sqlite design step refused: %v", err)
	}
	if result.ResultingVersion <= version {
		t.Fatalf("dispatch did not advance the version: %d", result.ResultingVersion)
	}
}

func TestJoinAdmitsReviewLaneAtEffectStep(t *testing.T) {
	// CD-0140: review inspects produced work, which exists only after an
	// external-effect step has run; the repair step is the break_fix family's
	// external_effect step. Main dispatches Review over the accepted
	// implementation delivery (CD-0242 D5).
	const workID = "work-join-admit-review"
	fixture := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, mustBuiltinDefinition(t, "workflow.break_fix"), "repair", []string{"verification"}, []string{"verification", "artifact"})
	s := fixture.store
	seedCheckpointReviewAcceptedDelivery(t, s, workID, fixture.owner, "repair", "repair")
	version := readWorkVersion(t, s, workID)
	laneVersion, laneDigest := registeredLaneIdentity(t, "review")
	packet := joinPacketFor(t, s, workID, "repair", "attempt-join-admit-review", "review", laneVersion, laneDigest)
	attachReadyWorkerJobIfJobCapable(t, s, workID, "repair", fixture.owner, packet)
	result, err := dispatchJoinAttempt(context.Background(), t, s, workID, readWorkVersion(t, s, workID), fixture.owner, packet)
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
	actor := seedJoinFixture(t, s, workID, "workflow.implementation", "design")
	version := readWorkVersion(t, s, workID)

	implVersion, implDigest := registeredLaneIdentity(t, "implement")
	implPacket := joinPacketFor(t, s, workID, "design", "attempt-join-refuse-impl", "implement", implVersion, implDigest)
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
	repairActor := seedJoinFixture(t, s, repairWorkID, "workflow.break_fix", "repair")
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
	version := readWorkVersion(t, s, workID)
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{
		workflowEvent("checkpoint-reviewer-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "actor_ref": reviewerRef, "principal_ref": reviewer.PrincipalRef, "client_ref": reviewer.ClientRef, "agent_ref": reviewer.AgentRef, "session_ref": reviewer.SessionRef, "actor_class": "agent"}),
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
	return reviewer
}

// A Review at the confirmation step must be accepted by Main before its
// receipt association can support the coordinator's engineering confirmation.
func TestAcceptanceReviewRoundTrip(t *testing.T) {
	const workID = "join-checkpoint-review"
	fixture := seedWorkflowReturnRouteFixtureRequiring(t, workID, "workflow.break_fix", "verify", []string{"verification", "review"}, []string{"verification", "artifact"})
	s := fixture.store
	f := workContextFixture{store: s, workID: workID, owner: fixture.owner, operator: fixture.operator}
	repo, _ := reviewSubjectClaim(t, s, workID)
	var branch string
	if err := s.db.QueryRow(`SELECT branch FROM worktree_entries WHERE set_id=? AND state='active'`, WorktreeSetID(workID)).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, repo, "branch", "-m", branch)
	j := &candidateJourney{fixture: f, s: s, workID: workID, repo: repo, acceptor: f.owner}
	run := j.observeRun(t, "checkpoint-proof")
	c := acceptDeliveryCandidate{f: f, j: j, early: run}
	attemptID := "attempt:" + workID + ":checkpoint-review"
	epoch := acceptDeliveryCompleteReview(t, c, attemptID, "ready_for_final_proof")
	seedPremiseDeliverablesInvestigation(t, s, workID)

	// The unsettled attempt keeps the confirmation closed: the report is
	// not evidence until the checkpoint accept binds it.
	association := criterionAssociationPayload(run.OperationRef, "predicate:return-route")
	err := runVerdictActionAs(t, s, workID, "record_verdict", association, 0, fixture.owner)
	var failure *Failure
	if err == nil || !failureAs(err, &failure) || failure.Kind != KindMissingEvidence || !strings.Contains(failure.Detail, "criterion association requires a current integrated candidate") {
		t.Fatalf("proof association over the unaccepted Review = %v, want the unaccepted-Review refusal", err)
	}
	err = pConfirmReceiptPremise(t, f, repo)
	if err == nil || !failureAs(err, &failure) || failure.Kind != KindMissingEvidence || !strings.Contains(failure.Detail, "requires a recorded workflow verdict") {
		t.Fatalf("confirm_premise over the unbound report = %v, want the missing-proof refusal", err)
	}

	// The checkpoint accept binds the report and holds the step.
	if err := runVerdictActionAs(t, s, workID, "accept_worker_result", json.RawMessage(`{"attempt_id":"`+attemptID+`","attempt_epoch":`+fmt.Sprint(epoch)+`}`), 0, fixture.owner); err != nil {
		t.Fatalf("accept_worker_result refused: %v", err)
	}
	if step := currentStep(t, s, workID); step != "verify" {
		t.Fatalf("step after accept_worker_result = %q, want verify", step)
	}
	var bound int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.evidence_kind')='review' AND json_extract(payload,'$.immutable_subject_ref')=?`, workID, WorkflowEvidenceBound, attemptID).Scan(&bound); err != nil {
		t.Fatal(err)
	}
	if bound != 1 {
		t.Fatalf("review evidence bindings for the accepted report = %d, want 1", bound)
	}

	if err := runVerdictActionAs(t, s, workID, "record_verdict", criterionAssociationPayload(run.OperationRef, "predicate:return-route"), 0, fixture.owner); err != nil {
		t.Fatalf("associate accepted Review proof: %v", err)
	}
	if err := pConfirmReceiptPremise(t, f, repo); err != nil {
		t.Fatalf("confirm_premise refused after the bound review: %v", err)
	}
	if step := currentStep(t, s, workID); step != "complete" {
		t.Fatalf("step after premise confirmation = %q, want complete", step)
	}
}

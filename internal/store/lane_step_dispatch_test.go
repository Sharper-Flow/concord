package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The lane-step dispatch join (#892) tests: definition composition derives
// the steps that carry the worker-dispatch pair from the generated join, and
// dispatch-time validation refuses a lane whose capability class the current
// step kind does not admit.

func joinPacketFor(workID, stepID, attemptID, laneID string, laneVersion int64, laneDigest string) map[string]any {
	return map[string]any{
		"schema_version": "1.0",
		"attempt_id":     attemptID,
		"lane_id":        laneID,
		"lane_version":   laneVersion,
		"lane_digest":    laneDigest,
		"work_id":        workID,
		"step_id":        stepID,
		"inputs": map[string]any{
			"task":        "lane-step dispatch join probe",
			"constraints": []string{"do-not-modify-product-truth"},
		},
	}
}

func mustLaneIdentity(laneID string) (int64, string) {
	for _, lane := range BuiltinLaneDefinitions() {
		if lane.ID == laneID {
			return lane.Version, lane.Digest
		}
	}
	panic("lane " + laneID + " is not registered")
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
		Tool: "concord_work_transition", IdempotencyKey: "join-key-" + attemptID, RequestID: "join-req-" + attemptID, IdempotencyIdentity: "join-" + attemptID, OperationID: "join-op-" + attemptID, PrincipalRef: actor.PrincipalRef,
		Now: time.Now().UTC(),
	})
}

func TestJoinComposesWorkerActionsOnAdmittedStepKinds(t *testing.T) {
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

func TestJoinAdmitsResearchLaneAtReadStep(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	workID := "work-join-admit"
	actor := seedJoinFixture(t, s, workID, "reproduce")
	version := readWorkVersion(t, s, workID)
	laneVersion, laneDigest := registeredLaneIdentity(t, "research")
	packet := joinPacketFor(workID, "reproduce", "attempt-join-admit", "research", laneVersion, laneDigest)
	result, err := dispatchJoinAttempt(context.Background(), t, s, workID, version, actor, packet)
	if err != nil {
		t.Fatalf("research lane dispatch at reproduce refused: %v", err)
	}
	if result.ResultingVersion <= version {
		t.Fatalf("dispatch did not advance the version: %d", result.ResultingVersion)
	}
}

func TestJoinRefusesLaneAtUnadmittedStepKind(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	workID := "work-join-refuse"
	actor := seedJoinFixture(t, s, workID, "reproduce")
	version := readWorkVersion(t, s, workID)

	implVersion, implDigest := registeredLaneIdentity(t, "implement")
	implPacket := joinPacketFor(workID, "reproduce", "attempt-join-refuse-impl", "implement", implVersion, implDigest)
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
	researchPacket := joinPacketFor(repairWorkID, "repair", "attempt-join-refuse-research", "research", researchVersion, researchDigest)
	_, err = dispatchJoinAttempt(context.Background(), t, s, repairWorkID, repairVersion, repairActor, researchPacket)
	if err == nil {
		t.Fatal("research lane dispatch at an external_effect step was admitted")
	}
	failure, ok = err.(*Failure)
	if !ok || failure.Kind != KindUnauthorizedDispatch {
		t.Fatalf("refusal = %v, want unauthorized_dispatch", err)
	}
}

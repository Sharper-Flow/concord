package store

import (
	"context"
	"strings"
	"testing"
)

// secondSessionActor is the same principal, client, and agent as the fixture
// actor in a different session. DeriveWorkflowActorRef includes the session, so
// this is a distinct actor_ref that no row records.
func secondSessionActor() WorkflowActor {
	actor := stepFixtureActor()
	actor.SessionRef = "session-2"
	return actor
}

// A work item is drivable by a session that did not capture it.
//
// The action guard admits an unrecorded actor and appends
// workflow.actor_recorded with it inside the action transaction. The preflight
// used to refuse first, which made that path unreachable and pinned every work
// item to its capturing session: a restart, a resume, a retarget, or any
// handoff was locked out of its own workflow.
func TestPreflightAdmitsAnActorTheCapturingSessionNeverRecorded(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	seedStepWork(t, s, "work-handoff")
	var implementation WorkflowDefinition
	for _, definition := range BuiltinWorkflowDefinitions() {
		if definition.Ref == "workflow.implementation" {
			implementation = definition
		}
	}
	initializeStepWorkflow(t, s, "work-handoff", implementation)

	ctx := context.Background()
	var version int64
	if err := s.db.QueryRow(`SELECT version FROM work_items WHERE id=?`, "work-handoff").Scan(&version); err != nil {
		t.Fatal(err)
	}

	// Only the capturing session's actor is recorded.
	recordedRef, err := WorkflowActorRef(stepFixtureActor())
	if err != nil {
		t.Fatal(err)
	}
	secondRef, err := WorkflowActorRef(secondSessionActor())
	if err != nil {
		t.Fatal(err)
	}
	if recordedRef == secondRef {
		t.Fatal("the two sessions derived one actor reference; the fixture proves nothing")
	}
	var rows int
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_actors WHERE actor_ref=?`, secondRef).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("second session actor rows=%d, want none before its first action", rows)
	}

	request := WorkflowActionPreflightRequest{
		WorkID:          "work-handoff",
		ExpectedVersion: version,
		StepID:          readInstanceStep(t, s, "work-handoff"),
		ActionID:        "record_proposal",
		Actor:           secondSessionActor(),
	}
	if err := WorkflowActionPreflightWithRegistry(ctx, s, BuiltinWorkflowRegistry(), request); err != nil {
		if strings.Contains(err.Error(), "workflow actor is not recorded") {
			t.Fatalf("preflight refused an unrecorded actor: %v", err)
		}
		t.Fatalf("preflight failed for another reason: %v", err)
	}
}

// A recorded actor_ref whose tuple disagrees still fails closed. The preflight
// stopped reading the actor row, so this check must remain reachable in the
// guard, which reads both the stored tuple and the authenticated one.
func TestRecordedActorTupleMismatchStillFailsClosed(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	seedStepWork(t, s, "work-mismatch")
	var implementation WorkflowDefinition
	for _, definition := range BuiltinWorkflowDefinitions() {
		if definition.Ref == "workflow.implementation" {
			implementation = definition
		}
	}
	initializeStepWorkflow(t, s, "work-mismatch", implementation)

	ctx := context.Background()
	actorRef, err := WorkflowActorRef(stepFixtureActor())
	if err != nil {
		t.Fatal(err)
	}
	impostor := stepFixtureActor()
	impostor.AgentRef = "agent-impostor"

	guard := &workflowActionGuardContext{ctx: ctx, actorRef: actorRef, request: WorkflowActionExecutionRequest{Actor: impostor}}
	err = s.Transact(ctx, func(transaction *Transaction) error {
		tx, txErr := transactionSQL(transaction, "workflow_actor_mismatch_test")
		if txErr != nil {
			return txErr
		}
		guard.tx = tx
		return guardRecordedActorTuple(guard)
	})
	if err == nil {
		t.Fatal("a recorded actor_ref carrying another tuple was admitted")
	}
	if !strings.Contains(err.Error(), "does not match the authenticated actor") {
		t.Fatalf("mismatch refusal = %v, want the tuple mismatch", err)
	}
}

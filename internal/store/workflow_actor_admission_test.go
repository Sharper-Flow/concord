package store

import (
	"context"
	"database/sql"
	"encoding/json"
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
	t.Parallel()
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
		Payload:         json.RawMessage(`{"problem":"The bounded problem statement.","affected":["The affected system."],"stakes":"The bounded stakes statement.","user_outcomes":["The expected user outcome."]}`),
		Actor:           secondSessionActor(),
	}
	if err := WorkflowActionPreflightWithRegistry(ctx, s, BuiltinWorkflowRegistry(), request); err != nil {
		if strings.Contains(err.Error(), "workflow actor is not recorded") {
			t.Fatalf("preflight refused an unrecorded actor: %v", err)
		}
		t.Fatalf("preflight failed for another reason: %v", err)
	}
}

// seedMismatchedActorRowTx opens an isolated synthetic SQLite database whose
// only table is the unguarded workflow_actors lookup, stores actor's real
// tuple under its canonical actor_ref with a deliberately inconsistent
// principal_ref, and returns the active transaction the fail-closed checks
// read through. In real authority workflow_actors is immutable and write-seam-only,
// and foldWorkflowActorRecorded refuses an event whose ref disagrees with its
// tuple, so no write or fold route can produce this row: it is negative
// corruption input, never a fabricated successful actor or approval.
func seedMismatchedActorRowTx(t *testing.T, ctx context.Context, actor WorkflowActor) *sql.Tx {
	t.Helper()
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open(driverName, "file:"+t.TempDir()+"/actor-mismatch.db")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = tx.Rollback()
		if closeErr := db.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	})
	if _, err := tx.Exec(`CREATE TABLE workflow_actors (actor_ref TEXT, principal_ref TEXT, client_ref TEXT, agent_ref TEXT, session_ref TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO workflow_actors (actor_ref,principal_ref,client_ref,agent_ref,session_ref) VALUES (?,?,?,?,?)`, actorRef, "principal/tampered", actor.ClientRef, actor.AgentRef, actor.SessionRef); err != nil {
		t.Fatal(err)
	}
	return tx
}

// A recorded actor_ref whose tuple disagrees still fails closed. The preflight
// stopped reading the actor row, so this check must remain reachable in the
// guard, which reads both the stored tuple and the authenticated one. Real
// dispatch derives the guard's actor ref from the request's own tuple, so the
// guard runs here with that same canonical ref and requested tuple against
// the synthetic mismatched row.
func TestRecordedActorTupleMismatchStillFailsClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	requested := stepFixtureActor()
	requestedRef, err := WorkflowActorRef(requested)
	if err != nil {
		t.Fatal(err)
	}
	guard := &workflowActionGuardContext{ctx: ctx, tx: seedMismatchedActorRowTx(t, ctx, requested), actorRef: requestedRef, request: WorkflowActionExecutionRequest{Actor: requested}}
	err = guardRecordedActorTuple(guard)
	if err == nil {
		t.Fatal("a recorded actor_ref carrying another tuple was admitted")
	}
	if !strings.Contains(err.Error(), "does not match the authenticated actor") {
		t.Fatalf("mismatch refusal = %v, want the tuple mismatch", err)
	}
	if guard.actorRecordPrefix != nil {
		t.Fatalf("mismatched tuple retained a recording draft %+v, want no actor prefix", guard.actorRecordPrefix)
	}
}

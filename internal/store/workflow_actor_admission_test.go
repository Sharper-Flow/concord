package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

// secondSessionActor is the capture actor in a different session.
func secondSessionActor() WorkflowActor {
	return WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord", AgentRef: "agent/concord", SessionRef: "session/bootstrap-work-handoff-restarted", ActorClass: ActorOperator}
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
	s, version := seedBootstrapCapturedWork(t, "work-handoff")
	defer s.Close()

	ctx := context.Background()

	// Only the capturing session's actor is recorded.
	capturing := secondSessionActor()
	capturing.SessionRef = "session/bootstrap-work-handoff"
	recordedRef, err := WorkflowActorRef(capturing)
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

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	step, _, err := readWorkflowActionStateTx(ctx, tx, "work-handoff", version)
	if err != nil {
		t.Fatal(err)
	}
	request := WorkflowActionPreflightRequest{
		WorkID:          "work-handoff",
		ExpectedVersion: version,
		StepID:          step,
		ActionID:        "record_intake",
		Payload:         json.RawMessage(`{"outcome":"proceed"}`),
		Actor:           secondSessionActor(),
	}
	if _, err := workflowActionPreflightTx(ctx, tx, request); err != nil {
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
// stopped reading the actor row, so the shared actor-prefix owner must still
// compare the stored tuple with the authenticated one.
func TestRecordedActorTupleMismatchStillFailsClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	requested := stepFixtureActor()
	prefix, err := workflowActorRecordPrefixTx(ctx, seedMismatchedActorRowTx(t, ctx, requested), requested)
	if err == nil {
		t.Fatal("a recorded actor_ref carrying another tuple was admitted")
	}
	if !strings.Contains(err.Error(), "does not match the authenticated actor") {
		t.Fatalf("mismatch refusal = %v, want the tuple mismatch", err)
	}
	if prefix != nil {
		t.Fatalf("mismatched tuple retained a recording draft %+v, want no actor prefix", prefix)
	}
}

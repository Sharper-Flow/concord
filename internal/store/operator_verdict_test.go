package store

import (
	"encoding/json"
	"testing"
)

// #909: a host restart mints a new session identity, so the completing
// action can be the first place that tuple appears. The complete path lands
// the guard's actor-recording events instead of dropping them.
func TestCompleteRecordsAFirstSeenActorTuple(t *testing.T) {
	t.Parallel()
	const workID = "complete-first-seen-actor"
	f := qCurrentAcceptance(t, workID)
	ctx := t.Context()
	if err := f.run(ctx, "confirm_premise", "confirm", f.version(ctx, t), nil, nil); err != nil {
		t.Fatalf("confirm the premise: %v", err)
	}
	completeWorkflowPlanFixture(t, f.store, workID)
	// The restarted session: same principal and client, new agent and
	// session identity, never recorded on this work item before.
	restarted := WorkflowActor{PrincipalRef: f.owner.PrincipalRef, ClientRef: f.owner.ClientRef, AgentRef: "agent/owner-restarted", SessionRef: "session/" + workID + "-restarted", ActorClass: ActorAgent}
	restartedFixture := f
	restartedFixture.owner = restarted
	if err := restartedFixture.run(ctx, "complete", "complete", f.version(ctx, t), json.RawMessage(`{"impact_verdict":"non-breaking"}`), nil); err != nil {
		t.Fatalf("complete from a first-seen actor tuple refused: %v", err)
	}
	var state string
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "completed" {
		t.Fatalf("instance_state=%s, want completed", state)
	}
	restartedRef, _ := WorkflowActorRef(restarted)
	var recorded int
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_actors WHERE actor_ref=?`, restartedRef).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 1 {
		t.Fatalf("restarted actor recordings=%d, want 1", recorded)
	}
}

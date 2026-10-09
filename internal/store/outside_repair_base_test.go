package store

import (
	"context"
	"testing"
)

// This reproduction uses only pre-existing test helpers and literal event
// names, so it runs on the git base without copying implementation code.
func TestOutsideRepairTypedHoldBaseReproduction(t *testing.T) {
	s := openTemp(t)
	work := "outside-base-reproduction"
	seedWork(t, s, work)
	operator := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "approval:" + deliveryCorrectionApprovalRef, SessionRef: "session/outside-base", ActorClass: ActorOperator}
	ref, err := WorkflowActorRef(operator)
	if err != nil {
		t.Fatal(err)
	}
	actor := workflowEvent(work+":operator", WorkflowActorRecorded, work, map[string]any{
		"work_id": work, "expected_version": 2, "resulting_version": 3,
		"actor_ref": ref, "principal_ref": operator.PrincipalRef, "client_ref": operator.ClientRef,
		"agent_ref": operator.AgentRef, "session_ref": operator.SessionRef, "actor_class": "operator",
	})
	hold := workflowEventWithActor(work+":hold", "workflow.outside_repair_disposition_set", work, ref, map[string]any{
		"work_id": work, "expected_version": 3, "resulting_version": 4,
		"reason": "bounded defect repair before any merge or release", "state": "active",
		"approval_ref": deliveryCorrectionApprovalRef, "approval_operation_digest": deliveryCorrectionDigest,
		"approval_scope_json": deliveryCorrectionScopeJSON(work), "approval_versions_json": deliveryCorrectionVersionsJSON(2), "approval_consequence": "recovery",
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{actor, hold}, ExpectedVersions: workVersion(work, 2)}); err != nil {
		t.Fatalf("typed operator hold must append before completion evidence exists: %v", err)
	}
	pin, err := ReadWorkPin(context.Background(), s, work)
	if err != nil {
		t.Fatalf("hold without an instance must remain readable: %v", err)
	}
	if len(pin.NextValidIntents) != 0 {
		t.Fatalf("held work advertises managed intents: %+v", pin.NextValidIntents)
	}
}

package store

import (
	"encoding/json"
	"testing"
)

func TestWorkflowPlanPreflightReadsWithoutEffects(t *testing.T) {
	const workID = "plan-preflight-read-only"
	f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanChange, "implement", nil)
	defer f.store.Close()
	actor := f.owner
	actor.SessionRef = "session:preflight-read-only"
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, f.store, workID)
	events := approvalEventCount(t, f.store, workID)
	var planBefore string
	if err := f.store.db.QueryRow(`SELECT step_plan FROM workflow_instances WHERE work_id=?`, workID).Scan(&planBefore); err != nil {
		t.Fatal(err)
	}
	request := WorkflowActionPreflightRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: "bind_evidence", Actor: actor,
		Payload: json.RawMessage(`{"evidence_kind":"artifact","immutable_subject_ref":"artifact:preflight","producer_id":"principal:preflight","producer_run_ref":"run:preflight","producer_watermark":"request:preflight"}`),
	}
	for i := 0; i < 2; i++ {
		action, err := WorkflowActionDefinitionFor(t.Context(), f.store, request)
		if err != nil || action.ID != request.ActionID {
			t.Fatalf("preflight action=%+v err=%v", action, err)
		}
	}
	stale := request
	stale.ExpectedVersion++
	if _, err := WorkflowActionDefinitionFor(t.Context(), f.store, stale); !hasFailureKind(err, KindVersionConflict) {
		t.Fatalf("stale preflight error=%v", err)
	}
	malformed := request
	malformed.Payload = json.RawMessage(`{"unexpected":true}`)
	if _, err := WorkflowActionDefinitionFor(t.Context(), f.store, malformed); !hasFailureKind(err, KindInvalidPayload) {
		t.Fatalf("malformed preflight error=%v", err)
	}
	var planAfter string
	var actors, guards int
	if err := f.store.db.QueryRow(`SELECT step_plan FROM workflow_instances WHERE work_id=?`, workID).Scan(&planAfter); err != nil {
		t.Fatal(err)
	}
	if err := f.store.db.QueryRow(`SELECT count(*) FROM workflow_actors WHERE actor_ref=?`, actorRef).Scan(&actors); err != nil {
		t.Fatal(err)
	}
	if err := f.store.db.QueryRow(`SELECT count(*) FROM write_guard`).Scan(&guards); err != nil {
		t.Fatal(err)
	}
	if planAfter != planBefore || readWorkVersion(t, f.store, workID) != version || approvalEventCount(t, f.store, workID) != events || actors != 0 || guards != 0 {
		t.Fatalf("preflight changed state: plan changed=%v actors=%d guards=%d", planAfter != planBefore, actors, guards)
	}
}

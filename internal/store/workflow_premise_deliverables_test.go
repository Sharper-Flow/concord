package store

import (
	"encoding/json"
	"testing"
)

func TestConfirmPremiseRecordsActiveContractWithoutEvidenceGate(t *testing.T) {
	t.Parallel()
	const workID = "premise-without-evidence-gate"
	route := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanChange, "review", nil)
	version := readWorkVersion(t, route.store, workID)
	request := planAdmissionRequest(workID, "confirm_premise", version, route.owner, json.RawMessage(`{}`))
	if err := planAdmissionPreflight(t, route.store, request); err != nil {
		t.Fatalf("preflight confirmation: %v", err)
	}
	if err := planAdmissionDispatch(t, route.store, request); err != nil {
		t.Fatalf("confirm premise: %v", err)
	}
	var count int
	if err := route.store.db.QueryRow(`SELECT count(*) FROM workflow_premise_confirmations WHERE work_id=? AND contract_version=1`, workID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("confirmation count=%d err=%v", count, err)
	}
	if readWorkVersion(t, route.store, workID) <= version {
		t.Fatal("confirmation did not advance the work version")
	}
}

func TestPreflightAndDispatchRejectStaleConfirmation(t *testing.T) {
	t.Parallel()
	const workID = "preflight-stale-confirmation"
	f := rAcceptance(t, workID, nil, false)
	version := readWorkVersion(t, f.store, workID)
	request := planAdmissionRequest(workID, "confirm_premise", version-1, f.owner, json.RawMessage(`{}`))
	preflight := planAdmissionFailure(t, planAdmissionPreflight(t, f.store, request), KindVersionConflict)
	dispatch := planAdmissionFailure(t, planAdmissionDispatch(t, f.store, request), KindVersionConflict)
	if preflight.Detail != dispatch.Detail || readWorkVersion(t, f.store, workID) != version {
		t.Fatalf("stale confirmation preflight=%v dispatch=%v", preflight, dispatch)
	}
}

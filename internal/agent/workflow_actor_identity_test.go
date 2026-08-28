package agent

import (
	"context"
	"encoding/json"
	"testing"
)

// principal_ref leads the idempotency_records primary key and carries identity
// in the workflow actor tuple. CD-0080 D1 derives it from the registered
// client, so an envelope that omits it must still partition under the derived
// principal. Partitioning under the empty envelope value would collide every
// caller that omits it into one partition, where a shared idempotency key
// would replay another principal's mutation.
func TestWorkflowActionPartitionsUnderDerivedPrincipal(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
	if version := seedAgentWorkflow(t, s, grant); version != 4 {
		t.Fatalf("workflow seed version=%d, want 4", version)
	}
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	env.PrincipalRef = ""

	response, err := Dispatch(context.Background(), s, service, InvokeRequest{
		Tool:      "concord_work_transition",
		Operation: "workflow_action",
		Input:     json.RawMessage(`{"work_id":"work-1","expected_version":4,"action_id":"record_proposal","fields":[],"idempotency_key":"wf-partition"}`),
	}, env)
	if err != nil || response.Outcome != OutcomeOK || response.Error != nil {
		t.Fatalf("workflow action response=%+v err=%v", response, err)
	}

	var storedPrincipal string
	if err := s.DatabaseForTesting().QueryRow(`SELECT principal_ref FROM idempotency_records WHERE operation_kind='workflow_action'`).Scan(&storedPrincipal); err != nil {
		t.Fatal(err)
	}
	if storedPrincipal != grant.PrincipalRef {
		t.Fatalf("idempotency principal_ref=%q, want the derived %q", storedPrincipal, grant.PrincipalRef)
	}

	var actors int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_actors WHERE principal_ref=''`).Scan(&actors); err != nil {
		t.Fatal(err)
	}
	if actors != 0 {
		t.Fatalf("empty principal_ref reached the workflow actor tuple: rows=%d", actors)
	}
}

// A principal the caller asserts must match the client the request authorizes
// as. CD-0080 D1 leaves no room for the envelope to name a different one.
func TestWorkflowActionRefusesForgedEnvelopePrincipal(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
	if version := seedAgentWorkflow(t, s, grant); version != 4 {
		t.Fatalf("workflow seed version=%d, want 4", version)
	}
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	env.PrincipalRef = "principal-forged"

	response, err := Dispatch(context.Background(), s, service, InvokeRequest{
		Tool:      "concord_work_transition",
		Operation: "workflow_action",
		Input:     json.RawMessage(`{"work_id":"work-1","expected_version":4,"action_id":"record_proposal","fields":[],"idempotency_key":"wf-forged"}`),
	}, env)
	if err != nil {
		t.Fatalf("forged principal dispatch err=%v", err)
	}
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "unauthorized" {
		t.Fatalf("forged principal response=%+v", response)
	}
	var records int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM idempotency_records`).Scan(&records); err != nil {
		t.Fatal(err)
	}
	if records != 0 {
		t.Fatalf("refused request left idempotency records=%d", records)
	}
}

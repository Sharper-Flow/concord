package agent

import (
	"context"
	"encoding/json"
	"testing"
)

// A session whose ambient Product differs from the observed work item's
// Product takes the approval route for concord_work_define.observation_record:
// the operation declares no approval of its own, but the derived Product scope
// crosses the selected Product, so the core mints an operator approval
// challenge. The declared input schema must carry the optional typed approval
// property the approved resubmission travels in, or the observation can never
// be recorded on a cross-Product work item.
func TestCrossProductObservationRecordReachesApprovalChallenge(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := crossProductPolicyFixture(t, []Capability{"product_read", "work_define", "cross_scope"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	request := InvokeRequest{Tool: "concord_work_define", Operation: "observation_record", Input: json.RawMessage(`{"work_id":"work-2","statement":"progress note from the other Product","idempotency_key":"cross-observation-1"}`)}
	challenge, dispatchErr := Dispatch(ctx, s, service, request, env)
	if dispatchErr != nil {
		t.Fatal(dispatchErr)
	}
	if challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		if challenge.Error != nil {
			t.Fatalf("cross-Product observation_record = error kind %q message %q, want approval_required", challenge.Error.Kind, challenge.Error.Message)
		}
		t.Fatalf("cross-Product observation_record = %+v, want approval_required", challenge)
	}
	ref, _ := challenge.Error.Details["approval_ref"].(string)
	if len(ref) != 64 {
		t.Fatalf("approval_ref = %v, want a minted 64-character challenge ref", challenge.Error.Details["approval_ref"])
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_observations WHERE work_id='work-2'`); got != 0 {
		t.Fatalf("challenge creation recorded %d work observations, want none", got)
	}
}

// The approved resubmission: an operator approval bound to the minted
// challenge's scope lets the cross-Product observation_record commit, and the
// work observation lands in work_observations, the surface the workflow
// investigation gate reads.
func TestCrossProductObservationRecordApprovalRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, privateKey := crossProductPolicyFixture(t, []Capability{"product_read", "work_define", "cross_scope"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	input := json.RawMessage(`{"work_id":"work-2","statement":"progress note from the other Product","idempotency_key":"cross-observation-2"}`)
	request := InvokeRequest{Tool: "concord_work_define", Operation: "observation_record", Input: input}
	challenge, dispatchErr := Dispatch(ctx, s, service, request, env)
	if dispatchErr != nil || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("cross-Product observation_record challenge = %+v, err = %v, want approval_required", challenge, dispatchErr)
	}
	ref := challenge.Error.Details["approval_ref"].(string)
	scope := map[string]any{"product_id": "product-1", "product_ids": []string{"product-1", "product-2"}, "project_ids": []string{"project-1"}, "scope_version": scopeVersion, "work_ids": []string{"work-2"}}
	env.HostApproval = signedHostApproval(privateKey, ref, mutationDigest(request.Tool, request.Operation, env, input), scope, map[string]any{}, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), "cross-observation-approval")
	request.Input = json.RawMessage(`{"work_id":"work-2","statement":"progress note from the other Product","idempotency_key":"cross-observation-2","approval":{"approval_ref":"` + ref + `"}}`)
	approved, dispatchErr := Dispatch(ctx, s, service, request, env)
	if dispatchErr != nil || approved.Error != nil || approved.Outcome != OutcomeOK {
		t.Fatalf("approved cross-Product observation_record = %s / %v, err = %v", approved.Outcome, approved.Error, dispatchErr)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_observations WHERE work_id='work-2'`); got != 1 {
		t.Fatalf("approved observation_record recorded %d work observations, want 1", got)
	}
}

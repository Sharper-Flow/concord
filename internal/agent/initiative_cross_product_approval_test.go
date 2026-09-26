package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// A session whose ambient Product differs from the Initiative's Product takes
// the approval route for concord_work_initiative.add_entry: the mutation's
// derived Product scope crosses the selected Product, so the core must mint an
// operator approval challenge. The Initiative and its child stay inside one
// Product, the only entry shape the store fold accepts.
func TestCrossProductInitiativeAddEntryReachesApprovalChallenge(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := crossProductPolicyFixture(t, []Capability{"product_read", "work_initiative", "cross_scope"})
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{
		{EventID: "cross-initiative", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "initiative-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"initiative","title":"Other Product Initiative","priority":1}`)},
		{EventID: "cross-initiative-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "initiative-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-2","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "initiative-2"): 0}}); err != nil {
		t.Fatal(err)
	}
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	request := InvokeRequest{Tool: "concord_work_initiative", Operation: "add_entry", Input: json.RawMessage(`{"initiative_work_id":"initiative-2","child_work_id":"work-2","expected_version":2,"position":0,"idempotency_key":"cross-initiative-add"}`)}
	challenge, dispatchErr := Dispatch(ctx, s, service, request, env)
	if dispatchErr != nil {
		t.Fatal(dispatchErr)
	}
	if challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		if challenge.Error != nil {
			t.Fatalf("cross-Product initiative add_entry = error kind %q message %q, want approval_required", challenge.Error.Kind, challenge.Error.Message)
		}
		t.Fatalf("cross-Product initiative add_entry = %+v, want approval_required", challenge)
	}
	ref, _ := challenge.Error.Details["approval_ref"].(string)
	if len(ref) != 64 {
		t.Fatalf("approval_ref = %v, want a minted 64-character challenge ref", challenge.Error.Details["approval_ref"])
	}
	if got := workVersion(t, s, "initiative-2"); got != 2 {
		t.Fatalf("challenge creation changed the Initiative version to %d", got)
	}
}

// The signed-approval round trip: an operator approval bound to the minted
// challenge's scope and versions lets the cross-Product add_entry commit, and
// the Initiative and its child end up inside one Product — the only entry
// shape the store fold accepts.
func TestCrossProductInitiativeApprovalRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, privateKey := crossProductPolicyFixture(t, []Capability{"product_read", "work_initiative", "cross_scope"})
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{
		{EventID: "cross-initiative-roundtrip", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "initiative-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"initiative","title":"Other Product Initiative","priority":1}`)},
		{EventID: "cross-initiative-roundtrip-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "initiative-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-2","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "initiative-2"): 0}}); err != nil {
		t.Fatal(err)
	}
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	input := json.RawMessage(`{"initiative_work_id":"initiative-2","child_work_id":"work-2","expected_version":2,"position":0,"idempotency_key":"cross-initiative-roundtrip"}`)
	request := InvokeRequest{Tool: "concord_work_initiative", Operation: "add_entry", Input: input}
	challenge, dispatchErr := Dispatch(ctx, s, service, request, env)
	if dispatchErr != nil || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("cross-Product initiative challenge = %+v, err = %v, want approval_required", challenge, dispatchErr)
	}
	ref := challenge.Error.Details["approval_ref"].(string)
	scope := map[string]any{"product_id": "product-1", "product_ids": []string{"product-1", "product-2"}, "project_ids": []string{"project-1"}, "work_ids": []string{"initiative-2", "work-2"}, "scope_version": scopeVersion}
	versions := map[string]any{"initiative": workVersion(t, s, "initiative-2")}
	env.HostApproval = signedHostApproval(privateKey, ref, mutationDigest(request.Tool, request.Operation, env, input), scope, versions, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), "cross-initiative-roundtrip-approval")
	request.Input = json.RawMessage(`{"initiative_work_id":"initiative-2","child_work_id":"work-2","expected_version":2,"position":0,"idempotency_key":"cross-initiative-roundtrip","approval":{"approval_ref":"` + ref + `"}}`)
	approved, dispatchErr := Dispatch(ctx, s, service, request, env)
	if dispatchErr != nil || approved.Error != nil || approved.Outcome != OutcomeOK {
		t.Fatalf("approved cross-Product initiative add_entry = %s / %v, err = %v", approved.Outcome, approved.Error, dispatchErr)
	}
	if got := workVersion(t, s, "initiative-2"); got != 3 {
		t.Fatalf("approved add_entry left the Initiative at version %d, want 3", got)
	}
	if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM initiative_entries WHERE initiative_work_id='initiative-2' AND child_work_id='work-2'`) != 1 {
		t.Fatal("approved add_entry did not record the child inside the Initiative's own Product scope")
	}
}

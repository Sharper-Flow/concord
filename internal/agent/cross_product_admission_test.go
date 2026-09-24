package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

// crossProductPolicyFixture seeds two Products whose Projects are disjoint:
// the ambient Project belongs to product-1 alone, and product-2 owns only
// project-2. The trusted client policy lists both Products, so a session
// resolving in project-1 holds an explicit policy grant for product-2 that no
// ambient membership covers. This is the shape a shared work identity runs
// under when one Product's work carries another Product's remote records.
func crossProductPolicyFixture(t *testing.T, capabilities []Capability) (*store.Store, *Service, Authority, ed25519.PrivateKey) {
	t.Helper()
	ctx := context.Background()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	events := []store.Event{
		{EventID: "cross-policy-product-1", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Product One","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "cross-policy-product-2", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Product Two","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "cross-policy-project-1", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Home Project"}`)},
		{EventID: "cross-policy-project-2", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Other Project"}`)},
		{EventID: "cross-policy-p1-p1", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
		{EventID: "cross-policy-p2-p2", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-2","project_id":"project-2","role":"primary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
		{EventID: "cross-policy-work-1", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"Home Work","priority":1}`)},
		{EventID: "cross-policy-work-1-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
		{EventID: "cross-policy-work-2", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"Other Work","priority":1}`)},
		{EventID: "cross-policy-work-2-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-2","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProduct, "product-2"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0, store.VersionRef(store.SubjectProject, "project-2"): 0, store.VersionRef(store.SubjectWorkItem, "work-1"): 0, store.VersionRef(store.SubjectWorkItem, "work-2"): 0}}); err != nil {
		t.Fatal(err)
	}
	service, _, grant := newAuthorizedService(t, s, "client-1", "human-1", capabilities, []string{"product-1", "product-2"}, []string{"project-1", "project-2"}, store.ProjectResolution{ProjectID: "project-1"})
	privateKey := mustKey(t)
	return s, service, grant, privateKey
}

// A policy Product the ambient Project does not own must still sit in the
// authority's Product scope: the explicit policy is the authority source, and
// collapsing it to the ambient Project's Products makes the cross_scope
// capability unreachable. The ambient candidates stay the selection set.
func TestPolicyProductsAuthorizeBeyondAmbientProject(t *testing.T) {
	ctx := context.Background()
	_, service, grant, _ := crossProductPolicyFixture(t, []Capability{"product_read", "cross_scope"})
	inv := Invocation{ClientRef: grant.ClientRef, PrincipalRef: grant.PrincipalRef, SessionRef: grant.SessionRef, AgentRef: grant.AgentRef, Directory: grant.Directory, Worktree: grant.Worktree, ManifestDigest: ManifestDigest, RequiredCapability: "product_read", ProductID: "product-1"}
	authority, err := service.Authorize(ctx, inv)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"product-1", "product-2"}; !reflect.DeepEqual(authority.ProductScope, want) {
		t.Fatalf("ProductScope = %v, want the explicit policy products %v", authority.ProductScope, want)
	}
	if want := []string{"project-1", "project-2"}; !reflect.DeepEqual(authority.ProjectScope, want) {
		t.Fatalf("ProjectScope = %v, want every Project of every authorized Product %v", authority.ProjectScope, want)
	}
	if want := []string{"product-1"}; !reflect.DeepEqual(authority.CandidateProducts, want) {
		t.Fatalf("CandidateProducts = %v, want the ambient candidates %v", authority.CandidateProducts, want)
	}
}

// A read naming a policy Product outside the ambient selection is explicit
// stable scope for an authorized Product. product_read alone admits it; the
// ambient Project does not bound reads.
func TestAuthorizedProductReadBeyondAmbientProjectSucceeds(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := crossProductPolicyFixture(t, []Capability{"product_read"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	response, dispatchErr := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_browse", Operation: "list", Input: json.RawMessage(`{"product_id":"product-2","work_ids":["work-2"],"page":{"cursor":null,"limit":10}}`)}, env)
	if dispatchErr != nil {
		t.Fatal(dispatchErr)
	}
	if response.Error != nil || response.Outcome != OutcomeOK {
		t.Fatalf("policy Product read = %s / %v, want ok without error", response.Outcome, response.Error)
	}
	var payload struct {
		Items []workSummary `json:"items"`
	}
	if err := json.Unmarshal(response.Result, &payload); err != nil {
		t.Fatalf("read result %s: %v", response.Result, err)
	}
	if len(payload.Items) != 1 || payload.Items[0].ID != "work-2" {
		t.Fatalf("read items = %+v, want the other Product's work", payload.Items)
	}
}

// A Product the policy does not name stays outside grant scope, policy
// admission does not widen to every Product.
func TestReadOfProductOutsidePolicyRefuses(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := crossProductPolicyFixture(t, []Capability{"product_read"})
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{
		{EventID: "cross-policy-product-3", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-3", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Product Three","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "cross-policy-project-3", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-3", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Third Project"}`)},
		{EventID: "cross-policy-p3-p3", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-3", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-3","project_id":"project-3","role":"primary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-3"): 0, store.VersionRef(store.SubjectProject, "project-3"): 0}}); err != nil {
		t.Fatal(err)
	}
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	response, dispatchErr := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_browse", Operation: "list", Input: json.RawMessage(`{"product_id":"product-3","page":{"cursor":null,"limit":10}}`)}, env)
	if dispatchErr != nil {
		t.Fatal(dispatchErr)
	}
	if response.Error == nil || response.Error.Kind != "unauthorized" {
		t.Fatalf("outside-policy read = %+v, want unauthorized", response)
	}
}

// A cross-Product membership mutation demands the cross_scope capability and
// then an operator approval; it must reach the approval challenge instead of
// an unconditional unauthorized refusal, and the approval must carry the
// derived cross-Product scope.
func TestCrossProductMembershipChallengesUnderCrossScope(t *testing.T) {
	ctx := context.Background()
	s, service, grant, privateKey := crossProductPolicyFixture(t, []Capability{"work_relate", "cross_scope"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	input := json.RawMessage(`{"work_id":"work-1","expected_version":2,"memberships":[{"project_id":"project-2","role":"secondary"}],"idempotency_key":"cross-membership-1"}`)
	request := InvokeRequest{Tool: "concord_work_relate", Operation: "set_memberships", Input: input}
	challenge, dispatchErr := Dispatch(ctx, s, service, request, env)
	if dispatchErr != nil || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("cross-Product membership challenge = %+v, err = %v, want approval_required", challenge, dispatchErr)
	}
	if workVersion(t, s, "work-1") != 2 {
		t.Fatal("challenge creation changed the work item")
	}
	ref := challenge.Error.Details["approval_ref"].(string)
	scope := map[string]any{"product_id": "product-1", "product_ids": []string{"product-1", "product-2"}, "project_ids": []string{"project-2"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion}
	versions := map[string]any{"work": workVersion(t, s, "work-1")}
	env.HostApproval = signedHostApproval(privateKey, ref, mutationDigest(request.Tool, request.Operation, env, input), scope, versions, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), "cross-membership-approval")
	request.Input = json.RawMessage(`{"work_id":"work-1","expected_version":2,"memberships":[{"project_id":"project-2","role":"secondary"}],"idempotency_key":"cross-membership-1","approval":{"approval_ref":"` + ref + `"}}`)
	approved, dispatchErr := Dispatch(ctx, s, service, request, env)
	if dispatchErr != nil || approved.Error != nil || approved.Outcome != OutcomeOK {
		t.Fatalf("approved cross-Product membership = %s / %v, err = %v", approved.Outcome, approved.Error, dispatchErr)
	}
	memberships, err := s.ProjectsForWork(ctx, "work-1")
	if err != nil || len(memberships) != 1 || memberships[0].ID != "project-2" {
		t.Fatalf("work memberships = %+v, err = %v", memberships, err)
	}
}

// Without the cross_scope capability the same mutation refuses before any
// effect, and the refusal names the missing capability rather than a scope
// the client policy actually authorizes.
func TestCrossProductMembershipWithoutCrossScopeRefusesBeforeEffect(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := crossProductPolicyFixture(t, []Capability{"work_relate"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	response, dispatchErr := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "set_memberships", Input: json.RawMessage(`{"work_id":"work-1","expected_version":2,"memberships":[{"project_id":"project-2","role":"secondary"}],"idempotency_key":"cross-membership-2"}`)}, env)
	if dispatchErr != nil || response.Error == nil || response.Error.Kind != "unauthorized" {
		t.Fatalf("no-cross_scope response = %+v, err = %v, want unauthorized", response, dispatchErr)
	}
	if !strings.Contains(response.Error.Message, "cross_scope") {
		t.Fatalf("refusal = %q, want it to name the cross_scope capability", response.Error.Message)
	}
	if workVersion(t, s, "work-1") != 2 || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`) != 0 {
		t.Fatal("refused membership changed state or created a challenge")
	}
}

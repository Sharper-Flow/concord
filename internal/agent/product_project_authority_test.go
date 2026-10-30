package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func addAuthoritySibling(t *testing.T, s *store.Store) {
	t.Helper()
	err := store.ApplyOperation(context.Background(), s, store.Operation{
		Events: []store.Event{
			{EventID: "authority-sibling-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("project.created"), Payload: json.RawMessage(`{"display_name":"Sibling"}`)},
			{EventID: "authority-sibling-membership", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-2","role":"secondary","reason":"fixture","expected_version":2,"resulting_version":3}`)},
		},
		ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProject, "project-2"): 0, store.VersionRef(store.SubjectProduct, "product-1"): 2},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func removeAuthoritySibling(t *testing.T, s *store.Store) {
	t.Helper()
	err := store.ApplyOperation(context.Background(), s, store.Operation{
		Events: []store.Event{
			{EventID: "authority-other-product", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-other", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Other Product","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
			{EventID: "authority-other-membership", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-other", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-other","project_id":"project-2","role":"primary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
			{EventID: "authority-remove-sibling", Kind: "product_project.removed", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-2","role":"secondary","reason":"fixture","expected_version":3,"resulting_version":4}`)},
		},
		ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 3, store.VersionRef(store.SubjectProduct, "product-other"): 0},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func productAuthorityInvocation(grant Authority) Invocation {
	return Invocation{ClientRef: grant.ClientRef, PrincipalRef: grant.PrincipalRef, SessionRef: grant.SessionRef, AgentRef: grant.AgentRef, Directory: grant.Directory, Worktree: grant.Worktree, ManifestDigest: agentcontract.ManifestDigest, RequiredCapability: "work_relate", ProductID: "product-1"}
}

func TestSameProductSiblingMembershipMutationNeedsNoConsent(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_relate"})
	addAuthoritySibling(t, s)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	request := InvokeRequest{Tool: "concord_work_relate", Operation: "set_memberships", Input: json.RawMessage(`{"work_id":"work-1","expected_version":2,"memberships":[{"project_id":"project-2","role":"primary"}],"idempotency_key":"sibling-move"}`)}
	response, err := Dispatch(ctx, s, service, request, env)
	if err != nil || response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("sibling membership = %+v, err = %v", response, err)
	}
	memberships, err := s.ProjectsForWork(ctx, "work-1")
	if err != nil || len(memberships) != 1 || memberships[0].ID != "project-2" {
		t.Fatalf("work memberships = %+v, err = %v", memberships, err)
	}
}

func TestProjectOutsideAuthorizedProductRefusesBeforeEffect(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_relate"})
	addAuthoritySibling(t, s)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	removeAuthoritySibling(t, s)
	env := mutationEnvelope(grant, scopeVersion)
	request := InvokeRequest{Tool: "concord_work_relate", Operation: "set_memberships", Input: json.RawMessage(`{"work_id":"work-1","expected_version":2,"memberships":[{"project_id":"project-2","role":"primary"}],"idempotency_key":"outside-product"}`)}
	response, err := Dispatch(ctx, s, service, request, env)
	if err != nil || response.Error == nil || response.Error.Kind != "stale_context" {
		t.Fatalf("stale removal response = %+v, err = %v", response, err)
	}
	env.ScopeVersion, _, err = s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	response, err = Dispatch(ctx, s, service, request, env)
	if err != nil || response.Error == nil || response.Error.Kind != "unauthorized" {
		t.Fatalf("outside-product response = %+v, err = %v", response, err)
	}
	if workVersion(t, s, "work-1") != 2 {
		t.Fatal("refused membership changed state")
	}
}

func TestSharedProjectStaysAmbiguousUntilProductSelected(t *testing.T) {
	ctx := context.Background()
	s, service, grant := crossProductDispatchFixture(t, []agentcontract.Capability{"product_read", "work_relate"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	env.SelectedProductID = ""
	response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_browse", Operation: "scope", Input: json.RawMessage(`{"product_id":"product-1","work_id":"work-1"}`)}, env)
	if err != nil || response.Error == nil || response.Error.Kind != "ambiguous_scope" {
		t.Fatalf("unselected shared Project = %+v, err = %v", response, err)
	}
	inv := productAuthorityInvocation(grant)
	for _, selection := range []struct {
		product  string
		projects []string
	}{
		{"product-1", []string{"project-1"}},
		{"product-2", []string{"project-1"}},
	} {
		inv.ProductID = selection.product
		authority, err := service.Authorize(ctx, inv)
		if err != nil || !reflect.DeepEqual(authority.ProjectScope, selection.projects) {
			t.Fatalf("Product %s Project scope = %v, err = %v", selection.product, authority.ProjectScope, err)
		}
	}
}

func TestAmbiguousProductRefusesMutationBeforeEffects(t *testing.T) {
	ctx := context.Background()
	s, service, grant := crossProductDispatchFixture(t, []agentcontract.Capability{"work_relate"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	env.SelectedProductID = ""
	before := agentCurrentEffects(t, s)
	response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "set_memberships", Input: json.RawMessage(`{"work_id":"work-1","expected_version":2,"memberships":[{"project_id":"project-1","role":"primary"}],"idempotency_key":"ambiguous-membership"}`)}, env)
	if err != nil || response.Error == nil || response.Error.Kind != "ambiguous_scope" {
		t.Fatalf("ambiguous mutation response=%+v err=%v", response, err)
	}
	if after := agentCurrentEffects(t, s); after != before {
		t.Fatalf("ambiguous mutation changed current rows or receipts: before=%s after=%s", before, after)
	}
}

func TestRemovedProjectLosesAuthorityOnNextInvocation(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_relate"})
	addAuthoritySibling(t, s)
	inv := productAuthorityInvocation(grant)
	if authority, err := service.Authorize(ctx, inv); err != nil || !reflect.DeepEqual(authority.ProjectScope, []string{"project-1", "project-2"}) {
		t.Fatalf("current sibling authority = %v, err = %v", authority.ProjectScope, err)
	}
	removeAuthoritySibling(t, s)
	authority, err := service.Authorize(ctx, inv)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(authority.ProjectScope, []string{"project-1"}) {
		t.Fatalf("removed sibling authority = %v, want [project-1]", authority.ProjectScope)
	}
}

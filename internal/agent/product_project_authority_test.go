package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"reflect"
	"strconv"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

func addAuthoritySibling(t *testing.T, s *store.Store) {
	t.Helper()
	err := store.ApplyOperation(context.Background(), s, store.Operation{
		Events: []store.Event{
			{EventID: "authority-sibling-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Sibling"}`)},
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
	return Invocation{ClientRef: grant.ClientRef, PrincipalRef: grant.PrincipalRef, SessionRef: grant.SessionRef, AgentRef: grant.AgentRef, Directory: grant.Directory, Worktree: grant.Worktree, ManifestDigest: ManifestDigest, RequiredCapability: "work_relate", ProductID: "product-1"}
}

func TestSameProductSiblingMembershipMutationPassesApprovalChallenge(t *testing.T) {
	ctx := context.Background()
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_relate"})
	addAuthoritySibling(t, s)

	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	version := workVersion(t, s, "work-1")
	request := InvokeRequest{Tool: "concord_work_relate", Operation: "set_memberships", Input: json.RawMessage(`{"work_id":"work-1","expected_version":2,"memberships":[{"project_id":"project-2","role":"primary"}],"idempotency_key":"sibling-move"}`)}
	challenge, err := Dispatch(ctx, s, service, request, env)
	if err != nil || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("sibling membership challenge = %+v, err = %v", challenge, err)
	}
	ref := challenge.Error.Details["approval_ref"].(string)
	scope := map[string]any{"product_id": "product-1", "product_ids": []string{"product-1"}, "project_ids": []string{"project-2"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion}
	env.HostApproval = signedHostApproval(privateKey, ref, mutationDigest(request.Tool, request.Operation, env, request.Input), scope, map[string]any{"work": version}, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), "sibling-approval")
	request.Input = json.RawMessage(`{"work_id":"work-1","expected_version":2,"memberships":[{"project_id":"project-2","role":"primary"}],"idempotency_key":"sibling-move","approval":{"approval_ref":"` + ref + `"}}`)
	approved, err := Dispatch(ctx, s, service, request, env)
	if err != nil || approved.Outcome != OutcomeOK {
		t.Fatalf("approved sibling membership = %+v, err = %v", approved, err)
	}
	memberships, err := s.ProjectsForWork(ctx, "work-1")
	if err != nil || len(memberships) != 1 || memberships[0].ID != "project-2" {
		t.Fatalf("work memberships = %+v, err = %v", memberships, err)
	}
}

func TestProjectOutsideAuthorizedProductRefusesBeforeEffect(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_relate"})
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
	if workVersion(t, s, "work-1") != 2 || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`) != 0 {
		t.Fatal("refused membership changed state or created a challenge")
	}
}

func TestSharedProjectStaysAmbiguousUntilProductSelected(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := crossProductDispatchFixture(t, []Capability{"product_read", "work_relate"})
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

func TestRemovedProjectLosesAuthorityOnNextInvocation(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_relate"})
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

// productProjectLinkFixture builds three Products with three Projects:
// project-1 is shared by product-1 and product-2, project-2 belongs to
// product-2 alone, and project-3 belongs to product-3 alone. The grant covers
// product-1 and product-2 only, so project-3 is the untrusted case. work-1
// sits in project-1 and work-2 in project-2, so linking project-2 into
// product-1 changes derived scope for work-2.
func productProjectLinkFixture(t *testing.T, capabilities []Capability) (*store.Store, *Service, Authority, ed25519.PrivateKey) {
	return productProjectLinkFixturePolicy(t, capabilities, []string{"product-1", "product-2"}, []string{"project-1", "project-2"})
}

// productProjectLinkFixturePolicy builds the same three-Product topology and
// registers the trusted client with the named policy scopes, so a test can
// admit a Project the ambient-derived grant scope cannot see.
func productProjectLinkFixturePolicy(t *testing.T, capabilities []Capability, products, projects []string) (*store.Store, *Service, Authority, ed25519.PrivateKey) {
	t.Helper()
	ctx := context.Background()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	events := []store.Event{
		{EventID: "link-product-1", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Product One","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "link-product-2", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Product Two","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "link-product-3", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-3", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Product Three","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "link-project-1", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Ambient Project"}`)},
		{EventID: "link-project-2", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Sibling Project"}`)},
		{EventID: "link-project-3", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-3", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Foreign Project"}`)},
		{EventID: "link-p1-p1", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
		{EventID: "link-p2-p1", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-2","project_id":"project-1","role":"secondary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
		{EventID: "link-p2-p2", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-2","project_id":"project-2","role":"primary","reason":"fixture","expected_version":2,"resulting_version":3}`)},
		{EventID: "link-p3-p3", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-3", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-3","project_id":"project-3","role":"primary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
		{EventID: "link-work-1", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"Ambient Work","priority":1}`)},
		{EventID: "link-work-1-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
		{EventID: "link-work-2", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"Sibling Work","priority":1}`)},
		{EventID: "link-work-2-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-2","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProduct, "product-2"): 0, store.VersionRef(store.SubjectProduct, "product-3"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0, store.VersionRef(store.SubjectProject, "project-2"): 0, store.VersionRef(store.SubjectProject, "project-3"): 0, store.VersionRef(store.SubjectWorkItem, "work-1"): 0, store.VersionRef(store.SubjectWorkItem, "work-2"): 0}}); err != nil {
		t.Fatal(err)
	}
	service, _, grant := newAuthorizedService(t, s, "client-1", "human-1", capabilities, products, projects, store.ProjectResolution{ProjectID: "project-1"})
	privateKey := mustKey(t)
	return s, service, grant, privateKey
}

func productProjectLinkRequest(approvalRef string, expectedVersion int64, projectID string) json.RawMessage {
	input := `{"product_id":"product-1","project_id":"` + projectID + `","role":"secondary","reason":"register sibling project","expected_version":` + strconv.FormatInt(expectedVersion, 10) + `,"idempotency_key":"link-project"}`
	if approvalRef != "" {
		input = `{"product_id":"product-1","project_id":"` + projectID + `","role":"secondary","reason":"register sibling project","expected_version":` + strconv.FormatInt(expectedVersion, 10) + `,"idempotency_key":"link-project","approval":{"approval_ref":"` + approvalRef + `"}}`
	}
	return json.RawMessage(input)
}

func productProjectLinkChallenge(t *testing.T, s *store.Store, service *Service, grant Authority, privateKey ed25519.PrivateKey) (string, CallEnvelope, map[string]any) {
	return productProjectLinkChallengeFor(t, s, service, grant, privateKey, "project-2", []string{"product-1", "product-2"})
}

func productProjectLinkChallengeFor(t *testing.T, s *store.Store, service *Service, grant Authority, privateKey ed25519.PrivateKey, projectID string, productIDs []string) (string, CallEnvelope, map[string]any) {
	t.Helper()
	ctx := context.Background()
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	request := InvokeRequest{Tool: "concord_work_relate", Operation: "product_project_add", Input: productProjectLinkRequest("", 2, projectID)}
	challenge, err := Dispatch(ctx, s, service, request, env)
	if err != nil || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("link challenge = %+v, err = %v", challenge, err)
	}
	ref, _ := challenge.Error.Details["approval_ref"].(string)
	if len(ref) != 64 {
		t.Fatalf("link challenge approval_ref = %q", ref)
	}
	scope := map[string]any{"product_id": "product-1", "product_ids": productIDs, "project_id": projectID, "role": "secondary", "scope_version": scopeVersion}
	env.HostApproval = signedHostApproval(privateKey, ref, mutationDigest(request.Tool, request.Operation, env, productProjectLinkRequest(ref, 2, projectID)), scope, map[string]any{"product": int64(2)}, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), "link-approval")
	return ref, env, scope
}

func TestProductProjectAddRequiresExactOperatorApproval(t *testing.T) {
	ctx := context.Background()
	s, service, grant, privateKey := productProjectLinkFixture(t, []Capability{"work_relate", "cross_scope"})
	_, env, _ := productProjectLinkChallenge(t, s, service, grant, privateKey)
	request := InvokeRequest{Tool: "concord_work_relate", Operation: "product_project_add", Input: productProjectLinkRequest(challengeRef(env), 2, "project-2")}
	approved, err := Dispatch(ctx, s, service, request, env)
	if err != nil || approved.Outcome != OutcomeOK {
		t.Fatalf("approved link = %+v, err = %v", approved, err)
	}
	memberships, err := s.ProjectsForProduct(ctx, "product-1")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, membership := range memberships {
		if membership.ID == "project-2" && membership.Role == "secondary" {
			found = true
		}
	}
	if !found {
		t.Fatalf("product-1 memberships after link = %+v", memberships)
	}
	var productVersion int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM products WHERE id='product-1'`).Scan(&productVersion); err != nil || productVersion != 3 {
		t.Fatalf("product-1 version = %d, err = %v", productVersion, err)
	}
	// An exact replay answers with the stored result even though the consumed
	// approval and the new edge would both refuse a fresh attempt.
	replayed, err := Dispatch(ctx, s, service, request, env)
	if err != nil || replayed.Outcome != OutcomeOK || !replayed.Replayed {
		t.Fatalf("replayed link = %+v, err = %v", replayed, err)
	}
}

// challengeRef reads the approval reference an earlier challenge minted into
// the envelope's host approval assertion.
func challengeRef(env CallEnvelope) string {
	if env.HostApproval == nil {
		return ""
	}
	return env.HostApproval.ChallengeRef
}

func TestProductProjectAddReportsAffectedWorkScope(t *testing.T) {
	ctx := context.Background()
	s, service, grant, privateKey := productProjectLinkFixture(t, []Capability{"work_relate", "cross_scope"})
	_, env, _ := productProjectLinkChallenge(t, s, service, grant, privateKey)
	request := InvokeRequest{Tool: "concord_work_relate", Operation: "product_project_add", Input: productProjectLinkRequest(challengeRef(env), 2, "project-2")}
	approved, err := Dispatch(ctx, s, service, request, env)
	if err != nil || approved.Outcome != OutcomeOK {
		t.Fatalf("approved link = %+v, err = %v", approved, err)
	}
	var result struct {
		ProductVersion         int64    `json:"product_version"`
		AffectedWorkCount      int      `json:"affected_work_count"`
		TotalAffectedWorkCount int      `json:"total_affected_work_count"`
		AffectedWorkIDs        []string `json:"affected_work_ids"`
		EventIDs               []string `json:"event_ids"`
	}
	if err := json.Unmarshal(approved.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.ProductVersion != 3 || result.AffectedWorkCount != 1 || result.TotalAffectedWorkCount != 1 || len(result.EventIDs) != 1 {
		t.Fatalf("link impact = %+v", result)
	}
	if !reflect.DeepEqual(result.AffectedWorkIDs, []string{"work-2"}) {
		t.Fatalf("affected work ids = %v, want [work-2]", result.AffectedWorkIDs)
	}
	if approved.ResolvedScope == nil || approved.ResolvedScope.ProductID != "product-1" {
		t.Fatalf("resolved scope = %+v", approved.ResolvedScope)
	}
}

// The ambient grant intersects the ambient Project's Products with the policy,
// so it cannot see a Project disjoint from the ambient Products. The trusted
// check for this operation runs against the policy itself: a Project the
// policy names, whose current Products all sit in the policy Product scope,
// reaches the operator challenge even though no ambient edge carries it.
func TestProductProjectAddLinksDisjointProjectNamedByPolicy(t *testing.T) {
	ctx := context.Background()
	s, service, grant, privateKey := productProjectLinkFixturePolicy(t, []Capability{"work_relate", "cross_scope"}, []string{"product-1", "product-2", "product-3"}, []string{"project-1", "project-2", "project-3"})
	_, env, _ := productProjectLinkChallengeFor(t, s, service, grant, privateKey, "project-3", []string{"product-1", "product-3"})
	request := InvokeRequest{Tool: "concord_work_relate", Operation: "product_project_add", Input: productProjectLinkRequest(challengeRef(env), 2, "project-3")}
	approved, err := Dispatch(ctx, s, service, request, env)
	if err != nil || approved.Outcome != OutcomeOK {
		t.Fatalf("approved disjoint link = %+v, err = %v", approved, err)
	}
	memberships, err := s.ProjectsForProduct(ctx, "product-1")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, membership := range memberships {
		if membership.ID == "project-3" && membership.Role == "secondary" {
			found = true
		}
	}
	if !found {
		t.Fatalf("product-1 memberships after disjoint link = %+v", memberships)
	}
}

func TestProductProjectAddRefusals(t *testing.T) {
	ctx := context.Background()
	t.Run("cross_product_requires_cross_scope", func(t *testing.T) {
		s, service, grant, _ := productProjectLinkFixture(t, []Capability{"work_relate"})
		scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
		if err != nil {
			t.Fatal(err)
		}
		response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "product_project_add", Input: productProjectLinkRequest("", 2, "project-2")}, mutationEnvelope(grant, scopeVersion))
		if err != nil || response.Error == nil || response.Error.Kind != "unauthorized" {
			t.Fatalf("no-cross-scope response = %+v, err = %v", response, err)
		}
		if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`) != 0 {
			t.Fatal("refused link minted a challenge")
		}
	})
	t.Run("untrusted_project_refuses_before_challenge", func(t *testing.T) {
		s, service, grant, _ := productProjectLinkFixture(t, []Capability{"work_relate", "cross_scope"})
		scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
		if err != nil {
			t.Fatal(err)
		}
		response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "product_project_add", Input: productProjectLinkRequest("", 2, "project-3")}, mutationEnvelope(grant, scopeVersion))
		if err != nil || response.Error == nil || response.Error.Kind != "unauthorized" {
			t.Fatalf("untrusted project response = %+v, err = %v", response, err)
		}
		if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`) != 0 {
			t.Fatal("untrusted project minted a challenge")
		}
	})
	t.Run("unknown_project_refuses", func(t *testing.T) {
		s, service, grant, _ := productProjectLinkFixture(t, []Capability{"work_relate", "cross_scope"})
		scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
		if err != nil {
			t.Fatal(err)
		}
		response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "product_project_add", Input: productProjectLinkRequest("", 2, "project-none")}, mutationEnvelope(grant, scopeVersion))
		if err != nil || response.Error == nil || response.Error.Kind != "unknown_scope" {
			t.Fatalf("unknown project response = %+v, err = %v", response, err)
		}
	})
	t.Run("existing_edge_refuses", func(t *testing.T) {
		s, service, grant, _ := productProjectLinkFixture(t, []Capability{"work_relate", "cross_scope"})
		scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
		if err != nil {
			t.Fatal(err)
		}
		request := InvokeRequest{Tool: "concord_work_relate", Operation: "product_project_add", Input: []byte(`{"product_id":"product-1","project_id":"project-1","role":"secondary","reason":"register sibling project","expected_version":2,"idempotency_key":"link-existing"}`)}
		response, err := Dispatch(ctx, s, service, request, mutationEnvelope(grant, scopeVersion))
		if err != nil || response.Error == nil || response.Error.Kind != "invalid_input" {
			t.Fatalf("existing edge response = %+v, err = %v", response, err)
		}
		if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`) != 0 {
			t.Fatal("existing edge minted a challenge")
		}
	})
	t.Run("wrong_role_binding_refuses", func(t *testing.T) {
		s, service, grant, privateKey := productProjectLinkFixture(t, []Capability{"work_relate", "cross_scope"})
		scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
		if err != nil {
			t.Fatal(err)
		}
		env := mutationEnvelope(grant, scopeVersion)
		challenge, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "product_project_add", Input: productProjectLinkRequest("", 2, "project-2")}, env)
		if err != nil || challenge.Error == nil {
			t.Fatalf("challenge = %+v, err = %v", challenge, err)
		}
		ref := challenge.Error.Details["approval_ref"].(string)
		scope := map[string]any{"product_id": "product-1", "product_ids": []string{"product-1", "product-2"}, "project_id": "project-2", "role": "primary", "scope_version": scopeVersion}
		env.HostApproval = signedHostApproval(privateKey, ref, mutationDigest("concord_work_relate", "product_project_add", env, productProjectLinkRequest(ref, 2, "project-2")), scope, map[string]any{"product": int64(2)}, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), "wrong-role")
		response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "product_project_add", Input: productProjectLinkRequest(ref, 2, "project-2")}, env)
		if err != nil || response.Error == nil || response.Error.Kind != "approval_invalid" {
			t.Fatalf("wrong role response = %+v, err = %v", response, err)
		}
	})
	t.Run("stale_version_refuses_at_cas", func(t *testing.T) {
		s, service, grant, privateKey := productProjectLinkFixture(t, []Capability{"work_relate", "cross_scope"})
		_, env, _ := productProjectLinkChallenge(t, s, service, grant, privateKey)
		// A stage change bumps the Product version without moving the Project
		// scope, so the approval still binds and only the compare-and-swap
		// refuses the stale expected version.
		if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{{EventID: "link-stage-bump", Kind: "product.stage_changed", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"stage_maturity":"prototype","stage_audience_commitment":"operator_only","reason":"stage bump","expected_version":2,"resulting_version":3}`)}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 2}}); err != nil {
			t.Fatal(err)
		}
		response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "product_project_add", Input: productProjectLinkRequest(challengeRef(env), 2, "project-2")}, env)
		if err != nil || response.Error == nil || response.Error.Kind != "version_conflict" {
			t.Fatalf("stale version response = %+v, err = %v", response, err)
		}
		var exists int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM product_projects WHERE product_id='product-1' AND project_id='project-2'`).Scan(&exists); err != nil || exists != 0 {
			t.Fatalf("refused link created the edge (exists=%d, err=%v)", exists, err)
		}
	})
}

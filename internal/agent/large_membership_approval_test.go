package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func largeMembershipApprovalFixture(t *testing.T, projectCount int) (*store.Store, *Service, Authority, []string, []string) {
	t.Helper()
	ctx := context.Background()
	s, _, _, _ := crossProductPolicyFixture(t, []Capability{"work_relate", "cross_scope"})
	products := []string{"product-1", "product-2"}
	projects := []string{"project-1", "project-2"}
	for i := 3; i <= 9; i++ {
		product, project := fmt.Sprintf("product-%d", i), fmt.Sprintf("project-%d", i)
		applyAgentFixtureProduct(ctx, t, s, "large-"+product, product, project, product, project, "fixture")
		products = append(products, product)
		projects = append(projects, project)
	}
	for i := 10; i <= projectCount; i++ {
		project := fmt.Sprintf("project-%d", i)
		version := i - 8
		events := []store.Event{
			{EventID: "large-" + project, Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: project, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"` + project + `"}`)},
			{EventID: "large-link-" + project, Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(fmt.Sprintf(`{"product_id":"product-1","project_id":%q,"role":"secondary","reason":"fixture","expected_version":%d,"resulting_version":%d}`, project, version, version+1))},
		}
		if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProject, project): 0, store.VersionRef(store.SubjectProduct, "product-1"): int64(version)}}); err != nil {
			t.Fatal(err)
		}
		projects = append(projects, project)
	}
	sort.Strings(projects)
	service, _, grant := newAuthorizedService(t, s, "large-client", "human-1", []Capability{"work_relate", "cross_scope"}, products, projects, store.ProjectResolution{ProjectID: "project-1"})
	return s, service, grant, products, projects
}

func TestLargeCrossProductMembershipApprovalRoundTrip(t *testing.T) {
	for _, projectCount := range []int{13, 20} {
		t.Run(fmt.Sprintf("%d-projects", projectCount), func(t *testing.T) {
			testLargeMembershipApprovalRoundTrip(t, projectCount)
		})
	}
}

func testLargeMembershipApprovalRoundTrip(t *testing.T, projectCount int) {
	t.Helper()
	ctx := context.Background()
	s, service, grant, products, projects := largeMembershipApprovalFixture(t, projectCount)
	privateKey := mustKey(t)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	memberships := make([]map[string]string, 0, len(projects))
	for _, project := range projects {
		role := "secondary"
		if project == "project-1" {
			role = "primary"
		}
		memberships = append(memberships, map[string]string{"project_id": project, "role": role})
	}
	input := map[string]any{"work_id": "work-1", "expected_version": 2, "memberships": memberships, "idempotency_key": "large-memberships"}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := InvokeRequest{Tool: "concord_work_relate", Operation: "set_memberships", Input: raw}
	challenge, err := Dispatch(ctx, s, service, request, env)
	if err != nil || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("challenge = %+v, err = %v", challenge.Error, err)
	}
	if _, err := json.Marshal(challenge); err != nil {
		t.Fatalf("large membership approval must serialize: %v", err)
	}
	scope := map[string]any{"product_id": "product-1", "product_ids": products, "project_ids": projects, "work_ids": []string{"work-1"}, "scope_version": scopeVersion}
	if got := challenge.Error.ConsequenceSummary; got == nil || !reflect.DeepEqual(got.Scope, approvalScopeBindings(scope)) || len(got.Scope) != len(products)+len(projects)+3 {
		t.Fatalf("summary lost scope bindings: %+v", got)
	}
	if _, duplicated := challenge.Error.Details["scope"]; duplicated {
		t.Fatal("approval scope must have one wire owner: consequence_summary")
	}
	if _, duplicated := challenge.Error.Details["versions"]; duplicated {
		t.Fatal("approval versions must have one wire owner: consequence_summary")
	}
	if workVersion(t, s, "work-1") != 2 {
		t.Fatal("approval challenge changed work membership")
	}
	ref := challenge.Error.Details["approval_ref"].(string)
	versions := map[string]any{"work": int64(2)}
	digest := mutationDigest(request.Tool, request.Operation, env, raw)
	input["approval"] = map[string]string{"approval_ref": ref}
	request.Input, err = json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	tamperedScope := map[string]any{"product_id": "product-1", "product_ids": products, "project_ids": projects[:len(projects)-1], "work_ids": []string{"work-1"}, "scope_version": scopeVersion}
	env.HostApproval = signedHostApproval(privateKey, ref, digest, tamperedScope, versions, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), "large-tampered")
	refused, err := Dispatch(ctx, s, service, request, env)
	if err != nil || refused.Error == nil || refused.Error.Kind != "approval_invalid" || workVersion(t, s, "work-1") != 2 {
		t.Fatalf("tampered scope was not refused without effect: %+v, err = %v", refused.Error, err)
	}
	env.HostApproval = signedHostApproval(privateKey, ref, digest, scope, versions, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), "large-approved")
	approved, err := Dispatch(ctx, s, service, request, env)
	if err != nil || approved.Error != nil || approved.Outcome != OutcomeOK {
		t.Fatalf("exact-scope approval failed: %+v, err = %v", approved.Error, err)
	}
	got, err := s.ProjectsForWork(ctx, "work-1")
	if err != nil || len(got) != len(projects) || workVersion(t, s, "work-1") != 3 {
		t.Fatalf("approved membership = %+v, err = %v", got, err)
	}
	gotIDs := make([]string, 0, len(got))
	for _, project := range got {
		gotIDs = append(gotIDs, project.ID)
	}
	sort.Strings(gotIDs)
	if !reflect.DeepEqual(gotIDs, projects) {
		t.Fatalf("approved Project identities = %v, want %v", gotIDs, projects)
	}
}

func TestApprovalScopeOverSummaryCapacityRefusesBeforeMint(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _, projects := largeMembershipApprovalFixture(t, 21)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	memberships := make([]map[string]string, 0, len(projects))
	for _, project := range projects {
		memberships = append(memberships, map[string]string{"project_id": project, "role": "secondary"})
	}
	raw, err := json.Marshal(map[string]any{"work_id": "work-1", "expected_version": 2, "memberships": memberships, "idempotency_key": "over-capacity-memberships"})
	if err != nil {
		t.Fatal(err)
	}
	response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "set_memberships", Input: raw}, mutationEnvelope(grant, scopeVersion))
	if err != nil || response.Error == nil || response.Error.Kind != "limit_exceeded" || response.Error.EffectState != EffectNone || response.Error.RecoveryAction.Kind != "reduce_limit" {
		t.Fatalf("over-capacity approval = %+v, err = %v", response.Error, err)
	}
	if _, err := json.Marshal(response); err != nil {
		t.Fatalf("over-capacity refusal must serialize: %v", err)
	}
	if workVersion(t, s, "work-1") != 2 || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`) != 0 {
		t.Fatal("over-capacity approval changed work or minted an undeliverable challenge")
	}
}

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func largeMembershipApprovalFixture(t *testing.T, projectCount int) (*store.Store, *Service, Authority, []string) {
	t.Helper()
	ctx := context.Background()
	s, _, _ := crossProductPolicyFixture(t, []agentcontract.Capability{"work_relate", "cross_scope"})
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
			{EventID: "large-" + project, Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: project, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"display_name":"` + project + `"}`)},
			{EventID: "large-link-" + project, Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(fmt.Sprintf(`{"product_id":"product-1","project_id":%q,"role":"secondary","reason":"fixture","expected_version":%d,"resulting_version":%d}`, project, version, version+1))},
		}
		if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProject, project): 0, store.VersionRef(store.SubjectProduct, "product-1"): int64(version)}}); err != nil {
			t.Fatal(err)
		}
		projects = append(projects, project)
	}
	sort.Strings(projects)
	service, _, grant := newAuthorizedService(t, s, "large-client", "human-1", []agentcontract.Capability{"work_relate", "cross_scope"}, products, projects, store.ProjectResolution{ProjectID: "project-1"})
	return s, service, grant, projects
}

// CD-0243 D3/D7 retire ordinary membership consent. Approval summary bounds
// still apply to security grants, not to an ordinary membership mutation.
func TestLargeCrossProductMembershipWithoutConsent(t *testing.T) {
	for _, projectCount := range []int{13, 20} {
		t.Run(fmt.Sprintf("%d-projects", projectCount), func(t *testing.T) {
			testLargeMembershipApprovalRoundTrip(t, projectCount)
		})
	}
}

func testLargeMembershipApprovalRoundTrip(t *testing.T, projectCount int) {
	t.Helper()
	ctx := context.Background()
	s, service, grant, projects := largeMembershipApprovalFixture(t, projectCount)
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
	approved, err := Dispatch(ctx, s, service, request, env)
	if err != nil || approved.Error != nil || approved.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("ordinary membership failed: %+v, err = %v", approved.Error, err)
	}
	if _, err := approved.Encode(); err != nil {
		t.Fatalf("large membership response must serialize: %v", err)
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
	replay, err := Dispatch(ctx, s, service, request, env)
	if err != nil || replay.Outcome != agentcontract.OutcomeOK || !replay.Replayed || workVersion(t, s, "work-1") != 3 {
		t.Fatalf("large membership replay=%+v err=%v", replay.Error, err)
	}
}

func TestMembershipScopeSupportsTwentyOneProjects(t *testing.T) {
	ctx := context.Background()
	s, service, grant, projects := largeMembershipApprovalFixture(t, 21)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	memberships := make([]map[string]string, 0, len(projects))
	for _, project := range projects {
		role := "secondary"
		if project == "project-1" {
			role = "primary"
		}
		memberships = append(memberships, map[string]string{"project_id": project, "role": role})
	}
	raw, err := json.Marshal(map[string]any{"work_id": "work-1", "expected_version": 2, "memberships": memberships, "idempotency_key": "over-capacity-memberships"})
	if err != nil {
		t.Fatal(err)
	}
	response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "set_memberships", Input: raw}, mutationEnvelope(grant, scopeVersion))
	if err != nil || response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("ordinary large membership = %+v, err = %v", response.Error, err)
	}
	if _, err := json.Marshal(response); err != nil {
		t.Fatalf("ordinary large membership must serialize: %v", err)
	}
	if workVersion(t, s, "work-1") != 3 {
		t.Fatal("ordinary membership did not change work exactly once")
	}
}

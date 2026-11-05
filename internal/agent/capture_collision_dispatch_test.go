package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestCaptureCollisionDispatchRollsBack(t *testing.T) {
	for _, tc := range []struct{ name, key, uuid string }{
		{"key", "ZZ-9001", "other-uuid"},
		{"uuid", "ZZ-9002", "same-uuid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, service, grant, scope := captureTaskFixture(t)
			first := `{"title":"Holder","kind":"task","project_ids":["project-1"],"human_key":"ZZ-9001","remote_issue_uuid":"same-uuid","url":"https://example.com/issue/ZZ-9001","idempotency_key":"holder"}`
			response := dispatchCapture(t, s, service, grant, scope, "holder-request", first)
			if response.Outcome != agentcontract.OutcomeOK {
				t.Fatalf("holder: %+v", response)
			}
			holderID := (*response.ChangedRefs)[0].ID
			before := captureStateSnapshot(t, s)
			input := fmt.Sprintf(`{"title":"Collision","kind":"task","project_ids":["project-1"],"human_key":%q,"remote_issue_uuid":%q,"url":"https://example.com/issue/ZZ-9002","idempotency_key":"collision"}`, tc.key, tc.uuid)
			request := InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: json.RawMessage(input)}
			env := mutationEnvelope(grant, scope)
			env.RequestID = "collision-request"
			refused, err := Dispatch(context.Background(), s, service, request, env)
			if err != nil || refused.Error == nil || refused.Outcome != agentcontract.OutcomeError {
				t.Fatalf("collision: %+v, %v", refused, err)
			}
			if refused.Error.EffectState != "none" || !strings.Contains(refused.Error.Message, holderID) {
				t.Fatalf("collision must have no effect and must identify the same-product holder: %+v", refused.Error)
			}
			after := captureStateSnapshot(t, s)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("collision changed persisted state: before=%v after=%v", before, after)
			}
		})
	}
}

func TestCaptureCollisionDisclosureUsesGrantScope(t *testing.T) {
	for _, tc := range []struct {
		name      string
		products  []string
		wantNamed bool
	}{
		{"hidden_holder", []string{"product-1"}, false},
		{"readable_holder_without_shared_project", []string{"product-1", "product-2"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, _ := captureTaskFixture(t)
			ctx := context.Background()
			events := []store.Event{
				{EventID: "scope-product-2", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Product 2","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
				{EventID: "scope-project-2", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"display_name":"Project 2"}`)},
				{EventID: "scope-membership-2", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-2","project_id":"project-2","role":"primary","reason":"test","expected_version":1,"resulting_version":2}`)},
			}
			if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-2"): 0, store.VersionRef(store.SubjectProject, "project-2"): 0}}); err != nil {
				t.Fatal(err)
			}
			holderService, _, holderGrant := newAuthorizedService(t, s, "holder-client", "holder-human", []agentcontract.Capability{"work_define"}, []string{"product-2"}, []string{"project-2"}, store.ProjectResolution{ProjectID: "project-2"})
			holderScope, _, err := s.ScopeVersion(ctx, "project-2")
			if err != nil {
				t.Fatal(err)
			}
			holderEnv := mutationEnvelope(holderGrant, holderScope)
			holderEnv.AmbientProjectID, holderEnv.SelectedProductID, holderEnv.RequestID = "project-2", "product-2", "scope-holder"
			holder, err := Dispatch(ctx, s, holderService, InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: json.RawMessage(`{"title":"Holder","kind":"task","project_ids":["project-2"],"human_key":"ZZ-9100","remote_issue_uuid":"scope-holder-uuid","url":"https://example.com/issue/ZZ-9100","idempotency_key":"scope-holder"}`)}, holderEnv)
			if err != nil {
				t.Fatal(err)
			}
			if holder.Outcome != agentcontract.OutcomeOK || holder.ChangedRefs == nil {
				t.Fatalf("holder error: %+v", holder.Error)
			}
			holderID := (*holder.ChangedRefs)[0].ID
			service, _, grant := newAuthorizedService(t, s, "scope-client", "scope-human", []agentcontract.Capability{"work_define"}, tc.products, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
			scope, _, err := s.ScopeVersion(ctx, "project-1")
			if err != nil {
				t.Fatal(err)
			}
			refused := dispatchCapture(t, s, service, grant, scope, "scope-collision", `{"title":"Collision","kind":"task","project_ids":["project-1"],"human_key":"ZZ-9100","remote_issue_uuid":"scope-other-uuid","url":"https://example.com/issue/ZZ-9100","idempotency_key":"scope-collision"}`)
			if refused.Error == nil || refused.Outcome != agentcontract.OutcomeError || refused.Error.Kind != "invalid_relation" || refused.Error.EffectState != "none" || strings.Contains(refused.Error.Message, holderID) != tc.wantNamed {
				t.Fatalf("disclosure wantNamed=%v holder=%s error=%+v", tc.wantNamed, holderID, refused.Error)
			}
		})
	}
}

func captureStateSnapshot(t *testing.T, s *store.Store) []string {
	t.Helper()
	queries := []string{
		`SELECT COALESCE((SELECT last FROM store_sequences WHERE name='effect'),0)`,
		`SELECT COALESCE(json_group_array(json_array(id,version,lifecycle,title)),'[]') FROM (SELECT * FROM work_items ORDER BY id)`,
		`SELECT COALESCE(json_group_array(json_array(work_id,remote_issue_uuid,human_key,url)),'[]') FROM (SELECT * FROM linear_issue_links ORDER BY work_id)`,
		`SELECT count(*) FROM effect_receipts`,
		`SELECT count(*) FROM idempotency_records`,
		`SELECT count(*) FROM workflow_instances`,
	}
	values := make([]string, len(queries))
	for i, q := range queries {
		if err := s.DatabaseForTesting().QueryRow(q).Scan(&values[i]); err != nil {
			t.Fatal(err)
		}
	}
	return values
}

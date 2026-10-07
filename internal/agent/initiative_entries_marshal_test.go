package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestInitiativeEntriesReadAcrossSiblingProjects(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		sibling   bool
		populated bool
	}{
		{"empty", false, false},
		{"sibling_empty", true, false},
		{"sibling_populated", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			s, service, grant, _ := mutationDispatchFixture(t, []Capability{"product_read", "work_initiative"})
			projects := []string{"project-1"}
			if tc.sibling {
				addAuthoritySibling(t, s)
				projects = append(projects, "project-2")
				service, _, grant = newAuthorizedService(t, s, "client-sibling", "human-1", []Capability{"product_read", "work_initiative"}, []string{"product-1"}, projects, store.ProjectResolution{ProjectID: "project-1"})
			}
			scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
			if err != nil {
				t.Fatal(err)
			}
			env := mutationEnvelope(grant, scopeVersion)
			createInput, err := json.Marshal(map[string]any{"title": "Initiative", "value_statement": "Coordinate work", "project_ids": projects, "idempotency_key": "entries-create"})
			if err != nil {
				t.Fatal(err)
			}
			created, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_initiative", Operation: "create", Input: createInput}, env)
			if err != nil || created.Outcome != OutcomeOK {
				t.Fatalf("create outcome=%s error=%+v err=%v", created.Outcome, created.Error, err)
			}
			id := (*created.ChangedRefs)[0].ID
			revised, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_initiative", Operation: "revise_narrative", Input: json.RawMessage(`{"initiative_work_id":"` + id + `","expected_version":2,"narrative":"Current coordination context","reason":"Test narrative read","idempotency_key":"entries-narrative"}`)}, env)
			if err != nil || revised.Outcome != OutcomeOK {
				t.Fatalf("revise outcome=%s error=%+v err=%v", revised.Outcome, revised.Error, err)
			}
			want := []store.InitiativeEntry{}
			if tc.populated {
				if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{
					{EventID: "entries-child", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"Second child","priority":1}`)},
					{EventID: "entries-child-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-2","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
				}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "work-2"): 0}}); err != nil {
					t.Fatal(err)
				}
				for i, entry := range []store.InitiativeEntry{
					{InitiativeWorkID: id, ChildWorkID: "work-1", Position: 10, Required: false},
					{InitiativeWorkID: id, ChildWorkID: "work-2", Position: 0, Required: true},
				} {
					input, err := json.Marshal(map[string]any{"initiative_work_id": id, "child_work_id": entry.ChildWorkID, "position": entry.Position, "required": entry.Required, "expected_version": 3 + i, "idempotency_key": "entries-add-" + entry.ChildWorkID})
					if err != nil {
						t.Fatal(err)
					}
					added, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_initiative", Operation: "add_entry", Input: input}, env)
					if err != nil || added.Outcome != OutcomeOK {
						t.Fatalf("add outcome=%s error=%+v err=%v", added.Outcome, added.Error, err)
					}
				}
				want = []store.InitiativeEntry{
					{InitiativeWorkID: id, ChildWorkID: "work-2", Position: 0, Required: true},
					{InitiativeWorkID: id, ChildWorkID: "work-1", Position: 10, Required: false},
				}
			}
			before := workVersion(t, s, id)
			response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_initiative", Operation: "entries", Input: json.RawMessage(`{"initiative_work_id":"` + id + `"}`)}, env)
			if err != nil || response.Outcome != OutcomeOK {
				t.Fatalf("entries outcome=%s error=%+v err=%v", response.Outcome, response.Error, err)
			}
			wire, err := json.Marshal(response)
			if err != nil {
				t.Fatalf("entries response does not marshal: %v", err)
			}
			var decoded Envelope
			if err := json.Unmarshal(wire, &decoded); err != nil {
				t.Fatalf("entries response does not decode: %v", err)
			}
			var result struct {
				Entries   []store.InitiativeEntry `json:"entries"`
				Narrative string                  `json:"narrative"`
			}
			if err := json.Unmarshal(decoded.Result, &result); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(result.Entries, want) || result.Narrative != "Current coordination context" {
				t.Fatalf("result=%+v, want entries=%+v and the recorded narrative", result, want)
			}
			if decoded.Authority != AuthorityAuthoritative || decoded.ResolvedScope == nil || decoded.ResolvedScope.ProductID != "product-1" || decoded.ChangedRefs != nil || decoded.NextValidIntents != nil {
				t.Fatalf("invalid read metadata: %+v", decoded)
			}
			if after := workVersion(t, s, id); after != before {
				t.Fatalf("entries read changed work version from %d to %d", before, after)
			}
		})
	}
}

func TestInitiativeProductInvariantRefusalsMarshal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"product_read", "work_initiative", "cross_scope"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	created, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_initiative", Operation: "create", Input: json.RawMessage(`{"title":"Initiative","value_statement":"Coordinate work","project_ids":["project-1"],"idempotency_key":"invariant-create"}`)}, env)
	if err != nil || created.Outcome != OutcomeOK {
		t.Fatalf("create outcome=%s error=%+v err=%v", created.Outcome, created.Error, err)
	}
	id := (*created.ChangedRefs)[0].ID
	// A corrupt projection must produce a deliverable refusal, not a successful
	// read. This fixture bypasses folding only to exercise that failure boundary.
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1);
		INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('product-2','Other','prototype','operator_only',1,'now','now');
		INSERT INTO product_projects(product_id,project_id,role) VALUES('product-2','project-1','secondary');
		DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	env.ScopeVersion, _, err = s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []InvokeRequest{
		{Tool: "concord_work_initiative", Operation: "entries", Input: json.RawMessage(`{"initiative_work_id":"` + id + `"}`)},
		{Tool: "concord_work_initiative", Operation: "create", Input: json.RawMessage(`{"title":"Ambiguous","value_statement":"Must refuse","project_ids":["project-1"],"idempotency_key":"invariant-ambiguous"}`)},
	} {
		t.Run(request.Operation, func(t *testing.T) {
			response, err := Dispatch(ctx, s, service, request, env)
			if err != nil || response.Error == nil || response.Error.Kind != "invariant_violation" || response.Error.RecoveryAction.Kind != "reread_entities" {
				t.Fatalf("refusal=%+v err=%v", response.Error, err)
			}
			if _, err := json.Marshal(response); err != nil {
				t.Fatalf("refusal does not marshal: %v", err)
			}
		})
	}
}

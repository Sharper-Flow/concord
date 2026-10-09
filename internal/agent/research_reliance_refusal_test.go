package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestResearchRelianceRefusalHasNoEffects(t *testing.T) {
	for _, test := range []struct {
		name string
		kind string
	}{
		{"missing_revision", "unknown_scope"},
		{"omitted_stale_pin", "stale_requires_review"},
		{"conflicting_requiredness", "invalid_input"},
		{"conflicting_role", "invalid_input"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			s, service, grant, _ := researchSurfaceFixture(t)
			pack, err := s.CreateResearchPack(ctx, store.CreateResearchPackRequest{
				Identity:    store.ResearchMutationIdentity{PrincipalRef: "operator", Tool: "research-test", OperationKind: "create", IdempotencyKey: "create"},
				OwnerWorkID: "work-1", Revision: store.ResearchRevisionInput{Question: "Which revision?", Method: "source inspection", ScopeIn: json.RawMessage(`{}`), ScopeOut: json.RawMessage(`{}`), DoneWhen: json.RawMessage(`{}`)},
			})
			if err != nil {
				t.Fatal(err)
			}
			scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
			if err != nil {
				t.Fatal(err)
			}
			invoke := func(input any) Envelope {
				t.Helper()
				raw, err := json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				out, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, mutationEnvelope(grant, scopeVersion))
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			binding := map[string]any{"pack_id": pack.PackID, "revision": 1, "use_role": "context", "required": true}
			input := map[string]any{"work_id": "work-1", "expected_version": 4, "action_id": "record_reproduction", "idempotency_key": "refused", "fields": map[string]any{}, "research_bindings": []any{binding}}
			if test.name == "missing_revision" {
				binding["revision"] = 999
			} else {
				accepted := invoke(map[string]any{"work_id": "work-1", "expected_version": 4, "action_id": "record_reproduction", "idempotency_key": "pin", "fields": map[string]any{}, "research_bindings": []any{binding}})
				if accepted.Outcome != OutcomeOK {
					t.Fatalf("pin: %+v", accepted.Error)
				}
				input["expected_version"] = 5
				input["action_id"] = "record_alignment"
				input["fields"] = map[string]any{"searched": "The bounded backlog.", "outcome": "none_found"}
				switch test.name {
				case "omitted_stale_pin":
					if err := s.SetResearchFreshness(ctx, store.SetResearchFreshnessRequest{Identity: store.ResearchMutationIdentity{PrincipalRef: "operator", Tool: "research-test", OperationKind: "stale", IdempotencyKey: "stale"}, PackID: pack.PackID, ExpectedVersion: 2, Freshness: store.ResearchStale}); err != nil {
						t.Fatal(err)
					}
					delete(input, "research_bindings")
				case "conflicting_requiredness":
					binding["required"] = false
				case "conflicting_role":
					binding["use_role"] = "design_input"
				}
			}
			var beforeEvents, beforePins, beforeVersion int
			for query, target := range map[string]*int{"SELECT count(*) FROM domain_events": &beforeEvents, "SELECT count(*) FROM active_research_consumers": &beforePins, "SELECT expected_version FROM active_research_packs": &beforeVersion} {
				if err := s.DatabaseForTesting().QueryRow(query).Scan(target); err != nil {
					t.Fatal(err)
				}
			}
			out := invoke(input)
			if out.Error == nil || out.Error.Kind != test.kind || out.Error.EffectState != EffectNone {
				t.Fatalf("refusal=%+v, want %s/effect none", out.Error, test.kind)
			}
			for query, expected := range map[string]int{"SELECT count(*) FROM domain_events": beforeEvents, "SELECT count(*) FROM active_research_consumers": beforePins, "SELECT expected_version FROM active_research_packs": beforeVersion} {
				var got int
				if err := s.DatabaseForTesting().QueryRow(query).Scan(&got); err != nil {
					t.Fatal(err)
				}
				if got != expected {
					t.Fatalf("%s=%d, want %d", query, got, expected)
				}
			}
		})
	}
}

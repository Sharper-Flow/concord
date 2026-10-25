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
		{"required_stale_declaration", "stale_requires_review"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			s, service, grant, _ := researchSurfaceFixture(t)
			pack, err := createResearchPack(ctx, s, store.CreateResearchPackRequest{
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
			diagnosisFields := map[string]any{
				"reproduction": "The bounded defect reproduction statement.",
				"searched":     "The bounded backlog.",
				"outcome":      "none_found",
				"root_cause":   "The bounded root cause statement.",
			}
			input := map[string]any{"work_id": "work-1", "expected_version": 4, "action_id": "record_diagnosis", "idempotency_key": "refused", "fields": diagnosisFields, "research_bindings": []any{binding}}
			if test.name == "missing_revision" {
				binding["revision"] = 999
			} else {
				accepted := invoke(map[string]any{"work_id": "work-1", "expected_version": 4, "action_id": "record_diagnosis", "idempotency_key": "pin", "fields": diagnosisFields, "research_bindings": []any{binding}})
				if accepted.Outcome != OutcomeOK {
					t.Fatalf("pin: %+v", accepted.Error)
				}
				input["expected_version"] = 6
				input["action_id"] = "record_work_context"
				input["fields"] = map[string]any{"required_reading": []any{}, "finding_refs": []any{}, "context_findings": []any{}}
				switch test.name {
				case "omitted_stale_pin", "required_stale_declaration":
					if err := setResearchFreshness(ctx, s, store.SetResearchFreshnessRequest{PackID: pack.PackID, ExpectedVersion: 2, Freshness: store.ResearchStale}); err != nil {
						t.Fatal(err)
					}
					if test.name == "omitted_stale_pin" {
						delete(input, "research_bindings")
					}
				}
			}
			var beforeEvents, beforePins, beforeVersion int
			for query, target := range map[string]*int{"SELECT last FROM store_sequences WHERE name='effect'": &beforeEvents, "SELECT count(*) FROM active_research_consumers": &beforePins, "SELECT expected_version FROM active_research_packs": &beforeVersion} {
				if err := s.DatabaseForTesting().QueryRow(query).Scan(target); err != nil {
					t.Fatal(err)
				}
			}
			out := invoke(input)
			if out.Error == nil || out.Error.Kind != test.kind || out.Error.EffectState != EffectNone {
				t.Fatalf("refusal=%+v, want %s/effect none", out.Error, test.kind)
			}
			for query, expected := range map[string]int{"SELECT last FROM store_sequences WHERE name='effect'": beforeEvents, "SELECT count(*) FROM active_research_consumers": beforePins, "SELECT expected_version FROM active_research_packs": beforeVersion} {
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

func TestResearchRelianceDeclarationReplacesPin(t *testing.T) {
	for _, name := range []string{"rebind_after_stale", "remove_requirement_on_stale", "role_change"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s, service, grant, _ := researchSurfaceFixture(t)
			revision := store.ResearchRevisionInput{Question: "Which revision?", Method: "source inspection", ScopeIn: json.RawMessage(`{}`), ScopeOut: json.RawMessage(`{}`), DoneWhen: json.RawMessage(`{}`)}
			pack, err := createResearchPack(ctx, s, store.CreateResearchPackRequest{OwnerWorkID: "work-1", Revision: revision})
			if err != nil {
				t.Fatal(err)
			}
			scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
			if err != nil {
				t.Fatal(err)
			}
			invoke := func(version int, action, key string, fields map[string]any, binding map[string]any) Envelope {
				t.Helper()
				input := map[string]any{"work_id": "work-1", "expected_version": version, "action_id": action, "idempotency_key": key, "fields": fields}
				if binding != nil {
					input["research_bindings"] = []any{binding}
				}
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
			if out := invoke(4, "record_diagnosis", "pin", map[string]any{
				"reproduction": "The bounded defect reproduction statement.",
				"searched":     "The bounded backlog.",
				"outcome":      "none_found",
				"root_cause":   "The bounded root cause statement.",
			}, binding); out.Outcome != OutcomeOK {
				t.Fatalf("pin: %+v", out.Error)
			}
			packVersion := int64(2)
			wantRevision, wantRole, wantRequired := int64(1), "context", true
			if name == "rebind_after_stale" {
				if err := appendResearchRevision(ctx, s, store.AppendResearchRevisionRequest{PackID: pack.PackID, ExpectedVersion: packVersion, Revision: revision}); err != nil {
					t.Fatal(err)
				}
				packVersion++
				binding["revision"] = 2
				wantRevision = 2
			}
			if name != "role_change" {
				if err := setResearchFreshness(ctx, s, store.SetResearchFreshnessRequest{PackID: pack.PackID, Revision: 1, ExpectedVersion: packVersion, Freshness: store.ResearchStale}); err != nil {
					t.Fatal(err)
				}
				packVersion++
			}
			if name == "remove_requirement_on_stale" {
				binding["required"], wantRequired = false, false
			}
			if name == "role_change" {
				binding["use_role"], wantRole = "design_input", "design_input"
			}
			if out := invoke(6, "record_work_context", "replace", map[string]any{"required_reading": []any{}, "finding_refs": []any{}, "context_findings": []any{}}, binding); out.Outcome != OutcomeOK || out.Error != nil {
				t.Fatalf("replacement: %+v", out.Error)
			}
			var pins int
			if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM active_research_consumers WHERE pack_id=? AND consumer_work_id='work-1'`, pack.PackID).Scan(&pins); err != nil || pins != 1 {
				t.Fatalf("pins=%d err=%v", pins, err)
			}
			var gotRevision int64
			var gotRole string
			var gotRequired bool
			if err := s.DatabaseForTesting().QueryRow(`SELECT revision,use_role,required FROM active_research_consumers WHERE pack_id=? AND consumer_work_id='work-1'`, pack.PackID).Scan(&gotRevision, &gotRole, &gotRequired); err != nil {
				t.Fatal(err)
			}
			if gotRevision != wantRevision || gotRole != wantRole || gotRequired != wantRequired {
				t.Fatalf("pin=%d/%s/%t, want %d/%s/%t", gotRevision, gotRole, gotRequired, wantRevision, wantRole, wantRequired)
			}
			var gotVersion int64
			if err := s.DatabaseForTesting().QueryRow(`SELECT expected_version FROM active_research_packs WHERE pack_id=?`, pack.PackID).Scan(&gotVersion); err != nil || gotVersion != packVersion+1 {
				t.Fatalf("version=%d, want %d err=%v", gotVersion, packVersion+1, err)
			}
			if out := invoke(7, "record_work_context", "omitted", map[string]any{"required_reading": []any{}, "finding_refs": []any{}, "context_findings": []any{}}, nil); out.Outcome != OutcomeOK {
				t.Fatalf("following action: %+v", out.Error)
			}
		})
	}
}

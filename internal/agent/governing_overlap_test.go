package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

func governingOverlapInput(input []byte, kind string) []byte {
	return []byte(strings.Replace(string(input), `"resolution_kind":"depends_on"`, `"resolution_kind":"`+kind+`"`, 1))
}

func TestGoverningCaptureStoresActualDeclaredSet(t *testing.T) {
	for _, declaration := range []string{"omitted", "[]", `["audit_required"]`} {
		t.Run(declaration, func(t *testing.T) {
			s, service, grant, _, _ := agentJobsMutationPM1Fixture(t)
			input := map[string]any{"title": "Capture genuine scope", "value_statement": "Preserve the declaration at capture", "kind": "task", "project_ids": []string{"proj-web"}, "idempotency_key": "declared-capture"}
			want := "[]"
			if declaration != "omitted" {
				var refs []string
				if err := json.Unmarshal([]byte(declaration), &refs); err != nil {
					t.Fatal(err)
				}
				input["governing_requirements"] = refs
				want = declaration
			}
			if declaration == `["audit_required"]` {
				for _, requirement := range []string{"audit_required", "review_required"} {
					if err := pm1fixture.SeedGoverningRequirement(context.Background(), s, "proj-web", requirement, "Synthetic capture obligation"); err != nil {
						t.Fatal(err)
					}
				}
			}
			env := agentJobsMutationEnvelope(t, s, grant, "proj-web")
			raw, _ := json.Marshal(input)
			request := InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: raw}
			response := dispatchMutation(t, s, service, request, env)
			if declaration == `["audit_required"]` {
				if response.Error == nil || len(response.Error.Violations) != 1 || response.Error.Violations[0] != "review_required" {
					t.Fatalf("partial cut=%+v", response.Error)
				}
				if err := service.ExpandTrustedClientPolicy(context.Background(), grant.ClientRef, TrustedClientPolicy{Capabilities: []agentcontract.Capability{"cross_scope"}}); err != nil {
					t.Fatal(err)
				}
				response = dispatchMutation(t, s, service, request, env)
			}
			if response.Outcome != agentcontract.OutcomeOK {
				t.Fatalf("capture=%+v", response.Error)
			}
			var got string
			if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(intent_json,'$.governing_requirements') FROM work_items WHERE id=?`, (*response.ChangedRefs)[0].ID).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("stored declared=%s want=%s", got, want)
			}
		})
	}
}

func setAgentDeclaredRequirements(t *testing.T, s *store.Store, workID string, requirements []string) {
	t.Helper()
	raw, err := json.Marshal(requirements)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := s.DatabaseForTesting().Exec(`DELETE FROM write_guard`); err != nil {
			t.Errorf("leave requirement fixture write guard: %v", err)
		}
	}()
	if _, err := s.DatabaseForTesting().Exec(`UPDATE work_items SET intent_json=json_set(intent_json,'$.governing_requirements',json(?)) WHERE id=?`, string(raw), workID); err != nil {
		t.Fatal(err)
	}
}

func TestGoverningOverlapScopeCutRequiresCrossScope(t *testing.T) {
	for _, kind := range []string{store.ResolutionMergedInto, store.ResolutionSupersedes} {
		t.Run(kind, func(t *testing.T) {
			s, service, _, _, env, input := seedAgentOverlapFixture(t)
			terminated, survivor := "work-1", "work-2"
			if kind == store.ResolutionSupersedes {
				terminated, survivor = survivor, terminated
			}
			setAgentDeclaredRequirements(t, s, terminated, []string{"audit_required"})
			setAgentDeclaredRequirements(t, s, survivor, []string{})
			input = governingOverlapInput(input, kind)
			response := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: input}, env)
			if response.Error == nil || response.Error.Kind != "unauthorized" || len(response.Error.Violations) != 1 || response.Error.Violations[0] != "audit_required" {
				t.Fatalf("requirement-discarding %s outcome=%s error=%+v, want unauthorized", kind, response.Outcome, response.Error)
			}
			if workVersion(t, s, "work-1") != 2 || workVersion(t, s, "work-2") != 2 || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_overlap_resolutions`) != 0 {
				t.Fatal("refusal changed overlap state")
			}
		})
	}
}

func TestGoverningOverlapRetainedAndUnknownScope(t *testing.T) {
	for _, kind := range []string{store.ResolutionMergedInto, store.ResolutionSupersedes} {
		for _, unknown := range []string{"", "work-1", "work-2"} {
			t.Run(kind+"/unknown="+unknown, func(t *testing.T) {
				s, service, _, _, env, input := seedAgentOverlapFixture(t)
				terminated, survivor := "work-1", "work-2"
				if kind == store.ResolutionSupersedes {
					terminated, survivor = survivor, terminated
				}
				setAgentDeclaredRequirements(t, s, terminated, []string{"audit_required"})
				setAgentDeclaredRequirements(t, s, survivor, []string{"audit_required", "retained_extra"})
				if unknown != "" {
					if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE work_items SET intent_json=json_remove(intent_json,'$.governing_requirements') WHERE id='` + unknown + `'; DELETE FROM write_guard`); err != nil {
						t.Fatal(err)
					}
				}
				before := agentCurrentEffects(t, s)
				response := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: governingOverlapInput(input, kind)}, env)
				if unknown == "" {
					if response.Outcome != agentcontract.OutcomeOK {
						t.Fatalf("retained scope=%+v", response.Error)
					}
				} else if response.Error == nil || response.Error.Kind != "missing_evidence" || agentCurrentEffects(t, s) != before {
					t.Fatalf("unknown historical scope=%+v", response.Error)
				}
			})
		}
	}
}

func TestGoverningOverlapCrossScopePreservesDeclaredSets(t *testing.T) {
	for _, kind := range []string{store.ResolutionMergedInto, store.ResolutionSupersedes} {
		t.Run(kind, func(t *testing.T) {
			s, service, grant, _, env, input := seedAgentOverlapFixture(t)
			terminated, survivor := "work-1", "work-2"
			if kind == store.ResolutionSupersedes {
				terminated, survivor = survivor, terminated
			}
			setAgentDeclaredRequirements(t, s, terminated, []string{"audit_required"})
			setAgentDeclaredRequirements(t, s, survivor, []string{})
			input = governingOverlapInput(input, kind)
			if err := service.ExpandTrustedClientPolicy(context.Background(), grant.ClientRef, TrustedClientPolicy{Capabilities: []agentcontract.Capability{"cross_scope"}}); err != nil {
				t.Fatal(err)
			}
			response := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: input}, env)
			if response.Outcome != agentcontract.OutcomeOK {
				t.Fatalf("exact scope cut=%+v", response.Error)
			}
			if workVersion(t, s, "work-1") != 3 || workVersion(t, s, "work-2") != 3 {
				t.Fatal("exact cut did not settle once")
			}
			for workID, want := range map[string]string{terminated: `["audit_required"]`, survivor: `[]`} {
				var got string
				if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(intent_json,'$.governing_requirements') FROM work_items WHERE id=?`, workID).Scan(&got); err != nil {
					t.Fatal(err)
				}
				if got != want {
					t.Fatalf("rewrote %s requirements to %s", workID, got)
				}
			}
		})
	}
}

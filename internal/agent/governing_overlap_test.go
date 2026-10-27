package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

func governingOverlapInput(input []byte, kind string) []byte {
	return []byte(strings.Replace(string(input), `"resolution_kind":"compatible_with"`, `"resolution_kind":"`+kind+`"`, 1))
}

func TestGoverningCaptureStoresActualDeclaredSet(t *testing.T) {
	for _, declaration := range []string{"omitted", "[]", `["audit_required"]`} {
		t.Run(declaration, func(t *testing.T) {
			s, service, grant, key, _ := agentJobsMutationPM1Fixture(t)
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
				ref := response.Error.Details["approval_ref"].(string)
				scope, versions := challengeApprovalBindings(t, response.Error.ConsequenceSummary)
				raw, _ = injectApproval(raw, ref)
				digest := mutationDigest("concord_work_define", "capture", env, raw)
				env.HostApproval = signedHostApproval(key, ref, digest, scope, versions, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), nonceForChallenge(ref))
				request.Input = raw
				response = dispatchMutation(t, s, service, request, env)
			}
			if response.Outcome != OutcomeOK {
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

func TestGoverningOverlapScopeCutRequiresConsent(t *testing.T) {
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
			if response.Error == nil || response.Error.Kind != "invariant_violation" || len(response.Error.Violations) != 1 || response.Error.Violations[0] != "audit_required" {
				t.Fatalf("requirement-discarding %s outcome=%s error=%+v, want governing scope-cut challenge", kind, response.Outcome, response.Error)
			}
			if ref, ok := response.Error.Details["approval_ref"].(string); !ok || ref == "" {
				t.Fatal("missing exact approval challenge")
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
					if response.Outcome != OutcomeOK {
						t.Fatalf("retained scope=%+v", response.Error)
					}
				} else if response.Error == nil || response.Error.Kind != "missing_evidence" || agentCurrentEffects(t, s) != before {
					t.Fatalf("unknown historical scope=%+v", response.Error)
				}
				if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`) != 0 || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approvals`) != 0 {
					t.Fatal("retained or unknown scope minted consent")
				}
			})
		}
	}
}

func TestGoverningOverlapExactApprovalAndChangedBindings(t *testing.T) {
	for _, kind := range []string{store.ResolutionMergedInto, store.ResolutionSupersedes} {
		for _, change := range []string{"", "from", "to", "from_version", "to_version", "terminated_set", "retained_set", "cut_removed"} {
			t.Run(kind+"/change="+change, func(t *testing.T) {
				s, service, _, key, env, input := seedAgentOverlapFixture(t)
				terminated, survivor := "work-1", "work-2"
				if kind == store.ResolutionSupersedes {
					terminated, survivor = survivor, terminated
				}
				setAgentDeclaredRequirements(t, s, terminated, []string{"audit_required"})
				setAgentDeclaredRequirements(t, s, survivor, []string{})
				input = governingOverlapInput(input, kind)
				challenge := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: input}, env)
				if challenge.Error == nil || challenge.Error.Kind != "invariant_violation" {
					t.Fatalf("challenge=%+v", challenge.Error)
				}
				ref := challenge.Error.Details["approval_ref"].(string)
				scope, versions := challengeApprovalBindings(t, challenge.Error.ConsequenceSummary)
				approvedInput, err := injectApproval(input, ref)
				if err != nil {
					t.Fatal(err)
				}
				digest := mutationDigest("concord_work_relate", "resolve_overlap", env, approvedInput)
				var changed map[string]any
				if err := json.Unmarshal(approvedInput, &changed); err != nil {
					t.Fatal(err)
				}
				switch change {
				case "from":
					changed["from_work_id"] = "work-2"
				case "to":
					changed["to_work_id"] = "work-1"
				case "from_version":
					changed["from_expected_version"] = 3
				case "to_version":
					changed["to_expected_version"] = 3
				case "terminated_set":
					setAgentDeclaredRequirements(t, s, terminated, []string{"audit_required", "other_required"})
				case "retained_set":
					setAgentDeclaredRequirements(t, s, survivor, []string{"other_retained"})
				case "cut_removed":
					setAgentDeclaredRequirements(t, s, survivor, []string{"audit_required"})
				}
				approvedInput, err = json.Marshal(changed)
				if err != nil {
					t.Fatal(err)
				}
				before := agentCurrentEffects(t, s)
				response := routineOverlapApprovedDispatch(t, s, service, key, env, ref, digest, scope, versions, approvedInput)
				if change != "" {
					if response.Error == nil || agentCurrentEffects(t, s) != before || workVersion(t, s, "work-1") != 2 || workVersion(t, s, "work-2") != 2 {
						t.Fatalf("altered binding outcome=%s error=%+v", response.Outcome, response.Error)
					}
					if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approvals`) != 0 || countRows(t, s.DatabaseForTesting(), `SELECT used_count FROM agent_approval_challenges`) != 0 {
						t.Fatal("refusal spent approval")
					}
					return
				}
				if response.Outcome != OutcomeOK {
					t.Fatalf("exact scope cut=%+v", response.Error)
				}
				if workVersion(t, s, "work-1") != 3 || workVersion(t, s, "work-2") != 3 || countRows(t, s.DatabaseForTesting(), `SELECT used_count FROM agent_approvals`) != 1 {
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
				// Reuse the genuinely consumed receipt through the SQL-only entry.
				// A changed requirement set must refuse before any endpoint write.
				request := store.WorkflowDomainOverlapResolutionRequest{EventID: "changed-core-scope", FromWorkID: "work-1", ToWorkID: "work-2", FromExpectedVersion: 2, ToExpectedVersion: 2, FromContractVersion: 1, ToContractVersion: 1, ResolutionKind: kind, Reason: "operator confirms compatible work", Actor: "human-1"}
				if err := s.DatabaseForTesting().QueryRow(`SELECT approval_ref,operation_digest,scope_json,version_json,consequence FROM agent_approvals`).Scan(&request.ApprovalRef, &request.ApprovalOperationDigest, &request.ApprovalScopeJSON, &request.ApprovalVersionsJSON, &request.ApprovalConsequence); err != nil {
					t.Fatal(err)
				}
				setAgentDeclaredRequirements(t, s, terminated, []string{"audit_required", "changed_required"})
				before = agentCurrentEffects(t, s)
				err = s.Transact(context.Background(), func(tx *store.Transaction) error {
					_, err := store.ResolveWorkflowDomainOverlapTx(context.Background(), tx, request)
					return err
				})
				var failure *store.Failure
				if !errors.As(err, &failure) || failure.Kind != store.KindUnauthorized || !strings.Contains(failure.Detail, "governing_requirements") || agentCurrentEffects(t, s) != before {
					t.Fatalf("SQL-only changed set=%v", err)
				}
			})
		}
	}
}

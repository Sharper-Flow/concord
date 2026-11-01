package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

func TestInvokeContractRequestCorrection(t *testing.T) {
	for _, name := range []string{"missing_modified_law_home_domain", "omitted_spec_mandate"} {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_define", "work_transition"})
			// The committed registry declares the child law-home Domain, and
			// the committed decision CD-0002 homes there, so the modified
			// law's canonical home exists in current law while starting
			// outside the request's affected set (law-folder-authority.md,
			// Identity and change).
			registry := store.KnowledgeDomainRegistry{
				SchemaVersion: "1.0",
				ProductKey:    "fixture-product-1",
				RootDomainID:  pm1fixture.FixtureRootDomainID,
				Domains: []store.KnowledgeDomain{
					{DomainID: pm1fixture.FixtureRootDomainID, Name: "Fixture root", Purpose: "Product law fixture", Status: "current", ArchitectureRelations: []store.KnowledgeArchitectureRelation{}},
					{DomainID: "law-home", Name: "Law home", Purpose: "Modified law ownership", ParentDomainID: pm1fixture.FixtureRootDomainID, Status: "current", ArchitectureRelations: []store.KnowledgeArchitectureRelation{}},
				},
			}
			home, err := pm1fixture.SeedProductDomainRegistry(ctx, s, "product-1", "project-1", t.TempDir(), registry)
			if err != nil {
				t.Fatal(err)
			}
			commitAgentFixtureLaw(t, home.RepoPath, ".concord/law/decisions/CD-0002-fixture-derived-law.md",
				agentFixtureLawFrontmatter("CD-0002", "law-home", ""))
			env := mutationEnvelope(grant, scopeVersionForProject(t, s, "project-1"))
			workID := captureCompositionWork(t, ctx, s, service, env, "Correct a bounded contract request", "task", "workflow.implementation", "correction-capture")
			version := advanceCapturedWorkToContract(t, s, service, env, workID, "correction")
			fields := workflowContractFieldsForSeededRegistry(t, s)
			binding := fields["architecture_binding"].(map[string]any)
			root := binding["home_domain_id"].(string)
			fields["law_change_reason"] = "Correct the bounded engineering law without changing an operator decision."

			var field, diagnostic string
			var correct func()
			if name == "missing_modified_law_home_domain" {
				// Keep the source-backed law and its owning-start baseline, but
				// place its canonical home outside this request's affected set.
				fields["spec_mandate"] = []string{"CD-0002"}
				fields["law_modifies"] = []string{"CD-0002"}
				field = "affected_domain_ids"
				diagnostic = "modified law home Domain is outside architecture_binding.affected_domain_ids"
				correct = func() { binding["affected_domain_ids"] = []string{root, "law-home"} }
			} else {
				binding["law_additions"] = []map[string]any{{"law_id": "law:request-correction", "home_domain_id": root}}
				delete(fields, "spec_mandate")
				field = "spec_mandate"
				diagnostic = "law addition is outside spec_mandate"
				correct = func() { fields["spec_mandate"] = []string{"law:request-correction"} }
			}

			// The key, expected version, caller, grant, and envelope remain
			// identical across the refused and corrected calls. No consent is added.
			env.RequestID = "request:contract-correction"
			input := map[string]any{"work_id": workID, "expected_version": version, "action_id": "approve_contract", "fields": fields, "idempotency_key": "contract-correction"}
			invoke := func() agentcontract.Envelope {
				t.Helper()
				raw, err := json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				response, err := Invoke(ctx, s, service, mustMarshalInvoke(t, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env))
				if err != nil {
					t.Fatal(err)
				}
				return response
			}
			before := contractRequestCorrectionState(t, s, workID)
			refused := invoke()
			if after := contractRequestCorrectionState(t, s, workID); !reflect.DeepEqual(after, before) {
				t.Fatalf("pre-effect refusal changed durable state: before=%v after=%v", before, after)
			}
			if refused.Outcome != agentcontract.OutcomeError || refused.Error == nil {
				t.Fatalf("malformed contract was not refused: %+v", refused)
			}
			if refused.Error.EffectState != agentcontract.EffectNone || refused.Error.RetrySafe {
				t.Errorf("refusal must have effect none and not permit an unchanged retry: %+v", refused.Error)
			}
			if refused.Error.Kind != "invalid_input" || refused.Error.RecoveryAction.Kind != "correct_request" {
				t.Errorf("request-only defect must be invalid_input/correct_request: %+v", refused.Error)
			}
			if !strings.Contains(refused.Error.Message, diagnostic) {
				t.Errorf("refusal did not reach the intended guard %q: %+v", diagnostic, refused.Error)
			}
			if !strings.Contains(refused.Error.Message, field) {
				t.Errorf("refusal does not name the field to correct %q: %+v", field, refused.Error)
			}
			if refused.RequestID != env.RequestID || refused.Tool != "concord_work_transition" || refused.Operation != "workflow_action" {
				t.Errorf("refusal lost request identity: %+v", refused)
			}
			if _, err := refused.Encode(); err != nil {
				t.Errorf("refusal cannot serialize: %v", err)
			}

			correct()
			corrected := invoke()
			if corrected.Outcome != agentcontract.OutcomeOK {
				t.Fatalf("corrected identical-key request under unchanged authority was not admitted: %+v", corrected.Error)
			}
			if workVersion(t, s, workID) <= version || usedCount(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_contracts WHERE work_id=?`, workID) != 1 {
				t.Fatal("correction did not commit exactly one contract and advance the work")
			}
			var actor, mandate, modifies, domains string
			if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT actor FROM effect_receipts WHERE subject_id=? AND kind=?`, workID, store.WorkflowContractApproved).Scan(&actor); err != nil {
				t.Fatal(err)
			}
			if actor != store.DeriveWorkflowActorRef(grant.PrincipalRef, grant.ClientRef, grant.AgentRef, grant.SessionRef) {
				t.Fatalf("corrected contract changed author authority: %s", actor)
			}
			if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT spec_mandate,law_modifies,decision_domains FROM workflow_contracts WHERE work_id=?`, workID).Scan(&mandate, &modifies, &domains); err != nil {
				t.Fatal(err)
			}
			for key, raw := range map[string]string{"spec_mandate": mandate, "law_modifies": modifies, "decision_domains": domains} {
				var got []string
				if err := json.Unmarshal([]byte(raw), &got); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, fields[key]) {
					t.Errorf("correction changed %s: got %v want %v", key, got, fields[key])
				}
			}
			if name == "missing_modified_law_home_domain" {
				if usedCount(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_contract_affected_domains WHERE work_id=? AND domain_id='law-home'`, workID) != 1 {
					t.Fatal("corrected contract did not bind the modified law home")
				}
			} else if usedCount(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_contract_law_additions WHERE work_id=? AND law_id='law:request-correction'`, workID) != 1 {
				t.Fatal("corrected contract did not bind the mandated law addition")
			}
			t.Log("corrected identical-key request admitted with unchanged authority")
		})
	}
}

func contractRequestCorrectionState(t *testing.T, s *store.Store, workID string) map[string]any {
	t.Helper()
	state := map[string]any{"work_version": workVersion(t, s, workID)}
	var step string
	if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	state["step"] = step
	var sequence int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&sequence); err != nil {
		t.Fatal(err)
	}
	state["effect_sequence"] = sequence
	for _, table := range []string{
		"effect_receipts", "idempotency_records",
		"workflow_contracts", "workflow_contract_predicates", "workflow_architecture_bindings",
		"workflow_contract_affected_domains", "workflow_contract_domain_modifications",
		"workflow_contract_domain_relation_modifications", "workflow_contract_law_modifications",
		"workflow_contract_law_revisions", "workflow_contract_law_additions",
		"workflow_law_addition_reservations", "workflow_contract_verification_obligations",
	} {
		state[table] = countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM `+table)
	}
	return state
}

func TestWorkflowRequestRecoveryClassification(t *testing.T) {
	for _, test := range []struct {
		name           string
		failure        store.Failure
		kind, recovery string
	}{
		{"payload", store.Failure{Kind: store.KindInvalidPayload}, "invalid_input", "correct_request"},
		{"filter", store.Failure{Kind: store.KindInvalidFilter}, "invalid_input", "correct_request"},
		{"input", store.Failure{Kind: store.KindInvalidInput}, "invalid_input", "correct_request"},
		{"state", store.Failure{Kind: store.KindInvalidOperation}, "invalid_input", "reread_entities"},
		{"retained knowledge", store.Failure{Kind: store.KindKnowledgeMissing}, "invalid_input", "reread_entities"},
		{"corrupt state", store.Failure{Kind: store.KindInvariantViolation}, "invariant_violation", "reread_entities"},
		{"scope", store.Failure{Kind: store.KindUnknownScope}, "unknown_scope", "contact_operator"},
		{"authorization", store.Failure{Kind: store.KindUnauthorized}, "unauthorized", "contact_operator"},
		{"stale context", store.Failure{Kind: store.KindStaleContext}, "stale_context", "refresh_context"},
		{"retryable input", store.Failure{Kind: store.KindInvalidPayload, RetrySafe: true}, "invalid_input", "reread_entities"},
		{"possible effect", store.Failure{Kind: store.KindInvalidPayload, EffectPossible: true}, "invalid_input", "reconcile_operation"},
		{"committed effect", store.Failure{Kind: store.KindInvalidPayload, CommittedRefs: []store.SubjectCurrentVersion{{SubjectType: "work", SubjectID: "work-1", Version: 2}}}, "invalid_input", "reconcile_operation"},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, proposed := range []string{"", "fix fields.premise", "correct_request"} {
				failure := test.failure
				failure.RecoveryAction = proposed
				out := failureEnvelope(agentcontract.NewBase("request-classification", "concord_work_transition", "workflow_action"), &failure)
				if out.Error.Kind != test.kind || out.Error.RecoveryAction.Kind != test.recovery {
					t.Errorf("proposal %q: got %+v, want %s/%s", proposed, out.Error, test.kind, test.recovery)
				}
				if _, err := out.Encode(); err != nil {
					t.Fatalf("proposal %q cannot serialize: %v", proposed, err)
				}
			}
		})
	}
}

func TestLiteralInputRecoveryKeepsStateAndAuthority(t *testing.T) {
	base := agentcontract.NewBase("literal-recovery", "concord_work_transition", "workflow_action")
	for _, proposed := range []string{"reread_entities", "contact_operator"} {
		out := coreError(base, "invalid_input", "state or authority is unavailable", proposed, false)
		if out.Error.RecoveryAction.Kind != proposed {
			t.Fatalf("explicit state recovery %q became %+v", proposed, out.Error)
		}
	}
	for _, recovery := range []string{coreError(base, "invalid_input", "fields are malformed", "", false).Error.RecoveryAction.Kind, newRuntimeFailure("invalid_input", "fields are malformed", "", false).recovery} {
		if recovery != "correct_request" {
			t.Fatalf("request default = %q", recovery)
		}
	}
}

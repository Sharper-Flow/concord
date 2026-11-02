package agent

import (
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

func contractRevisionInput(t *testing.T, s *store.Store, workID string, version int64, contract map[string]any, key string) json.RawMessage {
	t.Helper()
	var raw string
	if err := s.DatabaseForTesting().QueryRow(`SELECT intent_json FROM work_items WHERE id=?`, workID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var intent map[string]any
	if err := json.Unmarshal([]byte(raw), &intent); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"work_id": workID, "expected_version": version, "contract": contract, "reason": "Update the recorded plan", "idempotency_key": key}
	for _, field := range []string{"title", "value_statement", "kind", "priority", "urgency", "tags", "component_id", "workflow_type_ref", "task"} {
		if value, ok := intent[field]; ok && value != nil && value != "" {
			input[field] = value
		}
	}
	if _, ok := input["value_statement"]; !ok {
		input["value_statement"] = "Retain the approved outcome"
	}
	return retryJSON(input)
}

func dispatchContractRevisionResult(t *testing.T, s *store.Store, service *Service, env CallEnvelope, workID string, version int64, contract map[string]any, key string) (agentcontract.Envelope, int64) {
	t.Helper()
	raw := contractRevisionInput(t, s, workID, version, contract, key)
	response := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "revise_intent", Input: raw}, env)
	return response, workVersion(t, s, workID)
}

func TestReviseIntentUpdatesContractAtomicallyAndReplays(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_define", "work_transition"})
	if _, err := pm1fixture.SeedCommittedProductDomain(t.Context(), s, "product-1", "project-1", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	definition, err := store.LiveWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), "work-1")
	if err != nil {
		t.Fatal(err)
	}
	actor := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: "work-1", Definition: definition, Registry: store.BuiltinWorkflowRegistry(), Actor: actor, Now: fixedTime()})
	}); err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersionForProject(t, s, "project-1"))
	invokeAction := func(action string, fields map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "action_id": action, "fields": fields, "idempotency_key": "update-fixture-" + action})
		response := invokeWorkflowIssue31(t, s, service, env, raw)
		if response.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("%s: %+v", action, response.Error)
		}
	}
	invokeAction("record_diagnosis", map[string]any{"reproduction": "The plan needs revision.", "searched": "No overlapping work.", "outcome": "none_found", "root_cause": "The recorded premise was incomplete."})
	contract := workflowContractFieldsFixture()
	contract["architecture_binding"] = workflowArchitectureBindingForSeededRegistry(t, s)
	contract["decision_domains"] = []string{}
	contract["outcome_predicates"] = []map[string]any{{"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:plan", "immutable_subject_ref": "commit:fixture", "expected_result": "pass"}}}
	invokeAction("approve_contract", contract)
	contract["contract_version"] = 2
	delete(contract, "route_convention")
	var required, routes, rigor string
	if err := s.DatabaseForTesting().QueryRow(`SELECT required_evidence,route_conventions,rigor_class FROM workflow_contracts WHERE work_id='work-1' AND contract_version=1`).Scan(&required, &routes, &rigor); err != nil {
		t.Fatal(err)
	}
	contract["required_evidence"], contract["route_conventions"], contract["rigor_class"] = json.RawMessage(required), json.RawMessage(routes), rigor
	expected := workVersion(t, s, "work-1")
	raw, _ := json.Marshal(map[string]any{"work_id": "work-1", "expected_version": expected, "title": "Revised plan", "value_statement": "Retain proof", "kind": "task", "reason": "Clarify the plan", "contract": contract, "idempotency_key": "ordinary-contract-update"})
	request := InvokeRequest{Tool: "concord_work_define", Operation: "revise_intent", Input: raw}
	for attempt := 0; attempt < 2; attempt++ {
		response, err := Dispatch(t.Context(), s, service, request, env)
		if err != nil || response.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("update attempt %d: %+v, %v", attempt, response.Error, err)
		}
		if response.Replayed != (attempt == 1) {
			t.Fatalf("attempt %d replay = %v", attempt, response.Replayed)
		}
	}
	if got := workVersion(t, s, "work-1"); got != expected+1 {
		t.Fatalf("update version = %d, want %d", got, expected+1)
	}
	var active int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id='work-1' AND superseded_by IS NULL`).Scan(&active); err != nil || active != 2 {
		t.Fatalf("active contract = %d, %v", active, err)
	}
}

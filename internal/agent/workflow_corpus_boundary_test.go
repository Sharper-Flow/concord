package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func invokeWorkflowBoundary(t *testing.T, s *store.Store, service *Service, env CallEnvelope, input map[string]any) agentcontract.Envelope {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	response, err := Invoke(context.Background(), s, service, mustMarshalInvoke(t, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env))
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func advanceWorkflowBoundaryToExecution(t *testing.T, s *store.Store, service *Service, env CallEnvelope) (CallEnvelope, int64) {
	t.Helper()
	version, err := workVersionForWorkflow(t, s)
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "approve_contract", "fields": map[string]any{}, "idempotency_key": "boundary-approve"}
	addWorkflowContractApprovalFields(input)
	input["fields"].(map[string]any)["architecture_binding"] = workflowArchitectureBindingForSeededRegistry(t, s)
	response := invokeWorkflowBoundary(t, s, service, env, input)
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("approved action=%+v", response.Error)
	}
	version = workVersion(t, s, "work-1")
	response = invokeWorkflowBoundary(t, s, service, env, map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "record_plan", "fields": map[string]any{
		"outcomes": "Working outcome", "approach": "Bounded change", "decisions": []map[string]any{{"id": "decision:one", "question": "Which approach?", "choice": "One approach", "rationale": "It fits", "rejected": []string{}}}, "touches": []string{"file:one"}, "merge_split": "One item", "risks": "Bounded risk",
	}, "idempotency_key": "boundary-record-plan"})
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("record plan=%+v", response.Error)
	}
	version = workVersion(t, s, "work-1")
	response = invokeWorkflowBoundary(t, s, service, env, map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "complete_step", "fields": map[string]any{"step_id": "plan", "note": "Plan is complete."}, "idempotency_key": "boundary-complete-plan"})
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("complete plan=%+v", response.Error)
	}
	return env, workVersion(t, s, "work-1")
}

func TestWorkflowActionStrictInvokeBoundaryForStepActorAndPayload(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})
	seedAgentWorkflow(t, s, grant)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	env, version := advanceWorkflowBoundaryToExecution(t, s, service, env)
	beforeEffects := workflowReplayEffectCounts(t, s)
	input := map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "start_execution", "fields": map[string]any{"payload": map[string]any{"current_step": "plan"}}, "idempotency_key": "boundary-malformed-payload"}
	var before int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE kind=?`, store.WorkflowActionCompleted).Scan(&before); err != nil {
		t.Fatal(err)
	}
	response := invokeWorkflowBoundary(t, s, service, env, input)
	if response.Outcome != agentcontract.OutcomeError || response.Error == nil || response.Error.Kind != "invalid_input" {
		t.Fatalf("malformed step/payload response=%+v", response)
	}
	var after int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE kind=?`, store.WorkflowActionCompleted).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("malformed request appended action event: before=%d after=%d", before, after)
	}

	actorEnv := env
	actorEnv.AgentRef = "agent:malformed"
	actorInput := map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "start_execution", "fields": map[string]any{}, "idempotency_key": "boundary-malformed-actor"}
	actorResponse := invokeWorkflowBoundary(t, s, service, actorEnv, actorInput)
	if actorResponse.Outcome != agentcontract.OutcomeError || actorResponse.Error == nil || actorResponse.Error.Kind != "unauthorized" {
		t.Fatalf("malformed actor response outcome=%s error=%+v", actorResponse.Outcome, actorResponse.Error)
	}
	if afterEffects := workflowReplayEffectCounts(t, s); afterEffects != beforeEffects {
		t.Fatalf("malformed payload or actor changed durable state: before=%+v after=%+v", beforeEffects, afterEffects)
	}
}

func TestWorkflowActionRejectsReplayWithoutEffects(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})
	version := seedAgentWorkflow(t, s, grant)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	var eventsBefore int
	if err := s.DatabaseForTesting().QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "replay", "fields": map[string]any{}, "idempotency_key": "boundary-replay-refused"}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	response, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, mutationEnvelope(grant, scopeVersion))
	if err != nil {
		t.Fatal(err)
	}
	// The closed action union refuses replay before mutation authority.
	if response.Outcome != agentcontract.OutcomeError || response.Error == nil || response.Error.Kind != "invalid_input" || !strings.Contains(response.Error.Message, "action_id") {
		if response.Error == nil {
			t.Fatalf("forbidden replay response=%+v", response)
		}
		t.Fatalf("forbidden replay response outcome=%s error=%+v", response.Outcome, *response.Error)
	}
	var eventsAfter int
	if err := s.DatabaseForTesting().QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&eventsAfter); err != nil {
		t.Fatal(err)
	}
	if eventsAfter != eventsBefore {
		t.Fatalf("forbidden replay shape appended events: before=%d after=%d", eventsBefore, eventsAfter)
	}
}

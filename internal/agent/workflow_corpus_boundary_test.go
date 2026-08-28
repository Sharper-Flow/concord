package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

type workflowBoundaryScenario struct {
	ID       string         `json:"id"`
	Action   string         `json:"action"`
	Setup    corpusSetup    `json:"setup"`
	Request  corpusRequest  `json:"request"`
	Expected corpusExpected `json:"expected"`
}

func readWorkflowBoundaryScenario(t *testing.T, id string) workflowBoundaryScenario {
	t.Helper()
	data, err := os.ReadFile("../../scenarios/workflow-engine.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Scenarios []workflowBoundaryScenario `json:"scenarios"`
	}
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range corpus.Scenarios {
		if scenario.ID == id {
			return scenario
		}
	}
	t.Fatalf("scenario %s is missing", id)
	return workflowBoundaryScenario{}
}

func invokeWorkflowBoundary(t *testing.T, s *store.Store, service *Service, env CallEnvelope, input map[string]any, registry store.DefinitionRegistry) Envelope {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	response, err := InvokeWithRegistry(context.Background(), s, service, mustMarshalInvoke(t, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env), registry)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestWorkflowCorpusWF37UsesAgentAvailabilityBeforePayloadOrAuth(t *testing.T) {
	scenario := readWorkflowBoundaryScenario(t, "WF37-action-availability-before-register")
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
	if got := seedAgentWorkflow(t, s, grant); got != 4 {
		t.Fatalf("workflow seed version=%d, want 4", got)
	}
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	input := map[string]any{"work_id": "work-1", "expected_version": scenario.Request.ExpectedVersion, "action_id": scenario.Request.ActionID, "fields": []any{}, "idempotency_key": scenario.Request.Idempotency.Key}
	response := invokeWorkflowBoundary(t, s, service, env, input, store.NewWorkflowDefinitionRegistry())
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "invalid_transition" {
		t.Fatalf("WF37 response outcome=%s error=%+v", response.Outcome, response.Error)
	}
	var started int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE kind=?`, store.WorkflowActionStarted).Scan(&started); err != nil {
		t.Fatal(err)
	}
	if started != 0 {
		t.Fatalf("WF37 availability failure reached mutation authority: action_started=%d", started)
	}
}

func advanceWorkflowBoundaryToExecution(t *testing.T, s *store.Store, service *Service, grant Authority, privateKey ed25519.PrivateKey, env CallEnvelope) (CallEnvelope, int64) {
	t.Helper()
	for index, action := range []string{"record_proposal", "record_discovery", "record_design"} {
		input := map[string]any{"work_id": "work-1", "expected_version": int64(4 + index), "action_id": action, "fields": []any{}, "idempotency_key": "boundary-" + action}
		response := invokeWorkflowBoundary(t, s, service, env, input, store.BuiltinWorkflowRegistry())
		if response.Outcome != OutcomeOK {
			t.Fatalf("advance action=%s response=%+v", action, response)
		}
	}
	challengeInput := map[string]any{"work_id": "work-1", "expected_version": int64(7), "action_id": "approve_contract", "fields": []any{}, "idempotency_key": "boundary-approve"}
	addWorkflowContractApprovalFields(challengeInput)
	challenge := invokeWorkflowBoundary(t, s, service, env, challengeInput, store.BuiltinWorkflowRegistry())
	if challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("approval challenge outcome=%s error=%+v", challenge.Outcome, challenge.Error)
	}
	challengeRef, _ := challenge.Error.Details["approval_ref"].(string)
	digest := mutationDigest("concord_work_transition", "workflow_action", env, mustJSON(t, challengeInput))
	scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": env.ScopeVersion}
	versions := map[string]any{"work": int64(7)}
	approvalEnv := env
	approvalEnv.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), "boundary-approval")
	approved := challengeInput
	approved["approval"] = map[string]any{"approval_ref": challengeRef}
	response := invokeWorkflowBoundary(t, s, service, approvalEnv, approved, store.BuiltinWorkflowRegistry())
	if response.Outcome != OutcomeOK {
		t.Fatalf("approved action=%+v", response)
	}
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-1'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return approvalEnv, version
}

func TestWorkflowCorpusWF38UsesStrictInvokeBoundaryForStepActorAndPayload(t *testing.T) {
	scenario := readWorkflowBoundaryScenario(t, "WF38-action-payload-step-actor")
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition"})
	seedAgentWorkflow(t, s, grant)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	env, version := advanceWorkflowBoundaryToExecution(t, s, service, grant, privateKey, env)
	input := map[string]any{"work_id": "work-1", "expected_version": version, "action_id": scenario.Request.ActionID, "fields": []any{map[string]any{"name": "payload", "value": `{"current_step":"plan"}`}}, "idempotency_key": scenario.Request.Idempotency.Key}
	var before int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE kind=?`, store.WorkflowActionCompleted).Scan(&before); err != nil {
		t.Fatal(err)
	}
	response := invokeWorkflowBoundary(t, s, service, env, input, store.BuiltinWorkflowRegistry())
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "invariant_violation" {
		t.Fatalf("WF38 malformed step/payload response=%+v", response)
	}
	var after int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE kind=?`, store.WorkflowActionCompleted).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("WF38 malformed request appended action event: before=%d after=%d", before, after)
	}

	actorEnv := env
	actorEnv.AgentRef = "agent:malformed"
	actorInput := input
	actorInput["idempotency_key"] = "wf38-malformed-actor"
	actorResponse := invokeWorkflowBoundary(t, s, service, actorEnv, actorInput, store.BuiltinWorkflowRegistry())
	if actorResponse.Outcome != OutcomeError || actorResponse.Error == nil || actorResponse.Error.Kind != "unauthorized" {
		t.Fatalf("WF38 malformed actor response outcome=%s error=%+v", actorResponse.Outcome, actorResponse.Error)
	}
}

func TestWorkflowCorpusWF46RejectsRemovedAgentReplayShape(t *testing.T) {
	scenario := readWorkflowBoundaryScenario(t, "WF46-event-version-fail-closed")
	if scenario.Action != "replay" {
		t.Fatalf("WF46 no longer declares the historical replay action: %q", scenario.Action)
	}
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
	workID := scenario.Setup.FixtureRefs.WorkItem
	seedCorpusWorkflowWork(t, s, grant, workID, scenario.Setup.EventHistory)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	var eventsBefore int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events`).Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"work_id": workID, "expected_version": scenario.Request.ExpectedVersion, "action_id": scenario.Action, "fields": scenario.Request.Fields, "idempotency_key": scenario.Request.Idempotency.Key}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	response, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, mutationEnvelope(grant, scopeVersion))
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "invalid_input" {
		if response.Error == nil {
			t.Fatalf("WF46 removed replay response=%+v", response)
		}
		t.Fatalf("WF46 removed replay response outcome=%s error=%+v", response.Outcome, *response.Error)
	}
	var eventsAfter int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events`).Scan(&eventsAfter); err != nil {
		t.Fatal(err)
	}
	if eventsAfter != eventsBefore {
		t.Fatalf("WF46 removed replay shape appended events: before=%d after=%d", eventsBefore, eventsAfter)
	}
}

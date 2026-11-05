package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestCaptureWorkflowTypeInitializesAndDispatchesFirstAction(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_define", "work_transition"})
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	capture := InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: json.RawMessage(`{"title":"Workflow work","kind":"task","project_ids":["project-1"],"idempotency_key":"capture-workflow-31","human_key":"ZZ-3191","remote_issue_uuid":"fixture-capture-workflow-31","url":"https://linear.app/example/issue/ZZ-3191"}`)}
	captured, err := Dispatch(context.Background(), s, service, capture, env)
	if err != nil || captured.Outcome != agentcontract.OutcomeOK || captured.ChangedRefs == nil || len(*captured.ChangedRefs) != 2 {
		t.Fatalf("capture response=%+v err=%v", captured, err)
	}
	workID := (*captured.ChangedRefs)[0].ID
	var state, actor string
	var plan *string
	if err := s.DatabaseForTesting().QueryRow(`SELECT instance_state,execution_actor_ref,step_plan FROM workflow_instances WHERE work_id=?`, workID).Scan(&state, &actor, &plan); err != nil {
		t.Fatal(err)
	}
	if state != "intake" || actor != store.DeriveWorkflowActorRef(grant.PrincipalRef, grant.ClientRef, grant.AgentRef, grant.SessionRef) || plan != nil {
		t.Fatalf("capture state=%q actor=%q plan=%v", state, actor, plan)
	}
	for _, action := range []struct {
		id     string
		fields map[string]any
	}{
		{"record_intake", map[string]any{"outcome": "proceed", "open_questions_found": false}},
		{"pick_type", map[string]any{"type": "change", "reason": "The task changes behavior."}},
	} {
		input := retryJSON(map[string]any{"work_id": workID, "expected_version": workVersion(t, s, workID), "action_id": action.id, "fields": action.fields, "idempotency_key": "capture-31-" + action.id})
		response, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: input}, env)
		if err != nil || response.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("%s response=%+v err=%v", action.id, response.Error, err)
		}
	}
	var raw string
	if err := s.DatabaseForTesting().QueryRow(`SELECT step_plan FROM workflow_instances WHERE work_id=?`, workID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var picked store.WorkflowStepPlan
	if err := json.Unmarshal([]byte(raw), &picked); err != nil {
		t.Fatal(err)
	}
	frontier := store.WorkflowStepPlanFrontier(picked)
	if picked.Type != store.WorkflowStepPlanChange || len(frontier) != 1 || frontier[0].ID != "plan" {
		t.Fatalf("picked plan=%+v frontier=%+v", picked, frontier)
	}
}

func TestWorkflowActionMalformedBoundaryPrecedesPinAndAuthorityChecks(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	malformed := []string{
		`{"work_id":"work-1","expected_version":1,"action_id":"record_proposal","idempotency_key":"bad-1","unknown":true}`,
		`{"work_id":"work-1","expected_version":1,"action_id":"record_proposal","action_id":"record_design","idempotency_key":"bad-2"}`,
		`{"work_id":7,"expected_version":1,"action_id":"record_proposal","idempotency_key":"bad-3"}`,
		`{"work_id":"work-1","expected_version":1,"action_id":"record_proposal","idempotency_key":"bad-4"} trailing`,
		`{"work_id":"","expected_version":1,"action_id":"record_proposal","idempotency_key":"bad-5"}`,
	}
	for _, raw := range malformed {
		response, dispatchErr := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: json.RawMessage(raw)}, env)
		if dispatchErr != nil || response.Outcome != agentcontract.OutcomeError || response.Error == nil || response.Error.Kind != "invalid_input" {
			t.Fatalf("malformed workflow request response=%+v err=%v", response, dispatchErr)
		}
	}
}

func invokeWorkflowIssue31Action(t *testing.T, s *store.Store, service *Service, env CallEnvelope, actionID string, version int64, key string) {
	t.Helper()
	fields := ""
	if actionID == "record_alignment" {
		fields = `,"fields":{"searched":"The bounded backlog search statement.","outcome":"none_found"}`
	}
	if actionID == "record_design" {
		fields = `,"fields":{"approach":"The recorded approach is the implementation boundary.","decisions":[{"id":"decision:issue31","question":"What crosses into execution?","choice":"The typed design record.","rationale":"The worker must receive the approved decision.","rejected":[]}],"touched_refs":["path:issue31"]}`
	}
	if actionID == "record_proposal" {
		fields = `,"fields":{"problem":"The bounded problem statement.","affected":["The affected system."],"stakes":"The bounded stakes statement.","user_outcomes":["The expected user outcome."]}`
	}
	input := json.RawMessage(`{"work_id":"work-1","expected_version":` + strconv.FormatInt(version, 10) + `,"action_id":"` + actionID + `"` + fields + `,"idempotency_key":"` + key + `"}`)
	response := invokeWorkflowIssue31(t, s, service, env, input)
	if response.Outcome != agentcontract.OutcomeOK {
		if response.Error != nil {
			t.Fatalf("action=%s error=%+v response=%+v", actionID, *response.Error, response)
		}
		t.Fatalf("action=%s response=%+v", actionID, response)
	}
}

func invokeWorkflowIssue31(t *testing.T, s *store.Store, service *Service, env CallEnvelope, input json.RawMessage) agentcontract.Envelope {
	t.Helper()
	request, err := json.Marshal(struct {
		CallEnvelope CallEnvelope    `json:"call_envelope"`
		Tool         string          `json:"tool"`
		Operation    string          `json:"operation"`
		Input        json.RawMessage `json:"input"`
	}{CallEnvelope: env, Tool: "concord_work_transition", Operation: "workflow_action", Input: input})
	if err != nil {
		t.Fatal(err)
	}
	response, err := Invoke(context.Background(), s, service, request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func workOneVersion(t *testing.T, s *store.Store) int64 {
	t.Helper()
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-1'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func issue31EvaluatorGrant(t *testing.T, service *Service, privateKey ed25519.PrivateKey) Authority {
	t.Helper()
	_ = privateKey
	grant, err := service.Authorize(context.Background(), Invocation{ClientRef: "client-1", PrincipalRef: "human-1", SessionRef: "session-evaluator", AgentRef: "agent-evaluator", Directory: "/repo", Worktree: "/repo-wt", ManifestDigest: agentcontract.ManifestDigest, RequiredCapability: "work_transition", ProductID: "product-1", ProjectID: "project-1"})
	if err != nil {
		t.Fatal(err)
	}
	return grant
}

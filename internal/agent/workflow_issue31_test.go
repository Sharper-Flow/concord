package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestCaptureWorkflowTypeInitializesAndDispatchesFirstAction(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_define", "work_transition"})
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	capture := InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: json.RawMessage(`{"title":"Workflow work","value_statement":"Ship the workflow","kind":"task","project_ids":["project-1"],"workflow_type_ref":"workflow.implementation","idempotency_key":"capture-workflow-31"}`)}
	captured, err := Dispatch(context.Background(), s, service, capture, env)
	if err != nil || captured.Outcome != OutcomeOK || len(captured.ChangedRefs) != 1 {
		t.Fatalf("capture response=%+v err=%v", captured, err)
	}
	if captured.ChangedRefs[0].Version != "4" {
		t.Fatalf("captured version=%s, want 4", captured.ChangedRefs[0].Version)
	}
	workID := captured.ChangedRefs[0].ID
	var definition string
	if err := s.DB().QueryRow(`SELECT definition_ref FROM workflow_instances WHERE work_id=?`, workID).Scan(&definition); err != nil {
		t.Fatal(err)
	}
	if definition != "workflow.implementation" {
		t.Fatalf("definition=%q", definition)
	}
	var eventKinds []string
	rows, err := s.DB().Query(`SELECT kind FROM domain_events WHERE subject_id=? ORDER BY seq`, workID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			t.Fatal(err)
		}
		eventKinds = append(eventKinds, kind)
	}
	want := []string{"work.created", "work.memberships_replaced", store.WorkflowActorRecorded, store.WorkflowDefinitionSelected}
	if len(eventKinds) != len(want) {
		t.Fatalf("initialization events=%v, want %v", eventKinds, want)
	}
	for i := range want {
		if eventKinds[i] != want[i] {
			t.Fatalf("initialization event[%d]=%q, want %q", i, eventKinds[i], want[i])
		}
	}

	action := InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: json.RawMessage(`{"work_id":"` + workID + `","expected_version":4,"action_id":"record_proposal","idempotency_key":"first-workflow-action-31"}`)}
	acted, err := Dispatch(context.Background(), s, service, action, env)
	if err != nil || acted.Outcome != OutcomeOK {
		t.Fatalf("first action response=%+v err=%v", acted, err)
	}
	var completed, currentStep int
	if err := s.DB().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, store.WorkflowActionCompleted).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT count(*) FROM workflow_instances WHERE work_id=? AND current_step='discovery'`, workID).Scan(&currentStep); err != nil {
		t.Fatal(err)
	}
	if completed != 1 || currentStep != 1 {
		t.Fatalf("first action completion=%d step=%d", completed, currentStep)
	}
}

func TestOperatorQuestionPremiseSummaryUsesSchemaRuneBound(t *testing.T) {
	choice := map[string]any{"id": "confirm", "label": "Confirm", "description": "Continue", "action_id": "confirm_premise"}
	for _, testCase := range []struct {
		name  string
		value string
		valid bool
	}{
		{name: "ascii-256", value: strings.Repeat("a", 256), valid: true},
		{name: "ascii-257", value: strings.Repeat("a", 257), valid: false},
		{name: "multibyte-256", value: strings.Repeat("界", 256), valid: true},
		{name: "multibyte-257", value: strings.Repeat("界", 257), valid: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			payload, err := json.Marshal(map[string]any{
				"action_id": "confirm_premise", "prompt": "Choose", "header": "Checkpoint", "choices": []any{choice},
				"allow_multiple": false, "allow_custom": false, "premise_summary": testCase.value,
				"contract_summary": "contract v1", "decision_context_digest": "sha256:" + strings.Repeat("a", 64),
			})
			if err != nil {
				t.Fatal(err)
			}
			err = ValidatePayloadSchema("operator_question", payload)
			if (err == nil) != testCase.valid {
				t.Fatalf("schema result err=%v valid=%v", err, testCase.valid)
			}
		})
	}
}

func TestWorkflowActionMalformedBoundaryPrecedesPinAndAuthorityChecks(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	malformed := []string{
		`{"work_id":"work-1","expected_version":1,"action_id":"record_proposal","idempotency_key":"bad-1","unknown":true}`,
		`{"work_id":"work-1","expected_version":1,"action_id":"record_proposal","action_id":"record_design","idempotency_key":"bad-2"}`,
		`{"work_id":7,"expected_version":1,"action_id":"record_proposal","idempotency_key":"bad-3"}`,
		`{"work_id":"work-1","expected_version":1,"action_id":"record_proposal","fields":{},"idempotency_key":"bad-4"}`,
		`{"work_id":"work-1","expected_version":1,"action_id":"record_proposal","idempotency_key":"bad-5"} trailing`,
		`{"work_id":"","expected_version":1,"action_id":"record_proposal","idempotency_key":"bad-6"}`,
	}
	for _, raw := range malformed {
		response, dispatchErr := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: json.RawMessage(raw)}, env)
		if dispatchErr != nil || response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "invalid_input" {
			t.Fatalf("malformed workflow request response=%+v err=%v", response, dispatchErr)
		}
	}
	var used int
	if err := s.DB().QueryRow(`SELECT used_count FROM agent_grants WHERE grant_hash=?`, sha256Bytes([]byte(grant.Token))).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if used != 0 {
		t.Fatalf("malformed workflow requests consumed grant authorization: %d", used)
	}
}

func TestConfirmPremiseInvokeDerivesOperatorFromSignedApproval(t *testing.T) {
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition"})
	if got := seedAgentWorkflow(t, s, grant); got != 4 {
		t.Fatalf("workflow seed version=%d, want 4", got)
	}
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	evaluatorEnv := mutationEnvelope(issue31EvaluatorGrant(t, service, privateKey), scopeVersion)
	for _, actionID := range []string{"record_proposal", "record_discovery", "record_design"} {
		invokeWorkflowIssue31Action(t, s, service, env, "work-1", actionID, workflowIssue31Version(t, s), "issue31-"+actionID)
	}
	approve := json.RawMessage(`{"work_id":"work-1","expected_version":7,"action_id":"approve_contract","idempotency_key":"issue31-approve"}`)
	challenge := invokeWorkflowIssue31(t, s, service, env, "concord_work_transition", "workflow_action", approve)
	if challenge.Outcome != OutcomeError || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("approval challenge=%+v", challenge)
	}
	challengeRef, _ := challenge.Error.Details["approval_ref"].(string)
	digest := mutationDigest("concord_work_transition", "workflow_action", env, approve)
	scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion}
	versions := map[string]any{"work": 7}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, grant.ClientVersion, fixedTime(), "issue31-approve-nonce")
	approved := json.RawMessage(`{"work_id":"work-1","expected_version":7,"action_id":"approve_contract","idempotency_key":"issue31-approve","approval":{"approval_ref":"` + challengeRef + `"}}`)
	response := invokeWorkflowIssue31(t, s, service, env, "concord_work_transition", "workflow_action", approved)
	if response.Outcome != OutcomeOK {
		t.Fatalf("approved contract=%+v", response)
	}
	invokeWorkflowIssue31Action(t, s, service, env, "work-1", "start_execution", workflowIssue31Version(t, s), "issue31-start")
	// The identity boundary is the subject of this test; place the fixture at
	// its accepted checkpoint without bypassing the fold-only trigger policy.
	if _, err := s.DB().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE workflow_instances SET current_step='acceptance' WHERE work_id='work-1'; DELETE FROM fold_guard WHERE active=1`); err != nil {
		t.Fatal(err)
	}
	invokeWorkflowIssue31Action(t, s, service, evaluatorEnv, "work-1", "record_verdict", workflowIssue31Version(t, s), "issue31-verdict")

	confirm := issue31ConfirmInput(t, s, workflowIssue31Version(t, s), "issue31-confirm")
	challenge = invokeWorkflowIssue31(t, s, service, env, "concord_work_transition", "workflow_action", confirm)
	if challenge.Outcome != OutcomeError || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		if challenge.Error != nil {
			t.Fatalf("premise challenge error=%+v response=%+v", *challenge.Error, challenge)
		}
		t.Fatalf("premise challenge=%+v", challenge)
	}
	challengeRef, _ = challenge.Error.Details["approval_ref"].(string)
	digest = mutationDigest("concord_work_transition", "workflow_action", env, confirm)
	versions = map[string]any{"work": workflowIssue31Version(t, s), "contract": 1}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, grant.ClientVersion, fixedTime(), "issue31-confirm-nonce")
	approved = json.RawMessage(`{"work_id":"work-1","expected_version":` + strconv.FormatInt(workflowIssue31Version(t, s), 10) + `,"action_id":"confirm_premise","selected_choice":"confirm","decision_context_digest":"` + extractDecisionDigest(t, confirm) + `","idempotency_key":"issue31-confirm","approval":{"approval_ref":"` + challengeRef + `"}}`)
	beforeEvents := countWorkflowEvents(t, s)
	response = invokeWorkflowIssue31(t, s, service, env, "concord_work_transition", "workflow_action", approved)
	if response.Outcome != OutcomeOK {
		t.Fatalf("confirmed premise=%+v error=%+v", response, response.Error)
	}
	var actorClass, confirmedBy, eventActor string
	if err := s.DB().QueryRow(`SELECT a.actor_class,pc.confirmed_by,e.actor FROM workflow_premise_confirmations pc JOIN workflow_actors a ON a.actor_ref=pc.confirmed_by JOIN domain_events e ON e.subject_id=pc.work_id AND e.kind=?`, store.WorkflowPremiseConfirmed).Scan(&actorClass, &confirmedBy, &eventActor); err != nil {
		t.Fatal(err)
	}
	if actorClass != string(store.ActorOperator) || confirmedBy != eventActor {
		t.Fatalf("operator confirmation actor_class=%q confirming=%q event_actor=%q", actorClass, confirmedBy, eventActor)
	}
	var actorRows int
	if err := s.DB().QueryRow(`SELECT count(*) FROM workflow_actors WHERE actor_class=?`, store.ActorOperator).Scan(&actorRows); err != nil {
		t.Fatal(err)
	}
	if actorRows != 1 || countWorkflowEvents(t, s) <= beforeEvents {
		t.Fatalf("operator actor/event persistence rows=%d events_before=%d events_after=%d", actorRows, beforeEvents, countWorkflowEvents(t, s))
	}

	replay := invokeWorkflowIssue31(t, s, service, env, "concord_work_transition", "workflow_action", approved)
	if replay.Outcome != OutcomeOK || !replay.Replayed {
		t.Fatalf("stable replay=%+v error=%+v", replay, replay.Error)
	}
	var replayActors, replayPremises int
	if err := s.DB().QueryRow(`SELECT count(*) FROM workflow_actors WHERE actor_class=?`, store.ActorOperator).Scan(&replayActors); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT count(*) FROM workflow_premise_confirmations`).Scan(&replayPremises); err != nil {
		t.Fatal(err)
	}
	if replayActors != 1 || replayPremises != 1 || countWorkflowEvents(t, s) <= beforeEvents {
		t.Fatalf("replay changed operator state actors=%d premises=%d events=%d", replayActors, replayPremises, countWorkflowEvents(t, s))
	}
}

func invokeWorkflowIssue31Action(t *testing.T, s *store.Store, service *Service, env CallEnvelope, workID, actionID string, version int64, key string) {
	t.Helper()
	input := json.RawMessage(`{"work_id":"` + workID + `","expected_version":` + strconv.FormatInt(version, 10) + `,"action_id":"` + actionID + `","idempotency_key":"` + key + `"}`)
	response := invokeWorkflowIssue31(t, s, service, env, "concord_work_transition", "workflow_action", input)
	if response.Outcome != OutcomeOK {
		if response.Error != nil {
			t.Fatalf("action=%s error=%+v response=%+v", actionID, *response.Error, response)
		}
		t.Fatalf("action=%s response=%+v", actionID, response)
	}
}

func invokeWorkflowIssue31(t *testing.T, s *store.Store, service *Service, env CallEnvelope, tool, operation string, input json.RawMessage) Envelope {
	t.Helper()
	request, err := json.Marshal(struct {
		CallEnvelope CallEnvelope    `json:"call_envelope"`
		Tool         string          `json:"tool"`
		Operation    string          `json:"operation"`
		Input        json.RawMessage `json:"input"`
	}{CallEnvelope: env, Tool: tool, Operation: operation, Input: input})
	if err != nil {
		t.Fatal(err)
	}
	response, err := Invoke(context.Background(), s, service, request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func workflowIssue31Version(t *testing.T, s *store.Store) int64 {
	t.Helper()
	var version int64
	if err := s.DB().QueryRow(`SELECT version FROM work_items WHERE id='work-1'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func issue31ConfirmInput(t *testing.T, s *store.Store, version int64, key string) json.RawMessage {
	t.Helper()
	question, err := store.ReadWorkflowOperatorQuestion(context.Background(), s, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	if question == nil {
		t.Fatal("missing operator question")
	}
	if question.ActionID != "confirm_premise" || question.Header != "Operator checkpoint" || question.AllowMultiple || question.AllowCustom || len(question.Choices) != 3 {
		t.Fatalf("operator question shape=%+v", question)
	}
	if question.Choices[0].ID != "confirm" || question.Choices[0].ActionID != "confirm_premise" || question.Choices[1].ActionID != "concord_work_define.revise_intent" || question.Choices[2].ActionID != "concord_work_transition.lifecycle" {
		t.Fatalf("operator question choices=%+v", question.Choices)
	}
	return json.RawMessage(`{"work_id":"work-1","expected_version":` + strconv.FormatInt(version, 10) + `,"action_id":"confirm_premise","selected_choice":"confirm","decision_context_digest":"` + question.DecisionContextDigest + `","idempotency_key":"` + key + `"}`)
}

func extractDecisionDigest(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var input struct {
		DecisionContextDigest string `json:"decision_context_digest"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		t.Fatal(err)
	}
	return input.DecisionContextDigest
}

func TestConfirmPremiseInvokeRejectsMissingWrongAndPayloadActorsWithoutState(t *testing.T) {
	for _, testCase := range []struct {
		name string
	}{
		{name: "missing assertion"},
		{name: "wrong version assertion"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			s, service, grant, privateKey, env, confirm, challengeRef, digest, scope, versions := prepareIssue31Confirm(t)
			beforeEvents := countWorkflowEvents(t, s)
			beforeVersion := workflowIssue31Version(t, s)
			beforeActors := countWorkflowActorRowsIssue31(t, s)
			if testCase.name == "missing assertion" {
				env.HostApproval = nil
			} else {
				versions["work"] = beforeVersion + 1
				env.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, grant.ClientVersion, fixedTime(), "issue31-wrong-version")
			}
			approved := json.RawMessage(`{"work_id":"work-1","expected_version":` + strconv.FormatInt(beforeVersion, 10) + `,"action_id":"confirm_premise","idempotency_key":"issue31-rejected-confirm","approval":{"approval_ref":"` + challengeRef + `"}}`)
			response := invokeWorkflowIssue31(t, s, service, env, "concord_work_transition", "workflow_action", approved)
			if response.Outcome != OutcomeError {
				t.Fatalf("rejected confirmation response=%+v", response)
			}
			if countWorkflowEvents(t, s) != beforeEvents || workflowIssue31Version(t, s) != beforeVersion || countWorkflowActorRowsIssue31(t, s) != beforeActors {
				t.Fatalf("rejected confirmation changed state events=%d/%d version=%d/%d actors=%d/%d", countWorkflowEvents(t, s), beforeEvents, workflowIssue31Version(t, s), beforeVersion, countWorkflowActorRowsIssue31(t, s), beforeActors)
			}
			_ = confirm
		})
	}
}

func TestConfirmPremiseInvokeRejectsPayloadActorBorrowing(t *testing.T) {
	s, service, _, _, env, _, challengeRef, _, _, _ := prepareIssue31Confirm(t)
	beforeEvents := countWorkflowEvents(t, s)
	beforeVersion := workflowIssue31Version(t, s)
	beforeActors := countWorkflowActorRowsIssue31(t, s)
	input := json.RawMessage(`{"work_id":"work-1","expected_version":` + strconv.FormatInt(beforeVersion, 10) + `,"action_id":"confirm_premise","fields":{"confirming_actor_ref":"actor:payload-selected"},"idempotency_key":"payload-actor-confirm","approval":{"approval_ref":"` + challengeRef + `"}}`)
	response := invokeWorkflowIssue31(t, s, service, env, "concord_work_transition", "workflow_action", input)
	if response.Outcome != OutcomeError {
		t.Fatalf("payload actor response=%+v", response)
	}
	if countWorkflowEvents(t, s) != beforeEvents || workflowIssue31Version(t, s) != beforeVersion || countWorkflowActorRowsIssue31(t, s) != beforeActors {
		t.Fatalf("payload actor changed state events=%d/%d version=%d/%d actors=%d/%d", countWorkflowEvents(t, s), beforeEvents, workflowIssue31Version(t, s), beforeVersion, countWorkflowActorRowsIssue31(t, s), beforeActors)
	}
}

func TestConfirmPremiseQuestionFailuresAreAuthorityNoOps(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "missing-digest", mutate: func(input map[string]any) { delete(input, "decision_context_digest") }},
		{name: "malformed-digest", mutate: func(input map[string]any) { input["decision_context_digest"] = "not-a-digest" }},
		{name: "stale-digest", mutate: func(input map[string]any) { input["decision_context_digest"] = "sha256:" + strings.Repeat("f", 64) }},
		{name: "revise-routed-away", mutate: func(input map[string]any) { input["selected_choice"] = "revise" }},
		{name: "stop-routed-away", mutate: func(input map[string]any) { input["selected_choice"] = "stop" }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			s, service, grant, _, env, confirm, _, _, _, _ := prepareIssue31Confirm(t)
			before := workflowIssue31AuthoritySnapshot(t, s, grant)
			var input map[string]any
			if err := json.Unmarshal(confirm, &input); err != nil {
				t.Fatal(err)
			}
			input["idempotency_key"] = "negative-" + testCase.name
			testCase.mutate(input)
			raw, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			response := invokeWorkflowIssue31(t, s, service, env, "concord_work_transition", "workflow_action", raw)
			if response.Outcome == OutcomeOK {
				t.Fatalf("negative selection unexpectedly succeeded: %+v", response)
			}
			after := workflowIssue31AuthoritySnapshot(t, s, grant)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("negative selection changed authority before=%+v after=%+v", before, after)
			}
		})
	}
}

type workflowIssue31AuthorityState struct {
	Version        int64
	GrantUsed      int
	Events         int
	Actors         int
	Challenges     int
	Approvals      int
	Projections    map[string]int
	InstanceState  string
	CurrentStep    string
	ChallengeState string
	ApprovalState  string
}

func workflowIssue31AuthoritySnapshot(t *testing.T, s *store.Store, grant Grant) workflowIssue31AuthorityState {
	t.Helper()
	state := workflowIssue31AuthorityState{Projections: map[string]int{}}
	if err := s.DB().QueryRow(`SELECT version FROM work_items WHERE id='work-1'`).Scan(&state.Version); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT used_count FROM agent_grants WHERE grant_hash=?`, sha256Bytes([]byte(grant.Token))).Scan(&state.GrantUsed); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id='work-1'`).Scan(&state.Events); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT count(*) FROM workflow_actors`).Scan(&state.Actors); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT count(*) FROM agent_approval_challenges`).Scan(&state.Challenges); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT count(*) FROM agent_approvals`).Scan(&state.Approvals); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT instance_state,current_step FROM workflow_instances WHERE work_id='work-1'`).Scan(&state.InstanceState, &state.CurrentStep); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"workflow_instances", "workflow_contracts", "workflow_candidate_sets", "workflow_actors", "workflow_checkpoints", "workflow_external_conditions", "workflow_impact_edges", "workflow_impact_notices", "workflow_decision_records", "workflow_premise_confirmations"} {
		var count int
		if err := s.DB().QueryRow(fmt.Sprintf(`SELECT count(*) FROM %s`, table)).Scan(&count); err != nil {
			t.Fatal(err)
		}
		state.Projections[table] = count
	}
	state.ChallengeState = workflowIssue31Rows(t, s, `SELECT challenge_ref,status,used_count FROM agent_approval_challenges ORDER BY challenge_ref`)
	state.ApprovalState = workflowIssue31Rows(t, s, `SELECT approval_ref,used_count FROM agent_approvals ORDER BY approval_ref`)
	return state
}

func workflowIssue31Rows(t *testing.T, s *store.Store, query string) string {
	t.Helper()
	rows, err := s.DB().Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out strings.Builder
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(values))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		for _, value := range values {
			out.WriteString(fmt.Sprint(value))
			out.WriteByte('|')
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func countWorkflowActorRowsIssue31(t *testing.T, s *store.Store) int {
	t.Helper()
	var count int
	if err := s.DB().QueryRow(`SELECT count(*) FROM workflow_actors`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func prepareIssue31Confirm(t *testing.T) (*store.Store, *Service, Grant, ed25519.PrivateKey, CallEnvelope, json.RawMessage, string, string, map[string]any, map[string]any) {
	t.Helper()
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition"})
	if got := seedAgentWorkflow(t, s, grant); got != 4 {
		t.Fatalf("workflow seed version=%d, want 4", got)
	}
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	for _, actionID := range []string{"record_proposal", "record_discovery", "record_design"} {
		invokeWorkflowIssue31Action(t, s, service, env, "work-1", actionID, workflowIssue31Version(t, s), "prepare-"+actionID)
	}
	approve := json.RawMessage(`{"work_id":"work-1","expected_version":7,"action_id":"approve_contract","idempotency_key":"prepare-approve"}`)
	challenge := invokeWorkflowIssue31(t, s, service, env, "concord_work_transition", "workflow_action", approve)
	if challenge.Error == nil {
		t.Fatalf("missing contract approval challenge: %+v", challenge)
	}
	challengeRef, _ := challenge.Error.Details["approval_ref"].(string)
	scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion}
	approveDigest := mutationDigest("concord_work_transition", "workflow_action", env, approve)
	env.HostApproval = signedHostApproval(privateKey, challengeRef, approveDigest, scope, map[string]any{"work": 7}, grant.SessionRef, grant.AgentRef, grant.Worktree, grant.ClientVersion, fixedTime(), "prepare-approve-nonce")
	approved := json.RawMessage(`{"work_id":"work-1","expected_version":7,"action_id":"approve_contract","idempotency_key":"prepare-approve","approval":{"approval_ref":"` + challengeRef + `"}}`)
	if response := invokeWorkflowIssue31(t, s, service, env, "concord_work_transition", "workflow_action", approved); response.Outcome != OutcomeOK {
		t.Fatalf("contract approval response=%+v", response)
	}
	invokeWorkflowIssue31Action(t, s, service, env, "work-1", "start_execution", workflowIssue31Version(t, s), "prepare-start")
	if _, err := s.DB().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE workflow_instances SET current_step='acceptance' WHERE work_id='work-1'; DELETE FROM fold_guard WHERE active=1`); err != nil {
		t.Fatal(err)
	}
	evaluatorEnv := mutationEnvelope(issue31EvaluatorGrant(t, service, privateKey), scopeVersion)
	invokeWorkflowIssue31Action(t, s, service, evaluatorEnv, "work-1", "record_verdict", workflowIssue31Version(t, s), "prepare-verdict")
	confirm := issue31ConfirmInput(t, s, workflowIssue31Version(t, s), "prepare-confirm")
	challenge = invokeWorkflowIssue31(t, s, service, env, "concord_work_transition", "workflow_action", confirm)
	if challenge.Error == nil {
		t.Fatalf("missing premise challenge: %+v", challenge)
	}
	challengeRef, _ = challenge.Error.Details["approval_ref"].(string)
	digest := mutationDigest("concord_work_transition", "workflow_action", env, confirm)
	versions := map[string]any{"work": workflowIssue31Version(t, s), "contract": 1}
	return s, service, grant, privateKey, env, confirm, challengeRef, digest, scope, versions
}

func issue31EvaluatorGrant(t *testing.T, service *Service, privateKey ed25519.PrivateKey) Grant {
	t.Helper()
	request := grantRequest(privateKey, "issue31-evaluator-grant")
	request.Assertion.AgentRef = "agent-evaluator"
	request.Assertion.SessionRef = "session-evaluator"
	request.Assertion.RequestedCapabilities = []Capability{"work_transition"}
	request.Assertion.Signature = ed25519.Sign(privateKey, CanonicalAssertion(request.Assertion))
	grant, err := service.IssueGrant(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	return grant
}

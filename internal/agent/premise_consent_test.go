package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// The caller supplies an evidenced current question, not a seeded acceptance
// projection. Invoke owns host assertion validation before operator relabeling.
func assertPublicPremiseConsent(t *testing.T, s *store.Store, service *Service, env CallEnvelope, workID string) {
	t.Helper()
	question, err := readOperatorQuestionFromWorkPinForTest(context.Background(), s, workID)
	if err != nil || question == nil {
		t.Fatalf("operator question=%+v err=%v", question, err)
	}
	if question.ActionID != "confirm_premise" || question.Header != "Operator checkpoint" || question.AllowMultiple || question.AllowCustom || len(question.Choices) != 3 {
		t.Fatalf("operator question shape=%+v", question)
	}
	if question.Choices[0].ID != "confirm" || question.Choices[0].ActionID != "confirm_premise" || question.Choices[1].ActionID != "supersede_contract" || question.Choices[2].ActionID != "concord_work_transition.lifecycle" {
		t.Fatalf("operator question choices=%+v", question.Choices)
	}
	input := map[string]any{
		"work_id": workID, "expected_version": workVersion(t, s, workID), "action_id": "confirm_premise",
		"selected_choice": "confirm", "decision_context_digest": question.DecisionContextDigest, "idempotency_key": "research-premise-consent",
	}
	refuse := func(t *testing.T, actorEnv CallEnvelope, request map[string]any, kind string) {
		t.Helper()
		before := premiseConsentSnapshot(t, s)
		response := invokeWorkflowIssue31(t, s, service, actorEnv, retryJSON(request))
		if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != kind || response.Error.EffectState != "none" {
			t.Fatalf("premise refusal=%+v, want %s with no effect", response.Error, kind)
		}
		if after := premiseConsentSnapshot(t, s); !reflect.DeepEqual(before, after) {
			t.Fatalf("refused premise changed authority: before=%+v after=%+v", before, after)
		}
	}
	for _, vector := range []struct {
		name, kind string
		mutate     func(map[string]any)
	}{
		{"missing digest", "invalid_input", func(in map[string]any) { delete(in, "decision_context_digest") }},
		{"malformed digest", "invalid_input", func(in map[string]any) { in["decision_context_digest"] = "not-a-digest" }},
		{"stale digest", "stale_requires_review", func(in map[string]any) { in["decision_context_digest"] = "sha256:" + strings.Repeat("f", 64) }},
		{"revise uses its declared route", "invalid_transition", func(in map[string]any) { in["selected_choice"] = "revise" }},
		{"stop uses its declared route", "invalid_transition", func(in map[string]any) { in["selected_choice"] = "stop" }},
		{"payload cannot select operator", "invalid_input", func(in map[string]any) {
			in["fields"] = map[string]any{"confirming_actor_ref": "actor:payload-selected"}
		}},
	} {
		t.Run(vector.name, func(t *testing.T) {
			request := make(map[string]any, len(input))
			for key, value := range input {
				request[key] = value
			}
			vector.mutate(request)
			refuse(t, env, request, vector.kind)
		})
	}
	challenge := invokeWorkflowIssue31(t, s, service, env, retryJSON(input))
	if challenge.Outcome != OutcomeError || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("product premise did not require operator consent: %+v", challenge.Error)
	}
	ref, _ := challenge.Error.Details["approval_ref"].(string)
	summary := challenge.Error.ConsequenceSummary
	if ref == "" || summary == nil || summary.OperationDigest != mutationDigest("concord_work_transition", "workflow_action", env, retryJSON(input)) || !slices.Contains(summary.Versions, "work:"+strconv.FormatInt(workVersion(t, s, workID), 10)) {
		t.Fatalf("premise challenge lacks exact intent bindings: %+v", challenge.Error)
	}
	input["approval"] = map[string]any{"approval_ref": ref}
	t.Run("missing host assertion", func(t *testing.T) { refuse(t, env, input, "approval_invalid") })
	env.HostApproval = &HostApprovalAssertion{
		ChallengeRef: ref, RequestDigest: summary.OperationDigest, Scope: summary.Scope, Versions: summary.Versions,
		SessionRef: env.SessionRef, AgentRef: env.AgentRef, Worktree: env.Worktree, IssuedAt: fixedTime().Format(time.RFC3339Nano),
	}
	t.Run("wrong version assertion", func(t *testing.T) {
		wrong := *env.HostApproval
		wrong.Versions = slices.Clone(wrong.Versions)
		for i, binding := range wrong.Versions {
			if strings.HasPrefix(binding, "work:") {
				wrong.Versions[i] = "work:" + strconv.FormatInt(workVersion(t, s, workID)+1, 10)
			}
		}
		wrongEnv := env
		wrongEnv.HostApproval = &wrong
		refuse(t, wrongEnv, input, "approval_invalid")
	})
	// The host names no human. The authenticated registry and exact consent
	// determine the operator identity, including when the wire principal is empty.
	env.PrincipalRef = ""
	spineOK(t, invokeWorkflowIssue31(t, s, service, env, retryJSON(input)))
	var actorClass, confirmedBy, eventActor string
	if err := s.DatabaseForTesting().QueryRow(`SELECT a.actor_class,pc.confirmed_by,e.actor FROM workflow_premise_confirmations pc JOIN workflow_actors a ON a.actor_ref=pc.confirmed_by JOIN domain_events e ON e.subject_id=pc.work_id AND e.kind=? WHERE pc.work_id=?`, store.WorkflowPremiseConfirmed, workID).Scan(&actorClass, &confirmedBy, &eventActor); err != nil {
		t.Fatalf("read confirmed operator identity: %v", err)
	}
	if actorClass != string(store.ActorOperator) || confirmedBy != eventActor || confirmedBy == store.DeriveWorkflowActorRef(env.PrincipalRef, env.ClientRef, env.AgentRef, env.SessionRef) {
		t.Fatalf("confirmation did not derive operator identity: class=%s confirming=%s event=%s", actorClass, confirmedBy, eventActor)
	}
	var got int
	if err := s.DatabaseForTesting().QueryRow(`SELECT used_count FROM agent_approvals WHERE protected_evidence_ref=?`, "approval-challenge:"+ref).Scan(&got); err != nil {
		t.Fatalf("read consumed premise approval: %v", err)
	}
	if got != 1 {
		t.Fatalf("operator approval used %d times, want once", got)
	}
	before := premiseConsentSnapshot(t, s)
	replay := spineOK(t, invokeWorkflowIssue31(t, s, service, env, retryJSON(input)))
	if !replay.Replayed || !reflect.DeepEqual(before, premiseConsentSnapshot(t, s)) {
		t.Fatal("premise replay changed workflow, operator identity or consent consumption")
	}
}

func premiseConsentSnapshot(t *testing.T, s *store.Store) map[string]string {
	t.Helper()
	state := map[string]string{}
	queries := map[string]string{
		"versions":   `SELECT id,version FROM work_items ORDER BY id`,
		"instances":  `SELECT work_id,instance_state,current_step FROM workflow_instances ORDER BY work_id`,
		"actors":     `SELECT actor_ref,actor_class FROM workflow_actors ORDER BY actor_ref`,
		"challenges": `SELECT challenge_ref,status,used_count FROM agent_approval_challenges ORDER BY challenge_ref`,
		"approvals":  `SELECT approval_ref,used_count FROM agent_approvals ORDER BY approval_ref`,
	}
	for _, table := range []string{"domain_events", "workflow_instances", "workflow_contracts", "workflow_candidate_sets", "workflow_actors", "workflow_checkpoints", "workflow_external_conditions", "workflow_impact_edges", "workflow_impact_notices", "workflow_decision_records", "workflow_premise_confirmations"} {
		queries[table] = fmt.Sprintf(`SELECT count(*) FROM %s`, table)
	}
	for key, query := range queries {
		rows, err := s.DatabaseForTesting().Query(query)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		var out strings.Builder
		for rows.Next() {
			values, pointers := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			encoded, err := json.Marshal(values)
			if err != nil {
				rows.Close()
				t.Fatal(err)
			}
			out.Write(encoded)
			out.WriteByte('\n')
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		state[key] = out.String()
	}
	return state
}

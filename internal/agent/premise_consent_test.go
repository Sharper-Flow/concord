package agent

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// The coordinator records its own evidenced confirmation under its own identity.
func assertPublicPremiseConsent(t *testing.T, s *store.Store, service *Service, env CallEnvelope, workID string) {
	t.Helper()
	input := map[string]any{
		"work_id": workID, "expected_version": workVersion(t, s, workID), "action_id": "confirm_premise",
		"idempotency_key": "research-premise-consent",
	}
	refuse := func(t *testing.T, actorEnv CallEnvelope, request map[string]any, kind string) {
		t.Helper()
		before := premiseConsentSnapshot(t, s)
		response := invokeWorkflowIssue31(t, s, service, actorEnv, retryJSON(request))
		if response.Outcome != agentcontract.OutcomeError || response.Error == nil || response.Error.Kind != kind || response.Error.EffectState != "none" {
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
		{"choice is not an input", "invalid_input", func(in map[string]any) { in["selected_choice"] = "confirm" }},
		{"digest is not an input", "invalid_input", func(in map[string]any) { in["decision_context_digest"] = "sha256:" + strings.Repeat("f", 64) }},
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
	spineOK(t, invokeWorkflowIssue31(t, s, service, env, retryJSON(input)))
	var actorClass, confirmedBy, eventActor string
	if err := s.DatabaseForTesting().QueryRow(`SELECT a.actor_class,pc.confirmed_by,e.actor FROM workflow_premise_confirmations pc JOIN workflow_actors a ON a.actor_ref=pc.confirmed_by JOIN effect_receipts e ON e.subject_id=pc.work_id AND e.kind=? WHERE pc.work_id=?`, store.WorkflowPremiseConfirmed, workID).Scan(&actorClass, &confirmedBy, &eventActor); err != nil {
		t.Fatalf("read confirmed operator identity: %v", err)
	}
	if actorClass != string(store.ActorAgent) || confirmedBy != eventActor || confirmedBy != store.DeriveWorkflowActorRef(env.PrincipalRef, env.ClientRef, env.AgentRef, env.SessionRef) {
		t.Fatalf("confirmation did not keep caller identity: class=%s confirming=%s event=%s", actorClass, confirmedBy, eventActor)
	}
	before := premiseConsentSnapshot(t, s)
	replay := spineOK(t, invokeWorkflowIssue31(t, s, service, env, retryJSON(input)))
	if !replay.Replayed || !reflect.DeepEqual(before, premiseConsentSnapshot(t, s)) {
		t.Fatal("premise replay changed workflow, operator identity or confirmation state")
	}
}

func premiseConsentSnapshot(t *testing.T, s *store.Store) map[string]string {
	t.Helper()
	state := map[string]string{}
	queries := map[string]string{
		"versions":  `SELECT id,version FROM work_items ORDER BY id`,
		"instances": `SELECT work_id,instance_state,current_step FROM workflow_instances ORDER BY work_id`,
		"actors":    `SELECT actor_ref,actor_class FROM workflow_actors ORDER BY actor_ref`,
	}
	for _, table := range []string{"effect_receipts", "workflow_instances", "workflow_contracts", "workflow_candidate_sets", "workflow_actors", "workflow_checkpoints", "workflow_impact_edges", "workflow_decision_records", "workflow_premise_confirmations"} {
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

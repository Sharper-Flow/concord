package agent

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// CD-0116 at the agent surface: the session that delivered in-session submits
// its verdict, the surface mints the operator challenge instead of refusing
// blind, and the signed approval records the verdict with the operator as the
// verdict actor.
func TestOperatorVerdictChallengeAfterInSessionDelivery(t *testing.T) {
	ctx := context.Background()
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"product_read", "work_define", "work_transition"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	workID := captureCompositionWork(t, ctx, s, service, env, "Operator verdict challenge", "task", "workflow.generic_one_off", "operator-verdict-capture")

	signedAction := func(actionID string, fields map[string]any, key string) Envelope {
		t.Helper()
		var version int64
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
			t.Fatal(err)
		}
		input := map[string]any{"work_id": workID, "expected_version": version, "action_id": actionID, "fields": fields, "idempotency_key": key}
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		env.RequestID = "request:" + key + ":" + strconv.FormatInt(version, 10)
		response := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
		if response.Error != nil && response.Error.Kind == "approval_required" {
			challengeRef, _ := response.Error.Details["approval_ref"].(string)
			if challengeRef == "" {
				t.Fatalf("%s minted no challenge", actionID)
			}
			input["approval"] = map[string]any{"approval_ref": challengeRef}
			approvedRaw, _ := json.Marshal(input)
			scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{workID}, "scope_version": env.ScopeVersion}
			versions := map[string]any{"work": version}
			approvalEnv := env
			approvalEnv.HostApproval = signedHostApproval(privateKey, challengeRef, mutationDigest("concord_work_transition", "workflow_action", env, approvedRaw), scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), nonceForChallenge(challengeRef))
			response = dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approvedRaw}, approvalEnv)
		}
		return response
	}

	contract := signedAction("approve_contract", map[string]any{
		"premise": "The operator signs the verdict after an in-session delivery.", "contract_version": 1,
		"required_evidence": []string{"verification"}, "route_conventions": []string{},
		"outcome_predicates": []map[string]any{{"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:fixture", "immutable_subject_ref": "commit:fixture", "expected_result": "pass"}}},
	}, "operator-verdict-contract")
	if contract.Outcome != OutcomeOK {
		t.Fatalf("approve_contract refused: %+v", contract.Error)
	}
	if started := signedAction("start_action", map[string]any{"summary": "in-session step"}, "operator-verdict-start"); started.Outcome != OutcomeOK {
		t.Fatalf("start_action refused: %+v", started.Error)
	}
	if bound := signedAction("bind_evidence", map[string]any{"evidence_kind": "verification", "evidence_ref": "evidence:operator-challenge"}, "operator-verdict-bind"); bound.Outcome != OutcomeOK {
		t.Fatalf("bind_evidence refused: %+v", bound.Error)
	}
	if delivered := signedAction("record_delivery", map[string]any{"summary": "delivered in-session"}, "operator-verdict-delivery"); delivered.Outcome != OutcomeOK {
		t.Fatalf("record_delivery refused: %+v", delivered.Error)
	}

	// The verdict from the delivering session mints the operator challenge,
	// and the signed approval records the verdict.
	verdict := signedAction("record_verdict", map[string]any{
		"predicate_id": "predicate:primary", "verdict_kind": "ok", "evaluation_evidence": []string{"evidence:operator-challenge"},
	}, "operator-verdict-verdict")
	if verdict.Outcome != OutcomeOK {
		t.Fatalf("operator-signed verdict refused: %+v", verdict.Error)
	}
	var step string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	if step != "verify" && step != "acceptance" {
		t.Fatalf("current_step=%q after the verdict", step)
	}
	var verdictActor string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT json_extract(payload,'$.verdict_actor_ref') FROM domain_events WHERE subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, workID, store.WorkflowVerdictRecorded).Scan(&verdictActor); err != nil {
		t.Fatal(err)
	}
	if verdictActor == "" || verdictActor == store.DeriveWorkflowActorRef(grant.PrincipalRef, grant.ClientRef, grant.AgentRef, grant.SessionRef) {
		t.Fatalf("verdict actor=%q, want the operator identity", verdictActor)
	}
}

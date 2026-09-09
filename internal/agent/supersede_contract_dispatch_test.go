package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestSupersedeContractDispatchChallengesThenBindsApprovalOperator(t *testing.T) {
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition"})
	version := seedAgentWorkflow(t, s, grant)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)

	for _, actionID := range []string{"record_proposal", "record_discovery", "record_design"} {
		invokeWorkflowIssue31Action(t, s, service, env, "work-1", actionID, version, "supersede-"+actionID)
		version = workflowIssue31Version(t, s)
	}
	contract, version := approvedOpsAction(t, s, service, grant, privateKey, env, version, "approve_contract", workflowContractFieldsFixture(), "supersede-approve-contract")
	if contract.Outcome != OutcomeOK {
		t.Fatalf("approve_contract=%+v", contract.Error)
	}
	invokeWorkflowIssue31Action(t, s, service, env, "work-1", "start_execution", version, "supersede-start-execution")
	version = workflowIssue31Version(t, s)
	invokeWorkflowIssue31Action(t, s, service, env, "work-1", "bind_evidence", version, "supersede-bind-evidence")
	version = workflowIssue31Version(t, s)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE workflow_instances SET current_step='acceptance' WHERE work_id='work-1'; DELETE FROM fold_guard WHERE active=1`); err != nil {
		t.Fatal(err)
	}
	evaluatorEnv := mutationEnvelope(issue31EvaluatorGrant(t, service, privateKey), scopeVersion)
	invokeWorkflowIssue31Action(t, s, service, evaluatorEnv, "work-1", "record_verdict", version, "supersede-record-verdict")
	version = workflowIssue31Version(t, s)

	fields := map[string]any{
		"contract_version":     2,
		"premise":              "corrected premise",
		"architecture_binding": workflowArchitectureBindingFixture(),
		"outcome_predicates": []map[string]any{{
			"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check",
			"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:workflow", "expected_result": "pass"},
		}},
		"required_evidence": []string{"verification"},
		"route_conventions": []string{},
		"spec_mandate":      []string{},
		"law_modifies":      []string{},
		"rigor_class":       "prototype_internal",
		"supersede_reason":  "correct the accepted premise",
		"audit_evidence":    []string{"evidence:supersede-dispatch"},
	}
	input := map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "supersede_contract", "fields": fields, "idempotency_key": "supersede-contract-dispatch"}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	challenge := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if challenge.Outcome != OutcomeError || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("challenge=%+v", challenge)
	}
	challengeRef, ok := challenge.Error.Details["approval_ref"].(string)
	if !ok || len(challengeRef) != 64 {
		t.Fatalf("approval challenge ref=%v", challenge.Error.Details["approval_ref"])
	}

	approved := input
	approved["approval"] = map[string]any{"approval_ref": challengeRef}
	approvedRaw, err := json.Marshal(approved)
	if err != nil {
		t.Fatal(err)
	}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, mutationDigest("concord_work_transition", "workflow_action", env, approvedRaw), map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion}, map[string]any{"work": version, "contract": 1}, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), "supersede-contract-approval")
	response := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approvedRaw}, env)
	if response.Outcome != OutcomeOK {
		t.Fatalf("approved supersede=%+v", response.Error)
	}
	var activeVersion int
	if err := s.DatabaseForTesting().QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id='work-1' AND superseded_by IS NULL`).Scan(&activeVersion); err != nil {
		t.Fatal(err)
	}
	if activeVersion != 2 {
		t.Fatalf("active contract version=%d, want 2", activeVersion)
	}
	var actorClass string
	if err := s.DatabaseForTesting().QueryRow(`SELECT a.actor_class FROM workflow_contracts c JOIN workflow_actors a ON a.actor_ref=c.approved_by WHERE c.work_id='work-1' AND c.contract_version=2`).Scan(&actorClass); err != nil {
		t.Fatal(err)
	}
	if actorClass != string(store.ActorOperator) {
		t.Fatalf("successor approval actor class=%q, want operator", actorClass)
	}
}

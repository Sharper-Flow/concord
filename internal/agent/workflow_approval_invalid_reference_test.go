package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// An approval reference that names no issued approval or challenge is an
// invalid approval (workflow-engine contract §11 step 4): the workflow action
// refuses with approval_invalid and request_approval, and nothing changes.
func TestWorkflowActionRefusesAnInvalidApprovalReference(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name    string
		ref     string
		message string
	}{
		{"malformed reference", "approval-ref-not-64-characters", "approval reference malformed"},
		{"non-hex reference", strings.Repeat("zz", 32), "approval reference malformed"},
		{"unissued reference", strings.Repeat("ab", 32), "approval not found"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition"})
			if got := seedAgentWorkflow(t, s, grant); got != 4 {
				t.Fatalf("workflow seed version=%d, want 4", got)
			}
			ctx := context.Background()
			scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
			if err != nil {
				t.Fatal(err)
			}
			env := mutationEnvelope(grant, scopeVersion)
			if got := advanceCapturedWorkToContract(t, s, service, env, "work-1", "wf-advance"); got != 8 {
				t.Fatalf("pre-contract version=%d, want 8", got)
			}
			input := workflowContractActionInput(t, "wf-approve-invalid-ref", testCase.ref)
			digest := mutationDigest("concord_work_transition", "workflow_action", env, input)
			scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion}
			env.HostApproval = signedHostApproval(privateKey, testCase.ref, digest, scope, map[string]any{"work": 8}, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), "workflow-invalid-ref-0001")
			before := agentDurableStateSnapshot(t, s)
			response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: input}, env)
			if err != nil || response.Outcome != OutcomeError || response.Error == nil {
				t.Fatalf("invalid approval reference response=%+v err=%v", response, err)
			}
			if response.Error.Kind != "approval_invalid" || response.Error.RecoveryAction.Kind != "request_approval" || response.Error.Message != testCase.message || response.Error.RetrySafe {
				t.Fatalf("invalid approval reference refusal kind=%q recovery=%q message=%q retry_safe=%v, want approval_invalid/request_approval/%q/false", response.Error.Kind, response.Error.RecoveryAction.Kind, response.Error.Message, response.Error.RetrySafe, testCase.message)
			}
			if after := agentDurableStateSnapshot(t, s); after != before {
				t.Fatalf("invalid approval reference changed durable state:\nbefore %s\nafter  %s", before, after)
			}
		})
	}
}

// agentDurableStateSnapshot reads every table an approval consumption or an
// approval-gated mutation can change, including the idempotency record and
// the trusted-client policy.
func agentDurableStateSnapshot(t *testing.T, s *store.Store) string {
	t.Helper()
	var out []string
	for _, query := range []string{
		`SELECT (SELECT count(*) FROM effect_receipts) || ':' || last FROM store_sequences WHERE name='effect'`,
		`SELECT count(*) FROM durable_operations`,
		`SELECT count(*) || ':' || coalesce(sum(version), 0) FROM work_items`,
		`SELECT coalesce(group_concat(work_id || '=' || current_step, ','), '') FROM (SELECT work_id, current_step FROM workflow_instances ORDER BY work_id)`,
		`SELECT count(*) || ':' || coalesce(sum(replayed_count), 0) FROM idempotency_records`,
		`SELECT count(*) || ':' || coalesce(sum(used_count), 0) FROM agent_approvals`,
		`SELECT count(*) || ':' || coalesce(sum(used_count), 0) || ':' || coalesce(group_concat(status, ','), '') FROM (SELECT used_count, status FROM agent_approval_challenges ORDER BY challenge_ref)`,
		`SELECT coalesce(group_concat(client_ref || '|' || capabilities_json || '|' || product_scope_json || '|' || project_scope_json, ','), '') FROM (SELECT * FROM agent_clients ORDER BY client_ref)`,
	} {
		var value string
		if err := s.DatabaseForTesting().QueryRow(query).Scan(&value); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		out = append(out, value)
	}
	return strings.Join(out, " ")
}

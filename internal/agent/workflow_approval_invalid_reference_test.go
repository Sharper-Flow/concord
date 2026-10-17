package agent

import (
	"context"
	"strconv"
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
			advanceAgentWorkflowToContractApproval(t, s, service, env)
			input := workflowContractActionInput(t, "wf-approve-invalid-ref", testCase.ref)
			digest := mutationDigest("concord_work_transition", "workflow_action", env, input)
			scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion}
			env.HostApproval = signedHostApproval(privateKey, testCase.ref, digest, scope, map[string]any{"work": 10}, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), "workflow-invalid-ref-0001")
			before := agentDurableStateSnapshot(t, s)
			response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: input}, env)
			if err != nil || response.Outcome != OutcomeError || response.Error == nil {
				t.Fatalf("invalid approval reference response=%+v err=%v", response, err)
			}
			if response.Error.Kind != "approval_invalid" || response.Error.RecoveryAction.Kind != "request_approval" || response.Error.Message != testCase.message {
				t.Fatalf("invalid approval reference refusal kind=%q recovery=%q message=%q, want approval_invalid/request_approval/%q", response.Error.Kind, response.Error.RecoveryAction.Kind, response.Error.Message, testCase.message)
			}
			if after := agentDurableStateSnapshot(t, s); after != before {
				t.Fatalf("invalid approval reference changed durable state:\nbefore %s\nafter  %s", before, after)
			}
		})
	}
}

// advanceAgentWorkflowToContractApproval walks the seeded implementation
// workflow through proposal, alignment, and design so approve_contract is the
// next action.
func advanceAgentWorkflowToContractApproval(t *testing.T, s *store.Store, service *Service, env CallEnvelope) {
	t.Helper()
	for i, action := range []string{"record_proposal", "record_alignment", "record_design"} {
		fields := map[string]string{
			"record_proposal":  `,"fields":{"problem":"The bounded problem statement.","affected":["The affected system."],"stakes":"The bounded stakes statement.","user_outcomes":["The expected user outcome."]}`,
			"record_alignment": `,"fields":{"searched":"The bounded backlog search statement.","outcome":"none_found"}`,
			"record_design":    `,"fields":{"approach":"The recorded approach is the implementation boundary.","decisions":[{"id":"decision:dispatch","question":"What crosses into execution?","choice":"The typed design record.","rationale":"The worker must receive the approved decision.","rejected":[]}],"touched_refs":["path:dispatch"]}`,
		}[action]
		expectedVersion := []int{4, 6, 8}[i]
		input := []byte(`{"work_id":"work-1","expected_version":` + strconv.Itoa(expectedVersion) + `,"action_id":"` + action + `"` + fields + `,"idempotency_key":"wf-advance-` + action + `"}`)
		response, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: input}, env)
		if err != nil || response.Outcome != OutcomeOK {
			t.Fatalf("advance action=%s response=%+v err=%v", action, response, err)
		}
	}
}

// agentDurableStateSnapshot reads every table an approval consumption or a
// workflow action can change.
func agentDurableStateSnapshot(t *testing.T, s *store.Store) string {
	t.Helper()
	var out []string
	for _, query := range []string{
		`SELECT count(*) || ':' || coalesce(max(seq), 0) FROM domain_events`,
		`SELECT count(*) FROM durable_operations`,
		`SELECT version FROM work_items WHERE id='work-1'`,
		`SELECT current_step FROM workflow_instances WHERE work_id='work-1'`,
		`SELECT count(*) || ':' || coalesce(sum(used_count), 0) FROM agent_approvals`,
		`SELECT count(*) || ':' || coalesce(sum(used_count), 0) FROM agent_approval_challenges`,
	} {
		var value string
		if err := s.DatabaseForTesting().QueryRow(query).Scan(&value); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		out = append(out, value)
	}
	return strings.Join(out, " ")
}

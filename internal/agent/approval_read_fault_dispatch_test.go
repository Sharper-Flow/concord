package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// approvalConsumingCall is one approval-gated operation, set up so its first
// dispatch mints an approval challenge.
type approvalConsumingCall struct {
	name  string
	setup func(t *testing.T) (*store.Store, *Service, CallEnvelope, InvokeRequest)
}

// A store fault while the core reads the approval it is consuming is not an
// invalid approval: every caller renders it by its store kind, retry-safe,
// and nothing changes.
func TestApprovedCallRendersAnApprovalReadFaultByItsStoreKind(t *testing.T) {
	t.Parallel()
	for _, call := range []approvalConsumingCall{
		{"workflow action", workflowActionApprovalCall},
		{"knowledge publication", knowledgePublicationApprovalCall},
	} {
		t.Run(call.name, func(t *testing.T) {
			t.Parallel()
			s, service, env, request := call.setup(t)
			response, before, after := dispatchApprovedCallWithFault(t, s, service, env, request, `ALTER TABLE agent_approvals RENAME COLUMN revoked_at TO revoked_at_unreadable`, `ALTER TABLE agent_approvals RENAME COLUMN revoked_at_unreadable TO revoked_at`)
			if response.Outcome != OutcomeError || response.Error == nil {
				t.Fatalf("approval read fault response=%+v", response)
			}
			if response.Error.Kind != "unreachable" || response.Error.RecoveryAction.Kind != "contact_operator" || !response.Error.RetrySafe || !strings.HasPrefix(response.Error.Message, "cannot read approval: ") {
				t.Fatalf("approval read fault refusal kind=%q recovery=%q retry_safe=%v message=%q, want unreachable/contact_operator/true/\"cannot read approval: ...\"", response.Error.Kind, response.Error.RecoveryAction.Kind, response.Error.RetrySafe, response.Error.Message)
			}
			if after != before {
				t.Fatalf("approval read fault changed durable state:\nbefore %s\nafter  %s", before, after)
			}
		})
	}
}

// dispatchApprovedCallWithFault mints the challenge, answers it the way the
// host does from the typed consequence summary, and dispatches the approved
// call while fault is applied. It returns the response and the durable state
// before and after the approved call.
func dispatchApprovedCallWithFault(t *testing.T, s *store.Store, service *Service, env CallEnvelope, request InvokeRequest, fault, restore string) (Envelope, string, string) {
	t.Helper()
	ctx := context.Background()
	challenge, err := Dispatch(ctx, s, service, request, env)
	if err != nil || challenge.Error == nil || challenge.Error.Kind != "approval_required" || challenge.Error.ConsequenceSummary == nil {
		t.Fatalf("approval challenge response=%+v err=%v", challenge, err)
	}
	ref, _ := challenge.Error.Details["approval_ref"].(string)
	summary := challenge.Error.ConsequenceSummary
	env.HostApproval = &HostApprovalAssertion{ChallengeRef: ref, RequestDigest: summary.OperationDigest, Scope: summary.Scope, Versions: summary.Versions, SessionRef: env.SessionRef, AgentRef: env.AgentRef, Worktree: env.Worktree, IssuedAt: fixedTime().Format(time.RFC3339Nano)}
	approved, err := injectApproval(request.Input, ref)
	if err != nil {
		t.Fatal(err)
	}
	request.Input = approved
	before := agentDurableStateSnapshot(t, s)
	if _, err := s.DatabaseForTesting().Exec(fault); err != nil {
		t.Fatal(err)
	}
	response, err := Dispatch(ctx, s, service, request, env)
	if _, restoreErr := s.DatabaseForTesting().Exec(restore); restoreErr != nil {
		t.Fatal(restoreErr)
	}
	if err != nil {
		t.Fatalf("approved dispatch err=%v", err)
	}
	return response, before, agentDurableStateSnapshot(t, s)
}

func workflowActionApprovalCall(t *testing.T) (*store.Store, *Service, CallEnvelope, InvokeRequest) {
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
	if got := seedAgentWorkflow(t, s, grant); got != 4 {
		t.Fatalf("workflow seed version=%d, want 4", got)
	}
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	if got := advanceCapturedWorkToContract(t, s, service, env, "work-1", "wf-advance"); got != 10 {
		t.Fatalf("pre-contract version=%d, want 10", got)
	}
	return s, service, env, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: workflowContractActionInput(t, "wf-approve-read-fault", "")}
}

func knowledgePublicationApprovalCall(t *testing.T) (*store.Store, *Service, CallEnvelope, InvokeRequest) {
	s, service, grant, _, home := agentJobsCompactionFixture(t)
	publication := newCancelledNotePublication(t, s, grant, home, "compact-cancelled-read-fault")
	return s, service, publication.env, publication.request()
}

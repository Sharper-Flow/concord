package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/agentcontract"
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
	for _, call := range approvalConsumingCalls() {
		t.Run(call.name, func(t *testing.T) {
			t.Parallel()
			s, service, env, request := call.setup(t)
			response, before, after := dispatchApprovedCallWithFault(t, s, service, env, request, time.Time{}, `ALTER TABLE agent_approvals RENAME COLUMN revoked_at TO revoked_at_unreadable`, `ALTER TABLE agent_approvals RENAME COLUMN revoked_at_unreadable TO revoked_at`)
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

// A late answer whose challenge re-arm loses its race is a coordination
// conflict, not an invalid approval: the approval still stands, so a reread
// and retry succeeds. Every caller renders the store's operation_conflict.
func TestApprovedCallRendersAChallengeRearmConflictAsOperationConflict(t *testing.T) {
	t.Parallel()
	for _, call := range approvalConsumingCalls() {
		t.Run(call.name, func(t *testing.T) {
			t.Parallel()
			s, service, env, request := call.setup(t)
			response, before, after := dispatchApprovedCallWithFault(t, s, service, env, request, fixedTime().Add(11*time.Minute), `CREATE TRIGGER challenge_rearm_lost_race BEFORE UPDATE OF expires_at ON agent_approval_challenges BEGIN SELECT RAISE(IGNORE); END`, `DROP TRIGGER challenge_rearm_lost_race`)
			if response.Outcome != OutcomeError || response.Error == nil {
				t.Fatalf("re-arm conflict response=%+v", response)
			}
			if response.Error.Kind != "operation_conflict" || response.Error.RecoveryAction.Kind != "reconcile_operation" || response.Error.RetrySafe || response.Error.Message != "approval challenge re-arm lost race" {
				t.Fatalf("re-arm conflict refusal kind=%q recovery=%q retry_safe=%v message=%q, want operation_conflict/reconcile_operation/false/\"approval challenge re-arm lost race\"", response.Error.Kind, response.Error.RecoveryAction.Kind, response.Error.RetrySafe, response.Error.Message)
			}
			if after != before {
				t.Fatalf("re-arm conflict changed durable state:\nbefore %s\nafter  %s", before, after)
			}
		})
	}
}

// An approval reference without the signed host assertion that answers it
// is an invalid approval on every route, never an internal fault.
func TestApprovedCallWithoutAHostAssertionRefusesApprovalInvalid(t *testing.T) {
	t.Parallel()
	s, service, env, request := workflowActionApprovalCall(t)
	input, err := injectApproval(request.Input, strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	request.Input = input
	before := agentDurableStateSnapshot(t, s)
	response, err := Dispatch(context.Background(), s, service, request, env)
	if err != nil || response.Outcome != OutcomeError || response.Error == nil {
		t.Fatalf("missing host assertion response=%+v err=%v", response, err)
	}
	if response.Error.Kind != "approval_invalid" || response.Error.RecoveryAction.Kind != "request_approval" || response.Error.RetrySafe || response.Error.Message != "signed host approval assertion is required" {
		t.Fatalf("missing host assertion refusal kind=%q recovery=%q retry_safe=%v message=%q, want approval_invalid/request_approval/false", response.Error.Kind, response.Error.RecoveryAction.Kind, response.Error.RetrySafe, response.Error.Message)
	}
	if after := agentDurableStateSnapshot(t, s); after != before {
		t.Fatalf("missing host assertion changed durable state:\nbefore %s\nafter  %s", before, after)
	}
}

// CD-0243 D3/D7 retire ordinary publication, retirement, and membership
// consent. Keep approval fault coverage on the surviving consumers.
func approvalConsumingCalls() []approvalConsumingCall {
	return []approvalConsumingCall{
		{"workflow action", workflowActionApprovalCall},
		{"product project link", productProjectLinkApprovalCall},
		{"client policy grant", clientPolicyGrantApprovalCall},
	}
}

func TestOrdinaryCoordinationDoesNotReadApprovalRecords(t *testing.T) {
	for _, call := range []approvalConsumingCall{
		{"knowledge publication", knowledgePublicationApprovalCall},
		{"research retirement", researchRetirementApprovalCall},
		{"work memberships", workMembershipsApprovalCall},
	} {
		t.Run(call.name, func(t *testing.T) {
			s, service, env, request := call.setup(t)
			execFixtureSQL(t, s, `ALTER TABLE agent_approvals RENAME COLUMN revoked_at TO revoked_at_unreadable`)
			response, err := Dispatch(context.Background(), s, service, request, env)
			execFixtureSQL(t, s, `ALTER TABLE agent_approvals RENAME COLUMN revoked_at_unreadable TO revoked_at`)
			if err != nil || response.Outcome != OutcomeOK {
				t.Fatalf("ordinary call error=%+v err=%v", response.Error, err)
			}
			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`); got != 0 {
				t.Fatalf("ordinary call minted %d approval challenges", got)
			}
		})
	}
}

// dispatchApprovedCallWithFault mints the challenge, answers it the way the
// host does from the typed consequence summary, and dispatches the approved
// call while fault is applied. A non-zero answerAt answers after the
// challenge deadline. It returns the response and the durable state before
// and after the approved call.
func dispatchApprovedCallWithFault(t *testing.T, s *store.Store, service *Service, env CallEnvelope, request InvokeRequest, answerAt time.Time, fault, restore string) (Envelope, string, string) {
	t.Helper()
	ctx := context.Background()
	challenge, err := Dispatch(ctx, s, service, request, env)
	if err != nil || challenge.Error == nil || challenge.Error.Kind != "approval_required" || challenge.Error.ConsequenceSummary == nil {
		t.Fatalf("approval challenge response=%+v err=%v", challenge, err)
	}
	ref, _ := challenge.Error.Details["approval_ref"].(string)
	summary := challenge.Error.ConsequenceSummary
	if answerAt.IsZero() {
		answerAt = fixedTime()
	} else {
		service.Now = func() time.Time { return answerAt }
	}
	env.HostApproval = &HostApprovalAssertion{ChallengeRef: ref, RequestDigest: summary.OperationDigest, Scope: summary.Scope, Versions: summary.Versions, SessionRef: env.SessionRef, AgentRef: env.AgentRef, Worktree: env.Worktree, IssuedAt: answerAt.Format(time.RFC3339Nano)}
	approved, err := injectApproval(request.Input, ref)
	if err != nil {
		t.Fatal(err)
	}
	request.Input = approved
	before := agentDurableStateSnapshot(t, s)
	execFixtureSQL(t, s, fault)
	response, err := Dispatch(ctx, s, service, request, env)
	execFixtureSQL(t, s, restore)
	if err != nil {
		t.Fatalf("approved dispatch err=%v", err)
	}
	return response, before, agentDurableStateSnapshot(t, s)
}

func workflowActionApprovalCall(t *testing.T) (*store.Store, *Service, CallEnvelope, InvokeRequest) {
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})
	if got := seedAgentWorkflow(t, s, grant); got != 4 {
		t.Fatalf("workflow seed version=%d, want 4", got)
	}
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	if got := advanceCapturedWorkToContract(t, s, service, env, "work-1", "wf-advance"); got != 8 {
		t.Fatalf("pre-contract version=%d, want 8", got)
	}
	return s, service, env, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: workflowContractActionInput(t, s, "wf-approve-read-fault", "")}
}

func knowledgePublicationApprovalCall(t *testing.T) (*store.Store, *Service, CallEnvelope, InvokeRequest) {
	s, service, grant, _, home := agentJobsCompactionFixture(t)
	publication := newCancelledNotePublication(t, s, grant, home, "compact-cancelled-read-fault")
	return s, service, publication.env, publication.request()
}

func researchRetirementApprovalCall(t *testing.T) (*store.Store, *Service, CallEnvelope, InvokeRequest) {
	s, service, grant, candidates := researchRetireFixture(t, []agentcontract.Capability{"product_read", "research"})
	ctx := context.Background()
	// Exercise cross-Product retirement as ordinary coordination.
	event := store.Event{EventID: "cross-retire-owner", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "retire-owner-0", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"},{"project_id":"project-2","role":"secondary"}],"expected_version":3,"resulting_version":4}`)}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{event}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "retire-owner-0"): 3}}); err != nil {
		t.Fatal(err)
	}
	if err := service.UpdateTrustedClientPolicy(ctx, "client-1", TrustedClientPolicy{PrincipalRef: "human-1", Capabilities: []agentcontract.Capability{"product_read", "research", "cross_scope"}, ProductScope: []string{"product-1", "product-2"}, ProjectScope: []string{"project-1", "project-2"}, AgentScope: testFixtureAgents}); err != nil {
		t.Fatal(err)
	}
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(map[string]any{"product_id": "product-1", "candidates": candidates[:1], "dry_run": false, "idempotency_key": "retire-read-fault"})
	if err != nil {
		t.Fatal(err)
	}
	return s, service, mutationEnvelope(grant, scopeVersion), InvokeRequest{Tool: "concord_work_define", Operation: "research_retire", Input: input}
}

func productProjectLinkApprovalCall(t *testing.T) (*store.Store, *Service, CallEnvelope, InvokeRequest) {
	s, service, grant, _ := productProjectLinkFixture(t, []agentcontract.Capability{"work_relate", "cross_scope"})
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	return s, service, mutationEnvelope(grant, scopeVersion), InvokeRequest{Tool: "concord_work_relate", Operation: "product_project_add", Input: productProjectLinkRequest("", "project-2")}
}

func clientPolicyGrantApprovalCall(t *testing.T) (*store.Store, *Service, CallEnvelope, InvokeRequest) {
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_relate"})
	return s, service, grantRequestEnvelope(t, s, grant), InvokeRequest{Tool: "concord_work_relate", Operation: "client_policy_grant_request", Input: json.RawMessage(grantRequestInput(""))}
}

func workMembershipsApprovalCall(t *testing.T) (*store.Store, *Service, CallEnvelope, InvokeRequest) {
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_relate"})
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	return s, service, mutationEnvelope(grant, scopeVersion), InvokeRequest{Tool: "concord_work_relate", Operation: "set_memberships", Input: json.RawMessage(`{"work_id":"work-1","expected_version":2,"memberships":[{"project_id":"project-1","role":"primary"}],"idempotency_key":"membership-read-fault"}`)}
}

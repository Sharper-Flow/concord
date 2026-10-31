package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// A takeover opens the next epoch under the new session's authenticated identity.

type takeoverFixture struct {
	s          *store.Store
	service    *Service
	owner      CallEnvelope
	ownerGrant Authority
	other      CallEnvelope
	otherGrant Authority
}

func newTakeoverFixture(t *testing.T) takeoverFixture {
	t.Helper()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})
	seedAgentWorkflow(t, s, grant)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	owner, _ := advanceWorkflowBoundaryToExecution(t, s, service, mutationEnvelope(grant, scopeVersion))
	if err := service.RegisterTrustedClient(ctx, testClientRegistration("client-2", "human-2", []agentcontract.Capability{"work_transition"}, []string{"product-1"}, []string{"project-1"})); err != nil {
		t.Fatal(err)
	}
	otherGrant, err := service.Authorize(ctx, Invocation{ClientRef: "client-2", PrincipalRef: "human-2", SessionRef: "session-2", AgentRef: "agent-other", Directory: "/repo", Worktree: "/repo-wt", ManifestDigest: agentcontract.ManifestDigest, RequiredCapability: "work_transition", ProductID: "product-1", ProjectID: "project-1"})
	if err != nil {
		t.Fatal(err)
	}
	other := mutationEnvelope(otherGrant, scopeVersion)
	other.RequestID = "other-request"
	return takeoverFixture{s: s, service: service, owner: owner, ownerGrant: grant, other: other, otherGrant: otherGrant}
}

func (f takeoverFixture) action(t *testing.T, env CallEnvelope, actionID, key string) agentcontract.Envelope {
	t.Helper()
	input := map[string]any{"work_id": "work-1", "expected_version": workVersion(t, f.s, "work-1"), "action_id": actionID, "fields": map[string]any{}, "idempotency_key": key}
	return invokeWorkflowBoundary(t, f.s, f.service, env, input, store.BuiltinWorkflowRegistry())
}

func latestExecutionStart(t *testing.T, s *store.Store) (int64, string) {
	t.Helper()
	var epoch int64
	var actor string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(latest_start_payload,'$.attempt_epoch'),json_extract(latest_start_payload,'$.actor_ref') FROM workflow_step_epochs WHERE step_id='execution' ORDER BY latest_start_seq DESC LIMIT 1`).Scan(&epoch, &actor); err != nil {
		t.Fatal(err)
	}
	return epoch, actor
}

func actorRefOf(grant Authority) string {
	return store.DeriveWorkflowActorRef(grant.PrincipalRef, grant.ClientRef, grant.AgentRef, grant.SessionRef)
}

func TestStartExecutionTakeoverKeepsEpochWriterAuthority(t *testing.T) {
	t.Parallel()
	f := newTakeoverFixture(t)
	if first := f.action(t, f.owner, "start_execution", "owner-start"); first.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("owner start_execution: %+v", first.Error)
	}
	response := f.action(t, f.other, "start_execution", "other-start")
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("approved takeover: %+v", response.Error)
	}
	if epoch, actor := latestExecutionStart(t, f.s); epoch != 2 || actor != actorRefOf(f.otherGrant) {
		t.Fatalf("approved takeover epoch %d actor %s, want epoch 2 held by the taker", epoch, actor)
	}
	// Criterion 21: the superseded holder's epoch no longer writes.
	if stale := f.action(t, f.owner, "checkpoint_execution", "owner-stale-checkpoint"); stale.Error == nil || stale.Error.Kind != "unauthorized" {
		t.Fatalf("superseded holder checkpoint = %+v, want unauthorized", stale.Error)
	}
	if current := f.action(t, f.other, "checkpoint_execution", "taker-checkpoint"); current.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("taker checkpoint: %+v", current.Error)
	}
}

// The approved takeover rereads the held attempt inside the action
// transaction: a change after the boundary read refuses the approval rather
// than letting it authorize a different holder's attempt.
func TestStartExecutionTakeoverFencesHeldAttemptChangeInsideTransaction(t *testing.T) {
	t.Parallel()
	f := newTakeoverFixture(t)
	if first := f.action(t, f.owner, "start_execution", "owner-start"); first.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("owner start_execution: %+v", first.Error)
	}
	input := map[string]any{"work_id": "work-1", "expected_version": workVersion(t, f.s, "work-1"), "action_id": "start_execution", "fields": map[string]any{}, "idempotency_key": "other-start"}
	originalProbe := f.service.ProjectHostProber
	probes := 0
	f.service.ProjectHostProber = func(ctx context.Context, directory, worktree string) (store.ResolvedProjectHost, error) {
		probes++
		// The third host probe falls between the boundary takeover read and
		// the action transaction. Change the held workflow epoch without
		// changing the work version, so only the takeover fence can refuse it.
		if probes == 3 {
			execProjection(t, f.s, []string{
				`UPDATE workflow_step_epochs SET attempt_epoch=2,latest_start_payload=json_set(latest_start_payload,'$.attempt_epoch',2) WHERE work_id='work-1' AND step_id='execution'`,
			}, [][]any{nil})
		}
		return originalProbe(ctx, directory, worktree)
	}
	response := invokeWorkflowBoundary(t, f.s, f.service, f.other, input, store.BuiltinWorkflowRegistry())
	f.service.ProjectHostProber = originalProbe
	if probes < 3 {
		t.Fatalf("the mutation probed the host %d times, want the third probe to change the held epoch", probes)
	}
	if response.Error == nil || response.Error.Kind != "operation_conflict" || !strings.Contains(response.Error.Message, "held attempt changed") {
		t.Fatalf("takeover after the holder changed mid-flight = outcome %s error %+v, want the held-attempt fence", response.Outcome, response.Error)
	}
	if epoch, actor := latestExecutionStart(t, f.s); epoch != 2 || actor != actorRefOf(f.ownerGrant) {
		t.Fatalf("fenced takeover moved the attempt: epoch %d actor %s", epoch, actor)
	}
}

func TestStartExecutionHolderRestartNeedsNoApproval(t *testing.T) {
	t.Parallel()
	f := newTakeoverFixture(t)
	if first := f.action(t, f.owner, "start_execution", "owner-start"); first.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("owner start_execution: %+v", first.Error)
	}
	if restart := f.action(t, f.owner, "start_execution", "owner-restart"); restart.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("holder restart = %+v, want ok without approval", restart.Error)
	}
	if epoch, actor := latestExecutionStart(t, f.s); epoch != 2 || actor != actorRefOf(f.ownerGrant) {
		t.Fatalf("holder restart epoch %d actor %s, want epoch 2", epoch, actor)
	}
}

func TestStartExecutionTakeoverAfterHolderRestartNeedsNoApproval(t *testing.T) {
	t.Parallel()
	f := newTakeoverFixture(t)
	if first := f.action(t, f.owner, "start_execution", "owner-start"); first.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("owner start_execution: %+v", first.Error)
	}
	if restart := f.action(t, f.owner, "start_execution", "owner-restart"); restart.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("holder restart: %+v", restart.Error)
	}
	if handoff := f.action(t, f.other, "start_execution", "other-start"); handoff.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("takeover after the holder restarted = %+v, want ok without approval", handoff.Error)
	}
	if epoch, actor := latestExecutionStart(t, f.s); epoch != 3 || actor != actorRefOf(f.otherGrant) {
		t.Fatalf("handoff epoch %d actor %s, want epoch 3 held by the next session", epoch, actor)
	}
}

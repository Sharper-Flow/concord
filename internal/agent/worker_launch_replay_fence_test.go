package agent

import (
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
)

func TestOrdinaryRetryReplayPreservesUnknownLaunchFence(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition", "worker_dispatch"})
	_, failedID, grantWorktree := seedFailedWorkerRetryMutation(t, s, service, grant)
	grant.Worktree = grantWorktree
	env := mutationEnvelope(grant, mustScopeVersion(t, s))
	correction := verifyWallPinCorrection(t, s)
	if correction.FailedAttemptID != failedID || correction.Disposition != "failed" {
		t.Fatalf("initial correction = %+v, want the exact failed worker", correction)
	}
	request := func(attempt string, version int64) InvokeRequest {
		return InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{
			"work_id": "work-1", "expected_version": version, "action_id": "dispatch_worker", "idempotency_key": attempt,
			"fields": map[string]any{"attempt_id": attempt, "worker_packet": retryMutationPacket(t, s, attempt, correction)},
		})}
	}
	const attempt = "attempt:work-1:unknown-launch"
	launch := request(attempt, workVersion(t, s, "work-1"))
	if result := dispatchMutation(t, s, service, launch, env); result.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("ordinary retry = %+v", result.Error)
	}
	version := workVersion(t, s, "work-1")
	events := agentCurrentEffects(t, s)
	if result := dispatchMutation(t, s, service, launch, env); result.Outcome != agentcontract.OutcomeOK || !result.Replayed {
		t.Fatalf("exact launch replay = %+v", result)
	}
	if verifyWallDispatchCompletions(t, s, attempt) != 1 {
		t.Fatal("replay minted a second launch authorization")
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1' AND attempt_id='attempt:work-1:unknown-launch' AND lifecycle_state='in_flight'`); got != 1 {
		t.Fatalf("unknown launch has %d in-flight rows, want one", got)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1' AND dispatch_event_id IS NOT NULL AND attempt_id='attempt:work-1:unknown-launch'`); got != 0 {
		t.Fatalf("unknown launch has %d materializations, want zero", got)
	}
	env.RequestID = "request:blind-launch"
	const blind = "attempt:work-1:blind-launch"
	if result := dispatchMutation(t, s, service, request(blind, version), env); result.Error == nil || result.Error.Kind != "operation_conflict" || !strings.Contains(result.Error.Message, "unresolved launch outcome") {
		t.Fatalf("blind launch = %+v, want unresolved launch conflict", result.Error)
	}
	if workVersion(t, s, "work-1") != version || agentCurrentEffects(t, s) != events || verifyWallDispatchCompletions(t, s, blind) != 0 {
		t.Fatal("replay or blind launch changed workflow effects")
	}
	if after := verifyWallPinCorrection(t, s); after.SourceEventID != correction.SourceEventID || after.FailedAttemptID != failedID || after.FailedAttemptEpoch != correction.FailedAttemptEpoch {
		t.Fatalf("unmaterialized launch consumed or changed correction: %+v", after)
	}
	if verifyWallChallenges(t, s) != 0 {
		t.Fatal("ordinary launch or refusal minted retry consent")
	}
	assertDispatchHasNoRetryBudget(t, s, attempt)
}

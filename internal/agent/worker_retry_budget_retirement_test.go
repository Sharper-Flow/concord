package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestRepeatedFailedWorkersUseOrdinaryLaunchAuthority(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition", "worker_dispatch"})
	_, _, grant.Worktree = seedFailedWorkerRetryMutation(t, s, service, grant)
	env := mutationEnvelope(grant, mustScopeVersion(t, s))
	for round := 2; round <= 5; round++ {
		pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
		if err != nil || pin.Correction == nil {
			t.Fatalf("correction before round %d: %+v, %v", round, pin, err)
		}
		advertised := false
		for _, intent := range pin.NextValidIntents {
			if intent.ActionID == "dispatch_worker" {
				advertised = true
				if intent.ReasonCode == "escalated_retry_requires_convergence" {
					t.Fatalf("round %d advertises a retired convergence gate", round)
				}
			}
		}
		if !advertised {
			t.Fatalf("round %d hides ordinary dispatch from the work pin", round)
		}
		attempt := fmt.Sprintf("attempt:work-1:ordinary-%d", round)
		request := InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{
			"work_id": "work-1", "expected_version": pin.Version, "action_id": "dispatch_worker", "idempotency_key": attempt,
			"fields": map[string]any{"attempt_id": attempt, "worker_packet": retryMutationPacket(t, s, attempt, pin.Correction)},
		})}
		if result := dispatchMutation(t, s, service, request, env); result.Outcome != OutcomeOK {
			t.Fatalf("ordinary launch %d required retry credit: %+v", round, result.Error)
		}
		if got := verifyWallDispatchCompletions(t, s, attempt); got != 1 {
			t.Fatalf("launch %d recorded %d authorizations, want one", round, got)
		}
		lane := retryLane(t)
		started := store.Event{EventID: "ordinary-start:" + attempt, Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: retryJSON(store.WorkerDispatchedPayload{
			AttemptID: attempt, LaneID: lane.ID, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass,
			PacketDigest: "sha256:" + strings.Repeat("b", 64), ReadbackModel: "openai/gpt-5.6-luna", PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion, WorkerJob: authorizedWorkerJob(t, s, attempt),
		})}
		failure := agentWorkerFailedAttributedEvent("ordinary-failure:"+attempt, store.WorkerFailedPayload{AttemptID: attempt, ReadbackModel: "openai/gpt-5.6-luna", FailureKind: store.WorkerFailureWorkerError, Detail: "synthetic terminal failure"})
		if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
			enriched, err := store.PrepareLaneActorDispatch(context.Background(), tx, started, grant.PrincipalRef, grant.ClientRef)
			if err != nil {
				return err
			}
			if _, err := store.AppendLaneActorDispatchTx(context.Background(), tx, enriched); err != nil {
				return err
			}
			_, err = store.ApplyWorkerEvidenceOperationTx(context.Background(), tx, failure)
			return err
		}); err != nil {
			t.Fatalf("terminal ingestion %d: %v", round, err)
		}
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM domain_events WHERE subject_id='work-1' AND kind='workflow.action_completed' AND (json_extract(payload,'$.retry_convergence') IS NOT NULL OR json_extract(payload,'$.checks_repair_review.funded') IS NOT NULL)`); got != 0 {
		t.Fatalf("ordinary launches produced %d budget authority markers", got)
	}
	if got := verifyWallChallenges(t, s); got != 0 {
		t.Fatalf("ordinary launches produced %d retry-only consent challenges", got)
	}
}

package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/hostlease"
	"github.com/sharper-flow/concord/internal/store"
)

func TestHostRestartDoesNotSupplyRetryAuthority(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition", "worker_dispatch"})
	_, _, grant.Worktree = seedFailedWorkerRetryMutation(t, s, service, grant)
	env := mutationEnvelope(grant, mustScopeVersion(t, s))
	for round := 2; round <= 3; round++ {
		pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
		if err != nil || pin.Correction == nil {
			t.Fatalf("read correction before round %d: %+v, %v", round, pin, err)
		}
		attemptID := fmt.Sprintf("attempt:work-1:host-failure-%d", round)
		request := InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{
			"work_id": "work-1", "expected_version": pin.Version, "action_id": "dispatch_worker", "idempotency_key": attemptID,
			"fields": map[string]any{"attempt_id": attemptID, "worker_packet": retryMutationPacket(t, s, attemptID, pin.Correction)},
		})}
		if result := dispatchMutation(t, s, service, request, env); result.Outcome != OutcomeOK {
			t.Fatalf("authorize round %d: %+v", round, result.Error)
		}
		failure := agentWorkerFailedAttributedEvent("host-terminal:"+attemptID, store.WorkerFailedPayload{
			AttemptID: attemptID, ReadbackModel: "openai/gpt-5.6-luna", FailureKind: store.WorkerFailureWorkerError, Detail: "synthetic host execution failure",
		})
		failure.OccurredAt = time.Unix(1, 0).UTC()
		lane := retryLane(t)
		started := store.Event{EventID: "host-start:" + attemptID, Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: retryJSON(store.WorkerDispatchedPayload{
			AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass,
			PacketDigest: "sha256:" + strings.Repeat("b", 64), ReadbackModel: "openai/gpt-5.6-luna", PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion, WorkerJob: authorizedWorkerJob(t, s, attemptID),
		})}
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
			t.Fatalf("record host failure %d: %v", round, err)
		}
	}
	if _, err := s.RecordWorktreeClaimLanding(context.Background(), store.WorktreeClaimLandingRequest{
		WorkID: "work-1", SessionRef: grant.SessionRef, LandedDirectory: grant.Worktree, HostPID: os.Getpid(),
	}); err != nil {
		t.Fatal(err)
	}
	start, err := hostlease.ProcessStart(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := hostlease.Write(filepath.Dir(s.Path()), hostlease.Lease{PID: os.Getpid(), PidStart: start, ReleaseRoot: filepath.Dir(self), CoreBinary: self, SchemaVersion: 1, ManifestDigest: ManifestDigest}); err != nil {
		t.Fatal(err)
	}
	if leases := s.ReadHostLeases(); leases.Err != nil || len(leases.Leases) != 1 {
		t.Fatalf("live lease observation: %+v", leases)
	}
	pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil || pin.Correction == nil || pin.Correction.FailedAttemptID != "attempt:work-1:host-failure-3" {
		t.Fatalf("the host observation lost the current correction: %+v, %v", pin, err)
	}
	request := InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{
		"work_id": "work-1", "expected_version": pin.Version, "action_id": "dispatch_worker", "idempotency_key": "host-credit-not-authority",
		"fields": map[string]any{"attempt_id": "attempt:work-1:no-host-credit", "worker_packet": retryMutationPacket(t, s, "attempt:work-1:no-host-credit", pin.Correction)},
	})}
	result := dispatchMutation(t, s, service, request, env)
	if result.Error != nil {
		if result.Error.Kind != "missing_evidence" {
			t.Fatalf("dispatch failed outside ordinary admission: %+v", result.Error)
		}
		if strings.Contains(result.Error.Message, "restart the host") || strings.Contains(result.Error.Message, "host_changed") {
			t.Fatalf("refusal advertises a retired host credit: %+v", result.Error)
		}
		if got := workOneVersion(t, s); got != pin.Version {
			t.Fatalf("host-credit refusal changed version: got %d, want %d", got, pin.Version)
		}
		if got := verifyWallDispatchCompletions(t, s, "attempt:work-1:no-host-credit"); got != 0 {
			t.Fatalf("host-credit refusal recorded %d dispatch completions", got)
		}
	} else if result.Outcome != OutcomeOK || verifyWallDispatchCompletions(t, s, "attempt:work-1:no-host-credit") != 1 {
		t.Fatalf("ordinary admission did not record exactly one authorization: %+v", result)
	}
	// Process identity is not an admission basis, even when the request can
	// proceed through ordinary admission.
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM domain_events WHERE subject_id='work-1' AND kind='workflow.action_completed' AND json_extract(payload,'$.retry_convergence.basis')='host_changed'`); got != 0 {
		t.Fatalf("host restart supplied %d convergence credits", got)
	}
	if got := verifyWallChallenges(t, s); got != 0 {
		t.Fatalf("host-credit refusal created %d consent challenges", got)
	}
}

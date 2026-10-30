package agent

import (
	"context"
	"encoding/json"
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
		// the action transaction. A vacate injected earlier or later lets the
		// start succeed, so a changed probe order fails this test.
		if probes == 3 {
			payload, _ := json.Marshal(map[string]any{"work_id": "work-1", "project_id": "project-1", "session_ref": f.ownerGrant.SessionRef, "source_directory": "/repo-wt", "destination_directory": "/repo"})
			if err := store.ApplyOperation(ctx, f.s, store.Operation{Events: []store.Event{{EventID: "owner-vacated-mid-flight", Kind: "work.session_vacated", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: f.ownerGrant.PrincipalRef, OccurredAt: fixedTime(), PayloadVersion: 2, Payload: payload}}}); err != nil {
				t.Fatal(err)
			}
		}
		return originalProbe(ctx, directory, worktree)
	}
	response := invokeWorkflowBoundary(t, f.s, f.service, f.other, input, store.BuiltinWorkflowRegistry())
	f.service.ProjectHostProber = originalProbe
	if probes < 3 {
		t.Fatalf("the mutation probed the host %d times, want the third probe to inject the vacate", probes)
	}
	if response.Error == nil || response.Error.Kind != "operation_conflict" || !strings.Contains(response.Error.Message, "held attempt changed") {
		t.Fatalf("takeover after the holder changed mid-flight = outcome %s error %+v, want the held-attempt fence", response.Outcome, response.Error)
	}
	if epoch, actor := latestExecutionStart(t, f.s); epoch != 1 || actor != actorRefOf(f.ownerGrant) {
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

// session_vacate records work.session_vacated naming the leaving session
// (internal/agent/mutations.go planSessionVacate). The fixture appends that
// recorded shape directly; the vacate route itself has its own coverage.
func TestStartExecutionAfterHolderVacatesNeedsNoApproval(t *testing.T) {
	t.Parallel()
	f := newTakeoverFixture(t)
	if first := f.action(t, f.owner, "start_execution", "owner-start"); first.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("owner start_execution: %+v", first.Error)
	}
	payload, _ := json.Marshal(map[string]any{"work_id": "work-1", "project_id": "project-1", "session_ref": f.ownerGrant.SessionRef, "source_directory": "/repo-wt", "destination_directory": "/repo"})
	if err := store.ApplyOperation(context.Background(), f.s, store.Operation{Events: []store.Event{{EventID: "owner-vacated", Kind: "work.session_vacated", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: f.ownerGrant.PrincipalRef, OccurredAt: fixedTime(), PayloadVersion: 2, Payload: payload}}}); err != nil {
		t.Fatal(err)
	}
	if handoff := f.action(t, f.other, "start_execution", "other-start"); handoff.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("start after the holder vacated = %+v, want ok without approval", handoff.Error)
	}
	if epoch, actor := latestExecutionStart(t, f.s); epoch != 2 || actor != actorRefOf(f.otherGrant) {
		t.Fatalf("handoff epoch %d actor %s, want epoch 2 held by the next session", epoch, actor)
	}
}

// A coordinator start_execution over a live worker lane takes the attempt
// from the lane, so it needs approval until the lane relinquishes. On the
// head definition an abandoned terminal derives record_worker_failure in the
// same commit, which disposes of the lane attempt.
func TestStartExecutionOverLiveLaneRequiresApprovalUntilLaneRelinquishes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition", "worker_dispatch"})
	grant.Worktree = seedWorkerRetryMutationFixture(t, s, grant)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	start := func(key string) agentcontract.Envelope {
		t.Helper()
		return dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "action_id": "start_execution", "idempotency_key": key})}, env)
	}
	if first := start("lane-owner-start"); first.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("owner start_execution: %+v", first.Error)
	}
	recordReviewedRetryJob(t, s, service, env, "job:retry-objective")
	const attemptID = "attempt:work-1:live-lane"
	if dispatched := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{
		"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "action_id": "dispatch_worker", "idempotency_key": "live-lane-dispatch",
		"fields": map[string]any{"attempt_id": attemptID, "worker_packet": retryMutationPacket(t, s, attemptID, nil)},
	})}, env); dispatched.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("dispatch_worker: %+v", dispatched.Error)
	}
	lane := retryLane(t)
	laneDispatch := store.Event{EventID: "live-lane-dispatched", Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: retryJSON(store.WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, PacketDigest: "sha256:" + strings.Repeat("b", 64), ReadbackModel: "openai/gpt-5.6-luna", PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion, WorkerJob: authorizedWorkerJob(t, s, attemptID)})}
	var laneActor string
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		enriched, err := store.PrepareLaneActorDispatch(ctx, tx, laneDispatch, grant.PrincipalRef, grant.ClientRef)
		if err != nil {
			return err
		}
		for _, event := range enriched {
			if event.Kind != store.WorkerDispatched {
				continue
			}
			var payload store.WorkerDispatchedPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return err
			}
			laneActor = payload.LaneActorRef
		}
		_, err = store.AppendLaneActorDispatchTx(ctx, tx, enriched)
		return err
	}); err != nil {
		t.Fatalf("lane dispatch: %v", err)
	}

	if laneActor == "" {
		t.Fatal("live lane has no recorded actor")
	}

	failure := agentWorkerFailedAttributedEvent("live-lane-abandoned", store.WorkerFailedPayload{AttemptID: attemptID, FailureKind: store.WorkerFailureAbandoned, Detail: "operator abandoned the lane"})
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		_, err := store.ApplyWorkerEvidenceOperationTx(ctx, tx, failure)
		return err
	}); err != nil {
		t.Fatalf("abandon lane: %v", err)
	}
	if after := start("lane-owner-after-abandon"); after.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("start after the lane relinquished = %+v, want ok without approval", after.Error)
	}
}

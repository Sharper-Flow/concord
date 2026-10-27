package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// CD-0013 criterion 25: an operator takeover of a stuck attempt succeeds only
// with a valid approval_ref. CD-0008 D4 forbids detecting "stuck" by liveness,
// so the holder is the session that holds the attempt's writing authority. A
// fresh start_execution by any other session, while the holder has not
// relinquished, is the takeover. Criterion 21 still holds: the approved
// takeover opens the next epoch and only that epoch completes.

type takeoverFixture struct {
	s          *store.Store
	service    *Service
	privateKey ed25519.PrivateKey
	owner      CallEnvelope
	ownerGrant Authority
	other      CallEnvelope
	otherGrant Authority
}

func newTakeoverFixture(t *testing.T) takeoverFixture {
	t.Helper()
	ctx := context.Background()
	s, service, grant, privateKey := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})
	seedAgentWorkflow(t, s, grant)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	owner, _ := advanceWorkflowBoundaryToExecution(t, s, service, grant, privateKey, mutationEnvelope(grant, scopeVersion))
	owner.HostApproval = nil
	if err := service.RegisterTrustedClient(ctx, testClientRegistration("client-2", "human-2", []agentcontract.Capability{"work_transition"}, []string{"product-1"}, []string{"project-1"})); err != nil {
		t.Fatal(err)
	}
	otherGrant, err := service.Authorize(ctx, Invocation{ClientRef: "client-2", PrincipalRef: "human-2", SessionRef: "session-2", AgentRef: "agent-other", Directory: "/repo", Worktree: "/repo-wt", ManifestDigest: agentcontract.ManifestDigest, RequiredCapability: "work_transition", ProductID: "product-1", ProjectID: "project-1"})
	if err != nil {
		t.Fatal(err)
	}
	other := mutationEnvelope(otherGrant, scopeVersion)
	other.RequestID = "other-request"
	return takeoverFixture{s: s, service: service, privateKey: privateKey, owner: owner, ownerGrant: grant, other: other, otherGrant: otherGrant}
}

func (f takeoverFixture) action(t *testing.T, env CallEnvelope, actionID, key string) (Envelope, map[string]any) {
	t.Helper()
	input := map[string]any{"work_id": "work-1", "expected_version": workVersion(t, f.s, "work-1"), "action_id": actionID, "fields": map[string]any{}, "idempotency_key": key}
	return invokeWorkflowBoundary(t, f.s, f.service, env, input, store.BuiltinWorkflowRegistry()), input
}

// approveTakeover signs the exact bindings the challenge minted for the caller.
func approveTakeover(t *testing.T, privateKey ed25519.PrivateKey, env CallEnvelope, grant Authority, challenge Envelope, input map[string]any) (CallEnvelope, map[string]any) {
	t.Helper()
	ref, _ := challenge.Error.Details["approval_ref"].(string)
	if ref == "" {
		t.Fatalf("challenge carries no approval_ref: %+v", challenge.Error)
	}
	approved := cloneWithApproval(t, input, ref)
	scope, versions := challengeApprovalBindings(t, challenge.Error.ConsequenceSummary)
	env.HostApproval = signedHostApproval(privateKey, ref, mutationDigest("concord_work_transition", "workflow_action", env, mustJSON(t, approved)), scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), nonceForChallenge(ref))
	return env, approved
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

func TestStartExecutionTakeoverRequiresApprovalBoundToHeldAttempt(t *testing.T) {
	t.Parallel()
	f := newTakeoverFixture(t)
	if first, _ := f.action(t, f.owner, "start_execution", "owner-start"); first.Outcome != OutcomeOK {
		t.Fatalf("owner start_execution: %+v", first.Error)
	}
	ownerRef := actorRefOf(f.ownerGrant)

	challenge, input := f.action(t, f.other, "start_execution", "other-start")
	if challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		epoch, actor := latestExecutionStart(t, f.s)
		t.Fatalf("unapproved takeover = outcome %s error %+v (latest epoch %d actor %s), want approval_required", challenge.Outcome, challenge.Error, epoch, actor)
	}
	summary := challenge.Error.ConsequenceSummary
	if summary == nil || !slices.Contains(summary.Scope, "held_attempt_holder:"+ownerRef) || !slices.Contains(summary.Versions, "held_attempt_epoch:1") {
		t.Fatalf("takeover challenge does not bind the held attempt: %#v", summary)
	}
	if epoch, actor := latestExecutionStart(t, f.s); epoch != 1 || actor != ownerRef {
		t.Fatalf("challenge moved the attempt: epoch %d actor %s", epoch, actor)
	}

	approvedEnv, approved := approveTakeover(t, f.privateKey, f.other, f.otherGrant, challenge, input)
	response := invokeWorkflowBoundary(t, f.s, f.service, approvedEnv, approved, store.BuiltinWorkflowRegistry())
	if response.Outcome != OutcomeOK {
		t.Fatalf("approved takeover: %+v", response.Error)
	}
	if epoch, actor := latestExecutionStart(t, f.s); epoch != 2 || actor != actorRefOf(f.otherGrant) {
		t.Fatalf("approved takeover epoch %d actor %s, want epoch 2 held by the taker", epoch, actor)
	}
	// Criterion 21: the superseded holder's epoch no longer writes.
	if stale, _ := f.action(t, f.owner, "checkpoint_execution", "owner-stale-checkpoint"); stale.Error == nil || stale.Error.Kind != "unauthorized" {
		t.Fatalf("superseded holder checkpoint = %+v, want unauthorized", stale.Error)
	}
	if current, _ := f.action(t, approvedEnv, "checkpoint_execution", "taker-checkpoint"); current.Outcome != OutcomeOK {
		t.Fatalf("taker checkpoint: %+v", current.Error)
	}
}

func TestStartExecutionTakeoverApprovalRefusesAfterHeldAttemptMoves(t *testing.T) {
	t.Parallel()
	f := newTakeoverFixture(t)
	if first, _ := f.action(t, f.owner, "start_execution", "owner-start"); first.Outcome != OutcomeOK {
		t.Fatalf("owner start_execution: %+v", first.Error)
	}
	challenge, input := f.action(t, f.other, "start_execution", "other-start")
	if challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("unapproved takeover = %+v, want approval_required", challenge.Error)
	}
	// The holder restarts its own attempt after the challenge, so the
	// approval names an epoch that is no longer held.
	if restart, _ := f.action(t, f.owner, "start_execution", "owner-restart"); restart.Outcome != OutcomeOK {
		t.Fatalf("owner restart: %+v", restart.Error)
	}
	approved := cloneWithApproval(t, input, challenge.Error.Details["approval_ref"].(string))
	approved["expected_version"] = workVersion(t, f.s, "work-1")
	ref := challenge.Error.Details["approval_ref"].(string)
	scope, versions := challengeApprovalBindings(t, challenge.Error.ConsequenceSummary)
	versions["work"] = approved["expected_version"]
	env := f.other
	env.HostApproval = signedHostApproval(f.privateKey, ref, mutationDigest("concord_work_transition", "workflow_action", env, mustJSON(t, approved)), scope, versions, f.otherGrant.SessionRef, f.otherGrant.AgentRef, f.otherGrant.Worktree, fixedTime(), nonceForChallenge(ref))
	response := invokeWorkflowBoundary(t, f.s, f.service, env, approved, store.BuiltinWorkflowRegistry())
	if response.Error == nil || response.Error.Kind != "approval_invalid" {
		t.Fatalf("approval bound to epoch 1 over epoch 2 = outcome %s error %+v, want approval_invalid", response.Outcome, response.Error)
	}
	if epoch, actor := latestExecutionStart(t, f.s); epoch != 2 || actor != actorRefOf(f.ownerGrant) {
		t.Fatalf("refused stale takeover moved the attempt: epoch %d actor %s", epoch, actor)
	}
}

// The approved takeover rereads the held attempt inside the action
// transaction: a change after the boundary read refuses the approval rather
// than letting it authorize a different holder's attempt.
func TestStartExecutionTakeoverFencesHeldAttemptChangeInsideTransaction(t *testing.T) {
	t.Parallel()
	f := newTakeoverFixture(t)
	if first, _ := f.action(t, f.owner, "start_execution", "owner-start"); first.Outcome != OutcomeOK {
		t.Fatalf("owner start_execution: %+v", first.Error)
	}
	challenge, input := f.action(t, f.other, "start_execution", "other-start")
	if challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("unapproved takeover = %+v, want approval_required", challenge.Error)
	}
	approvedEnv, approved := approveTakeover(t, f.privateKey, f.other, f.otherGrant, challenge, input)
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
	response := invokeWorkflowBoundary(t, f.s, f.service, approvedEnv, approved, store.BuiltinWorkflowRegistry())
	f.service.ProjectHostProber = originalProbe
	if probes < 3 {
		t.Fatalf("the mutation probed the host %d times, want the third probe to inject the vacate", probes)
	}
	if response.Error == nil || response.Error.Kind != "approval_invalid" || !strings.Contains(response.Error.Message, "held attempt changed") {
		t.Fatalf("takeover after the holder changed mid-flight = outcome %s error %+v, want the held-attempt fence", response.Outcome, response.Error)
	}
	if epoch, actor := latestExecutionStart(t, f.s); epoch != 1 || actor != actorRefOf(f.ownerGrant) {
		t.Fatalf("fenced takeover moved the attempt: epoch %d actor %s", epoch, actor)
	}
}

func TestStartExecutionHolderRestartNeedsNoApproval(t *testing.T) {
	t.Parallel()
	f := newTakeoverFixture(t)
	if first, _ := f.action(t, f.owner, "start_execution", "owner-start"); first.Outcome != OutcomeOK {
		t.Fatalf("owner start_execution: %+v", first.Error)
	}
	if restart, _ := f.action(t, f.owner, "start_execution", "owner-restart"); restart.Outcome != OutcomeOK {
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
	if first, _ := f.action(t, f.owner, "start_execution", "owner-start"); first.Outcome != OutcomeOK {
		t.Fatalf("owner start_execution: %+v", first.Error)
	}
	payload, _ := json.Marshal(map[string]any{"work_id": "work-1", "project_id": "project-1", "session_ref": f.ownerGrant.SessionRef, "source_directory": "/repo-wt", "destination_directory": "/repo"})
	if err := store.ApplyOperation(context.Background(), f.s, store.Operation{Events: []store.Event{{EventID: "owner-vacated", Kind: "work.session_vacated", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: f.ownerGrant.PrincipalRef, OccurredAt: fixedTime(), PayloadVersion: 2, Payload: payload}}}); err != nil {
		t.Fatal(err)
	}
	if handoff, _ := f.action(t, f.other, "start_execution", "other-start"); handoff.Outcome != OutcomeOK {
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
	start := func(key string) Envelope {
		t.Helper()
		return dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "action_id": "start_execution", "idempotency_key": key})}, env)
	}
	if first := start("lane-owner-start"); first.Outcome != OutcomeOK {
		t.Fatalf("owner start_execution: %+v", first.Error)
	}
	recordReviewedRetryJob(t, s, service, env, "job:retry-objective")
	const attemptID = "attempt:work-1:live-lane"
	if dispatched := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{
		"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "action_id": "dispatch_worker", "idempotency_key": "live-lane-dispatch",
		"fields": map[string]any{"attempt_id": attemptID, "worker_packet": retryMutationPacket(t, s, attemptID, nil)},
	})}, env); dispatched.Outcome != OutcomeOK {
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

	challenge := start("lane-owner-takeover")
	if challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("start over a live lane = outcome %s error %+v, want approval_required", challenge.Outcome, challenge.Error)
	}
	if summary := challenge.Error.ConsequenceSummary; summary == nil || laneActor == "" || !slices.Contains(summary.Scope, "held_attempt_holder:"+laneActor) {
		t.Fatalf("live-lane challenge does not name the lane %q as holder: %#v", laneActor, challenge.Error.ConsequenceSummary)
	}

	failure := agentWorkerFailedAttributedEvent("live-lane-abandoned", store.WorkerFailedPayload{AttemptID: attemptID, FailureKind: store.WorkerFailureAbandoned, Detail: "operator abandoned the lane"})
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		_, err := store.ApplyWorkerEvidenceOperationTx(ctx, tx, failure)
		return err
	}); err != nil {
		t.Fatalf("abandon lane: %v", err)
	}
	if after := start("lane-owner-after-abandon"); after.Outcome != OutcomeOK {
		t.Fatalf("start after the lane relinquished = %+v, want ok without approval", after.Error)
	}
}

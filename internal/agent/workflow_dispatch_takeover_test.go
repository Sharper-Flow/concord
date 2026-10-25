package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// A fresh dispatch_worker opens the step's next attempt epoch, so over an
// unfinished attempt another session holds it takes that attempt's
// completion away: the CD-0013 criterion 25 takeover the start rule already
// governs. The caller's own unfinished child instead requires exact recovery
// before a fresh dispatch (CD-0242 D6). A recorded failure permits retry,
// and the holder's relinquishment releases the takeover requirement.

type dispatchTakeoverFixture struct {
	s          *store.Store
	service    *Service
	privateKey ed25519.PrivateKey
	owner      CallEnvelope
	ownerGrant Authority
	other      CallEnvelope
	otherGrant Authority
}

func newDispatchTakeoverFixture(t *testing.T) dispatchTakeoverFixture {
	t.Helper()
	ctx := context.Background()
	capabilities := []Capability{"work_transition", "worker_dispatch"}
	s, service, grant, privateKey := mutationDispatchFixture(t, capabilities)
	grant.Worktree = seedWorkerRetryMutationFixture(t, s, grant)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RegisterTrustedClient(ctx, testClientRegistration("client-2", "human-2", capabilities, []string{"product-1"}, []string{"project-1"})); err != nil {
		t.Fatal(err)
	}
	otherGrant, err := service.Authorize(ctx, Invocation{ClientRef: "client-2", PrincipalRef: "human-2", SessionRef: "session-2", AgentRef: "agent-other", Directory: grant.Directory, Worktree: grant.Worktree, ManifestDigest: ManifestDigest, RequiredCapability: "worker_dispatch", ProductID: "product-1", ProjectID: "project-1"})
	if err != nil {
		t.Fatal(err)
	}
	other := mutationEnvelope(otherGrant, scopeVersion)
	other.RequestID = "other-request"
	f := dispatchTakeoverFixture{s: s, service: service, privateKey: privateKey, owner: mutationEnvelope(grant, scopeVersion), ownerGrant: grant, other: other, otherGrant: otherGrant}
	if start := f.invoke(t, f.owner, map[string]any{"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "action_id": "start_execution", "idempotency_key": "owner-start"}); start.Outcome != OutcomeOK {
		t.Fatalf("owner start_execution: %+v", start.Error)
	}
	recordReadyRetryJob(t, s, service, f.owner, "job:retry-objective")
	return f
}

func (f dispatchTakeoverFixture) invoke(t *testing.T, env CallEnvelope, input map[string]any) Envelope {
	t.Helper()
	return invokeWorkflowBoundary(t, f.s, f.service, env, input, store.BuiltinWorkflowRegistry())
}

func (f dispatchTakeoverFixture) dispatch(t *testing.T, env CallEnvelope, attemptID, key string) (Envelope, map[string]any) {
	t.Helper()
	pin, err := store.ReadWorkPin(context.Background(), f.s, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]any{
		"work_id": "work-1", "expected_version": pin.Version, "action_id": "dispatch_worker", "idempotency_key": key,
		"fields": map[string]any{"attempt_id": attemptID, "worker_packet": retryMutationPacket(t, f.s, attemptID, pin.Correction)},
	}
	return f.invoke(t, env, input), input
}

func (f dispatchTakeoverFixture) failLane(t *testing.T, eventID, attemptID string) {
	t.Helper()
	failure := agentWorkerFailedAttributedEvent(eventID, store.WorkerFailedPayload{AttemptID: attemptID, ReadbackModel: "openai/gpt-5.6-luna", FailureKind: store.WorkerFailureWorkerError, Detail: "synthetic failure"})
	if err := f.s.Transact(context.Background(), func(tx *store.Transaction) error {
		_, err := store.ApplyWorkerEvidenceOperationTx(context.Background(), tx, failure)
		return err
	}); err != nil {
		t.Fatalf("fail lane: %v", err)
	}
}

// laneDispatch records the worker session's own worker.dispatched for the
// attempt and returns the lane actor it pins.
func (f dispatchTakeoverFixture) laneDispatch(t *testing.T, eventID, attemptID string, caller Authority) string {
	t.Helper()
	ctx := context.Background()
	lane := retryLane(t)
	event := store.Event{EventID: eventID, Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: retryJSON(store.WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, PacketDigest: "sha256:" + strings.Repeat("b", 64), ReadbackModel: "openai/gpt-5.6-luna", PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion, WorkerJob: authorizedWorkerJob(t, f.s, attemptID)})}
	var laneActor string
	if err := f.s.Transact(ctx, func(tx *store.Transaction) error {
		enriched, err := store.PrepareLaneActorDispatch(ctx, tx, event, caller.PrincipalRef, caller.ClientRef)
		if err != nil {
			return err
		}
		for _, candidate := range enriched {
			if candidate.Kind != store.WorkerDispatched {
				continue
			}
			var payload store.WorkerDispatchedPayload
			if err := json.Unmarshal(candidate.Payload, &payload); err != nil {
				return err
			}
			laneActor = payload.LaneActorRef
		}
		_, err = store.AppendLaneActorDispatchTx(ctx, tx, enriched)
		return err
	}); err != nil {
		t.Fatalf("lane dispatch %s: %v", attemptID, err)
	}
	return laneActor
}

func requireTakeoverChallenge(t *testing.T, challenge Envelope, holder string, epoch int64) {
	t.Helper()
	if challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("dispatch over another session's unfinished attempt = outcome %s error %+v, want approval_required", challenge.Outcome, challenge.Error)
	}
	summary := challenge.Error.ConsequenceSummary
	if summary == nil || holder == "" || !slices.Contains(summary.Scope, "held_attempt_holder:"+holder) || !slices.Contains(summary.Versions, "held_attempt_epoch:"+strconv.FormatInt(epoch, 10)) {
		t.Fatalf("dispatch takeover challenge does not bind holder %q epoch %d: %#v", holder, epoch, summary)
	}
}

func TestDispatchWorkerOverAnotherSessionsInFlightAttemptRequiresBoundApproval(t *testing.T) {
	t.Parallel()
	f := newDispatchTakeoverFixture(t)
	if first, _ := f.dispatch(t, f.owner, "attempt:work-1:owner", "owner-dispatch"); first.Outcome != OutcomeOK {
		t.Fatalf("owner dispatch_worker: %+v", first.Error)
	}
	laneActor := f.laneDispatch(t, "owner-lane-dispatched", "attempt:work-1:owner", f.ownerGrant)
	heldEpoch, ownerRef := latestExecutionStart(t, f.s)

	challenge, input := f.dispatch(t, f.other, "attempt:work-1:other", "other-dispatch")
	requireTakeoverChallenge(t, challenge, laneActor, heldEpoch)
	if epoch, actor := latestExecutionStart(t, f.s); epoch != heldEpoch || actor != ownerRef {
		t.Fatalf("challenge moved the attempt: epoch %d actor %s", epoch, actor)
	}

	approvedEnv, approved := approveTakeover(t, f.privateKey, f.other, f.otherGrant, challenge, input)
	if response := f.invoke(t, approvedEnv, approved); response.Outcome != OutcomeOK {
		t.Fatalf("approved dispatch takeover: %+v", response.Error)
	}
	if epoch, actor := latestExecutionStart(t, f.s); epoch != heldEpoch+1 || actor != actorRefOf(f.otherGrant) {
		t.Fatalf("approved dispatch takeover epoch %d actor %s, want epoch %d opened by the taker", epoch, actor, heldEpoch+1)
	}

	// The taker's later start must not hide the original caller's unresolved
	// child, and no takeover approval is requested over it.
	f.laneDispatch(t, "other-lane-dispatched", "attempt:work-1:other", f.otherGrant)
	challenges, effects := verifyWallChallenges(t, f.s), agentCurrentEffects(t, f.s)
	if again, _ := f.dispatch(t, f.owner, "attempt:work-1:owner-again", "owner-redispatch"); again.Error == nil || again.Error.Kind != "operation_conflict" {
		t.Fatalf("owner fresh dispatch behind a later takeover start = %+v, want operation_conflict", again.Error)
	}
	if verifyWallChallenges(t, f.s) != challenges || agentCurrentEffects(t, f.s) != effects {
		t.Fatal("refused own-child replacement minted an approval challenge or changed durable progress")
	}
}

// Once the worker records its dispatch, the lane holds the unfinished
// attempt until a result decision relinquishes it. The original caller can
// replay its authorization, but cannot open another over the unfinished child.
func TestDispatchWorkerOverAnotherSessionsLiveLaneBindsTheLane(t *testing.T) {
	t.Parallel()
	f := newDispatchTakeoverFixture(t)
	const attemptID = "attempt:work-1:owner"
	first, original := f.dispatch(t, f.owner, attemptID, "owner-dispatch")
	if first.Outcome != OutcomeOK {
		t.Fatalf("owner dispatch_worker: %+v", first.Error)
	}
	heldEpoch, _ := latestExecutionStart(t, f.s)
	laneActor := f.laneDispatch(t, "owner-lane-dispatched", attemptID, f.ownerGrant)

	challenge, _ := f.dispatch(t, f.other, "attempt:work-1:other", "other-dispatch")
	requireTakeoverChallenge(t, challenge, laneActor, heldEpoch)

	version, effects := workVersion(t, f.s, "work-1"), agentCurrentEffects(t, f.s)
	if replay := f.invoke(t, f.owner, original); replay.Outcome != OutcomeOK || !replay.Replayed {
		t.Fatalf("original started authorization replay = %+v", replay)
	}
	if again, _ := f.dispatch(t, f.owner, "attempt:work-1:owner-again", "owner-redispatch"); again.Error == nil || again.Error.Kind != "operation_conflict" {
		t.Fatalf("owner re-dispatch over its own live lane = %+v, want operation_conflict", again.Error)
	}
	if workVersion(t, f.s, "work-1") != version || agentCurrentEffects(t, f.s) != effects {
		t.Fatal("replay or refused own-child replacement changed durable progress")
	}
}

func TestDispatchWorkerTakeoverApprovalRefusesAfterHeldAttemptMoves(t *testing.T) {
	t.Parallel()
	f := newDispatchTakeoverFixture(t)
	if first, _ := f.dispatch(t, f.owner, "attempt:work-1:owner", "owner-dispatch"); first.Outcome != OutcomeOK {
		t.Fatalf("owner dispatch_worker: %+v", first.Error)
	}
	f.laneDispatch(t, "owner-lane-dispatched", "attempt:work-1:owner", f.ownerGrant)
	challenge, input := f.dispatch(t, f.other, "attempt:work-1:other", "other-dispatch")
	if challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("unapproved dispatch takeover = %+v, want approval_required", challenge.Error)
	}
	// A recorded terminal permits the holder's retry. The earlier takeover
	// approval must not authorize replacement of that different held epoch.
	f.failLane(t, "owner-lane-failed", "attempt:work-1:owner")
	if again, _ := f.dispatch(t, f.owner, "attempt:work-1:owner-again", "owner-redispatch"); again.Outcome != OutcomeOK {
		t.Fatalf("owner re-dispatch: %+v", again.Error)
	}
	f.laneDispatch(t, "owner-lane-redispatched", "attempt:work-1:owner-again", f.ownerGrant)
	movedEpoch, _ := latestExecutionStart(t, f.s)
	ref := challenge.Error.Details["approval_ref"].(string)
	approved := cloneWithApproval(t, input, ref)
	approved["expected_version"] = workVersion(t, f.s, "work-1")
	scope, versions := challengeApprovalBindings(t, challenge.Error.ConsequenceSummary)
	versions["work"] = approved["expected_version"]
	env := f.other
	env.HostApproval = signedHostApproval(f.privateKey, ref, mutationDigest("concord_work_transition", "workflow_action", env, mustJSON(t, approved)), scope, versions, f.otherGrant.SessionRef, f.otherGrant.AgentRef, f.otherGrant.Worktree, fixedTime(), nonceForChallenge(ref))
	if response := f.invoke(t, env, approved); response.Error == nil || response.Error.Kind != "approval_invalid" {
		t.Fatalf("approval bound to the superseded epoch = outcome %s error %+v, want approval_invalid", response.Outcome, response.Error)
	}
	if epoch, actor := latestExecutionStart(t, f.s); epoch != movedEpoch || actor != actorRefOf(f.ownerGrant) {
		t.Fatalf("refused stale takeover moved the attempt: epoch %d actor %s", epoch, actor)
	}
}

// session_vacate records work.session_vacated naming the leaving session
// (internal/agent/mutations.go planSessionVacate); the fixture appends that
// recorded shape directly.
func TestDispatchWorkerAfterHolderVacatesNeedsNoApproval(t *testing.T) {
	t.Parallel()
	f := newDispatchTakeoverFixture(t)
	if first, _ := f.dispatch(t, f.owner, "attempt:work-1:owner", "owner-dispatch"); first.Outcome != OutcomeOK {
		t.Fatalf("owner dispatch_worker: %+v", first.Error)
	}
	laneActor := f.laneDispatch(t, "owner-lane-dispatched", "attempt:work-1:owner", f.ownerGrant)
	var holderSession string
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT session_ref FROM workflow_actors WHERE actor_ref=?`, laneActor).Scan(&holderSession); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"work_id": "work-1", "project_id": "project-1", "session_ref": holderSession, "source_directory": f.ownerGrant.Worktree, "destination_directory": "/repo"})
	if err := store.ApplyOperation(context.Background(), f.s, store.Operation{Events: []store.Event{{EventID: "owner-vacated", Kind: "work.session_vacated", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: f.ownerGrant.PrincipalRef, OccurredAt: fixedTime(), PayloadVersion: 2, Payload: payload}}}); err != nil {
		t.Fatal(err)
	}
	if handoff, _ := f.dispatch(t, f.other, "attempt:work-1:other", "other-dispatch"); handoff.Outcome != OutcomeOK {
		t.Fatalf("dispatch after the holder vacated = %+v, want ok without approval", handoff.Error)
	}
}

// A failed attempt is finished: the next dispatch is a retry, whose
// challenge binds the failed attempt and never the takeover holder.
func TestDispatchWorkerAfterFailedAttemptConsumesCorrectionWithoutConsent(t *testing.T) {
	t.Parallel()
	f := newDispatchTakeoverFixture(t)
	const attemptID = "attempt:work-1:owner"
	if first, _ := f.dispatch(t, f.owner, attemptID, "owner-dispatch"); first.Outcome != OutcomeOK {
		t.Fatalf("owner dispatch_worker: %+v", first.Error)
	}
	f.laneDispatch(t, "owner-lane-dispatched", attemptID, f.ownerGrant)
	f.failLane(t, "owner-lane-failed", attemptID)
	pin, err := store.ReadWorkPin(context.Background(), f.s, "work-1")
	if err != nil || pin.Correction == nil || pin.Correction.FailedAttemptID != attemptID {
		t.Fatalf("failed attempt correction = %+v err=%v", pin.Correction, err)
	}
	if retry, _ := f.dispatch(t, f.other, "attempt:work-1:other", "other-retry"); retry.Outcome != OutcomeOK {
		t.Fatalf("consuming retry after the failure = %+v, want ok without consent", retry.Error)
	}
	if challenges := verifyWallChallenges(t, f.s); challenges != 0 {
		t.Fatalf("retry after a finished attempt minted %d takeover or retry challenges", challenges)
	}
	if epoch, actor := latestExecutionStart(t, f.s); epoch != pin.Correction.FailedAttemptEpoch+1 || actor != actorRefOf(f.otherGrant) {
		t.Fatalf("retry epoch=%d actor=%s, want the next epoch held by the retrying session", epoch, actor)
	}
}

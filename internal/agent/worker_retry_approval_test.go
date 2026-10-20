package agent

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

func TestWorkerRetryMutationRequiresExactApprovalAndFreshAttempt(t *testing.T) {
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition", "worker_dispatch"})
	version, attemptID, worktree := seedFailedWorkerRetryMutation(t, s, service, grant)
	grant.Worktree = worktree
	pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil || pin.Correction == nil {
		t.Fatalf("read retry correction: pin=%#v err=%v", pin.Correction, err)
	}
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	packet := retryMutationPacket(t, s, attemptID, pin.Correction)
	input := map[string]any{
		"work_id": "work-1", "expected_version": version, "action_id": "dispatch_worker",
		"idempotency_key": "retry-approval-1", "fields": map[string]any{"attempt_id": attemptID, "worker_packet": packet},
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	challenge := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("retry without approval = %+v, want approval_required", challenge.Error)
	}
	challengeRef, ok := challenge.Error.Details["approval_ref"].(string)
	if !ok || challengeRef == "" {
		t.Fatalf("retry challenge has no approval reference: %+v", challenge.Error.Details)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1'`); got != 1 {
		t.Fatalf("challenge created %d worker attempts, want 1", got)
	}

	approvedInput := cloneWithApproval(t, input, challengeRef)
	approvedRaw, _ := json.Marshal(approvedInput)
	// The approval signs exactly what the challenge minted: the derived
	// failure record binds the dispatch window's own epoch, so a literal
	// epoch guess breaks the equality the approval validates.
	scope, versions := challengeApprovalBindings(t, challenge.Error.ConsequenceSummary)
	env.HostApproval = signedHostApproval(privateKey, challengeRef, mutationDigest("concord_work_transition", "workflow_action", env, approvedRaw), scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), nonceForChallenge(challengeRef))
	approved := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approvedRaw}, env)
	if approved.Outcome != OutcomeError || approved.Error == nil || !strings.Contains(approved.Error.Message, "fresh attempt identity") {
		t.Fatalf("retry with failed attempt identity = %+v, want a fresh-identity refusal", approved.Error)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1'`); got != 1 {
		t.Fatalf("stale retry created %d worker attempts, want 1", got)
	}

	// A fresh request gets a fresh challenge. Reusing the first approval cannot
	// authorize the changed request or consume a second dispatch boundary.
	changedInput := cloneWithApproval(t, input, challengeRef)
	changedInput["idempotency_key"] = "retry-approval-2"
	changedInput["fields"].(map[string]any)["attempt_id"] = "attempt:work-1:fresh"
	changedInput["fields"].(map[string]any)["worker_packet"] = retryMutationPacket(t, s, "attempt:work-1:fresh", pin.Correction)
	changedRaw, _ := json.Marshal(changedInput)
	env.HostApproval = signedHostApproval(privateKey, challengeRef, mutationDigest("concord_work_transition", "workflow_action", env, changedRaw), scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), nonceForChallenge(challengeRef))
	reused := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: changedRaw}, env)
	if reused.Error == nil || !strings.Contains(reused.Error.Message, "approval challenge binding invalid") {
		t.Fatalf("reused approval = %+v, want an approval-binding refusal", reused.Error)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1'`); got != 1 {
		t.Fatalf("reused approval created %d worker attempts, want 1", got)
	}

	freshInput := cloneWithApproval(t, input, "unused")
	delete(freshInput, "approval")
	freshInput["idempotency_key"] = "retry-approval-fresh"
	freshInput["fields"].(map[string]any)["attempt_id"] = "attempt:work-1:fresh"
	freshInput["fields"].(map[string]any)["worker_packet"] = retryMutationPacket(t, s, "attempt:work-1:fresh", pin.Correction)
	freshRaw, _ := json.Marshal(freshInput)
	freshChallenge := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: freshRaw}, env)
	freshRef, ok := freshChallenge.Error.Details["approval_ref"].(string)
	if !ok || freshChallenge.Error.Kind != "approval_required" {
		t.Fatalf("fresh retry challenge = %+v", freshChallenge.Error)
	}
	freshApproved := cloneWithApproval(t, freshInput, freshRef)
	freshApprovedRaw, _ := json.Marshal(freshApproved)
	freshScope, freshVersions := challengeApprovalBindings(t, freshChallenge.Error.ConsequenceSummary)
	env.HostApproval = signedHostApproval(privateKey, freshRef, mutationDigest("concord_work_transition", "workflow_action", env, freshApprovedRaw), freshScope, freshVersions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), nonceForChallenge(freshRef))
	valid := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: freshApprovedRaw}, env)
	if valid.Outcome != OutcomeOK {
		t.Fatalf("fresh approved retry = %+v", valid.Error)
	}
	var retryEpoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE kind=? AND subject_id='work-1' AND json_extract(payload,'$.action_id')='dispatch_worker' ORDER BY seq DESC LIMIT 1`, store.WorkflowActionStarted).Scan(&retryEpoch); err != nil {
		t.Fatal(err)
	}
	// The seed's public dispatch opens the failed attempt's own fenced
	// epoch; the fresh approved retry opens the next one.
	var binding *store.WorkflowRetryApprovalBinding
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		var txErr error
		binding, txErr = store.WorkflowFailedWorkerRetryBindingTx(context.Background(), tx, nil, "work-1")
		return txErr
	}); err != nil || binding == nil {
		t.Fatalf("failed retry binding after the approved retry = %+v err=%v", binding, err)
	}
	if retryEpoch != binding.FailedAttemptEpoch+1 {
		t.Fatalf("fresh approved retry epoch=%d, want the next fenced epoch after the failed %d", retryEpoch, binding.FailedAttemptEpoch)
	}
}

func TestAuthorizedWorkerRetryChallengesBeforeFailureDisposition(t *testing.T) {
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition", "worker_dispatch"})
	grant.Worktree = seedWorkerRetryMutationFixture(t, s, grant)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	const failedID = "attempt:work-1:authorized-failed"
	recordReadyRetryJob(t, s, service, env, "job:authorize-unstarted-worker")
	first := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{
		"work_id": "work-1", "expected_version": workOneVersion(t, s), "action_id": "dispatch_worker", "idempotency_key": "authorize-unstarted-worker",
		"fields": map[string]any{"attempt_id": failedID, "worker_packet": retryMutationPacket(t, s, failedID, nil)},
	})}, env)
	if first.Outcome != OutcomeOK {
		t.Fatalf("first authorization: %+v", first.Error)
	}
	// The abandoned terminal carries the recorded host observation and lands
	// through the live worker-evidence seam. On the current head the pinned
	// definition derives the failure record from this original terminal in
	// the same commit; the record keeps the dispatch completion's actor rather
	// than the terminal producer's actor.
	failure := agentWorkerFailedAttributedEvent("abandon-unstarted-worker", store.WorkerFailedPayload{AttemptID: failedID, FailureKind: store.WorkerFailureAbandoned, Detail: "no admissible dispatch evidence"})
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		_, err := store.ApplyWorkerEvidenceOperationTx(context.Background(), tx, failure)
		return err
	}); err != nil {
		t.Fatalf("abandon authorization: %v", err)
	}
	// Exactly one failure record stands, and it is the original terminal's
	// own derived record.
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM domain_events WHERE subject_id='work-1' AND kind='workflow.action_completed' AND json_extract(payload,'$.action_id')='record_worker_failure'`); got != 1 {
		t.Fatalf("failure records after the original terminal = %d, want exactly the one derived record", got)
	}
	var recordAttempt, recordEvent string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.worker_attempt_id'), event_id FROM domain_events WHERE subject_id='work-1' AND kind=? AND json_extract(payload,'$.action_id')='record_worker_failure'`, store.WorkflowActionCompleted).Scan(&recordAttempt, &recordEvent); err != nil {
		t.Fatal(err)
	}
	if recordAttempt != failedID || recordEvent != "abandon-unstarted-worker:workflow-failure" {
		t.Fatalf("derived record = (%s, %s), want the original terminal's own record for %s", recordAttempt, recordEvent, failedID)
	}
	pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	scopeVersion, _, err = s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	const freshID = "attempt:work-1:authorized-retry"
	// The derived record stands open as the correction context, so the retry
	// packet consumes it.
	retryInput := map[string]any{
		"work_id": "work-1", "expected_version": pin.Version, "action_id": "dispatch_worker", "idempotency_key": "retry-unstarted-worker",
		"fields": map[string]any{"attempt_id": freshID, "worker_packet": retryMutationPacket(t, s, freshID, pin.Correction)},
	}
	env = mutationEnvelope(grant, scopeVersion)
	challenge := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(retryInput)}, env)
	if challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("retry before disposition: %+v, want approval_required", challenge.Error)
	}
	summary := challenge.Error.ConsequenceSummary
	if summary == nil || !slices.Contains(summary.Scope, "failed_attempt_id:"+failedID) || !slices.Contains(summary.Versions, "failed_attempt_epoch:1") || !slices.Contains(summary.Versions, "contract:1") {
		t.Fatalf("retry challenge lacks exact failure/contract bindings: %#v", summary)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1'`); got != 1 {
		t.Fatalf("approval challenge created %d attempts, want only the failed attempt", got)
	}
	// The retry route fabricates no failure record: exactly the one record
	// derived from the original terminal stands, and the challenge added
	// neither a dispatch nor a disposition.
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM domain_events WHERE subject_id='work-1' AND (kind='worker.dispatched' OR (kind='workflow.action_completed' AND json_extract(payload,'$.action_id')='record_worker_failure'))`); got != 1 {
		t.Fatalf("records and dispatches after the challenge = %d, want only the one derived record and no fabricated dispatch", got)
	}
	ref, ok := challenge.Error.Details["approval_ref"].(string)
	if !ok || ref == "" {
		t.Fatal("retry challenge has no approval reference")
	}
	approvedRaw := retryJSON(cloneWithApproval(t, retryInput, ref))
	scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "failed_attempt_id": failedID, "scope_version": scopeVersion}
	versions := map[string]any{"work": pin.Version, "contract": 1, "failed_attempt_epoch": 1}
	env.HostApproval = signedHostApproval(privateKey, ref, mutationDigest("concord_work_transition", "workflow_action", env, approvedRaw), scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), nonceForChallenge(ref))
	approved := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approvedRaw}, env)
	if approved.Outcome != OutcomeOK {
		t.Fatalf("approved fresh authorization: %+v", approved.Error)
	}
	var epoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id='work-1' AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' ORDER BY seq DESC LIMIT 1`, store.WorkflowActionStarted).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	if epoch != 2 {
		t.Fatalf("fresh authorization epoch=%d, want 2", epoch)
	}
	// The fresh authorization becomes a genuinely live attempt: its lane
	// dispatch evidence lands through the lane-actor seam, so the older
	// failed authorization cannot stand in for it and the no-stale-binding
	// property below reads the live attempt, not an authorized-but-unstarted
	// one.
	var freshDigest string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.worker_packet_digest') FROM domain_events WHERE subject_id='work-1' AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=? ORDER BY seq DESC LIMIT 1`, store.WorkflowActionCompleted, freshID).Scan(&freshDigest); err != nil {
		t.Fatal(err)
	}
	retryLaneDef := retryLane(t)
	freshDispatch := store.Event{EventID: "authorized-retry-dispatch", Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: store.WorkerEvidenceEventPayloadVersion(store.WorkerDispatched), Payload: retryJSON(store.WorkerDispatchedPayload{AttemptID: freshID, LaneID: retryLaneDef.ID, LaneVersion: retryLaneDef.Version, LaneDigest: retryLaneDef.Digest, CapabilityClass: retryLaneDef.CapabilityClass, PacketDigest: freshDigest, ReadbackModel: "openai/gpt-5.6-luna", PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion, WorkerJob: authorizedWorkerJob(t, s, freshID)})}
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		enriched, err := store.PrepareLaneActorDispatch(context.Background(), tx, freshDispatch, grant.PrincipalRef, grant.ClientRef)
		if err != nil {
			return err
		}
		_, err = store.AppendLaneActorDispatchTx(context.Background(), tx, enriched)
		return err
	}); err != nil {
		t.Fatalf("the fresh attempt's lane dispatch evidence: %v", err)
	}
	// The fresh authorization is the work's live attempt: the pin's attempt
	// projection names it at epoch 2, and the older failed authorization
	// cannot replace it. On the current head the derived record keeps the
	// correction window open until a satisfying acceptance, so the retry
	// binding still names the original failed attempt — the fresh attempt
	// itself is live and is never re-bound as failed.
	livePin, pinErr := store.ReadWorkPin(context.Background(), s, "work-1")
	if pinErr != nil || livePin.Attempt == nil || livePin.Attempt.ID != freshID || livePin.Attempt.Epoch != 2 {
		t.Fatalf("fresh live attempt pin=%+v err=%v, want %s at epoch 2", livePin.Attempt, pinErr, freshID)
	}
	var binding *store.WorkflowRetryApprovalBinding
	bindingErr := s.Transact(context.Background(), func(tx *store.Transaction) error {
		var txErr error
		binding, txErr = store.WorkflowFailedWorkerRetryBindingTx(context.Background(), tx, nil, "work-1")
		return txErr
	})
	if bindingErr != nil || binding == nil || binding.FailedAttemptID != failedID || binding.FailedAttemptEpoch != 1 {
		t.Fatalf("retry binding after the fresh live attempt = %+v err=%v, want the original failed attempt's open window, never the fresh attempt", binding, bindingErr)
	}
}

func TestWorkerRetryApprovalFencesConcurrentAbandonment(t *testing.T) {
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition", "worker_dispatch"})
	grant.Worktree = seedWorkerRetryMutationFixture(t, s, grant)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	const failedID = "attempt:work-1:concurrent-abandonment"
	recordReadyRetryJob(t, s, service, mutationEnvelope(grant, scopeVersion), "job:concurrent-abandonment")
	first := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{
		"work_id": "work-1", "expected_version": workOneVersion(t, s), "action_id": "dispatch_worker", "idempotency_key": "authorize-concurrent-abandonment",
		"fields": map[string]any{"attempt_id": failedID, "worker_packet": retryMutationPacket(t, s, failedID, nil)},
	})}, mutationEnvelope(grant, scopeVersion))
	if first.Outcome != OutcomeOK {
		t.Fatalf("first authorization: %+v", first.Error)
	}
	pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	scopeVersion, _, err = s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	const freshID = "attempt:work-1:concurrent-retry"
	input := map[string]any{
		"work_id": "work-1", "expected_version": pin.Version, "action_id": "dispatch_worker", "idempotency_key": "retry-concurrent-abandonment",
		"fields": map[string]any{"attempt_id": freshID, "worker_packet": retryMutationPacket(t, s, freshID, nil)},
	}
	originalProbe := service.ProjectHostProber
	probes := 0
	service.ProjectHostProber = func(ctx context.Context, directory, worktree string) (store.ResolvedProjectHost, error) {
		probes++
		// The mutation's host probe follows the initial retry-binding read;
		// the two dispatch identity probes precede it.
		if probes == 3 {
			// The concurrent abandonment carries the recorded host observation
			// and lands through the live worker-evidence seam; the derived
			// record opens the correction context mid-flight and bumps the
			// work version under the in-flight retry.
			failure := agentWorkerFailedAttributedEvent("concurrent-abandonment", store.WorkerFailedPayload{AttemptID: failedID, FailureKind: store.WorkerFailureAbandoned, Detail: "synthetic concurrent abandonment"})
			if err := s.Transact(ctx, func(tx *store.Transaction) error {
				_, err := store.ApplyWorkerEvidenceOperationTx(ctx, tx, failure)
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}
		return originalProbe(ctx, directory, worktree)
	}
	env := mutationEnvelope(grant, scopeVersion)
	refused := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(input)}, env)
	service.ProjectHostProber = originalProbe
	if probes != 3 {
		t.Fatalf("host probes=%d, want abandonment after the initial binding read", probes)
	}
	if refused.Error == nil || refused.Error.Kind != "version_conflict" {
		t.Fatalf("concurrent abandonment retry: outcome=%s error=%+v, want version_conflict", refused.Outcome, refused.Error)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1'`); got != 1 {
		t.Fatalf("refused retry created %d attempts, want 1", got)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM domain_events WHERE subject_id='work-1' AND kind='workflow.action_started' AND json_extract(payload,'$.action_id')='dispatch_worker'`); got != 1 {
		t.Fatalf("refused retry created %d dispatch epochs, want 1", got)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM domain_events WHERE subject_id='work-1' AND kind='worker.dispatched'`); got != 0 {
		t.Fatalf("refused retry fabricated %d dispatch events", got)
	}
	// The concurrent terminal derived exactly its own failure record.
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM domain_events WHERE subject_id='work-1' AND kind='workflow.action_completed' AND json_extract(payload,'$.action_id')='record_worker_failure'`); got != 1 {
		t.Fatalf("failure records after the concurrent terminal = %d, want exactly the one derived record", got)
	}
	pin, err = store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	if pin.Correction == nil || pin.Correction.FailedAttemptID != failedID {
		t.Fatalf("correction after the concurrent terminal = %#v, want the derived record's open window on %s", pin.Correction, failedID)
	}
	// The packet built before the concurrent terminal stands stale beside the
	// derived record's open correction. The real admission order puts the
	// approval wall in front of the packet admission, so the stale retry
	// challenges first — and then refuses with the exact approval signed:
	// correction consumption is enforced behind approval, never asserted by
	// an invented ordering.
	input["expected_version"] = pin.Version
	staleChallenge := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(input)}, env)
	if staleChallenge.Error == nil || staleChallenge.Error.Kind != "approval_required" {
		t.Fatalf("stale pre-terminal packet challenge = %+v, want approval_required before the packet admission", staleChallenge.Error)
	}
	staleRef, staleRefOK := staleChallenge.Error.Details["approval_ref"].(string)
	if !staleRefOK || staleRef == "" {
		t.Fatal("stale packet challenge has no approval reference")
	}
	staleApprovedRaw := retryJSON(cloneWithApproval(t, input, staleRef))
	staleScope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "failed_attempt_id": failedID, "scope_version": scopeVersion}
	staleVersions := map[string]any{"work": pin.Version, "contract": 1, "failed_attempt_epoch": 1}
	env.HostApproval = signedHostApproval(privateKey, staleRef, mutationDigest("concord_work_transition", "workflow_action", env, staleApprovedRaw), staleScope, staleVersions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), nonceForChallenge(staleRef))
	staleApproved := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: staleApprovedRaw}, env)
	if staleApproved.Error == nil || !strings.Contains(staleApproved.Error.Message, "worker_packet binding records work_version") {
		t.Fatalf("stale pre-terminal packet with exact approval = %+v, want the stale work-version binding refusal", staleApproved.Error)
	}
	// A version-current packet without the correction consumes nothing, and
	// the consumption gate refuses it with the exact approval signed —
	// correction consumption is enforced behind approval, never by an
	// invented ordering.
	input["fields"] = map[string]any{"attempt_id": freshID, "worker_packet": retryMutationPacket(t, s, freshID, nil)}
	unconsumingChallenge := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(input)}, env)
	if unconsumingChallenge.Error == nil || unconsumingChallenge.Error.Kind != "approval_required" {
		t.Fatalf("unconsuming packet challenge = %+v, want approval_required", unconsumingChallenge.Error)
	}
	unconsumingRef, unconsumingRefOK := unconsumingChallenge.Error.Details["approval_ref"].(string)
	if !unconsumingRefOK || unconsumingRef == "" {
		t.Fatal("unconsuming packet challenge has no approval reference")
	}
	unconsumingApprovedRaw := retryJSON(cloneWithApproval(t, input, unconsumingRef))
	env.HostApproval = signedHostApproval(privateKey, unconsumingRef, mutationDigest("concord_work_transition", "workflow_action", env, unconsumingApprovedRaw), staleScope, staleVersions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), nonceForChallenge(unconsumingRef))
	unconsumingApproved := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: unconsumingApprovedRaw}, env)
	if unconsumingApproved.Error == nil || !strings.Contains(unconsumingApproved.Error.Message, "does not consume the current correction context") {
		t.Fatalf("unconsuming packet with exact approval = %+v, want the correction-consumption refusal", unconsumingApproved.Error)
	}
	// The approved retry succeeds only through a re-read packet that consumes
	// the correction.
	input["fields"] = map[string]any{"attempt_id": freshID, "worker_packet": retryMutationPacket(t, s, freshID, pin.Correction)}
	challenge := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(input)}, env)
	if challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("reread retry: %+v, want approval_required", challenge.Error)
	}
	ref, ok := challenge.Error.Details["approval_ref"].(string)
	if !ok || ref == "" {
		t.Fatal("reread retry challenge has no approval reference")
	}
	summary := challenge.Error.ConsequenceSummary
	if summary == nil || !slices.Contains(summary.Scope, "failed_attempt_id:"+failedID) || !slices.Contains(summary.Versions, "failed_attempt_epoch:1") || !slices.Contains(summary.Versions, "contract:1") {
		t.Fatalf("reread retry challenge lacks exact bindings: %#v", summary)
	}
	approvedRaw := retryJSON(cloneWithApproval(t, input, ref))
	scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "failed_attempt_id": failedID, "scope_version": scopeVersion}
	versions := map[string]any{"work": pin.Version, "contract": 1, "failed_attempt_epoch": 1}
	env.HostApproval = signedHostApproval(privateKey, ref, mutationDigest("concord_work_transition", "workflow_action", env, approvedRaw), scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), nonceForChallenge(ref))
	approved := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approvedRaw}, env)
	if approved.Outcome != OutcomeOK {
		t.Fatalf("exact approved retry: %+v", approved.Error)
	}
	pin, err = store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil || pin.Attempt == nil || pin.Attempt.ID != freshID || pin.Attempt.Epoch != 2 {
		t.Fatalf("approved retry pin=%#v err=%v, want fresh epoch 2", pin.Attempt, err)
	}
}

func seedWorkerRetryMutationFixture(t *testing.T, s *store.Store, grant Authority) string {
	t.Helper()
	// A failed retry below the wall asks for the operator's approval only
	// under the legacy approval policy; consequence_v1 revalidates the
	// binding instead.
	return seedWorkerRetryMutationFixtureAt(t, s, grant, legacyApprovalPolicyDefinitionForRef(t, "workflow.implementation"))
}

// seedWorkerRetryMutationFixtureAt shares the execution/worktree setup across
// registered pins without changing the legacy consent fixture's default.
func seedWorkerRetryMutationFixtureAt(t *testing.T, s *store.Store, grant Authority, registered store.RegisteredDefinition) string {
	t.Helper()
	if got := seedAgentWorkflowAt(t, s, grant, registered); got != 4 {
		t.Fatalf("workflow seed version=%d, want 4", got)
	}
	owner := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	ownerRef, err := store.WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		UPDATE workflow_instances SET current_step='execution' WHERE work_id='work-1';
		INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES('work-1',1,'approved retry objective','internal_sqlite','[]','[]','now',?,'[]','[]',0,'prototype_internal');
		INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES('work-1',1,'predicate:retry-objective',0,'check','{"kind":"check","check_ref":"check:retry-objective","immutable_subject_ref":"commit:retry-objective","expected_result":"pass"}');
		DELETE FROM fold_guard`, ownerRef); err != nil {
		t.Fatalf("seed retry projections: %v", err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at) VALUES(?,?,?,?,?,?,?,'active',?);
		DELETE FROM fold_guard`, store.WorktreeSetID("work-1"), "project-1", "claim:retry", "retry", strings.Repeat("a", 40), path, "repo:retry", fixedTime().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seed retry worktree: %v", err)
	}
	var claimed int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worktree_entries WHERE set_id=? AND state='active'`, store.WorktreeSetID("work-1")).Scan(&claimed); err != nil || claimed != 1 {
		t.Fatalf("retry worktree claim count=%d err=%v", claimed, err)
	}
	return path
}

// agentWorkerFailureAttribution is the recorded producer observation the
// promoted definition requires on a failed terminal: the host boundary
// observed the failure with an unknown cause and basis, citing the one host
// event it saw. The block carries origin and observation alone — the store
// derives the attribution identity from the attempt's own authorized
// dispatch window, and no fixture ever authors one.
func agentWorkerFailureAttribution() *store.WorkerFailureAttribution {
	return &store.WorkerFailureAttribution{
		Origin: store.WorkerFailureAttributionRecorded,
		Observation: &store.WorkerFailureObservation{
			Observer: store.WorkerFailureObserverHostAdapter,
			Phase:    store.WorkerFailurePhaseHostExecution,
			Cause:    store.WorkerFailureCauseUnknown,
			Basis:    store.WorkerFailureBasisUnknown,
			Sources: []store.WorkerFailureSource{{
				Kind:    store.WorkerFailureSourceHostEvent,
				Locator: "evt-agent-host-0001",
				Digest:  "sha256:" + strings.Repeat("a", 64),
				Host: &store.WorkerFailureHostObservation{
					Tag:       store.WorkerFailureHostTagCancellation,
					EventType: "session.status",
					SessionID: "ses-agent-fixture",
				},
			}},
		},
	}
}

// agentWorkerFailedAttributedEvent builds one current-version worker.failed
// event carrying the recorded observation, for the fixtures whose terminal
// fact lands through the live worker-evidence seam on a promoted pin.
func agentWorkerFailedAttributedEvent(eventID string, payload store.WorkerFailedPayload) store.Event {
	payload.FailureAttribution = agentWorkerFailureAttribution()
	return store.Event{EventID: eventID, Kind: store.WorkerFailed, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: store.WorkerEvidenceEventPayloadVersion(store.WorkerFailed), Payload: retryJSON(payload)}
}

func seedFailedWorkerRetryMutation(t *testing.T, s *store.Store, service *Service, grant Authority) (int64, string, string) {
	t.Helper()
	path := seedWorkerRetryMutationFixture(t, s, grant)
	grant.Worktree = path
	attemptID := "attempt:work-1:failed"
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	start := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: json.RawMessage(`{"work_id":"work-1","expected_version":4,"action_id":"start_execution","idempotency_key":"retry-start"}`)}, mutationEnvelope(grant, scopeVersion))
	if start.Outcome != OutcomeOK {
		t.Fatalf("seed retry start: %+v", start.Error)
	}
	recordReadyRetryJob(t, s, service, mutationEnvelope(grant, scopeVersion), "job:retry-objective")
	// The canonical head derives the workflow failure record from the
	// attempt's own terminal event (CD-0198 D5): the public dispatch opens
	// the fenced window and the live worker-evidence seam lands the
	// terminal fact with the record it owes, exactly once. The worker
	// session's own lane dispatch precedes its terminal fact.
	env := mutationEnvelope(grant, scopeVersion)
	version := workVersion(t, s, "work-1")
	dispatch := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{
		"work_id": "work-1", "expected_version": version, "action_id": "dispatch_worker", "idempotency_key": "retry-failed-dispatch",
		"fields": map[string]any{"attempt_id": attemptID, "worker_packet": retryMutationPacket(t, s, attemptID, nil)},
	})}, env)
	if dispatch.Outcome != OutcomeOK {
		t.Fatalf("seed retry dispatch: %+v", dispatch.Error)
	}
	lane := retryLane(t)
	job := authorizedWorkerJob(t, s, attemptID)
	laneDispatch := store.Event{EventID: "retry-dispatch", Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: retryJSON(store.WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, PacketDigest: "sha256:" + strings.Repeat("b", 64), ReadbackModel: "openai/gpt-5.6-luna", PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion, WorkerJob: job})}
	failure := agentWorkerFailedAttributedEvent("retry-failed", store.WorkerFailedPayload{AttemptID: attemptID, ReadbackModel: "openai/gpt-5.6-luna", FailureKind: store.WorkerFailureWorkerError, Detail: "synthetic failure"})
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		enriched, err := store.PrepareLaneActorDispatch(context.Background(), tx, laneDispatch, grant.PrincipalRef, grant.ClientRef)
		if err != nil {
			return err
		}
		if _, err := store.AppendLaneActorDispatchTx(context.Background(), tx, enriched); err != nil {
			return err
		}
		_, err = store.ApplyWorkerEvidenceOperationTx(context.Background(), tx, failure)
		return err
	}); err != nil {
		t.Fatalf("seed retry failure: %v", err)
	}
	var binding *store.WorkflowRetryApprovalBinding
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		var txErr error
		binding, txErr = store.WorkflowFailedWorkerRetryBindingTx(context.Background(), tx, nil, "work-1")
		return txErr
	}); err != nil || binding == nil {
		t.Fatalf("retry binding=%+v err=%v", binding, err)
	}
	var failedVersion int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-1'`).Scan(&failedVersion); err != nil {
		t.Fatal(err)
	}
	return failedVersion, attemptID, path
}

func retryJSON(value any) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}

func retryMutationPacket(t *testing.T, s *store.Store, attemptID string, correction *store.WorkflowCorrectionContext) map[string]any {
	t.Helper()
	lane := retryLane(t)
	inputs := map[string]any{"task": "retry the approved objective", "constraints": []string{"preserve the approved contract"}}
	if correction != nil {
		inputs["correction"] = correction
	}
	return bindPacketToRecordedState(t, s, map[string]any{"schema_version": store.WorkerPacketSchemaVersion, "attempt_id": attemptID, "lane_id": lane.ID, "lane_version": lane.Version, "lane_digest": lane.Digest, "work_id": "work-1", "step_id": "execution", "inputs": inputs})
}

func retryLane(t *testing.T) store.LaneDefinition {
	t.Helper()
	for _, lane := range store.BuiltinLaneDefinitions() {
		if lane.ID == "implement" {
			return lane
		}
	}
	t.Fatal("implement lane is not registered")
	return store.LaneDefinition{}
}

func cloneWithApproval(t *testing.T, input map[string]any, ref string) map[string]any {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := injectApproval(raw, ref)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(approved, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

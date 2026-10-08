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
	scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "failed_attempt_id": attemptID, "scope_version": scopeVersion}
	versions := map[string]any{"work": version, "contract": 1, "failed_attempt_epoch": 1}
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
	env.HostApproval = signedHostApproval(privateKey, freshRef, mutationDigest("concord_work_transition", "workflow_action", env, freshApprovedRaw), scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), nonceForChallenge(freshRef))
	valid := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: freshApprovedRaw}, env)
	if valid.Outcome != OutcomeOK {
		t.Fatalf("fresh approved retry = %+v", valid.Error)
	}
	var retryEpoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE kind=? AND subject_id='work-1' AND json_extract(payload,'$.action_id')='dispatch_worker' ORDER BY seq DESC LIMIT 1`, store.WorkflowActionStarted).Scan(&retryEpoch); err != nil {
		t.Fatal(err)
	}
	if retryEpoch != 2 {
		t.Fatalf("fresh approved retry epoch=%d, want 2", retryEpoch)
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
		"work_id": "work-1", "expected_version": retryWorkVersion(t, s), "action_id": "dispatch_worker", "idempotency_key": "authorize-unstarted-worker",
		"fields": map[string]any{"attempt_id": failedID, "worker_packet": retryMutationPacket(t, s, failedID, nil)},
	})}, env)
	if first.Outcome != OutcomeOK {
		t.Fatalf("first authorization: %+v", first.Error)
	}
	failure := store.Event{EventID: "abandon-unstarted-worker", Kind: store.WorkerFailed, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: retryJSON(store.WorkerFailedPayload{AttemptID: failedID, FailureKind: store.WorkerFailureAbandoned, Detail: "no admissible dispatch evidence"})}
	if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{failure}}); err != nil {
		t.Fatalf("abandon authorization: %v", err)
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
	retryInput := map[string]any{
		"work_id": "work-1", "expected_version": pin.Version, "action_id": "dispatch_worker", "idempotency_key": "retry-unstarted-worker",
		"fields": map[string]any{"attempt_id": freshID, "worker_packet": retryMutationPacket(t, s, freshID, nil)},
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
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM domain_events WHERE subject_id='work-1' AND (kind='worker.dispatched' OR (kind='workflow.action_completed' AND json_extract(payload,'$.action_id')='record_worker_failure'))`); got != 0 {
		t.Fatalf("approval challenge fabricated %d dispatches or dispositions", got)
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
	if binding, err := store.WorkflowFailedWorkerRetryBinding(context.Background(), s, nil, "work-1"); err != nil || binding != nil {
		t.Fatalf("older failed authorization replaced the fresh live attempt: binding=%#v error=%v", binding, err)
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
		"work_id": "work-1", "expected_version": retryWorkVersion(t, s), "action_id": "dispatch_worker", "idempotency_key": "authorize-concurrent-abandonment",
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
			failure := store.Event{EventID: "concurrent-abandonment", Kind: store.WorkerFailed, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: retryJSON(store.WorkerFailedPayload{AttemptID: failedID, FailureKind: store.WorkerFailureAbandoned, Detail: "synthetic concurrent abandonment"})}
			if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{failure}}); err != nil {
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
	pin, err = store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	input["expected_version"] = pin.Version
	input["fields"] = map[string]any{"attempt_id": freshID, "worker_packet": retryMutationPacket(t, s, freshID, nil)}
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
	if got := seedAgentWorkflow(t, s, grant); got != 4 {
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

func seedFailedWorkerRetryMutation(t *testing.T, s *store.Store, service *Service, grant Authority) (int64, string, string) {
	t.Helper()
	path := seedWorkerRetryMutationFixture(t, s, grant)
	lane := retryLane(t)
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
	dispatch := store.Event{EventID: "retry-dispatch", Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: retryJSON(store.WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, PacketDigest: "sha256:" + strings.Repeat("b", 64), ReadbackModel: "openai/gpt-5.6-luna", PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion})}
	failure := store.Event{EventID: "retry-failed", Kind: store.WorkerFailed, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: retryJSON(store.WorkerFailedPayload{AttemptID: attemptID, ReadbackModel: "openai/gpt-5.6-luna", FailureKind: store.WorkerFailureWorkerError, Detail: "synthetic failure"})}
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		enriched, err := store.PrepareLaneActorDispatch(context.Background(), tx, dispatch, grant.PrincipalRef, grant.ClientRef)
		if err != nil {
			return err
		}
		if _, err := store.AppendLaneActorDispatchTx(context.Background(), tx, enriched); err != nil {
			return err
		}
		_, err = store.ApplyOperationTx(context.Background(), tx, store.Operation{Events: []store.Event{failure}})
		return err
	}); err != nil {
		t.Fatalf("seed retry failure: %v", err)
	}
	var failureVersion int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-1'`).Scan(&failureVersion); err != nil {
		t.Fatal(err)
	}
	scopeVersion, _, err = s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	record := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{"work_id": "work-1", "expected_version": failureVersion, "action_id": "record_worker_failure", "fields": map[string]any{"attempt_id": attemptID, "attempt_epoch": 1}, "idempotency_key": "retry-record-failure"})}, mutationEnvelope(grant, scopeVersion))
	if record.Outcome != OutcomeOK {
		t.Fatalf("seed retry failure record: %+v", record.Error)
	}
	if binding, err := store.WorkflowFailedWorkerRetryBinding(context.Background(), s, nil, "work-1"); err != nil || binding == nil {
		t.Fatalf("retry binding=%+v err=%v", binding, err)
	}
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-1'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version, attemptID, path
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
	return bindPacketToRecordedState(t, s, map[string]any{"schema_version": "1.0", "attempt_id": attemptID, "lane_id": lane.ID, "lane_version": lane.Version, "lane_digest": lane.Digest, "work_id": "work-1", "step_id": "execution", "inputs": inputs})
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

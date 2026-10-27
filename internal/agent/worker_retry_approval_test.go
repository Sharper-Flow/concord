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

func TestWorkerRetryWithoutConsentKeepsFreshIdentityAndReplay(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition", "worker_dispatch"})
	version, failedID, worktree := seedFailedWorkerRetryMutation(t, s, service, grant)
	attemptsBefore := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1'`)
	grant.Worktree = worktree
	pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil || pin.Correction == nil {
		t.Fatalf("read correction: pin=%+v err=%v", pin, err)
	}
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	input := map[string]any{
		"work_id": "work-1", "expected_version": version, "action_id": "dispatch_worker", "idempotency_key": "retry-failed-identity",
		"fields": map[string]any{"attempt_id": failedID, "worker_packet": retryMutationPacket(t, s, failedID, pin.Correction)},
	}
	refused := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(input)}, env)
	if refused.Error == nil || !strings.Contains(refused.Error.Message, "fresh attempt identity") {
		t.Fatalf("reused failed identity = %+v, want fresh-identity refusal", refused.Error)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1'`); got != attemptsBefore {
		t.Fatalf("reused identity left %d attempts, want %d", got, attemptsBefore)
	}
	input["idempotency_key"] = "retry-fresh-identity"
	input["fields"] = map[string]any{"attempt_id": "attempt:work-1:fresh", "worker_packet": retryMutationPacket(t, s, "attempt:work-1:fresh", pin.Correction)}
	raw := retryJSON(input)
	result := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if result.Outcome != OutcomeOK {
		t.Fatalf("fresh retry without consent = %+v", result.Error)
	}
	replayed := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if replayed.Outcome != OutcomeOK || !replayed.Replayed {
		t.Fatalf("retry replay = %+v", replayed)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1'`); got != attemptsBefore+1 {
		t.Fatalf("fresh retry and replay left %d attempts, want %d", got, attemptsBefore+1)
	}
	if got := verifyWallChallenges(t, s); got != 0 {
		t.Fatalf("retry minted %d approval challenges", got)
	}
	var epoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM worker_authorizations WHERE work_id='work-1' AND attempt_id='attempt:work-1:fresh'`).Scan(&epoch); err != nil || epoch != pin.Correction.FailedAttemptEpoch+1 {
		t.Fatalf("fresh retry epoch=%d err=%v, want %d", epoch, err, pin.Correction.FailedAttemptEpoch+1)
	}
}

func TestWorkerRetryRequiresTerminalReconciliationAndCurrentPacket(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition", "worker_dispatch"})
	grant.Worktree = seedWorkerRetryMutationFixture(t, s, grant)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	const failedID = "attempt:work-1:concurrent-abandonment"
	recordReviewedRetryJob(t, s, service, env, "job:concurrent-abandonment")
	first := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{
		"work_id": "work-1", "expected_version": workOneVersion(t, s), "action_id": "dispatch_worker", "idempotency_key": "authorize-concurrent-abandonment",
		"fields": map[string]any{"attempt_id": failedID, "worker_packet": retryMutationPacket(t, s, failedID, nil)},
	})}, env)
	if first.Outcome != OutcomeOK {
		t.Fatalf("first authorization: %+v", first.Error)
	}
	const freshID = "attempt:work-1:concurrent-retry"
	input := map[string]any{
		"work_id": "work-1", "expected_version": workOneVersion(t, s), "action_id": "dispatch_worker", "idempotency_key": "retry-concurrent-abandonment",
		"fields": map[string]any{"attempt_id": freshID, "worker_packet": retryMutationPacket(t, s, freshID, nil)},
	}
	pending := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(input)}, env)
	if pending.Error == nil || pending.Error.Kind != "operation_conflict" || !strings.Contains(pending.Error.Message, "unresolved launch outcome") {
		t.Fatalf("unresolved authorization=%+v, want reconciliation", pending.Error)
	}
	failure := agentWorkerFailedAttributedEvent("concurrent-abandonment", store.WorkerFailedPayload{AttemptID: failedID, FailureKind: store.WorkerFailureAbandoned, Detail: "synthetic concurrent abandonment"})
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		_, err := store.ApplyWorkerEvidenceOperationTx(context.Background(), tx, failure)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	refused := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(input)}, env)
	if refused.Error == nil || refused.Error.Kind != "version_conflict" {
		t.Fatalf("terminal changed the work version: %+v, want version_conflict", refused.Error)
	}
	pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil || pin.Correction == nil || pin.Correction.FailedAttemptID != failedID {
		t.Fatalf("terminal correction=%+v err=%v", pin, err)
	}
	input["expected_version"] = pin.Version
	stale := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(input)}, env)
	if stale.Error == nil || !strings.Contains(stale.Error.Message, "worker_packet binding records work_version") {
		t.Fatalf("stale pre-terminal packet=%+v", stale.Error)
	}
	input["fields"] = map[string]any{"attempt_id": freshID, "worker_packet": retryMutationPacket(t, s, freshID, nil)}
	unconsuming := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(input)}, env)
	if unconsuming.Error == nil || !strings.Contains(unconsuming.Error.Message, "does not consume the current correction context") {
		t.Fatalf("unconsuming packet=%+v", unconsuming.Error)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1' AND capability_class='implementation'`); got != 1 {
		t.Fatalf("refused retry created %d attempts, want 1", got)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempt_dispositions WHERE work_id='work-1' AND action_id='record_worker_failure'`); got != 1 {
		t.Fatalf("terminal derived %d failure records, want 1", got)
	}
	input["fields"] = map[string]any{"attempt_id": freshID, "worker_packet": retryMutationPacket(t, s, freshID, pin.Correction)}
	result := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(input)}, env)
	if result.Outcome != OutcomeOK {
		t.Fatalf("reread consuming retry=%+v", result.Error)
	}
	if got := verifyWallChallenges(t, s); got != 0 {
		t.Fatalf("retry minted %d consent challenges", got)
	}
}

func seedWorkerRetryMutationFixture(t *testing.T, s *store.Store, grant Authority) string {
	t.Helper()
	registered, err := store.BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	return seedWorkerRetryMutationFixtureAt(t, s, grant, registered)
}

// recordReviewedRetryJob records one ready job and accepts the architecture
// Review that binds it; continue leaves the job ready for implementation
// (CD-0205 D3).
func recordReviewedRetryJob(t *testing.T, s *store.Store, service *Service, env CallEnvelope, jobID string) {
	t.Helper()
	recordReadyRetryJob(t, s, service, env, jobID)
	oAgentReview(t, oAgentFixture(t, s, service, env, "work-1"), "continue", false)
}

func seedWorkerRetryMutationFixtureAt(t *testing.T, s *store.Store, grant Authority, registered store.RegisteredDefinition) string {
	t.Helper()
	if got := seedAgentWorkflowAt(t, s, grant, registered); got != 4 {
		t.Fatalf("workflow seed version=%d, want 4", got)
	}
	owner := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	approvalStep := ""
	for _, step := range registered.Definition.StepGraph.Steps {
		if slices.Contains(step.Actions, "approve_contract") {
			approvalStep = step.ID
			break
		}
	}
	if approvalStep == "" {
		t.Fatal("retry fixture definition has no contract approval step")
	}
	path := t.TempDir()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
		UPDATE workflow_instances SET current_step=? WHERE work_id='work-1';
		DELETE FROM write_guard`, approvalStep); err != nil {
		t.Fatalf("seed retry projections: %v", err)
	}
	fields := workflowContractFieldsFixture()
	fields["premise"] = "approved retry objective"
	fields["outcome_predicates"] = []map[string]any{{"predicate_id": "predicate:retry-objective", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:retry-objective", "immutable_subject_ref": "commit:retry-objective", "expected_result": "pass"}}}
	fields["architecture_binding"] = workflowArchitectureBindingForSeededRegistry(t, s)
	runVerificationStoreAction(t, s, "approve_contract", fields, owner, "retry-fixture")
	runVerificationStoreAction(t, s, "record_design", workflowDesignFieldsFixture("retry-fixture"), owner, "retry-fixture-design")
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
		INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at) VALUES(?,?,?,?,?,?,?,'active',?);
		DELETE FROM write_guard`, store.WorktreeSetID("work-1"), "project-1", "claim:retry", "retry", strings.Repeat("a", 40), path, "repo:retry", fixedTime().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seed retry worktree: %v", err)
	}
	var claimed int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worktree_entries WHERE set_id=? AND state='active'`, store.WorktreeSetID("work-1")).Scan(&claimed); err != nil || claimed != 1 {
		t.Fatalf("retry worktree claim count=%d err=%v", claimed, err)
	}
	return path
}

// agentWorkerFailureAttribution carries the host's observation. The store
// derives its identity from the authorized dispatch window.
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

func agentWorkerFailedAttributedEvent(eventID string, payload store.WorkerFailedPayload) store.Event {
	payload.FailureAttribution = agentWorkerFailureAttribution()
	return store.Event{EventID: eventID, Kind: store.WorkerFailed, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion(store.WorkerFailed), Payload: retryJSON(payload)}
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
	start := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{"work_id": "work-1", "expected_version": workOneVersion(t, s), "action_id": "start_execution", "idempotency_key": "retry-start"})}, mutationEnvelope(grant, scopeVersion))
	if start.Outcome != OutcomeOK {
		t.Fatalf("seed retry start: %+v", start.Error)
	}
	recordReviewedRetryJob(t, s, service, mutationEnvelope(grant, scopeVersion), "job:retry-objective")
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
	laneDispatch := store.Event{EventID: "retry-dispatch", Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion(store.WorkerDispatched), Payload: retryJSON(store.WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, PacketDigest: "sha256:" + strings.Repeat("b", 64), ReadbackModel: "openai/gpt-5.6-luna", PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion, WorkerJob: job})}
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
	return bindPacketToRecordedState(t, s, map[string]any{"schema_version": store.WorkerPacketSchemaVersion, "attempt_id": attemptID, "lane_id": lane.ID, "lane_digest": lane.Digest, "work_id": "work-1", "step_id": "execution", "inputs": inputs})
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

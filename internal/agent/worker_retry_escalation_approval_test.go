package agent

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// TestEscalatedCorrectionRetryMintsBindableChallenge pins the repaired
// escalation wall: dispatch_worker against a correction at the three-attempt
// limit mints the standard approval challenge, and one operator approval
// admits exactly one fresh fenced attempt of the unchanged approved contract.
// Before the repair this dispatch returned approval_required with no approval
// reference, so the correction could never leave the limit.
func TestEscalatedCorrectionRetryMintsBindableChallengeAndAdmitsOneAttempt(t *testing.T) {
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition", "worker_dispatch"})
	version, failedAttemptID, worktree := seedEscalatedFailedWorkerMutation(t, s, service, grant)
	grant.Worktree = worktree
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	retryAttemptID := "attempt:work-1:escalated-4"
	pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil || pin.Correction == nil || !pin.Correction.Escalated {
		t.Fatalf("read escalated correction: pin=%#v err=%v", pin.Correction, err)
	}
	input := map[string]any{
		"work_id": "work-1", "expected_version": version, "action_id": "dispatch_worker",
		"idempotency_key": "escalated-retry-1", "fields": map[string]any{"attempt_id": retryAttemptID, "worker_packet": retryMutationPacket(t, "work-1", "execution", retryAttemptID, pin.Correction)},
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	challenge := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("escalated retry without approval = %+v, want approval_required", challenge.Error)
	}
	details := challenge.Error.Details
	challengeRef, _ := details["approval_ref"].(string)
	if len(challengeRef) != 64 {
		t.Fatalf("escalated challenge carries no approval reference: %+v", details)
	}
	if got, _ := details["operation_digest"].(string); got == "" {
		t.Fatalf("escalated challenge carries no operation digest: %+v", details)
	}
	if got, _ := details["work_id"].(string); got != "work-1" {
		t.Fatalf("escalated challenge work_id = %v", details["work_id"])
	}
	if got, _ := details["action_id"].(string); got != "dispatch_worker" {
		t.Fatalf("escalated challenge action_id = %v", details["action_id"])
	}
	if got, _ := details["contract_version"].(string); got != "1" {
		t.Fatalf("escalated challenge contract_version = %v, want the approved contract version", details["contract_version"])
	}
	if got, _ := details["premise_summary"].(string); got != "approved retry objective" {
		t.Fatalf("escalated challenge premise_summary = %q, want the approved contract premise", details["premise_summary"])
	}
	assertBindingContains(t, details["scope"], "failed_attempt_id:"+failedAttemptID)
	assertBindingContains(t, details["versions"], "failed_attempt_epoch:3")
	assertBindingContains(t, details["versions"], "contract:1")
	assertBindingContains(t, details["versions"], "work:"+strconv.FormatInt(version, 10))
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1'`); got != 3 {
		t.Fatalf("challenge created %d worker attempts, want 3", got)
	}

	approvedInput := cloneWithApproval(t, input, challengeRef)
	approvedRaw, _ := json.Marshal(approvedInput)
	scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "failed_attempt_id": failedAttemptID, "scope_version": scopeVersion}
	versions := map[string]any{"work": version, "contract": int64(1), "failed_attempt_epoch": int64(3)}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, mutationDigest("concord_work_transition", "workflow_action", env, approvedRaw), scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), nonceForChallenge(challengeRef))
	approved := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approvedRaw}, env)
	if approved.Outcome != OutcomeOK {
		t.Fatalf("approved escalated retry = %+v", approved.Error)
	}
	var retryEpoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE kind=? AND subject_id='work-1' AND json_extract(payload,'$.action_id')='dispatch_worker' ORDER BY seq DESC LIMIT 1`, store.WorkflowActionStarted).Scan(&retryEpoch); err != nil {
		t.Fatal(err)
	}
	if retryEpoch != 4 {
		t.Fatalf("approved escalated retry epoch=%d, want 4", retryEpoch)
	}
}

// TestEscalatedCorrectionRetryRefusesStaleAndReusedApproval pins the fence
// around the approvable wall: a stale or reused approval has no effect, and
// the wall re-arms so the next attempt needs a fresh operator decision.
func TestEscalatedCorrectionRetryRefusesStaleAndReusedApproval(t *testing.T) {
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition", "worker_dispatch"})
	version, failedAttemptID, worktree := seedEscalatedFailedWorkerMutation(t, s, service, grant)
	grant.Worktree = worktree
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	retryAttemptID := "attempt:work-1:escalated-4"
	pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil || pin.Correction == nil {
		t.Fatalf("read escalated correction: pin=%#v err=%v", pin.Correction, err)
	}
	input := map[string]any{
		"work_id": "work-1", "expected_version": version, "action_id": "dispatch_worker",
		"idempotency_key": "escalated-retry-stale", "fields": map[string]any{"attempt_id": retryAttemptID, "worker_packet": retryMutationPacket(t, "work-1", "execution", retryAttemptID, pin.Correction)},
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	challenge := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	challengeRef, _ := challenge.Error.Details["approval_ref"].(string)
	if challengeRef == "" {
		t.Fatalf("escalated challenge = %+v", challenge.Error)
	}

	// A stale approval names a superseded failed attempt epoch. The boundary
	// compares the signed bindings against the current failed attempt, so the
	// mismatch refuses with no durable effect.
	bindingScope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "failed_attempt_id": failedAttemptID, "scope_version": scopeVersion}
	staleVersions := map[string]any{"work": version, "contract": int64(1), "failed_attempt_epoch": int64(1)}
	staleInput := cloneWithApproval(t, input, challengeRef)
	staleRaw, _ := json.Marshal(staleInput)
	env.HostApproval = signedHostApproval(privateKey, challengeRef, mutationDigest("concord_work_transition", "workflow_action", env, staleRaw), bindingScope, staleVersions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), nonceForChallenge(challengeRef))
	stale := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: staleRaw}, env)
	if stale.Outcome != OutcomeError || stale.Error == nil || !strings.Contains(stale.Error.Message, "scope or versions invalid") {
		t.Fatalf("stale approval = %+v, want a stale-binding refusal", stale.Error)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1'`); got != 3 {
		t.Fatalf("stale approval created %d worker attempts, want 3", got)
	}

	// Consume the challenge with a valid approval, fail the retry, and reuse
	// the spent challenge on the next escalated correction. The spent
	// challenge refuses with no new attempt even though the approval matches
	// the current binding.
	approvedVersions := map[string]any{"work": version, "contract": int64(1), "failed_attempt_epoch": int64(3)}
	approvedInput := cloneWithApproval(t, input, challengeRef)
	approvedRaw, _ := json.Marshal(approvedInput)
	env.HostApproval = signedHostApproval(privateKey, challengeRef, mutationDigest("concord_work_transition", "workflow_action", env, approvedRaw), bindingScope, approvedVersions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), nonceForChallenge(challengeRef))
	approved := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approvedRaw}, env)
	if approved.Outcome != OutcomeOK {
		t.Fatalf("approved escalated retry = %+v", approved.Error)
	}
	recordFailureForEscalatedRetry(t, s, service, grant, retryAttemptID, 4)

	nextAttemptID := "attempt:work-1:escalated-5"
	nextPin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil || nextPin.Correction == nil || nextPin.Correction.FailedAttemptID != retryAttemptID || !nextPin.Correction.Escalated {
		t.Fatalf("reread escalated correction after retry: pin=%#v err=%v", nextPin.Correction, err)
	}
	version = workVersion(t, s, "work-1")
	reusedInput := map[string]any{
		"work_id": "work-1", "expected_version": version, "action_id": "dispatch_worker",
		"idempotency_key": "escalated-retry-reuse", "fields": map[string]any{"attempt_id": nextAttemptID, "worker_packet": retryMutationPacket(t, "work-1", "execution", nextAttemptID, nextPin.Correction)},
	}
	withSpentApproval := cloneWithApproval(t, reusedInput, challengeRef)
	reusedRaw, _ := json.Marshal(withSpentApproval)
	currentScope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "failed_attempt_id": retryAttemptID, "scope_version": scopeVersion}
	currentVersions := map[string]any{"work": version, "contract": int64(1), "failed_attempt_epoch": int64(4)}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, mutationDigest("concord_work_transition", "workflow_action", env, reusedRaw), currentScope, currentVersions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), nonceForChallenge(challengeRef))
	reusedResponse := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: reusedRaw}, env)
	if reusedResponse.Outcome != OutcomeError || reusedResponse.Error == nil || !strings.Contains(reusedResponse.Error.Message, "approval challenge binding invalid") {
		t.Fatalf("reused approval = %+v, want an approval-binding refusal", reusedResponse.Error)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1'`); got != 4 {
		t.Fatalf("reused approval created %d worker attempts, want 4", got)
	}

	// The wall re-arms: a fresh request mints a fresh challenge bound to the
	// new failed attempt, and its approval admits the next fenced attempt.
	freshInput := map[string]any{
		"work_id": "work-1", "expected_version": version, "action_id": "dispatch_worker",
		"idempotency_key": "escalated-retry-fresh", "fields": map[string]any{"attempt_id": nextAttemptID, "worker_packet": retryMutationPacket(t, "work-1", "execution", nextAttemptID, nextPin.Correction)},
	}
	freshRaw, _ := json.Marshal(freshInput)
	freshChallenge := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: freshRaw}, env)
	freshRef, _ := freshChallenge.Error.Details["approval_ref"].(string)
	if freshRef == "" || freshRef == challengeRef {
		t.Fatalf("fresh escalated challenge = %+v, want a new approval reference", freshChallenge.Error)
	}
	if got, _ := freshChallenge.Error.Details["premise_summary"].(string); got != "approved retry objective" {
		t.Fatalf("fresh escalated challenge premise_summary = %q", freshChallenge.Error.Details["premise_summary"])
	}
	freshApproved := cloneWithApproval(t, freshInput, freshRef)
	freshApprovedRaw, _ := json.Marshal(freshApproved)
	freshScope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "failed_attempt_id": retryAttemptID, "scope_version": scopeVersion}
	freshVersions := map[string]any{"work": version, "contract": int64(1), "failed_attempt_epoch": int64(4)}
	env.HostApproval = signedHostApproval(privateKey, freshRef, mutationDigest("concord_work_transition", "workflow_action", env, freshApprovedRaw), freshScope, freshVersions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), nonceForChallenge(freshRef))
	valid := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: freshApprovedRaw}, env)
	if valid.Outcome != OutcomeOK {
		t.Fatalf("fresh approved escalated retry = %+v", valid.Error)
	}
}

func assertBindingContains(t *testing.T, bindings any, want string) {
	t.Helper()
	list, ok := bindings.([]string)
	if !ok {
		raw, err := json.Marshal(bindings)
		if err != nil {
			t.Fatalf("bindings %+v marshal: %v", bindings, err)
		}
		var decoded []string
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("challenge bindings are not a string list: %+v", bindings)
		}
		list = decoded
	}
	for _, binding := range list {
		if binding == want {
			return
		}
	}
	t.Fatalf("challenge bindings %v omit %q", list, want)
}

// seedEscalatedFailedWorkerMutation drives three full failed correction
// attempts through the mutation boundary, so the fourth dispatch faces the
// escalated wall with a durable failed attempt identity to bind.
func seedEscalatedFailedWorkerMutation(t *testing.T, s *store.Store, service *Service, grant Authority) (int64, string, string) {
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
		t.Fatalf("seed escalated projections: %v", err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at) VALUES(?,?,?,?,?,?,?,'active',?);
		DELETE FROM fold_guard`, store.WorktreeSetID("work-1"), "project-1", "claim:escalated", "escalated", strings.Repeat("a", 40), path, "repo:escalated", fixedTime().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seed escalated worktree: %v", err)
	}
	var claimed int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worktree_entries WHERE set_id=? AND state='active'`, store.WorktreeSetID("work-1")).Scan(&claimed); err != nil || claimed != 1 {
		t.Fatalf("escalated worktree claim count=%d err=%v", claimed, err)
	}
	version := int64(4)
	attemptID := ""
	for cycle := int64(1); cycle <= 3; cycle++ {
		attemptID = "attempt:work-1:escalated-" + strconv.FormatInt(cycle, 10)
		scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
		if err != nil {
			t.Fatal(err)
		}
		start := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: json.RawMessage(`{"work_id":"work-1","expected_version":` + strconv.FormatInt(version, 10) + `,"action_id":"start_execution","idempotency_key":"escalated-start-` + strconv.FormatInt(cycle, 10) + `"}`)}, mutationEnvelope(grant, scopeVersion))
		if start.Outcome != OutcomeOK {
			t.Fatalf("seed escalated start %d: %+v", cycle, start.Error)
		}
		applyEscalatedWorkerDispatchAndFailure(t, s, grant, attemptID, "escalated")
		version = workVersion(t, s, "work-1")
		record := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "record_worker_failure", "fields": map[string]any{"attempt_id": attemptID, "attempt_epoch": cycle}, "idempotency_key": "escalated-record-" + strconv.FormatInt(cycle, 10)})}, mutationEnvelope(grant, scopeVersion))
		if record.Outcome != OutcomeOK {
			t.Fatalf("seed escalated failure record %d: %+v", cycle, record.Error)
		}
		version = workVersion(t, s, "work-1")
	}
	pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	if pin.Correction == nil || !pin.Correction.Escalated || pin.Correction.AttemptCount != 3 || pin.Correction.FailedAttemptID != attemptID || pin.Correction.FailedAttemptEpoch != 3 {
		t.Fatalf("escalated correction = %#v, want three failed attempts", pin.Correction)
	}
	if binding, err := store.WorkflowFailedWorkerRetryBinding(context.Background(), s, "work-1"); err != nil || binding == nil || binding.FailedAttemptID != attemptID || binding.FailedAttemptEpoch != 3 || binding.ContractVersion != 1 {
		t.Fatalf("escalated retry binding=%+v err=%v", binding, err)
	}
	return version, attemptID, path
}

func applyEscalatedWorkerDispatchAndFailure(t *testing.T, s *store.Store, grant Authority, attemptID, suffix string) {
	t.Helper()
	lane := retryLane(t)
	dispatch := store.Event{EventID: suffix + "-dispatch-" + attemptID, Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: retryJSON(store.WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, PacketDigest: "sha256:" + strings.Repeat("c", 64), ReadbackModel: "openai/gpt-5.6-luna", PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion})}
	failure := store.Event{EventID: suffix + "-failed-" + attemptID, Kind: store.WorkerFailed, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: retryJSON(store.WorkerFailedPayload{AttemptID: attemptID, ReadbackModel: "openai/gpt-5.6-luna", FailureKind: store.WorkerFailureWorkerError, Detail: "synthetic escalated failure"})}
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
		t.Fatalf("seed escalated dispatch and failure for %s: %v", attemptID, err)
	}
}

func recordFailureForEscalatedRetry(t *testing.T, s *store.Store, service *Service, grant Authority, attemptID string, epoch int64) {
	t.Helper()
	// The worker session records its lane dispatch and its failure before the
	// agent reports the failed attempt to the workflow.
	applyEscalatedWorkerDispatchAndFailure(t, s, grant, attemptID, "escalated-retry")
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	version := workVersion(t, s, "work-1")
	record := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "record_worker_failure", "fields": map[string]any{"attempt_id": attemptID, "attempt_epoch": epoch}, "idempotency_key": "escalated-retry-record"})}, mutationEnvelope(grant, scopeVersion))
	if record.Outcome != OutcomeOK {
		t.Fatalf("record escalated retry failure: %+v", record.Error)
	}
}

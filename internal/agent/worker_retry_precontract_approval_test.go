package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// A work item with no pinned contract still has to be able to retry a failed
// lane attempt: the approval challenge binds the failed attempt identity, the
// attempt epoch, and the work version, and carries no contract key. The
// contract version zero in the binding is the absence of a contract, not a
// version, so it never enters the challenge versions.
func TestPreContractWorkerRetryMintsChallengeAndOpensFreshAttempt(t *testing.T) {
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition", "worker_dispatch"})
	version, attemptID, worktree := seedFailedPreContractResearchRetry(t, s, service, grant)
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
	freshAttemptID := "attempt:work-1:fresh"
	packet := preContractRetryPacket(t, "work-1", "discovery", freshAttemptID, pin.Correction)
	input := map[string]any{
		"work_id": "work-1", "expected_version": version, "action_id": "dispatch_worker",
		"idempotency_key": "precontract-retry-1", "fields": map[string]any{"attempt_id": freshAttemptID, "worker_packet": packet},
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	challenge := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("pre-contract retry without approval = %+v, want approval_required", challenge.Error)
	}
	challengeRef, ok := challenge.Error.Details["approval_ref"].(string)
	if !ok || challengeRef == "" {
		t.Fatalf("pre-contract retry challenge has no approval reference: %+v", challenge.Error.Details)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1'`); got != 1 {
		t.Fatalf("challenge created %d worker attempts, want 1", got)
	}

	approvedInput := cloneWithApproval(t, input, challengeRef)
	approvedRaw, _ := json.Marshal(approvedInput)
	scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "failed_attempt_id": attemptID, "scope_version": scopeVersion}
	versions := map[string]any{"work": version, "failed_attempt_epoch": 1}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, mutationDigest("concord_work_transition", "workflow_action", env, approvedRaw), scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), nonceForChallenge(challengeRef))
	approved := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approvedRaw}, env)
	if approved.Outcome != OutcomeOK {
		t.Fatalf("approved pre-contract retry = %+v, want ok", approved.Error)
	}
	var retryEpoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE kind=? AND subject_id='work-1' AND json_extract(payload,'$.action_id')='dispatch_worker' ORDER BY seq DESC LIMIT 1`, store.WorkflowActionStarted).Scan(&retryEpoch); err != nil {
		t.Fatal(err)
	}
	if retryEpoch != 2 {
		t.Fatalf("approved pre-contract retry epoch=%d, want 2", retryEpoch)
	}
}

// A challenge spec whose versions carry a non-positive value is refused for
// version validity, and the refusal names versions rather than scope
// authority.
func TestApprovalChallengeVersionValidityRefusalNamesVersions(t *testing.T) {
	db := openAgentDB(t)
	service := NewService(db)
	service.Now = func() time.Time { return fixedTime() }
	seedSimpleAuthorityScope(t, db)
	if err := service.RegisterTrustedClient(context.Background(), testClientRegistration("client-1", "human-1", []Capability{"product_read"}, []string{"product-1"}, []string{"project-1"})); err != nil {
		t.Fatal(err)
	}
	service.ProjectResolver = func(context.Context, *store.Transaction, string, string) (store.ProjectResolution, error) {
		return store.ProjectResolution{ProjectID: "project-1"}, nil
	}
	invocation := Invocation{ClientRef: "client-1", PrincipalRef: "human-1", SessionRef: "session-1", AgentRef: "agent-1", Directory: "/repo", Worktree: "/repo-wt", ManifestDigest: ManifestDigest, RequiredCapability: "product_read", HostAssertionDigest: "sha256:host-resolution", ProductID: "product-1", ProjectID: "project-1"}
	err := db.Transact(context.Background(), func(tx *store.Transaction) error {
		_, err := service.CreateApprovalChallengeTx(context.Background(), tx, invocation, ApprovalChallengeSpec{OperationDigest: "sha256:operation", Scope: map[string]any{"product_id": "product-1"}, Versions: map[string]any{"work": 0}, Consequence: "internal_sqlite", HostAssertionDigest: invocation.HostAssertionDigest, ExpiresAt: fixedTime().Add(time.Hour)})
		return err
	})
	if err == nil {
		t.Fatal("challenge with work version 0 was minted, want a refusal")
	}
	if !strings.Contains(err.Error(), "approval challenge versions invalid") {
		t.Fatalf("refusal = %v, want it to name version validity", err)
	}
	if strings.Contains(err.Error(), "scope") {
		t.Fatalf("refusal = %v, want no scope-authority attribution", err)
	}
}

// seedFailedPreContractResearchRetry mirrors seedFailedWorkerRetryMutation
// without the workflow contract row and with a failed research dispatch at
// the discovery step: the exact live shape of the reported defect.
func seedFailedPreContractResearchRetry(t *testing.T, s *store.Store, service *Service, grant Authority) (int64, string, string) {
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
		UPDATE workflow_instances SET current_step='discovery' WHERE work_id='work-1';
		DELETE FROM fold_guard`, ownerRef); err != nil {
		t.Fatalf("seed pre-contract projections: %v", err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at) VALUES(?,?,?,?,?,?,?,'active',?);
		DELETE FROM fold_guard`, store.WorktreeSetID("work-1"), "project-1", "claim:precontract-retry", "precontract-retry", strings.Repeat("a", 40), path, "repo:precontract-retry", fixedTime().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seed pre-contract worktree: %v", err)
	}
	attemptID := "attempt:work-1:failed"
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	grant.Worktree = path
	initialEnv := mutationEnvelope(grant, scopeVersion)
	initialInput := retryJSON(map[string]any{"work_id": "work-1", "expected_version": 4, "action_id": "dispatch_worker", "idempotency_key": "precontract-initial", "fields": map[string]any{"attempt_id": attemptID, "worker_packet": preContractRetryPacket(t, "work-1", "discovery", attemptID, nil)}})
	initial := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: initialInput}, initialEnv)
	if initial.Outcome != OutcomeOK {
		t.Fatalf("seed initial pre-contract research dispatch: %+v", initial.Error)
	}
	lane := preContractResearchLane(t)
	dispatch := store.Event{EventID: "precontract-dispatch", Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: retryJSON(store.WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, PacketDigest: "sha256:" + strings.Repeat("b", 64), ReadbackModel: "openai/gpt-5.6-luna", PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion})}
	failure := store.Event{EventID: "precontract-failed", Kind: store.WorkerFailed, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: retryJSON(store.WorkerFailedPayload{AttemptID: attemptID, ReadbackModel: "openai/gpt-5.6-luna", FailureKind: store.WorkerFailureWorkerError, Detail: "usage limit reached"})}
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
		t.Fatalf("seed pre-contract failure: %v", err)
	}
	var failureVersion int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-1'`).Scan(&failureVersion); err != nil {
		t.Fatal(err)
	}
	scopeVersion, _, err = s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	record := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{"work_id": "work-1", "expected_version": failureVersion, "action_id": "record_worker_failure", "fields": map[string]any{"attempt_id": attemptID, "attempt_epoch": 1}, "idempotency_key": "precontract-record-failure"})}, mutationEnvelope(grant, scopeVersion))
	if record.Outcome != OutcomeOK {
		t.Fatalf("seed pre-contract failure record: %+v", record.Error)
	}
	binding, err := store.WorkflowFailedWorkerRetryBinding(context.Background(), s, "work-1")
	if err != nil || binding == nil {
		t.Fatalf("pre-contract retry binding=%+v err=%v", binding, err)
	}
	if binding.ContractVersion != 0 {
		t.Fatalf("pre-contract retry binding contract version=%d, want 0", binding.ContractVersion)
	}
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-1'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version, attemptID, path
}

func preContractRetryPacket(t *testing.T, workID, stepID, attemptID string, correction *store.WorkflowCorrectionContext) map[string]any {
	t.Helper()
	lane := preContractResearchLane(t)
	inputs := map[string]any{"task": "retry the recorded discovery question", "constraints": []string{"cite primary sources"}}
	if correction != nil {
		inputs["correction"] = correction
	}
	return map[string]any{"schema_version": "1.0", "attempt_id": attemptID, "lane_id": lane.ID, "lane_version": lane.Version, "lane_digest": lane.Digest, "work_id": workID, "step_id": stepID, "inputs": inputs}
}

func preContractResearchLane(t *testing.T) store.LaneDefinition {
	t.Helper()
	for _, lane := range store.BuiltinLaneDefinitions() {
		if lane.ID == "research" {
			return lane
		}
	}
	t.Fatal("research lane is not registered")
	return store.LaneDefinition{}
}

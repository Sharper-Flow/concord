package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestPreContractWorkerRetryOpensFreshAttemptWithoutConsent(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition", "worker_dispatch"})
	version, _, worktree := seedFailedPreContractResearchRetry(t, s, service, grant)
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
	packet := preContractRetryPacket(t, s, "work-1", "investigate", freshAttemptID, pin.Correction)
	input := map[string]any{
		"work_id": "work-1", "expected_version": version, "action_id": "dispatch_worker",
		"idempotency_key": "precontract-retry-1", "fields": map[string]any{"attempt_id": freshAttemptID, "worker_packet": packet},
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	before := verifyWallChallenges(t, s)
	result := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if result.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("pre-contract retry without consent = %+v, want ok", result.Error)
	}
	if got := verifyWallChallenges(t, s); got != before {
		t.Fatalf("retry minted a consent challenge: %d -> %d", before, got)
	}
	var retryEpoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(latest_dispatch_start_payload,'$.attempt_epoch') FROM workflow_step_epochs WHERE work_id='work-1' ORDER BY latest_dispatch_start_seq DESC LIMIT 1`).Scan(&retryEpoch); err != nil {
		t.Fatal(err)
	}
	if retryEpoch != 2 {
		t.Fatalf("approved pre-contract retry epoch=%d, want 2", retryEpoch)
	}
	launches := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1'`)
	replay := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if replay.Outcome != agentcontract.OutcomeOK || !replay.Replayed {
		t.Fatalf("retry replay did not return its original response: %+v", replay)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1'`); got != launches {
		t.Fatalf("replay created another authorization: %d -> %d", launches, got)
	}
	input["expected_version"] = workVersion(t, s, "work-1")
	input["idempotency_key"] = "precontract-unresolved-next"
	input["fields"] = map[string]any{"attempt_id": "attempt:work-1:unknown-next", "worker_packet": preContractRetryPacket(t, s, "work-1", "investigate", "attempt:work-1:unknown-next", pin.Correction)}
	pendingRaw, _ := json.Marshal(input)
	pending := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: pendingRaw}, env)
	if pending.Error == nil || pending.Error.Kind != "operation_conflict" || !strings.Contains(pending.Error.Message, "unresolved launch outcome") {
		t.Fatalf("unknown launch effect did not require reconciliation: %+v", pending)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1'`); got != launches {
		t.Fatalf("unresolved launch refusal created an authorization: %d -> %d", launches, got)
	}
}

// A challenge spec whose versions carry a non-positive value is refused for
// version validity, and the refusal names versions rather than scope
// authority.
func TestApprovalChallengeVersionValidityRefusalNamesVersions(t *testing.T) {
	db := openAgentDB(t)
	service := NewService(db)
	service.Now = fixedTime
	seedSimpleAuthorityScope(t, db)
	if err := service.RegisterTrustedClient(context.Background(), testClientRegistration("client-1", "human-1", []agentcontract.Capability{"product_read"}, []string{"product-1"}, []string{"project-1"})); err != nil {
		t.Fatal(err)
	}
	resolveProjectAuthority(service, store.ProjectResolution{ProjectID: "project-1"})
	invocation := Invocation{ClientRef: "client-1", PrincipalRef: "human-1", SessionRef: "session-1", AgentRef: "agent-1", Directory: "/repo", Worktree: "/repo-wt", ManifestDigest: agentcontract.ManifestDigest, RequiredCapability: "product_read", HostAssertionDigest: "sha256:host-resolution", ProductID: "product-1", ProjectID: "project-1"}
	err := db.Transact(context.Background(), func(tx *store.Transaction) error {
		_, err := service.CreateApprovalChallengeTx(context.Background(), tx, probedHost(), invocation, ApprovalChallengeSpec{OperationDigest: "sha256:operation", Scope: map[string]any{"product_id": "product-1"}, Versions: map[string]any{"work": 0}, Consequence: "internal_sqlite", HostAssertionDigest: invocation.HostAssertionDigest, ExpiresAt: fixedTime().Add(time.Hour)})
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
// the investigate step: the exact live shape of the reported defect. The
// canonical research head derives the workflow failure record from the
// attempt's own terminal event (CD-0198 D5, CD-0218 D2).
func seedFailedPreContractResearchRetry(t *testing.T, s *store.Store, service *Service, grant Authority) (int64, string, string) {
	t.Helper()
	seedCurrentWorkflowDomainFixture(t, s)
	registered, lookupErr := store.BuiltinWorkflowDefinitionForRef("workflow.research")
	if lookupErr != nil {
		t.Fatal(lookupErr)
	}
	owner := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	ctx := agentWorkflowSourceProof(t, context.Background(), s, "work-1")
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: "work-1", Definition: registered, Actor: owner, Now: fixedTime()})
	}); err != nil {
		t.Fatal(err)
	}
	if got := workVersion(t, s, "work-1"); got != 4 {
		t.Fatalf("workflow seed version=%d, want 4", got)
	}
	path := t.TempDir()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
		UPDATE workflow_instances SET current_step='investigate' WHERE work_id='work-1';
		DELETE FROM write_guard`); err != nil {
		t.Fatalf("seed pre-contract projections: %v", err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
		INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at) VALUES(?,?,?,?,?,?,?,'active',?);
		DELETE FROM write_guard`, store.WorktreeSetID("work-1"), "project-1", "claim:precontract-retry", "precontract-retry", strings.Repeat("a", 40), path, "repo:precontract-retry", fixedTime().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seed pre-contract worktree: %v", err)
	}
	attemptID := "attempt:work-1:failed"
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	grant.Worktree = path
	initialEnv := mutationEnvelope(grant, scopeVersion)
	initialInput := retryJSON(map[string]any{"work_id": "work-1", "expected_version": 4, "action_id": "dispatch_worker", "idempotency_key": "precontract-initial", "fields": map[string]any{"attempt_id": attemptID, "worker_packet": preContractRetryPacket(t, s, "work-1", "investigate", attemptID, nil)}})
	initial := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: initialInput}, initialEnv)
	if initial.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("seed initial pre-contract research dispatch: %+v", initial.Error)
	}
	// The worker session's own lane dispatch precedes its terminal fact;
	// the live worker-evidence seam is the route the worker-fail CLI owns:
	// the terminal fact and the derived workflow record commit together.
	lane := preContractResearchLane(t)
	laneDispatch := store.Event{EventID: "precontract-dispatch", Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: retryJSON(store.WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, PacketDigest: "sha256:" + strings.Repeat("b", 64), ReadbackModel: "openai/gpt-5.6-luna", PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion})}
	// The failed terminal carries the recorded host observation the promoted
	// definition requires; the store derives the identity from the attempt's
	// own dispatch authorization.
	failure := agentWorkerFailedAttributedEvent("precontract-failed", store.WorkerFailedPayload{AttemptID: attemptID, ReadbackModel: "openai/gpt-5.6-luna", FailureKind: store.WorkerFailureWorkerError, Detail: "usage limit reached"})
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
		t.Fatalf("seed pre-contract failure: %v", err)
	}
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-1'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version, attemptID, path
}

func preContractRetryPacket(t *testing.T, s *store.Store, workID, stepID, attemptID string, correction *store.WorkflowCorrectionContext) map[string]any {
	t.Helper()
	lane := preContractResearchLane(t)
	inputs := map[string]any{"task": "retry the recorded discovery question", "constraints": []string{"cite primary sources"}}
	if correction != nil {
		inputs["correction"] = correction
	}
	return bindPacketToRecordedState(t, s, map[string]any{"schema_version": store.WorkerPacketSchemaVersion, "attempt_id": attemptID, "lane_id": lane.ID, "lane_digest": lane.Digest, "work_id": workID, "step_id": stepID, "inputs": inputs})
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

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestRepeatedFailedCorrectionKeepsLaunchFenceAndRecovery(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition", "worker_dispatch"})
	version, failedAttemptID, worktree := seedEscalatedFailedWorkerMutation(t, s, service, grant)
	grant.Worktree = worktree
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	owner := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	seedChallenges := verifyWallChallenges(t, s)
	retryID := "attempt:work-1:escalated-4"
	input := map[string]any{
		"work_id": "work-1", "expected_version": version, "action_id": "dispatch_worker",
		"idempotency_key": "escalated-convergence-3", "fields": map[string]any{"attempt_id": retryID, "worker_packet": deliveryRecoveryPacket(t, s, "execution", retryID, "implementation")},
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	env.RequestID = "request:escalated-convergence-3"
	admitted := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if admitted.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("ordinary retry without any approval = %+v", admitted.Error)
	}
	if got := verifyWallChallenges(t, s); got != seedChallenges {
		t.Fatalf("ordinary retry minted %d approval challenges, want 0", got)
	}
	if got := verifyWallDispatchCompletions(t, s, retryID); got != 1 {
		t.Fatalf("ordinary retry recorded %d dispatch completions, want 1", got)
	}
	// Ordinary launch authority fences the next epoch after the failed attempt.
	if epoch := escalatedDispatchEpoch(t, s); epoch != attemptEpoch(t, s, failedAttemptID)+1 {
		t.Fatalf("ordinary retry epoch=%d, want the fresh fenced epoch after the seed's %d", epoch, attemptEpoch(t, s, failedAttemptID))
	}
	assertDispatchHasNoRetryBudget(t, s, retryID)

	// The identical request replays its one authorization without a second launch.
	env.HostApproval = nil
	replayed := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if replayed.Outcome != agentcontract.OutcomeOK || !replayed.Replayed {
		t.Fatalf("replay of the admitted retry = %+v replayed=%v, want the recorded result", replayed.Error, replayed.Replayed)
	}
	if got := verifyWallDispatchCompletions(t, s, retryID); got != 1 {
		t.Fatalf("replayed retry recorded %d dispatch completions, want 1", got)
	}
	// The three admitted prerequisite Reviews are not implementation retries.
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1' AND capability_class='implementation'`); got != 4 {
		t.Fatalf("replayed retry left %d worker attempts, want 4", got)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1' AND capability_class='review'`); got != 3 {
		t.Fatalf("prerequisite Review attempts=%d, want 3", got)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1'`); got != 7 {
		t.Fatalf("replayed retry left %d total worker attempts, want four implementation attempts and three Reviews", got)
	}

	// An unresolved launch must be reconciled before another authorization.
	secondID := "attempt:work-1:escalated-5"
	nextInput := map[string]any{
		"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "action_id": "dispatch_worker",
		"idempotency_key": "escalated-convergence-4", "fields": map[string]any{"attempt_id": secondID, "worker_packet": deliveryRecoveryPacket(t, s, "execution", secondID, "implementation")},
	}
	blindRaw, err := json.Marshal(nextInput)
	if err != nil {
		t.Fatal(err)
	}
	env.RequestID = "request:escalated-convergence-4"
	blind := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: blindRaw}, env)
	if blind.Error == nil || blind.Error.Kind != "operation_conflict" || !strings.Contains(blind.Error.Message, "unresolved launch outcome") {
		t.Fatalf("blind re-dispatch = %+v, want unresolved launch conflict", blind.Error)
	}
	if got := verifyWallChallenges(t, s); got != seedChallenges {
		t.Fatalf("blind re-dispatch minted %d approval challenges, want 0", got)
	}
	if got := verifyWallDispatchCompletions(t, s, secondID); got != 0 {
		t.Fatalf("blind re-dispatch recorded %d dispatch completions, want 0", got)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1' AND attempt_id='`+secondID+`'`); got != 0 {
		t.Fatalf("blind re-dispatch created %d worker attempt rows, want 0", got)
	}

	// The terminal failure replaces the correction source with its own identity.
	recordWorkflowFailureForPin(t, s, grant, retryID, 0, "escalated-retry")
	rearmed := verifyWallPinCorrection(t, s)
	if rearmed.Disposition != "failed" || rearmed.FailedAttemptID != retryID {
		t.Fatalf("re-armed correction = %+v, want failed attempt %s", rearmed, retryID)
	}
	retryAttemptEpoch := attemptEpoch(t, s, retryID)
	if rearmed.FailedAttemptEpoch != retryAttemptEpoch {
		t.Fatalf("re-armed correction epoch %d, want the admitted attempt's own fenced epoch %d", rearmed.FailedAttemptEpoch, retryAttemptEpoch)
	}

	// Contract recovery still retires the old job revision.
	supersedeEscalatedCorrectionContract(t, s, owner, workVersion(t, s, "work-1"), 2, "the failed retry needs a corrected objective")
	recordReadyRetryJobUnderContract(t, s, service, mutationEnvelope(grant, mustScopeVersion(t, s)), "job:retry-objective-2", 2)
	oAgentReview(t, oAgentFixture(t, s, service, env, "work-1"), "continue", false)
	if epoch := escalatedDispatchEpoch(t, s); epoch != retryAttemptEpoch+1 {
		t.Fatalf("contract-recovery Review epoch=%d, want the fenced epoch after %d", epoch, retryAttemptEpoch)
	}
	renewedWantEpoch := escalatedDispatchEpoch(t, s) + 1
	renewedInput := map[string]any{
		"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "action_id": "dispatch_worker",
		"idempotency_key": "escalated-convergence-5", "fields": map[string]any{"attempt_id": secondID, "worker_packet": deliveryRecoveryPacket(t, s, "execution", secondID, "implementation")},
	}
	renewedRaw, err := json.Marshal(renewedInput)
	if err != nil {
		t.Fatal(err)
	}
	env.RequestID = "request:escalated-convergence-5"
	renewed := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: renewedRaw}, env)
	if renewed.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("retry after contract recovery = %+v", renewed.Error)
	}
	if got := verifyWallChallenges(t, s); got != seedChallenges {
		t.Fatalf("the whole escalated journey minted %d approval challenges, want 0", got)
	}
	if epoch := escalatedDispatchEpoch(t, s); epoch != renewedWantEpoch {
		t.Fatalf("retry after contract recovery epoch=%d, want the fresh fenced epoch %d", epoch, renewedWantEpoch)
	}
	if renewedWantEpoch <= attemptEpoch(t, s, retryID) {
		t.Fatal("contract recovery did not advance beyond the failed retry epoch")
	}
	assertDispatchHasNoRetryBudget(t, s, secondID)
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

// escalatedDispatchEpoch reads the epoch the latest dispatch start fenced.
func escalatedDispatchEpoch(t *testing.T, s *store.Store) int64 {
	t.Helper()
	var epoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(latest_dispatch_start_payload,'$.attempt_epoch') FROM workflow_step_epochs WHERE work_id='work-1' ORDER BY latest_dispatch_start_seq DESC LIMIT 1`).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	return epoch
}

// seedEscalatedFailedWorkerMutation records three terminal failures through
// the mutation boundary, retaining the latest correction identity and epoch.
func seedEscalatedFailedWorkerMutation(t *testing.T, s *store.Store, service *Service, grant Authority, failureAt ...time.Time) (int64, string, string) {
	t.Helper()
	// The fixture resolves the current workflow family.
	definition := legacyApprovalPolicyDefinitionForRef(t)
	if got := seedAgentWorkflowAt(t, s, grant, definition); got != 4 {
		t.Fatalf("workflow seed version=%d, want 4", got)
	}
	owner := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	approvalStep := ""
	for _, step := range definition.Definition.StepGraph.Steps {
		if slices.Contains(step.Actions, "approve_contract") {
			approvalStep = step.ID
			break
		}
	}
	if approvalStep == "" {
		t.Fatal("escalated fixture definition has no contract approval step")
	}
	path := t.TempDir()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
		UPDATE workflow_instances SET current_step=? WHERE work_id='work-1';
		DELETE FROM write_guard`, approvalStep); err != nil {
		t.Fatalf("seed escalated projections: %v", err)
	}
	fields := workflowContractFieldsForSeededRegistry(t, s)
	fields["premise"] = "approved retry objective"
	fields["outcome_predicates"] = []map[string]any{{"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:escalated-retry", "expected_result": "pass"}}}
	fields["architecture_binding"] = workflowArchitectureBindingForSeededRegistry(t, s)
	runVerificationStoreAction(t, s, "approve_contract", fields, owner, "escalated-fixture")
	runVerificationStoreAction(t, s, "record_design", workflowDesignFieldsFixture("escalated-fixture"), owner, "escalated-fixture-design")
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
		INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at) VALUES(?,?,?,?,?,?,?,'active',?);
		DELETE FROM write_guard`, store.WorktreeSetID("work-1"), "project-1", "claim:escalated", "escalated", strings.Repeat("a", 40), path, "repo:escalated", fixedTime().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seed escalated worktree: %v", err)
	}
	var claimed int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worktree_entries WHERE set_id=? AND state='active'`, store.WorktreeSetID("work-1")).Scan(&claimed); err != nil || claimed != 1 {
		t.Fatalf("escalated worktree claim count=%d err=%v", claimed, err)
	}
	grant.Worktree = path
	version := workVersion(t, s, "work-1")
	attemptID := ""
	for cycle := int64(1); cycle <= 3; cycle++ {
		attemptID = "attempt:work-1:escalated-" + strconv.FormatInt(cycle, 10)
		scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
		if err != nil {
			t.Fatal(err)
		}
		start := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: json.RawMessage(`{"work_id":"work-1","expected_version":` + strconv.FormatInt(version, 10) + `,"action_id":"start_execution","idempotency_key":"escalated-start-` + strconv.FormatInt(cycle, 10) + `"}`)}, mutationEnvelope(grant, scopeVersion))
		if start.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("seed escalated start %d: %+v", cycle, start.Error)
		}
		// Each cycle executes its own recorded job: an accepted job is
		// satisfied and never redispatches, and a failed attempt leaves its
		// revision ready, so the cycle's dispatch binds a fresh one.
		recordReadyRetryJob(t, s, service, mutationEnvelope(grant, scopeVersion), "job:escalated-"+strconv.FormatInt(cycle, 10))
		env := mutationEnvelope(grant, scopeVersion)
		// After a failed correction the dispatch packet consumes the live
		// correction context the pin carries; cycle 1 carries none. The
		// packet selects the freshly recorded ready job explicitly, as the
		// adapter does, so earlier failed revisions never shadow it.
		pin, pinErr := store.ReadWorkPin(context.Background(), s, "work-1")
		if pinErr != nil {
			t.Fatal(pinErr)
		}
		lane := retryLane(t)
		inputs := map[string]any{"task": "carry out the escalated objective", "constraints": []string{"preserve the approved contract"}}
		if pin.Correction != nil {
			inputs["correction"] = pin.Correction
		}
		snapshot, snapshotErr := store.ReadWorkflowContinuity(context.Background(), s, store.ContinuityRequest{Work: "work-1"})
		if snapshotErr != nil {
			t.Fatal(snapshotErr)
		}
		if len(snapshot.ReadyWorkerJobs) == 0 {
			t.Fatalf("seed escalated dispatch %d: no ready worker job to select", cycle)
		}
		selected, selectErr := json.Marshal(snapshot.ReadyWorkerJobs[len(snapshot.ReadyWorkerJobs)-1])
		if selectErr != nil {
			t.Fatal(selectErr)
		}
		inputs["worker_job"] = json.RawMessage(selected)
		// Review binds the selected job; continue leaves it ready for this
		// cycle's dispatch (CD-0205 D3).
		oAgentReview(t, oAgentFixture(t, s, service, env, "work-1"), "continue", false)
		version = workVersion(t, s, "work-1")
		packet := bindPacketToRecordedState(t, s, map[string]any{"schema_version": store.WorkerPacketSchemaVersion, "attempt_id": attemptID, "lane_id": lane.ID, "lane_digest": lane.Digest, "work_id": "work-1", "step_id": "execution", "inputs": inputs})
		input := map[string]any{
			"work_id": "work-1", "expected_version": version, "action_id": "dispatch_worker", "idempotency_key": "escalated-dispatch-" + strconv.FormatInt(cycle, 10),
			"fields": map[string]any{"attempt_id": attemptID, "worker_packet": packet},
		}
		raw, marshalErr := json.Marshal(input)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		dispatched := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
		if dispatched.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("seed escalated dispatch %d: %+v", cycle, dispatched.Error)
		}
		var dispatchedJob string
		if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.worker_job.job_id') FROM worker_authorizations WHERE work_id='work-1' AND attempt_id=?`, attemptID).Scan(&dispatchedJob); err != nil {
			t.Fatal(err)
		}
		if want := "job:escalated-" + strconv.FormatInt(cycle, 10); dispatchedJob != want {
			t.Fatalf("seed cycle %d dispatched job %q, want its own job %q", cycle, dispatchedJob, want)
		}
		version = recordWorkflowFailureForPin(t, s, grant, attemptID, 0, "escalated-seed-"+strconv.FormatInt(cycle, 10), failureAt...)
	}
	pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	// The correction binds the failed attempt's own fenced epoch.
	failedAttemptEpoch := attemptEpoch(t, s, attemptID)
	if pin.Correction == nil || pin.Correction.Disposition != "failed" || pin.Correction.FailedAttemptID != attemptID {
		t.Fatalf("correction = %#v, want failed attempt %s", pin.Correction, attemptID)
	}
	if pin.Correction.FailedAttemptEpoch != failedAttemptEpoch {
		t.Fatalf("escalated correction epoch %d, want the third attempt's own fenced epoch %d", pin.Correction.FailedAttemptEpoch, failedAttemptEpoch)
	}
	return version, attemptID, path
}

// supersedeEscalatedCorrectionContract records an engineering successor with
// unchanged criteria and architecture. It mints no operator challenge.
func supersedeEscalatedCorrectionContract(t *testing.T, s *store.Store, owner store.WorkflowActor, version, next int64, reason string) {
	t.Helper()
	fields := map[string]any{
		"decision_domains": []string{},
		"contract_version": next,
		"premise":          fmt.Sprintf("corrected retry objective %d: %s", next, reason),
		"outcome_predicates": []map[string]any{{
			"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check",
			"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:escalated-retry", "expected_result": "pass"},
		}},
		"required_evidence": []string{"artifact"}, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{},
		"architecture_binding": workflowArchitectureBindingForSeededRegistry(t, s),
		"rigor_class":          "prototype_internal", "supersede_reason": reason, "audit_evidence": []string{"evidence:escalated-supersede"},
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	operationID := "escalated-supersede-" + strconv.FormatInt(next, 10)
	ctx, err := s.EstablishKnowledgeSourceSetProof(context.Background(), "work-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		_, err := store.ApplyWorkflowActionTx(ctx, tx, store.BuiltinWorkflowRegistry(), store.WorkflowActionExecutionRequest{
			WorkID: "work-1", ExpectedVersion: version, ActionID: "supersede_contract", Payload: raw, Actor: owner,
			AcceptedInputsDigest: "sha256:" + strings.Repeat("f", 64) + operationID, IdempotencyIdentity: operationID, OperationID: operationID,
			PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID, RequestID: "request:" + operationID,
			ContractDigest: agentcontract.ManifestDigest, Now: fixedTime(),
		})
		return err
	}); err != nil {
		t.Fatalf("supersede contract v%d: %v", next, err)
	}
	var active int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id='work-1' AND superseded_by IS NULL`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != next {
		t.Fatalf("active contract after supersession = %d, want %d", active, next)
	}
}

func applyEscalatedWorkerDispatchAndFailure(t *testing.T, s *store.Store, grant Authority, attemptID, suffix string, failureAt ...time.Time) {
	t.Helper()
	lane := retryLane(t)
	job := authorizedWorkerJob(t, s, attemptID)
	dispatch := store.Event{EventID: suffix + "-dispatch-" + attemptID, Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: retryJSON(store.WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, PacketDigest: "sha256:" + strings.Repeat("c", 64), ReadbackModel: "openai/gpt-5.6-luna", PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion, WorkerJob: job})}
	// The failed terminal carries the recorded host observation the promoted
	// definition requires; on a released pin the block lands unchanged beside
	// the store-level record disposition.
	failure := agentWorkerFailedAttributedEvent(suffix+"-failed-"+attemptID, store.WorkerFailedPayload{AttemptID: attemptID, ReadbackModel: "openai/gpt-5.6-luna", FailureKind: store.WorkerFailureWorkerError, Detail: "synthetic escalated failure"})
	if len(failureAt) != 0 {
		failure.OccurredAt = failureAt[0]
	}
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		enriched, err := store.PrepareLaneActorDispatch(context.Background(), tx, dispatch, grant.PrincipalRef, grant.ClientRef)
		if err != nil {
			return err
		}
		if _, err := store.AppendLaneActorDispatchTx(context.Background(), tx, enriched); err != nil {
			return err
		}
		// The live worker-evidence seam: on a canonical head the terminal
		// fact derives the workflow failure record in the same commit
		// (CD-0198 D5); on a released pin the event lands unchanged and the
		// store-level record disposition follows.
		_, err = store.ApplyWorkerEvidenceOperationTx(context.Background(), tx, failure)
		return err
	}); err != nil {
		t.Fatalf("seed escalated dispatch and failure for %s: %v", attemptID, err)
	}
}

// recordWorkflowFailureForPin records the workflow failure record the
// failed attempt owes and returns the work version after it. The canonical
// implementation heads derive the record from the attempt's own terminal
// event through the live worker-evidence seam (CD-0198 D5). A released pin
// keeps its callable record_worker_failure behavior store-executable; the
// closed live union publishes no authoring shape for the retired call
// (CD-0218 D2/D4).
func recordWorkflowFailureForPin(t *testing.T, s *store.Store, grant Authority, attemptID string, epoch int64, suffix string, failureAt ...time.Time) int64 {
	t.Helper()
	var pinnedRef string
	var pinnedDigest string
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_digest FROM workflow_instances WHERE work_id='work-1'`).Scan(&pinnedRef, &pinnedDigest); err != nil {
		t.Fatal(err)
	}
	if err := store.BuiltinWorkflowRegistry().Verify(pinnedRef, pinnedDigest); err != nil {
		t.Fatal(err)
	}
	pinned, ok := store.BuiltinWorkflowRegistry().Current(pinnedRef)
	if !ok {
		t.Fatalf("workflow definition %s is not registered", pinnedRef)
	}
	if slices.Contains(pinned.Definition.Capabilities, store.WorkflowCapabilityDerivedWorkerFailure) {
		// The verified definition derives the failure record in the same
		// commit as the worker's terminal fact.
		applyEscalatedWorkerDispatchAndFailure(t, s, grant, attemptID, "escalated-derived-"+suffix, failureAt...)
		return workVersion(t, s, "work-1")
	}
	// The worker session records its lane dispatch and its failure before
	// the released pin's store-level record disposition lands.
	applyEscalatedWorkerDispatchAndFailure(t, s, grant, attemptID, suffix, failureAt...)
	if epoch == 0 {
		epoch = escalatedDispatchEpoch(t, s)
	}
	actor := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	version := workVersion(t, s, "work-1")
	operationID := "escalated-record-" + suffix + "-" + attemptID
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		_, err := store.ApplyWorkflowActionTx(context.Background(), tx, store.BuiltinWorkflowRegistry(), store.WorkflowActionExecutionRequest{
			WorkID: "work-1", ExpectedVersion: version, ActionID: "record_worker_failure", Payload: retryJSON(map[string]any{"attempt_id": attemptID, "attempt_epoch": epoch}), Actor: actor,
			AcceptedInputsDigest: "sha256:" + strings.Repeat("f", 64) + operationID, IdempotencyIdentity: operationID, OperationID: operationID,
			PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID, RequestID: "request:" + operationID,
			ContractDigest: agentcontract.ManifestDigest, Now: fixedTime(),
		})
		return err
	}); err != nil {
		t.Fatalf("released-pin failure record for %s: %v", attemptID, err)
	}
	return workVersion(t, s, "work-1")
}

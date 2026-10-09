package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// TestEscalatedFailedCorrectionWallAdmitsOnlyBehindApproachConvergence pins
// the CON885 convergence gate on a failed-disposition escalated correction at
// the default workflow's execution step. Three recorded failed corrections
// arm the wall; the next dispatch refuses with missing_evidence and mints no
// approval challenge, a supplied operator approval has no effect, and the
// boundary keeps no retry approval binding (WorkflowFailedWorkerRetryBinding
// returns nil behind the escalated wall). A store-level contract supersession
// derives the approach_changed basis and admits exactly one fresh fenced
// attempt with no approval at all; the interrupted dispatch consumes the
// basis, the recorded failure of the admitted attempt re-arms the wall, and
// only a fresh supersession derives a fresh basis. The whole journey mints
// zero approval challenges: below the wall the ordinary failed-retry exact
// approval (worker_retry_approval_test.go) still governs, at the wall only a
// stored convergence basis admits.
func TestEscalatedFailedCorrectionWallAdmitsOnlyBehindApproachConvergence(t *testing.T) {
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition", "worker_dispatch"})
	version, failedAttemptID, worktree := seedEscalatedFailedWorkerMutation(t, s, service, grant)
	grant.Worktree = worktree
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	owner := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	if got := verifyWallChallenges(t, s); got != 0 {
		t.Fatalf("fixture minted %d approval challenges, want 0", got)
	}

	// The armed wall keeps no approvable identity: the retry approval binding
	// the ordinary failed-retry route mints is absent behind the escalation.
	binding, err := store.WorkflowFailedWorkerRetryBinding(context.Background(), s, nil, "work-1")
	if err != nil || binding != nil {
		t.Fatalf("retry approval binding behind the escalated wall = %+v err=%v, want none", binding, err)
	}

	// The wall refuses a fresh dispatch with the convergence refusal, and the
	// refusal is never an operator approval ask.
	retryID := "attempt:work-1:escalated-4"
	input := map[string]any{
		"work_id": "work-1", "expected_version": version, "action_id": "dispatch_worker",
		"idempotency_key": "escalated-convergence-1", "fields": map[string]any{"attempt_id": retryID, "worker_packet": retryMutationPacket(t, s, retryID, verifyWallPinCorrection(t, s))},
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	env.RequestID = "request:escalated-convergence-1"
	refused := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if refused.Error == nil || refused.Error.Kind != "missing_evidence" {
		t.Fatalf("escalated retry without a basis = %+v, want missing_evidence", refused.Error)
	}
	if !strings.Contains(refused.Error.Message, "worker correction reached the three-attempt limit without a convergence basis") {
		t.Fatalf("escalated refusal does not name the convergence limit: %q", refused.Error.Message)
	}
	if refused.Error.RecoveryAction.Kind != "provide_evidence" {
		t.Fatalf("escalated refusal recovery = %q, want provide_evidence and never an approval ask", refused.Error.RecoveryAction.Kind)
	}
	if refused.Error.ConsequenceSummary != nil {
		t.Fatal("escalated refusal carries a consequence summary, but no challenge was minted")
	}
	if ref, ok := refused.Error.Details["approval_ref"].(string); ok && ref != "" {
		t.Fatalf("escalated refusal carries approval_ref %q, but no challenge was minted", ref)
	}
	if got := verifyWallChallenges(t, s); got != 0 {
		t.Fatalf("escalated refusal minted %d approval challenges, want 0", got)
	}
	if got := verifyWallDispatchCompletions(t, s, retryID); got != 0 {
		t.Fatalf("refused escalation recorded %d dispatch completions, want 0", got)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1' AND attempt_id='`+retryID+`'`); got != 0 {
		t.Fatalf("refused escalation created %d worker attempt rows, want 0", got)
	}
	if after := workVersion(t, s, "work-1"); after != version {
		t.Fatalf("refused escalation changed the work version %d -> %d", version, after)
	}

	// A supplied operator approval has no effect: no challenge exists to
	// consume, and the fold still refuses without a basis. The approval binds
	// the exact identity the pre-CON885 wall would have minted a challenge
	// for, so the refusal proves the route is gone, not misbound.
	approvedInput := cloneWithApproval(t, input, strings.Repeat("b", 64))
	approvedRaw, err := json.Marshal(approvedInput)
	if err != nil {
		t.Fatal(err)
	}
	scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "failed_attempt_id": failedAttemptID, "scope_version": scopeVersion}
	versions := map[string]any{"work": version, "contract": int64(1), "failed_attempt_epoch": int64(3)}
	env.HostApproval = signedHostApproval(privateKey, strings.Repeat("b", 64), mutationDigest("concord_work_transition", "workflow_action", env, approvedRaw), scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), "escalated-convergence-unused-approval")
	env.RequestID = "request:escalated-convergence-2"
	bypass := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approvedRaw}, env)
	if bypass.Error == nil || bypass.Error.Kind != "missing_evidence" {
		t.Fatalf("escalated retry behind a supplied approval = %+v, want missing_evidence", bypass.Error)
	}
	if got := verifyWallChallenges(t, s); got != 0 {
		t.Fatalf("supplied approval minted %d approval challenges, want 0", got)
	}
	if after := workVersion(t, s, "work-1"); after != version {
		t.Fatalf("supplied approval changed the work version %d -> %d", version, after)
	}
	env.HostApproval = nil

	// A superseded contract after the latest dispatch is a changed approach:
	// the store derives the basis, the pin flips its dispatch reason, and the
	// boundary admits the dispatch with no approval at all. The successor
	// contract retires the seed's job revision, so the fixture records a
	// fresh ready job under it before the dispatch, as the adapter does.
	supersedeEscalatedCorrectionContract(t, s, owner, workVersion(t, s, "work-1"), 2, "the escalated correction wall demanded a changed approach")
	recordReadyRetryJobUnderContract(t, s, service, mutationEnvelope(grant, mustScopeVersion(t, s)), "job:retry-objective-2", 2)
	pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	reason := ""
	for _, intent := range pin.NextValidIntents {
		if intent.ActionID == "dispatch_worker" {
			reason = intent.ReasonCode
		}
	}
	if reason != "escalated_retry_convergence_recorded" {
		t.Fatalf("pin dispatch reason behind a derived basis = %q, want escalated_retry_convergence_recorded", reason)
	}
	version = workVersion(t, s, "work-1")
	input = map[string]any{
		"work_id": "work-1", "expected_version": version, "action_id": "dispatch_worker",
		"idempotency_key": "escalated-convergence-3", "fields": map[string]any{"attempt_id": retryID, "worker_packet": retryMutationPacket(t, s, retryID, verifyWallPinCorrection(t, s))},
	}
	raw, err = json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	env.RequestID = "request:escalated-convergence-3"
	admitted := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if admitted.Outcome != OutcomeOK {
		t.Fatalf("converging escalated retry without any approval = %+v", admitted.Error)
	}
	if got := verifyWallChallenges(t, s); got != 0 {
		t.Fatalf("converging escalated retry minted %d approval challenges, want 0", got)
	}
	if got := verifyWallDispatchCompletions(t, s, retryID); got != 1 {
		t.Fatalf("converging escalated retry recorded %d dispatch completions, want 1", got)
	}
	if epoch := escalatedDispatchEpoch(t, s); epoch != 4 {
		t.Fatalf("converging escalated retry epoch=%d, want the fresh fenced epoch 4", epoch)
	}
	basis := verifyWallRecordedConvergence(t, s, retryID)
	if basis.Basis != "approach_changed" || basis.SupersededSeq == 0 || basis.PreviousRecordSeq != 0 || basis.LatestRecordSeq != 0 {
		t.Fatalf("recorded convergence = %+v, want the approach_changed basis naming its supersession sequence", basis)
	}

	// Replaying the identical admitted request returns the recorded result:
	// one basis bought one authorization, and the replay creates no second
	// attempt or completion. The envelope is byte-identical to the admitted
	// call, so the idempotency digest matches the recorded one.
	env.HostApproval = nil
	replayed := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if replayed.Outcome != OutcomeOK || !replayed.Replayed {
		t.Fatalf("replay of the admitted escalation = %+v replayed=%v, want the recorded result", replayed.Error, replayed.Replayed)
	}
	if got := verifyWallDispatchCompletions(t, s, retryID); got != 1 {
		t.Fatalf("replayed escalation recorded %d dispatch completions, want 1", got)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1'`); got != 4 {
		t.Fatalf("replayed escalation left %d worker attempts, want 4", got)
	}

	// The session dies before the host dispatches the worker: the
	// half-materialized dispatch consumed the basis, so a blind re-dispatch
	// refuses and mints no challenge.
	secondID := "attempt:work-1:escalated-5"
	nextInput := map[string]any{
		"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "action_id": "dispatch_worker",
		"idempotency_key": "escalated-convergence-4", "fields": map[string]any{"attempt_id": secondID, "worker_packet": retryMutationPacket(t, s, secondID, verifyWallPinCorrection(t, s))},
	}
	blindRaw, err := json.Marshal(nextInput)
	if err != nil {
		t.Fatal(err)
	}
	env.RequestID = "request:escalated-convergence-4"
	blind := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: blindRaw}, env)
	if blind.Error == nil || blind.Error.Kind != "missing_evidence" {
		t.Fatalf("blind re-dispatch after the consumed basis = %+v, want missing_evidence", blind.Error)
	}
	if got := verifyWallChallenges(t, s); got != 0 {
		t.Fatalf("blind re-dispatch minted %d approval challenges, want 0", got)
	}
	if got := verifyWallDispatchCompletions(t, s, secondID); got != 0 {
		t.Fatalf("blind re-dispatch recorded %d dispatch completions, want 0", got)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1' AND attempt_id='`+secondID+`'`); got != 0 {
		t.Fatalf("blind re-dispatch created %d worker attempt rows, want 0", got)
	}

	// The admitted attempt fails and the failure records: the correction
	// re-arms at four counted failures bound to the new attempt identity, and
	// the wall still keeps no approval binding.
	recordFailureForEscalatedRetry(t, s, service, grant, retryID, 4)
	rearmed := verifyWallPinCorrection(t, s)
	if rearmed.Disposition != "failed" || !rearmed.Escalated || rearmed.AttemptCount != 4 || rearmed.FailedAttemptID != retryID || rearmed.FailedAttemptEpoch != 4 {
		t.Fatalf("re-armed correction = %+v, want four counted failures bound to %s at epoch 4", rearmed, retryID)
	}
	if binding, err := store.WorkflowFailedWorkerRetryBinding(context.Background(), s, nil, "work-1"); err != nil || binding != nil {
		t.Fatalf("retry approval binding behind the re-armed wall = %+v err=%v, want none", binding, err)
	}

	// A fresh supersession derives a fresh basis, and each basis buys exactly
	// one more fresh fenced attempt.
	supersedeEscalatedCorrectionContract(t, s, owner, workVersion(t, s, "work-1"), 3, "the failed converging retry needed a renewed approach")
	recordReadyRetryJobUnderContract(t, s, service, mutationEnvelope(grant, mustScopeVersion(t, s)), "job:retry-objective-3", 3)
	renewedInput := map[string]any{
		"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "action_id": "dispatch_worker",
		"idempotency_key": "escalated-convergence-5", "fields": map[string]any{"attempt_id": secondID, "worker_packet": retryMutationPacket(t, s, secondID, verifyWallPinCorrection(t, s))},
	}
	renewedRaw, err := json.Marshal(renewedInput)
	if err != nil {
		t.Fatal(err)
	}
	env.RequestID = "request:escalated-convergence-5"
	renewed := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: renewedRaw}, env)
	if renewed.Outcome != OutcomeOK {
		t.Fatalf("renewed converging retry = %+v", renewed.Error)
	}
	if got := verifyWallChallenges(t, s); got != 0 {
		t.Fatalf("the whole escalated journey minted %d approval challenges, want 0", got)
	}
	if epoch := escalatedDispatchEpoch(t, s); epoch != 5 {
		t.Fatalf("renewed converging retry epoch=%d, want the fresh fenced epoch 5", epoch)
	}
	renewedBasis := verifyWallRecordedConvergence(t, s, secondID)
	if renewedBasis.Basis != "approach_changed" || renewedBasis.SupersededSeq <= basis.SupersededSeq {
		t.Fatalf("renewed convergence = %+v, want a fresh supersession after seq %d", renewedBasis, basis.SupersededSeq)
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

// escalatedDispatchEpoch reads the epoch the latest dispatch start fenced.
func escalatedDispatchEpoch(t *testing.T, s *store.Store) int64 {
	t.Helper()
	var epoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id='work-1' AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' ORDER BY seq DESC LIMIT 1`, store.WorkflowActionStarted).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	return epoch
}

// seedEscalatedFailedWorkerMutation drives three full failed correction
// attempts through the mutation boundary — each cycle's dispatch stays
// approval-free below the limit, as the ordinary below-wall route keeps it —
// so the fourth dispatch faces the escalated wall with a durable failed
// attempt identity. The wall itself owns no approval binding under CON885,
// so the seed pins only the correction record.
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
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	recordReadyRetryJob(t, s, service, mutationEnvelope(grant, scopeVersion), "job:retry-objective")
	version := workVersion(t, s, "work-1")
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
	return version, attemptID, path
}

// supersedeEscalatedCorrectionContract runs the operator-approved contract
// supersession at store level: the changed approach the convergence basis
// reads. The default workflow family is Product-changing, so the successor
// carries the complete architecture binding, and the store-level run mints
// nothing on the agent challenge table.
func supersedeEscalatedCorrectionContract(t *testing.T, s *store.Store, owner store.WorkflowActor, version, next int64, reason string) {
	t.Helper()
	approvalRef := strings.Repeat("a", 64)
	operator := store.WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "approval:" + approvalRef, SessionRef: "session/escalated-supersede", ActorClass: store.ActorOperator}
	fields := map[string]any{
		"contract_version": next,
		"premise":          fmt.Sprintf("corrected retry objective %d: %s", next, reason),
		"outcome_predicates": []map[string]any{{
			"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check",
			"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:escalated-retry", "expected_result": "pass"},
		}},
		"required_evidence": []string{"artifact"}, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{},
		"architecture_binding": workflowArchitectureBindingFixture(),
		"rigor_class":          "prototype_internal", "supersede_reason": reason, "audit_evidence": []string{"evidence:escalated-supersede"},
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	operationID := "escalated-supersede-" + strconv.FormatInt(next, 10)
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		_, err := store.ApplyWorkflowActionTx(context.Background(), tx, store.BuiltinWorkflowRegistry(), store.WorkflowActionExecutionRequest{
			WorkID: "work-1", ExpectedVersion: version, ActionID: "supersede_contract", Payload: raw, Actor: owner,
			OperatorActor: &operator, OperatorApprovalRef: approvalRef,
			ApprovalOperationDigest: "sha256:" + strings.Repeat("c", 64), ApprovalScopeJSON: `{}`, ApprovalVersionsJSON: fmt.Sprintf(`{"work":%d}`, version), ApprovalConsequence: "recovery",
			AcceptedInputsDigest: "sha256:" + strings.Repeat("f", 64) + operationID, IdempotencyIdentity: operationID, OperationID: operationID,
			PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID, RequestID: "request:" + operationID,
			ContractDigest: ManifestDigest, Now: fixedTime(),
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

func applyEscalatedWorkerDispatchAndFailure(t *testing.T, s *store.Store, grant Authority, attemptID, suffix string) {
	t.Helper()
	lane := retryLane(t)
	job := authorizedWorkerJob(t, s, attemptID)
	dispatch := store.Event{EventID: suffix + "-dispatch-" + attemptID, Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: retryJSON(store.WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, PacketDigest: "sha256:" + strings.Repeat("c", 64), ReadbackModel: "openai/gpt-5.6-luna", PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion, WorkerJob: job})}
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
	record := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "record_worker_failure", "fields": map[string]any{"attempt_id": attemptID, "attempt_epoch": epoch}, "idempotency_key": "escalated-retry-record-" + attemptID})}, mutationEnvelope(grant, scopeVersion))
	if record.Outcome != OutcomeOK {
		t.Fatalf("record escalated retry failure: %+v", record.Error)
	}
}

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
// approval challenge, and a supplied operator approval has no effect.
// A store-level contract supersession
// derives the approach_changed basis and admits exactly one fresh fenced
// attempt with no approval at all; the interrupted dispatch consumes the
// basis, the recorded failure of the admitted attempt re-arms the wall, and
// only a fresh supersession derives a fresh basis. The whole journey mints
// zero approval challenges; at the wall only a stored convergence basis admits.
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
	// Neither failed attempts nor a missing basis mint consent challenges.
	seedChallenges := verifyWallChallenges(t, s)

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
	if got := verifyWallChallenges(t, s); got != seedChallenges {
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
	// The supplied approval binds the identity the pre-CON885 wall would have
	// minted a challenge for: the failed attempt's own derived window epoch.
	versions := map[string]any{"work": version, "contract": int64(1), "failed_attempt_epoch": attemptEpoch(t, s, failedAttemptID)}
	env.HostApproval = signedHostApproval(privateKey, strings.Repeat("b", 64), mutationDigest("concord_work_transition", "workflow_action", env, approvedRaw), scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), "escalated-convergence-unused-approval")
	env.RequestID = "request:escalated-convergence-2"
	bypass := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approvedRaw}, env)
	if bypass.Error == nil || bypass.Error.Kind != "missing_evidence" {
		t.Fatalf("escalated retry behind a supplied approval = %+v, want missing_evidence", bypass.Error)
	}
	if got := verifyWallChallenges(t, s); got != seedChallenges {
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
	if got := verifyWallChallenges(t, s); got != seedChallenges {
		t.Fatalf("converging escalated retry minted %d approval challenges, want 0", got)
	}
	if got := verifyWallDispatchCompletions(t, s, retryID); got != 1 {
		t.Fatalf("converging escalated retry recorded %d dispatch completions, want 1", got)
	}
	// The basis buys exactly one fresh fenced epoch: the next one after the
	// seed's failed attempt. Refused wall dispatches claim no epoch.
	if epoch := escalatedDispatchEpoch(t, s); epoch != attemptEpoch(t, s, failedAttemptID)+1 {
		t.Fatalf("converging escalated retry epoch=%d, want the fresh fenced epoch after the seed's %d", epoch, attemptEpoch(t, s, failedAttemptID))
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
	if got := verifyWallChallenges(t, s); got != seedChallenges {
		t.Fatalf("blind re-dispatch minted %d approval challenges, want 0", got)
	}
	if got := verifyWallDispatchCompletions(t, s, secondID); got != 0 {
		t.Fatalf("blind re-dispatch recorded %d dispatch completions, want 0", got)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1' AND attempt_id='`+secondID+`'`); got != 0 {
		t.Fatalf("blind re-dispatch created %d worker attempt rows, want 0", got)
	}

	// The admitted attempt fails and the failure records: the correction
	// re-arms at four counted failures bound to the new attempt identity at
	// the attempt's own fenced epoch, and the wall still keeps no approval
	// binding.
	recordWorkflowFailureForPin(t, s, grant, retryID, 0, "escalated-retry")
	rearmed := verifyWallPinCorrection(t, s)
	if rearmed.Disposition != "failed" || !rearmed.Escalated || rearmed.AttemptCount != 4 || rearmed.FailedAttemptID != retryID {
		t.Fatalf("re-armed correction = %+v, want four counted failures bound to %s", rearmed, retryID)
	}
	retryAttemptEpoch := attemptEpoch(t, s, retryID)
	if rearmed.FailedAttemptEpoch != retryAttemptEpoch {
		t.Fatalf("re-armed correction epoch %d, want the admitted attempt's own fenced epoch %d", rearmed.FailedAttemptEpoch, retryAttemptEpoch)
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
	if got := verifyWallChallenges(t, s); got != seedChallenges {
		t.Fatalf("the whole escalated journey minted %d approval challenges, want 0", got)
	}
	// The renewed basis buys the next fresh fenced epoch after the failed
	// converging retry's own.
	renewedWantEpoch := attemptEpoch(t, s, retryID) + 1
	if epoch := escalatedDispatchEpoch(t, s); epoch != renewedWantEpoch {
		t.Fatalf("renewed converging retry epoch=%d, want the fresh fenced epoch %d", epoch, renewedWantEpoch)
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
func seedEscalatedFailedWorkerMutation(t *testing.T, s *store.Store, service *Service, grant Authority, failureAt ...time.Time) (int64, string, string) {
	t.Helper()
	// The wall's changed approach lands through an operator-substituted
	// supersession, a legacy approval-policy flow.
	if got := seedAgentWorkflowAt(t, s, grant, legacyApprovalPolicyDefinitionForRef(t, "workflow.implementation")); got != 4 {
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
		INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES('work-1',1,'predicate:primary',0,'check','{"kind":"check","check_ref":"check:workflow","immutable_subject_ref":"commit:escalated-retry","expected_result":"pass"}');
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
		if start.Outcome != OutcomeOK {
			t.Fatalf("seed escalated start %d: %+v", cycle, start.Error)
		}
		// Each cycle executes its own recorded job: an accepted job is
		// satisfied and never redispatches, and a failed attempt leaves its
		// revision ready, so the cycle's dispatch binds a fresh one.
		recordReadyRetryJob(t, s, service, mutationEnvelope(grant, scopeVersion), "job:escalated-"+strconv.FormatInt(cycle, 10))
		env := mutationEnvelope(grant, scopeVersion)
		version = workVersion(t, s, "work-1")
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
		packet := bindPacketToRecordedState(t, s, map[string]any{"schema_version": store.WorkerPacketSchemaVersion, "attempt_id": attemptID, "lane_id": lane.ID, "lane_version": lane.Version, "lane_digest": lane.Digest, "work_id": "work-1", "step_id": "execution", "inputs": inputs})
		input := map[string]any{
			"work_id": "work-1", "expected_version": version, "action_id": "dispatch_worker", "idempotency_key": "escalated-dispatch-" + strconv.FormatInt(cycle, 10),
			"fields": map[string]any{"attempt_id": attemptID, "worker_packet": packet},
		}
		raw, marshalErr := json.Marshal(input)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		dispatched := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
		if dispatched.Outcome != OutcomeOK {
			t.Fatalf("seed escalated dispatch %d: %+v", cycle, dispatched.Error)
		}
		version = recordWorkflowFailureForPin(t, s, grant, attemptID, 0, "escalated-seed-"+strconv.FormatInt(cycle, 10), failureAt...)
	}
	pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	// The derived failure record binds the dispatch window's own epoch, not
	// a literal cycle count: the substance is three counted failed attempts
	// bound to the third attempt's identity and its fenced epoch.
	// The fenced epoch comes from the failed attempt's worker-attempt row.
	failedAttemptEpoch := attemptEpoch(t, s, attemptID)
	if pin.Correction == nil || !pin.Correction.Escalated || pin.Correction.AttemptCount != 3 || pin.Correction.FailedAttemptID != attemptID {
		t.Fatalf("escalated correction = %#v, want three failed attempts bound to %s", pin.Correction, attemptID)
	}
	if pin.Correction.FailedAttemptEpoch != failedAttemptEpoch {
		t.Fatalf("escalated correction epoch %d, want the third attempt's own fenced epoch %d", pin.Correction.FailedAttemptEpoch, failedAttemptEpoch)
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

func applyEscalatedWorkerDispatchAndFailure(t *testing.T, s *store.Store, grant Authority, attemptID, suffix string, failureAt ...time.Time) {
	t.Helper()
	lane := retryLane(t)
	job := authorizedWorkerJob(t, s, attemptID)
	dispatch := store.Event{EventID: suffix + "-dispatch-" + attemptID, Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: retryJSON(store.WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, PacketDigest: "sha256:" + strings.Repeat("c", 64), ReadbackModel: "openai/gpt-5.6-luna", PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion, WorkerJob: job})}
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
	var pinnedVersion int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_version FROM workflow_instances WHERE work_id='work-1'`).Scan(&pinnedRef, &pinnedVersion); err != nil {
		t.Fatal(err)
	}
	derivedHead := map[string]int64{"workflow.implementation": 29, "workflow.break_fix": 26, "workflow.research": 17}[pinnedRef]
	if pinnedVersion >= derivedHead && derivedHead > 0 {
		// The canonical head derives the record: the store's own live-authoring
		// tests pin the derivation rule (implementation v29+, break_fix v26+,
		// research v17+), and this fixture's pin rides it. The worker
		// session's lane dispatch precedes its terminal fact, and the seam
		// derives the record in the same commit.
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
			ContractDigest: ManifestDigest, Now: fixedTime(),
		})
		return err
	}); err != nil {
		t.Fatalf("released-pin failure record for %s: %v", attemptID, err)
	}
	return workVersion(t, s, "work-1")
}

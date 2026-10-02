package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// seedGenericOneOffVerifyWorkflow initializes a generic_one_off instance for
// work-1, pins it at the verify checkpoint under an approved contract, and
// seeds the active worktree claim the fenced dispatch requires, so the
// review-lane dispatch loop the escalation wall arms can run there.
func seedGenericOneOffVerifyWorkflow(t *testing.T, s *store.Store, grant Authority) string {
	t.Helper()
	seedCurrentWorkflowDomainFixture(t, s)
	lookup, err := store.BuiltinWorkflowDefinitionForRef("workflow.generic_one_off")
	if err != nil {
		t.Fatal(err)
	}
	registered, err := store.BuiltinWorkflowRegistry().Register(lookup.Definition)
	if err != nil {
		t.Fatal(err)
	}
	owner := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(context.Background(), tx, store.WorkflowInitializationRequest{WorkID: "work-1", Definition: registered, Actor: owner, Now: fixedTime()})
	}); err != nil {
		t.Fatal(err)
	}
	ownerRef, err := store.WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		UPDATE workflow_instances SET current_step='verify' WHERE work_id='work-1';
		INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES('work-1',1,'approved retry objective','internal_sqlite','[]','[]','now',?,'[]','[]',0,'prototype_internal');
		INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES('work-1',1,'predicate:primary',0,'check','{"kind":"check","check_ref":"check:workflow","immutable_subject_ref":"commit:verify-review","expected_result":"pass"}');
		DELETE FROM fold_guard`, ownerRef); err != nil {
		t.Fatalf("seed generic_one_off verify projections: %v", err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at) VALUES(?,?,?,?,?,?,?,'active',?);
		DELETE FROM fold_guard`, store.WorktreeSetID("work-1"), "project-1", "claim:verify-wall", "verify-wall", strings.Repeat("a", 40), path, "repo:verify-wall", fixedTime().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seed verify-wall worktree: %v", err)
	}
	var claimed int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worktree_entries WHERE set_id=? AND state='active'`, store.WorktreeSetID("work-1")).Scan(&claimed); err != nil || claimed != 1 {
		t.Fatalf("verify-wall worktree claim count=%d err=%v", claimed, err)
	}
	return path
}

// runGenericOneOffStoreAction applies one store-level workflow action so the
// fixture can open dispatch windows and record the failure without minting
// approval challenges on the mutation boundary.
func runGenericOneOffStoreAction(t *testing.T, s *store.Store, action string, fields map[string]any, actor store.WorkflowActor, sessionWorktree, suffix string) {
	t.Helper()
	version := workVersion(t, s, "work-1")
	operationID := "generic-verify-" + action + "-" + suffix + "-" + strconv.FormatInt(version, 10)
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		_, err := store.ApplyWorkflowActionTx(context.Background(), tx, store.BuiltinWorkflowRegistry(), store.WorkflowActionExecutionRequest{
			WorkID: "work-1", ExpectedVersion: version, ActionID: action, Payload: raw, Actor: actor, SessionWorktree: sessionWorktree,
			AcceptedInputsDigest: "sha256:" + strings.Repeat("f", 64) + operationID, IdempotencyIdentity: operationID, OperationID: operationID,
			PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID, RequestID: "request:" + operationID,
			ContractDigest: ManifestDigest, Now: fixedTime(),
		})
		return err
	}); err != nil {
		t.Fatalf("store action %s (%s): %v", action, suffix, err)
	}
}

// seedGenericOneOffVerifyWall drives three failed review-lane attempts
// through the verify checkpoint's dispatch action, so the same-step wall is
// armed with three counted failures and no correction record stands yet. It
// returns the third attempt's identity and epoch for the failure record the
// journey records after the wall's own refusal.
func seedGenericOneOffVerifyWall(t *testing.T, s *store.Store, grant Authority, worktree string) (string, int64) {
	t.Helper()
	owner := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	var failedID string
	var failedEpoch int64
	for cycle := 1; cycle <= 3; cycle++ {
		attemptID := fmt.Sprintf("attempt:work-1:verify-%d", cycle)
		runGenericOneOffStoreAction(t, s, "dispatch_worker", map[string]any{
			"attempt_id":    attemptID,
			"worker_packet": verifyWallPacket(t, attemptID, nil),
		}, owner, worktree, "dispatch-"+strconv.Itoa(cycle))
		var epoch int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id='work-1' AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' ORDER BY seq DESC LIMIT 1`, store.WorkflowActionStarted).Scan(&epoch); err != nil {
			t.Fatal(err)
		}
		applyVerifyWallDispatchAndFailure(t, s, grant, attemptID)
		if cycle == 3 {
			failedID, failedEpoch = attemptID, epoch
		}
	}
	return failedID, failedEpoch
}

// verifyWallReviewLane returns the builtin lane whose capability class the
// verify checkpoint admits.
func verifyWallReviewLane(t *testing.T) store.LaneDefinition {
	t.Helper()
	for _, lane := range store.BuiltinLaneDefinitions() {
		if lane.CapabilityClass == "review" {
			return lane
		}
	}
	t.Fatal("no builtin lane carries the review capability class")
	return store.LaneDefinition{}
}

// verifyWallPacket builds the lane packet the verify checkpoint's dispatch
// consumes, on the review lane the step admits.
func verifyWallPacket(t *testing.T, attemptID string, correction *store.WorkflowCorrectionContext) map[string]any {
	t.Helper()
	lane := verifyWallReviewLane(t)
	inputs := map[string]any{"task": "review the bounded change", "binding": map[string]any{"objective_source": "contract_premise", "work_version": 1, "contract_version": 1, "assigned_result": "contract_findings"}, "constraints": []string{"preserve the approved contract"}}
	if correction != nil {
		inputs["correction"] = correction
	}
	return map[string]any{"schema_version": "1.0", "attempt_id": attemptID, "lane_id": lane.ID, "lane_version": lane.Version, "lane_digest": lane.Digest, "work_id": "work-1", "step_id": "verify", "inputs": inputs}
}

// applyVerifyWallDispatchAndFailure records the lane dispatch on the review
// lane and the failed report for one verify-wall attempt.
func applyVerifyWallDispatchAndFailure(t *testing.T, s *store.Store, grant Authority, attemptID string) {
	t.Helper()
	lane := verifyWallReviewLane(t)
	dispatch := store.Event{EventID: "verify-wall-dispatch-" + attemptID, Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: retryJSON(store.WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, PacketDigest: "sha256:" + strings.Repeat("c", 64), ReadbackModel: "openai/gpt-5.6-luna", PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion})}
	failure := store.Event{EventID: "verify-wall-failed-" + attemptID, Kind: store.WorkerFailed, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: retryJSON(store.WorkerFailedPayload{AttemptID: attemptID, ReadbackModel: "openai/gpt-5.6-luna", FailureKind: store.WorkerFailureWorkerError, Detail: "verify review failed"})}
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
		t.Fatalf("seed verify-wall dispatch and failure for %s: %v", attemptID, err)
	}
}

// recordGenericOneOffWorkerDispatch records one lane actor dispatch for the
// named attempt on the review lane the verify checkpoint admits: the event
// whose existence materializes the attempt and consumes the correction record.
func recordGenericOneOffWorkerDispatch(t *testing.T, s *store.Store, grant Authority, attemptID, suffix string) {
	t.Helper()
	lane := verifyWallReviewLane(t)
	dispatch := store.Event{EventID: suffix + "-dispatch-" + attemptID, Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: retryJSON(store.WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, PacketDigest: "sha256:" + strings.Repeat("c", 64), ReadbackModel: "openai/gpt-5.6-luna", PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion})}
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		enriched, err := store.PrepareLaneActorDispatch(context.Background(), tx, dispatch, grant.PrincipalRef, grant.ClientRef)
		if err != nil {
			return err
		}
		_, err = store.AppendLaneActorDispatchTx(context.Background(), tx, enriched)
		return err
	}); err != nil {
		t.Fatalf("record lane dispatch %s: %v", attemptID, err)
	}
}

// TestGenericOneOffVerifyWallKeepsTheEscalationOperatorApprovable pins the
// CON-729 journey on a generic_one_off verify checkpoint through the mutation
// boundary. Three counted same-step failed review dispatches arm the same-step
// wall, whose exact store-level refusal still fires on an unapproved dispatch;
// the boundary then mints the approval challenge bound to the failed attempt
// identity and epoch; one signed approval admits a dispatch intent whose
// worker never materializes; the interrupted intent leaves the correction
// record live, so a fresh challenge mints again; and the matching worker
// dispatch consumes the record only once it exists.
func TestGenericOneOffVerifyWallKeepsTheEscalationOperatorApprovable(t *testing.T) {
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition", "worker_dispatch"})
	worktree := seedGenericOneOffVerifyWorkflow(t, s, grant)
	grant.Worktree = worktree
	failedID, failedEpoch := seedGenericOneOffVerifyWall(t, s, grant, worktree)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil || pin.Correction != nil {
		t.Fatalf("correction before the failure record = %#v err=%v, want none", pin.Correction, err)
	}

	// The armed wall refuses an unapproved store-level dispatch with the
	// exact same-step refusal: three counted failures, no approval bound.
	retryID := "attempt:work-1:verify-4"
	unapprovedPayload, err := json.Marshal(map[string]any{
		"attempt_id":    retryID,
		"worker_packet": verifyWallPacket(t, retryID, nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	unapprovedVersion := workVersion(t, s, "work-1")
	unapprovedErr := s.Transact(context.Background(), func(tx *store.Transaction) error {
		_, err := store.ApplyWorkflowActionTx(context.Background(), tx, store.BuiltinWorkflowRegistry(), store.WorkflowActionExecutionRequest{
			WorkID: "work-1", ExpectedVersion: unapprovedVersion, ActionID: "dispatch_worker", Payload: unapprovedPayload,
			Actor:                store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent},
			SessionWorktree:      worktree,
			AcceptedInputsDigest: "sha256:" + strings.Repeat("e", 64), IdempotencyIdentity: "generic-verify-unapproved", OperationID: "generic-verify-unapproved",
			PrincipalRef: grant.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "generic-verify-unapproved", RequestID: "request:generic-verify-unapproved",
			ContractDigest: ManifestDigest, Now: fixedTime(),
		})
		return err
	})
	var refusal *store.Failure
	if !errors.As(unapprovedErr, &refusal) || refusal.Kind != store.KindApprovalRequired {
		t.Fatalf("unapproved same-step dispatch = %v, want approval_required", unapprovedErr)
	}
	if !strings.Contains(refusal.Detail, "worker dispatch at step verify reached the three-failed-attempt limit: 3 failed attempts dispatched at this step since the last accepted result or step entry") {
		t.Fatalf("same-step refusal does not name the counted population: %q", refusal.Detail)
	}

	// The hold-mode failure record dispositions the third failed review, and
	// the failed disposition stands as the unconsumed correction record.
	owner := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	runGenericOneOffStoreAction(t, s, "record_worker_failure", map[string]any{"attempt_id": failedID, "attempt_epoch": failedEpoch}, owner, "", "record-3")
	pin, err = store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil || pin.Correction == nil || !pin.Correction.Escalated || pin.Correction.FailedAttemptID != failedID || pin.Correction.FailedAttemptEpoch != failedEpoch {
		t.Fatalf("escalated correction = %#v, want the failed attempt %s at epoch %d", pin.Correction, failedID, failedEpoch)
	}

	// The mutation boundary mints the operator challenge bound to the failed
	// attempt identity and epoch, so the wall keeps its approvable escape.
	version := workVersion(t, s, "work-1")
	input := map[string]any{
		"work_id": "work-1", "expected_version": version, "action_id": "dispatch_worker",
		"idempotency_key": "verify-wall-challenge-1", "fields": map[string]any{"attempt_id": retryID, "worker_packet": verifyWallPacket(t, retryID, pin.Correction)},
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	env.RequestID = "request:verify-wall-challenge-1"
	challenge := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("same-step wall retry without approval = %+v, want approval_required", challenge.Error)
	}
	details := challenge.Error.Details
	challengeRef, _ := details["approval_ref"].(string)
	if len(challengeRef) != 64 {
		t.Fatalf("same-step wall challenge carries no approval reference: %+v", details)
	}
	if got, _ := details["action_id"].(string); got != "dispatch_worker" {
		t.Fatalf("same-step wall challenge action_id = %v", details["action_id"])
	}
	assertBindingContains(t, details["scope"], "failed_attempt_id:"+failedID)
	assertBindingContains(t, details["versions"], "failed_attempt_epoch:"+strconv.FormatInt(failedEpoch, 10))
	assertBindingContains(t, details["versions"], "contract:1")
	assertBindingContains(t, details["versions"], "work:"+strconv.FormatInt(version, 10))

	// The signed approval admits exactly one dispatch intent, and the session
	// then dies before the host dispatches the worker.
	approvedInput := cloneWithApproval(t, input, challengeRef)
	approvedRaw, err := json.Marshal(approvedInput)
	if err != nil {
		t.Fatal(err)
	}
	scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "failed_attempt_id": failedID, "scope_version": scopeVersion}
	versions := map[string]any{"work": version, "contract": int64(1), "failed_attempt_epoch": failedEpoch}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, mutationDigest("concord_work_transition", "workflow_action", env, approvedRaw), scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), nonceForChallenge(challengeRef))
	env.RequestID = "request:verify-wall-approved-1"
	approved := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approvedRaw}, env)
	if approved.Outcome != OutcomeOK {
		t.Fatalf("approved same-step wall retry = %+v", approved.Error)
	}

	// The interruption: the dispatch intent folded, and no worker.dispatched
	// event exists for the attempt. The completion's durable in-flight
	// binding is the one trace the interrupted retry leaves.
	if got := countRows(t, s.DatabaseForTesting(), fmt.Sprintf(`SELECT count(*) FROM domain_events WHERE subject_id='work-1' AND kind='%s' AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')='%s'`, store.WorkflowActionCompleted, retryID)); got != 1 {
		t.Fatalf("approved retry recorded %d dispatch completions for %s, want 1", got, retryID)
	}
	if got := countRows(t, s.DatabaseForTesting(), fmt.Sprintf(`SELECT count(*) FROM domain_events WHERE subject_id='work-1' AND kind='%s' AND json_extract(payload,'$.attempt_id')='%s'`, store.WorkerDispatched, retryID)); got != 0 {
		t.Fatalf("interrupted retry shows %d lane dispatches for %s, want 0", got, retryID)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1' AND attempt_id='`+retryID+`' AND lifecycle_state='in_flight'`); got != 1 {
		t.Fatalf("interrupted retry left %d in-flight worker attempt rows for %s, want 1", got, retryID)
	}

	// The half-materialized dispatch consumed nothing: the retry binding
	// still names the failed attempt, so the wall's escape stays reachable.
	binding, err := store.WorkflowFailedWorkerRetryBinding(context.Background(), s, nil, "work-1")
	if err != nil || binding == nil || binding.FailedAttemptID != failedID || binding.FailedAttemptEpoch != failedEpoch {
		t.Fatalf("retry binding after the interruption = %+v err=%v, want the live record", binding, err)
	}

	// A fresh unapproved retry mints a fresh challenge: the wall never
	// dead-ends while the record stands.
	retry2ID := "attempt:work-1:verify-5"
	version = workVersion(t, s, "work-1")
	input2 := map[string]any{
		"work_id": "work-1", "expected_version": version, "action_id": "dispatch_worker",
		"idempotency_key": "verify-wall-challenge-2", "fields": map[string]any{"attempt_id": retry2ID, "worker_packet": verifyWallPacket(t, retry2ID, pin.Correction)},
	}
	raw2, err := json.Marshal(input2)
	if err != nil {
		t.Fatal(err)
	}
	env.HostApproval = nil
	env.RequestID = "request:verify-wall-challenge-2"
	challenge2 := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw2}, env)
	if challenge2.Error == nil || challenge2.Error.Kind != "approval_required" {
		t.Fatalf("post-interruption challenge = %+v, want approval_required", challenge2.Error)
	}
	challenge2Ref, _ := challenge2.Error.Details["approval_ref"].(string)
	if len(challenge2Ref) != 64 || challenge2Ref == challengeRef {
		t.Fatalf("post-interruption challenge ref = %q, want a fresh approval reference", challenge2Ref)
	}

	// The signed approval admits the re-dispatch, and this time the worker
	// materializes: the matching worker.dispatched consumes the record.
	approved2Input := cloneWithApproval(t, input2, challenge2Ref)
	approved2Raw, err := json.Marshal(approved2Input)
	if err != nil {
		t.Fatal(err)
	}
	env.HostApproval = signedHostApproval(privateKey, challenge2Ref, mutationDigest("concord_work_transition", "workflow_action", env, approved2Raw), scope, map[string]any{"work": version, "contract": int64(1), "failed_attempt_epoch": failedEpoch}, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), nonceForChallenge(challenge2Ref))
	env.RequestID = "request:verify-wall-approved-2"
	approved2 := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approved2Raw}, env)
	if approved2.Outcome != OutcomeOK {
		t.Fatalf("approved re-dispatch after the interruption = %+v", approved2.Error)
	}
	// The materialized re-dispatch consumes the correction record, and the
	// same-step wall keeps the escape on the counted failures: the binding
	// names the latest counted failed attempt, so a fresh refusal stays
	// operator approvable (CD-0164).
	recordGenericOneOffWorkerDispatch(t, s, grant, retry2ID, "verify-wall-materialized")
	binding, err = store.WorkflowFailedWorkerRetryBinding(context.Background(), s, nil, "work-1")
	if err != nil || binding == nil || binding.FailedAttemptID != failedID || binding.FailedAttemptEpoch != failedEpoch || binding.CorrectionAttempts != 0 {
		t.Fatalf("retry binding after the materialized re-dispatch = %+v err=%v, want the same-step wall binding for %s at epoch %d", binding, err, failedID, failedEpoch)
	}
	pin, err = store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil || pin.Correction != nil {
		t.Fatalf("work pin correction after the materialized re-dispatch = %#v err=%v, want none", pin.Correction, err)
	}
}

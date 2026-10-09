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

// seedGenericOneOffVerifyWorkflow initializes a generic_one_off instance for
// work-1, pins it at the verify checkpoint under an approved contract, and
// seeds the active worktree claim the fenced dispatch requires, so the
// review-lane dispatch loop the escalation wall arms can run there.
func seedGenericOneOffVerifyWorkflow(t *testing.T, s *store.Store, grant Authority) string {
	t.Helper()
	return seedGenericOneOffWorkflowAtStep(t, s, grant, "verify")
}

// seedGenericOneOffWorkflowAtStep seeds the same generic_one_off fixture
// pinned at the named step, so a journey can spend its non-progress budget at
// an external-effect step and read the wall at a checkpoint step.
func seedGenericOneOffWorkflowAtStep(t *testing.T, s *store.Store, grant Authority, step string) string {
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
		UPDATE workflow_instances SET current_step='`+step+`' WHERE work_id='work-1';
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
// fixture can open dispatch windows, disposition results, and supersede the
// contract without minting approval challenges on the mutation boundary.
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
			"worker_packet": verifyWallPacket(t, s, attemptID, nil),
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
// consumes, on the review lane the step admits, carrying the live correction
// record when one stands: the fold refuses a packet that does not consume it.
func verifyWallPacket(t *testing.T, s *store.Store, attemptID string, correction *store.WorkflowCorrectionContext) map[string]any {
	t.Helper()
	return verifyWallPacketForStep(t, s, attemptID, correction, "verify")
}

// verifyWallPacketForStep builds the same review-lane packet pinned to the
// named step, whose id the fold's step match reads back from the packet.
func verifyWallPacketForStep(t *testing.T, s *store.Store, attemptID string, correction *store.WorkflowCorrectionContext, step string) map[string]any {
	t.Helper()
	lane := verifyWallReviewLane(t)
	inputs := map[string]any{"task": "review the bounded change", "constraints": []string{"preserve the approved contract"}}
	if correction != nil {
		inputs["correction"] = correction
	}
	return bindPacketToRecordedState(t, s, map[string]any{"schema_version": "1.0", "attempt_id": attemptID, "lane_id": lane.ID, "lane_version": lane.Version, "lane_digest": lane.Digest, "work_id": "work-1", "step_id": step, "inputs": inputs})
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

// recordGenericOneOffWorkerReviewCompletion records the completed report for
// one review-lane attempt with the named verdict: the judgeable result
// reject_worker_result dispositions and the no_ship verdict the non-progress
// population counts. A no_ship verdict must bind at least one finding.
func recordGenericOneOffWorkerReviewCompletion(t *testing.T, s *store.Store, attemptID, verdict string, findings []store.WorkerReviewFinding) {
	t.Helper()
	if findings == nil {
		findings = []store.WorkerReviewFinding{}
	}
	completion := store.Event{EventID: "verify-wall-completed-" + attemptID, Kind: store.WorkerCompleted, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 3, Payload: retryJSON(store.WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: "openai/gpt-5.6-luna", ReportSchemaVersion: store.WorkerReportSchemaVersion, EvidenceOrigin: store.WorkerEvidenceLegacyUnavailable, Review: &store.WorkerReviewBlock{Verdict: verdict, Findings: findings}})}
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		_, err := store.ApplyOperationTx(context.Background(), tx, store.Operation{Events: []store.Event{completion}})
		return err
	}); err != nil {
		t.Fatalf("record completed attempt %s: %v", attemptID, err)
	}
}

// verifyWallNoShipFinding is the one finding a no_ship review binds.
func verifyWallNoShipFinding() []store.WorkerReviewFinding {
	return []store.WorkerReviewFinding{{Severity: "P2", Confidence: "high", Detail: "the bounded change does not match the approved contract"}}
}

// verifyWallAttemptEpoch returns the attempt epoch the latest verify-step
// dispatch start fenced. Call it immediately after the dispatch it names.
func verifyWallAttemptEpoch(t *testing.T, s *store.Store) int64 {
	t.Helper()
	return verifyWallAttemptEpochAtStep(t, s, "verify")
}

// verifyWallAttemptEpochAtStep reads the latest fenced dispatch start epoch
// at the named step.
func verifyWallAttemptEpochAtStep(t *testing.T, s *store.Store, step string) int64 {
	t.Helper()
	var epoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(MAX(json_extract(payload,'$.attempt_epoch')),0) FROM domain_events WHERE subject_id='work-1' AND kind=? AND json_extract(payload,'$.step_id')=?`, store.WorkflowActionStarted, step).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	return epoch
}

// verifyWallCurrentStep reads the instance's pinned step.
func verifyWallCurrentStep(t *testing.T, s *store.Store) string {
	t.Helper()
	var step string
	if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id='work-1'`).Scan(&step); err != nil {
		t.Fatal(err)
	}
	return step
}

// rejectVerifyWallResult records the rejected disposition for one completed
// review-lane attempt, carrying the complete open finding set the rejection
// reports: the population the findings convergence basis compares.
func rejectVerifyWallResult(t *testing.T, s *store.Store, grant Authority, attemptID string, epoch int64, openFindings []string) {
	t.Helper()
	owner := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	fields := map[string]any{
		"attempt_id": attemptID, "attempt_epoch": epoch,
		"diagnosis": "the completed review still reports open findings", "strategy": "fix the remaining findings and review again",
		"predicate_ids": []string{"predicate:primary"}, "evidence_refs": []string{"evidence:verify-wall-reject"},
	}
	if openFindings != nil {
		fields["open_finding_ids"] = openFindings
	}
	runGenericOneOffStoreAction(t, s, "reject_worker_result", fields, owner, "", "reject-"+attemptID)
}

// supersedeVerifyWallContract runs the operator-approved contract supersession
// at store level, the changed approach the convergence basis reads. The
// operator actor is the distinct identity the fold's operator guard demands,
// and the store-level run mints nothing on the agent challenge table.
func supersedeVerifyWallContract(t *testing.T, s *store.Store, owner store.WorkflowActor, version, next int64, reason string) {
	t.Helper()
	approvalRef := strings.Repeat("a", 64)
	operator := store.WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "approval:" + approvalRef, SessionRef: "session/verify-wall-supersede", ActorClass: store.ActorOperator}
	fields := map[string]any{
		"contract_version": next,
		"premise":          fmt.Sprintf("corrected verify objective %d: %s", next, reason),
		"outcome_predicates": []map[string]any{{
			"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check",
			"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:verify-review", "expected_result": "pass"},
		}},
		"required_evidence": []string{"artifact"}, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{},
		"rigor_class": "prototype_internal", "supersede_reason": reason, "audit_evidence": []string{"evidence:verify-wall-supersede"},
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	operationID := "verify-wall-supersede-" + strconv.FormatInt(next, 10)
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

// verifyWallChallenges counts the approval challenges the mutation boundary
// minted so far: the durable proof a journey minted none.
func verifyWallChallenges(t *testing.T, s *store.Store) int {
	t.Helper()
	return countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`)
}

// verifyWallDispatchInput builds one dispatch_worker workflow action input
// for the mutation boundary at the verify checkpoint, bound to the live work
// version, the recorded lane packet, and the standing correction record.
func verifyWallDispatchInput(t *testing.T, s *store.Store, attemptID, key string, correction *store.WorkflowCorrectionContext) (map[string]any, []byte) {
	t.Helper()
	return verifyWallDispatchInputForStep(t, s, attemptID, key, correction, "verify")
}

// verifyWallDispatchInputForStep builds the same boundary input with the
// packet pinned to the named step.
func verifyWallDispatchInputForStep(t *testing.T, s *store.Store, attemptID, key string, correction *store.WorkflowCorrectionContext, step string) (map[string]any, []byte) {
	t.Helper()
	input := map[string]any{
		"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "action_id": "dispatch_worker",
		"idempotency_key": key, "fields": map[string]any{"attempt_id": attemptID, "worker_packet": verifyWallPacketForStep(t, s, attemptID, correction, step)},
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return input, raw
}

// verifyWallDispatchCompletions counts the dispatch completions the fold
// recorded for one attempt.
func verifyWallDispatchCompletions(t *testing.T, s *store.Store, attemptID string) int {
	t.Helper()
	return countRows(t, s.DatabaseForTesting(), fmt.Sprintf(`SELECT count(*) FROM domain_events WHERE subject_id='work-1' AND kind='%s' AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')='%s'`, store.WorkflowActionCompleted, attemptID))
}

// verifyWallRecordedConvergence reads the convergence basis the fold recorded
// on the dispatch completion for one attempt.
func verifyWallRecordedConvergence(t *testing.T, s *store.Store, attemptID string) store.WorkflowRetryConvergence {
	t.Helper()
	var raw []byte
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.retry_convergence') FROM domain_events WHERE subject_id='work-1' AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=?`, store.WorkflowActionCompleted, attemptID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var basis store.WorkflowRetryConvergence
	if err := json.Unmarshal(raw, &basis); err != nil {
		t.Fatalf("recorded convergence for %s is malformed: %s", attemptID, raw)
	}
	return basis
}

// verifyWallPinCorrection reads the standing correction record from the work
// pin, failing when none stands.
func verifyWallPinCorrection(t *testing.T, s *store.Store) *store.WorkflowCorrectionContext {
	t.Helper()
	pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	if pin.Correction == nil {
		t.Fatal("no correction record stands on the work pin")
	}
	return pin.Correction
}

// TestGenericOneOffBelowWallFailedRetryKeepsTheExactApproval pins the
// below-limit behavior CD-0148 keeps: one counted failure with a recorded
// failed disposition makes the next dispatch an exact-approval route. The
// boundary mints the challenge bound to the failed attempt's identity, epoch,
// and contract, the signed approval admits exactly one dispatch intent, and
// the next retry mints a fresh challenge.
func TestGenericOneOffBelowWallFailedRetryKeepsTheExactApproval(t *testing.T) {
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition", "worker_dispatch"})
	worktree := seedGenericOneOffVerifyWorkflow(t, s, grant)
	grant.Worktree = worktree
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	owner := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}

	// One failed attempt with its recorded failed disposition: below the
	// limit, the ordinary retry approval route.
	first := "attempt:work-1:verify-1"
	runGenericOneOffStoreAction(t, s, "dispatch_worker", map[string]any{
		"attempt_id": first, "worker_packet": verifyWallPacket(t, s, first, nil),
	}, owner, worktree, "dispatch-1")
	failedEpoch := verifyWallAttemptEpoch(t, s)
	applyVerifyWallDispatchAndFailure(t, s, grant, first)
	runGenericOneOffStoreAction(t, s, "record_worker_failure", map[string]any{"attempt_id": first, "attempt_epoch": failedEpoch}, owner, "", "record-1")
	correction := verifyWallPinCorrection(t, s)
	if correction.Escalated || correction.FailedAttemptID != first || correction.FailedAttemptEpoch != failedEpoch {
		t.Fatalf("below-wall correction = %+v, want the unescalated failed attempt %s at epoch %d", correction, first, failedEpoch)
	}
	if got := verifyWallChallenges(t, s); got != 0 {
		t.Fatalf("fixture minted %d approval challenges, want 0", got)
	}

	// The boundary mints the operator challenge bound to the failed
	// attempt, its dispatch completion epoch, and the active contract.
	version := workVersion(t, s, "work-1")
	retryID := "attempt:work-1:verify-2"
	input, raw := verifyWallDispatchInput(t, s, retryID, "verify-below-wall-1", correction)
	env.RequestID = "request:verify-below-wall-1"
	challenge := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("below-wall retry without approval = %+v, want approval_required", challenge.Error)
	}
	details := challenge.Error.Details
	challengeRef, _ := details["approval_ref"].(string)
	if len(challengeRef) != 64 {
		t.Fatalf("below-wall challenge carries no approval reference: %+v", details)
	}
	if got, _ := details["action_id"].(string); got != "dispatch_worker" {
		t.Fatalf("below-wall challenge action_id = %v", details["action_id"])
	}
	if got, _ := details["contract_version"].(string); got != "1" {
		t.Fatalf("below-wall challenge contract_version = %v", details["contract_version"])
	}
	summary := challenge.Error.ConsequenceSummary
	if summary == nil {
		t.Fatal("below-wall challenge lacks a consequence summary")
	}
	assertBindingContains(t, summary.Scope, "failed_attempt_id:"+first)
	assertBindingContains(t, summary.Versions, "failed_attempt_epoch:"+strconv.FormatInt(failedEpoch, 10))
	assertBindingContains(t, summary.Versions, "contract:1")
	assertBindingContains(t, summary.Versions, "work:"+strconv.FormatInt(version, 10))
	if got := verifyWallChallenges(t, s); got != 1 {
		t.Fatalf("below-wall retry minted %d approval challenges, want 1", got)
	}

	// The signed approval admits exactly one dispatch intent.
	approvedInput := cloneWithApproval(t, input, challengeRef)
	approvedRaw, err := json.Marshal(approvedInput)
	if err != nil {
		t.Fatal(err)
	}
	scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "failed_attempt_id": first, "scope_version": scopeVersion}
	versions := map[string]any{"work": version, "contract": int64(1), "failed_attempt_epoch": failedEpoch}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, mutationDigest("concord_work_transition", "workflow_action", env, approvedRaw), scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), nonceForChallenge(challengeRef))
	env.RequestID = "request:verify-below-wall-2"
	approved := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approvedRaw}, env)
	if approved.Outcome != OutcomeOK {
		t.Fatalf("approved below-wall retry = %+v", approved.Error)
	}
	if got := verifyWallDispatchCompletions(t, s, retryID); got != 1 {
		t.Fatalf("approved below-wall retry recorded %d dispatch completions, want 1", got)
	}
	// Consumption retires the challenge without deleting it.
	if got := verifyWallChallenges(t, s); got != 1 {
		t.Fatalf("approved below-wall retry changed the challenge count to %d, want 1", got)
	}

	// The next retry needs a fresh exact approval: the consumed challenge
	// never becomes a standing dispatch right.
	nextID := "attempt:work-1:verify-3"
	env.HostApproval = nil
	_, freshRaw := verifyWallDispatchInput(t, s, nextID, "verify-below-wall-3", correction)
	env.RequestID = "request:verify-below-wall-3"
	fresh := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: freshRaw}, env)
	if fresh.Error == nil || fresh.Error.Kind != "approval_required" {
		t.Fatalf("retry after one approved intent = %+v, want a fresh approval_required", fresh.Error)
	}
	freshRef, _ := fresh.Error.Details["approval_ref"].(string)
	if len(freshRef) != 64 || freshRef == challengeRef {
		t.Fatalf("fresh challenge ref = %q, want a distinct approval reference", freshRef)
	}
	if got := verifyWallChallenges(t, s); got != 2 {
		t.Fatalf("second below-wall retry minted %d approval challenges in total, want 2", got)
	}
}

// TestGenericOneOffEscalatedWallAdmitsOnlyBehindStoreConvergence pins the
// amended CD-0148 wall on a generic_one_off verify checkpoint through the
// mutation boundary. Three counted failures with an escalated correction
// record refuse the next dispatch with missing_evidence and mint no approval
// challenge; a supplied operator approval neither bypasses the refusal nor
// mints one; a superseded contract derives the approach_changed basis and
// admits one fresh fenced dispatch without any challenge; the interrupted
// dispatch consumes the basis, so a blind re-dispatch refuses until a fresh
// supersession derives a new basis.
func TestGenericOneOffEscalatedWallAdmitsOnlyBehindStoreConvergence(t *testing.T) {
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition", "worker_dispatch"})
	worktree := seedGenericOneOffVerifyWorkflow(t, s, grant)
	grant.Worktree = worktree
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	owner := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	failedID, failedEpoch := seedGenericOneOffVerifyWall(t, s, grant, worktree)
	runGenericOneOffStoreAction(t, s, "record_worker_failure", map[string]any{"attempt_id": failedID, "attempt_epoch": failedEpoch}, owner, "", "record-3")
	correction := verifyWallPinCorrection(t, s)
	if !correction.Escalated || correction.FailedAttemptID != failedID {
		t.Fatalf("escalated correction = %+v, want the failed attempt %s", correction, failedID)
	}

	// The wall refuses a fresh dispatch without a derivable basis, and the
	// refusal never becomes an operator approval ask.
	version := workVersion(t, s, "work-1")
	retryID := "attempt:work-1:verify-4"
	input, raw := verifyWallDispatchInput(t, s, retryID, "verify-wall-convergence-1", correction)
	env.RequestID = "request:verify-wall-convergence-1"
	refused := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if refused.Error == nil || refused.Error.Kind != "missing_evidence" {
		t.Fatalf("escalated retry without a basis = %+v, want missing_evidence", refused.Error)
	}
	if !strings.Contains(refused.Error.Message, "worker correction reached the three-attempt limit without a convergence basis: 3 non-progress attempts since the last accepted productive result (step verify)") {
		t.Fatalf("escalated refusal does not name the counted population: %q", refused.Error.Message)
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
	if after := workVersion(t, s, "work-1"); after != version {
		t.Fatalf("refused escalation changed the work version %d -> %d", version, after)
	}
	binding, err := store.WorkflowFailedWorkerRetryBinding(context.Background(), s, nil, "work-1")
	if err != nil || binding != nil {
		t.Fatalf("retry approval binding behind the escalated wall = %+v err=%v, want none", binding, err)
	}

	// A supplied operator approval has no effect: no challenge exists to
	// consume, and the fold still refuses without a basis.
	approvedInput := cloneWithApproval(t, input, strings.Repeat("b", 64))
	approvedRaw, err := json.Marshal(approvedInput)
	if err != nil {
		t.Fatal(err)
	}
	scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "failed_attempt_id": failedID, "scope_version": scopeVersion}
	versions := map[string]any{"work": version, "contract": int64(1), "failed_attempt_epoch": failedEpoch}
	env.HostApproval = signedHostApproval(privateKey, strings.Repeat("b", 64), mutationDigest("concord_work_transition", "workflow_action", env, approvedRaw), scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), "verify-wall-unused-approval")
	env.RequestID = "request:verify-wall-convergence-2"
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

	// A superseded contract after the latest dispatch is a changed
	// approach: the store derives the basis and the boundary admits the
	// dispatch with no approval at all.
	supersedeVerifyWallContract(t, s, owner, workVersion(t, s, "work-1"), 2, "the escalated review wall demanded a changed approach")
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
	correction = verifyWallPinCorrection(t, s)
	_, admittedRaw := verifyWallDispatchInput(t, s, retryID, "verify-wall-convergence-3", correction)
	env.RequestID = "request:verify-wall-convergence-3"
	admitted := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: admittedRaw}, env)
	if admitted.Outcome != OutcomeOK {
		t.Fatalf("converging escalated retry without any approval = %+v", admitted.Error)
	}
	if got := verifyWallChallenges(t, s); got != 0 {
		t.Fatalf("converging escalated retry minted %d approval challenges, want 0", got)
	}
	if got := verifyWallDispatchCompletions(t, s, retryID); got != 1 {
		t.Fatalf("converging escalated retry recorded %d dispatch completions, want 1", got)
	}
	basis := verifyWallRecordedConvergence(t, s, retryID)
	if basis.Basis != "approach_changed" || basis.SupersededSeq == 0 || basis.PreviousRecordSeq != 0 || basis.LatestRecordSeq != 0 {
		t.Fatalf("recorded convergence = %+v, want the approach_changed basis naming its supersession sequence", basis)
	}

	// The session dies before the host dispatches the worker: the
	// half-materialized dispatch consumed the basis, so a blind
	// re-dispatch refuses and mints no challenge.
	if got := countRows(t, s.DatabaseForTesting(), fmt.Sprintf(`SELECT count(*) FROM domain_events WHERE subject_id='work-1' AND kind='%s' AND json_extract(payload,'$.attempt_id')='%s'`, store.WorkerDispatched, retryID)); got != 0 {
		t.Fatalf("interrupted retry shows %d lane dispatches, want 0", got)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1' AND attempt_id='`+retryID+`' AND lifecycle_state='in_flight'`); got != 1 {
		t.Fatalf("interrupted retry left %d in-flight worker attempt rows, want 1", got)
	}
	version = workVersion(t, s, "work-1")
	secondID := "attempt:work-1:verify-5"
	correction = verifyWallPinCorrection(t, s)
	_, blindRaw := verifyWallDispatchInput(t, s, secondID, "verify-wall-convergence-4", correction)
	env.RequestID = "request:verify-wall-convergence-4"
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
	if after := workVersion(t, s, "work-1"); after != version {
		t.Fatalf("blind re-dispatch changed the work version %d -> %d", version, after)
	}

	// A fresh supersession derives a fresh basis, and each basis buys
	// exactly one more fresh fenced attempt.
	supersedeVerifyWallContract(t, s, owner, workVersion(t, s, "work-1"), 3, "the interrupted converging retry needed a renewed approach")
	correction = verifyWallPinCorrection(t, s)
	_, renewedRaw := verifyWallDispatchInput(t, s, secondID, "verify-wall-convergence-5", correction)
	env.RequestID = "request:verify-wall-convergence-5"
	renewed := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: renewedRaw}, env)
	if renewed.Outcome != OutcomeOK {
		t.Fatalf("renewed converging retry = %+v", renewed.Error)
	}
	if got := verifyWallChallenges(t, s); got != 0 {
		t.Fatalf("the whole escalated journey minted %d approval challenges, want 0", got)
	}
	renewedBasis := verifyWallRecordedConvergence(t, s, secondID)
	if renewedBasis.Basis != "approach_changed" || renewedBasis.SupersededSeq <= basis.SupersededSeq {
		t.Fatalf("renewed convergence = %+v, want a fresh supersession after seq %d", renewedBasis, basis.SupersededSeq)
	}
}

// TestGenericOneOffFindingsConvergenceAdmitsOneEscalatedRetry pins the
// findings basis family on the mutation boundary. Three rejected review
// results arm the escalated wall; the latest rejection's open finding set
// decides admission: a strict subset of the previous comparable rejection
// derives the findings_shrinking basis and admits exactly one fresh dispatch
// without any challenge, while an unchanged set keeps the wall closed.
func TestGenericOneOffFindingsConvergenceAdmitsOneEscalatedRetry(t *testing.T) {
	for _, tc := range []struct {
		name   string
		latest []string
		admit  bool
	}{
		{"shrinking open findings admit one retry", []string{"finding:a"}, true},
		{"unchanged open findings keep the wall closed", []string{"finding:a", "finding:b"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition", "worker_dispatch"})
			worktree := seedGenericOneOffVerifyWorkflow(t, s, grant)
			grant.Worktree = worktree
			owner := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
			env := mutationEnvelope(grant, mustScopeVersion(t, s))

			// Three review rounds: dispatch, complete, reject with the
			// reported open findings. The third rejection arms the
			// escalated wall, whatever the finding sets carried.
			findings := [][]string{{"finding:a", "finding:b"}, {"finding:a", "finding:b"}, tc.latest}
			for cycle := 1; cycle <= 3; cycle++ {
				attemptID := fmt.Sprintf("attempt:work-1:verify-reject-%d", cycle)
				pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
				if err != nil {
					t.Fatal(err)
				}
				runGenericOneOffStoreAction(t, s, "dispatch_worker", map[string]any{
					"attempt_id": attemptID, "worker_packet": verifyWallPacket(t, s, attemptID, pin.Correction),
				}, owner, worktree, "reject-dispatch-"+strconv.Itoa(cycle))
				epoch := verifyWallAttemptEpoch(t, s)
				recordGenericOneOffWorkerDispatch(t, s, grant, attemptID, "verify-wall-reject-"+strconv.Itoa(cycle))
				recordGenericOneOffWorkerReviewCompletion(t, s, attemptID, "ship", nil)
				rejectVerifyWallResult(t, s, grant, attemptID, epoch, findings[cycle-1])
			}
			correction := verifyWallPinCorrection(t, s)
			if !correction.Escalated {
				t.Fatalf("correction after three rejections = %+v, want the escalated wall", correction)
			}
			if got := verifyWallChallenges(t, s); got != 0 {
				t.Fatalf("the rejection fixture minted %d approval challenges, want 0", got)
			}

			version := workVersion(t, s, "work-1")
			retryID := "attempt:work-1:verify-converge"
			_, raw := verifyWallDispatchInput(t, s, retryID, "verify-findings-convergence-1", correction)
			env.RequestID = "request:verify-findings-convergence-1"
			attempt := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
			if !tc.admit {
				if attempt.Error == nil || attempt.Error.Kind != "missing_evidence" {
					t.Fatalf("retry behind unchanged findings = %+v, want missing_evidence", attempt.Error)
				}
				// The rejected rounds are the counted population now, so
				// the refusal names all three non-progress attempts.
				if !strings.Contains(attempt.Error.Message, "worker correction reached the three-attempt limit without a convergence basis: 3 non-progress attempts since the last accepted productive result (step verify)") {
					t.Fatalf("refusal does not name the counted population: %q", attempt.Error.Message)
				}
				if got := verifyWallChallenges(t, s); got != 0 {
					t.Fatalf("refusal behind unchanged findings minted %d approval challenges, want 0", got)
				}
				if got := verifyWallDispatchCompletions(t, s, retryID); got != 0 {
					t.Fatalf("refusal behind unchanged findings recorded %d dispatch completions, want 0", got)
				}
				if after := workVersion(t, s, "work-1"); after != version {
					t.Fatalf("refusal behind unchanged findings changed the work version %d -> %d", version, after)
				}
				return
			}
			if attempt.Outcome != OutcomeOK {
				t.Fatalf("retry behind shrinking findings = %+v", attempt.Error)
			}
			if got := verifyWallChallenges(t, s); got != 0 {
				t.Fatalf("retry behind shrinking findings minted %d approval challenges, want 0", got)
			}
			basis := verifyWallRecordedConvergence(t, s, retryID)
			if basis.Basis != "findings_shrinking" || basis.PreviousRecordSeq == 0 || basis.LatestRecordSeq <= basis.PreviousRecordSeq || basis.SupersededSeq != 0 {
				t.Fatalf("recorded convergence = %+v, want the findings_shrinking basis naming both rejection sequences", basis)
			}

			// The basis admitted exactly one dispatch: the interrupted
			// attempt consumed it, so a blind re-dispatch refuses.
			nextID := "attempt:work-1:verify-converge-2"
			correction = verifyWallPinCorrection(t, s)
			_, blindRaw := verifyWallDispatchInput(t, s, nextID, "verify-findings-convergence-2", correction)
			env.RequestID = "request:verify-findings-convergence-2"
			blind := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: blindRaw}, env)
			if blind.Error == nil || blind.Error.Kind != "missing_evidence" {
				t.Fatalf("blind re-dispatch behind the consumed findings basis = %+v, want missing_evidence", blind.Error)
			}
			if got := verifyWallChallenges(t, s); got != 0 {
				t.Fatalf("blind re-dispatch minted %d approval challenges, want 0", got)
			}
		})
	}
}

// mustScopeVersion reads the fixture's project scope version.
func mustScopeVersion(t *testing.T, s *store.Store) string {
	t.Helper()
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	return scopeVersion
}

// TestGenericOneOffNonProgressBudgetSpansStepsAndNoShipAcceptances pins the
// CD-0164 counting population behind the convergence wall: failed, rejected,
// and completed no_ship review attempts all spend one work-item budget,
// counted distinct across steps, and neither the no_ship acceptance nor the
// step change it advances renews the budget. Only a store-derived basis
// still admits over the spent budget, with no approval challenge.
func TestGenericOneOffNonProgressBudgetSpansStepsAndNoShipAcceptances(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition", "worker_dispatch"})
	worktree := seedGenericOneOffWorkflowAtStep(t, s, grant, "execute")
	grant.Worktree = worktree
	env := mutationEnvelope(grant, mustScopeVersion(t, s))
	owner := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}

	// Three non-progress attempts at the execute step, one per disposition
	// class and none dispositioned: a completed no_ship review, a failed
	// attempt, and another completed no_ship review. The population itself
	// spends the budget — no correction record stands.
	first := "attempt:work-1:execute-noship-1"
	runGenericOneOffStoreAction(t, s, "dispatch_worker", map[string]any{
		"attempt_id": first, "worker_packet": verifyWallPacketForStep(t, s, first, nil, "execute"),
	}, owner, worktree, "noship-dispatch-1")
	recordGenericOneOffWorkerDispatch(t, s, grant, first, "noship-materialize-1")
	recordGenericOneOffWorkerReviewCompletion(t, s, first, "no_ship", verifyWallNoShipFinding())

	second := "attempt:work-1:execute-failed-2"
	runGenericOneOffStoreAction(t, s, "dispatch_worker", map[string]any{
		"attempt_id": second, "worker_packet": verifyWallPacketForStep(t, s, second, nil, "execute"),
	}, owner, worktree, "noship-dispatch-2")
	applyVerifyWallDispatchAndFailure(t, s, grant, second)

	third := "attempt:work-1:execute-noship-3"
	runGenericOneOffStoreAction(t, s, "dispatch_worker", map[string]any{
		"attempt_id": third, "worker_packet": verifyWallPacketForStep(t, s, third, nil, "execute"),
	}, owner, worktree, "noship-dispatch-3")
	thirdEpoch := verifyWallAttemptEpochAtStep(t, s, "execute")
	recordGenericOneOffWorkerDispatch(t, s, grant, third, "noship-materialize-3")
	recordGenericOneOffWorkerReviewCompletion(t, s, third, "no_ship", verifyWallNoShipFinding())
	pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	if pin.Correction != nil {
		t.Fatalf("correction record behind the undispositioned population = %+v, want none", pin.Correction)
	}

	// The mixed population spent the whole budget: the wall refuses the
	// next dispatch with the non-progress population text and no challenge.
	version := workVersion(t, s, "work-1")
	retryID := "attempt:work-1:execute-wall-4"
	_, raw := verifyWallDispatchInputForStep(t, s, retryID, "noship-convergence-1", nil, "execute")
	env.RequestID = "request:noship-convergence-1"
	refused := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if refused.Error == nil || refused.Error.Kind != "missing_evidence" {
		t.Fatalf("retry behind the mixed non-progress population = %+v, want missing_evidence", refused.Error)
	}
	if !strings.Contains(refused.Error.Message, "worker correction reached the three-attempt limit without a convergence basis: 3 non-progress attempts since the last accepted productive result (step execute)") {
		t.Fatalf("refusal does not name the mixed non-progress population: %q", refused.Error.Message)
	}
	if refused.Error.RecoveryAction.Kind != "provide_evidence" {
		t.Fatalf("refusal recovery = %q, want provide_evidence and never an approval ask", refused.Error.RecoveryAction.Kind)
	}
	if got := verifyWallChallenges(t, s); got != 0 {
		t.Fatalf("refusal behind the mixed population minted %d approval challenges, want 0", got)
	}
	if got := verifyWallDispatchCompletions(t, s, retryID); got != 0 {
		t.Fatalf("refusal behind the mixed population recorded %d dispatch completions, want 0", got)
	}
	if after := workVersion(t, s, "work-1"); after != version {
		t.Fatalf("refusal behind the mixed population changed the work version %d -> %d", version, after)
	}

	// The coordinator accepts the latest no_ship report: the acceptance
	// binds the findings and advances the step, but renews no budget, and
	// the step entry it lands on renews none either.
	runGenericOneOffStoreAction(t, s, "accept_worker_result", map[string]any{"attempt_id": third, "attempt_epoch": thirdEpoch}, owner, "", "accept-noship-3")
	if step := verifyWallCurrentStep(t, s); step != "verify" {
		t.Fatalf("accepted no_ship review left the instance at step %q, want the advanced verify step", step)
	}
	if got := verifyWallChallenges(t, s); got != 0 {
		t.Fatalf("no_ship acceptance minted %d approval challenges, want 0", got)
	}
	version = workVersion(t, s, "work-1")
	fourthID := "attempt:work-1:verify-wall-5"
	_, spentRaw := verifyWallDispatchInputForStep(t, s, fourthID, "noship-convergence-2", nil, "verify")
	env.RequestID = "request:noship-convergence-2"
	spent := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: spentRaw}, env)
	if spent.Error == nil || spent.Error.Kind != "missing_evidence" {
		t.Fatalf("retry after the no_ship acceptance and step change = %+v, want missing_evidence", spent.Error)
	}
	// The budget was spent at execute and the refusal names it at the new
	// step: the population counts across steps and nothing renewed it.
	if !strings.Contains(spent.Error.Message, "worker correction reached the three-attempt limit without a convergence basis: 3 non-progress attempts since the last accepted productive result (step verify)") {
		t.Fatalf("refusal after the no_ship acceptance does not name the spent population: %q", spent.Error.Message)
	}
	if got := verifyWallChallenges(t, s); got != 0 {
		t.Fatalf("refusal after the no_ship acceptance minted %d approval challenges, want 0", got)
	}
	if got := verifyWallDispatchCompletions(t, s, fourthID); got != 0 {
		t.Fatalf("refusal after the no_ship acceptance recorded %d dispatch completions, want 0", got)
	}
	if after := workVersion(t, s, "work-1"); after != version {
		t.Fatalf("refusal after the no_ship acceptance changed the work version %d -> %d", version, after)
	}

	// The convergence escape still admits over the spent budget: a
	// superseded approach opens the wall for exactly one fresh attempt
	// without any approval challenge.
	supersedeVerifyWallContract(t, s, owner, workVersion(t, s, "work-1"), 2, "the spent non-progress budget demanded a changed approach")
	_, admittedRaw := verifyWallDispatchInputForStep(t, s, fourthID, "noship-convergence-3", nil, "verify")
	env.RequestID = "request:noship-convergence-3"
	admitted := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: admittedRaw}, env)
	if admitted.Outcome != OutcomeOK {
		t.Fatalf("converging retry behind the spent budget = %+v", admitted.Error)
	}
	if got := verifyWallChallenges(t, s); got != 0 {
		t.Fatalf("the whole non-progress journey minted %d approval challenges, want 0", got)
	}
	basis := verifyWallRecordedConvergence(t, s, fourthID)
	if basis.Basis != "approach_changed" || basis.SupersededSeq == 0 {
		t.Fatalf("recorded convergence = %+v, want the approach_changed basis", basis)
	}
}

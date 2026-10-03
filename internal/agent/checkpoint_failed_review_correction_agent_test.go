package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// checkpointFailedReviewCorrectionContext is the correction context the
// checkpoint failed-review return (amended CD-0143 D1, CD-0193 D1) projects:
// the failure disposition names the failed review, the bound evidence and
// affected predicates stay bounded, and the dispositioned attempt identity
// binds the escalated retry wall.
func checkpointFailedReviewCorrectionContext() map[string]any {
	return map[string]any{
		"disposition": "failed", "attempt_count": 1, "attempt_limit": 3, "escalated": false,
		"predicate_ids": []string{"predicate:return-route"}, "evidence_refs": []string{"evidence:return-route-verification"},
		"diagnosis":         "the checkpoint review attempt failed and the latest verification verdict is not healthy",
		"strategy":          "return to the repair step and dispatch a fresh attempt",
		"failed_attempt_id": "attempt:work-1:failed-review", "failed_attempt_epoch": 1,
	}
}

// TestFailedReviewCorrectionContextFitsTheClosedSchemas pins the failed-review
// variant against the closed result schema the work pin and the mutation
// envelope project, and against the closed request input the exact operator
// approval authorizes. A failure disposition is the only enum value that can
// represent it: the accepted-success reading of the dispositioned attempt has
// no schema shape.
func TestFailedReviewCorrectionContextFitsTheClosedSchemas(t *testing.T) {
	context := checkpointFailedReviewCorrectionContext()
	raw, err := json.Marshal(context)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePayloadSchema("workflow_correction_context", raw); err != nil {
		t.Fatalf("failed-review correction context rejected by the closed pin schema: %v", err)
	}

	input := map[string]any{
		"work_id": "work-1", "expected_version": 7, "action_id": "request_correction", "idempotency_key": "checkpoint-correction-1",
		"fields": map[string]any{
			"diagnosis":     "the review failed while the delivered subject still mismatches",
			"strategy":      "rebuild the helper and re-verify with a fresh review",
			"predicate_ids": []string{"predicate:return-route"}, "evidence_refs": []string{"evidence:return-route-verification"},
		},
	}
	inputRaw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePayloadSchema("work_transition_action_input", inputRaw); err != nil {
		t.Fatalf("failed-review request_correction input rejected by the closed input schema: %v", err)
	}

	// The closed schema stays closed: the failed-review context cannot grow
	// an undeclared field, and no variant of it claims a schema shape the
	// projection does not own.
	padded := checkpointFailedReviewCorrectionContext()
	padded["accepted_delivery"] = "evidence:return-route-verification"
	paddedRaw, err := json.Marshal(padded)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePayloadSchema("workflow_correction_context", paddedRaw); err == nil {
		t.Fatal("the closed correction schema accepted an undeclared accepted-delivery claim")
	}
}

// TestFailedReviewCheckpointCorrectionBoundaryApprovesExactOperatorOnly
// drives the failed-review checkpoint return through the real mutation
// boundary: the unbound predicate, unbound evidence, and consumed stale
// verdict refusals name their gaps, the admitted shape mints the operator
// challenge, and one exact operator approval returns implementation to
// execution while request_correction mints no worker attempt.
func TestFailedReviewCheckpointCorrectionBoundaryApprovesExactOperatorOnly(t *testing.T) {
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition", "worker_dispatch"})
	ctx := context.Background()
	seedCurrentWorkflowDomainFixture(t, s)
	worktree := t.TempDir()
	grant.Worktree = worktree

	// The instance is pinned to the released implementation version whose
	// acceptance step is the human_checkpoint carrying the review dispatch.
	pinned, err := store.BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	grantActor := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	grantActorRef, err := store.WorkflowActorRef(grantActor)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: "work-1", Definition: pinned, Actor: grantActor, Now: fixedTime()})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		UPDATE workflow_instances SET current_step='execution' WHERE work_id='work-1';
		INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES('work-1',1,'approved checkpoint return objective','internal_sqlite','[]','[]','now',?1,'[]','[]',0,'prototype_internal');
		INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES('work-1',1,'predicate:primary',0,'check','{"kind":"check","check_ref":"check:checkpoint-return","immutable_subject_ref":"commit:checkpoint-return","expected_result":"pass"}');
		DELETE FROM fold_guard`, grantActorRef); err != nil {
		t.Fatalf("seed the checkpoint contract under the fold guard: %v", err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at) VALUES(?,?,?,?,?,?,?,'active',?)`, store.WorktreeSetID("work-1"), "project-1", "claim:checkpoint-return", "checkpoint-return", strings.Repeat("a", 40), worktree, "repo:checkpoint-return", fixedTime().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seed the checkpoint worktree claim: %v", err)
	}
	var claims int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worktree_entries WHERE set_id=? AND state='active'`, store.WorktreeSetID("work-1")).Scan(&claims); err != nil || claims != 1 {
		t.Fatalf("checkpoint worktree claim count=%d err=%v", claims, err)
	}

	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	invoke := func(input map[string]any) Envelope {
		t.Helper()
		response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(input)}, env)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	stepOf := func() string {
		t.Helper()
		var step string
		if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id='work-1'`).Scan(&step); err != nil {
			t.Fatal(err)
		}
		return step
	}

	// The verification evidence the verdict will cite binds on the execution
	// step through the boundary, then the instance parks at the acceptance
	// checkpoint: the checkpoint itself declares no bind_evidence exit.
	if started := invoke(map[string]any{"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "action_id": "start_execution", "idempotency_key": "checkpoint-execution-start"}); started.Outcome != OutcomeOK {
		t.Fatalf("checkpoint fixture start_execution: %+v", started.Error)
	}
	if bound := invoke(map[string]any{"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "action_id": "bind_evidence", "fields": map[string]any{"evidence_kind": "verification", "immutable_subject_ref": "evidence:checkpoint-review"}, "idempotency_key": "checkpoint-evidence-bind"}); bound.Outcome != OutcomeOK {
		t.Fatalf("checkpoint fixture bind_evidence: %+v", bound.Error)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		UPDATE workflow_instances SET current_step='acceptance' WHERE work_id='work-1';
		DELETE FROM fold_guard`); err != nil {
		t.Fatalf("park the instance at the acceptance checkpoint: %v", err)
	}

	// A review-lane attempt dispatches at the acceptance checkpoint, fails,
	// and the mismatch verdict lands under the active contract: the issue
	// 1062 shape behind the amended admission.
	attemptID := "attempt:work-1:failed-review"
	lane := deliveryRecoveryLane(t, "review")
	reviewPacket := bindPacketToRecordedState(t, s, map[string]any{"schema_version": "1.0", "attempt_id": attemptID, "lane_id": lane.ID, "lane_version": lane.Version, "lane_digest": lane.Digest, "work_id": "work-1", "step_id": "acceptance", "inputs": map[string]any{"task": "review the delivered subject at the acceptance checkpoint", "constraints": []string{"preserve the approved contract"}}})
	if dispatched := invoke(map[string]any{"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "action_id": "dispatch_worker", "fields": map[string]any{"attempt_id": attemptID, "worker_packet": reviewPacket}, "idempotency_key": "checkpoint-review-dispatch"}); dispatched.Outcome != OutcomeOK {
		t.Fatalf("checkpoint review dispatch: %+v", dispatched.Error)
	}
	var packetDigest string
	var reviewEpoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.worker_packet_digest') FROM domain_events WHERE subject_id='work-1' AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' ORDER BY seq DESC LIMIT 1`, store.WorkflowActionCompleted).Scan(&packetDigest); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id='work-1' AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' ORDER BY seq DESC LIMIT 1`, store.WorkflowActionStarted).Scan(&reviewEpoch); err != nil {
		t.Fatal(err)
	}
	laneDispatch := store.Event{EventID: "checkpoint-review-dispatch-" + attemptID, Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: retryJSON(store.WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, PacketDigest: packetDigest, ReadbackModel: "openai/gpt-5.6-luna", PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion})}
	laneFailure := store.Event{EventID: "checkpoint-review-failed-" + attemptID, Kind: store.WorkerFailed, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: retryJSON(store.WorkerFailedPayload{AttemptID: attemptID, ReadbackModel: "openai/gpt-5.6-luna", FailureKind: store.WorkerFailureWorkerError, Detail: "review lane failed"})}
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		enriched, err := store.PrepareLaneActorDispatch(ctx, tx, laneDispatch, grant.PrincipalRef, grant.ClientRef)
		if err != nil {
			return err
		}
		if _, err := store.AppendLaneActorDispatchTx(ctx, tx, enriched); err != nil {
			return err
		}
		_, err = store.ApplyOperationTx(ctx, tx, store.Operation{Events: []store.Event{laneFailure}})
		return err
	}); err != nil {
		t.Fatalf("seed the failed review lane report: %v", err)
	}
	reviewer := store.WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/reviewer", SessionRef: "session/work-1-reviewer", ActorClass: store.ActorAgent}
	runVerificationStoreAction(t, s, "record_verdict", map[string]any{
		"contract_version": 1, "predicate_id": "predicate:primary", "verdict_kind": "outcome_mismatch",
		"evaluation_evidence": []string{"evidence:checkpoint-review"}, "incomparable_with_approved": true,
	}, reviewer, nil, "checkpoint-mismatch")
	if recorded := invoke(map[string]any{"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "action_id": "record_worker_failure", "fields": map[string]any{"attempt_id": attemptID, "attempt_epoch": reviewEpoch}, "idempotency_key": "checkpoint-review-record"}); recorded.Outcome != OutcomeOK {
		t.Fatalf("record_worker_failure at the acceptance checkpoint: %+v", recorded.Error)
	}

	// The payload gates fire before the approval wall: a predicate without a
	// current non-ok verdict and unbound evidence are refused by name.
	if unbound := invoke(map[string]any{"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "action_id": "request_correction", "idempotency_key": "checkpoint-unbound-predicate", "fields": map[string]any{"diagnosis": "the review failed", "strategy": "rebuild and re-verify", "predicate_ids": []string{"predicate:elsewhere"}, "evidence_refs": []string{attemptID}}}); unbound.Outcome != OutcomeError || unbound.Error == nil || !strings.Contains(unbound.Error.Message, "without a current non-ok verdict") {
		t.Fatalf("unbound predicate through the boundary = %+v, want the verdict-class payload refusal", unbound.Error)
	}
	if unboundEvidence := invoke(map[string]any{"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "action_id": "request_correction", "idempotency_key": "checkpoint-unbound-evidence", "fields": map[string]any{"diagnosis": "the review failed", "strategy": "rebuild and re-verify", "predicate_ids": []string{"predicate:primary"}, "evidence_refs": []string{"evidence:unbound"}}}); unboundEvidence.Outcome != OutcomeError || unboundEvidence.Error == nil || !strings.Contains(unboundEvidence.Error.Message, "not durably bound") {
		t.Fatalf("unbound evidence through the boundary = %+v, want the evidence-binding refusal", unboundEvidence.Error)
	}

	// The admitted shape mints the operator challenge; request_correction
	// carries the contract version binding the approval consumes.
	correctionInput := map[string]any{"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "action_id": "request_correction", "idempotency_key": "checkpoint-correction-return", "fields": map[string]any{
		"diagnosis": "the checkpoint review attempt failed and the delivered subject still mismatches", "strategy": "return to the execution step and dispatch a fresh attempt",
		"predicate_ids": []string{"predicate:primary"}, "evidence_refs": []string{"evidence:checkpoint-review"},
	}}
	challenge := invoke(correctionInput)
	if challenge.Outcome != OutcomeError || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("unauthenticated checkpoint return = %+v, want approval_required", challenge.Error)
	}
	challengeRef, _ := challenge.Error.Details["approval_ref"].(string)
	if len(challengeRef) != 64 {
		t.Fatalf("checkpoint return challenge carries no approval reference: %+v", challenge.Error.Details)
	}
	if got, _ := challenge.Error.Details["action_id"].(string); got != "request_correction" {
		t.Fatalf("checkpoint return challenge action_id = %v", challenge.Error.Details["action_id"])
	}

	// One exact operator approval admits the return: implementation leaves
	// the checkpoint for execution, and request_correction minted no worker
	// attempt. The failed review stays the only one.
	approvedInput := cloneWithApproval(t, correctionInput, challengeRef)
	approvedRaw, err := json.Marshal(approvedInput)
	if err != nil {
		t.Fatal(err)
	}
	scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion}
	versions := map[string]any{"work": workVersion(t, s, "work-1"), "contract": int64(1)}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, mutationDigest("concord_work_transition", "workflow_action", env, approvedRaw), scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), nonceForChallenge(challengeRef))
	returned := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approvedRaw}, env)
	if returned.Outcome != OutcomeOK {
		t.Fatalf("approved checkpoint return = %+v", returned.Error)
	}
	if got := stepOf(); got != "execution" {
		t.Fatalf("step after the approved checkpoint return = %q, want execution", got)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1'`); got != 1 {
		t.Fatalf("request_correction minted %d worker attempts, want only the failed review", got)
	}

	// The consumed stale verdict cannot reopen the return: the second request
	// names the absent fresh verdict instead of reusing the failed review.
	if stale := invoke(map[string]any{"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "action_id": "request_correction", "idempotency_key": "checkpoint-correction-stale", "fields": correctionInput["fields"]}); stale.Outcome != OutcomeError || stale.Error == nil || !strings.Contains(stale.Error.Message, "current non-ok verification verdict") {
		t.Fatalf("stale-verdict re-request through the boundary = %+v, want the verdict-class refusal", stale.Error)
	}
}

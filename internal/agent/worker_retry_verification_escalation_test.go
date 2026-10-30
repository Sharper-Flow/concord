package agent

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestRepeatedVerificationCorrectionsKeepOrdinaryLaunchAuthority(t *testing.T) {
	runVerificationCorrectionWall(t)
}

func TestWorkerRetryVerificationOracleKeepsOrdinaryLaunchAuthority(t *testing.T) {
	runVerificationCorrectionWall(t)
}

func runVerificationCorrectionWall(t *testing.T) {
	t.Helper()
	for _, tc := range []struct {
		name           string
		finalPredicate []string
	}{
		{"unchanged request predicates", []string{"predicate:primary", "predicate:secondary"}},
		{"shrinking request predicates", []string{"predicate:primary"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition", "worker_dispatch"})
			version, worktree := seedEscalatedVerificationWorkerMutation(t, s, service, grant, tc.finalPredicate)
			grant.Worktree = worktree
			scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
			if err != nil {
				t.Fatal(err)
			}
			env := mutationEnvelope(grant, scopeVersion)
			var seedEpoch int64
			if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(latest_dispatch_start_payload,'$.attempt_epoch') FROM workflow_step_epochs WHERE work_id='work-1' ORDER BY latest_dispatch_start_seq DESC LIMIT 1`).Scan(&seedEpoch); err != nil {
				t.Fatal(err)
			}

			retryID := "attempt:work-1:verification-5"
			input := map[string]any{
				"work_id": "work-1", "expected_version": version, "action_id": "dispatch_worker",
				"idempotency_key": "verification-convergence-1", "fields": map[string]any{"attempt_id": retryID, "worker_packet": retryMutationPacket(t, s, retryID, verifyWallPinCorrection(t, s))},
			}
			raw, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			env.RequestID = "request:verification-convergence-1"
			attempt := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
			// Predicate selection remains context, not a retry credit.
			if attempt.Outcome != agentcontract.OutcomeOK {
				t.Fatalf("verification retry with predicates %v = %+v", tc.finalPredicate, attempt.Error)
			}
			if got := verifyWallDispatchCompletions(t, s, retryID); got != 1 {
				t.Fatalf("verification retry recorded %d dispatch completions, want 1", got)
			}
			if epoch := escalatedDispatchEpoch(t, s); epoch != seedEpoch+1 {
				t.Fatalf("verification retry epoch=%d, want %d", epoch, seedEpoch+1)
			}
			assertDispatchHasNoRetryBudget(t, s, retryID)

			// An unresolved launch blocks another authorization.
			nextID := "attempt:work-1:verification-6"
			blindInput := map[string]any{
				"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "action_id": "dispatch_worker",
				"idempotency_key": "verification-convergence-3", "fields": map[string]any{"attempt_id": nextID, "worker_packet": retryMutationPacket(t, s, nextID, verifyWallPinCorrection(t, s))},
			}
			blindRaw, err := json.Marshal(blindInput)
			if err != nil {
				t.Fatal(err)
			}
			env.RequestID = "request:verification-convergence-3"
			blind := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: blindRaw}, env)
			if blind.Error == nil || blind.Error.Kind != "operation_conflict" || !strings.Contains(blind.Error.Message, "unresolved launch outcome") {
				t.Fatalf("blind re-dispatch = %+v, want unresolved launch conflict", blind.Error)
			}
			if got := verifyWallDispatchCompletions(t, s, nextID); got != 0 {
				t.Fatalf("blind re-dispatch recorded %d dispatch completions, want 0", got)
			}

			// The terminal failure owns the correction source and opens ordinary retry.
			recordWorkflowFailureForPin(t, s, grant, retryID, seedEpoch+1, "escalated-retry-"+strconv.FormatInt(seedEpoch+1, 10))
			rearmed := verifyWallPinCorrection(t, s)
			if rearmed.Disposition != "failed" || rearmed.FailedAttemptID != retryID || rearmed.FailedAttemptEpoch != seedEpoch+1 {
				t.Fatalf("correction after retry failure = %+v, want failed attempt %s at epoch %d", rearmed, retryID, seedEpoch+1)
			}
			afterID := "attempt:work-1:verification-7"
			afterInput := map[string]any{
				"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "action_id": "dispatch_worker",
				"idempotency_key": "verification-convergence-4", "fields": map[string]any{"attempt_id": afterID, "worker_packet": retryMutationPacket(t, s, afterID, rearmed)},
			}
			afterRaw, err := json.Marshal(afterInput)
			if err != nil {
				t.Fatal(err)
			}
			env.RequestID = "request:verification-convergence-4"
			after := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: afterRaw}, env)
			if after.Outcome != agentcontract.OutcomeOK {
				t.Fatalf("dispatch after retry failure = %+v", after.Error)
			}
			if got := verifyWallDispatchCompletions(t, s, afterID); got != 1 {
				t.Fatalf("dispatch after retry failure recorded %d dispatch completions, want 1", got)
			}
			assertDispatchHasNoRetryBudget(t, s, afterID)
		})
	}
}

// seedEscalatedVerificationWorkerMutation records four correction cycles
// under the selected definition. The fourth request selects predicates
// without claiming any finding closure.
func seedEscalatedVerificationWorkerMutation(t *testing.T, s *store.Store, service *Service, grant Authority, finalPredicates []string) (int64, string) {
	t.Helper()
	seedCurrentWorkflowDomainFixture(t, s)
	definition, err := store.BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	owner := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	ctx := agentWorkflowSourceProof(t, context.Background(), s, "work-1")
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: "work-1", Definition: definition, Actor: owner, Now: fixedTime()})
	}); err != nil {
		t.Fatal(err)
	}
	if got := workVersion(t, s, "work-1"); got != 4 {
		t.Fatalf("workflow seed version=%d, want 4", got)
	}
	approvalStep := ""
	for _, step := range definition.Definition.StepGraph.Steps {
		if slices.Contains(step.Actions, "approve_contract") {
			approvalStep = step.ID
			break
		}
	}
	if approvalStep == "" {
		t.Fatal("verification fixture definition has no contract approval step")
	}
	path := t.TempDir()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
		UPDATE workflow_instances SET current_step=? WHERE work_id='work-1';
		DELETE FROM write_guard`, approvalStep); err != nil {
		t.Fatalf("seed verification projections: %v", err)
	}
	fields := workflowContractFieldsFixture()
	fields["premise"] = "approved retry objective"
	fields["outcome_predicates"] = []map[string]any{
		{"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:verification", "expected_result": "pass"}},
		{"predicate_id": "predicate:secondary", "ordinal": 1, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:verification", "expected_result": "pass"}},
	}
	fields["architecture_binding"] = workflowArchitectureBindingForSeededRegistry(t, s)
	runVerificationStoreAction(t, s, "approve_contract", fields, owner, "verification-fixture")
	runVerificationStoreAction(t, s, "record_design", workflowDesignFieldsFixture("verification-fixture"), owner, "verification-fixture-design")
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
		INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at) VALUES(?,?,?,?,?,?,?,'active',?);
		DELETE FROM write_guard`, store.WorktreeSetID("work-1"), "project-1", "claim:verification", "verification", strings.Repeat("a", 40), path, "repo:verification", fixedTime().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seed verification worktree: %v", err)
	}
	var claimed int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worktree_entries WHERE set_id=? AND state='active'`, store.WorktreeSetID("work-1")).Scan(&claimed); err != nil || claimed != 1 {
		t.Fatalf("verification worktree claim count=%d err=%v", claimed, err)
	}
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	grant.Worktree = path
	env := mutationEnvelope(grant, scopeVersion)
	var version int64
	for cycle := int64(1); cycle <= 4; cycle++ {
		attemptID := "attempt:work-1:verification-" + strconv.FormatInt(cycle, 10)
		version = workVersion(t, s, "work-1")
		start := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: json.RawMessage(`{"work_id":"work-1","expected_version":` + strconv.FormatInt(version, 10) + `,"action_id":"start_execution","idempotency_key":"verification-start-` + strconv.FormatInt(cycle, 10) + `"}`)}, env)
		if start.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("seed verification start %d: %+v", cycle, start.Error)
		}
		// Each cycle executes its own recorded job: an accepted job is
		// satisfied and never redispatches (CD-0205).
		recordReadyRetryJob(t, s, service, env, "job:verification-"+strconv.FormatInt(cycle, 10))
		if cycle == 1 {
			// The architecture Review binds the job it permits (CD-0205 D3).
			oAgentReview(t, oAgentFixture(t, s, service, env, "work-1"), "continue", false)
		}
		version = workVersion(t, s, "work-1")
		pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
		if err != nil {
			t.Fatal(err)
		}
		input := map[string]any{
			"work_id": "work-1", "expected_version": version, "action_id": "dispatch_worker",
			"idempotency_key": "verification-dispatch-" + strconv.FormatInt(cycle, 10), "fields": map[string]any{"attempt_id": attemptID, "worker_packet": retryMutationPacket(t, s, attemptID, pin.Correction)},
		}
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		env.RequestID = "request:verification-dispatch-" + strconv.FormatInt(cycle, 10)
		dispatch := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
		if dispatch.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("seed verification dispatch %d: %+v", cycle, dispatch.Error)
		}
		var attemptEpoch int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(latest_dispatch_start_payload,'$.attempt_epoch') FROM workflow_step_epochs WHERE work_id='work-1' ORDER BY latest_dispatch_start_seq DESC LIMIT 1`).Scan(&attemptEpoch); err != nil {
			t.Fatal(err)
		}
		applyVerificationWorkerDispatchAndCompletion(t, s, grant, attemptID, "verification")
		version = workVersion(t, s, "work-1")
		accept := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "accept_worker_result", "fields": map[string]any{"attempt_id": attemptID, "attempt_epoch": attemptEpoch}, "idempotency_key": "verification-accept-" + strconv.FormatInt(cycle, 10)})}, env)
		if accept.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("seed verification accept %d: %+v", cycle, accept.Error)
		}
		// Correction authority is an accepted current Review whose disposition
		// is correction_required (CD-0242 D8); the delivered candidate stays in
		// execution until that Review routes it back.
		acceptVerificationCorrectionReview(t, s, service, env, grant, cycle)
		requestPredicates := []string{"predicate:primary", "predicate:secondary"}
		if cycle == 4 {
			requestPredicates = finalPredicates
		}
		runVerificationStoreAction(t, s, "request_correction", map[string]any{
			"diagnosis":     "the delivered subject still misses the approved predicates",
			"strategy":      "repeat the implementation external effect",
			"predicate_ids": requestPredicates,
			"evidence_refs": []string{attemptID},
		}, owner, "correction-"+strconv.FormatInt(cycle, 10))
		pin, err = store.ReadWorkPin(context.Background(), s, "work-1")
		if err != nil {
			t.Fatal(err)
		}
		if pin.Correction == nil || pin.Correction.Disposition != "verification" || pin.Correction.SourceEventID == "" || pin.Correction.SourceEventSeq == 0 {
			t.Fatalf("verification correction after cycle %d = %#v", cycle, pin.Correction)
		}
	}
	// The corrective retry executes a freshly recorded job.
	recordReadyRetryJob(t, s, service, env, "job:verification-correction")
	pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	if pin.Correction == nil || pin.Correction.Disposition != "verification" || strings.Join(pin.Correction.PredicateIDs, ",") != strings.Join(finalPredicates, ",") {
		t.Fatalf("verification correction after the fourth request = %#v, want predicates %v", pin.Correction, finalPredicates)
	}
	return workVersion(t, s, "work-1"), path
}

// acceptVerificationCorrectionReview records a Review job, dispatches the
// review lane on the current candidate, completes it with a
// correction_required disposition, and accepts that Review.
func acceptVerificationCorrectionReview(t *testing.T, s *store.Store, service *Service, env CallEnvelope, grant Authority, cycle int64) {
	t.Helper()
	suffix := strconv.FormatInt(cycle, 10)
	recordReadyRetryJob(t, s, service, env, "job:verification-review-"+suffix)
	pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	if pin.Review == nil || pin.Review.Candidate == nil || pin.Review.Dependencies == nil {
		t.Fatalf("cycle %d Review candidate or dependencies unavailable: %+v", cycle, pin.Review)
	}
	candidate := *pin.Review.Candidate
	var lane store.LaneDefinition
	for _, registered := range store.BuiltinLaneDefinitions() {
		if registered.ID == "review" {
			lane = registered
		}
	}
	attemptID := "attempt:work-1:verification-review-" + suffix
	packet := bindPacketToRecordedState(t, s, map[string]any{"schema_version": store.WorkerPacketSchemaVersion, "attempt_id": attemptID, "lane_id": lane.ID, "lane_digest": lane.Digest, "work_id": "work-1", "step_id": pin.Step, "inputs": map[string]any{}})
	env.RequestID = "request:verification-review-dispatch-" + suffix
	dispatch := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "action_id": "dispatch_worker", "idempotency_key": "verification-review-dispatch-" + suffix, "fields": map[string]any{"attempt_id": attemptID, "worker_packet": packet}})}, env)
	if dispatch.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("seed verification Review dispatch %d: %+v", cycle, dispatch.Error)
	}
	var epoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(latest_dispatch_start_payload,'$.attempt_epoch') FROM workflow_step_epochs WHERE work_id='work-1' ORDER BY latest_dispatch_start_seq DESC LIMIT 1`).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	review := &store.WorkerReviewBlock{Disposition: "correction_required", InputCandidate: candidate, OutputCandidate: candidate, Dependencies: *pin.Review.Dependencies,
		Assessments: []store.WorkerReviewAssessment{}, Findings: []store.WorkerReviewFinding{}, RemainingObligations: []store.WorkerReviewObligation{}}
	completeVerificationAttempt(t, s, grant, lane, attemptID, "verification-review", review)
	env.RequestID = "request:verification-review-accept-" + suffix
	accept := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "action_id": "accept_worker_result", "fields": map[string]any{"attempt_id": attemptID, "attempt_epoch": epoch}, "idempotency_key": "verification-review-accept-" + suffix})}, env)
	if accept.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("seed verification Review accept %d: %+v", cycle, accept.Error)
	}
}

// applyVerificationWorkerDispatchAndCompletion records the lane dispatch and
// the completed report for one implementation attempt before the boundary
// accepts the result.
func applyVerificationWorkerDispatchAndCompletion(t *testing.T, s *store.Store, grant Authority, attemptID, suffix string) {
	t.Helper()
	completeVerificationAttempt(t, s, grant, retryLane(t), attemptID, suffix, nil)
}

// completeVerificationAttempt appends the host dispatch for one authorized
// attempt, commits an implementation delivery inside the job's path scope,
// and records the completion through the terminal preparation, so the core
// observes the delivered delta from the live claimed worktree.
func completeVerificationAttempt(t *testing.T, s *store.Store, grant Authority, lane store.LaneDefinition, attemptID, suffix string, review *store.WorkerReviewBlock) {
	t.Helper()
	ctx := context.Background()
	job := authorizedWorkerJob(t, s, attemptID)
	evidence := make([]store.WorkerReportEvidence, 0, len(lane.EvidenceObligations))
	for _, obligation := range lane.EvidenceObligations {
		if obligation == "severity" && review != nil {
			continue
		}
		evidence = append(evidence, store.WorkerReportEvidence{Obligation: obligation, Detail: "synthetic reported evidence for " + obligation})
	}
	dispatch := store.Event{EventID: suffix + "-dispatch-" + attemptID, Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion(store.WorkerDispatched), Payload: retryJSON(store.WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, PacketDigest: "sha256:" + strings.Repeat("c", 64), ReadbackModel: "openai/gpt-5.6-luna", PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion, WorkerJob: job})}
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		enriched, err := store.PrepareLaneActorDispatch(ctx, tx, dispatch, grant.PrincipalRef, grant.ClientRef)
		if err != nil {
			return err
		}
		_, err = store.AppendLaneActorDispatchTx(ctx, tx, enriched)
		return err
	}); err != nil {
		t.Fatalf("seed verification dispatch for %s: %v", attemptID, err)
	}
	if review == nil {
		deliverAgentFixtureChange(t, s, "work-1", attemptID)
	}
	completion := store.Event{EventID: suffix + "-completed-" + attemptID, Kind: store.WorkerCompleted, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion(store.WorkerCompleted), Payload: retryJSON(store.WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: "openai/gpt-5.6-luna", ReportSchemaVersion: store.WorkerReportSchemaVersion, WorkerDirectory: grant.Worktree, WorkerJob: job, EvidenceOrigin: store.WorkerEvidenceReported, Evidence: evidence, Review: review})}
	prepared, err := store.PrepareWorkerTerminalDelivery(ctx, s, store.PrepareWorkerTerminalDeliveryRequest{WorkID: "work-1", AttemptID: attemptID, EventID: completion.EventID})
	if err != nil {
		t.Fatalf("prepare verification completion for %s: %v", attemptID, err)
	}
	ctx = store.WithWorkerTerminalPreparation(ctx, prepared)
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		_, err := store.ApplyWorkerEvidenceOperationTx(ctx, tx, completion)
		return err
	}); err != nil {
		t.Fatalf("seed verification completion for %s: %v", attemptID, err)
	}
}

// runVerificationStoreAction applies one store-level workflow action so the
// fixture can record verdict and correction records without an operator
// question on the mutation boundary.
func runVerificationStoreAction(t *testing.T, s *store.Store, action string, fields map[string]any, actor store.WorkflowActor, suffix string) {
	t.Helper()
	version := workVersion(t, s, "work-1")
	operationID := action + "-verification-" + suffix + "-" + strconv.FormatInt(version, 10)
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	request := store.WorkflowActionExecutionRequest{
		WorkID: "work-1", ExpectedVersion: version, ActionID: action, Payload: raw, Actor: actor,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("f", 64) + operationID, IdempotencyIdentity: operationID, OperationID: operationID,
		PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID, RequestID: "request:" + operationID,
		ContractDigest: agentcontract.ManifestDigest, Now: fixedTime(),
	}
	ctx := context.Background()
	if action == "approve_contract" || action == "supersede_contract" || action == "complete" {
		ctx, err = s.EstablishKnowledgeSourceSetProof(ctx, request.WorkID)
		if err != nil {
			t.Fatalf("prepare %s for %s: %v", action, suffix, err)
		}
	}
	if store.WorkflowActionRequiresSubjectFreshness(action) {
		preparation, prepErr := store.PrepareWorkflowAcceptanceFreshness(ctx, s, store.PrepareWorkerDispatchRequest{WorkID: request.WorkID})
		if prepErr != nil {
			t.Fatalf("seed %s for %s: %v", action, suffix, prepErr)
		}
		ctx = store.WithWorkerDispatchPreparation(ctx, preparation)
	}
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		_, err := store.ApplyWorkflowActionTx(ctx, tx, store.BuiltinWorkflowRegistry(), request)
		return err
	}); err != nil {
		t.Fatalf("seed %s for %s: %v", action, suffix, err)
	}
}

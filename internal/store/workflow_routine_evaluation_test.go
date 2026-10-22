package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The evaluation authority owner keeps independent evaluation and completion
// recording separate. Under consequence_v1 there is no operator substitution
// for verdicts, completion, or repair requests: the independent evaluator
// invokes record_verdict under its own authenticated identity, the coordinator
// completes without an operator actor, and a correction request is a repair
// request, not an evaluation. Every retained gate — executor refusal, worker
// refusal, evidence binding, model distinctness, operator-typed premise
// confirmation — must hold. New-policy owners are exercised through the
// synthetic definition value (fixture shape plus the discriminator); no
// unreleased builtin is registered, so builtin-only end-to-end pins wait for
// the promotion slice.

// routinePolicyDefinition copies a registered fixture definition and declares
// consequence_v1 on the copy. The registry keeps the legacy version; only the
// pure and transactional owners under test see the declared policy.
func routinePolicyDefinition(t *testing.T) WorkflowDefinition {
	t.Helper()
	registered, ok := BuiltinWorkflowRegistry().Lookup(workflowFixtureRef, 2)
	if !ok {
		t.Fatalf("workflow fixture v%d is not registered", 2)
	}
	definition := registered.Definition
	definition.ApprovalPolicy = WorkflowApprovalPolicyConsequenceV1
	return definition
}

// routineDefinitionOnPin copies the instance's own pinned definition and
// declares consequence_v1 on the copy, so admission derivations see a shape
// that matches the seeded instance.
func routineDefinitionOnPin(t *testing.T, s *Store, workID string) WorkflowDefinition {
	t.Helper()
	var ref string
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_version FROM workflow_instances WHERE work_id=?`, workID).Scan(&ref, &version); err != nil {
		t.Fatal(err)
	}
	registered, ok := BuiltinWorkflowRegistry().Lookup(ref, version)
	if !ok {
		t.Fatalf("pinned definition %s v%d is not registered", ref, version)
	}
	definition := registered.Definition
	definition.ApprovalPolicy = WorkflowApprovalPolicyConsequenceV1
	return definition
}

// seedRoutineActors records an independent reviewer, an operator, and a
// non-executing coordinator on a delivered seed, and confirms the premise with
// the operator identity the legacy clause 5 requirement keeps demanding.
func seedRoutineActors(t *testing.T, s *Store, workID string) (reviewer WorkflowActor, operator WorkflowActor, coordinator WorkflowActor) {
	t.Helper()
	ctx := context.Background()
	reviewer = WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/independent-reviewer", SessionRef: "session/" + workID + "-reviewer", ActorClass: ActorAgent}
	operator = WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/operator", SessionRef: "session/" + workID + "-operator", ActorClass: ActorOperator}
	coordinator = WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/coordinator", SessionRef: "session/" + workID + "-coordinator", ActorClass: ActorAgent}
	refs := make([]string, 3)
	for index, actor := range []WorkflowActor{reviewer, operator, coordinator} {
		ref, err := WorkflowActorRef(actor)
		if err != nil {
			t.Fatal(err)
		}
		refs[index] = ref
	}
	version := readWorkVersion(t, s, workID)
	setup := []Event{
		workflowEventWithActor("routine-reviewer-"+workID, WorkflowActorRecorded, workID, refs[0], map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "actor_ref": refs[0], "principal_ref": reviewer.PrincipalRef, "client_ref": reviewer.ClientRef, "agent_ref": reviewer.AgentRef, "session_ref": reviewer.SessionRef, "actor_class": "agent"}),
		workflowEventWithActor("routine-operator-"+workID, WorkflowActorRecorded, workID, refs[1], map[string]any{"work_id": workID, "expected_version": version + 1, "resulting_version": version + 2, "actor_ref": refs[1], "principal_ref": operator.PrincipalRef, "client_ref": operator.ClientRef, "agent_ref": operator.AgentRef, "session_ref": operator.SessionRef, "actor_class": "operator"}),
		workflowEventWithActor("routine-coordinator-"+workID, WorkflowActorRecorded, workID, refs[2], map[string]any{"work_id": workID, "expected_version": version + 2, "resulting_version": version + 3, "actor_ref": refs[2], "principal_ref": coordinator.PrincipalRef, "client_ref": coordinator.ClientRef, "agent_ref": coordinator.AgentRef, "session_ref": coordinator.SessionRef, "actor_class": "agent"}),
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: setup, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO workflow_premise_confirmations(work_id,contract_version,confirmed_by,confirmed_at) VALUES(?,1,?,'2026-01-01T00:00:00Z'); DELETE FROM fold_guard`, workID, refs[1]); err != nil {
		t.Fatal(err)
	}
	return reviewer, operator, coordinator
}

// foldRoutineVerdict folds one ok verdict from the independent evaluator so
// the completion clause 4 gate has its witness.
func foldRoutineVerdict(t *testing.T, s *Store, workID string, reviewer WorkflowActor) {
	t.Helper()
	reviewerRef, err := WorkflowActorRef(reviewer)
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, workID)
	event := workflowEventWithActor("routine-verdict-"+workID, WorkflowVerdictRecorded, workID, reviewerRef, map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "contract_version": 1, "predicate_id": "predicate:primary", "verdict_kind": "ok", "verdict_actor_ref": reviewerRef, "evaluation_evidence": []string{"evidence:operator-verdict"}, "incomparable_with_approved": false})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("fold independent verdict: %v", err)
	}
}

func TestRoutineEvaluation(t *testing.T) {
	t.Parallel()

	t.Run("legacy operator substitution still works", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-legacy-verdict"
		s, owner := seedDeliveredItemAtAcceptance(t, workID)
		operator := operatorVerdictActor(t, workID)
		if err := runOperatorVerdictForPredicate(t, s, workID, owner, operator, "predicate:primary", "evidence:operator-verdict"); err != nil {
			t.Fatalf("legacy operator-substituted verdict refused: %v", err)
		}
	})

	t.Run("legacy correction keeps the operator approval identity", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-legacy-correction"
		fixture := prepareVerdictCorrectionFixture(t, workID)
		g := routineGuardContext(t, fixture.store, workID, "request_correction", nil, false)
		err := guardOperatorPremiseActor(g)
		var failure *Failure
		if err == nil || !failureAs(err, &failure) || failure.Kind != KindApprovalRequired || !strings.Contains(failure.Detail, "verified operator approval identity") {
			t.Fatalf("legacy correction without operator identity returned %v, want the approval-identity refusal", err)
		}
	})

	t.Run("new policy refuses operator-substituted verdicts", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-new-verdict-substitution"
		s, _ := seedDeliveredItemAtAcceptance(t, workID)
		operator := operatorVerdictActor(t, workID)
		version := readWorkVersion(t, s, workID)
		tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		fields := map[string]json.RawMessage{"predicate_id": json.RawMessage(`"predicate:primary"`), "verdict_kind": json.RawMessage(`"ok"`), "evaluation_evidence": json.RawMessage(`["evidence:operator-verdict"]`)}
		_, err = workflowRecordVerdictEvents(context.Background(), tx, WorkflowActionExecutionRequest{WorkID: workID, OperatorActor: &operator, OperationID: "routine-verdict-" + workID}, "actor:owner", fields, "routine-verdict-"+workID, version)
		var failure *Failure
		if err == nil || !failureAs(err, &failure) || failure.Kind != KindUnauthorized || !strings.Contains(failure.Detail, "consequence_v1 takes no operator substitution") {
			t.Fatalf("consequence_v1 verdict with an operator actor returned %v, want the no-substitution refusal", err)
		}
	})

	t.Run("new policy refuses operator-substituted completion", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-new-completion-substitution"
		s, _ := seedDeliveredItemAtAcceptance(t, workID)
		reviewer, operator, coordinator := seedRoutineActors(t, s, workID)
		foldRoutineVerdict(t, s, workID, reviewer)
		setWorkflowStepForOperatorVerdictTest(t, s, workID, "release")
		version := readWorkVersion(t, s, workID)
		tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		execution := WorkflowActionExecutionRequest{
			WorkID: workID, ExpectedVersion: version, ActionID: "complete", Payload: json.RawMessage(`{"impact_verdict":"non-breaking"}`), Actor: coordinator, OperatorActor: &operator,
			OperationID: "routine-complete-" + workID, Now: time.Unix(11, 0).UTC(),
		}
		_, err = applyCompleteWorkflowActionTx(context.Background(), tx, newFoldScope(tx), BuiltinWorkflowRegistry(), RegisteredDefinition{Definition: routinePolicyDefinition(t)}, execution, ownerRefForTest(t, coordinator), json.RawMessage(`{"impact_verdict":"non-breaking"}`), nil)
		var failure *Failure
		if err == nil || !failureAs(err, &failure) || failure.Kind != KindUnauthorized || !strings.Contains(failure.Detail, "consequence_v1 takes no operator substitution") {
			t.Fatalf("consequence_v1 completion with an operator actor returned %v, want the no-substitution refusal", err)
		}
	})

	t.Run("new policy admits coordinator completion with an independent verdict", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-new-coordinator-completion"
		s, _ := seedDeliveredItemAtAcceptance(t, workID)
		reviewer, _, coordinator := seedRoutineActors(t, s, workID)
		foldRoutineVerdict(t, s, workID, reviewer)
		setWorkflowStepForOperatorVerdictTest(t, s, workID, "release")
		version := readWorkVersion(t, s, workID)
		tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		execution := WorkflowActionExecutionRequest{
			WorkID: workID, ExpectedVersion: version, ActionID: "complete", Payload: json.RawMessage(`{"impact_verdict":"non-breaking"}`), Actor: coordinator,
			OperationID: "routine-complete-" + workID, Now: time.Unix(11, 0).UTC(),
		}
		result, err := applyCompleteWorkflowActionTx(context.Background(), tx, newFoldScope(tx), BuiltinWorkflowRegistry(), RegisteredDefinition{Definition: routinePolicyDefinition(t)}, execution, ownerRefForTest(t, coordinator), json.RawMessage(`{"impact_verdict":"non-breaking"}`), nil)
		if err != nil {
			t.Fatalf("coordinator completion with an independent verdict refused: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if len(result.EventIDs) == 0 {
			t.Fatal("completion recorded no events")
		}
		var state string
		if err := s.DatabaseForTesting().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != "completed" {
			t.Fatalf("instance state after coordinator completion = %q, want completed", state)
		}
	})

	t.Run("self-verdict refuses without a challenge", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-new-self-verdict"
		s, owner := seedDeliveredItemAtAcceptance(t, workID)
		ownerRef := ownerRefForTest(t, owner)
		version := readWorkVersion(t, s, workID)
		tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		fields := map[string]json.RawMessage{"verdict_actor_ref": json.RawMessage(`"` + ownerRef + `"`), "predicate_id": json.RawMessage(`"predicate:primary"`), "verdict_kind": json.RawMessage(`"ok"`), "evaluation_evidence": json.RawMessage(`["evidence:operator-verdict"]`)}
		_, err = workflowRecordVerdictEvents(context.Background(), tx, WorkflowActionExecutionRequest{WorkID: workID, OperationID: "routine-self-" + workID}, ownerRef, fields, "routine-self-"+workID, version)
		var failure *Failure
		if err == nil || !failureAs(err, &failure) || failure.Kind != KindUnauthorized || !strings.Contains(failure.Detail, "executing actor cannot evaluate its own delivery") {
			t.Fatalf("self-verdict returned %v, want the executor refusal without an approval challenge", err)
		}
	})

	t.Run("independent non-worker evaluator succeeds under new policy", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-new-independent-verdict"
		s, _ := seedDeliveredItemAtAcceptance(t, workID)
		reviewer, _, _ := seedRoutineActors(t, s, workID)
		reviewerRef := ownerRefForTest(t, reviewer)
		version := readWorkVersion(t, s, workID)
		tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		fields := map[string]json.RawMessage{"predicate_id": json.RawMessage(`"predicate:primary"`), "verdict_kind": json.RawMessage(`"ok"`), "evaluation_evidence": json.RawMessage(`["evidence:operator-verdict"]`)}
		events, err := workflowRecordVerdictEvents(context.Background(), tx, WorkflowActionExecutionRequest{WorkID: workID, OperationID: "routine-independent-" + workID}, reviewerRef, fields, "routine-independent-"+workID, version)
		if err != nil {
			t.Fatalf("independent evaluator verdict refused: %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("independent evaluator verdict produced %d events, want 1", len(events))
		}
		var payload struct {
			VerdictActorRef string `json:"verdict_actor_ref"`
		}
		if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.VerdictActorRef != reviewerRef {
			t.Fatalf("verdict actor = %q, want the invoking independent evaluator", payload.VerdictActorRef)
		}
	})

	t.Run("worker authority refuses evaluation", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-new-worker-verdict"
		s, _ := seedDeliveredItemAtAcceptance(t, workID)
		reviewer, _, _ := seedRoutineActors(t, s, workID)
		reviewerRef := ownerRefForTest(t, reviewer)
		lane := BuiltinLaneDefinitions()[0]
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{{
			EventID: "routine-dispatch-" + workID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID,
			Actor: reviewerRef, OccurredAt: time.Unix(9, 30).UTC(), PayloadVersion: 2,
			Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: "attempt:" + workID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion, LaneActorRef: reviewerRef}),
		}}}); err != nil {
			t.Fatalf("seed worker dispatch: %v", err)
		}
		var authority WorkflowEvaluationAuthority
		err := s.readSnapshot(context.Background(), func(q queryer) error {
			var innerErr error
			authority, innerErr = workflowEvaluationAuthority(context.Background(), q, workID, reviewerRef)
			return innerErr
		})
		if err != nil {
			t.Fatal(err)
		}
		if authority != WorkflowWorkerEvaluator {
			t.Fatalf("dispatched reviewer authority = %q, want worker", authority)
		}
		version := readWorkVersion(t, s, workID)
		tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		fields := map[string]json.RawMessage{"predicate_id": json.RawMessage(`"predicate:primary"`), "verdict_kind": json.RawMessage(`"ok"`), "evaluation_evidence": json.RawMessage(`["evidence:operator-verdict"]`)}
		_, err = workflowRecordVerdictEvents(context.Background(), tx, WorkflowActionExecutionRequest{WorkID: workID, OperationID: "routine-worker-" + workID}, reviewerRef, fields, "routine-worker-"+workID, version)
		var failure *Failure
		if err == nil || !failureAs(err, &failure) || failure.Kind != KindUnauthorized {
			t.Fatalf("worker-authority verdict returned %v, want the unauthorized refusal", err)
		}
	})

	t.Run("new policy admits repair request without operator identity", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-new-correction"
		fixture := prepareVerdictCorrectionFixture(t, workID)
		g := routineGuardContext(t, fixture.store, workID, "request_correction", nil, true)
		if err := guardOperatorPremiseActor(g); err != nil {
			t.Fatalf("consequence_v1 repair request without an operator identity refused: %v", err)
		}
	})

	t.Run("new policy rejects supplied operator identity on substitution actions", func(t *testing.T) {
		t.Parallel()
		for _, actionID := range []string{"record_verdict", "complete", "request_correction"} {
			const workID = "routine-new-operator-claim"
			fixture := prepareVerdictCorrectionFixture(t, workID)
			g := routineGuardContext(t, fixture.store, workID, actionID, &fixture.operator, true)
			err := guardOperatorPremiseActor(g)
			var failure *Failure
			if err == nil || !failureAs(err, &failure) || failure.Kind != KindUnauthorized || !strings.Contains(failure.Detail, "consequence_v1 takes no operator substitution") {
				t.Fatalf("consequence_v1 %s with a supplied operator identity returned %v, want the no-substitution refusal", actionID, err)
			}
		}
	})

	t.Run("premise confirmation keeps its operator identity under new policy", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-new-premise"
		fixture := prepareVerdictCorrectionFixture(t, workID)
		g := routineGuardContext(t, fixture.store, workID, "confirm_premise", &fixture.operator, true)
		if err := guardOperatorPremiseActor(g); err != nil {
			t.Fatalf("consequence_v1 premise confirmation with the verified operator identity refused: %v", err)
		}
	})

	t.Run("model mismatch refuses at completion distinctness", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-new-model-mismatch"
		s, _ := seedDeliveredItemAtAcceptance(t, workID)
		reviewer, _, _ := seedRoutineActors(t, s, workID)
		reviewerRef := ownerRefForTest(t, reviewer)
		var executionModel string
		if err := s.DatabaseForTesting().QueryRow(`SELECT execution_model FROM workflow_instances WHERE work_id=?`, workID).Scan(&executionModel); err != nil {
			t.Fatal(err)
		}
		tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		err = workflowCompletionActorDistinct(context.Background(), tx, workID, reviewerRef, executionModel, true)
		var failure *Failure
		if err == nil || !failureAs(err, &failure) || failure.Kind != KindUnauthorized || !strings.Contains(failure.Detail, "same readback model identity") {
			t.Fatalf("model-matched verdict distinctness returned %v, want the readback-model refusal", err)
		}
	})

	t.Run("unbound evidence refuses under new policy", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-new-unbound-evidence"
		s, _ := seedDeliveredItemAtAcceptance(t, workID)
		reviewer, _, _ := seedRoutineActors(t, s, workID)
		reviewerRef := ownerRefForTest(t, reviewer)
		version := readWorkVersion(t, s, workID)
		tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		fields := map[string]json.RawMessage{"predicate_id": json.RawMessage(`"predicate:primary"`), "verdict_kind": json.RawMessage(`"ok"`), "evaluation_evidence": json.RawMessage(`["evidence:not-bound"]`)}
		_, err = workflowRecordVerdictEvents(context.Background(), tx, WorkflowActionExecutionRequest{WorkID: workID, OperationID: "routine-unbound-" + workID}, reviewerRef, fields, "routine-unbound-"+workID, version)
		var failure *Failure
		if err == nil || !failureAs(err, &failure) || failure.Kind != KindMissingEvidence {
			t.Fatalf("unbound evaluation evidence returned %v, want the missing-evidence refusal", err)
		}
	})
	t.Run("seam admits the executing coordinator under new policy", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-new-executing-completion"
		s, owner := seedDeliveredItemAtAcceptance(t, workID)
		reviewer, _, _ := seedRoutineActors(t, s, workID)
		foldRoutineVerdict(t, s, workID, reviewer)
		coordinatorRef := ownerRefForTest(t, owner)
		tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := workflowCompletionCallerAuthority(context.Background(), tx, routinePolicyDefinition(t), workID, coordinatorRef); err != nil {
			t.Fatalf("consequence_v1 completion authority refused the initiating coordinator: %v", err)
		}
	})

	t.Run("seam refuses worker completion authority under new policy", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-new-worker-completion"
		s, _ := seedDeliveredItemAtAcceptance(t, workID)
		_, _, coordinator := seedRoutineActors(t, s, workID)
		coordinatorRef := ownerRefForTest(t, coordinator)
		lane := BuiltinLaneDefinitions()[0]
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{{
			EventID: "routine-completion-dispatch-" + workID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID,
			Actor: coordinatorRef, OccurredAt: time.Unix(9, 45).UTC(), PayloadVersion: 2,
			Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: "attempt:" + workID + "-completion", LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion, LaneActorRef: coordinatorRef}),
		}}}); err != nil {
			t.Fatalf("seed completion-worker dispatch: %v", err)
		}
		tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		err = workflowCompletionCallerAuthority(context.Background(), tx, routinePolicyDefinition(t), workID, coordinatorRef)
		var failure *Failure
		if err == nil || !failureAs(err, &failure) || failure.Kind != KindUnauthorized || !strings.Contains(failure.Detail, "worker actors cannot record workflow completion") {
			t.Fatalf("worker-authority completion returned %v, want the worker refusal", err)
		}
	})

	t.Run("seam keeps the legacy executor refusal and non-executor admission", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-seam-legacy-completion"
		s, owner := seedDeliveredItemAtAcceptance(t, workID)
		reviewer, _, coordinator := seedRoutineActors(t, s, workID)
		foldRoutineVerdict(t, s, workID, reviewer)
		ownerRef := ownerRefForTest(t, owner)
		coordinatorRef := ownerRefForTest(t, coordinator)
		tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		legacy := workflowFixtureDefinition(t, 2).Definition
		err = workflowCompletionCallerAuthority(context.Background(), tx, legacy, workID, ownerRef)
		var failure *Failure
		if err == nil || !failureAs(err, &failure) || failure.Kind != KindUnauthorized || !strings.Contains(failure.Detail, "executing actor cannot author its own verdict") {
			t.Fatalf("legacy executor completion authority returned %v, want the retained distinctness refusal", err)
		}
		if err := workflowCompletionCallerAuthority(context.Background(), tx, legacy, workID, coordinatorRef); err != nil {
			t.Fatalf("legacy non-executing coordinator completion authority refused: %v", err)
		}
	})

	t.Run("new policy keeps verdict gates for coordinator completion", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-new-completion-gates"
		s, _ := seedDeliveredItemAtAcceptance(t, workID)
		_, _, coordinator := seedRoutineActors(t, s, workID)
		setWorkflowStepForOperatorVerdictTest(t, s, workID, "release")
		version := readWorkVersion(t, s, workID)
		tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		execution := WorkflowActionExecutionRequest{
			WorkID: workID, ExpectedVersion: version, ActionID: "complete", Payload: json.RawMessage(`{"impact_verdict":"non-breaking"}`), Actor: coordinator,
			OperationID: "routine-complete-" + workID, Now: time.Unix(11, 0).UTC(),
		}
		_, err = applyCompleteWorkflowActionTx(context.Background(), tx, newFoldScope(tx), BuiltinWorkflowRegistry(), RegisteredDefinition{Definition: workflowFixtureDefinition(t, 2).Definition}, execution, ownerRefForTest(t, coordinator), json.RawMessage(`{"impact_verdict":"non-breaking"}`), nil)
		var failure *Failure
		if err == nil || !failureAs(err, &failure) || failure.Kind != KindMissingEvidence || !strings.Contains(failure.Detail, "verdict is missing") {
			t.Fatalf("completion without a verdict returned %v, want the missing-verdict clause refusal", err)
		}
	})
}

// routineGuardContext builds a direct guard invocation over one seeded store:
// the entry carries the instance's pinned definition with consequence_v1
// declared when requested, and the current step comes from the instance.
func routineGuardContext(t *testing.T, s *Store, workID, actionID string, operator *WorkflowActor, newPolicy bool) *workflowActionGuardContext {
	t.Helper()
	ctx := context.Background()
	var ref string
	var version int64
	var currentStep string
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_version,current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&ref, &version, &currentStep); err != nil {
		t.Fatal(err)
	}
	registered, ok := BuiltinWorkflowRegistry().Lookup(ref, version)
	if !ok {
		t.Fatalf("pinned definition %s v%d is not registered", ref, version)
	}
	definition := registered.Definition
	if newPolicy {
		definition.ApprovalPolicy = WorkflowApprovalPolicyConsequenceV1
	}
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The guard reads one read-mostly snapshot; rolling the transaction back
	// releases the pool's sole connection for the next fixture.
	t.Cleanup(func() { _ = tx.Rollback() })
	return &workflowActionGuardContext{
		ctx: ctx, tx: tx, currentStep: currentStep,
		entry:    RegisteredDefinition{Definition: definition},
		request:  WorkflowActionExecutionRequest{WorkID: workID, ActionID: actionID, OperatorActor: operator},
		actorRef: ownerRefForTest(t, owner),
	}
}

func ownerRefForTest(t *testing.T, actor WorkflowActor) string {
	t.Helper()
	ref, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

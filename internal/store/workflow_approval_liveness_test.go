package store

// TestApprovalPolicyLiveness is the model/fold conformance witness for the
// approval classes: the abstract admission model and the real fold agree on
// the confirmation class of every seeded route — engineering work holds no
// human-question exit, decision work keeps the exact choice/digest/operator
// chain, a class revision flips the folded confirmation class, a below-wall
// retry consumes the policy instead of an approval, and a contract whose
// declared decision fact is absent or malformed refuses as a typed failure. A
// human-question exit is not an engineering exit.
//
// The comparisons run through the shared conformance machinery
// (conformanceCheckpoint): the model state carries the explicit engineering
// classification dimension and the lift applies it, so a fold that disagrees
// with the model on the projected question fails the comparison. The work
// pin's own question phases project the operator question and its digest.
//
// The class revision authors the successor fact as a versioned
// contract_superseded event folded through the registered fold — the decision
// fact has no public emitter yet. The legacy enumeration in
// workflow_admission_liveness_test.go is unchanged: its states carry no
// classification dimension, so every registered version keeps its prior
// model.

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestApprovalPolicyLiveness(t *testing.T) {
	t.Parallel()

	t.Run("engineering folds no human-question exit beside its real confirm exit", func(t *testing.T) {
		t.Parallel()
		const workID = "approval-liveness-engineering"
		s, registry, definition, _, actors := seedApprovalItemAtAcceptance(t, workID, []string{})
		if err := recordApprovalVerdict(t, s, registry, workID, actors.reviewer); err != nil {
			t.Fatalf("the independent evaluator's verdict refused: %v", err)
		}
		// The model lifts engineering to no pending operator question; the
		// real fold must agree on every compared dimension.
		conformanceCheckpoint(t, s, registry, workID, definition, admissionModelState{step: "acceptance", contracts: 1, verdict: "ok", artifact: true, engineering: true}, "engineering at acceptance")
		state := foldedApprovalAdmission(t, s, registry, definition, workID)
		decision := workflowAdmit(definition, state, "confirm_premise")
		if !decision.Admitted {
			t.Fatalf("engineering confirm_premise is no exit at all: %v", decision.Failure)
		}
		if decision.OperatorQuestionClosed {
			t.Fatal("engineering confirm_premise folded the closed-question wall; the exit must not be a human question")
		}
		// The work pin projects no operator question and no withheld reason
		// for engineering work.
		question, withheld := approvalOperatorQuestionProjection(t, s, registry, definition, registeredDigest(t, registry, definition), workID)
		if question != nil || withheld != nil {
			t.Fatalf("engineering work projects question %+v withheld %+v, want neither", question, withheld)
		}
	})

	t.Run("the model folds the engineering investigation prerequisite like the fold", func(t *testing.T) {
		t.Parallel()
		const workID = "approval-liveness-engineering-unobserved"
		definition := approvalRouteDefinition(t)
		registry, registered := newResolutionRegistry(t, definition)
		s, owner := seedResolutionSelection(t, workID, registry, registered)
		actors := recordApprovalActors(t, s, workID)
		advanceResolutionToExecution(t, s, registry, workID, owner, []string{})
		advanceResolutionDelivery(t, s, registry, workID, owner)
		if err := recordApprovalVerdict(t, s, registry, workID, actors.reviewer); err != nil {
			t.Fatalf("the independent evaluator's verdict refused: %v", err)
		}
		// The real fold over the seeded history: the investigation
		// observation is absent, so the confirmation refuses with the
		// folded missing-evidence prerequisite.
		state := foldedApprovalAdmission(t, s, registry, definition, workID)
		foldDecision := workflowAdmit(definition, state, "confirm_premise")
		// The model lift over the same dimensions must reach the same
		// decision: engineering without its recorded investigation folds the
		// prerequisite refusal, never an open confirmation.
		model := admissionWorkflowState(definition, admissionModelState{step: "acceptance", contracts: 1, verdict: "ok", artifact: false, engineering: true})
		modelDecision := workflowAdmit(definition, model, "confirm_premise")
		var foldFailure, modelFailure *Failure
		failureAs(foldDecision.Failure, &foldFailure)
		failureAs(modelDecision.Failure, &modelFailure)
		if foldDecision.Admitted != modelDecision.Admitted || foldDecision.Admitted || foldFailure == nil || modelFailure == nil || foldFailure.Kind != modelFailure.Kind {
			t.Fatalf("model decision parity drifted: fold admitted=%v failure=%v, model admitted=%v failure=%v", foldDecision.Admitted, foldDecision.Failure, modelDecision.Admitted, modelDecision.Failure)
		}
		// The conformance view carries the prerequisite dimension, so a lift
		// that omits it fails the comparison instead of drifting silently.
		// Both views — the fold's and the model's — must report the refusal
		// present, or the dimension compares two absent answers.
		if !admissionConformanceViewOf(state).AcceptanceDeliverablesMissing {
			t.Fatal("the conformance view of the real fold reports no missing deliverable; the dimension is not lifted")
		}
		if !admissionConformanceViewOf(model).AcceptanceDeliverablesMissing {
			t.Fatal("the conformance view of the model lift reports no missing deliverable; the dimension is not lifted")
		}
		conformanceCheckpoint(t, s, registry, workID, definition, admissionModelState{step: "acceptance", contracts: 1, verdict: "ok", artifact: false, engineering: true}, "engineering without the recorded investigation")
	})

	t.Run("decision keeps the open question exit and the evaluator's admitted route", func(t *testing.T) {
		t.Parallel()
		const workID = "approval-liveness-decision"
		s, registry, definition, _, actors := seedApprovalItemAtAcceptance(t, workID, []string{"product"})
		// The independently authenticated evaluator's action is admitted at
		// the acceptance step, not assumed.
		if decision := workflowAdmit(definition, foldedApprovalAdmission(t, s, registry, definition, workID), "record_verdict"); !decision.Admitted {
			t.Fatalf("the evaluator's record_verdict route is not admitted at acceptance: %v", decision.Failure)
		}
		if err := recordApprovalVerdict(t, s, registry, workID, actors.reviewer); err != nil {
			t.Fatalf("the independent evaluator's verdict refused: %v", err)
		}
		conformanceCheckpoint(t, s, registry, workID, definition, admissionModelState{step: "acceptance", contracts: 1, verdict: "ok", artifact: true}, "decision at acceptance")
		state := foldedApprovalAdmission(t, s, registry, definition, workID)
		if decision := workflowAdmit(definition, state, "confirm_premise"); !decision.Admitted {
			t.Fatalf("decision confirm_premise refused behind its open question: %v", decision.Failure)
		}
		// The work pin projects the real question, and its digest is the
		// exact verified one.
		digest := approvalDecisionDigest(t, s, workID)
		question, withheld := approvalOperatorQuestionProjection(t, s, registry, definition, registeredDigest(t, registry, definition), workID)
		if question == nil || withheld != nil {
			t.Fatalf("decision work projects question %+v withheld %+v, want the open question without a withheld reason", question, withheld)
		}
		if question.DecisionContextDigest != digest {
			t.Fatalf("the projected question digest = %q, want the exact verified digest %q", question.DecisionContextDigest, digest)
		}
	})

	t.Run("decision without its investigation keeps the question-closed wall", func(t *testing.T) {
		t.Parallel()
		const workID = "approval-liveness-decision-unopened"
		definition := approvalRouteDefinition(t)
		registry, registered := newResolutionRegistry(t, definition)
		s, owner := seedResolutionSelection(t, workID, registry, registered)
		recordApprovalActors(t, s, workID)
		advanceResolutionToExecution(t, s, registry, workID, owner, []string{"product"})
		advanceResolutionDelivery(t, s, registry, workID, owner)
		// No investigation observation: the decision question never opens.
		state := foldedApprovalAdmission(t, s, registry, definition, workID)
		if state.PendingOperatorDecision {
			t.Fatal("the decision question opened without its recorded investigation")
		}
		decision := workflowAdmit(definition, state, "confirm_premise")
		var failure *Failure
		if decision.Admitted || !failureAs(decision.Failure, &failure) || failure.Kind != KindStaleRequiresReview {
			t.Fatalf("decision confirm_premise without the investigation folded %v, want the question-closed wall", decision.Failure)
		}
	})

	t.Run("terminal failure retry consumes the folded policy", func(t *testing.T) {
		t.Parallel()
		// The failed binding is fold-derived: the fenced dispatch start, its
		// attempt-binding completion, and the terminal worker.failed event are
		// authored through the registered folds, and the loader derives the
		// current failed binding with its exact attempt, epoch, and contract
		// identity. No live dispatch owner is invoked.
		const workID = "approval-liveness-retry"
		definition := approvalRouteDefinition(t)
		registry, registered := newResolutionRegistry(t, definition)
		s, owner := seedResolutionSelection(t, workID, registry, registered)
		recordApprovalActors(t, s, workID)
		advanceResolutionPrefix(t, s, registry, workID, owner, []string{}, false)
		ownerRef := ownerRefForTest(t, owner)
		lane := BuiltinLaneDefinitions()[0]
		version := readWorkVersion(t, s, workID)
		appendWorkflowEventsWithRegistry(t, s, registry, Operation{
			Events: []Event{
				workflowEventWithActor("approval-retry-start-"+workID, WorkflowActionStarted, workID, ownerRef, map[string]any{
					"work_id": workID, "expected_version": version, "resulting_version": version + 1,
					"step_id": "execution", "action_id": "dispatch_worker", "attempt_epoch": 2,
					"accepted_inputs_digest": "sha256:" + strings.Repeat("f", 64), "idempotency_identity": "approval-retry-start:" + workID,
					"actor_ref": ownerRef, "execution_model": preferredModelForLane(lane),
				}),
				workflowRetryDispatchCompletion(workID, ownerRef, version+1, lane),
				{EventID: "approval-retry-promote-" + workID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: ownerRef, OccurredAt: time.Unix(14, 30).UTC(), PayloadVersion: 2, Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: "attempt:approval-retry", LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion, LaneActorRef: ownerRef})},
				{EventID: "approval-retry-failed-" + workID, Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: ownerRef, OccurredAt: time.Unix(15, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(WorkerFailedPayload{AttemptID: "attempt:approval-retry", ReadbackModel: preferredModelForLane(lane), FailureKind: WorkerFailureWorkerError, Detail: "the retry witness lane failed"})},
			},
			ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version},
		})
		// Both policies answer over the same seeded terminal failure.
		newState := foldedApprovalAdmission(t, s, registry, definition, workID)
		if newState.Step != "execution" {
			t.Fatalf("the retry seed left the step at %q, want execution", newState.Step)
		}
		if !newState.WorkerFailureRecovery || newState.AttemptState != "failed" {
			t.Fatalf("terminal failure recovery = %+v, want the failed attempt recovery route", newState)
		}
		newDecision := workflowAdmit(definition, newState, "dispatch_worker")
		if !newDecision.Admitted {
			t.Fatalf("consequence_v1 below-wall retry refused at admission: %v", newDecision.Failure)
		}
		// The legacy half is a pure legacy twin: the fixture definition over
		// the same folded history, not an actual legacy pinned fold — the
		// released legacy witnesses carry that separately. The seeded fixture
		// declares no worker jobs, so the job-binding member of the retry
		// binding stays outside this witness's scope.
		legacy := workflowFixtureDefinition(t, 2).Definition
		legacyState := foldedApprovalAdmission(t, s, registry, legacy, workID)
		legacyDecision := workflowAdmit(legacy, legacyState, "dispatch_worker")
		if !legacyDecision.Admitted {
			t.Fatalf("terminal failure refused legacy dispatch: %v", legacyDecision.Failure)
		}
	})

	t.Run("correction stays a routine admission route for both policies", func(t *testing.T) {
		t.Parallel()
		newPolicy := approvalRouteDefinition(t)
		legacy := workflowFixtureDefinition(t, 2).Definition
		available := WorkflowAdmissionState{CorrectionRequestRecovery: true}
		for name, definition := range map[string]WorkflowDefinition{"consequence_v1": newPolicy, "legacy": legacy} {
			decision := workflowAdmit(definition, available, "request_correction")
			if !decision.Admitted {
				t.Fatalf("%s correction route folded admitted=%v, want a routine admitted route", name, decision.Admitted)
			}
			unavailable := workflowAdmit(definition, WorkflowAdmissionState{}, "request_correction")
			var failure *Failure
			if unavailable.Admitted || !failureAs(unavailable.Failure, &failure) {
				t.Fatalf("%s unavailable correction folded %v, want the typed availability refusal without an approval route", name, unavailable.Failure)
			}
		}
	})

	t.Run("the fold routes the unhealthy artifact through the real correction action", func(t *testing.T) {
		t.Parallel()
		const workID = "approval-liveness-correction"
		s, registry, definition, owner, actors := seedApprovalItemAtAcceptance(t, workID, []string{})
		target := workflowUnhealthyVerdictRouteTarget(definition, "acceptance")
		if target == "" {
			t.Fatal("the pinned definition declares no unhealthy-verdict route at acceptance")
		}
		// A healthy verdict stands first; the evaluator's fresh verdict then
		// fails the delivered outcome at the verdict step.
		if err := recordApprovalVerdict(t, s, registry, workID, actors.reviewer); err != nil {
			t.Fatalf("the independent evaluator's verdict refused: %v", err)
		}
		if err := runApprovalActionTx(t, s, registry, workID, "record_verdict", actors.reviewer, nil, "", "", `{"predicate_id":"predicate:primary","verdict_kind":"outcome_mismatch","evaluation_evidence":["evidence:operator-verdict"],"incomparable_with_approved":true}`); err != nil {
			t.Fatalf("the failed independent evaluation verdict refused: %v", err)
		}
		// The model and the fold agree: the unhealthy artifact opens the
		// correction route at the verdict step.
		conformanceCheckpoint(t, s, registry, workID, definition, admissionModelState{step: "acceptance", contracts: 1, verdict: "bad", artifact: true, artifactStale: true, engineering: true}, "unhealthy artifact at acceptance")
		state := foldedApprovalAdmission(t, s, registry, definition, workID)
		if !state.CorrectionRequestRecovery {
			t.Fatal("the fold opened no correction route behind the unhealthy verdict")
		}
		// The real correction action runs without any operator substitution
		// or consent artifact: the authenticated coordinator requests the
		// repair, and the fold routes the instance to the declared target.
		if err := runApprovalActionTx(t, s, registry, workID, "request_correction", owner, nil, "", "", `{"diagnosis":"the delivered subject does not satisfy the approved predicate","strategy":"repeat the implementation external effect","predicate_ids":["predicate:primary"],"evidence_refs":["evidence:operator-verdict"]}`); err != nil {
			t.Fatalf("the real correction action refused: %v", err)
		}
		var currentStep string
		if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&currentStep); err != nil {
			t.Fatal(err)
		}
		if currentStep != target {
			t.Fatalf("the correction returned the instance to %q, want the declared route target %q", currentStep, target)
		}
		// The model and the fold agree at the returned producer state: the
		// prefix's fenced execution start anchors the delivery-bearing epoch
		// the fold still carries.
		conformanceCheckpoint(t, s, registry, workID, definition, admissionModelState{step: target, contracts: 1, verdict: "bad", artifact: true, artifactStale: true, started: true, engineering: true}, "correction returned to the producer")
		// The route is spent at the target: a second correction refuses with
		// no version advance and no stranded fold guard.
		baselineVersion := readWorkVersion(t, s, workID)
		againErr := runApprovalActionTx(t, s, registry, workID, "request_correction", owner, nil, "", "", `{"diagnosis":"the delivered subject still does not satisfy the approved predicate","strategy":"repeat the implementation external effect","predicate_ids":["predicate:primary"],"evidence_refs":["evidence:operator-verdict"]}`)
		var againFailure *Failure
		if againErr == nil || !failureAs(againErr, &againFailure) {
			t.Fatalf("the second correction folded %v, want a typed refusal", againErr)
		}
		if version := readWorkVersion(t, s, workID); version != baselineVersion {
			t.Fatalf("the refused correction advanced the work version from %d to %d", baselineVersion, version)
		}
		assertNoFoldGuardRows(t, s)
		// The pure legacy twin: the same folded history read under the
		// legacy-shaped fixture definition keeps the correction route, and no
		// approval route replaces it.
		legacy := workflowFixtureDefinition(t, 2).Definition
		legacyState := foldedApprovalAdmission(t, s, registry, legacy, workID)
		if legacyState.CorrectionRequestRecovery {
			t.Fatalf("the legacy twin opened a correction route at the producer step: %+v", legacyState)
		}
		legacyDecision := workflowAdmit(legacy, WorkflowAdmissionState{Step: "acceptance", CorrectionRequestRecovery: true}, "request_correction")
		if !legacyDecision.Admitted {
			t.Fatalf("the legacy correction route folded %+v, want the unchanged routine route", legacyDecision)
		}
	})

	t.Run("legacy pins keep the shape-based question chain", func(t *testing.T) {
		t.Parallel()
		legacy := workflowFixtureDefinition(t, 2).Definition
		open := workflowAdmit(legacy, WorkflowAdmissionState{Step: "acceptance", PendingOperatorDecision: true}, "confirm_premise")
		if !open.Admitted {
			t.Fatalf("legacy confirm_premise behind its open question refused: %v", open.Failure)
		}
		closed := workflowAdmit(legacy, WorkflowAdmissionState{Step: "acceptance"}, "confirm_premise")
		var failure *Failure
		if closed.Admitted || !closed.OperatorQuestionClosed || !failureAs(closed.Failure, &failure) || failure.Kind != KindStaleRequiresReview {
			t.Fatalf("legacy closed-question confirm folded %v, want the unchanged question-closed wall", closed.Failure)
		}
	})

	t.Run("engineering to decision revision flips the folded class and the successor keeps the operator chain", func(t *testing.T) {
		t.Parallel()
		const workID = "approval-liveness-revision-e2d"
		s, registry, definition, owner, actors := seedApprovalItemAtAcceptance(t, workID, []string{})
		if err := recordApprovalVerdict(t, s, registry, workID, actors.reviewer); err != nil {
			t.Fatalf("the independent evaluator's verdict refused: %v", err)
		}
		conformanceCheckpoint(t, s, registry, workID, definition, admissionModelState{step: "acceptance", contracts: 1, verdict: "ok", artifact: true, engineering: true}, "engineering before the revision")
		predecessorDigest := approvalDecisionDigest(t, s, workID)
		// The class revision authors the successor fact through the
		// registered fold; the supersession stales the judged artifact until
		// fresh production at its route target.
		appendWorkflowEventsWithRegistry(t, s, registry, Operation{
			Events:           []Event{approvalClassRevisionEvent(t, s, workID, ownerRefForTest(t, owner), []string{"product"})},
			ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): readWorkVersion(t, s, workID)},
		})
		// The revision stales the judged artifact: a fresh healthy verdict
		// needs real production at the route target, so the successor's
		// acceptance obligations stay unmet by design.
		conformanceCheckpoint(t, s, registry, workID, definition, admissionModelState{step: "acceptance", contracts: 1, verdict: "ok", artifact: true, artifactStale: true}, "decision successor after the revision")
		// The successor's projected question carries its own digest: the
		// predecessor's recorded digest cannot answer it.
		successorDigest := approvalDecisionDigest(t, s, workID)
		if successorDigest == predecessorDigest {
			t.Fatal("the successor's decision digest equals the predecessor's; a stale digest could answer the successor")
		}
		question, withheld := approvalOperatorQuestionProjection(t, s, registry, definition, registeredDigest(t, registry, definition), workID)
		if question == nil || withheld != nil {
			t.Fatalf("the decision successor projects question %+v withheld %+v, want the open question without a withheld reason", question, withheld)
		}
		if question.DecisionContextDigest != successorDigest {
			t.Fatalf("the successor's projected digest = %q, want %q", question.DecisionContextDigest, successorDigest)
		}
		// The successor is unconfirmed and unproduced: completion cannot run
		// from the acceptance step, and the work stays open.
		baselineVersion := readWorkVersion(t, s, workID)
		completeErr := runApprovalActionTx(t, s, registry, workID, "complete", owner, nil, "", "", `{"impact_verdict":"non-breaking"}`)
		var completeFailure *Failure
		if completeErr == nil || !failureAs(completeErr, &completeFailure) {
			t.Fatal("the unconfirmed decision successor completed")
		}
		if _, instanceState := approvalWorkAndInstanceState(t, s, workID); instanceState == "completed" {
			t.Fatal("the unconfirmed successor completed the revised work")
		}
		if version := readWorkVersion(t, s, workID); version != baselineVersion {
			t.Fatalf("the refused completion advanced the work version to %d", version)
		}
		assertNoFoldGuardRows(t, s)
	})

	t.Run("decision to engineering revision flips the folded class and closes the question", func(t *testing.T) {
		t.Parallel()
		const workID = "approval-liveness-revision-d2e"
		s, registry, definition, owner, actors := seedApprovalItemAtAcceptance(t, workID, []string{"product"})
		if err := recordApprovalVerdict(t, s, registry, workID, actors.reviewer); err != nil {
			t.Fatalf("the independent evaluator's verdict refused: %v", err)
		}
		conformanceCheckpoint(t, s, registry, workID, definition, admissionModelState{step: "acceptance", contracts: 1, verdict: "ok", artifact: true}, "decision before the revision")
		appendWorkflowEventsWithRegistry(t, s, registry, Operation{
			Events:           []Event{approvalClassRevisionEvent(t, s, workID, ownerRefForTest(t, owner), []string{})},
			ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): readWorkVersion(t, s, workID)},
		})
		// The revision stales the judged artifact: a fresh healthy verdict
		// needs real production at the route target, so the successor's
		// acceptance obligations stay unmet by design.
		conformanceCheckpoint(t, s, registry, workID, definition, admissionModelState{step: "acceptance", contracts: 1, verdict: "ok", artifact: true, artifactStale: true, engineering: true}, "engineering successor after the revision")
		// The work pin projects no operator question for the engineering
		// successor.
		question, withheld := approvalOperatorQuestionProjection(t, s, registry, definition, registeredDigest(t, registry, definition), workID)
		if question != nil || withheld != nil {
			t.Fatalf("the engineering successor projects question %+v withheld %+v, want neither", question, withheld)
		}
		// The judged artifact went stale with the revision: the successor's
		// confirmation refuses on the artifact until fresh production at the
		// route target, whatever class the successor declares.
		err := runApprovalActionTx(t, s, registry, workID, "confirm_premise", owner, nil, "confirm", "", `{"selected_choice":"confirm"}`)
		var staleFailure *Failure
		if err == nil || !failureAs(err, &staleFailure) || staleFailure.Kind != KindStaleRequiresReview {
			t.Fatalf("the engineering successor's confirmation folded %v, want the artifact-staleness refusal", err)
		}
		// The successor is unconfirmed and unproduced: completion cannot run
		// from the acceptance step, and the work stays open.
		baselineVersion := readWorkVersion(t, s, workID)
		completeErr := runApprovalActionTx(t, s, registry, workID, "complete", owner, nil, "", "", `{"impact_verdict":"non-breaking"}`)
		var completeFailure *Failure
		if completeErr == nil || !failureAs(completeErr, &completeFailure) {
			t.Fatal("the unconfirmed engineering successor completed")
		}
		if _, instanceState := approvalWorkAndInstanceState(t, s, workID); instanceState == "completed" {
			t.Fatal("the unconfirmed successor completed the revised work")
		}
		if version := readWorkVersion(t, s, workID); version != baselineVersion {
			t.Fatalf("the refused completion advanced the work version to %d", version)
		}
		assertNoFoldGuardRows(t, s)
	})

	t.Run("continuity projects the same folded confirmation answer at both levels", func(t *testing.T) {
		t.Parallel()
		const engineeringID = "approval-continuity-engineering"
		const decisionID = "approval-continuity-decision"
		const absentID = "approval-continuity-absent"
		s, registry, _, _, actors := seedApprovalItemAtAcceptance(t, engineeringID, []string{})
		s2, registry2, _, _, actors2 := seedApprovalItemAtAcceptance(t, decisionID, []string{"product"})
		s3, registry3, _ := seedApprovalAbsentFactAtAcceptance(t, absentID)
		if err := recordApprovalVerdict(t, s, registry, engineeringID, actors.reviewer); err != nil {
			t.Fatalf("the independent evaluator's verdict refused: %v", err)
		}
		if err := recordApprovalVerdict(t, s2, registry2, decisionID, actors2.reviewer); err != nil {
			t.Fatalf("the independent evaluator's verdict refused: %v", err)
		}
		// The engineering continuity snapshot projects neither a question nor
		// a withholding at the top level or on the seated work pin.
		engineeringSnapshot := approvalContinuitySnapshot(t, s, registry, engineeringID)
		if engineeringSnapshot.PendingOperatorDecision != nil || engineeringSnapshot.WithheldOperatorDecision != nil {
			t.Fatalf("engineering continuity top level projects %+v/%+v, want neither", engineeringSnapshot.PendingOperatorDecision, engineeringSnapshot.WithheldOperatorDecision)
		}
		if engineeringSnapshot.WorkPin == nil || engineeringSnapshot.WorkPin.PendingOperatorDecision != nil || engineeringSnapshot.WorkPin.WithheldOperatorDecision != nil {
			t.Fatalf("engineering continuity work pin projects %+v/%+v, want neither", approvalWorkPinQuestion(engineeringSnapshot), approvalWorkPinWithheld(engineeringSnapshot))
		}
		// The decision continuity snapshot projects the open question with its
		// exact verified digest at both levels.
		decisionDigest := approvalDecisionDigest(t, s2, decisionID)
		decisionSnapshot := approvalContinuitySnapshot(t, s2, registry2, decisionID)
		if decisionSnapshot.PendingOperatorDecision == nil || decisionSnapshot.WithheldOperatorDecision != nil {
			t.Fatalf("decision continuity top level projects %+v/%+v, want the open question without a withheld reason", decisionSnapshot.PendingOperatorDecision, decisionSnapshot.WithheldOperatorDecision)
		}
		if decisionSnapshot.PendingOperatorDecision.DecisionContextDigest != decisionDigest {
			t.Fatalf("the continuity top-level digest = %q, want the exact verified digest %q", decisionSnapshot.PendingOperatorDecision.DecisionContextDigest, decisionDigest)
		}
		if decisionSnapshot.WorkPin == nil || decisionSnapshot.WorkPin.PendingOperatorDecision == nil || decisionSnapshot.WorkPin.WithheldOperatorDecision != nil {
			t.Fatalf("decision continuity work pin projects %+v/%+v, want the open question", approvalWorkPinQuestion(decisionSnapshot), approvalWorkPinWithheld(decisionSnapshot))
		}
		if decisionSnapshot.WorkPin.PendingOperatorDecision.DecisionContextDigest != decisionDigest {
			t.Fatalf("the continuity work pin digest = %q, want the exact verified digest %q", decisionSnapshot.WorkPin.PendingOperatorDecision.DecisionContextDigest, decisionDigest)
		}
		// A missing new-policy fact withholds the question with its typed
		// reason at both levels: absence is never engineering.
		absentSnapshot := approvalContinuitySnapshot(t, s3, registry3, absentID)
		if absentSnapshot.WithheldOperatorDecision == nil || !strings.Contains(absentSnapshot.WithheldOperatorDecision.Reason, "absence is a legacy shape") {
			t.Fatalf("the absent-fact continuity top level withholds %+v, want the typed absence reason", absentSnapshot.WithheldOperatorDecision)
		}
		if absentSnapshot.PendingOperatorDecision != nil {
			t.Fatalf("the absent-fact continuity top level projects %+v, want no question", absentSnapshot.PendingOperatorDecision)
		}
		if absentSnapshot.WorkPin == nil || absentSnapshot.WorkPin.WithheldOperatorDecision == nil || !strings.Contains(absentSnapshot.WorkPin.WithheldOperatorDecision.Reason, "absence is a legacy shape") {
			t.Fatalf("the absent-fact continuity work pin withholds %+v, want the typed absence reason", approvalWorkPinWithheld(absentSnapshot))
		}
		if absentSnapshot.WorkPin.PendingOperatorDecision != nil {
			t.Fatalf("the absent-fact continuity work pin projects %+v, want no question", approvalWorkPinQuestion(absentSnapshot))
		}
		// A drifted registry carrying the same family and version with
		// different bytes refuses the continuity projection, and the builtin
		// convenience keeps refusing the local pin.
		drifted := approvalRouteDefinition(t)
		drifted.EvaluatorIndependence.ModelDistinct = !drifted.EvaluatorIndependence.ModelDistinct
		driftRegistry := NewWorkflowDefinitionRegistry()
		if _, err := driftRegistry.Register(drifted); err != nil {
			t.Fatalf("the drifted definition refused registration: %v", err)
		}
		if err := approvalContinuityProjectionError(t, s, driftRegistry, engineeringID); err == nil {
			t.Fatal("the continuity projection accepted a digest-drifted registry")
		}
		missingRegistry := NewWorkflowDefinitionRegistry()
		if err := approvalContinuityProjectionError(t, s, missingRegistry, engineeringID); err == nil {
			t.Fatal("the continuity projection accepted a registry missing the pinned definition")
		}
		if err := approvalContinuityProjectionError(t, s, nil, engineeringID); err == nil {
			t.Fatal("the builtin continuity convenience resolved the local pin")
		}
	})

	t.Run("public selection keeps its no-op fast path without an instance", func(t *testing.T) {
		t.Parallel()
		s := openTemp(t)
		if err := ValidateWorkflowOperatorSelectionForRegistry(context.Background(), s, nil, WorkflowActionPreflightRequest{WorkID: "approval-fastpath-public-absent", ExpectedVersion: 1, ActionID: "record_proposal", SelectedChoice: ""}); err != nil {
			t.Fatalf("the public non-confirm selection without fields refused on absent work: %v", err)
		}
		publicInvalidErr := ValidateWorkflowOperatorSelectionForRegistry(context.Background(), s, nil, WorkflowActionPreflightRequest{WorkID: "approval-fastpath-public-absent", ExpectedVersion: 1, ActionID: "record_proposal", SelectedChoice: "confirm"})
		var publicInvalidFailure *Failure
		if publicInvalidErr == nil || !failureAs(publicInvalidErr, &publicInvalidFailure) || publicInvalidFailure.Kind != KindInvalidPayload {
			t.Fatalf("the public non-confirm selection with question fields folded %v, want the invalid-payload refusal", publicInvalidErr)
		}
	})

	t.Run("non-confirm selection keeps its no-op fast path without an instance", func(t *testing.T) {
		t.Parallel()
		s := openTemp(t)
		ctx := context.Background()
		tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		request := WorkflowActionPreflightRequest{WorkID: "approval-fastpath-absent", ActionID: "record_proposal"}
		if err := validateWorkflowOperatorSelectionTx(ctx, tx, BuiltinWorkflowRegistry(), request); err != nil {
			t.Fatalf("the non-confirm selection without fields refused on absent work: %v", err)
		}
		request.SelectedChoice = "confirm"
		invalidErr := validateWorkflowOperatorSelectionTx(ctx, tx, BuiltinWorkflowRegistry(), request)
		var invalidFailure *Failure
		if invalidErr == nil || !failureAs(invalidErr, &invalidFailure) || invalidFailure.Kind != KindInvalidPayload {
			t.Fatalf("the non-confirm selection with question fields folded %v, want the invalid-payload refusal", invalidErr)
		}
	})

	t.Run("missing and malformed decision facts refuse as typed failures", func(t *testing.T) {
		t.Parallel()
		const workID = "approval-liveness-absent-fact"
		definition := approvalRouteDefinition(t)
		s, registry, owner := seedApprovalAbsentFactAtAcceptance(t, workID)
		// The confirmation classifies from the active contract's fact: absence
		// is a legacy shape, not engineering, and refuses before any effect.
		baselineVersion := readWorkVersion(t, s, workID)
		baselineEvents := int64(approvalEventCount(t, s, workID))
		var baselineConfirmations int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_premise_confirmations WHERE work_id=?`, workID).Scan(&baselineConfirmations); err != nil {
			t.Fatal(err)
		}
		err := runApprovalActionTx(t, s, registry, workID, "confirm_premise", owner, nil, "confirm", "", `{"selected_choice":"confirm"}`)
		var absentFailure *Failure
		if err == nil || !failureAs(err, &absentFailure) || absentFailure.Kind != KindInvalidInput || !strings.Contains(absentFailure.Detail, "absence is a legacy shape") {
			t.Fatalf("the absent decision fact folded %v, want the typed absence refusal", err)
		}
		if events := int64(approvalEventCount(t, s, workID)); events != baselineEvents {
			t.Fatalf("the refused confirmation left %d events (baseline %d)", events, baselineEvents)
		}
		if version := readWorkVersion(t, s, workID); version != baselineVersion {
			t.Fatalf("the refused confirmation advanced the work version to %d", version)
		}
		var confirmations int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_premise_confirmations WHERE work_id=?`, workID).Scan(&confirmations); err != nil {
			t.Fatal(err)
		}
		if confirmations != baselineConfirmations {
			t.Fatalf("the refused confirmation left %d confirmation rows (baseline %d)", confirmations, baselineConfirmations)
		}
		assertNoFoldGuardRows(t, s)
		// The question owner must not fabricate a confirmation withholding on
		// a step that declares no confirmation action: absence at execution is
		// simply no question, whatever the folded fact says. The state-backed
		// projection resolves the actual carried registered definition and
		// digest, the pin the carry forward retained, never a freshly made
		// definition shape.
		var carriedRef, carriedDigest string
		var carriedVersion int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_version,definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&carriedRef, &carriedVersion, &carriedDigest); err != nil {
			t.Fatal(err)
		}
		definitionEntry, ok := registry.Lookup(carriedRef, carriedVersion)
		if !ok || definitionEntry.Digest != carriedDigest {
			t.Fatalf("the carried pin %s@%d %s does not resolve in the caller registry", carriedRef, carriedVersion, carriedDigest)
		}
		// The store pools one connection: the version reads before the
		// transaction opens, never beside it.
		noActionVersion := readWorkVersion(t, s, workID)
		tx, txErr := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if txErr != nil {
			t.Fatal(txErr)
		}
		confirmation, confirmationErr := foldWorkflowConfirmationTx(context.Background(), tx, workID, definitionEntry.Definition)
		if confirmationErr != nil {
			t.Fatal(confirmationErr)
		}
		noActionQuestion, noActionWithheld, noActionErr := workflowOperatorQuestionForDefinitionTx(context.Background(), tx, workID, "execution", noActionVersion, definitionEntry.Definition, WorkflowReadDefinition{Ref: definitionEntry.Definition.Ref, Version: definitionEntry.Definition.Version, Digest: definitionEntry.Digest}, WorkflowReadContract{}, confirmation)
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			t.Fatal(rollbackErr)
		}
		if noActionErr != nil {
			t.Fatalf("the question projection refused at a step without a confirmation action: %v", noActionErr)
		}
		if noActionQuestion != nil || noActionWithheld != nil {
			t.Fatalf("the absent fact fabricated question %+v withheld %+v on a step without a confirmation action", noActionQuestion, noActionWithheld)
		}
		// The classification boundary holds the same limit: no fact, a
		// non-canonical order, and a duplicate domain refuse as typed input
		// failures instead of folding to engineering.
		for name, domains := range map[string][]string{
			"absent":            nil,
			"non-canonical":     {"product", "preference"},
			"duplicate":         {"product", "product"},
			"outside the limit": {"preference", "product", "ui_ux", "preference"},
		} {
			_, classifyErr := ClassifyWorkflowApproval(definition, "confirm_premise", WorkflowApprovalFacts{ActiveDecisionDomains: domains})
			var classifyFailure *Failure
			if classifyErr == nil || !failureAs(classifyErr, &classifyFailure) || classifyFailure.Kind != KindInvalidInput {
				t.Fatalf("the classifier accepted the %s decision fact set %v: %v", name, domains, classifyErr)
			}
		}
	})

	t.Run("the acceptance fold stays total under a duplicated active count", func(t *testing.T) {
		t.Parallel()
		// Direct state over real seeds: the duplicated count claims the
		// ambiguous projection the supersede recovery owns, so the acceptance
		// fold must skip its singular active-contract read and leave the
		// confirmation unfolded — never fail the loader before
		// workflowAdmitSupersede classifies the recovery.
		const engineeringID = "approval-duplicate-count-engineering"
		const decisionID = "approval-duplicate-count-decision"
		s, registry, definition, _, engineeringActors := seedApprovalItemAtAcceptance(t, engineeringID, []string{})
		s2, registry2, _, _, decisionActors := seedApprovalItemAtAcceptance(t, decisionID, []string{"product"})
		if err := recordApprovalVerdict(t, s, registry, engineeringID, engineeringActors.reviewer); err != nil {
			t.Fatalf("the independent evaluator's verdict refused: %v", err)
		}
		if err := recordApprovalVerdict(t, s2, registry2, decisionID, decisionActors.reviewer); err != nil {
			t.Fatalf("the independent evaluator's verdict refused: %v", err)
		}
		for name, seed := range map[string]struct {
			store  *Store
			workID string
		}{"engineering": {s, engineeringID}, "decision": {s2, decisionID}} {
			ctx := context.Background()
			tx, txErr := seed.store.DatabaseForTesting().BeginTx(ctx, nil)
			if txErr != nil {
				t.Fatal(txErr)
			}
			state := WorkflowAdmissionState{ActiveContracts: 2, Step: "acceptance"}
			if err := loadWorkflowAcceptanceAdmission(ctx, tx, seed.workID, definition, &state); err != nil {
				tx.Rollback()
				t.Fatalf("%s acceptance fold under the duplicated count refused: %v", name, err)
			}
			if state.ConfirmationAuthority != WorkflowConfirmationNone || state.ConfirmationFactFailure != nil {
				tx.Rollback()
				t.Fatalf("%s acceptance fold classified the duplicated projection: authority %q fact %+v, want the unfolded zero answer", name, state.ConfirmationAuthority, state.ConfirmationFactFailure)
			}
			if state.PendingOperatorDecision {
				tx.Rollback()
				t.Fatalf("%s acceptance fold opened an operator question on the duplicated projection", name)
			}
			decision := workflowAdmitSupersede(definition, state, WorkflowAdmissionDecision{})
			if !decision.RecoveryRoute || !decision.Admitted {
				tx.Rollback()
				t.Fatalf("%s duplicated projection lost the supersede recovery: %+v", name, decision)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
		}
	})
}

// approvalClassRevisionEvent authors the versioned contract_superseded event
// whose successor carries the explicit decision fact. The event folds through
// the registered fold; no projection column is touched.
func approvalClassRevisionEvent(t *testing.T, s *Store, workID, actorRef string, successorDomains []string) Event {
	t.Helper()
	version := readWorkVersion(t, s, workID)
	successor := map[string]any{
		"contract_version": 2, "premise": "the reclassified accepted change",
		"outcome_kind":      "check",
		"outcome_payload":   map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"},
		"required_evidence": []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{},
		"law_revisions": []any{}, "law_boundary_version": 1,
		"rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
		"decision_domains": successorDomains,
	}
	return workflowTypedEvent("approval-revision-"+workID, WorkflowContractSuperseded, workID, actorRef, time.Unix(14, 0).UTC(), version, map[string]any{
		"previous_contract_version": 1, "predecessor_contract_versions": []int64{1}, "new_contract_version": 2,
		"supersede_reason": "reclassify the delivered work", "audit_evidence": []string{"audit:approval-revision-" + workID},
		"successor_contract": successor,
	})
}

// approvalContinuitySnapshot seats one continuity snapshot through the
// registry-threaded continuity core; the nil registry rides the builtin
// convenience and its refusal.
func approvalContinuitySnapshot(t *testing.T, s *Store, registry DefinitionRegistry, workID string) ContinuitySnapshot {
	t.Helper()
	snapshot, err := readWorkflowContinuityForRegistry(context.Background(), s, ContinuityRequest{Work: workID}, registry)
	if err != nil {
		t.Fatalf("the continuity snapshot refused: %v", err)
	}
	return snapshot
}

func approvalWorkPinQuestion(snapshot ContinuitySnapshot) *WorkflowOperatorQuestion {
	if snapshot.WorkPin == nil {
		return nil
	}
	return snapshot.WorkPin.PendingOperatorDecision
}

func approvalWorkPinWithheld(snapshot ContinuitySnapshot) *WorkflowOperatorQuestionWithheld {
	if snapshot.WorkPin == nil {
		return nil
	}
	return snapshot.WorkPin.WithheldOperatorDecision
}

// workflowRetryDispatchCompletion authors the versioned dispatch completion
// whose worker fields the current payload version carries.
func workflowRetryDispatchCompletion(workID, actorRef string, expected int64, lane LaneDefinition) Event {
	event := workflowEventWithActor("approval-retry-dispatch-"+workID, WorkflowActionCompleted, workID, actorRef, map[string]any{
		"work_id": workID, "expected_version": expected, "resulting_version": expected + 1,
		"step_id": "execution", "action_id": "dispatch_worker", "attempt_epoch": 2,
		"worker_attempt_id": "attempt:approval-retry", "result_evidence_refs": []string{}, "changed_refs": []string{workID},
		"worker_lane_id": lane.ID, "worker_lane_version": lane.Version, "worker_lane_digest": lane.Digest,
		"worker_capability_class": lane.CapabilityClass, "actor_ref": actorRef,
	})
	event.PayloadVersion = 2
	return event
}

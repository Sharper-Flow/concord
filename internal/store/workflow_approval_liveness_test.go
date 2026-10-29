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
)

func TestApprovalPolicyLiveness(t *testing.T) {
	t.Parallel()

	t.Run("engineering folds no human-question exit beside its real confirm exit", func(t *testing.T) {
		t.Parallel()
		const workID = "approval-liveness-engineering"
		s, registry, definition, _, _ := seedApprovalItemAtAcceptance(t, workID, []string{})
		// The model lifts engineering to no pending operator question; the
		// real fold must agree on every compared dimension.
		conformanceCheckpoint(t, s, registry, workID, definition, rAcceptanceModel(true), "engineering at acceptance")
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
		f := rAcceptance(t, workID, []string{}, true, false)
		s, registry, definition := f.store, BuiltinWorkflowRegistry(), mustBuiltinDefinition(t, "workflow.implementation").Definition
		unobserved := rAcceptanceModel(true)
		unobserved.artifact = false
		// The real fold over the seeded history: the investigation
		// observation is absent, so the confirmation refuses with the
		// folded missing-evidence prerequisite.
		state := foldedApprovalAdmission(t, s, registry, definition, workID)
		foldDecision := workflowAdmit(definition, state, "confirm_premise")
		// The model lift over the same dimensions must reach the same
		// decision: engineering without its recorded investigation folds the
		// prerequisite refusal, never an open confirmation.
		model := admissionWorkflowState(definition, unobserved)
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
		conformanceCheckpoint(t, s, registry, workID, definition, unobserved, "engineering without the recorded investigation")
	})

	t.Run("decision keeps the open question exit", func(t *testing.T) {
		t.Parallel()
		const workID = "approval-liveness-decision"
		s, registry, definition, _, _ := seedApprovalItemAtAcceptance(t, workID, []string{"product"})
		conformanceCheckpoint(t, s, registry, workID, definition, rAcceptanceModel(false), "decision at acceptance")
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
		f := rAcceptance(t, workID, []string{"product"}, true, false)
		s, registry, definition := f.store, BuiltinWorkflowRegistry(), mustBuiltinDefinition(t, "workflow.implementation").Definition
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

	t.Run("correction stays a routine admission route", func(t *testing.T) {
		t.Parallel()
		available := WorkflowAdmissionState{CorrectionRequestRecovery: true}
		for name, definition := range map[string]WorkflowDefinition{"consequence_v1": mustBuiltinDefinition(t, "workflow.implementation").Definition} {
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

	t.Run("engineering to decision revision flips the folded class and the successor keeps the operator chain", func(t *testing.T) {
		t.Parallel()
		const workID = "approval-liveness-revision-e2d"
		s, registry, definition, owner, _ := seedApprovalItemAtAcceptance(t, workID, []string{})
		conformanceCheckpoint(t, s, registry, workID, definition, rAcceptanceModel(true), "engineering before the revision")
		predecessorDigest := approvalDecisionDigest(t, s, workID)
		// The class revision authors the successor fact through the
		// registered fold. It changes no criterion, so the judged artifact
		// stays current (CD-0242 D7).
		rReviseContract(t, s, workID, ownerRefForTest(t, owner), []string{"product"})
		conformanceCheckpoint(t, s, registry, workID, definition, rAcceptanceModel(false), "decision successor after the revision")
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
		s, registry, definition, owner, _ := seedApprovalItemAtAcceptance(t, workID, []string{"product"})
		conformanceCheckpoint(t, s, registry, workID, definition, rAcceptanceModel(false), "decision before the revision")
		// The revision changes no criterion, so the judged artifact stays
		// current (CD-0242 D7).
		rReviseContract(t, s, workID, ownerRefForTest(t, owner), []string{})
		conformanceCheckpoint(t, s, registry, workID, definition, rAcceptanceModel(true), "engineering successor after the revision")
		// The work pin projects no operator question for the engineering
		// successor.
		question, withheld := approvalOperatorQuestionProjection(t, s, registry, definition, registeredDigest(t, registry, definition), workID)
		if question != nil || withheld != nil {
			t.Fatalf("the engineering successor projects question %+v withheld %+v, want neither", question, withheld)
		}
		// The judged artifact stays current through the revision, so the
		// coordinator confirms the engineering successor with no repeated
		// production (CD-0242 D7).
		if err := runApprovalActionTx(t, s, registry, workID, "confirm_premise", owner, nil, "confirm", "", `{"selected_choice":"confirm"}`); err != nil {
			t.Fatalf("the engineering successor's confirmation: %v", err)
		}
		// The confirmed successor reaches its terminal on the preserved proof.
		bindCompletionClosure(t, s, workID, owner)
		if err := runApprovalActionTx(t, s, registry, workID, "complete", owner, nil, "", "", `{"impact_verdict":"non-breaking"}`); err != nil {
			t.Fatalf("the confirmed engineering successor's completion: %v", err)
		}
		if lifecycle, instanceState := approvalWorkAndInstanceState(t, s, workID); lifecycle != "completed" || instanceState != "completed" {
			t.Fatalf("the confirmed successor ended lifecycle %q instance %q, want completed", lifecycle, instanceState)
		}
		assertNoFoldGuardRows(t, s)
	})

	t.Run("continuity projects the same folded confirmation answer at both levels", func(t *testing.T) {
		t.Parallel()
		const engineeringID = "approval-continuity-engineering"
		const decisionID = "approval-continuity-decision"
		const absentID = "approval-continuity-absent"
		s, registry, _, _, _ := seedApprovalItemAtAcceptance(t, engineeringID, []string{})
		s2, registry2, _, _, _ := seedApprovalItemAtAcceptance(t, decisionID, []string{"product"})
		s3, registry3, _ := seedApprovalAbsentFactAtAcceptance(t, absentID)
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
		missingRegistry := NewWorkflowDefinitionRegistry()
		if err := approvalContinuityProjectionError(t, s, missingRegistry, engineeringID); err == nil {
			t.Fatal("the continuity projection accepted a registry missing the pinned definition")
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
		if err == nil || !failureAs(err, &absentFailure) || absentFailure.Kind != KindInvariantViolation || !strings.Contains(absentFailure.Detail, "absence is a legacy shape") {
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
		if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&carriedRef, &carriedDigest); err != nil {
			t.Fatal(err)
		}
		definitionEntry, ok := registry.Current(carriedRef)
		if !ok || definitionEntry.Digest != carriedDigest {
			t.Fatalf("the carried pin %s %s does not resolve in the caller registry", carriedRef, carriedDigest)
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
		noActionQuestion, noActionWithheld, noActionErr := workflowOperatorQuestionForDefinitionTx(context.Background(), tx, workID, "execution", noActionVersion, definitionEntry.Definition, WorkflowReadDefinition{Ref: definitionEntry.Definition.Ref, Digest: definitionEntry.Digest}, WorkflowReadContract{}, confirmation)
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
		// non-canonical order, and a duplicate stored domain refuse as invariant
		// failures instead of folding to engineering.
		for name, domains := range map[string][]string{
			"absent":            nil,
			"non-canonical":     {"product", "preference"},
			"duplicate":         {"product", "product"},
			"outside the limit": {"preference", "product", "ui_ux", "preference"},
		} {
			_, classifyErr := ClassifyWorkflowApproval(definition, "confirm_premise", WorkflowApprovalFacts{ActiveDecisionDomains: domains})
			var classifyFailure *Failure
			if classifyErr == nil || !failureAs(classifyErr, &classifyFailure) || classifyFailure.Kind != KindInvariantViolation {
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
		s, _, definition, _, _ := seedApprovalItemAtAcceptance(t, engineeringID, []string{})
		s2, _, _, _, _ := seedApprovalItemAtAcceptance(t, decisionID, []string{"product"})
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
		"worker_lane_id": lane.ID, "worker_lane_digest": lane.Digest,
		"worker_capability_class": lane.CapabilityClass, "actor_ref": actorRef,
	})
	return event
}

package store

import (
	"encoding/json"
	"reflect"
	"testing"
)

func reviewGateStartStep(t *testing.T, s *Store, workID, stepID, startAction string, actor WorkflowActor) {
	t.Helper()
	reviewGateRequireStep(t, s, workID, stepID)
	if err := runVerdictActionAs(t, s, workID, startAction, json.RawMessage(`{}`), 0, actor); err != nil {
		t.Fatalf("start %s at %s: %v", startAction, stepID, err)
	}
}

func reviewGateRequireStep(t *testing.T, s *Store, workID, want string) {
	t.Helper()
	if got := currentStep(t, s, workID); got != want {
		t.Fatalf("current step=%q, want %q", got, want)
	}
}

func reviewGateRecordDelivery(t *testing.T, s *Store, workID string, actor WorkflowActor) error {
	t.Helper()
	payload := json.RawMessage(`{"delivery_artifact":"artifact:review-gate-` + workID + `","delivery_state":"asserted"}`)
	return runVerdictActionAs(t, s, workID, "record_delivery", payload, 0, actor)
}

func reviewGateRefineFixture(t *testing.T, workID string, planType WorkflowStepPlanType) *candidateJourney {
	t.Helper()
	j := pReturnReviewFixture(t, workID, planType, "ready_for_final_proof")
	j.acceptor = j.fixture.owner
	moveWorkflowPlanFixtureFrontier(t, j.s, workID, "review")
	return j
}

func reviewGateCompleteReview(t *testing.T, j *candidateJourney, disposition string) (string, int64) {
	t.Helper()
	return completeCurrentReviewForTest(t, j.fixture, disposition)
}

func reviewGateDeliveryRefusal(t *testing.T, j *candidateJourney) {
	t.Helper()
	before := j.effects(t)
	deliveries := reviewGateCountDeliveries(t, j.s, j.workID)
	err := j.action(t, "record_delivery", map[string]any{"delivery_artifact": "artifact:review-gate-" + j.workID, "delivery_state": "asserted"}, j.fixture.owner)
	if !hasFailureKind(err, KindMissingEvidence) {
		t.Fatalf("delivery with correction required=%v, want missing_evidence", err)
	}
	if j.effects(t) != before || reviewGateCountDeliveries(t, j.s, j.workID) != deliveries {
		t.Fatal("refused delivery changed durable state")
	}
	reviewGateRequireStep(t, j.s, j.workID, "review")
}

func reviewGateExitWithCurrentReview(t *testing.T, j *candidateJourney) {
	t.Helper()
	reviewGateCompleteReview(t, j, "ready_for_final_proof")
	j.bindVerification(t, j.observeRun(t, "review-integration"))
	if err := reviewGateRecordDelivery(t, j.s, j.workID, j.fixture.owner); err != nil {
		t.Fatalf("delivery after current Review: %v", err)
	}
	reviewGateRequireStep(t, j.s, j.workID, "review")
}

func reviewGateAcceptor(workID string) WorkflowActor {
	return WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/review-gate-acceptor", SessionRef: "session/" + workID + "-acceptor", ActorClass: ActorAgent}
}

func reviewGateChangeRecordedCriterion(t *testing.T, j *candidateJourney) {
	t.Helper()
	successor := pContractSuccessor(t, j.s, j.workID, "changed criterion")
	successor["outcome_predicates"].([]any)[0].(map[string]any)["outcome_payload"].(map[string]any)["expected_result"] = "fail"
	if err := reviseContractRecord(t, j.s, j.workID, j.fixture.owner, successor, readWorkVersion(t, j.s, j.workID)); err != nil {
		t.Fatal(err)
	}
}

func TestReviewOfReplacedCandidateCannotAuthorizeDelivery(t *testing.T) {
	j := reviewGateRefineFixture(t, "review-gate-stale-review", WorkflowStepPlanFix)
	input := currentReviewBlockForTest(t, j.s, j.workID, "ready_for_final_proof").InputCandidate
	oldRef := j.observeRun(t, "old-candidate").OperationRef
	pReturnProduce(t, j, "repair")
	early := currentReviewBlockForTest(t, j.s, j.workID, "ready_for_final_proof")
	early.InputCandidate = input
	reviewGateCompleteReview(t, j, "correction_required")
	state, err := readWorkflowReviewFacts(t.Context(), j.s.db, j.workID)
	if err != nil || state.Candidate == nil || state.Candidate.Commit == input.Commit {
		t.Fatalf("recorded Review did not replace the candidate: %+v, %v", state, err)
	}
	if err := workflowAcceptanceSubjectCurrent(t.Context(), j.s.db, j.workID, state.Candidate, []string{oldRef}); !hasFailureKind(err, KindStaleRequiresReview) {
		t.Fatalf("old candidate report authorized the replacement: %v", err)
	}
	before := j.effects(t)
	if err := recordReviewForTest(t, j.s, j.workID, j.fixture.owner, early); !hasFailureKind(err, KindStaleRequiresReview) {
		t.Fatalf("old report after candidate replacement=%v, want stale_requires_review", err)
	}
	if j.effects(t) != before {
		t.Fatal("refused stale report changed durable state")
	}
	reviewGateDeliveryRefusal(t, j)
	reviewGateExitWithCurrentReview(t, j)
}

func TestDeliveryGateTakesOrdinaryReviewUpdate(t *testing.T) {
	for _, family := range []WorkflowStepPlanType{WorkflowStepPlanFix, WorkflowStepPlanChange} {
		t.Run(string(family), func(t *testing.T) {
			j := reviewGateRefineFixture(t, "review-gate-parked-update", family)
			reviewGateCompleteReview(t, j, "ready_for_final_proof")
			j.bindVerification(t, j.observeRun(t, "parked-review"))
			if err := reviewGateRecordDelivery(t, j.s, j.workID, j.fixture.owner); err != nil {
				t.Fatalf("review delivery: %v", err)
			}
			reviewGateRequireStep(t, j.s, j.workID, "review")
			reviewGateChangeRecordedCriterion(t, j)
			reviewGateRequireStep(t, j.s, j.workID, "review")
			pin := issue1013Pin(t, j.s, j.workID)
			if pin.Review == nil || pin.Review.DependenciesCurrent || !workPinContainsAction(pin.NextValidIntents, "record_delivery") || !workPinContainsAction(pin.NextValidIntents, "record_review") {
				t.Fatalf("stale gate intents=%+v review=%+v", pin.NextValidIntents, pin.Review)
			}
			refuse := func(label string, kind FailureKind) {
				t.Helper()
				before := j.effects(t)
				deliveries := reviewGateCountDeliveries(t, j.s, j.workID)
				payload := json.RawMessage(`{"delivery_artifact":"artifact:review-gate-` + j.workID + `","delivery_state":"asserted"}`)
				_, preflightErr := WorkflowActionDefinitionFor(t.Context(), j.s, WorkflowActionPreflightRequest{WorkID: j.workID, ExpectedVersion: readWorkVersion(t, j.s, j.workID), ActionID: "record_delivery", Payload: payload, Actor: j.fixture.owner})
				if !hasFailureKind(preflightErr, kind) {
					t.Fatalf("%s: preflight=%v, want %s", label, preflightErr, kind)
				}
				if err := reviewGateRecordDelivery(t, j.s, j.workID, j.fixture.owner); !hasFailureKind(err, kind) {
					t.Fatalf("%s: gate crossing=%v, want %s", label, err, kind)
				}
				if j.effects(t) != before || reviewGateCountDeliveries(t, j.s, j.workID) != deliveries {
					t.Fatalf("%s: refused crossing changed durable state", label)
				}
				reviewGateRequireStep(t, j.s, j.workID, "review")
			}
			refuse("stale Review", KindStaleRequiresReview)
			reviewGateCompleteReview(t, j, "continue")
			reviewGateRequireStep(t, j.s, j.workID, "review")
			refuse("non-ready Review update", KindMissingEvidence)
			reviewGateCompleteReview(t, j, "ready_for_final_proof")
			reviewGateRequireStep(t, j.s, j.workID, "review")
			if err := reviewGateRecordDelivery(t, j.s, j.workID, j.fixture.owner); err != nil {
				t.Fatalf("delivery after fresh Review: %v", err)
			}
			reviewGateRequireStep(t, j.s, j.workID, "review")
		})
	}
}

func TestReviewApplicabilityFollowsCandidateNotDispatchOrder(t *testing.T) {
	t.Run("recorded correction blocks changed subject", func(t *testing.T) {
		j := reviewGateRefineFixture(t, "review-gate-before-repair", WorkflowStepPlanFix)
		reviewGateCompleteReview(t, j, "continue")
		reviewGateCompleteReview(t, j, "ready_for_final_proof")
		pReturnProduce(t, j, "repair")
		reviewGateCompleteReview(t, j, "correction_required")
		reviewGateDeliveryRefusal(t, j)
		reviewGateExitWithCurrentReview(t, j)
	})
	t.Run("unchanged qualification and draft keep Review applicable", func(t *testing.T) {
		j := reviewGateRefineFixture(t, "review-gate-unchanged", WorkflowStepPlanFix)
		before := issue1013Pin(t, j.s, j.workID).Review
		draft := currentReviewBlockForTest(t, j.s, j.workID, "ready_for_final_proof")
		j.observeRun(t, "unchanged-subject")
		after := issue1013Pin(t, j.s, j.workID).Review
		if before == nil || after == nil || !reflect.DeepEqual(before.Accepted, after.Accepted) || *before.Candidate != *after.Candidate || !after.DependenciesCurrent || draft.OutputCandidate != *after.Candidate {
			t.Fatal("unchanged qualification or draft invalidated recorded Review")
		}
		if err := workflowAdmitReview(*after); err != nil {
			t.Fatalf("unchanged subject lost readiness: %v", err)
		}
		j.bindVerification(t, j.observeRun(t, "unchanged-proof"))
		if err := reviewGateRecordDelivery(t, j.s, j.workID, j.fixture.owner); err != nil {
			t.Fatalf("unchanged recorded Review did not authorize delivery: %v", err)
		}
		reviewGateRequireStep(t, j.s, j.workID, "review")
	})
	t.Run("Review corrective output needs no further Review", func(t *testing.T) {
		j := reviewGateRefineFixture(t, "review-gate-corrective-output", WorkflowStepPlanFix)
		input := currentReviewBlockForTest(t, j.s, j.workID, "continue").InputCandidate
		output := descendantCommit(t, j, "correct-the-candidate")
		completeCurrentReviewForTest(t, j.fixture, "ready_for_final_proof", func(report *ReviewBlock) { report.InputCandidate = input })
		pin := issue1013Pin(t, j.s, j.workID)
		if pin.Review.Candidate.Commit != output || pin.Review.Accepted.Report.OutputCandidate.Commit != output || !pin.Review.DependenciesCurrent {
			t.Fatalf("corrective Review did not bind its output: %+v", pin.Review)
		}
		j.bindVerification(t, j.observeRun(t, "corrective-output-proof"))
		if err := reviewGateRecordDelivery(t, j.s, j.workID, j.fixture.owner); err != nil {
			t.Fatalf("corrective output required another Review: %v", err)
		}
		reviewGateRequireStep(t, j.s, j.workID, "review")
	})
}

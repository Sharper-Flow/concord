package store

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func reviewGateStartStep(t *testing.T, s *Store, workID, stepID, startAction string, actor WorkflowActor) {
	t.Helper()
	entry, err := VerifyWorkflowInstanceDefinition(context.Background(), s, BuiltinWorkflowRegistry(), workID)
	if err != nil {
		t.Fatal(err)
	}
	action := startAction
	if !definitionStepAllows(entry.Definition, stepID, startAction) {
		for candidate, step := range workflowDerivedStartCheckpoints {
			if step == stepID && definitionStepAllows(entry.Definition, stepID, candidate) {
				action = candidate
				break
			}
		}
	}
	if err := runVerdictActionAs(t, s, workID, action, json.RawMessage(`{}`), 0, actor); err != nil {
		t.Fatalf("start %s at %s: %v", action, stepID, err)
	}
	reviewGateStartedEpoch(t, s, workID, action)
}

func reviewGateStartedEpoch(t *testing.T, s *Store, workID, action string) int64 {
	t.Helper()
	var epoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT attempt_epoch FROM workflow_step_epochs WHERE work_id=? AND json_extract(latest_start_payload,'$.action_id')=? ORDER BY latest_start_seq DESC LIMIT 1`, workID, action).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	return epoch
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

func reviewGateRefineFixture(t *testing.T, workID, family string) *candidateJourney {
	t.Helper()
	j := pReturnReviewFixture(t, workID, family, "ready_for_final_proof")
	j.acceptor = j.fixture.owner
	step := currentStep(t, j.s, workID)
	if step == "execution" {
		reviewGateStartStep(t, j.s, workID, step, "start_execution", j.fixture.owner)
	} else {
		reviewGateStartStep(t, j.s, workID, step, "checkpoint_repair", j.fixture.owner)
	}
	j.bindVerification(t, j.observeRun(t, "execution-integration"))
	if err := reviewGateRecordDelivery(t, j.s, workID, j.fixture.owner); err != nil {
		t.Fatalf("deliver reviewed execution: %v", err)
	}
	reviewGateRequireStep(t, j.s, workID, "refine")
	reviewGateStartStep(t, j.s, workID, "refine", "start_refine", j.fixture.owner)
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
	reviewGateRequireStep(t, j.s, j.workID, "refine")
}

func reviewGateExitWithCurrentReview(t *testing.T, j *candidateJourney) {
	t.Helper()
	reviewGateCompleteReview(t, j, "ready_for_final_proof")
	reviewGateRequireStep(t, j.s, j.workID, "refine")
	j.bindVerification(t, j.observeRun(t, "refine-integration"))
	if err := reviewGateRecordDelivery(t, j.s, j.workID, j.fixture.owner); err != nil {
		t.Fatalf("delivery after current Review: %v", err)
	}
	reviewGateRequireStep(t, j.s, j.workID, "delivery")
	pin := issue1013Pin(t, j.s, j.workID)
	if !workPinContainsAction(pin.NextValidIntents, "record_delivery") {
		t.Fatalf("current Review did not restore gate exit: %+v", pin.NextValidIntents)
	}
	if err := reviewGateRecordDelivery(t, j.s, j.workID, j.fixture.owner); err != nil {
		t.Fatalf("gate crossing after current Review: %v", err)
	}
	reviewGateRequireStep(t, j.s, j.workID, "verify")
}

func reviewGateAcceptor(workID string) WorkflowActor {
	return WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/review-gate-acceptor", SessionRef: "session/" + workID + "-acceptor", ActorClass: ActorAgent}
}

func reviewGateChangeRecordedCriterion(t *testing.T, j *candidateJourney) {
	t.Helper()
	successor := pContractSuccessor(t, j.s, j.workID, "changed criterion")
	successor["outcome_predicates"].([]any)[0].(map[string]any)["outcome_payload"].(map[string]any)["expected_result"] = "fail"
	if err := reviseContractRecord(t, j.s, j.workID, j.fixture.owner, successor, readWorkVersion(t, j.s, j.workID), nil); err != nil {
		t.Fatal(err)
	}
}

func TestReviewOfReplacedCandidateCannotAuthorizeDelivery(t *testing.T) {
	j := reviewGateRefineFixture(t, "review-gate-stale-review", "workflow.break_fix")
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

func TestReviewApplicabilityFollowsCandidateNotDispatchOrder(t *testing.T) {
	t.Run("recorded correction blocks changed subject", func(t *testing.T) {
		j := reviewGateRefineFixture(t, "review-gate-before-repair", "workflow.break_fix")
		reviewGateCompleteReview(t, j, "continue")
		reviewGateCompleteReview(t, j, "ready_for_final_proof")
		pReturnProduce(t, j, "repair")
		reviewGateCompleteReview(t, j, "correction_required")
		reviewGateDeliveryRefusal(t, j)
		reviewGateExitWithCurrentReview(t, j)
	})
	t.Run("unchanged qualification and draft keep Review applicable", func(t *testing.T) {
		j := reviewGateRefineFixture(t, "review-gate-unchanged", "workflow.break_fix")
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
		reviewGateRequireStep(t, j.s, j.workID, "delivery")
	})
	t.Run("Review corrective output needs no further Review", func(t *testing.T) {
		j := reviewGateRefineFixture(t, "review-gate-corrective-output", "workflow.break_fix")
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
		reviewGateRequireStep(t, j.s, j.workID, "delivery")
	})
}

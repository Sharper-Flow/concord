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

func TestDeliveryRequiresReviewOfCurrentCandidate(t *testing.T) {
	j := reviewGateRefineFixture(t, "review-gate-break-fix", "workflow.break_fix")
	reviewGateCompleteReview(t, j, "correction_required")
	reviewGateCompleteReview(t, j, "continue")
	pReturnProduce(t, j, "repair")
	before := j.effects(t)
	if err := j.action(t, "request_correction", reviewGateCorrectionPayload("evidence:return-route-verification"), j.fixture.owner); err == nil {
		t.Fatal("refine admitted correction without an applicable correction-required Review")
	}
	if j.effects(t) != before {
		t.Fatal("refused corrective return changed durable state")
	}
	reviewGateCompleteReview(t, j, "correction_required")
	reviewGateDeliveryRefusal(t, j)
	pin := issue1013Pin(t, j.s, j.workID)
	if workPinContainsAction(pin.NextValidIntents, "record_delivery") {
		t.Fatalf("pin offered delivery despite recorded correction: %+v", pin.NextValidIntents)
	}
	reviewGateExitWithCurrentReview(t, j)
}

func TestDeliveryGateDeclaresCorrectiveReturn(t *testing.T) {
	for _, family := range []string{"workflow.implementation", "workflow.break_fix"} {
		entry := mustBuiltinDefinition(t, family)
		gate := workflowStep(entry.Definition, "delivery")
		if !workflowStepIsDeliveryGate(gate) || len(gate.Actions) != 4 || gate.Actions[0] != "record_delivery" || gate.Actions[1] != "request_correction" {
			t.Fatalf("%s gate actions=%v", family, gate.Actions)
		}
		if workflowCorrectionReturnTarget(entry.Definition, "delivery") == "" {
			t.Fatalf("%s gate has no producer return", family)
		}
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

func TestCurrentDeliveryGateAdmitsEvidenceBearingReturn(t *testing.T) {
	for _, tc := range []struct{ family, target string }{{"workflow.break_fix", "repair"}, {"workflow.implementation", "execution"}} {
		t.Run(tc.family, func(t *testing.T) {
			j := reviewGateRefineFixture(t, "current-gate-return", tc.family)
			reviewGateCompleteReview(t, j, "continue")
			pReturnProduce(t, j, "repair")
			actor, err := WorkflowActorRef(j.fixture.owner)
			if err != nil {
				t.Fatal(err)
			}
			if err := advanceWorkflowTestInstanceToStep(t.Context(), j.s, j.workID, "delivery", actor); err != nil {
				t.Fatal(err)
			}
			reviewGateChangeRecordedCriterion(t, j)
			reviewGateRequireStep(t, j.s, j.workID, "delivery")
			current := mustBuiltinDefinition(t, tc.family)
			ref, digest := reviewGateInstancePin(t, j.s, j.workID)
			if ref != current.Definition.Ref || digest != current.Digest {
				t.Fatalf("pinned definition=%s %s, want %s %s", ref, digest, current.Definition.Ref, current.Digest)
			}
			pin := issue1013Pin(t, j.s, j.workID)
			if workPinContainsAction(pin.NextValidIntents, "record_delivery") {
				t.Fatal("stale gate offered delivery")
			}
			reason := ""
			for _, intent := range pin.NextValidIntents {
				if intent.ActionID == "request_correction" {
					reason = intent.ReasonCode
				}
			}
			if reason != "delivery_gate_correction" {
				t.Fatalf("gate return reason=%q", reason)
			}
			deliveries := reviewGateCountDeliveries(t, j.s, j.workID)
			if err := j.action(t, "request_correction", reviewGateCorrectionPayload("evidence:return-route-verification"), j.fixture.owner); err != nil {
				t.Fatalf("evidence-bearing gate return: %v", err)
			}
			reviewGateRequireStep(t, j.s, j.workID, tc.target)
			if count := reviewGateCountDeliveries(t, j.s, j.workID); count != deliveries {
				t.Fatalf("return changed deliveries %d -> %d", deliveries, count)
			}
		})
	}
}

func TestCurrentDeliveryGateReturnsWhenSuccessorStalesAcceptedReview(t *testing.T) {
	for _, family := range []string{"workflow.break_fix", "workflow.implementation"} {
		t.Run(family, func(t *testing.T) {
			j := pReturnReviewFixture(t, "current-gate-return", family, "ready_for_final_proof")
			owner, err := WorkflowActorRef(j.fixture.owner)
			if err != nil {
				t.Fatal(err)
			}
			if err := advanceWorkflowTestInstanceToStep(t.Context(), j.s, j.workID, "delivery", owner); err != nil {
				t.Fatal(err)
			}
			successor := pContractSuccessor(t, j.s, j.workID, "changed criterion")
			successor["outcome_predicates"].([]any)[0].(map[string]any)["outcome_payload"].(map[string]any)["expected_result"] = "fail"
			if err := reviseContractRecord(t, j.s, j.workID, j.fixture.owner, successor, readWorkVersion(t, j.s, j.workID), nil); err != nil {
				t.Fatal(err)
			}
			pin := issue1013Pin(t, j.s, j.workID)
			if pin.Review == nil || pin.Review.DependenciesCurrent || issue1013HasIntent(pin, "record_delivery") {
				t.Fatalf("stale gate offered delivery: %+v", pin.NextValidIntents)
			}
			if !issue1013HasIntent(pin, "request_correction") {
				t.Fatalf("stale gate hides corrective return: %+v", pin.NextValidIntents)
			}
			reason := ""
			for _, intent := range pin.NextValidIntents {
				if intent.ActionID == "request_correction" {
					reason = intent.ReasonCode
				}
			}
			if reason != "delivery_gate_correction" {
				t.Fatalf("gate return reason=%q", reason)
			}
			deliveries := reviewGateCountDeliveries(t, j.s, j.workID)
			if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner); err != nil {
				t.Fatalf("evidence-bearing current gate return: %v", err)
			}
			effect := "execution"
			if family == "workflow.break_fix" {
				effect = "repair"
			}
			reviewGateRequireStep(t, j.s, j.workID, effect)
			if count := reviewGateCountDeliveries(t, j.s, j.workID); count != deliveries {
				t.Fatalf("return changed deliveries %d -> %d", deliveries, count)
			}
		})
	}
}

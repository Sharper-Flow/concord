package store

import (
	"encoding/json"
	"reflect"
	"testing"
)

func contractReviewAcceptance(t *testing.T, id, family string) *candidateJourney {
	t.Helper()
	producer, evaluator := "execution", "acceptance"
	if family == "workflow.break_fix" {
		producer, evaluator = "repair", "verify"
	}
	f := seedCurrentWorkflowReturnRouteFixture(t, id, family, producer)
	t.Cleanup(func() { _ = f.store.Close() })
	w := workContextFixture{store: f.store, workID: id, owner: f.owner, operator: f.operator}
	a := qAssociateCurrentProofAt(t, w, evaluator, "", true)
	return &candidateJourney{fixture: w, s: f.store, workID: id, repo: a.repo, acceptor: f.owner}
}

func TestForwardConfirmationRequiresReadyReview(t *testing.T) {
	for _, family := range []string{"workflow.implementation", "workflow.break_fix"} {
		for _, disposition := range []string{"continue", "revision_required", "correction_required"} {
			t.Run(family+"/"+disposition, func(t *testing.T) {
				j := contractReviewAcceptance(t, "forward-review-"+disposition, family)
				report := issue1013Pin(t, j.s, j.workID).Review.Accepted.Report
				report.Disposition = disposition
				if err := recordReviewForTest(t, j.s, j.workID, j.fixture.owner, &report); err != nil {
					t.Fatal(err)
				}
				before := outsideRepairDatabaseSnapshot(t, j.s)
				if issue1013HasIntent(issue1013Pin(t, j.s, j.workID), "confirm_premise") {
					t.Error("pin advertised non-ready forward confirmation")
				}
				if err := issue1013Preflight(t, j.s, j.workID, "confirm_premise", json.RawMessage(`{}`), j.fixture.owner); !hasFailureKind(err, KindMissingEvidence) {
					t.Errorf("non-ready forward confirmation preflight=%v, want missing_evidence", err)
				}
				if err := pConfirmReceiptPremise(t, j.fixture); !hasFailureKind(err, KindMissingEvidence) {
					t.Errorf("non-ready forward confirmation=%v, want missing_evidence", err)
				}
				if before != outsideRepairDatabaseSnapshot(t, j.s) {
					t.Error("refused confirmation changed durable state")
				}
				if issue1013HasIntent(issue1013Pin(t, j.s, j.workID), "confirm_premise") {
					t.Error("pin advertised non-ready forward confirmation")
				}
				entry := mustBuiltinDefinition(t, family)
				if _, err := runEngineeringConfirmation(t, j.s, entry.Definition, j.workID, ownerRefForTest(t, j.fixture.owner), nil); !hasFailureKind(err, KindMissingEvidence) {
					t.Errorf("constructor admitted non-ready forward confirmation: %v", err)
				}
			})
		}
	}
}

func TestTerminalContractUpdateReturnsForCurrentReview(t *testing.T) {
	for _, family := range []string{"workflow.implementation", "workflow.break_fix"} {
		t.Run(family, func(t *testing.T) {
			j := contractReviewAcceptance(t, "terminal-review-update", family)
			if err := pConfirmReceiptPremise(t, j.fixture); err != nil {
				t.Fatal(err)
			}
			terminal := currentStep(t, j.s, j.workID)
			beforeReview := issue1013Pin(t, j.s, j.workID).Review.Accepted
			unchanged := pContractSuccessor(t, j.s, j.workID, "same proof, clarified premise")
			if err := reviseContractRecord(t, j.s, j.workID, j.fixture.owner, unchanged, readWorkVersion(t, j.s, j.workID), nil); err != nil {
				t.Fatal(err)
			}
			pin := issue1013Pin(t, j.s, j.workID)
			if !pin.Review.DependenciesCurrent || !reflect.DeepEqual(beforeReview, pin.Review.Accepted) || issue1013HasIntent(pin, "request_correction") {
				t.Fatal("unchanged update invalidated Review or invented a corrective return")
			}
			changed := pContractSuccessor(t, j.s, j.workID, "changed proof obligation")
			changed["outcome_predicates"].([]any)[0].(map[string]any)["outcome_payload"].(map[string]any)["check_ref"] = "check:changed-obligation"
			if err := reviseContractRecord(t, j.s, j.workID, j.fixture.owner, changed, readWorkVersion(t, j.s, j.workID), nil); err != nil {
				t.Fatal(err)
			}
			reviewGateRequireStep(t, j.s, j.workID, terminal)
			pin = issue1013Pin(t, j.s, j.workID)
			if pin.Review.DependenciesCurrent || !reflect.DeepEqual(beforeReview, pin.Review.Accepted) || issue1013HasIntent(pin, "complete") || issue1013HasIntent(pin, "record_review") {
				t.Fatal("changed update erased old Review or certified stale proof")
			}
			before := outsideRepairDatabaseSnapshot(t, j.s)
			if err := j.action(t, "complete", map[string]any{"impact_verdict": "non-breaking"}, j.fixture.owner); !hasFailureKind(err, KindStaleRequiresReview) {
				t.Fatalf("stale Review completion=%v", err)
			}
			if err := runVerdictActionAs(t, j.s, j.workID, "record_verdict", criterionAssociationPayload(beforeReview.Report.Assessments[0].ReceiptRefs[0], "predicate:return-route"), 0, j.fixture.owner); !hasFailureKind(err, KindMissingEvidence) {
				t.Fatalf("old proof certified changed criterion=%v", err)
			}
			if err := j.action(t, "request_correction", map[string]any{"diagnosis": "stale Review", "strategy": "fresh Review", "predicate_ids": []string{"predicate:unknown"}, "evidence_refs": []string{"evidence:return-route-verification"}}, j.fixture.owner); err == nil {
				t.Fatal("invalid correction accepted")
			}
			if before != outsideRepairDatabaseSnapshot(t, j.s) {
				t.Fatal("refused correction changed durable state")
			}
			if !issue1013HasIntent(pin, "request_correction") {
				t.Fatal("proof-changing terminal update stranded work: pin has no producer return")
			}
			if err := issue1013Preflight(t, j.s, j.workID, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner); err != nil {
				t.Fatal(err)
			}
			if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner); err != nil {
				t.Fatal(err)
			}
			producer := "execution"
			if family == "workflow.break_fix" {
				producer = "repair"
			}
			reviewGateRequireStep(t, j.s, j.workID, producer)
			entry := mustBuiltinDefinition(t, family)
			correction, missing, err := workflowCorrectionRequestAdmissionForRegistry(t.Context(), j.s.db, j.workID, entry.Definition, terminal, "test", 0, BuiltinWorkflowRegistry())
			if err != nil || correction != nil || missing != workflowCorrectionMissingFreshReview {
				t.Fatalf("same accepted report can fund another terminal return: %+v %s %v", correction, missing, err)
			}
			loaded := issue1013Pin(t, j.s, j.workID).Review
			gate, err := workflowDeliveryGateCorrectionContextFolded(t.Context(), j.s.db, j.workID, entry.Definition, "delivery", "test", WorkflowAdmissionState{Review: loaded})
			if err != nil || gate != nil {
				t.Errorf("consumed report still funds a delivery-gate return: %+v %v", gate, err)
			}
			before = outsideRepairDatabaseSnapshot(t, j.s)
			if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner); !hasFailureKind(err, KindInvalidOperation) {
				t.Fatalf("consumed return=%v", err)
			}
			if before != outsideRepairDatabaseSnapshot(t, j.s) || issue1013HasIntent(issue1013Pin(t, j.s, j.workID), "request_correction") {
				t.Fatal("consumed return wrote state or remained advertised")
			}
			contractReviewFinish(t, j, producer)
		})
	}
}

func contractReviewFinish(t *testing.T, j *candidateJourney, producer string) {
	t.Helper()
	start := "start_execution"
	if producer == "repair" {
		start = "checkpoint_repair"
	}
	reviewGateStartStep(t, j.s, j.workID, producer, start, j.fixture.owner)
	proof := j.observeRun(t, "fresh-contract-proof")
	j.bindVerification(t, proof)
	input := *issue1013Pin(t, j.s, j.workID).Review.Candidate
	completeCurrentReviewForTest(t, j.fixture, "ready_for_final_proof", func(report *ReviewBlock) {
		report.InputCandidate = input
		for _, criterion := range report.Dependencies.Criteria {
			report.Assessments = append(report.Assessments, ReviewAssessment{PredicateID: criterion.PredicateID, Assessment: "supported", ReceiptRefs: []string{proof.OperationRef}})
		}
	})
	if err := reviewGateRecordDelivery(t, j.s, j.workID, j.fixture.owner); err != nil {
		t.Fatal(err)
	}
	reviewGateRequireStep(t, j.s, j.workID, "refine")
	reviewGateStartStep(t, j.s, j.workID, "refine", "start_refine", j.fixture.owner)
	j.bindVerification(t, proof)
	for range 2 {
		if err := reviewGateRecordDelivery(t, j.s, j.workID, j.fixture.owner); err != nil {
			t.Fatal(err)
		}
	}
	if err := runVerdictActionAs(t, j.s, j.workID, "record_verdict", criterionAssociationPayload(proof.OperationRef, "predicate:return-route"), 0, j.fixture.owner); err != nil {
		t.Fatal(err)
	}
	if err := pConfirmReceiptPremise(t, j.fixture); err != nil {
		t.Fatal(err)
	}
	bindCompletionClosure(t, j.s, j.workID, j.fixture.owner)
	if err := runVerdictActionAs(t, j.s, j.workID, "complete", json.RawMessage(`{"impact_verdict":"non-breaking"}`), 0, diagnosisS1Actor(j.workID, "fresh-completer")); err != nil {
		t.Fatalf("completion after producer return and fresh current proof: %v", err)
	}
}

func TestTerminalCandidateChangeReturnsForCurrentReview(t *testing.T) {
	for _, family := range []string{"workflow.implementation", "workflow.break_fix"} {
		t.Run(family, func(t *testing.T) {
			j := contractReviewAcceptance(t, "terminal-candidate-change", family)
			if err := pConfirmReceiptPremise(t, j.fixture); err != nil {
				t.Fatal(err)
			}
			descendantCommit(t, j, "changed-final-candidate")
			contract := pContractSuccessor(t, j.s, j.workID, "verify the changed candidate")
			contract["outcome_predicates"].([]any)[0].(map[string]any)["outcome_payload"].(map[string]any)["immutable_subject_ref"] = "commit:" + j.head(t)
			if err := reviseContractRecord(t, j.s, j.workID, j.fixture.owner, contract, readWorkVersion(t, j.s, j.workID), nil); err != nil {
				t.Fatal(err)
			}
			j.bindVerification(t, j.observeRun(t, "changed-final-proof"))
			pin := issue1013Pin(t, j.s, j.workID)
			if pin.Review.DependenciesCurrent || !issue1013HasIntent(pin, "request_correction") || issue1013HasIntent(pin, "complete") {
				t.Fatal("changed candidate did not close completion and open the producer return")
			}
			if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner); err != nil {
				t.Fatal(err)
			}
			producer := "execution"
			if family == "workflow.break_fix" {
				producer = "repair"
			}
			contractReviewFinish(t, j, producer)
		})
	}
}

func TestForwardReviewReadinessPreservesUnhealthyFailureReturn(t *testing.T) {
	for _, family := range []string{"workflow.implementation", "workflow.break_fix"} {
		t.Run(family, func(t *testing.T) {
			step := "acceptance"
			if family == "workflow.break_fix" {
				step = "verify"
			}
			const id = "unhealthy-forward-review"
			fixture := seedCurrentWorkflowReturnRouteFixture(t, id, family, step)
			s := fixture.store
			t.Cleanup(func() { _ = s.Close() })
			var bad map[string]any
			if err := json.Unmarshal([]byte(stalenessVerdictPayload("bad")), &bad); err != nil {
				t.Fatal(err)
			}
			bad["predicate_id"] = "predicate:return-route"
			stalenessEvent(t, s, id, WorkflowVerdictRecorded, string(mustJSONValue(bad)))
			definition := mustBuiltinDefinition(t, family).Definition
			before := outsideRepairDatabaseSnapshot(t, s)
			if err := workflowForwardConfirmationReview(t.Context(), s.db, id, definition, step, &WorkflowReviewState{}); err != nil {
				t.Fatalf("forward Review readiness blocked an unhealthy failure return: %v", err)
			}
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			target, err := workflowNextStepForAction(t.Context(), tx, id, definition, step, "confirm_premise", BuiltinWorkflowRegistry(), 0)
			_ = tx.Rollback()
			if err != nil || target != "refine" {
				t.Fatalf("unhealthy confirmation target=%q, %v", target, err)
			}
			if before != outsideRepairDatabaseSnapshot(t, s) {
				t.Fatal("readiness check changed durable state")
			}
		})
	}
}

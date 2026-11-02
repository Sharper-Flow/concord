package store

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func reviewConsumptionUnrelatedReturn(t *testing.T, id string) *candidateJourney {
	t.Helper()
	j := contractReviewAcceptance(t, id, "workflow.implementation")
	before := issue1013Pin(t, j.s, j.workID).Review.Accepted
	var bad map[string]any
	if err := json.Unmarshal([]byte(stalenessVerdictPayload("bad")), &bad); err != nil {
		t.Fatal(err)
	}
	bad["predicate_id"] = "predicate:return-route"
	stalenessEvent(t, j.s, j.workID, WorkflowVerdictRecorded, string(mustJSONValue(bad)))
	if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner); err != nil {
		t.Fatalf("unhealthy-verdict return: %v", err)
	}
	reviewGateRequireStep(t, j.s, j.workID, "execution")
	review := issue1013Pin(t, j.s, j.workID).Review
	if !reflect.DeepEqual(before, review.Accepted) || !review.DependenciesCurrent || review.Accepted.Report.OutputCandidate != *review.Candidate {
		t.Fatal("unrelated verdict return changed the ready Review or its dependencies")
	}
	return j
}

func reviewConsumptionResumeToGate(t *testing.T, j *candidateJourney) candidateReport {
	t.Helper()
	reviewGateStartStep(t, j.s, j.workID, "execution", "start_execution", j.fixture.owner)
	proof := j.observeRun(t, "rerun-same-candidate")
	j.bindVerification(t, proof)
	if err := reviewGateRecordDelivery(t, j.s, j.workID, j.fixture.owner); err != nil {
		t.Fatalf("delivery with the surviving ready Review: %v", err)
	}
	reviewGateStartStep(t, j.s, j.workID, "refine", "start_refine", j.fixture.owner)
	j.bindVerification(t, proof)
	if err := reviewGateRecordDelivery(t, j.s, j.workID, j.fixture.owner); err != nil {
		t.Fatal(err)
	}
	reviewGateRequireStep(t, j.s, j.workID, "delivery")
	return proof
}

func reviewConsumptionChangeCriterion(t *testing.T, j *candidateJourney, check string) {
	t.Helper()
	changed := pContractSuccessor(t, j.s, j.workID, "changed proof obligation")
	changed["outcome_predicates"].([]any)[0].(map[string]any)["outcome_payload"].(map[string]any)["check_ref"] = check
	if err := reviseContractRecord(t, j.s, j.workID, j.fixture.owner, changed, readWorkVersion(t, j.s, j.workID), nil); err != nil {
		t.Fatal(err)
	}
}

func reviewConsumptionRequireReturn(t *testing.T, j *candidateJourney, step string) {
	t.Helper()
	reviewGateRequireStep(t, j.s, j.workID, step)
	pin := issue1013Pin(t, j.s, j.workID)
	if pin.Review.DependenciesCurrent {
		t.Fatal("changed criterion kept Review current")
	}
	if !issue1013HasIntent(pin, "request_correction") {
		t.Fatalf("%s update stranded work after an unrelated verdict return: %v", step, j.action(t, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner))
	}
	if err := issue1013Preflight(t, j.s, j.workID, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner); err != nil {
		t.Fatal(err)
	}
	if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner); err != nil {
		t.Fatal(err)
	}
	reviewGateRequireStep(t, j.s, j.workID, "execution")
	contractReviewFinish(t, j, "execution")
}

func TestUnrelatedVerdictReturnPreservesReviewForTerminalUpdate(t *testing.T) {
	j := reviewConsumptionUnrelatedReturn(t, "unrelated-terminal-review")
	proof := reviewConsumptionResumeToGate(t, j)
	if err := reviewGateRecordDelivery(t, j.s, j.workID, j.fixture.owner); err != nil {
		t.Fatal(err)
	}
	reviewGateRequireStep(t, j.s, j.workID, "acceptance")
	if err := runVerdictActionAs(t, j.s, j.workID, "record_verdict", criterionAssociationPayload(proof.OperationRef, "predicate:return-route"), 0, j.fixture.owner); err != nil {
		t.Fatal(err)
	}
	if err := pConfirmReceiptPremise(t, j.fixture); err != nil {
		t.Fatal(err)
	}
	reviewGateRequireStep(t, j.s, j.workID, "release")
	reviewConsumptionChangeCriterion(t, j, "check:changed-terminal-obligation")
	reviewConsumptionRequireReturn(t, j, "release")
}

func TestConsumedReviewCannotRegainAuthorityAfterCriterionRestoration(t *testing.T) {
	j := contractReviewAcceptance(t, "restored-consumed-review", "workflow.implementation")
	if err := pConfirmReceiptPremise(t, j.fixture); err != nil {
		t.Fatal(err)
	}
	reviewGateRequireStep(t, j.s, j.workID, "release")
	originalContract := pContractSuccessor(t, j.s, j.workID, "original proof obligation")
	original := originalContract["outcome_predicates"].([]any)[0].(map[string]any)["outcome_payload"].(map[string]any)["check_ref"].(string)
	accepted := issue1013Pin(t, j.s, j.workID).Review.Accepted
	reviewConsumptionChangeCriterion(t, j, "check:changed-obligation")
	if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner); err != nil {
		t.Fatal(err)
	}
	reviewGateRequireStep(t, j.s, j.workID, "execution")
	reviewConsumptionChangeCriterion(t, j, original)
	review := issue1013Pin(t, j.s, j.workID).Review
	if !reflect.DeepEqual(accepted, review.Accepted) || !review.DependenciesCurrent || review.Accepted.Report.OutputCandidate != *review.Candidate {
		t.Fatal("restoration did not reproduce the consumed Review's original dependencies and candidate")
	}
	reviewGateStartStep(t, j.s, j.workID, "execution", "start_execution", j.fixture.owner)
	proof := j.observeRun(t, "rerun-restored-candidate")
	j.bindVerification(t, proof)
	before := outsideRepairDatabaseSnapshot(t, j.s)
	definition := mustBuiltinDefinition(t, "workflow.implementation").Definition
	if err := workflowForwardConfirmationReview(t.Context(), j.s.db, j.workID, definition, "acceptance", review); err == nil {
		t.Error("restored dependencies let the consumed Review authorize forward confirmation")
	}
	if issue1013HasIntent(issue1013Pin(t, j.s, j.workID), "record_delivery") {
		t.Error("pin advertised delivery funded by a consumed Review")
	}
	payload := json.RawMessage(`{"delivery_artifact":"artifact:review-gate-` + j.workID + `","delivery_state":"asserted"}`)
	if err := issue1013Preflight(t, j.s, j.workID, "record_delivery", payload, j.fixture.owner); err == nil {
		t.Error("preflight admitted delivery funded by a consumed Review")
	}
	if err := reviewGateRecordDelivery(t, j.s, j.workID, j.fixture.owner); err == nil {
		t.Fatal("restored dependencies let the consumed Review authorize delivery")
	}
	if before != outsideRepairDatabaseSnapshot(t, j.s) {
		t.Fatal("refused consumed-Review delivery changed durable state")
	}
	contractReviewFinish(t, j, "execution")
}

func TestUnrelatedVerdictReturnPreservesReviewForGateUpdate(t *testing.T) {
	j := reviewConsumptionUnrelatedReturn(t, "unrelated-gate-review")
	reviewConsumptionResumeToGate(t, j)
	reviewConsumptionChangeCriterion(t, j, "check:changed-gate-obligation")
	reviewConsumptionRequireReturn(t, j, "delivery")
}

func reviewConsumptionFunding(t *testing.T, j *candidateJourney) map[string]string {
	t.Helper()
	var raw string
	if err := j.s.db.QueryRow(`SELECT state FROM workflow_review_state WHERE work_id=?`, j.workID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var facts struct {
		Funding map[string]string `json:"correction_funding"`
	}
	if err := json.Unmarshal([]byte(raw), &facts); err != nil {
		t.Fatal(err)
	}
	return facts.Funding
}

func reviewConsumptionRequireExactFunding(t *testing.T, j *candidateJourney, reportRef string) {
	t.Helper()
	correction, err := readWorkflowCorrectionContext(t.Context(), j.s.db, j.workID)
	if err != nil || correction == nil {
		t.Fatalf("recorded correction=%+v: %v", correction, err)
	}
	want := map[string]string{"report_ref": reportRef, "correction_ref": correction.SourceEventID}
	if got := reviewConsumptionFunding(t, j); !reflect.DeepEqual(got, want) {
		t.Fatalf("funding=%v, want exact report/return binding %v", got, want)
	}
}

func TestUnboundReviewIgnoresLaterCorrectionPositions(t *testing.T) {
	for _, family := range []string{"workflow.implementation", "workflow.break_fix"} {
		for _, source := range []string{"verdict", "ambiguous"} {
			t.Run(family+"/"+source, func(t *testing.T) {
				j := contractReviewAcceptance(t, "unbound-review-"+source, family)
				if err := pConfirmReceiptPremise(t, j.fixture); err != nil {
					t.Fatal(err)
				}
				reportRef := issue1013Pin(t, j.s, j.workID).Review.Accepted.ReportRef
				if source == "verdict" {
					var bad map[string]any
					if err := json.Unmarshal([]byte(stalenessVerdictPayload("bad")), &bad); err != nil {
						t.Fatal(err)
					}
					bad["predicate_id"] = "predicate:return-route"
					bad["verdict_actor_ref"] = ownerRefForTest(t, j.fixture.owner)
					stalenessEvent(t, j.s, j.workID, WorkflowVerdictRecorded, string(mustJSONValue(bad)))
				} else {
					reviewConsumptionChangeCriterion(t, j, "check:ambiguous-old-return")
				}
				if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner); err != nil {
					t.Fatal(err)
				}
				// Old JSON carries the accepted report and later correction facts,
				// but no exact funding link. Its historical cause is not inferred.
				if _, err := j.s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_review_state SET state=json_remove(state,'$.correction_funding') WHERE work_id=?; DELETE FROM write_guard`, j.workID); err != nil {
					t.Fatal(err)
				}
				reviewConsumptionChangeCriterion(t, j, "check:next-old-report-return")
				before := outsideRepairDatabaseSnapshot(t, j.s)
				invalid := map[string]any{"diagnosis": "invalid correction", "strategy": "fresh Review", "predicate_ids": []string{"predicate:unknown"}, "evidence_refs": []string{"evidence:return-route-verification"}}
				if err := j.action(t, "request_correction", invalid, j.fixture.owner); err == nil {
					t.Fatal("invalid correction accepted")
				}
				if before != outsideRepairDatabaseSnapshot(t, j.s) {
					t.Fatal("invalid correction consumed an unbound report or changed durable state")
				}
				if err := issue1013Preflight(t, j.s, j.workID, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner); err != nil {
					t.Fatalf("unbound old Review denied by positional history: %v", err)
				}
				if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner); err != nil {
					t.Fatal(err)
				}
				reviewConsumptionRequireExactFunding(t, j, reportRef)
				before = outsideRepairDatabaseSnapshot(t, j.s)
				if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner); !hasFailureKind(err, KindInvalidOperation) {
					t.Fatalf("repeat report use=%v", err)
				}
				if before != outsideRepairDatabaseSnapshot(t, j.s) {
					t.Fatal("repeat report use changed durable state")
				}
			})
		}
	}
}

func TestReviewFundingPersistsThroughDischargeUntilFreshReview(t *testing.T) {
	for _, family := range []string{"workflow.implementation", "workflow.break_fix"} {
		t.Run(family, func(t *testing.T) {
			j := contractReviewAcceptance(t, "funding-retention", family)
			if err := pConfirmReceiptPremise(t, j.fixture); err != nil {
				t.Fatal(err)
			}
			reportRef := issue1013Pin(t, j.s, j.workID).Review.Accepted.ReportRef
			original := pContractSuccessor(t, j.s, j.workID, "original criterion")["outcome_predicates"].([]any)[0].(map[string]any)["outcome_payload"].(map[string]any)["check_ref"].(string)
			reviewConsumptionChangeCriterion(t, j, "check:funded-return")
			if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner); err != nil {
				t.Fatal(err)
			}
			reviewConsumptionRequireExactFunding(t, j, reportRef)
			funding := reviewConsumptionFunding(t, j)
			// Exercise the correction-discharge fold independently of admission:
			// discharge must not erase the accepted report's funding fact.
			tx, err := j.s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
				t.Fatal(err)
			}
			if err := advanceWorkflowCorrectionFactsTx(t.Context(), tx, Event{SubjectID: j.workID, Kind: WorkflowActionCompleted, Payload: json.RawMessage(`{"action_id":"record_finding"}`)}); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(`DELETE FROM write_guard`); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if correction, err := readWorkflowCorrectionContext(t.Context(), j.s.db, j.workID); err != nil || correction != nil {
				t.Fatalf("discharged correction=%+v: %v", correction, err)
			}
			if got := reviewConsumptionFunding(t, j); !reflect.DeepEqual(got, funding) {
				t.Fatalf("discharge lost funding: %v, want %v", got, funding)
			}
			reviewConsumptionChangeCriterion(t, j, original)
			review := issue1013Pin(t, j.s, j.workID).Review
			if !review.DependenciesCurrent || !hasFailureKind(workflowAdmitReview(*review), KindMissingEvidence) {
				t.Fatal("restored criterion did not retain consumed-Review refusal")
			}
			public, err := json.Marshal(review)
			if err != nil {
				t.Fatal(err)
			}
			var view map[string]any
			if err := json.Unmarshal(public, &view); err != nil {
				t.Fatal(err)
			}
			if _, present := view["correction_funding"]; present {
				t.Fatal("private funding fact leaked into the public Review view")
			}
			definition := mustBuiltinDefinition(t, family).Definition
			evaluator := "acceptance"
			if family == "workflow.break_fix" {
				evaluator = "verify"
			}
			if err := workflowForwardConfirmationReview(t.Context(), j.s.db, j.workID, definition, evaluator, review); !hasFailureKind(err, KindMissingEvidence) {
				t.Fatalf("consumed Review forward confirmation=%v", err)
			}
			var bad map[string]any
			if err := json.Unmarshal([]byte(stalenessVerdictPayload("bad")), &bad); err != nil {
				t.Fatal(err)
			}
			version, err := activeWorkflowContractVersion(t.Context(), j.s.db, j.workID, "test")
			if err != nil {
				t.Fatal(err)
			}
			bad["contract_version"] = version
			bad["predicate_id"], bad["verdict_actor_ref"] = "predicate:return-route", ownerRefForTest(t, j.fixture.owner)
			stalenessEvent(t, j.s, j.workID, WorkflowVerdictRecorded, string(mustJSONValue(bad)))
			if err := workflowForwardConfirmationReview(t.Context(), j.s.db, j.workID, definition, evaluator, review); err != nil {
				t.Fatalf("consumed Review blocked an unhealthy failure-edge return: %v", err)
			}
			producer := "execution"
			if family == "workflow.break_fix" {
				producer = "repair"
			}
			contractReviewFinish(t, j, producer)
			if got := reviewConsumptionFunding(t, j); got != nil {
				t.Fatalf("fresh accepted Review inherited funding: %v", got)
			}
		})
	}
}

func TestReviewFundingRollsBackWithFailedCompletion(t *testing.T) {
	j := contractReviewAcceptance(t, "funding-rollback", "workflow.implementation")
	reviewConsumptionChangeCriterion(t, j, "check:rollback-obligation")
	if _, err := j.s.db.Exec(`CREATE TRIGGER reject_correction_exit BEFORE INSERT ON workflow_completed_exits WHEN NEW.action_id='request_correction' BEGIN SELECT RAISE(ABORT,'forced correction persistence failure'); END`); err != nil {
		t.Fatal(err)
	}
	before := outsideRepairDatabaseSnapshot(t, j.s)
	err := j.action(t, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner)
	if err == nil || !strings.Contains(err.Error(), "forced correction persistence failure") {
		t.Fatalf("late correction persistence failure=%v", err)
	}
	if before != outsideRepairDatabaseSnapshot(t, j.s) || reviewConsumptionFunding(t, j) != nil {
		t.Fatal("failed correction completion retained funding or changed durable state")
	}
	if _, err := j.s.db.Exec(`DROP TRIGGER reject_correction_exit`); err != nil {
		t.Fatal(err)
	}
	reportRef := issue1013Pin(t, j.s, j.workID).Review.Accepted.ReportRef
	if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner); err != nil {
		t.Fatal(err)
	}
	reviewConsumptionRequireExactFunding(t, j, reportRef)
}

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

func TestTerminalContractUpdateKeepsProgressForCurrentReview(t *testing.T) {
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
			if pin.Review.DependenciesCurrent || !reflect.DeepEqual(beforeReview, pin.Review.Accepted) || !issue1013HasIntent(pin, "record_review") {
				t.Fatal("changed update erased old Review or certified stale proof")
			}
			before := outsideRepairDatabaseSnapshot(t, j.s)
			if err := runVerdictActionAs(t, j.s, j.workID, "record_verdict", criterionAssociationPayload(beforeReview.Report.Assessments[0].ReceiptRefs[0], "predicate:return-route"), 0, j.fixture.owner); !hasFailureKind(err, KindMissingEvidence) {
				t.Fatalf("old proof certified changed criterion=%v", err)
			}
			if before != outsideRepairDatabaseSnapshot(t, j.s) {
				t.Fatal("refused stale proof changed durable state")
			}
			contractReviewFinish(t, j, terminal)
		})
	}
}

func contractReviewFinish(t *testing.T, j *candidateJourney, step string) {
	t.Helper()
	old, seq, err := latestVerificationReport(t.Context(), j.s.db, j.workID, "")
	if err != nil || old == nil {
		t.Fatalf("read previous report: %v", err)
	}
	before := outsideRepairDatabaseSnapshot(t, j.s)
	var oldRef string
	if err := j.s.db.QueryRowContext(t.Context(), `SELECT json_extract(payload,'$.immutable_subject_ref') FROM workflow_evidence_bindings WHERE work_id=? AND binding_seq=?`, j.workID, seq).Scan(&oldRef); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{oldRef, "report:relabelled-old"} {
		oldPayload := mustJSONValue(map[string]any{"evidence_kind": "verification", "immutable_subject_ref": ref, "verification_report": old})
		if err := runVerdictActionAs(t, j.s, j.workID, "bind_evidence", oldPayload, 0, j.fixture.owner); !hasFailureKind(err, KindIllegalLifecycleTransition) {
			t.Fatalf("old report binding at %s=%v", ref, err)
		}
	}
	if before != outsideRepairDatabaseSnapshot(t, j.s) {
		t.Fatal("refused old report changed durable state")
	}
	proof := j.observeRun(t, "fresh-contract-proof")
	proof.OperationRef = "report:fresh-contract-proof"
	proof.SubjectRef = proof.OperationRef
	stalePayload := mustJSONValue(map[string]any{"evidence_kind": "verification", "immutable_subject_ref": proof.SubjectRef, "verification_report": proof.Report})
	if err := runVerdictActionAs(t, j.s, j.workID, "bind_evidence", stalePayload, readWorkVersion(t, j.s, j.workID)-1, j.fixture.owner); !hasFailureKind(err, KindVersionConflict) {
		t.Fatalf("stale-version report binding=%v", err)
	}
	if before != outsideRepairDatabaseSnapshot(t, j.s) {
		t.Fatal("refused stale-version report changed durable state")
	}
	if !issue1013HasIntent(issue1013Pin(t, j.s, j.workID), "bind_evidence") {
		t.Fatal("current work pin omitted ordinary evidence binding")
	}
	if err := issue1013Preflight(t, j.s, j.workID, "bind_evidence", stalePayload, j.fixture.owner); err != nil {
		t.Fatalf("fresh report binding preflight: %v", err)
	}
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
	reviewGateRequireStep(t, j.s, j.workID, step)
	if err := runVerdictActionAs(t, j.s, j.workID, "record_verdict", criterionAssociationPayload(proof.OperationRef, "predicate:return-route"), 0, j.fixture.owner); err != nil {
		t.Fatal(err)
	}
	if err := runVerdictActionAs(t, j.s, j.workID, "complete", json.RawMessage(`{"impact_verdict":"non-breaking"}`), 0, diagnosisS1Actor(j.workID, "fresh-completer")); err != nil {
		t.Fatalf("completion after ordinary writes and fresh current proof: %v", err)
	}
}

func TestTerminalCandidateChangeKeepsProgressForCurrentReview(t *testing.T) {
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
			proof := j.observeRun(t, "changed-final-proof")
			proof.OperationRef = "report:changed-final-proof"
			proof.SubjectRef = proof.OperationRef
			j.bindVerification(t, proof)
			pin := issue1013Pin(t, j.s, j.workID)
			if pin.Review.DependenciesCurrent || !issue1013HasIntent(pin, "record_review") {
				t.Fatal("changed candidate did not stale Review and retain the ordinary Review write")
			}
			contractReviewFinish(t, j, pin.Step)
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

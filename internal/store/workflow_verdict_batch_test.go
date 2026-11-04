package store

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func pReceiptFixture(t *testing.T, workID string, criteria, receiptsPerCriterion int, beforeProduction ...func(workContextFixture)) (workContextFixture, string, [][]string) {
	t.Helper()
	var predicates []map[string]any
	for i := 0; i < criteria; i++ {
		predicates = append(predicates, map[string]any{"predicate_id": fmt.Sprintf("predicate:p%d", i), "ordinal": i, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": fmt.Sprintf("check:p%d", i), "immutable_subject_ref": "commit:fixture", "expected_result": "pass"}})
	}
	route := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanChange, "implement", []string{}, []string{}, predicates...)
	f := workContextFixture{store: route.store, workID: workID, owner: route.owner, operator: route.operator}
	t.Cleanup(func() { _ = f.store.Close() })
	for _, setup := range beforeProduction {
		setup(f)
	}
	repo, _ := reviewSubjectClaim(t, f.store, workID)
	j := &candidateJourney{fixture: f, s: f.store, workID: workID, repo: repo}
	dependencies, err := workflowReviewDependencies(t.Context(), f.store.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	if !dependencies.ArchitectureNotApplicable {
		acceptCurrentReviewForTest(t, f, "continue")
	}
	conformanceImplementChange(t, j, "criterion-proof")
	proof := j.observeRun(t, "criterion-proof")
	refs := make([][]string, criteria)
	for i := range refs {
		for n := 0; n < receiptsPerCriterion; n++ {
			report := proof
			report.SubjectRef = fmt.Sprintf("report:%s-%d-%d", workID, i, n)
			report.Report.RunRef = fmt.Sprintf("run:%s-%d-%d", workID, i, n)
			j.bindVerification(t, report)
			refs[i] = append(refs[i], report.SubjectRef)
		}
	}
	pAcceptReceiptReview(t, f, refs)
	if err := runVerdictActionAs(t, f.store, workID, "bind_evidence", mustJSONValue(map[string]any{"evidence_kind": "artifact", "immutable_subject_ref": "commit:" + runTestGit(t, repo, "rev-parse", "HEAD")}), 0, f.owner); err != nil {
		t.Fatalf("bind the checked candidate artifact: %v", err)
	}
	moveWorkflowPlanFixtureFrontier(t, f.store, workID, "review")
	return f, repo, refs
}

func pAcceptReceiptReview(t *testing.T, f workContextFixture, refs [][]string) {
	t.Helper()
	completeCurrentReviewForTest(t, f, "ready_for_final_proof", func(report *ReviewBlock) {
		for i, proof := range refs {
			report.Assessments = append(report.Assessments, ReviewAssessment{PredicateID: fmt.Sprintf("predicate:p%d", i), Assessment: "supported", ReceiptRefs: proof})
		}
	})
}

func pReceiptBatch(refs [][]string, indexes ...int) json.RawMessage {
	entries := make([]map[string]any, 0, len(indexes))
	for _, i := range indexes {
		entries = append(entries, map[string]any{"predicate_id": fmt.Sprintf("predicate:p%d", i), "receipt_refs": refs[i]})
	}
	return mustJSONValue(map[string]any{"verdicts": entries})
}

func pConfirmReceiptPremise(t *testing.T, f workContextFixture) error {
	t.Helper()
	ctx, err := prepareTerminalTestContext(t.Context(), f.store, f.workID, "confirm_premise")
	if err != nil {
		return err
	}
	version := readWorkVersion(t, f.store, f.workID)
	id := fmt.Sprintf("confirm-%s-%d", f.workID, version)
	return f.store.Transact(ctx, func(tx *Transaction) error {
		_, err := ApplyWorkflowActionTx(ctx, tx, WorkflowActionExecutionRequest{
			WorkID: f.workID, ExpectedVersion: version, ActionID: "confirm_premise", Payload: json.RawMessage(`{}`), Actor: f.owner,
			AcceptedInputsDigest: nativeDigest([]byte(id)), IdempotencyIdentity: id, OperationID: id,
			PrincipalRef: f.owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: id, RequestID: "request:" + id, ContractDigest: testManifestDigest, Now: time.Now().UTC(),
		})
		return err
	})
}

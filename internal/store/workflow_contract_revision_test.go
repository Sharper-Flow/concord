package store

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func pContractCriterionFixture(t *testing.T, id string) workflowReturnRouteFixture {
	t.Helper()
	f, ref := seedCriterionAssociationAtStep(t, id, "conclude")
	if err := runVerdictActionAs(t, f.store, id, "record_verdict", criterionAssociationPayload(ref, "predicate:first", "predicate:second"), 0, f.owner); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestLateVerdictReplacementHoldsStep(t *testing.T) {
	t.Parallel()
	const id = "issue933-late-verdict"
	f, ref := seedLateCriterionAssociation(t, id)
	payload := criterionAssociationPayload(ref, "predicate:first")
	if err := runVerdictActionAs(t, f.store, id, "record_verdict", payload, 0, f.owner); err != nil {
		t.Fatal(err)
	}
	reviewGateRequireStep(t, f.store, id, "conclude")
	for _, payload := range []json.RawMessage{payload, criterionAssociationPayload(ref, "predicate:second", "predicate:first")} {
		if err := issue1013Preflight(t, f.store, id, "record_verdict", payload, f.owner); err != nil {
			t.Fatalf("replacement preflight: %v", err)
		}
		if err := runVerdictActionAs(t, f.store, id, "record_verdict", payload, 0, f.owner); err != nil {
			t.Fatalf("replacement: %v", err)
		}
		reviewGateRequireStep(t, f.store, id, "conclude")
	}
}

// A premise correction preserves proof whose criterion content is unchanged,
// without rewriting the proof's original contract provenance.
func TestPremiseRevisionPreservesCriterionProvenance(t *testing.T) {
	t.Parallel()
	const id = "issue933-contract-correction"
	f := pContractCriterionFixture(t, id)
	planBefore, err := readWorkflowPlanTx(t.Context(), f.store.db, id)
	if err != nil {
		t.Fatal(err)
	}
	proofBefore, err := latestWorkflowVerdicts(t.Context(), f.store.db, id, 1)
	if err != nil {
		t.Fatal(err)
	}
	var receiptBytesBefore string
	if err := f.store.db.QueryRow(`SELECT json_group_array(payload) FROM (SELECT payload FROM workflow_predicate_verdicts WHERE work_id=? ORDER BY verdict_seq)`, id).Scan(&receiptBytesBefore); err != nil {
		t.Fatal(err)
	}
	if !issue1013HasIntent(issue1013Pin(t, f.store, id), "confirm_premise") {
		t.Fatal("proved criteria did not open premise confirmation")
	}
	successor := pContractSuccessor(t, f.store, id, "corrected premise")
	if err := reviseContractRecord(t, f.store, id, f.owner, successor, readWorkVersion(t, f.store, id)); err != nil {
		t.Fatalf("ordinary premise revision: %v", err)
	}
	var count, active int
	var premise string
	if err := f.store.db.QueryRow(`SELECT count(*),contract_version,premise FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, id).Scan(&count, &active, &premise); err != nil {
		t.Fatal(err)
	}
	if count != 1 || active != 2 || premise != "corrected premise" {
		t.Fatalf("active contract count=%d version=%d premise=%q", count, active, premise)
	}
	reviewGateRequireStep(t, f.store, id, "conclude")
	planAfter, err := readWorkflowPlanTx(t.Context(), f.store.db, id)
	if err != nil || !reflect.DeepEqual(planBefore, planAfter) {
		t.Fatalf("revision changed plan attempts: before=%+v after=%+v err=%v", planBefore, planAfter, err)
	}
	verdicts, err := latestWorkflowVerdicts(t.Context(), f.store.db, id, 2)
	if err != nil || len(verdicts) != 2 {
		t.Fatalf("unchanged criterion associations=%+v err=%v", verdicts, err)
	}
	for _, association := range verdicts {
		if association.ContractVersion != 1 {
			t.Fatal("compatible proof provenance was rewritten")
		}
		if err := verifyWorkflowCriterionAssociation(t.Context(), f.store.db, id, association); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(proofBefore, verdicts) {
		t.Fatalf("revision changed proof receipts: before=%+v after=%+v", proofBefore, verdicts)
	}
	var receiptBytesAfter string
	if err := f.store.db.QueryRow(`SELECT json_group_array(payload) FROM (SELECT payload FROM workflow_predicate_verdicts WHERE work_id=? ORDER BY verdict_seq)`, id).Scan(&receiptBytesAfter); err != nil || receiptBytesBefore != receiptBytesAfter {
		t.Fatalf("revision changed receipt payload bytes: %v", err)
	}
	// The premise revision changed no criterion digest, so the preserved
	// proof still answers the successor and needs no repeated production
	// (CD-0242 D7).
	if !issue1013HasIntent(issue1013Pin(t, f.store, id), "confirm_premise") {
		t.Fatal("unchanged-criterion revision withheld premise confirmation")
	}
	before := contractCorrectionEffectSnapshot(t, f.store, id)
	_, err = WorkflowActionDefinitionFor(t.Context(), f.store, WorkflowActionPreflightRequest{WorkID: id, ExpectedVersion: readWorkVersion(t, f.store, id), ActionID: "confirm_premise", Payload: json.RawMessage(`{}`), Actor: f.owner})
	if err != nil {
		t.Fatalf("premise confirmation on preserved proof: %v", err)
	}
	if contractCorrectionEffectSnapshot(t, f.store, id) != before {
		t.Fatal("confirm_premise preflight changed durable state")
	}
}

func TestReviewPreflightAfterContractRevision(t *testing.T) {
	t.Parallel()
	const id = "issue933-correction-preflight"
	j := pReturnReviewFixture(t, id, WorkflowStepPlanChange, "correction_required")
	moveWorkflowPlanFixtureFrontier(t, j.s, id, "review")
	candidate := issue1013Pin(t, j.s, id).Review.Candidate
	successor := pContractSuccessor(t, j.s, id, "corrected premise")
	if err := reviseContractRecord(t, j.s, id, j.fixture.owner, successor, readWorkVersion(t, j.s, id)); err != nil {
		t.Fatal(err)
	}
	if after := issue1013Pin(t, j.s, id).Review.Candidate; !reflect.DeepEqual(candidate, after) {
		t.Fatalf("revision changed current candidate: before=%+v after=%+v", candidate, after)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	payload := mustJSONValue(map[string]any{"review": currentReviewBlockForTest(t, j.s, id, "ready_for_final_proof")})
	before := j.effects(t)
	request := WorkflowActionPreflightRequest{WorkID: id, ExpectedVersion: readWorkVersion(t, j.s, id), StepID: "review", ActionID: "record_review", Payload: payload, Actor: j.fixture.owner}
	if _, err := WorkflowActionDefinitionFor(ctx, j.s, request); err != nil {
		t.Fatalf("public Review preflight after supersession: %v", err)
	}
	action, err := WorkflowActionDefinitionFor(ctx, j.s, request)
	if err != nil || action.ID != "record_review" {
		t.Fatalf("public Review discovery=%+v err=%v", action, err)
	}
	if j.effects(t) != before {
		t.Fatal("public read paths changed durable state")
	}
	if err := j.action(t, "record_review", payload, j.fixture.owner); err != nil {
		t.Fatalf("public Review after supersession: %v", err)
	}
	reviewGateRequireStep(t, j.s, id, "review")
}

func TestWorkflowActionPreflightRefusesUndeclaredActionAtCheckpoint(t *testing.T) {
	t.Parallel()
	const id = "preflight-supersede-at-checkpoint"
	f := pContractCriterionFixture(t, id)
	request := WorkflowActionPreflightRequest{WorkID: id, ExpectedVersion: readWorkVersion(t, f.store, id), StepID: "conclude", ActionID: "record_finding", Payload: json.RawMessage(`{}`), Actor: f.owner}
	before := contractCorrectionEffectSnapshot(t, f.store, id)
	if _, err := WorkflowActionDefinitionFor(t.Context(), f.store, request); !hasFailureKind(err, KindIllegalLifecycleTransition) {
		t.Fatalf("undeclared action preflight=%v", err)
	}
	if contractCorrectionEffectSnapshot(t, f.store, id) != before {
		t.Fatal("preflight changed effects")
	}
}

func TestVerdictOmittingContractVersionResolvesActiveContract(t *testing.T) {
	t.Parallel()
	const id = "verdict-version-default-supersede"
	f, ref := seedCriterionAssociationAtStep(t, id, "investigate")
	successor := pContractSuccessor(t, f.store, id, "corrected premise")
	predicates := successor["outcome_predicates"].([]any)
	successor["outcome_predicates"] = append(predicates, map[string]any{"predicate_id": "predicate:added", "ordinal": 2, "outcome_kind": "outcome", "outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{"report_recorded"}}})
	if err := reviseContractRecord(t, f.store, id, f.owner, successor, readWorkVersion(t, f.store, id)); err != nil {
		t.Fatal(err)
	}
	before := contractCorrectionEffectSnapshot(t, f.store, id)
	// A revision stales nothing by itself (CD-0242 D7); a verdict
	// before research production is refused because investigate does not
	// declare it.
	if err := runVerdictActionAs(t, f.store, id, "record_verdict", criterionAssociationPayload("report:pending", "predicate:added"), 0, f.owner); !hasFailureKind(err, KindIllegalLifecycleTransition) {
		t.Fatalf("successor association before research production=%v, want the off-step refusal", err)
	}
	if contractCorrectionEffectSnapshot(t, f.store, id) != before {
		t.Fatal("stale successor association changed durable state")
	}
	if err := runVerdictActionAs(t, f.store, id, "record_finding", json.RawMessage(`{}`), 0, f.owner); err != nil {
		t.Fatalf("successor research production: %v", err)
	}
	reviewGateRequireStep(t, f.store, id, "investigate")
	moveWorkflowPlanFixtureFrontier(t, f.store, id, "conclude")
	if err := runVerdictActionAs(t, f.store, id, "record_report", json.RawMessage(`{"evidence_kind":"artifact","evidence_ref":"evidence:return-route-artifact"}`), 0, f.owner); err != nil {
		t.Fatalf("successor research report: %v", err)
	}
	reviewGateRequireStep(t, f.store, id, "conclude")
	qAcceptCriterionReview(t, workContextFixture{store: f.store, workID: id, owner: f.owner, operator: f.operator}, ref)
	if err := runVerdictActionAs(t, f.store, id, "record_verdict", criterionAssociationPayload(ref, "predicate:first", "predicate:second", "predicate:added"), 0, f.owner); err != nil {
		t.Fatalf("association without contract_version: %v", err)
	}
	var recorded int64
	if err := f.store.db.QueryRow(`SELECT contract_version FROM workflow_predicate_verdicts WHERE work_id=? ORDER BY verdict_seq DESC LIMIT 1`, id).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 2 {
		t.Fatalf("association contract_version=%d, want 2", recorded)
	}
	associations, err := latestWorkflowVerdicts(t.Context(), f.store.db, id, 2)
	if err != nil || len(associations) != 3 {
		t.Fatalf("successor conjunction=%+v err=%v", associations, err)
	}
}

// A recorded upstream-revision obligation keeps contract repair reachable.
func TestCurrentReviewAllowsContractRevisionAfterProductiveAccept(t *testing.T) {
	const id = "correction-open-after-productive-accept"
	j := pReturnReviewFixture(t, id, WorkflowStepPlanFix, "revision_required")
	moveWorkflowPlanFixtureFrontier(t, j.s, id, "review")
	completeCurrentReviewForTest(t, j.fixture, "revision_required")
	planBefore, err := readWorkflowPlanTx(t.Context(), j.s.db, id)
	if err != nil {
		t.Fatal(err)
	}
	successor := pContractSuccessor(t, j.s, id, "corrected premise after accepted Review")
	if err := reviseContractRecord(t, j.s, id, j.fixture.owner, successor, readWorkVersion(t, j.s, id)); err != nil {
		t.Fatalf("ordinary update after productive acceptance: %v", err)
	}
	planAfter, err := readWorkflowPlanTx(t.Context(), j.s.db, id)
	if err != nil || !reflect.DeepEqual(planBefore, planAfter) {
		t.Fatalf("revision changed plan attempts: before=%+v after=%+v err=%v", planBefore, planAfter, err)
	}
}

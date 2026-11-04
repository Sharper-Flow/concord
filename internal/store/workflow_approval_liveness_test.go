package store

import (
	"encoding/json"
	"testing"
)

func TestWorkflowPlanRevisedContractKeepsConfirmationRoute(t *testing.T) {
	const workID = "plan-confirmation-revision"
	f := rAcceptance(t, workID, []string{}, true)
	before, err := loadWorkflowReviewState(t.Context(), f.store.db, workID)
	if err != nil || before.Accepted == nil || before.Candidate == nil {
		t.Fatalf("initial Review=%+v err=%v", before, err)
	}
	rReviseContract(t, f.store, workID, mustActorRef(t, f.owner), []string{})
	after, err := loadWorkflowReviewState(t.Context(), f.store.db, workID)
	if err != nil || after.Accepted == nil || after.Candidate == nil || *after.Candidate != *before.Candidate || after.Accepted.ReportDigest != before.Accepted.ReportDigest {
		t.Fatalf("unchanged candidate or Review was lost: %+v err=%v", after, err)
	}
	missing, err := workflowMissingCriterionAssociations(t.Context(), f.store.db, workID)
	if err != nil || len(missing) != 0 {
		t.Fatalf("unchanged criterion proof after revision: missing=%v err=%v", missing, err)
	}
	if err := approvalPlanAction(t, f, "confirm_premise", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("confirm revised contract: %v", err)
	}
	if actor, ok := approvalPremiseConfirmationRow(t, f.store, workID, 2); !ok || actor != mustActorRef(t, f.owner) {
		t.Fatalf("revised contract confirmation actor=%q present=%v", actor, ok)
	}
	completeWorkflowPlanFixture(t, f.store, workID)
	if err := approvalPlanAction(t, f, "complete", json.RawMessage(`{"impact_verdict":"non-breaking"}`)); err != nil {
		t.Fatalf("complete revised contract: %v", err)
	}
}

func TestWorkflowPlanConfirmationRequiresDeclaredApprovalEvidence(t *testing.T) {
	const workID = "plan-confirmation-approval-evidence"
	f := qCurrentAcceptance(t, workID, "approval")
	version, events := readWorkVersion(t, f.store, workID), approvalEventCount(t, f.store, workID)
	if approvalKindEvidenceCount(t, f.store, workID) != 0 {
		t.Fatal("fixture unexpectedly bound approval evidence")
	}
	confirmErr := approvalPlanAction(t, f, "confirm_premise", json.RawMessage(`{}`))
	_ = planAdmissionFailure(t, confirmErr, KindMissingEvidence)
	assertApprovalRefusalUnchanged(t, f, version, events)

	// This isolated producer is synthetic; it proves the binding's exact
	// producer tuple, not an operator approval outside this fixture.
	const ref, producer, run, watermark = "approval:fixture", "principal:approver", "approval-producer", "request:approval"
	seedWorkflowAuthority(t, f.store, run, workID, producer, watermark, []string{ref})
	payload := mustJSONValue(map[string]any{"evidence_kind": "approval", "immutable_subject_ref": ref, "producer_id": producer, "producer_run_ref": run, "producer_watermark": watermark})
	if err := approvalPlanAction(t, f, "bind_evidence", payload); err != nil {
		t.Fatalf("bind declared approval evidence: %v", err)
	}
	if approvalKindEvidenceCount(t, f.store, workID) != 1 {
		t.Fatal("explicit approval evidence was not recorded")
	}
	if err := approvalPlanAction(t, f, "confirm_premise", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("confirmation after declared evidence: %v", err)
	}
}

func TestWorkflowPlanConfirmationRefusesOperatorActor(t *testing.T) {
	const workID = "plan-confirmation-operator"
	f := qBuildAcceptancePredicates(t, workID, "", true, nil, false)
	version, events := readWorkVersion(t, f.store, workID), approvalEventCount(t, f.store, workID)
	request := planAdmissionRequest(workID, "confirm_premise", version, f.operator, json.RawMessage(`{}`))
	dispatchErr := planAdmissionDispatch(t, f.store, request)
	_ = planAdmissionFailure(t, dispatchErr, KindInvalidOperation)
	assertApprovalRefusalUnchanged(t, f, version, events)
	if err := approvalPlanAction(t, f, "confirm_premise", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("authenticated coordinator confirmation: %v", err)
	}
}

func TestWorkflowPlanConfirmationRefusesStaleReview(t *testing.T) {
	const workID = "plan-confirmation-stale-review"
	f := qBuildAcceptancePredicates(t, workID, "", true, nil, false)
	successor := pContractSuccessor(t, f.store, workID, "deliver the revised criterion")
	predicates := successor["outcome_predicates"].([]any)
	predicate := predicates[0].(map[string]any)
	predicate["outcome_payload"].(map[string]any)["check_ref"] = "check:changed-after-review"
	if err := reviseContractRecord(t, f.store, workID, f.owner, successor, readWorkVersion(t, f.store, workID)); err != nil {
		t.Fatalf("revise criterion: %v", err)
	}
	state, err := loadWorkflowReviewState(t.Context(), f.store.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	reviewErr := workflowAdmitReview(state)
	_ = planAdmissionFailure(t, reviewErr, KindStaleRequiresReview)
	version, events := readWorkVersion(t, f.store, workID), approvalEventCount(t, f.store, workID)
	confirmErr := approvalPlanAction(t, f, "confirm_premise", json.RawMessage(`{}`))
	_ = planAdmissionFailure(t, confirmErr, KindMissingEvidence)
	assertApprovalRefusalUnchanged(t, f, version, events)
}

func TestWorkflowPlanConfirmationContinuity(t *testing.T) {
	const workID = "plan-confirmation-continuity"
	f := qCurrentAcceptance(t, workID)
	snapshot, err := ReadWorkflowContinuity(t.Context(), f.store, ContinuityRequest{Work: workID})
	if err != nil || snapshot.WorkPin == nil {
		t.Fatalf("continuity pin=%+v err=%v", snapshot.WorkPin, err)
	}
	if snapshot.WorkPin.WorkflowType != string(WorkflowStepPlanChange) || snapshot.WorkPin.Step != "review" || !workPinContainsAction(snapshot.WorkPin.NextValidIntents, "confirm_premise") {
		t.Fatalf("continuity pin=%+v", snapshot.WorkPin)
	}
}

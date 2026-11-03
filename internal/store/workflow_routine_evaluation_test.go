package store

import (
	"encoding/json"
	"testing"
)

// ownerRefForTest is shared by current actor-identity fixtures.
func ownerRefForTest(t *testing.T, actor WorkflowActor) string {
	t.Helper()
	ref, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func TestWorkflowPlanRoutineEvaluationRequiresCurrentProof(t *testing.T) {
	const workID = "plan-routine-evaluation"
	f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanChange, "implement", []string{"verification"}, nil)
	repo, _ := reviewSubjectClaim(t, f.store, workID)
	commit := runTestGit(t, repo, "rev-parse", "HEAD")
	bindDeliveryReport(t, f, workID, "report:routine", commit, "pass")
	report := currentReviewBlockForTest(t, f.store, workID, "ready_for_final_proof")
	report.Assessments = []ReviewAssessment{{PredicateID: "predicate:return-route", Assessment: "supported", ReceiptRefs: []string{"report:routine"}}}
	review := planAdmissionRequest(workID, "record_review", readWorkVersion(t, f.store, workID), f.owner, mustJSONValue(map[string]any{"review": report}))
	if err := planAdmissionDispatch(t, f.store, review); err != nil {
		t.Fatalf("record Review: %v", err)
	}
	moveWorkflowPlanFixtureFrontier(t, f.store, workID, "review")
	version := readWorkVersion(t, f.store, workID)
	var before int
	if err := f.store.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=?`, workID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	unbound := json.RawMessage(`{"verdicts":[{"predicate_id":"predicate:return-route","receipt_refs":["evidence:not-bound"]}]}`)
	request := planAdmissionRequest(workID, "record_verdict", version, f.owner, unbound)
	first := planAdmissionFailure(t, planAdmissionPreflight(t, f.store, request), KindMissingEvidence)
	second := planAdmissionFailure(t, planAdmissionDispatch(t, f.store, request), KindMissingEvidence)
	var after int
	if err := f.store.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=?`, workID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if first.Detail != second.Detail || readWorkVersion(t, f.store, workID) != version || before != after {
		t.Fatalf("unbound evidence parity or rollback: %v / %v", first, second)
	}
	valid := json.RawMessage(`{"verdicts":[{"predicate_id":"predicate:return-route","receipt_refs":["report:routine"]}]}`)
	request = planAdmissionRequest(workID, "record_verdict", version, f.owner, valid)
	if err := planAdmissionPreflight(t, f.store, request); err != nil {
		t.Fatalf("reviewed evidence preflight: %v", err)
	}
	if err := planAdmissionDispatch(t, f.store, request); err != nil {
		t.Fatalf("reviewed evidence dispatch: %v", err)
	}
	verdicts, err := latestWorkflowVerdicts(t.Context(), f.store.db, workID, 1)
	if err != nil || len(verdicts) != 1 || verdicts[0].VerdictActorRef != mustActorRef(t, f.owner) || len(verdicts[0].EvaluationEvidence) != 1 || verdicts[0].EvaluationEvidence[0] != "report:routine" {
		t.Fatalf("verdicts=%+v err=%v", verdicts, err)
	}
	confirm := planAdmissionRequest(workID, "confirm_premise", readWorkVersion(t, f.store, workID), f.owner, json.RawMessage(`{}`))
	if err := planAdmissionDispatch(t, f.store, confirm); err != nil {
		t.Fatalf("routine coordinator confirmation: %v", err)
	}
	if actor, ok := approvalPremiseConfirmationRow(t, f.store, workID, 1); !ok || actor != mustActorRef(t, f.owner) || approvalKindEvidenceCount(t, f.store, workID) != 0 {
		t.Fatalf("confirmation actor=%q present=%v or spurious approval evidence", actor, ok)
	}
}

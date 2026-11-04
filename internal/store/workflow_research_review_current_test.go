package store

import "testing"

func TestResearchPlanRecordsReview(t *testing.T) {
	const workID = "research-review-current"
	fixture, receipt := seedCriterionAssociationAtStep(t, workID, "conclude")
	s := fixture.store
	report := currentReviewBlockForTest(t, s, workID, "ready_for_final_proof")
	for _, criterion := range report.Dependencies.Criteria {
		report.Assessments = append(report.Assessments, ReviewAssessment{PredicateID: criterion.PredicateID, Assessment: "supported", ReceiptRefs: []string{receipt}})
	}
	if err := recordReviewForTest(t, s, workID, fixture.owner, report); err != nil {
		t.Fatalf("record research Review: %v", err)
	}
	state, err := loadWorkflowReviewState(t.Context(), s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Accepted == nil || state.Accepted.ReportDigest != reviewDigest(report) {
		t.Fatalf("recorded research Review = %+v", state.Accepted)
	}
}

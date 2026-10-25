package store

import "testing"

func TestAssignmentGroupFollowsNewAcceptedReview(t *testing.T) {
	for _, disposition := range []string{"continue", "correction_required", "revision_required", "ready_for_final_proof"} {
		t.Run(disposition, func(t *testing.T) {
			report := reviewBlockFixture()
			report.Disposition = disposition
			report.Findings = nil
			state := WorkflowReviewState{ExecutionProgressed: true, DependenciesCurrent: true, Candidate: &report.OutputCandidate,
				Groups:   map[string]workflowAssignmentGroup{"job:existing": {Permission: "first_entry"}},
				Accepted: &WorkflowAcceptedReview{ReportRef: "report:newer", Report: *report}}
			err := workflowAdmitReview(state, "job:existing")
			if (err == nil) != (disposition == "continue") {
				t.Fatalf("relaunch after %s: %v", disposition, err)
			}
			state.Accepted = nil
			if err := workflowAdmitReview(state, "job:existing"); err != nil {
				t.Fatalf("existing group without newer Review: %v", err)
			}
			if err := workflowAdmitReview(state, "job:never-dispatched"); err == nil {
				t.Fatal("a new job inherited first entry after another group ran")
			}
		})
	}
}

func TestAssignmentGroupCannotReuseAnotherGroupsContinue(t *testing.T) {
	report := reviewBlockFixture()
	report.Disposition, report.Findings = "continue", nil
	state := WorkflowReviewState{ExecutionProgressed: true, DependenciesCurrent: true, Candidate: &report.OutputCandidate,
		Groups:   map[string]workflowAssignmentGroup{"job:first": {Permission: "first_entry"}, "job:continued": {Permission: "continue", ReviewRef: "report:continue"}},
		Accepted: &WorkflowAcceptedReview{ReportRef: "report:continue", Report: *report}, ContinueConsumed: true, GroupID: "job:continued"}
	if err := workflowAdmitReview(state, "job:continued"); err != nil {
		t.Fatal(err)
	}
	for _, job := range []string{"job:first", "job:never-dispatched"} {
		if err := workflowAdmitReview(state, job); err == nil {
			t.Fatalf("%s reused a consumed continuation", job)
		}
	}
	state.DependenciesCurrent = false
	if err := workflowAdmitReview(state, "job:continued"); err == nil {
		t.Fatal("relaunch ignored changed dependencies")
	}
}

func TestAssignmentGroupCorrectivePermissionDoesNotOpenOrdinaryGroups(t *testing.T) {
	report := reviewBlockFixture()
	report.Disposition = "correction_required"
	state := WorkflowReviewState{ExecutionProgressed: true, DependenciesCurrent: true, Candidate: &report.OutputCandidate,
		Groups:   map[string]workflowAssignmentGroup{"job:corrective": {ReviewRef: "report:correction", CorrectionRef: "request:correction"}},
		Accepted: &WorkflowAcceptedReview{ReportRef: "report:correction", Report: *report}, Findings: report.Findings}
	if err := workflowAdmitReview(state, "job:corrective"); err != nil {
		t.Fatal(err)
	}
	for _, job := range []string{"job:ordinary", ""} {
		if err := workflowAdmitReview(state, job); err == nil {
			t.Fatalf("corrective admission authorized ordinary progression %q", job)
		}
	}
	state.Accepted.ReportRef = "report:newer"
	if err := workflowAdmitReview(state, "job:corrective"); err == nil {
		t.Fatal("corrective relaunch ignored newer Review")
	}
}

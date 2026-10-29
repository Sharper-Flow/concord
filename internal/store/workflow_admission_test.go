package store

import "testing"

func TestWorkflowAdmissionStateComparable(t *testing.T) {
	_ = make(map[WorkflowAdmissionState]struct{})
}

func TestWorkflowAdmitDecisionTable(t *testing.T) {
	entry, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	definition := entry.Definition
	definition.ActionDefinitions = []WorkflowActionDefinition{
		{ID: "record_delivery", ExecutionMode: ActionAdvance},
		{ID: "accept_worker_result", ExecutionMode: ActionAdvance},
		{ID: "dispatch_worker", ExecutionMode: ActionHold},
		{ID: "checkpoint_context", ExecutionMode: ActionHold},
		{ID: "checkpoint_refine", ExecutionMode: ActionHold},
		{ID: "confirm_premise", ExecutionMode: ActionAdvance},
	}
	definition.StepGraph = WorkflowStepGraph{Steps: []WorkflowStep{
		{ID: "refine", Actions: []string{"checkpoint_refine", "record_delivery", "dispatch_worker", "accept_worker_result", "confirm_premise", "checkpoint_context"}},
		{ID: "delivery", Actions: []string{"record_delivery", "request_correction", "checkpoint_context"}},
	}}
	tests := []struct {
		name   string
		change func(*WorkflowAdmissionState)
		want   map[string]bool
	}{
		{"accepted ready admits delivery and continuity", func(*WorkflowAdmissionState) {}, map[string]bool{"record_delivery": true, "accept_worker_result": true, "dispatch_worker": true, "checkpoint_context": true}},
		{"report arrival is not readiness", func(s *WorkflowAdmissionState) { s.Review.Accepted = nil }, map[string]bool{"record_delivery": false, "accept_worker_result": true, "dispatch_worker": true, "checkpoint_context": true}},
		{"continue does not leave execution", func(s *WorkflowAdmissionState) { s.Review.Accepted.Report.Disposition = "continue" }, map[string]bool{"record_delivery": false, "accept_worker_result": true, "dispatch_worker": true}},
		{"correction remains an acceptable report without delivery", func(s *WorkflowAdmissionState) { s.Review.Accepted.Report.Disposition = "correction_required" }, map[string]bool{"record_delivery": false, "accept_worker_result": true, "dispatch_worker": true}},
		{"revision remains an acceptable report without delivery", func(s *WorkflowAdmissionState) { s.Review.Accepted.Report.Disposition = "revision_required" }, map[string]bool{"record_delivery": false, "accept_worker_result": true, "dispatch_worker": true}},
		{"current blocker holds delivery not report intake", func(s *WorkflowAdmissionState) { s.Review.Findings = reviewBlockFixture().Findings }, map[string]bool{"record_delivery": false, "accept_worker_result": true, "dispatch_worker": true}},
		{"new candidate needs applicable readiness", func(s *WorkflowAdmissionState) {
			candidate := *s.Review.Candidate
			candidate.Commit = "cccccccccccccccccccccccccccccccccccccccc"
			s.Review.Candidate = &candidate
		}, map[string]bool{"record_delivery": false, "accept_worker_result": true, "dispatch_worker": true}},
		{"changed dependencies hold delivery", func(s *WorkflowAdmissionState) { s.Review.DependenciesCurrent = false }, map[string]bool{"record_delivery": false, "accept_worker_result": true, "checkpoint_context": true}},
		{"gate holds delivery but keeps declared correction", func(s *WorkflowAdmissionState) {
			s.Step, s.CorrectionRequestRecovery, s.Review.Accepted = "delivery", true, nil
		}, map[string]bool{"record_delivery": false, "request_correction": true, "checkpoint_context": true}},
		{"correction cannot invent its prerequisites", func(s *WorkflowAdmissionState) {
			s.Step, s.Review.Accepted = "delivery", nil
			s.CorrectionRequestMissing = workflowCorrectionMissingGateReview
		}, map[string]bool{"record_delivery": false, "request_correction": false}},
		{"readiness does not erase a live dispatch hold", func(s *WorkflowAdmissionState) { s.DispatchHold = true }, map[string]bool{"record_delivery": false, "accept_worker_result": true, "checkpoint_context": true}},
		{"step legality still holds", func(*WorkflowAdmissionState) {}, map[string]bool{"record_verdict": false}},
		{"confirmation requires its open question", func(*WorkflowAdmissionState) {}, map[string]bool{"confirm_premise": false, "dispatch_worker": true}},
		{"open question admits confirmation", func(s *WorkflowAdmissionState) { s.PendingOperatorDecision = true }, map[string]bool{"confirm_premise": true, "dispatch_worker": true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			report := reviewBlockFixture()
			report.Disposition = "ready_for_final_proof"
			state := WorkflowAdmissionState{Step: "refine", RefinementWorkflow: true, ReviewLane: "review",
				Review: &WorkflowReviewState{ExecutionProgressed: true, Candidate: &report.OutputCandidate, Accepted: &WorkflowAcceptedReview{Report: *report}, DependenciesCurrent: true}}
			state.Delivery.Started = true
			tc.change(&state)
			for action, want := range tc.want {
				decision := workflowAdmit(definition, state, action)
				if decision.Admitted != want {
					t.Fatalf("%s: admitted=%v, want %v: %+v", action, decision.Admitted, want, decision.Failure)
				}
				if want && decision.Failure != nil || !want && decision.Failure == nil {
					t.Fatalf("%s: admission and refusal disagree: %+v", action, decision)
				}
				if !want && decision.Failure.RecoveryAction == "" {
					t.Fatalf("%s: refusal has no admitted recovery", action)
				}
				if action == "confirm_premise" && !want && (!decision.OperatorQuestionClosed || decision.Failure.Kind != KindStaleRequiresReview) {
					t.Fatalf("confirmation lost its typed closed-question refusal: %+v", decision)
				}
			}
		})
	}
}

func TestAdmissionLoaderFoldsDispatchCapabilityClass(t *testing.T) {
	j, _ := descendantReviewFixture(t, "admission-capability-class")
	tx, err := j.s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	definition, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	state, _, err := loadWorkflowAdmissionStateTxForRegistry(t.Context(), tx, j.workID, definition.Definition, "repair", "workflow_admission_test", BuiltinWorkflowRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if state.AttemptState != "completed" || state.AttemptCapabilityClass != "review" {
		t.Fatalf("latest attempt = %q/%q, want completed/review", state.AttemptState, state.AttemptCapabilityClass)
	}
	if state.Review == nil || state.Review.Accepted == nil || state.Review.Accepted.Report.Disposition != "continue" || !state.Review.DependenciesCurrent {
		t.Fatalf("the current accepted Review did not reach the shared admission: %+v", state.Review)
	}
}

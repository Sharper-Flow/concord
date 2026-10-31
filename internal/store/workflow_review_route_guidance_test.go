package store

import (
	"strings"
	"testing"
)

func requireReviewRouteGuidance(t *testing.T, guidance string, prerequisites ...string) {
	t.Helper()
	for _, want := range []string{"independent Review through native Task", "coordinator record_review", "exact candidate, current dependencies, findings and disposition", "no separate agent or RTE session is required"} {
		if !strings.Contains(guidance, want) {
			t.Fatalf("guidance %q does not name %q", guidance, want)
		}
	}
	for _, retired := range []string{"dispatch_worker", "lane_id=", "scoped job", "accept_worker_result"} {
		if strings.Contains(guidance, retired) {
			t.Fatalf("guidance restores %q: %s", retired, guidance)
		}
	}
	review := strings.Index(guidance, "independent Review through native Task")
	for _, prerequisite := range prerequisites {
		if at := strings.Index(guidance, prerequisite); at < 0 || at >= review {
			t.Fatalf("guidance %q must place %q before Review", guidance, prerequisite)
		}
	}
}

func reviewRouteFailure(t *testing.T, err error) *Failure {
	t.Helper()
	var failure *Failure
	if err == nil || !failureAs(err, &failure) {
		t.Fatalf("expected a typed refusal, got %v", err)
	}
	return failure
}

func TestReviewRouteGuidanceAdmission(t *testing.T) {
	for _, tc := range []struct {
		name             string
		change           func(*WorkflowReviewState)
		kind             FailureKind
		detail, recovery string
	}{
		{"blocking finding", func(s *WorkflowReviewState) { s.Findings = reviewBlockFixture().Findings }, KindMissingEvidence, "open in-scope blocking finding finding:one", "resolve the finding with output-subject proof"},
		{"missing review", func(s *WorkflowReviewState) { s.Accepted = nil }, KindMissingEvidence, "current candidate has no recorded Review", "obtain independent Review and record its subject-bound report"},
		{"changed candidate", func(s *WorkflowReviewState) {
			candidate := *s.Candidate
			candidate.Commit = strings.Repeat("c", 40)
			s.Candidate = &candidate
		}, KindStaleRequiresReview, "recorded Review does not cover the current candidate and dependencies", "review the exact current candidate and dependencies"},
		{"changed dependencies", func(s *WorkflowReviewState) { s.DependenciesCurrent = false }, KindStaleRequiresReview, "recorded Review does not cover the current candidate and dependencies", "review the exact current candidate and dependencies"},
		{"final proof", func(s *WorkflowReviewState) { s.Accepted.Report.Disposition = "continue" }, KindMissingEvidence, "Review has not reached ready_for_final_proof without remaining obligations", "finish the recorded obligations and record the independent Review outcome"},
		{"architecture revision", func(s *WorkflowReviewState) {
			s.Dependencies = &ReviewDependencies{ArchitectureNotApplicable: false}
			s.DependenciesCurrent = false
		}, KindStaleRequiresReview, "recorded Review does not cover the current candidate and dependencies", "review the exact current candidate and dependencies"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := reviewBlockFixture()
			report.Disposition, report.RemainingObligations = "ready_for_final_proof", []ReviewObligation{}
			state := WorkflowReviewState{Candidate: &report.OutputCandidate, Accepted: &WorkflowAcceptedReview{Report: *report}, DependenciesCurrent: true}
			tc.change(&state)
			before := string(mustJSONValue(state))
			failure := reviewRouteFailure(t, workflowAdmitReview(state))
			if failure.Kind != tc.kind || failure.Op != "workflow_review" || failure.Detail != tc.detail || failure.RetrySafe || failure.RecoveryAction != tc.recovery {
				t.Fatalf("Review refusal = %+v", failure)
			}
			if string(mustJSONValue(state)) != before {
				t.Fatal("guidance changed admission state")
			}
		})
	}
}

func TestReviewRouteGuidanceAcceptance(t *testing.T) {
	j := pReturnReviewFixture(t, "review-route-acceptance", "workflow.break_fix", "ready_for_final_proof")
	for _, tc := range []struct {
		name, detail, recovery string
		change                 func(*ReviewBlock)
	}{
		{"replaced input", "corrective Review input is not the retained candidate", "record the retained input and the reviewed output", func(r *ReviewBlock) { r.InputCandidate.Commit = strings.Repeat("f", 40) }},
		{"changed criteria", "Review judged different criterion or architecture content", "review the current criterion and architecture digests", func(r *ReviewBlock) {
			r.Dependencies.Criteria = []ReviewCriterion{{PredicateID: "predicate:changed", ContentDigest: nativeDigest([]byte("changed"))}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := currentReviewBlockForTest(t, j.s, j.workID, "ready_for_final_proof")
			tc.change(report)
			before := outsideRepairDatabaseSnapshot(t, j.s)
			failure := reviewRouteFailure(t, recordReviewForTest(t, j.s, j.workID, j.fixture.owner, report))
			if failure.Kind != KindStaleRequiresReview || failure.Op != "workflow_review" || failure.Detail != tc.detail || failure.RecoveryAction != tc.recovery {
				t.Fatalf("Review refusal = %+v", failure)
			}
			if outsideRepairDatabaseSnapshot(t, j.s) != before {
				t.Fatal("refused Review changed durable state")
			}
		})
	}
	state, err := loadWorkflowReviewState(t.Context(), j.s.db, j.workID)
	if err != nil || state.Candidate == nil {
		t.Fatalf("candidate missing: %v", err)
	}
	failure := reviewRouteFailure(t, workflowReviewedCommandReceipt(t.Context(), j.s.db, j.workID, "predicate:missing", "receipt:command", state.Candidate.Commit, state.Candidate.ProjectID))
	if failure.Kind != KindMissingEvidence || failure.Detail != "command receipt receipt:command has no current Review assessment of criterion predicate:missing" || failure.RecoveryAction != "record a Review assessing that criterion and captured receipt on the final candidate" {
		t.Fatalf("criterion assessment guidance = %+v", failure)
	}
}

func TestReviewRouteGuidanceWorkPinCorrection(t *testing.T) {
	for _, family := range []string{"workflow.implementation", "workflow.break_fix"} {
		t.Run(family, func(t *testing.T) {
			j := reviewGateRefineFixture(t, "review-route-pin", family)
			reviewGateCompleteReview(t, j, "correction_required")
			reviewGateRequireStep(t, j.s, j.workID, "refine")
			pin, err := ReadWorkPin(t.Context(), j.s, j.workID)
			if err != nil {
				t.Fatal(err)
			}
			if !workPinContainsAction(pin.NextValidIntents, "request_correction") {
				t.Fatalf("pin lost correction: %+v", pin)
			}
			_, admission := loadedAdmissionForTest(t, j.s, j.workID, pin)
			correction := admission.CorrectionRequestContext
			if correction == nil {
				t.Fatal("admitted correction has no guidance")
			}
			if err := j.action(t, "request_correction", map[string]any{"diagnosis": correction.Diagnosis, "strategy": correction.Strategy, "predicate_ids": correction.PredicateIDs, "evidence_refs": []string{"evidence:return-route-verification"}}, j.fixture.owner); err != nil {
				t.Fatal(err)
			}
			target := "execution"
			if family == "workflow.break_fix" {
				target = "repair"
			}
			reviewGateRequireStep(t, j.s, j.workID, target)
			pin, err = ReadWorkPin(t.Context(), j.s, j.workID)
			if err != nil {
				t.Fatal(err)
			}
			if pin.Correction == nil || pin.Correction.SourceEventID == "" || pin.Correction.SourceEventID == correction.SourceEventID || pin.Correction.Strategy != correction.Strategy {
				t.Fatalf("recorded correction provenance = %+v", pin.Correction)
			}
			requireReviewRouteGuidance(t, pin.Correction.Strategy, "perform the declared corrective work")
			if !strings.Contains(pin.Correction.Strategy, "covering the corrected candidate") {
				t.Fatalf("strategy lost subject: %s", pin.Correction.Strategy)
			}
			failure := workflowCorrectionRequestUnavailableFailure("workflow_action", workflowCorrectionMissingFreshReview)
			if failure.Kind != KindInvalidOperation {
				t.Fatalf("consumed correction refusal = %+v", failure)
			}
			requireReviewRouteGuidance(t, failure.RecoveryAction)
		})
	}
}

func TestReviewRouteGuidanceDeliveryGateReturn(t *testing.T) {
	j := pReturnReviewFixture(t, "review-route-gate", "workflow.break_fix", "ready_for_final_proof")
	state, err := loadWorkflowReviewState(t.Context(), j.s.db, j.workID)
	if err != nil {
		t.Fatal(err)
	}
	state.DependenciesCurrent = false
	definition := pinnedWorkflowDefinitionForTest(t, j.s, j.workID)
	correction, err := workflowDeliveryGateCorrectionContextFolded(t.Context(), j.s.db, j.workID, definition, "delivery", "workflow_action", WorkflowAdmissionState{Review: &state})
	if err != nil || correction == nil {
		t.Fatalf("stale Review gate return: %+v, %v", correction, err)
	}
	target := workflowCorrectionReturnTarget(definition, "delivery")
	requireReviewRouteGuidance(t, correction.Strategy, "return to step \""+target+"\"")
	if correction.SourceEventID != state.Accepted.ReportRef {
		t.Fatalf("return source = %+v", correction)
	}
}

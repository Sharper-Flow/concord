package store

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWorkflowReviewCurrentCriteriaAndArchitecture(t *testing.T) {
	for _, phase := range []string{"unreviewed", "current", "criterion-changed", "architecture-changed", "architecture-removed"} {
		t.Run(phase, func(t *testing.T) {
			report := reviewBlockFixture()
			report.Disposition = "ready_for_final_proof"
			report.Findings = []ReviewFinding{}
			report.Dependencies.ArchitectureNotApplicable = false
			report.Dependencies.ArchitectureDigest = "sha256:" + strings.Repeat("a", 64)
			current := report.Dependencies
			current.Criteria = append([]ReviewCriterion(nil), report.Dependencies.Criteria...)
			switch phase {
			case "criterion-changed":
				current.Criteria[0].ContentDigest = "sha256:" + strings.Repeat("b", 64)
			case "architecture-changed":
				current.ArchitectureDigest = "sha256:" + strings.Repeat("b", 64)
			case "architecture-removed":
				current.ArchitectureDigest, current.ArchitectureNotApplicable = "", true
			}
			state := WorkflowReviewState{Dependencies: &current, Findings: []ReviewFinding{}}
			if phase != "unreviewed" {
				state.Candidate = &report.OutputCandidate
				state.Accepted = &WorkflowAcceptedReview{ReportRef: "review:recorded", Report: *report}
				state.DependenciesCurrent = sameReviewDependencies(report.Dependencies, current)
			}
			err := workflowAdmitReview(state)
			switch phase {
			case "unreviewed":
				_ = planAdmissionFailure(t, err, KindMissingEvidence)
			case "current":
				if err != nil {
					t.Fatalf("current Review refused: %v", err)
				}
			default:
				_ = planAdmissionFailure(t, err, KindStaleRequiresReview)
			}
		})
	}
}

func TestWorkflowPlanDeliveryRefusesReviewOfChangedCriterion(t *testing.T) {
	const workID = "plan-delivery-changed-criterion"
	f, _ := deliveryPlanFixture(t, workID, []string{}, "ready_for_final_proof")
	successor := pContractSuccessor(t, f.store, workID, "Deliver the changed criterion.")
	predicates := successor["outcome_predicates"].([]any)
	predicate := predicates[0].(map[string]any)
	predicate["outcome_payload"].(map[string]any)["check_ref"] = "check:changed"
	if err := reviseContractRecord(t, f.store, workID, f.owner, successor, readWorkVersion(t, f.store, workID)); err != nil {
		t.Fatalf("revise criterion: %v", err)
	}
	state, err := loadWorkflowReviewState(t.Context(), f.store.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	_ = planAdmissionFailure(t, workflowAdmitReview(state), KindStaleRequiresReview)
	version := readWorkVersion(t, f.store, workID)
	request := planAdmissionRequest(workID, "record_delivery", version, f.owner, json.RawMessage(`{"delivery_artifact":"artifact:changed","delivery_state":"asserted"}`))
	first := planAdmissionFailure(t, planAdmissionPreflight(t, f.store, request), KindStaleRequiresReview)
	second := planAdmissionFailure(t, planAdmissionDispatch(t, f.store, request), KindStaleRequiresReview)
	if first.Detail != second.Detail || readWorkVersion(t, f.store, workID) != version {
		t.Fatalf("stale Review delivery parity or rollback: %v / %v", first, second)
	}
}

package store

import (
	"fmt"
	"strings"
	"testing"
)

func TestWorkflowReviewReadinessMatrix(t *testing.T) {
	for _, disposition := range []string{"", "continue", "correction_required", "revision_required", "ready_for_final_proof"} {
		for flags := range 16 {
			t.Run(fmt.Sprintf("%s/%d", disposition, flags), func(t *testing.T) {
				candidate := ReviewCandidate{ProjectID: "project:review", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}
				report := ReviewBlock{Disposition: disposition, InputCandidate: candidate, OutputCandidate: candidate, RemainingObligations: []ReviewObligation{}}
				state := WorkflowReviewState{DependenciesCurrent: flags&2 != 0}
				if disposition != "" {
					state.Accepted = &WorkflowAcceptedReview{Report: report}
				}
				if flags&1 != 0 {
					state.Candidate = &candidate
					if flags&4 != 0 {
						candidate.Commit = strings.Repeat("c", 40)
					}
				}
				if flags&8 != 0 {
					state.Findings = []ReviewFinding{{FindingID: "finding:block", InScope: true, Blocking: true, Status: "open"}}
				}
				want := disposition == "ready_for_final_proof" && flags&1 != 0 && flags&2 != 0 && flags&4 == 0 && flags&8 == 0
				if err := workflowAdmitReview(state); (err == nil) != want {
					t.Fatalf("admitted=%v, want %v: %v", err == nil, want, err)
				}
			})
		}
	}
}

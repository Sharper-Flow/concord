package store

import (
	"reflect"
	"strings"
	"testing"
)

func TestRetainedReviewFindingsRemainVisibleAfterRecording(t *testing.T) {
	for _, blocking := range []bool{false, true} {
		name := "crucial"
		if blocking {
			name = "blocking"
		}
		t.Run(name, func(t *testing.T) {
			j, input := retainedReviewFixture(t, "retained-finding-"+name)
			descendantCommit(t, j, "ordinary-editor")
			finding := ReviewFinding{
				FindingID: "finding:retained", Severity: "P2", Confidence: "high",
				Detail:  "the retained correction still violates its bound obligation",
				InScope: true, Blocking: blocking, Status: "open",
				EvidenceRefs: []string{"evidence:retained"}, ClosureRefs: []string{},
			}
			completeCurrentReviewForTest(t, j.fixture, j.repo, "correction_required", func(report *ReviewBlock) {
				report.InputCandidate = input
				report.Findings = []ReviewFinding{finding}
			})
			state, err := loadWorkflowReviewState(t.Context(), j.s.db, j.workID)
			if err != nil || len(state.Findings) != 1 || !reflect.DeepEqual(state.Findings[0], finding) {
				t.Fatalf("retained finding disappeared or changed: %+v, %v", state.Findings, err)
			}
			state.Crucial = !blocking
			if err := workflowAdmitReview(state); !hasFailureKind(err, KindMissingEvidence) || !strings.Contains(err.Error(), finding.FindingID) {
				t.Fatalf("open retained finding did not block exit: %v", err)
			}
		})
	}
}

func TestRetainedReviewDraftKeepsCandidateBoundary(t *testing.T) {
	j, input := retainedReviewFixture(t, "retained-draft-boundary")
	descendantCommit(t, j, "ordinary-editor")
	state, err := readWorkflowReviewFacts(t.Context(), j.s.db, j.workID)
	if err != nil {
		t.Fatal(err)
	}
	report := currentReviewBlockForTest(t, j.s, j.workID, "correction_required")
	report.InputCandidate = input
	report.Findings = []ReviewFinding{{FindingID: "finding:draft", Severity: "P2", Confidence: "high", Detail: "unrecorded finding", InScope: true, Blocking: true, Status: "open", EvidenceRefs: []string{}, ClosureRefs: []string{}}}
	before := j.effects(t)
	ctx, err := prepareTerminalTestContext(t.Context(), j.s, j.workID, "record_review")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateRecordedReview(ctx, j.s.db, j.workID, report); err != nil {
		t.Fatalf("validate draft Review: %v", err)
	}
	after, err := readWorkflowReviewFacts(t.Context(), j.s.db, j.workID)
	if err != nil || !reflect.DeepEqual(state, after) || j.effects(t) != before {
		t.Fatalf("unrecorded Review changed candidate/findings: %+v -> %+v, %v", state, after, err)
	}
}

func TestRetainedReviewReadyForFinalProofAdmitsExit(t *testing.T) {
	j, input := retainedReviewFixture(t, "retained-ready-for-proof")
	head := descendantCommit(t, j, "ordinary-editor")
	ref, _ := completeCurrentReviewForTest(t, j.fixture, j.repo, "ready_for_final_proof", func(report *ReviewBlock) {
		report.InputCandidate = input
	})
	state, err := loadWorkflowReviewState(t.Context(), j.s.db, j.workID)
	if err != nil || state.Candidate == nil || state.Candidate.Commit != head || state.Accepted == nil || state.Accepted.ReportRef != ref {
		t.Fatalf("recorded retained Review: %+v, %v", state, err)
	}
	if err := workflowAdmitReview(state); err != nil {
		t.Fatalf("recorded ready_for_final_proof did not admit exit: %v", err)
	}
}

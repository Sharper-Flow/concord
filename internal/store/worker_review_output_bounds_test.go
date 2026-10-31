package store

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

func TestReviewOracleOwnerIDBound(t *testing.T) {
	for _, length := range []int{6, 7, 128, 129, 133} {
		t.Run(fmt.Sprint(length), func(t *testing.T) {
			ownerID := "owner:" + strings.Repeat("a", length-len("owner:"))
			want := length >= 7 && length <= 128
			finding := &ReviewOracleFinding{Classification: OracleClassificationFollowUp, OwnerID: ownerID}
			report := reviewBlockFixture()
			report.Findings[0].Oracle = finding
			err := validateReviewBlock(report)
			if (err == nil) != want || !want && !hasFailureKind(err, KindInvalidPayload) {
				t.Errorf("report shape accepted=%v, want %v: %v", err == nil, want, err)
			}
			state := WorkflowReviewState{Findings: report.Findings}
			if err := payloadschema.Validate("workflow_review_state", mustJSONValue(state)); (err == nil) != want {
				t.Errorf("output schema accepted=%v, want %v: %v", err == nil, want, err)
			}
		})
	}
}

func TestReviewReportRefPreservesUnboundedEventID(t *testing.T) {
	for _, length := range []int{512, 513, 4096} {
		t.Run(fmt.Sprint(length), func(t *testing.T) {
			db, _ := g32ReviewFixture(t)
			report := reviewBlockFixture()
			reportRef := strings.Repeat("e", length)
			state := WorkflowReviewState{Accepted: &WorkflowAcceptedReview{ReportRef: reportRef, ReportDigest: reviewDigest(report), ActorRef: progressActorRecord(t, db, "main", "coordinator"), Report: *report}, Candidate: &report.OutputCandidate, Findings: report.Findings}
			if err := writeWorkflowReviewFacts(t.Context(), db, "work-history", state); err != nil {
				t.Fatal(err)
			}
			state, err := readWorkflowReviewFacts(t.Context(), db, "work-history")
			if err != nil {
				t.Fatal(err)
			}
			if state.Accepted == nil || state.Accepted.ReportRef != reportRef {
				t.Fatal("accepted review did not preserve the terminal event id unchanged")
			}
			if err := payloadschema.Validate("workflow_review_state", mustJSONValue(state)); err != nil {
				t.Fatalf("retained %d-byte event id refused by output schema: %v", length, err)
			}
			state.Accepted.ReportRef = ""
			if err := payloadschema.Validate("workflow_review_state", mustJSONValue(state)); err == nil {
				t.Fatal("empty report reference admitted")
			}
		})
	}
}

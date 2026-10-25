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
			finding := &WorkerOracleFinding{Classification: OracleClassificationFollowUp, OwnerID: ownerID}
			report := reviewBlockFixture()
			report.Findings[0].Oracle = finding
			err := ValidateWorkerOracleReportShape(WorkerCompletedPayload{Review: report})
			if (err == nil) != want || !want && !hasFailureKind(err, KindInvalidPayload) {
				t.Errorf("report shape accepted=%v, want %v: %v", err == nil, want, err)
			}
			if err := payloadschema.Validate("lane_report_oracle_finding", mustJSONValue(finding)); (err == nil) != want {
				t.Errorf("output schema accepted=%v, want %v: %v", err == nil, want, err)
			}
			if want {
				// An out-of-scope follow-up does not need a declared owner.
				if err := validateWorkerOracleFindingJoins(finding, &AcceptanceOracle{}, map[string]OracleOwner{}); err != nil {
					t.Errorf("follow-up required an owner join: %v", err)
				}
			}

			oracle := testOracleGraph()
			original := oracle.Owners[0].OwnerID
			oracle.Owners[0].OwnerID = ownerID
			for i := range oracle.Cases {
				if oracle.Cases[i].OwnerID == original {
					oracle.Cases[i].OwnerID = ownerID
				}
			}
			for i := range oracle.Controls {
				if oracle.Controls[i].OwnerID == original {
					oracle.Controls[i].OwnerID = ownerID
				}
			}
			if err := validateAcceptanceOracle(oracle, []string{"predicate:one", "predicate:two"}); (err == nil) != want {
				t.Errorf("oracle graph accepted=%v, want %v: %v", err == nil, want, err)
			}
		})
	}
}

func TestReviewReportRefPreservesUnboundedEventID(t *testing.T) {
	for _, length := range []int{512, 513, 4096} {
		t.Run(fmt.Sprint(length), func(t *testing.T) {
			db, appendEvent := g32ReviewFixture(t)
			report := reviewBlockFixture()
			const attempt = "review:unbounded-ref"
			appendEvent(WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "dispatch_worker", WorkerAttemptID: attempt, WorkerCapabilityClass: "review", WorkerSubjectCommit: report.InputCandidate.Commit})
			appendEvent(WorkerCompleted, WorkerCompletedPayload{AttemptID: attempt, Review: report})
			reportRef := strings.Repeat("e", length)
			if _, err := db.Exec(`UPDATE worker_attempts SET terminal_event_id=? WHERE work_id='work-history' AND attempt_id=?`, reportRef, attempt); err != nil {
				t.Fatal(err)
			}
			appendEvent(WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "accept_worker_result", WorkerAttemptID: attempt, ActorRef: progressActorRecord(t, db, "main", "coordinator")})
			state, err := loadWorkflowReviewState(t.Context(), db, "work-history")
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

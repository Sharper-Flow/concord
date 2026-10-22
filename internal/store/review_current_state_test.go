package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

func TestReviewCurrentDispositionAdmission(t *testing.T) {
	for _, disposition := range []string{"continue", "correction_required", "revision_required", "ready_for_final_proof"} {
		for _, group := range []string{"", "group:first"} {
			t.Run(disposition+"/"+group, func(t *testing.T) {
				report := reviewBlockFixture()
				report.Disposition = disposition
				state := WorkflowReviewState{Candidate: &report.OutputCandidate, Accepted: &WorkflowAcceptedReview{Report: *report}, DependenciesCurrent: true, Findings: []WorkerReviewFinding{}}
				before := string(mustJSONValue(state))
				err := workflowAdmitReview(state, group)
				wantAdmitted := group != "" && disposition == "continue" || group == "" && disposition == "ready_for_final_proof"
				if (err == nil) != wantAdmitted {
					t.Fatalf("admitted=%v, want %v: %v", err == nil, wantAdmitted, err)
				}
				if err != nil {
					failure := workflowFailureOf(err)
					if failure.RecoveryAction == "" || failure.RecoveryAction == "contact_operator" {
						t.Fatalf("refusal has no engineering recovery: %+v", failure)
					}
				}
				if after := string(mustJSONValue(state)); after != before {
					t.Fatal("admission mutated current state")
				}
			})
		}
	}
}

func TestReviewCurrentAdmissionNegativeCases(t *testing.T) {
	tests := []struct {
		name     string
		group    string
		change   func(*WorkflowReviewState)
		admitted bool
	}{
		{"unaccepted ready", "", func(s *WorkflowReviewState) { s.Accepted = nil }, false},
		{"new candidate", "", func(s *WorkflowReviewState) {
			copy := *s.Candidate
			copy.Commit = strings.Repeat("c", 40)
			s.Candidate = &copy
		}, false},
		{"changed depended-on revision", "", func(s *WorkflowReviewState) { s.DependenciesCurrent = false }, false},
		{"open in-scope blocker at exit", "", func(s *WorkflowReviewState) { s.Findings = reviewBlockFixture().Findings }, false},
		{"out-of-scope follow-up", "", func(s *WorkflowReviewState) {
			s.Findings = []WorkerReviewFinding{{FindingID: "finding:follow-up", InScope: false, Blocking: true, Status: "open"}}
		}, true},
		{"consumed continue cannot open another group", "group:second", func(s *WorkflowReviewState) {
			s.Accepted.Report.Disposition = "continue"
			s.ContinueConsumed = true
			s.GroupID = "group:first"
		}, false},
		{"second member needs no intermediate Review", "group:first", func(s *WorkflowReviewState) {
			s.Accepted.Report.Disposition = "continue"
			s.ContinueConsumed = true
			s.GroupID = "group:first"
			copy := *s.Candidate
			copy.Commit = strings.Repeat("c", 40)
			s.Candidate = &copy
		}, true},
		{"changed revision also invalidates same-group authority", "group:first", func(s *WorkflowReviewState) {
			s.Accepted.Report.Disposition = "continue"
			s.ContinueConsumed = true
			s.GroupID = "group:first"
			s.DependenciesCurrent = false
		}, false},
		{"blocker prevents opening a group", "group:first", func(s *WorkflowReviewState) {
			s.Accepted.Report.Disposition = "continue"
			s.Findings = reviewBlockFixture().Findings
		}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			report := reviewBlockFixture()
			report.Disposition = "ready_for_final_proof"
			state := WorkflowReviewState{Candidate: &report.OutputCandidate, Accepted: &WorkflowAcceptedReview{Report: *report}, DependenciesCurrent: true, Findings: []WorkerReviewFinding{}}
			tc.change(&state)
			before := string(mustJSONValue(state))
			err := workflowAdmitReview(state, tc.group)
			if (err == nil) != tc.admitted {
				t.Fatalf("admitted=%v, want %v: %v", err == nil, tc.admitted, err)
			}
			if string(mustJSONValue(state)) != before {
				t.Fatal("refusal or admission changed state")
			}
		})
	}
}

func TestReviewCurrentCandidateChangesOnlyOnMainAcceptance(t *testing.T) {
	db, appendEvent, _ := progressHistory(t, false)
	for _, statement := range []string{
		`CREATE TABLE workflow_contracts(work_id TEXT,contract_version INTEGER,superseded_by INTEGER)`,
		`CREATE TABLE workflow_contract_predicates(work_id TEXT,contract_version INTEGER,predicate_id TEXT,outcome_kind TEXT,outcome_payload BLOB)`,
		`CREATE TABLE workflow_design_records(work_id TEXT,work_version INTEGER,approach TEXT,decisions TEXT,touched_refs TEXT,recorded_at TEXT)`,
		`INSERT INTO workflow_contracts VALUES('work-history',1,NULL)`,
		`INSERT INTO workflow_contract_predicates VALUES('work-history',1,'predicate:primary','exists','{"kind":"exists","surface":"source","subjects":["owner"]}')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	dependencies, err := workflowReviewDependencies(context.Background(), db, "work-history")
	if err != nil {
		t.Fatal(err)
	}
	baseline := reviewBlockFixture()
	baseline.Disposition = "continue"
	baseline.Dependencies = dependencies
	baseline.Findings = []WorkerReviewFinding{}
	appendEvent(WorkerCompleted, WorkerCompletedPayload{AttemptID: "review:baseline", Review: baseline})
	appendEvent(WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "accept_worker_result", WorkerAttemptID: "review:baseline"})
	repair := reviewBlockFixture()
	repair.Dependencies = dependencies
	repair.Disposition = "ready_for_final_proof"
	repair.OutputCandidate.Commit = strings.Repeat("c", 40)
	repair.OutputCandidate.Tree = strings.Repeat("d", 40)
	repair.Findings[0].Status = "closed"
	repair.Findings[0].ClosureRefs = []string{"worktree_verify:repair"}
	appendEvent(WorkerCompleted, WorkerCompletedPayload{AttemptID: "review:repair", Review: repair})
	state, err := loadWorkflowReviewState(context.Background(), db, "work-history", 0)
	if err != nil {
		t.Fatal(err)
	}
	if *state.Candidate != baseline.OutputCandidate || state.Accepted.Report.Disposition != "continue" || state.blocker() == "" {
		t.Fatalf("unaccepted repair changed authority or closed its finding: %+v", state)
	}
	appendEvent(WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "accept_worker_result", WorkerAttemptID: "review:repair"})
	state, err = loadWorkflowReviewState(context.Background(), db, "work-history", 0)
	if err != nil {
		t.Fatal(err)
	}
	if *state.Candidate != repair.OutputCandidate || state.Accepted.Report.Disposition != "ready_for_final_proof" || state.blocker() != "" {
		t.Fatalf("accepted repair not installed atomically: %+v", state)
	}
	late := reviewBlockFixture()
	late.Dependencies = dependencies
	appendEvent(WorkerCompleted, WorkerCompletedPayload{AttemptID: "review:late-baseline", Review: late})
	state, err = loadWorkflowReviewState(context.Background(), db, "work-history", 0)
	if err != nil {
		t.Fatal(err)
	}
	if *state.Candidate != repair.OutputCandidate || state.Accepted.AttemptID != "review:repair" || state.blocker() != "" {
		t.Fatalf("late old-subject report replaced current authority: %+v", state)
	}
	appendEvent(WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "dispatch_worker", WorkerAttemptID: "member:late", WorkerCapabilityClass: "implementation", WorkerSubjectCommit: baseline.OutputCandidate.Commit})
	appendEvent(WorkerCompleted, WorkerCompletedPayload{AttemptID: "member:late", ObservedDelivery: &WorkerObservedDelivery{State: "clean", Commit: strings.Repeat("e", 40), Tree: strings.Repeat("f", 40)}})
	before := string(mustJSONValue(state))
	if err := validateReviewAcceptance(context.Background(), db, "work-history", "member:late", "main:fixture", 0); !hasFailureKind(err, KindStaleRequiresReview) {
		t.Fatalf("late member acceptance = %v, want stale refusal", err)
	}
	state, err = loadWorkflowReviewState(context.Background(), db, "work-history", 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(mustJSONValue(state)) != before {
		t.Fatal("refused late member changed current state")
	}

	open := reviewBlockFixture()
	open.InputCandidate, open.OutputCandidate, open.Dependencies = repair.OutputCandidate, repair.OutputCandidate, dependencies
	appendEvent(WorkerCompleted, WorkerCompletedPayload{AttemptID: "review:current-blocker", Review: open})
	omitted := *open
	omitted.Findings = []WorkerReviewFinding{}
	appendEvent(WorkerCompleted, WorkerCompletedPayload{AttemptID: "review:omitted-blocker", Review: &omitted})
	appendEvent(WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "accept_worker_result", WorkerAttemptID: "review:omitted-blocker"})
	state, err = loadWorkflowReviewState(context.Background(), db, "work-history", 0)
	if err != nil {
		t.Fatal(err)
	}
	if state.blocker() == "" {
		t.Fatal("omitting a current blocking finding closed it")
	}
}

func TestReviewCurrentCanonicalReportSchema(t *testing.T) {
	report := reviewBlockFixture()
	if err := payloadschema.Validate("lane_report_review_block", mustJSONValue(report)); err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(mustJSONValue(report), &value); err != nil {
		t.Fatal(err)
	}
	value["verdict"] = "ship"
	if err := payloadschema.Validate("lane_report_review_block", mustJSONValue(value)); err == nil {
		t.Fatal("canonical report admitted the retired verdict field")
	}
	delete(value, "verdict")
	for _, field := range []string{"input_candidate", "output_candidate", "dependencies", "assessments", "remaining_obligations"} {
		saved := value[field]
		delete(value, field)
		if err := payloadschema.Validate("lane_report_review_block", mustJSONValue(value)); err == nil {
			t.Fatalf("missing %s was admitted", field)
		}
		value[field] = saved
	}
}

func TestReviewCurrentFindingRetainsItsObligationBinding(t *testing.T) {
	for _, change := range []string{"omit oracle", "different owner", "different criterion", "different control"} {
		t.Run(change, func(t *testing.T) {
			db, appendEvent, _ := progressHistory(t, false)
			original := reviewBlockFixture()
			original.Findings[0].Oracle = &WorkerOracleFinding{
				Classification: OracleClassificationDeliveryBlocker,
				OwnerID:        "owner:original", PredicateIDs: []string{"predicate:primary"},
				ControlIDs: []string{"control:original"},
			}
			appendEvent(WorkerCompleted, WorkerCompletedPayload{AttemptID: "review:original", Review: original})
			var changed WorkerReviewBlock
			if err := json.Unmarshal(mustJSONValue(original), &changed); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "omit oracle":
				changed.Findings[0].Oracle = nil
			case "different owner":
				changed.Findings[0].Oracle.OwnerID = "owner:unrelated"
			case "different criterion":
				changed.Findings[0].Oracle.PredicateIDs = []string{"predicate:unrelated"}
			case "different control":
				changed.Findings[0].Oracle.ControlIDs = []string{"control:unrelated"}
			}
			appendEvent(WorkerCompleted, WorkerCompletedPayload{AttemptID: "review:changed", Review: &changed})
			state, err := loadWorkflowReviewState(context.Background(), db, "work-history", 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(state.Findings) != len(original.Findings) || string(mustJSONValue(state.Findings[0].Oracle)) != string(mustJSONValue(original.Findings[0].Oracle)) {
				t.Fatalf("a later claim replaced the finding's original obligation: %+v", state.Findings)
			}
			if err := validateReviewFindingBinding(state.Findings[0], changed.Findings[0]); !hasFailureKind(err, KindOutcomeMismatch) {
				t.Fatalf("acceptance binding check = %v, want outcome mismatch", err)
			}
			if err := validateReviewFindingBinding(state.Findings[0], original.Findings[0]); err != nil {
				t.Fatalf("unchanged obligation refused: %v", err)
			}
			var extended WorkerReviewFinding
			if err := json.Unmarshal(mustJSONValue(original.Findings[0]), &extended); err != nil { t.Fatal(err) }
			extended.Oracle.ControlIDs = append(extended.Oracle.ControlIDs, "control:additional")
			if err := validateReviewFindingBinding(state.Findings[0], extended); err != nil {
				t.Fatalf("additional coverage erased no original obligation but was refused: %v", err)
			}
		})
	}
}

func TestReviewCurrentProofAssociationHasNoCallerVerdict(t *testing.T) {
	for _, extra := range []string{`"verdict_kind":"ok",`, `"evaluation_evidence":["forged"],`, `"incomparable_with_approved":false,`} {
		raw := []byte(`{"predicate_id":"predicate:primary",` + extra + `"receipt_refs":["worktree_verify:captured"]}`)
		if err := payloadschema.Validate("workflow_verdict_batch_entry", raw); err == nil {
			t.Fatalf("caller proof authority was admitted: %s", raw)
		}
	}
	if err := payloadschema.Validate("workflow_verdict_batch_entry", []byte(`{"predicate_id":"predicate:primary","receipt_refs":["worktree_verify:captured"]}`)); err != nil {
		t.Fatal(err)
	}
}

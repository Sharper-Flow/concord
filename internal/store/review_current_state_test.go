package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

func TestReviewCurrentFirstGroupAdmission(t *testing.T) {
	for _, tc := range []struct {
		name       string
		progressed bool
		group      string
		blocker    bool
		want       bool
	}{
		{"first entry needs no baseline Review", false, "group:first", false, true},
		{"zero-change execution still needs accepted readiness", false, "", false, false},
		{"missing Review cannot reset execution", true, "group:second", false, false},
		{"blocker holds even first entry", false, "group:first", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := WorkflowReviewState{ExecutionProgressed: tc.progressed, Findings: []WorkerReviewFinding{}}
			if tc.blocker {
				state.Findings = reviewBlockFixture().Findings
			}
			if err := workflowAdmitReview(state, tc.group); (err == nil) != tc.want {
				t.Fatalf("admitted=%v, want %v: %v", err == nil, tc.want, err)
			}
		})
	}
}

func TestReviewCurrentFirstEntryKeepsSharedAdmissionGuards(t *testing.T) {
	entry, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	base := WorkflowAdmissionState{Step: "execution", InstanceState: "running", Lifecycle: "in_progress", ActiveContracts: 1,
		ReviewLane: "implementation", ReviewGroup: "group:first", Review: &WorkflowReviewState{}}
	if decision := workflowAdmit(entry.Definition, base, "dispatch_worker"); !decision.Admitted {
		t.Fatalf("initial dispatch was not admitted: %+v", decision.Failure)
	}
	for _, tc := range []struct {
		name string
		set  func(*WorkflowAdmissionState)
		kind FailureKind
	}{
		{"stale plan", func(s *WorkflowAdmissionState) { s.DesignStale = true }, KindMissingEvidence},
		{"stale authority", func(s *WorkflowAdmissionState) {
			s.LawPinStaleError = newFailure(KindStaleRequiresReview, "test", "bound authority changed", false, "refresh the bound inputs")
		}, KindStaleRequiresReview},
		{"unknown launch", func(s *WorkflowAdmissionState) { s.PendingWorkerAuthorization = true }, KindOperationConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := base
			tc.set(&state)
			decision := workflowAdmit(entry.Definition, state, "dispatch_worker")
			if decision.Admitted || decision.Failure == nil || decision.Failure.Kind != tc.kind {
				t.Fatalf("first entry bypassed %s: %+v", tc.name, decision)
			}
		})
	}
}

func TestReviewCurrentPlanRevisionCannotRestoreFirstEntry(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		name := "missing Review"
		if accepted {
			name = "stale Review"
		}
		t.Run(name, func(t *testing.T) {
			f := seedWorkContextFixture(t, "review-plan-revision")
			t.Cleanup(func() { _ = f.store.Close() })
			repo, _ := reviewSubjectClaim(t, f.store, f.workID)
			var branch string
			if err := f.store.db.QueryRow(`SELECT branch FROM worktree_entries WHERE set_id=? AND state='active'`, WorktreeSetID(f.workID)).Scan(&branch); err != nil {
				t.Fatal(err)
			}
			gitRunStore(t, repo, "branch", "-m", branch)
			reviewSubjectImplementComplete(t, f, "member:first", "member:first:completed", true)
			reviewSubjectAccept(t, f.store, f.workID, "member:first")
			if accepted {
				acceptCurrentReviewForTest(t, f, repo, "continue")
			}
			before, err := ReadWorkPin(t.Context(), f.store, f.workID)
			if err != nil || before.Review == nil || !before.Review.ExecutionProgressed {
				t.Fatalf("completed implementation is absent from current execution state: %+v, %v", before.Review, err)
			}
			owner, err := WorkflowActorRef(f.owner)
			if err != nil {
				t.Fatal(err)
			}
			// The design owner's normal fold writes a valid replacement. It
			// changes a depended-on input, not the execution's progress record.
			event := workflowEventWithActor("design:replacement", WorkflowDesignRecorded, f.workID, owner, map[string]any{
				"work_id": f.workID, "expected_version": before.Version, "resulting_version": before.Version + 1,
				"approach":     "Keep the accepted value while changing its implementation plan.",
				"decisions":    []map[string]any{{"id": "decision:plan", "question": "Which implementation plan applies?", "choice": "Use the revised plan.", "rationale": "The inputs changed.", "rejected": []string{}}},
				"touched_refs": []string{"seed.txt"},
			})
			if err := applyWorkflowTestOperation(t.Context(), f.store, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, f.workID): before.Version}}); err != nil {
				t.Fatal(err)
			}
			after, err := ReadWorkPin(t.Context(), f.store, f.workID)
			if err != nil || after.Review == nil || !after.Review.ExecutionProgressed || after.Review.DependenciesCurrent || after.Review.Dependencies.ArchitectureDigest == before.Review.Dependencies.ArchitectureDigest {
				t.Fatalf("plan replacement reset progress or retained stale authority: %+v, %v", after.Review, err)
			}
			if err := workflowAdmitReview(*after.Review, "group:next"); !hasFailureKind(err, KindMissingEvidence) {
				t.Fatalf("plan replacement reopened first entry: %v", err)
			}
		})
	}
}

func TestReviewCurrentUnstartedAuthorizationPreservesFirstEntry(t *testing.T) {
	f := seedWorkContextFixture(t, "review-confirmed-no-start")
	t.Cleanup(func() { _ = f.store.Close() })
	s := f.store
	repo, _ := reviewSubjectClaim(t, s, f.workID)
	var branch string
	if err := s.db.QueryRow(`SELECT branch FROM worktree_entries WHERE set_id=? AND state='active'`, WorktreeSetID(f.workID)).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, repo, "branch", "-m", branch)
	j := &candidateJourney{fixture: f, s: s, workID: f.workID, repo: repo}
	job := reviewSubjectReadyRevision(t, f)
	const attempt = "attempt:confirmed-no-start"
	if err := descendantDispatch(t, j, "implement", attempt, job, nil); err != nil {
		t.Fatal(err)
	}
	before, err := ReadWorkPin(t.Context(), s, f.workID)
	if err != nil || before.Review == nil || before.Review.Candidate != nil || before.Review.Groups[job.JobID].Permission != "first_entry" {
		t.Fatalf("authorization lost its first-entry assignment or invented a candidate: %+v, %v", before.Review, err)
	}
	// The existing host terminal boundary refuses abandonment while a worker
	// still owns the worktree. This fixture has no worker occupancy or start.
	event := s2WorkerFailedAttributedEvent(f.workID, attempt, "confirmed-no-start:abandoned", "", WorkerFailureAbandoned, "The host confirmed the task never started.", time.Now().UTC())
	if err := s2WorkerFailedLive(t, s, event); err != nil {
		t.Fatal(err)
	}
	after, err := ReadWorkPin(t.Context(), s, f.workID)
	if err != nil || after.Review == nil || after.Review.Candidate != nil || after.Review.Groups[job.JobID] != before.Review.Groups[job.JobID] {
		t.Fatalf("confirmed never-started launch changed its initial assignment: %+v, %v", after.Review, err)
	}
	if err := workflowAdmitReview(*after.Review, job.JobID); err != nil {
		t.Fatalf("the unchanged initial assignment now requires a baseline Review: %v", err)
	}
	if err := workflowAdmitReview(*after.Review, "group:unrelated"); !hasFailureKind(err, KindMissingEvidence) {
		t.Fatalf("an unstarted launch reopened first entry for another assignment: %v", err)
	}
}

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
	db, appendEvent, _ := progressHistory(t)
	for _, statement := range []string{
		`CREATE TABLE worker_job_revisions(work_id TEXT,job_id TEXT,revision INTEGER,group_permission TEXT,group_review_ref TEXT,correction_ref TEXT)`,
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
	appendReview := func(attempt string, report *WorkerReviewBlock) {
		appendEvent(WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "dispatch_worker", WorkerAttemptID: attempt, WorkerCapabilityClass: "review", WorkerSubjectCommit: report.InputCandidate.Commit})
		appendEvent(WorkerCompleted, WorkerCompletedPayload{AttemptID: attempt, Review: report})
	}
	appendReview("review:baseline", baseline)
	appendEvent(WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "accept_worker_result", WorkerAttemptID: "review:baseline"})
	repair := reviewBlockFixture()
	repair.Dependencies = dependencies
	repair.Disposition = "ready_for_final_proof"
	repair.OutputCandidate.Commit = strings.Repeat("c", 40)
	repair.OutputCandidate.Tree = strings.Repeat("d", 40)
	repair.Findings[0].Status = "closed"
	repair.Findings[0].ClosureRefs = []string{"worktree_verify:repair"}
	appendReview("review:repair", repair)
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
	appendReview("review:late-baseline", late)
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
	appendReview("review:current-blocker", open)
	omitted := *open
	omitted.Findings = []WorkerReviewFinding{}
	appendReview("review:omitted-blocker", &omitted)
	appendEvent(WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "accept_worker_result", WorkerAttemptID: "review:omitted-blocker"})
	state, err = loadWorkflowReviewState(context.Background(), db, "work-history", 0)
	if err != nil {
		t.Fatal(err)
	}
	if state.blocker() == "" {
		t.Fatal("omitting a current blocking finding closed it")
	}
	before = string(mustJSONValue(state))
	appendEvent(WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "dispatch_worker", WorkerAttemptID: "member:review-claim", WorkerCapabilityClass: "implementation", WorkerSubjectCommit: repair.OutputCandidate.Commit})
	appendEvent(WorkerCompleted, WorkerCompletedPayload{AttemptID: "member:review-claim", Review: &omitted})
	appendEvent(WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "accept_worker_result", WorkerAttemptID: "member:review-claim"})
	state, err = loadWorkflowReviewState(context.Background(), db, "work-history", 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(mustJSONValue(state)) != before {
		t.Fatal("an accepted non-Review assignment acquired Review authority")
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
			db, appendEvent, _ := progressHistory(t)
			if _, err := db.Exec(`CREATE TABLE workflow_contracts(work_id TEXT,contract_version INTEGER,superseded_by INTEGER); CREATE TABLE worker_job_revisions(work_id TEXT,job_id TEXT,revision INTEGER,group_permission TEXT,group_review_ref TEXT,correction_ref TEXT)`); err != nil {
				t.Fatal(err)
			}
			original := reviewBlockFixture()
			original.Findings[0].Oracle = &WorkerOracleFinding{
				Classification: OracleClassificationDeliveryBlocker,
				OwnerID:        "owner:original", PredicateIDs: []string{"predicate:primary"},
				ControlIDs: []string{"control:original"},
			}
			appendEvent(WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "dispatch_worker", WorkerAttemptID: "review:original", WorkerCapabilityClass: "review", WorkerSubjectCommit: original.InputCandidate.Commit})
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
			appendEvent(WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "dispatch_worker", WorkerAttemptID: "review:changed", WorkerCapabilityClass: "review", WorkerSubjectCommit: changed.InputCandidate.Commit})
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
			if err := json.Unmarshal(mustJSONValue(original.Findings[0]), &extended); err != nil {
				t.Fatal(err)
			}
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

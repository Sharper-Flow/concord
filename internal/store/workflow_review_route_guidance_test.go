package store

import (
	"strings"
	"testing"
)

func requireReviewRouteGuidance(t *testing.T, guidance string, prerequisites ...string) {
	t.Helper()
	for _, want := range []string{"typed dispatch_worker", "lane_id=review", "existing scoped job", "native concord-review Task subagent", "under Main's own session", "finish an already dispatched undispositioned attempt only if it meets this gate's required coverage and disposition; do not dispatch twice", "then have Main accept its exact Review report", "no separate agent or RTE session is required"} {
		if !strings.Contains(guidance, want) {
			t.Fatalf("guidance %q does not name %q", guidance, want)
		}
	}
	dispatch := strings.Index(guidance, "typed dispatch_worker")
	for _, prerequisite := range prerequisites {
		if at := strings.Index(guidance, prerequisite); at < 0 || at >= dispatch {
			t.Fatalf("guidance %q must place prerequisite %q before Review dispatch", guidance, prerequisite)
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
		name          string
		group         string
		change        func(*WorkflowReviewState)
		kind          FailureKind
		detail        string
		coverage      string
		prerequisites []string
	}{
		{name: "revised job", group: "job:next", change: func(s *WorkflowReviewState) { s.StaleJobs = map[string]bool{"job:next": true} }, kind: KindMissingEvidence, detail: "a changed worker-job revision requires a current accepted Review", coverage: "covering the revised job"},
		{name: "blocking finding", change: func(s *WorkflowReviewState) { s.Findings = reviewBlockFixture().Findings }, kind: KindMissingEvidence, detail: "open in-scope blocking finding finding:primary blocks execution progression", coverage: "carrying the evidenced closure of finding:primary", prerequisites: []string{"repair the blocking finding finding:primary", "capture its closure evidence"}},
		{name: "missing review", change: func(s *WorkflowReviewState) { s.Accepted = nil }, kind: KindMissingEvidence, detail: "execution progression requires an accepted Review on the current candidate and depended-on revisions", coverage: "covering the current integrated candidate and depended-on revisions"},
		{name: "changed candidate", change: func(s *WorkflowReviewState) {
			candidate := *s.Candidate
			candidate.Commit = strings.Repeat("c", 40)
			s.Candidate = &candidate
		}, kind: KindMissingEvidence, detail: "execution progression requires an accepted Review on the current candidate and depended-on revisions", coverage: "covering the current integrated candidate and depended-on revisions"},
		{name: "changed dependencies", change: func(s *WorkflowReviewState) { s.DependenciesCurrent = false }, kind: KindMissingEvidence, detail: "execution progression requires an accepted Review on the current candidate and depended-on revisions", coverage: "depended-on revisions"},
		{name: "correction before continuation", group: "job:next", change: func(s *WorkflowReviewState) { s.Accepted.Report.Disposition = "correction_required" }, kind: KindMissingEvidence, detail: "opening an ordinary assignment group requires accepted continue, not correction_required", coverage: "disposition continue for implementation of the same bound job", prerequisites: []string{"follow the report's declared corrective/upstream obligation"}},
		{name: "consumed continuation", group: "job:next", change: func(s *WorkflowReviewState) {
			s.ContinueConsumed, s.GroupID = true, "job:previous"
		}, kind: KindOperationConflict, detail: "this continue permission has already opened group job:previous", coverage: "new continue disposition for implementation of the same bound job", prerequisites: []string{"finish group job:previous", "integrate its results"}},
		{name: "final proof", kind: KindMissingEvidence, detail: "leaving execution requires accepted ready_for_final_proof, not continue", coverage: "disposition ready_for_final_proof on the current candidate before leaving execution", prerequisites: []string{"follow any declared corrective/upstream obligation"}},
		{name: "architecture revision", change: func(s *WorkflowReviewState) {
			s.Dependencies = &WorkerReviewDependencies{ArchitectureNotApplicable: false}
			s.DependenciesCurrent = false
		}, kind: KindMissingEvidence, detail: "execution progression requires an accepted Review covering the current architecture and criteria", coverage: "covering the current architecture and criteria", prerequisites: []string{"use request_correction if the architecture needs revision"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := reviewBlockFixture()
			report.Disposition = "continue"
			state := WorkflowReviewState{Candidate: &report.OutputCandidate, Accepted: &WorkflowAcceptedReview{Report: *report}, DependenciesCurrent: true, ExecutionProgressed: true}
			if tc.change != nil {
				tc.change(&state)
			}
			before := string(mustJSONValue(state))
			failure := reviewRouteFailure(t, workflowAdmitReview(state, tc.group))
			if failure == nil || failure.Kind != tc.kind || failure.Op != "workflow_review" || failure.Detail != tc.detail || failure.RetrySafe {
				t.Fatalf("Review admission changed its refusal identity: %+v", failure)
			}
			requireReviewRouteGuidance(t, failure.RecoveryAction, tc.prerequisites...)
			if !strings.Contains(failure.RecoveryAction, tc.coverage) {
				t.Fatalf("guidance lost gate obligation %q: %s", tc.coverage, failure.RecoveryAction)
			}
			if after := string(mustJSONValue(state)); after != before {
				t.Fatal("guidance changed admission state")
			}
		})
	}
}

func TestReviewRouteGuidanceUnsatisfiedJob(t *testing.T) {
	t.Run("unchanged unsatisfied job", func(t *testing.T) {
		failure := workflowDeliveryJobsFailure(workflowDeliveryAdmission{JobsUnsatisfiedKeys: "job:unsatisfied|1"}, nil)
		if failure == nil || failure.Kind != KindMissingEvidence || !strings.Contains(failure.Detail, "remain unsatisfied") {
			t.Fatalf("unsatisfied job refusal changed: %+v", failure)
		}
		if !strings.HasPrefix(failure.RecoveryAction, "execute and accept the listed unsatisfied worker jobs before whole-work delivery; do not re-record unchanged content to obtain satisfaction;") {
			t.Fatalf("unchanged job guidance lost its direct satisfaction route: %s", failure.RecoveryAction)
		}
		requireReviewRouteGuidance(t, failure.RecoveryAction, "if a job truly changes, record and prepare it")
		if !strings.Contains(failure.RecoveryAction, "covering the revised job before implementation") {
			t.Fatalf("conditional Review guidance lost its revised-job obligation: %s", failure.RecoveryAction)
		}
	})
	t.Run("integration only", func(t *testing.T) {
		failure := workflowDeliveryJobsFailure(workflowDeliveryAdmission{JobsRecorded: true, JobsIntegrated: false}, nil)
		if failure == nil || failure.Kind != KindMissingEvidence || failure.Detail != "the delivery assertion requires passing producer verification on the current integrated candidate, covering every required Project" {
			t.Fatalf("integration-only refusal changed: %+v", failure)
		}
		if !strings.Contains(failure.RecoveryAction, "run worktree_verify") || strings.Contains(failure.RecoveryAction, "dispatch_worker") {
			t.Fatalf("integration-only refusal lost its verification-only recovery: %+v", failure)
		}
	})
}

func TestReviewRouteGuidanceDispatchQualification(t *testing.T) {
	f := seedWorkContextFixture(t, "review-route-qualification")
	t.Cleanup(func() { _ = f.store.Close() })
	repo, head := reviewSubjectClaim(t, f.store, f.workID)
	tx, err := f.store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, tc := range []struct {
		name, subject, identity, detail, prerequisite string
		kind                                          FailureKind
	}{
		{"empty subject", "", workerWorktreeIdentity(repo), "review of implementation work requires a nonempty qualified subject; qualify the accepted candidate through worktree-verify before review", "run worktree-verify on the clean accepted candidate and rebuild the packet", KindUnauthorizedDispatch},
		{"unqualified subject", strings.Repeat("f", 40), workerWorktreeIdentity(repo), "Review input is not the qualified candidate on its claimed worktree", "qualify the clean current candidate through worktree-verify", KindMissingEvidence},
		{"wrong worktree", head, "other-worktree", "Review input is not the qualified candidate on its claimed worktree", "qualify the clean current candidate through worktree-verify", KindMissingEvidence},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failure := reviewRouteFailure(t, validateReviewAcceptedDeliveryTx(t.Context(), tx, f.workID, tc.subject, tc.identity))
			if failure == nil || failure.Kind != tc.kind || failure.Detail != tc.detail {
				t.Fatalf("subject refusal changed: %+v", failure)
			}
			requireReviewRouteGuidance(t, failure.RecoveryAction, tc.prerequisite)
		})
	}
	definition := mustBuiltinDefinition(t, "workflow.break_fix").Definition
	_, err = currentWorkerReviewInput(t.Context(), tx, f.workID, LaneDefinition{CapabilityClass: "review"}, definition)
	failure := reviewRouteFailure(t, err)
	if failure == nil || failure.Kind != KindMissingEvidence || failure.Detail != "Review input requires the native candidate tree observation" {
		t.Fatalf("missing tree refusal changed: %+v", failure)
	}
	requireReviewRouteGuidance(t, failure.RecoveryAction, "prepare dispatch from the qualified clean candidate with its native tree observation")
}

func TestReviewRouteGuidanceAcceptance(t *testing.T) {
	j := pReturnReviewFixture(t, "review-route-acceptance", "workflow.break_fix", "ready_for_final_proof")
	state, err := loadWorkflowReviewState(t.Context(), j.s.db, j.workID)
	if err != nil || state.Accepted == nil {
		t.Fatalf("accepted Review fixture: %+v, %v", state, err)
	}
	auth, result, err := readReviewAttempt(t.Context(), j.s.db, j.workID, state.Accepted.AttemptID)
	if err != nil || result.Review == nil {
		t.Fatalf("terminal Review fixture: %+v, %v", result, err)
	}
	for _, tc := range []struct {
		name, detail, prerequisite string
		change                     func(*workflowActionCompletedPayload, *WorkerReviewBlock)
		kind                       FailureKind
	}{
		{"unbound assignment", "Review has no admitted assignment binding", "bind the packet to the recorded scoped assignment", func(a *workflowActionCompletedPayload, _ *WorkerReviewBlock) { a.WorkerJob = nil }, KindMissingEvidence},
		{"replaced input", "a newer accepted candidate replaced this Review's input", "retain this report as evidence of its original subject", func(_ *workflowActionCompletedPayload, r *WorkerReviewBlock) {
			r.InputCandidate.Commit = strings.Repeat("f", 40)
		}, KindStaleRequiresReview},
		{"changed criteria", "Review judged different criterion or architecture content", "refresh the packet's depended-on revisions", func(_ *workflowActionCompletedPayload, r *WorkerReviewBlock) {
			r.Dependencies.Criteria = []WorkerReviewCriterion{{PredicateID: "predicate:changed", ContentDigest: "sha256:" + strings.Repeat("f", 64)}}
		}, KindStaleRequiresReview},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempt, report := auth, *result.Review
			tc.change(&attempt, &report)
			failure := reviewRouteFailure(t, validateReviewAssignmentAcceptance(t.Context(), j.s.db, j.workID, attempt, &report))
			if failure.Kind != tc.kind || failure.Op != "workflow_review" || failure.Detail != tc.detail {
				t.Fatalf("Review acceptance refusal changed: %+v", failure)
			}
			requireReviewRouteGuidance(t, failure.RecoveryAction, tc.prerequisite)
		})
	}
	failure := reviewRouteFailure(t, workflowReviewedCommandReceipt(t.Context(), j.s.db, j.workID, "predicate:missing", "receipt:command", state.Candidate.Commit, state.Candidate.ProjectID))
	requireReviewRouteGuidance(t, failure.RecoveryAction, "include the captured passing command receipt receipt:command in the Review inputs")
	if !strings.Contains(failure.RecoveryAction, "assessing criterion predicate:missing on the final candidate and citing that receipt") {
		t.Fatalf("command assessment guidance lost its coverage: %s", failure.RecoveryAction)
	}
	t.Run("missing terminal report", func(t *testing.T) {
		actor, err := WorkflowActorRef(j.fixture.owner)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := j.s.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(t.Context(), `INSERT INTO write_guard(active) VALUES(1)`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(t.Context(), `UPDATE worker_attempts SET terminal_payload=json_remove(terminal_payload,'$.review') WHERE work_id=? AND attempt_id=?`, j.workID, state.Accepted.AttemptID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(t.Context(), `DELETE FROM write_guard`); err != nil {
			t.Fatal(err)
		}
		failure := reviewRouteFailure(t, validateReviewAcceptance(t.Context(), tx, j.workID, state.Accepted.AttemptID, actor))
		if failure.Kind != KindMissingEvidence || failure.Detail != "Review has no exact terminal report" {
			t.Fatalf("missing terminal report refusal changed: %+v", failure)
		}
		requireReviewRouteGuidance(t, failure.RecoveryAction, "retain the incomplete terminal result as evidence")
		failure = reviewRouteFailure(t, workflowAcceptanceSubjectCurrent(t.Context(), tx, j.workID, []string{state.Accepted.AttemptID}, nil))
		if failure.Kind != KindMissingEvidence || failure.Detail != "accepted Review has no output candidate" {
			t.Fatalf("missing output candidate refusal changed: %+v", failure)
		}
		requireReviewRouteGuidance(t, failure.RecoveryAction)
	})
}

func TestReviewRouteGuidanceWorkPinCorrection(t *testing.T) {
	for _, family := range []string{"workflow.implementation", "workflow.break_fix"} {
		t.Run(family, func(t *testing.T) {
			j := reviewGateRefineFixture(t, "review-route-pin", family)
			attempt, epoch := reviewGateCompleteReview(t, j, "correction_required")
			if err := reviewGateAcceptResult(t, j.s, j.workID, attempt, epoch, j.fixture.owner); err != nil {
				t.Fatal(err)
			}
			reviewGateRequireStep(t, j.s, j.workID, "refine")
			pin, err := ReadWorkPin(t.Context(), j.s, j.workID)
			if err != nil {
				t.Fatal(err)
			}
			if !workPinContainsAction(pin.NextValidIntents, "request_correction") {
				t.Fatalf("pin lost its admitted correction request: %+v", pin)
			}
			_, admission := loadedAdmissionForTest(t, j.s, j.workID, pin)
			correction := admission.CorrectionRequestContext
			if correction == nil {
				t.Fatal("admitted correction request has no derived guidance")
			}
			if err := j.action(t, "request_correction", map[string]any{
				"diagnosis": correction.Diagnosis, "strategy": correction.Strategy,
				"predicate_ids": correction.PredicateIDs, "evidence_refs": []string{"evidence:return-route-verification"},
			}, j.fixture.owner); err != nil {
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
				t.Fatalf("pin lost the recorded correction request's guidance or provenance: %+v", pin.Correction)
			}
			requireReviewRouteGuidance(t, pin.Correction.Strategy, "perform the declared corrective work")
			if !strings.Contains(pin.Correction.Strategy, "covering the corrected candidate") {
				t.Fatalf("pin strategy lost its subject: %s", pin.Correction.Strategy)
			}
			failure := workflowCorrectionRequestUnavailableFailure("workflow_action", workflowCorrectionMissingFreshReview)
			if failure.Kind != KindInvalidOperation {
				t.Fatalf("consumed correction refusal changed: %+v", failure)
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
		t.Fatalf("guidance changed the corrective return's evidence source: %+v", correction)
	}
}

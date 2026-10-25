package store

import (
	"reflect"
	"strings"
	"testing"
)

// TestDeliveryGateCorrectionAdmitsInapplicableReview proves the gate's
// corrective return over the real rows behind an accepted Review: an
// applicable accepted Review opens no return, a successor contract's stale
// dependency or changed output candidate opens the declared producer return,
// and the accepted report is its evidence source.
func TestDeliveryGateCorrectionAdmitsInapplicableReview(t *testing.T) {
	f := seedWorkContextFixture(t, "gate-correction-inapplicable-review")
	t.Cleanup(func() { _ = f.store.Close() })
	repo, _ := reviewSubjectClaim(t, f.store, f.workID)
	var branch string
	if err := f.store.db.QueryRow(`SELECT branch FROM worktree_entries WHERE set_id=? AND state='active'`, WorktreeSetID(f.workID)).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, repo, "branch", "-m", branch)
	journey := &candidateJourney{fixture: f, s: f.store, workID: f.workID, repo: repo}
	job := WorkerJobBinding{JobID: "job:gate-correction"}
	conformanceImplementComplete(t, journey, &job, "attempt:gate-correction")
	window := reviewSubjectWindow(t, f.store, f.workID, "attempt:gate-correction")
	if err := journey.action(t, "accept_worker_result", map[string]any{"attempt_id": "attempt:gate-correction", "attempt_epoch": window.AttemptEpoch}, f.owner); err != nil {
		t.Fatal(err)
	}
	journey.observe(t, "gate-correction-implementation")
	attempt, epoch := completeCurrentReviewForTest(t, f, repo, "ready_for_final_proof")
	if err := journey.action(t, "accept_worker_result", map[string]any{"attempt_id": attempt, "attempt_epoch": epoch}, f.owner); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	loaded, err := loadWorkflowReviewState(ctx, f.store.db, f.workID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Accepted == nil || loaded.Candidate == nil || !loaded.DependenciesCurrent {
		t.Fatalf("accepted fixture Review did not fold applicable: %+v", loaded)
	}
	definition := pinnedWorkflowDefinitionForTest(t, f.store, f.workID)
	fold := func(label string, mutate func(*WorkflowReviewState)) *WorkflowCorrectionContext {
		t.Helper()
		review := loaded
		if mutate != nil {
			mutate(&review)
		}
		state := WorkflowAdmissionState{ActiveContracts: 1, Review: &review}
		context, err := workflowDeliveryGateCorrectionContextFolded(ctx, f.store.db, f.workID, definition, "delivery", "workflow_review_conformance_test", state)
		if err != nil {
			t.Fatalf("%s: gate correction fold: %v", label, err)
		}
		return context
	}
	if got := fold("applicable", nil); got != nil {
		t.Fatalf("an applicable accepted Review opened a gate return: %+v", got)
	}
	if got := fold("unaccepted", func(review *WorkflowReviewState) { review.Accepted = nil }); got != nil {
		t.Fatalf("an unaccepted Review opened a gate return: %+v", got)
	}
	target := workflowCorrectionReturnTarget(definition, "delivery")
	if target == "" {
		t.Fatal("the gate declares no corrective return target")
	}
	stale := fold("stale dependencies", func(review *WorkflowReviewState) { review.DependenciesCurrent = false })
	if stale == nil || stale.SourceEventID != loaded.Accepted.ReportRef || len(stale.PredicateIDs) == 0 {
		t.Fatalf("stale dependencies folded %+v, want the accepted report's corrective return with contract predicates", stale)
	}
	changed := fold("changed output", func(review *WorkflowReviewState) {
		candidate := *review.Candidate
		candidate.Commit = strings.Repeat("f", 40)
		review.Candidate = &candidate
	})
	if changed == nil || changed.SourceEventID != loaded.Accepted.ReportRef {
		t.Fatalf("changed output folded %+v, want the accepted report's corrective return", changed)
	}
}

func TestCurrentReviewAdmissionLoaderConformsToAcceptedDisposition(t *testing.T) {
	for _, disposition := range []string{"continue", "correction_required", "revision_required", "ready_for_final_proof"} {
		t.Run(disposition, func(t *testing.T) {
			f := seedWorkContextFixture(t, "review-conformance-"+disposition)
			t.Cleanup(func() { _ = f.store.Close() })
			repo, _ := reviewSubjectClaim(t, f.store, f.workID)
			var branch string
			if err := f.store.db.QueryRow(`SELECT branch FROM worktree_entries WHERE set_id=? AND state='active'`, WorktreeSetID(f.workID)).Scan(&branch); err != nil {
				t.Fatal(err)
			}
			gitRunStore(t, repo, "branch", "-m", branch)
			journey := &candidateJourney{fixture: f, s: f.store, workID: f.workID, repo: repo}
			job := WorkerJobBinding{JobID: "job:conformance"}
			conformanceImplementComplete(t, journey, &job, "member:conformance")
			window := reviewSubjectWindow(t, f.store, f.workID, "member:conformance")
			if err := journey.action(t, "accept_worker_result", map[string]any{"attempt_id": "member:conformance", "attempt_epoch": window.AttemptEpoch}, f.owner); err != nil {
				t.Fatal(err)
			}
			journey.observe(t, "accepted-implementation")
			head := journey.head(t)
			candidate := WorkerReviewCandidate{ProjectID: "project", Commit: head, Tree: reviewObservedTree(t, repo, head)}
			dependencies, err := workflowReviewDependencies(t.Context(), f.store.db, f.workID)
			if err != nil {
				t.Fatal(err)
			}
			model := WorkflowReviewState{
				Groups:              map[string]workflowAssignmentGroup{job.JobID: {Permission: "first_entry"}},
				ExecutionProgressed: true, Dependencies: &dependencies, Candidate: &candidate,
				Findings: []WorkerReviewFinding{}, PublicationOutstanding: true,
			}
			definition := pinnedWorkflowDefinitionForTest(t, f.store, f.workID)
			global := admissionModelState{step: "repair", contracts: 1, review: admissionModelReview{candidate: true, progressed: true, group: "first_entry"}}
			check := func(label string) WorkflowAdmissionState {
				t.Helper()
				loaded := conformanceFold(t, f.store, BuiltinWorkflowRegistry(), f.workID, definition, label)
				if loaded.Review == nil || !reflect.DeepEqual(*loaded.Review, model) {
					t.Fatalf("%s: loaded Review %+v differs from declared model %+v", label, loaded.Review, model)
				}
				lifted := admissionWorkflowState(definition, global)
				if got, want := admissionReviewViewOf(loaded.Review), admissionReviewViewOf(lifted.Review); got != want {
					t.Fatalf("%s: real Review %+v != global model %+v", label, got, want)
				}
				if loaded.Step != "repair" {
					t.Fatalf("%s: local intake advanced to %s", label, loaded.Step)
				}
				return loaded
			}
			before := check("after implementation acceptance")
			if err := workflowAdmitReview(*before.Review, "group:next"); !hasFailureKind(err, KindMissingEvidence) {
				t.Fatalf("implementation acceptance opened an unrelated group without Review: %v", err)
			}
			attempt, epoch := completeCurrentReviewForTest(t, f, repo, disposition)
			global.attempt, global.review.report = "completed", disposition
			terminal := check("after unaccepted Review terminal")
			if err := workflowAdmitReview(*terminal.Review, ""); !hasFailureKind(err, KindMissingEvidence) {
				t.Fatalf("an unaccepted %s report authorized execution exit: %v", disposition, err)
			}
			if err := journey.action(t, "accept_worker_result", map[string]any{"attempt_id": attempt, "attempt_epoch": epoch}, f.owner); err != nil {
				t.Fatalf("Main acceptance of %s: %v", disposition, err)
			}
			actor, err := WorkflowActorRef(f.owner)
			if err != nil {
				t.Fatal(err)
			}
			report := WorkerReviewBlock{Disposition: disposition, InputCandidate: candidate, OutputCandidate: candidate, Dependencies: dependencies,
				Assessments: []WorkerReviewAssessment{}, Findings: []WorkerReviewFinding{}, RemainingObligations: []WorkerReviewObligation{}}
			model.Accepted = &WorkflowAcceptedReview{AttemptID: attempt, ReportRef: attempt + ":completed", ReportDigest: reviewDigest(report), ActorRef: actor, Report: report}
			model.StaleJobs = map[string]bool{}
			model.DependenciesCurrent = true
			global = admissionSuccessors(definition, global, "accept_worker_result")[0]
			accepted := check("after Main acceptance")
			for _, group := range []string{"", "group:next"} {
				want := group == "" && disposition == "ready_for_final_proof" || group != "" && disposition == "continue"
				if err := workflowAdmitReview(*accepted.Review, group); (err == nil) != want {
					t.Fatalf("accepted %s, group %q: admitted=%v, want %v: %v", disposition, group, err == nil, want, err)
				}
				lifted := admissionWorkflowState(definition, global)
				if err := workflowAdmitReview(*lifted.Review, group); (err == nil) != want {
					t.Fatalf("global model accepted %s, group %q: admitted=%v, want %v: %v", disposition, group, err == nil, want, err)
				}
			}
		})
	}
}

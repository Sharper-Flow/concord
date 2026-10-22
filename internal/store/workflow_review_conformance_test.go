package store

import (
	"reflect"
	"testing"
)

func TestCurrentReviewAdmissionLoaderConformsToAcceptedDisposition(t *testing.T) {
	for _, disposition := range []string{"continue", "correction_required", "revision_required", "ready_for_final_proof"} {
		t.Run(disposition, func(t *testing.T) {
			f := seedWorkContextFixture(t, "review-conformance-"+disposition)
			t.Cleanup(func() { _ = f.store.Close() })
			repo, head := reviewSubjectClaim(t, f.store, f.workID)
			var branch string
			if err := f.store.db.QueryRow(`SELECT branch FROM worktree_entries WHERE set_id=? AND state='active'`, WorktreeSetID(f.workID)).Scan(&branch); err != nil {
				t.Fatal(err)
			}
			gitRunStore(t, repo, "branch", "-m", branch)
			job := reviewSubjectImplementComplete(t, f, "member:conformance", "member:conformance:completed", true)
			reviewSubjectAccept(t, f.store, f.workID, "member:conformance")
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
			check := func(label string) WorkflowAdmissionState {
				t.Helper()
				loaded := conformanceFold(t, f.store, BuiltinWorkflowRegistry(), f.workID, definition, label)
				if loaded.Review == nil || !reflect.DeepEqual(*loaded.Review, model) {
					t.Fatalf("%s: loaded Review %+v differs from declared model %+v", label, loaded.Review, model)
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
			terminal := check("after unaccepted Review terminal")
			if err := workflowAdmitReview(*terminal.Review, ""); !hasFailureKind(err, KindMissingEvidence) {
				t.Fatalf("an unaccepted %s report authorized execution exit: %v", disposition, err)
			}
			journey := &candidateJourney{fixture: f, s: f.store, workID: f.workID, repo: repo}
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
			model.DependenciesCurrent = true
			accepted := check("after Main acceptance")
			for _, group := range []string{"", "group:next"} {
				want := group == "" && disposition == "ready_for_final_proof" || group != "" && disposition == "continue"
				if err := workflowAdmitReview(*accepted.Review, group); (err == nil) != want {
					t.Fatalf("accepted %s, group %q: admitted=%v, want %v: %v", disposition, group, err == nil, want, err)
				}
			}
		})
	}
}

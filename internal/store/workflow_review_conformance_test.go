package store

import (
	"reflect"
	"testing"
)

func TestCurrentReviewAdmissionLoaderConformsToAcceptedDisposition(t *testing.T) {
	for _, disposition := range []string{"continue", "correction_required", "revision_required", "ready_for_final_proof"} {
		t.Run(disposition, func(t *testing.T) {
			journey := pReturnReviewFixture(t, "review-conformance-"+disposition, WorkflowStepPlanFix, "")
			f, repo := journey.fixture, journey.repo
			head := journey.head(t)
			candidate := ReviewCandidate{ProjectID: "project", Commit: head, Tree: reviewObservedTree(t, repo, head)}
			dependencies, err := workflowReviewDependencies(t.Context(), f.store.db, f.workID)
			if err != nil {
				t.Fatal(err)
			}
			model := WorkflowReviewState{
				Dependencies: &dependencies,
				Findings:     []ReviewFinding{}, PublicationOutstanding: true,
			}
			check := func(label string) WorkflowReviewState {
				t.Helper()
				loaded, err := loadWorkflowReviewState(t.Context(), f.store.db, f.workID)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(loaded, model) {
					t.Fatalf("%s: loaded Review %+v differs from declared model %+v", label, loaded, model)
				}
				if step := currentStep(t, f.store, f.workID); step != "implement" {
					t.Fatalf("%s: Review recording advanced to %s", label, step)
				}
				return loaded
			}
			before := check("after ordinary implementation")
			if err := workflowAdmitReview(before); !hasFailureKind(err, KindMissingEvidence) {
				t.Fatalf("ordinary implementation authorized exit without Review: %v", err)
			}
			report := currentReviewBlockForTest(t, f.store, f.workID, disposition)
			draft := check("after constructing unrecorded Review")
			if err := workflowAdmitReview(draft); !hasFailureKind(err, KindMissingEvidence) {
				t.Fatalf("unrecorded %s report authorized execution exit: %v", disposition, err)
			}
			actor, err := WorkflowActorRef(f.owner)
			if err != nil {
				t.Fatal(err)
			}
			ref, _ := completeCurrentReviewForTest(t, f, disposition)
			model.Candidate = &candidate
			model.Accepted = &WorkflowAcceptedReview{ReportRef: ref, ReportDigest: reviewDigest(report), ActorRef: actor, Report: *report}
			model.DependenciesCurrent = true
			accepted := check("after record_review")
			want := disposition == "ready_for_final_proof"
			if err := workflowAdmitReview(accepted); (err == nil) != want {
				t.Fatalf("recorded %s: admitted=%v, want %v: %v", disposition, err == nil, want, err)
			}
		})
	}
}

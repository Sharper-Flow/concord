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
			repo, _ := reviewSubjectClaim(t, f.store, f.workID)
			journey := &candidateJourney{fixture: f, s: f.store, workID: f.workID, repo: repo}
			conformanceImplementChange(t, journey, "ordinary-implementation")
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
			definition := pinnedWorkflowDefinitionForTest(t, f.store, f.workID)
			global := admissionModelState{step: "repair", contracts: 1}
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
					t.Fatalf("%s: Review recording advanced to %s", label, loaded.Step)
				}
				return loaded
			}
			before := check("after ordinary implementation")
			if err := workflowAdmitReview(*before.Review); !hasFailureKind(err, KindMissingEvidence) {
				t.Fatalf("ordinary implementation authorized exit without Review: %v", err)
			}
			report := currentReviewBlockForTest(t, f.store, f.workID, disposition)
			draft := check("after constructing unrecorded Review")
			if err := workflowAdmitReview(*draft.Review); !hasFailureKind(err, KindMissingEvidence) {
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
			global.review = admissionModelReview{candidate: true, current: true, accepted: disposition}
			accepted := check("after record_review")
			want := disposition == "ready_for_final_proof"
			if err := workflowAdmitReview(*accepted.Review); (err == nil) != want {
				t.Fatalf("recorded %s: admitted=%v, want %v: %v", disposition, err == nil, want, err)
			}
			lifted := admissionWorkflowState(definition, global)
			if err := workflowAdmitReview(*lifted.Review); (err == nil) != want {
				t.Fatalf("global model recorded %s: admitted=%v, want %v: %v", disposition, err == nil, want, err)
			}
		})
	}
}

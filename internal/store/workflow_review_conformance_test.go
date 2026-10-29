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
	conformanceImplementChange(t, journey, "gate-correction")
	completeCurrentReviewForTest(t, f, repo, "ready_for_final_proof")
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
			report := currentReviewBlockForTest(t, f.store, f.workID, "", disposition)
			draft := check("after constructing unrecorded Review")
			if err := workflowAdmitReview(*draft.Review); !hasFailureKind(err, KindMissingEvidence) {
				t.Fatalf("unrecorded %s report authorized execution exit: %v", disposition, err)
			}
			actor, err := WorkflowActorRef(f.owner)
			if err != nil {
				t.Fatal(err)
			}
			ref, _ := completeCurrentReviewForTest(t, f, repo, disposition)
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

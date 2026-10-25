package store

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestReviewDispatchRequiresExactReviewInput(t *testing.T) {
	for _, field := range []string{"missing", "project", "commit", "tree", "criterion", "architecture", "other lane", "valid"} {
		t.Run(field, func(t *testing.T) {
			f := seedWorkContextFixture(t, "review-input-"+strings.ReplaceAll(field, " ", "-"))
			t.Cleanup(func() { _ = f.store.Close() })
			repo, commit := reviewSubjectClaim(t, f.store, f.workID)
			var branch string
			if err := f.store.db.QueryRow(`SELECT branch FROM worktree_entries WHERE set_id=? AND state='active'`, WorktreeSetID(f.workID)).Scan(&branch); err != nil {
				t.Fatal(err)
			}
			gitRunStore(t, repo, "branch", "-m", branch)
			job := reviewSubjectReadyRevision(t, f)
			lane := "review"
			if field == "other lane" {
				lane = "implement"
			}
			digest := registeredLaneIdentity(t, lane)
			packet := joinPacketFor(t, f.store, f.workID, currentStep(t, f.store, f.workID), "attempt:review-input", lane, digest)
			inputs := packet["inputs"].(map[string]any)
			inputs["worker_job"] = recordedPacketJobForTest(t, f.store, f.workID, job, lane)
			deps, err := workflowReviewDependencies(t.Context(), f.store.db, f.workID)
			if err != nil {
				t.Fatal(err)
			}
			tree, err := readReviewCandidateTree(t.Context(), ExecGitRunner{}, repo, commit)
			if err != nil {
				t.Fatal(err)
			}
			descriptor := WorkerReviewInput{InputCandidate: WorkerReviewCandidate{ProjectID: "project", Commit: commit, Tree: tree}, Dependencies: deps}
			switch field {
			case "project":
				descriptor.InputCandidate.ProjectID = "another-project"
			case "commit":
				descriptor.InputCandidate.Commit = strings.Repeat("f", 40)
			case "tree":
				descriptor.InputCandidate.Tree = strings.Repeat("f", 40)
			case "criterion":
				descriptor.Dependencies.Criteria[0].ContentDigest = "sha256:" + strings.Repeat("f", 64)
			case "architecture":
				descriptor.Dependencies.ArchitectureDigest = "sha256:" + strings.Repeat("f", 64)
				descriptor.Dependencies.ArchitectureNotApplicable = false
			}
			if field != "missing" {
				inputs["review_input"] = descriptor
			} else {
				delete(inputs, "review_input")
			}
			prepared, err := PrepareWorkerDispatchFreshness(t.Context(), f.store, PrepareWorkerDispatchRequest{WorkID: f.workID, SessionWorktree: repo})
			if err != nil {
				t.Fatal(err)
			}
			_, err = dispatchJoinAttempt(WithWorkerDispatchPreparation(context.Background(), prepared), t, f.store, f.workID, readWorkVersion(t, f.store, f.workID), f.owner, packet)
			if (err == nil) != (field == "valid") {
				t.Fatalf("dispatch %s = %v", field, err)
			}
		})
	}
}

func TestReviewContinuityPreservesRecoveryOnCandidateTreeFailure(t *testing.T) {
	f := seedWorkContextFixture(t, "review-input-tree-failure")
	t.Cleanup(func() { _ = f.store.Close() })
	repo, _ := reviewSubjectClaim(t, f.store, f.workID)
	snapshot, err := ReadWorkflowContinuity(t.Context(), f.store, ContinuityRequest{Work: f.workID})
	if err != nil || snapshot.ReviewInput == nil {
		t.Fatalf("reachable candidate: %+v, %v", snapshot.ReviewInput, err)
	}
	if err := os.Rename(repo+"/.git", repo+"/.git-unavailable"); err != nil {
		t.Fatal(err)
	}
	t.Run("continuity", func(t *testing.T) {
		snapshot, err := ReadWorkflowContinuity(t.Context(), f.store, ContinuityRequest{Work: f.workID})
		if err != nil {
			t.Fatalf("optional review input blocked continuity: %v", err)
		}
		var failure *Failure
		if !failureAs(snapshot.ReviewInputWarning, &failure) || failure.Kind != KindGitUnreachable {
			t.Fatalf("want typed candidate-tree Git warning, got %v", snapshot.ReviewInputWarning)
		}
		if !strings.Contains(failure.Detail, "candidate tree") || failure.Err == nil || !failure.RetrySafe || failure.RecoveryAction == "" {
			t.Fatalf("candidate-tree failure lost its cause or recovery: %+v", failure)
		}
		if snapshot.ReviewInput != nil {
			t.Fatal("unreachable candidate must not expose review input")
		}
	})
}

func TestReviewPacketClaimsRemainBoundAtAcceptance(t *testing.T) {
	for _, field := range []string{"valid", "project", "commit", "tree", "criterion", "architecture"} {
		t.Run(field, func(t *testing.T) {
			f := seedWorkContextFixture(t, "review-input-accept-"+field)
			t.Cleanup(func() { _ = f.store.Close() })
			repo, _ := reviewSubjectClaim(t, f.store, f.workID)
			var branch string
			if err := f.store.db.QueryRow(`SELECT branch FROM worktree_entries WHERE set_id=? AND state='active'`, WorktreeSetID(f.workID)).Scan(&branch); err != nil {
				t.Fatal(err)
			}
			gitRunStore(t, repo, "branch", "-m", branch)
			snapshot, err := ReadWorkflowContinuity(t.Context(), f.store, ContinuityRequest{Work: f.workID})
			if err != nil || snapshot.ReviewInput == nil {
				t.Fatalf("review input: %+v, %v", snapshot.ReviewInput, err)
			}
			input := snapshot.ReviewInput
			attempt, epoch := completeCurrentReviewForTest(t, f, repo, "ready_for_final_proof", func(report *WorkerReviewBlock) {
				report.InputCandidate, report.OutputCandidate, report.Dependencies = input.InputCandidate, input.InputCandidate, input.Dependencies
				switch field {
				case "project":
					report.InputCandidate.ProjectID = "project:other"
				case "commit":
					report.InputCandidate.Commit = strings.Repeat("f", 40)
				case "tree":
					report.InputCandidate.Tree = strings.Repeat("f", 40)
				case "criterion":
					report.Dependencies.Criteria[0].ContentDigest = "sha256:" + strings.Repeat("f", 64)
				case "architecture":
					report.Dependencies.ArchitectureNotApplicable = false
					report.Dependencies.ArchitectureDigest = "sha256:" + strings.Repeat("f", 64)
				}
			})
			j := &candidateJourney{fixture: f, s: f.store, workID: f.workID, repo: repo}
			err = j.action(t, "accept_worker_result", map[string]any{"attempt_id": attempt, "attempt_epoch": epoch}, f.owner)
			if (err == nil) != (field == "valid") {
				t.Fatalf("acceptance %s: %v", field, err)
			}
		})
	}
}

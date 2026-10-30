package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func descendantReviewFixture(t *testing.T, id string) (*candidateJourney, string) {
	t.Helper()
	f := seedWorkContextFixture(t, id)
	t.Cleanup(func() { f.store.Close() })
	dir, head := reviewSubjectClaim(t, f.store, id)
	acceptCurrentReviewForTest(t, f, dir, "continue")
	j := &candidateJourney{fixture: f, s: f.store, workID: id, repo: dir}
	j.observeRun(t, "refine-a")
	return j, head
}

type reviewAncestryOutageRunner struct{ ancestryCalls int }

func (r *reviewAncestryOutageRunner) Run(ctx context.Context, path string, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "--no-replace-objects" {
		r.ancestryCalls++
		return nil, errors.New("synthetic ancestry outage")
	}
	return (ExecGitRunner{}).Run(ctx, path, args...)
}

func TestReviewUnrecordedDescendantRefusesWithoutAncestryReads(t *testing.T) {
	j, _ := descendantReviewFixture(t, "descendant-ancestry-outage")
	before := j.effects(t)
	if err := os.WriteFile(filepath.Join(j.repo, "seed.txt"), []byte("unobserved descendant\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	j.commit(t, "coordinator@example.invalid", "unobserved descendant")
	runner := &reviewAncestryOutageRunner{}
	_, err := PrepareWorkflowAcceptanceFreshness(t.Context(), j.s, PrepareWorkflowSubjectRequest{WorkID: j.workID, Runner: runner})
	if !hasFailureKind(err, KindStaleRequiresReview) {
		t.Fatalf("unobserved descendant = %v, want stale_requires_review", err)
	}
	if runner.ancestryCalls != 0 {
		t.Fatalf("Review inferred authority with %d ancestry reads", runner.ancestryCalls)
	}
	if j.effects(t) != before {
		t.Fatal("refused observation changed durable state")
	}
}

func descendantCommit(t *testing.T, j *candidateJourney, id string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(j.repo, "seed.txt"), []byte(id+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	head := j.commit(t, "coordinator@example.invalid", id)
	if run := j.observeRun(t, id); run.SubjectRef != "commit:"+head {
		t.Fatalf("qualified subject = %q, want %q", run.SubjectRef, head)
	}
	return head
}

func TestReviewDescendantNonDescendantRefuses(t *testing.T) {
	for _, shape := range []string{"sibling", "older"} {
		t.Run(shape, func(t *testing.T) {
			j, root := descendantReviewFixture(t, "descendant-nonancestor-"+shape)
			descendantCommit(t, j, "older-unaccepted")
			old := currentReviewBlockForTest(t, j.s, j.workID, "", "continue").InputCandidate
			descendantCommit(t, j, "anchor-new")
			acceptCurrentReviewForTest(t, j.fixture, j.repo, "continue")
			gitRunStore(t, j.repo, "reset", "--hard", root)
			if shape == "sibling" {
				descendantCommit(t, j, "sibling")
			} else {
				j.observeRun(t, "reset-older")
			}
			report := currentReviewBlockForTest(t, j.s, j.workID, "", "ready_for_final_proof")
			// A corrective report cannot substitute an older input for the
			// retained anchor, even if its output has a qualified receipt.
			report.InputCandidate = old
			before := j.effects(t)
			if err := recordReviewForTest(t, j.s, j.workID, j.fixture.owner, report); !hasFailureKind(err, KindStaleRequiresReview) {
				t.Fatalf("%s corrective input = %v, want stale_requires_review", shape, err)
			}
			if j.effects(t) != before {
				t.Fatal("refused old corrective input changed durable state")
			}
		})
	}
}

func TestReviewDescendantPreparedSubjectDriftRefuses(t *testing.T) {
	j, _ := descendantReviewFixture(t, "descendant-prepared-drift")
	descendantCommit(t, j, "b")
	report := currentReviewBlockForTest(t, j.s, j.workID, "", "continue")
	prepared, err := PrepareWorkflowAcceptanceFreshness(t.Context(), j.s, PrepareWorkflowSubjectRequest{WorkID: j.workID})
	if err != nil {
		t.Fatal(err)
	}
	descendantCommit(t, j, "c")
	before := j.effects(t)
	ctx := WithWorkflowSubjectPreparation(t.Context(), prepared)
	if err := j.terminalActionContext(t, ctx, "record_review", mustJSONValue(map[string]any{"review": report}), j.fixture.owner, nil); !hasFailureKind(err, KindStaleRequiresReview) {
		t.Fatalf("prepared subject drift = %v, want stale_requires_review", err)
	}
	if j.effects(t) != before {
		t.Fatal("prepared-subject refusal changed durable state")
	}
}

func TestReviewDescendantPostObservationMovementKeepsObservedSubject(t *testing.T) {
	j := journeyWithIntegratedB(t, "descendant-unobserved-prepared-drift")
	report := currentReviewBlockForTest(t, j.s, j.workID, "", "ready_for_final_proof")
	prepared, err := PrepareWorkflowAcceptanceFreshness(t.Context(), j.s, PrepareWorkflowSubjectRequest{WorkID: j.workID})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(j.repo, "README.md"), []byte("unobserved c\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := j.commit(t, "coordinator@example.invalid", "unobserved c after preparation")
	if c == j.b || j.pinned(t) != j.b {
		t.Fatal("fixture must move live HEAD without replacing the qualified B pin")
	}
	ctx := WithWorkflowSubjectPreparation(t.Context(), prepared)
	if err := j.terminalActionContext(t, ctx, "record_review", mustJSONValue(map[string]any{"review": report}), j.fixture.owner, nil); err != nil {
		t.Fatalf("record the sealed B observation: %v", err)
	}
	j.requireAcceptedReview(t, j.b)
	before := j.effects(t)
	if err := j.terminalAction(t, "record_verdict", criterionAssociationPayload(j.runA.OperationRef, "predicate:work-context"), j.fixture.owner); !hasFailureKind(err, KindStaleRequiresReview) {
		t.Fatalf("next re-observing gate = %v, want stale_requires_review", err)
	}
	if j.effects(t) != before {
		t.Fatal("refused re-observing gate changed durable state")
	}
	j.requireRecordsUnchanged(t, j.snapshotA)
}

func TestReviewDescendantLaterCorrectiveAnchor(t *testing.T) {
	for _, disposition := range []string{"correction_required", "ready_for_final_proof"} {
		t.Run(disposition, func(t *testing.T) {
			j, _ := descendantReviewFixture(t, "descendant-corrective-"+disposition)
			descendantCommit(t, j, "b")
			c := descendantCommit(t, j, "corrected")
			acceptCurrentReviewForTest(t, j.fixture, j.repo, disposition)
			state, err := loadWorkflowReviewState(t.Context(), j.s.db, j.workID)
			if err != nil || state.Candidate == nil || state.Candidate.Commit != c {
				t.Fatalf("recorded candidate = %+v err=%v, want correction %s", state.Candidate, err, c)
			}
			err = workflowAdmitReview(state)
			if disposition == "correction_required" {
				if !hasFailureKind(err, KindMissingEvidence) {
					t.Fatalf("unfinished correction admitted final proof: %v", err)
				}
			} else if err != nil {
				t.Fatalf("recorded ready correction refused: %v", err)
			}
		})
	}
}

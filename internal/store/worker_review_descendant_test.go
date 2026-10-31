package store

import (
	"os"
	"path/filepath"
	"testing"
)

func descendantReviewFixture(t *testing.T, id string) (*candidateJourney, string) {
	t.Helper()
	f := seedWorkContextFixture(t, id)
	t.Cleanup(func() { f.store.Close() })
	dir, head := reviewSubjectClaim(t, f.store, id)
	acceptCurrentReviewForTest(t, f, "continue")
	j := &candidateJourney{fixture: f, s: f.store, workID: id, repo: dir}
	return j, head
}

func descendantCommit(t *testing.T, j *candidateJourney, id string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(j.repo, "seed.txt"), []byte(id+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	head := j.commit(t, "coordinator@example.invalid", id)
	return head
}

func TestReviewDescendantNonDescendantRefuses(t *testing.T) {
	for _, shape := range []string{"sibling", "older"} {
		t.Run(shape, func(t *testing.T) {
			j, root := descendantReviewFixture(t, "descendant-nonancestor-"+shape)
			descendantCommit(t, j, "older-unaccepted")
			old := currentReviewBlockForTest(t, j.s, j.workID, "continue").InputCandidate
			descendantCommit(t, j, "anchor-new")
			acceptCurrentReviewForTest(t, j.fixture, "continue")
			gitRunStore(t, j.repo, "reset", "--hard", root)
			if shape == "sibling" {
				descendantCommit(t, j, "sibling")
			}
			report := currentReviewBlockForTest(t, j.s, j.workID, "ready_for_final_proof")
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

func TestReviewDescendantLaterCorrectiveAnchor(t *testing.T) {
	for _, disposition := range []string{"correction_required", "ready_for_final_proof"} {
		t.Run(disposition, func(t *testing.T) {
			j, _ := descendantReviewFixture(t, "descendant-corrective-"+disposition)
			descendantCommit(t, j, "b")
			c := descendantCommit(t, j, "corrected")
			acceptCurrentReviewForTest(t, j.fixture, disposition)
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

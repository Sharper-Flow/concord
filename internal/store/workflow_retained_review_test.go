package store

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func retainedReviewFixture(t *testing.T, id string) (*candidateJourney, ReviewCandidate) {
	t.Helper()
	f := seedWorkContextFixture(t, id)
	t.Cleanup(func() { _ = f.store.Close() })
	dir, _ := reviewSubjectClaim(t, f.store, id)
	var branch string
	if err := f.store.db.QueryRow(`SELECT branch FROM worktree_entries WHERE set_id=? AND state='active'`, WorktreeSetID(id)).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, dir, "branch", "-m", branch)
	j := &candidateJourney{fixture: f, s: f.store, workID: id, repo: dir}
	j.observeRun(t, "baseline")
	acceptCurrentReviewForTest(t, f, dir, "continue")
	state, err := readWorkflowReviewFacts(t.Context(), j.s.db, id)
	if err != nil || state.Candidate == nil || state.Accepted == nil {
		t.Fatalf("retained Review baseline: %+v, %v", state, err)
	}
	return j, *state.Candidate
}

func TestRetainedEditingReviewRecovery(t *testing.T) {
	j, input := retainedReviewFixture(t, "retained-positive")
	baseline, err := readWorkflowReviewFacts(t.Context(), j.s.db, j.workID)
	if err != nil {
		t.Fatal(err)
	}
	var beforeDigest string
	if err := j.s.db.QueryRow(`SELECT payload_digest FROM effect_receipts WHERE event_id=?`, baseline.Accepted.ReportRef).Scan(&beforeDigest); err != nil {
		t.Fatal(err)
	}
	head := descendantCommit(t, j, "ordinary-editor")
	current, err := readWorkflowReviewFacts(t.Context(), j.s.db, j.workID)
	if err != nil || !reflect.DeepEqual(current.Candidate, baseline.Candidate) || !reflect.DeepEqual(current.Accepted, baseline.Accepted) {
		t.Fatalf("subject observation promoted unreviewed input: %+v, %v", current, err)
	}
	ref, _ := completeCurrentReviewForTest(t, j.fixture, j.repo, "continue", func(report *ReviewBlock) {
		report.InputCandidate = input
	})
	current, err = readWorkflowReviewFacts(t.Context(), j.s.db, j.workID)
	if err != nil || current.Candidate == nil || current.Candidate.Commit != head || current.Accepted == nil || current.Accepted.ReportRef != ref || current.Accepted.Report.InputCandidate != input {
		t.Fatalf("recorded corrective candidate: %+v, %v", current, err)
	}
	var afterDigest string
	if err := j.s.db.QueryRow(`SELECT payload_digest FROM effect_receipts WHERE event_id=?`, baseline.Accepted.ReportRef).Scan(&afterDigest); err != nil || beforeDigest != afterDigest {
		t.Fatalf("predecessor receipt changed: %q -> %q, %v", beforeDigest, afterDigest, err)
	}
}

func TestRetainedEditingReviewCanRepair(t *testing.T) {
	j, _ := retainedReviewFixture(t, "retained-repair-output")
	inputCommit := descendantCommit(t, j, "input-needing-repair")
	acceptCurrentReviewForTest(t, j.fixture, j.repo, "correction_required")
	input := currentReviewBlockForTest(t, j.s, j.workID, "", "continue").InputCandidate
	output := descendantCommit(t, j, "fresh-review-repair")
	if inputCommit == output {
		t.Fatal("Review did not repair the retained input")
	}
	ref, _ := completeCurrentReviewForTest(t, j.fixture, j.repo, "continue", func(report *ReviewBlock) {
		report.InputCandidate = input
	})
	current, err := readWorkflowReviewFacts(t.Context(), j.s.db, j.workID)
	if err != nil || current.Candidate == nil || current.Candidate.Commit != output || current.Accepted == nil || current.Accepted.ReportRef != ref || current.Accepted.Report.InputCandidate != input {
		t.Fatalf("repair output not recorded: %+v, %v", current, err)
	}
}

func TestRetainedReviewCandidateBounds(t *testing.T) {
	for _, shape := range []string{"dirty", "foreign_input", "input_tree", "output_moved", "contract"} {
		t.Run(shape, func(t *testing.T) {
			j, input := retainedReviewFixture(t, "retained-bound-"+shape)
			descendantCommit(t, j, "ordinary-editor")
			report := currentReviewBlockForTest(t, j.s, j.workID, "", "continue")
			report.InputCandidate = input
			want := KindStaleRequiresReview
			switch shape {
			case "dirty":
				if err := os.WriteFile(filepath.Join(j.repo, "seed.txt"), []byte("dirty\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "foreign_input":
				report.InputCandidate.Commit = strings.Repeat("f", 40)
			case "input_tree":
				report.InputCandidate.Tree = strings.Repeat("e", 40)
			case "output_moved":
				descendantCommit(t, j, "later-observed-editor")
			case "contract":
				payload := pContractSuccessor(t, j.s, j.workID, "The approved outcome needs a corrected approach.")
				predicates := payload["outcome_predicates"].([]any)
				predicates[0].(map[string]any)["outcome_payload"].(map[string]any)["expected_result"] = "fail"
				if err := runVerdictActionAs(t, j.s, j.workID, "supersede_contract", mustJSONValue(payload), 0, j.fixture.owner); err != nil {
					t.Fatal(err)
				}
			}
			before := j.effects(t)
			state, err := readWorkflowReviewFacts(t.Context(), j.s.db, j.workID)
			if err != nil {
				t.Fatal(err)
			}
			err = recordReviewForTest(t, j.s, j.workID, j.fixture.owner, report)
			if !hasFailureKind(err, want) {
				t.Fatalf("%s corrective Review=%v, want %s", shape, err, want)
			}
			after, err := readWorkflowReviewFacts(t.Context(), j.s.db, j.workID)
			if err != nil || j.effects(t) != before || !reflect.DeepEqual(after, state) {
				t.Fatalf("refused Review changed durable state: %+v -> %+v, %v", state, after, err)
			}
		})
	}
}

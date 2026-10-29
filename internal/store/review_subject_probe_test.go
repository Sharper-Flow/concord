package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

type reviewSubjectProbeRunner func(context.Context, string, ...string) ([]byte, error)

func (run reviewSubjectProbeRunner) Run(ctx context.Context, path string, args ...string) ([]byte, error) {
	return run(ctx, path, args...)
}

func TestReviewSubjectPreparationDoesNotReadDeliveryHistory(t *testing.T) {
	j := pReturnReviewFixture(t, "subject-probe-current", "workflow.break_fix", "")
	head := runTestGit(t, j.repo, "rev-parse", "HEAD")
	tree := reviewObservedTree(t, j.repo, head)
	var calls [][]string
	runner := reviewSubjectProbeRunner(func(ctx context.Context, path string, args ...string) ([]byte, error) {
		calls = append(calls, slices.Clone(args))
		if path != j.repo {
			t.Fatalf("probe selected another worktree: %q", path)
		}
		return (ExecGitRunner{}).Run(ctx, path, args...)
	})
	before := outsideRepairDatabaseSnapshot(t, j.s)
	prepared, err := PrepareWorkflowAcceptanceFreshness(t.Context(), j.s, PrepareWorkflowSubjectRequest{WorkID: j.workID, Runner: runner})
	if err != nil || prepared == nil || prepared.candidate == nil || prepared.candidate.Commit != head || prepared.candidate.Tree != tree {
		t.Fatalf("wrong current subject: %+v, %v", prepared, err)
	}
	want := [][]string{{"status", "--porcelain=v1", "-z", "--untracked-files=all"}, {"rev-parse", "--verify", "HEAD^{commit}"}, {"rev-parse", "--verify", head + "^{tree}"}}
	if !slices.EqualFunc(calls, want, func(a, b []string) bool { return slices.Equal(a, b) }) {
		t.Fatalf("current candidate probe read more than clean status, HEAD and tree: %v", calls)
	}
	if outsideRepairDatabaseSnapshot(t, j.s) != before {
		t.Fatal("subject observation changed durable state")
	}
}

func TestReviewSubjectPreparationPreservesFailures(t *testing.T) {
	for _, tc := range []struct {
		name, failAt, head string
		wantKind           FailureKind
		wantCalls          int
	}{
		{"status unavailable", "status", "", KindGitUnreachable, 1},
		{"HEAD unavailable", "HEAD^{commit}", "", KindGitUnreachable, 2},
		{"tree unavailable", "tree", "", KindGitUnreachable, 3},
		{"malformed HEAD", "", "not-a-commit", KindStaleRequiresReview, 2},
		{"multiple HEADs", "", strings.Repeat("a", 40) + "\n" + strings.Repeat("b", 40), KindStaleRequiresReview, 2},
		{"another commit", "", strings.Repeat("b", 40), KindStaleRequiresReview, 2},
		{"expired probe", "deadline", "", KindGitUnreachable, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := pReturnReviewFixture(t, "subject-probe-failure", "workflow.break_fix", "")
			calls := 0
			runner := reviewSubjectProbeRunner(func(ctx context.Context, path string, args ...string) ([]byte, error) {
				calls++
				if path != j.repo {
					t.Fatalf("probe selected another worktree: %q", path)
				}
				if tc.failAt == "deadline" {
					return nil, context.DeadlineExceeded
				}
				if args[0] == tc.failAt || args[len(args)-1] == tc.failAt || tc.failAt == "tree" && strings.HasSuffix(args[len(args)-1], "^{tree}") {
					return nil, errors.New("synthetic Git observation failure")
				}
				if tc.head != "" && args[len(args)-1] == "HEAD^{commit}" {
					return []byte(tc.head), nil
				}
				return (ExecGitRunner{}).Run(ctx, path, args...)
			})
			before := outsideRepairDatabaseSnapshot(t, j.s)
			prepared, err := PrepareWorkflowAcceptanceFreshness(t.Context(), j.s, PrepareWorkflowSubjectRequest{WorkID: j.workID, Runner: runner})
			if prepared != nil || !hasFailureKind(err, tc.wantKind) || calls != tc.wantCalls {
				t.Fatalf("preparation=%+v error=%v calls=%d, want no preparation, %s and %d calls", prepared, err, calls, tc.wantKind, tc.wantCalls)
			}
			if outsideRepairDatabaseSnapshot(t, j.s) != before {
				t.Fatal("failed observation changed durable state")
			}
		})
	}
}

func TestReviewSubjectPreparationRequiresExactCleanSubject(t *testing.T) {
	for _, status := range []string{"", " M pkg/value.go\x00", "?? extra.txt\x00"} {
		t.Run(fmt.Sprintf("status=%q", status), func(t *testing.T) {
			j := pReturnReviewFixture(t, "subject-probe-clean", "workflow.break_fix", "")
			runner := reviewSubjectProbeRunner(func(ctx context.Context, path string, args ...string) ([]byte, error) {
				if args[0] == "status" {
					return []byte(status), nil
				}
				return (ExecGitRunner{}).Run(ctx, path, args...)
			})
			prepared, err := PrepareWorkflowAcceptanceFreshness(t.Context(), j.s, PrepareWorkflowSubjectRequest{WorkID: j.workID, Runner: runner})
			if status == "" {
				if err != nil || prepared == nil || prepared.candidate == nil || prepared.candidate.Commit != runTestGit(t, j.repo, "rev-parse", "HEAD") {
					t.Fatalf("clean current subject refused: %+v, %v", prepared, err)
				}
			} else if prepared != nil || !hasFailureKind(err, KindStaleRequiresReview) {
				t.Fatalf("dirty candidate admitted: %+v, %v", prepared, err)
			}
		})
	}
}

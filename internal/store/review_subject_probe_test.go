package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

type reviewCurrentSubjectRunner struct{ calls [][]string }

func (r *reviewCurrentSubjectRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, slices.Clone(args))
	if len(args) > 0 {
		switch args[0] {
		case "status":
			return nil, nil
		case "rev-parse":
			return []byte(strings.Repeat("a", 40) + "\n"), nil
		}
	}
	return nil, fmt.Errorf("unexpected historical-anchor probe: %v", args)
}

func TestReviewDispatchSubjectProbeDoesNotReadDeliveryHistory(t *testing.T) {
	runner := &reviewCurrentSubjectRunner{}
	observation, err := observeDispatchSubject(context.Background(), runner, "synthetic-claim")
	if err != nil {
		t.Fatal(err)
	}
	if observation.Commit != strings.Repeat("a", 40) || observation.State != "clean" {
		t.Fatalf("wrong current subject: %+v", observation)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("current candidate probe made %d calls, want status and HEAD only: %v", len(runner.calls), runner.calls)
	}
}

type reviewSubjectProbeRunner func(context.Context, string, ...string) ([]byte, error)

func (run reviewSubjectProbeRunner) Run(ctx context.Context, path string, args ...string) ([]byte, error) {
	return run(ctx, path, args...)
}

func TestReviewDispatchSubjectProbePreservesFailures(t *testing.T) {
	for _, tc := range []struct {
		name      string
		failAt    string
		head      string
		expired   bool
		wantKind  FailureKind
		wantCalls int
	}{
		{name: "status unavailable", failAt: "status", wantKind: KindGitUnreachable, wantCalls: 1},
		{name: "HEAD unavailable", failAt: "rev-parse", wantKind: KindGitUnreachable, wantCalls: 2},
		{name: "malformed HEAD", head: "not-a-commit", wantKind: KindGitUnreachable, wantCalls: 2},
		{name: "multiple HEADs", head: strings.Repeat("a", 40) + "\n" + strings.Repeat("b", 40), wantKind: KindGitUnreachable, wantCalls: 2},
		{name: "expired deadline", expired: true, wantKind: KindTimeout, wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.expired {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Unix(0, 0))
				defer cancel()
			}
			calls := 0
			runner := reviewSubjectProbeRunner(func(ctx context.Context, path string, args ...string) ([]byte, error) {
				calls++
				if path != "synthetic-claim" {
					t.Fatalf("probe selected another worktree: %q", path)
				}
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if args[0] == tc.failAt {
					return nil, errors.New("synthetic Git observation failure")
				}
				if args[0] == "rev-parse" {
					return []byte(tc.head), nil
				}
				return nil, nil
			})
			observation, err := observeDispatchSubject(ctx, runner, "synthetic-claim")
			var failure *Failure
			if !errors.As(err, &failure) || failure.Kind != tc.wantKind || observation != nil {
				t.Fatalf("observation=%+v error=%v, want no observation and %s", observation, err, tc.wantKind)
			}
			if calls != tc.wantCalls {
				t.Fatalf("probe made %d calls, want %d", calls, tc.wantCalls)
			}
		})
	}
}

func TestReviewDispatchSubjectProbePreservesExactCleanSubject(t *testing.T) {
	const subject = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, tc := range []struct {
		name, status, want string
	}{
		{name: "clean", want: "clean"},
		{name: "tracked edit", status: " M pkg/value.go\x00", want: "dirty"},
		{name: "untracked file", status: "?? extra.txt\x00", want: "dirty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := reviewSubjectProbeRunner(func(_ context.Context, _ string, args ...string) ([]byte, error) {
				switch args[0] {
				case "status":
					return []byte(tc.status), nil
				case "rev-parse":
					return []byte(subject), nil
				default:
					return nil, fmt.Errorf("unexpected probe: %v", args)
				}
			})
			observation, err := observeDispatchSubject(context.Background(), runner, "synthetic-claim")
			if err != nil || observation == nil || observation.Commit != subject || observation.State != tc.want {
				t.Fatalf("observation=%+v error=%v, want %s %s", observation, err, tc.want, subject)
			}
			prepared := &WorkerDispatchPreparation{proof: workerDispatchProof{observation: observation}}
			if prepared.observationMatches(subject) != (tc.want == "clean") {
				t.Fatal("dispatch freshness did not preserve clean-subject equality")
			}
			if prepared.observationMatches(strings.Repeat("b", 40)) {
				t.Fatal("dispatch freshness admitted another subject")
			}
		})
	}
}

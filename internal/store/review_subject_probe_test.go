package store

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
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

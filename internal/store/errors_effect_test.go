package store

import (
	"errors"
	"fmt"
	"testing"
)

func TestWrapFailurePreservesPossibleEffectsAcrossErrorTrees(t *testing.T) {
	possible := newFailure(KindUnavailable, "earlier_effect", "an earlier effect is uncertain", false, "reconcile_operation")
	possible.EffectPossible = true
	refusal := newFailure(KindInvalidOperation, "refusal", "request refused", false, "inspect the request")
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain", errors.New("plain error"), false},
		{"refusal", refusal, false},
		{"possible", possible, true},
		{"wrapped possible", fmt.Errorf("outer: %w", possible), true},
		{"joined possible", errors.Join(errors.New("cleanup failed"), possible), true},
		{"refusal before possible", errors.Join(refusal, possible), true},
		{"possible before refusal", errors.Join(possible, refusal), true},
		{"nested join", fmt.Errorf("outer: %w", errors.Join(refusal, errors.Join(errors.New("cleanup failed"), possible))), true},
		{"false wrapper before possible", &Failure{Kind: KindUnavailable, Err: possible}, true},
		{"joined refusals", errors.Join(refusal, errors.New("cleanup failed")), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wrapped := wrapFailure(KindUnavailable, "outer", "outer failure", false, "reconcile_operation", tc.err)
			if wrapped.EffectPossible != tc.want {
				t.Fatalf("effect_possible=%t, want %t for %v", wrapped.EffectPossible, tc.want, tc.err)
			}
			if tc.err != nil && !errors.Is(wrapped, tc.err) {
				t.Fatal("wrapper lost the original error")
			}
		})
	}
}

func TestFailureAsFindsJoinedTypedFailure(t *testing.T) {
	cause := newFailure(KindInvalidOperation, "refusal", "request refused", false, "inspect the request")
	var found *Failure
	if !failureAs(errors.Join(errors.New("cleanup failed"), cause), &found) || found != cause {
		t.Fatal("typed failure discovery did not traverse joined errors")
	}
}

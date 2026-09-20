package agent

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// routeVacate is the declared route a clean merged worktree admits: vacate
// the occupying session, then reclaim the worktree. RequiredRefs carries it
// in execution order, so a caller reads operations, not prose.
var routeVacate = []string{"session_vacate", "worktree_reclaim"}

// The main-checkout refusal names the declared route instead of
// contact_operator. worktree_destroy stays off every allowlist, so the
// destroy invocation still refuses; the route tells the agent holding a
// clean merged worktree what the workflow already admits.
func TestMainCheckoutRefusalNamesVacateRoute(t *testing.T) {
	t.Parallel()
	service := trunkFirewallFixture(t, true)
	_, err := service.Authorize(context.Background(), Invocation{ClientRef: "client-1", PrincipalRef: "human-1", SessionRef: "session-1", AgentRef: "agent-1", Directory: "/repo", Worktree: "/repo-wt", ManifestDigest: ManifestDigest, RequiredCapability: "work_transition", RequiredOperation: "worktree_destroy", ProductID: "product-1", ProjectID: "project-1"})
	if err == nil {
		t.Fatal("worktree_destroy is not a main-checkout operation; the invocation must refuse")
	}
	var refusal *runtimeFailure
	if !errors.As(err, &refusal) {
		t.Fatalf("err=%v, want a typed runtime refusal", err)
	}
	if refusal.recovery != "use_declared_route" {
		t.Fatalf("recovery=%q, want use_declared_route", refusal.recovery)
	}
	if !slices.Equal(refusal.recoveryRefs, routeVacate) {
		t.Fatalf("refs=%v, want %v in execution order", refusal.recoveryRefs, routeVacate)
	}

	out := failureEnvelope(NewBase("route-1", "concord_work_transition", "worktree_destroy"), err)
	if out.Error == nil || out.Error.Kind != "unauthorized" {
		t.Fatalf("envelope error=%+v, want kind unauthorized", out.Error)
	}
	if out.Error.RecoveryAction.Kind != "use_declared_route" {
		t.Fatalf("recovery action=%q, want use_declared_route", out.Error.RecoveryAction.Kind)
	}
	if !slices.Equal(out.Error.RecoveryAction.RequiredRefs, routeVacate) {
		t.Fatalf("required refs=%v, want %v", out.Error.RecoveryAction.RequiredRefs, routeVacate)
	}
	if _, encErr := out.Encode(); encErr != nil {
		t.Fatalf("the route refusal cannot be delivered: %v", encErr)
	}
}

// The store's occupancy refusal rides the same declared route through the
// envelope. A route proposal without its refs falls back to the kind's
// standing default, and a prose proposal keeps resolving to
// contact_operator, because unauthorized couples to no single action.
func TestOccupancyRouteSurvivesTheEnvelope(t *testing.T) {
	t.Parallel()
	base := NewBase("route-2", "concord_work_transition", "worktree_reclaim")
	build := func(proposed string, refs []string) *store.Failure {
		return &store.Failure{
			Kind:           store.KindWorktreeOwnershipConflict,
			Op:             "worktree_reclaim",
			Detail:         "session ses_live occupies worktree /repo-wt; removing it would strand that session",
			RecoveryAction: proposed,
			RecoveryRefs:   refs,
		}
	}
	t.Run("declared route carries refs", func(t *testing.T) {
		out := failureEnvelope(base, build(store.RecoveryUseDeclaredRoute, routeVacate))
		if out.Error == nil || out.Error.Kind != "unauthorized" {
			t.Fatalf("envelope error=%+v, want kind unauthorized", out.Error)
		}
		if out.Error.RecoveryAction.Kind != "use_declared_route" || !slices.Equal(out.Error.RecoveryAction.RequiredRefs, routeVacate) {
			t.Fatalf("recovery action=%+v, want use_declared_route with %v", out.Error.RecoveryAction, routeVacate)
		}
		if _, err := out.Encode(); err != nil {
			t.Fatalf("the occupancy route refusal cannot be delivered: %v", err)
		}
	})
	t.Run("refs-less route falls back to the standing default", func(t *testing.T) {
		out := failureEnvelope(base, build(store.RecoveryUseDeclaredRoute, nil))
		if out.Error == nil || out.Error.RecoveryAction.Kind != "contact_operator" {
			t.Fatalf("recovery action=%+v, want the contact_operator default", out.Error)
		}
		if _, err := out.Encode(); err != nil {
			t.Fatalf("the fallback refusal cannot be delivered: %v", err)
		}
	})
	t.Run("prose proposal keeps contact_operator", func(t *testing.T) {
		out := failureEnvelope(base, build("vacate the session, or use the operator-approved stale-occupancy release route", nil))
		if out.Error == nil || out.Error.RecoveryAction.Kind != "contact_operator" || len(out.Error.RecoveryAction.RequiredRefs) != 0 {
			t.Fatalf("recovery action=%+v, want bare contact_operator", out.Error)
		}
	})
}

// A declared route without refs is the names-no-operation defect in typed
// form, so the envelope validator refuses the pair wherever it appears.
func TestUseDeclaredRouteRequiresRefs(t *testing.T) {
	t.Parallel()
	err := validateError(TypedError{Kind: "unauthorized", RetrySafe: false, RecoveryAction: RecoveryAction{Kind: "use_declared_route"}, EffectState: EffectNone})
	if err == nil {
		t.Fatal("a declared route without required refs must refuse validation")
	}
	err = validateError(TypedError{Kind: "unauthorized", RetrySafe: false, RecoveryAction: RecoveryAction{Kind: "use_declared_route", RequiredRefs: routeVacate}, EffectState: EffectNone})
	if err != nil {
		t.Fatalf("a declared route with required refs must validate: %v", err)
	}
	if routeVacate[0] != "session_vacate" || routeVacate[1] != "worktree_reclaim" {
		t.Fatalf("route=%v, want session_vacate then worktree_reclaim", routeVacate)
	}
}

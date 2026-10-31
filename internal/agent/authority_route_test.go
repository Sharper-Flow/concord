package agent

import (
	"slices"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestDeclaredRouteSurvivesTheEnvelope(t *testing.T) {
	t.Parallel()
	base := agentcontract.NewBase("route", "concord_work_transition", "workflow_action")
	refs := []string{"continuity", "workflow_action"}
	build := func(proposed string, refs []string) *store.Failure {
		return &store.Failure{Kind: store.KindUnauthorized, Op: "workflow_action", Detail: "the caller cannot perform this action", RecoveryAction: proposed, RecoveryRefs: refs}
	}
	t.Run("declared route carries refs", func(t *testing.T) {
		out := failureEnvelope(base, build(store.RecoveryUseDeclaredRoute, refs))
		if out.Error == nil || out.Error.Kind != "unauthorized" {
			t.Fatalf("envelope error=%+v", out.Error)
		}
		if out.Error.RecoveryAction.Kind != "use_declared_route" || !slices.Equal(out.Error.RecoveryAction.RequiredRefs, refs) {
			t.Fatalf("recovery action=%+v", out.Error.RecoveryAction)
		}
		if _, err := out.Encode(); err != nil {
			t.Fatalf("route refusal cannot be delivered: %v", err)
		}
	})
	t.Run("refs-less route falls back to the standing default", func(t *testing.T) {
		out := failureEnvelope(base, build(store.RecoveryUseDeclaredRoute, nil))
		if out.Error == nil || out.Error.RecoveryAction.Kind != "contact_operator" {
			t.Fatalf("recovery action=%+v", out.Error)
		}
		if _, err := out.Encode(); err != nil {
			t.Fatalf("fallback refusal cannot be delivered: %v", err)
		}
	})
	t.Run("prose proposal keeps contact_operator", func(t *testing.T) {
		out := failureEnvelope(base, build("ask the operator to authorize this action", nil))
		if out.Error == nil || out.Error.RecoveryAction.Kind != "contact_operator" || len(out.Error.RecoveryAction.RequiredRefs) != 0 {
			t.Fatalf("recovery action=%+v", out.Error)
		}
	})
}

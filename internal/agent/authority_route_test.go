package agent

import (
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestRetiredRouteDoesNotAlterAuthorityDenial(t *testing.T) {
	t.Parallel()
	base := agentcontract.NewBase("route", "concord_work_transition", "workflow_action")
	build := func(proposed string) *store.Failure {
		return &store.Failure{Kind: store.KindUnauthorized, Op: "workflow_action", Detail: "the caller cannot perform this action", RecoveryAction: proposed}
	}
	t.Run("retired route falls back without refs", func(t *testing.T) {
		out := failureEnvelope(base, build("use_declared_route"))
		if out.Error == nil || out.Error.Kind != "unauthorized" {
			t.Fatalf("envelope error=%+v", out.Error)
		}
		if out.Error.RecoveryAction.Kind != "contact_operator" || len(out.Error.RecoveryAction.RequiredRefs) != 0 {
			t.Fatalf("recovery action=%+v", out.Error.RecoveryAction)
		}
		if _, err := out.Encode(); err != nil {
			t.Fatalf("route refusal cannot be delivered: %v", err)
		}
	})
	t.Run("prose proposal keeps contact_operator", func(t *testing.T) {
		out := failureEnvelope(base, build("ask the operator to authorize this action"))
		if out.Error == nil || out.Error.RecoveryAction.Kind != "contact_operator" || len(out.Error.RecoveryAction.RequiredRefs) != 0 {
			t.Fatalf("recovery action=%+v", out.Error)
		}
	})
}

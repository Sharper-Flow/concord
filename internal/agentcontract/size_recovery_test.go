package agentcontract

import (
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

func TestSizeRecoveryAllowsProducerRepairWithoutLimit(t *testing.T) {
	for _, action := range []string{"reduce_limit", "contact_operator"} {
		t.Run(action, func(t *testing.T) {
			if got := PublicRecovery("limit_exceeded", action); got != action {
				t.Fatalf("size recovery=%s, want the executable action %s", got, action)
			}
			response := NewCoreError(NewBase("size-recovery", "concord_work_transition", "workflow_action"), TypedError{
				Kind: "limit_exceeded", Message: "the fixed mutation frame needs producer repair",
				RecoveryAction: RecoveryAction{Kind: action}, EffectState: EffectNone,
			})
			raw, err := response.Encode()
			if err != nil {
				t.Fatal(err)
			}
			if err := payloadschema.ValidateEnvelope(raw); err != nil {
				t.Fatal(err)
			}
		})
	}
	if err := validateError(TypedError{Kind: "limit_exceeded", RecoveryAction: RecoveryAction{Kind: "restart_query"}, EffectState: EffectNone}); err == nil {
		t.Fatal("size refusal admitted a restart that cannot repair its frame")
	}
}

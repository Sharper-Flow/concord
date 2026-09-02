package agent

import (
	"strings"
	"testing"
)

// companionFields supplies the payload a coupled kind must carry for reasons
// other than its recovery action, so validateError judges the coupling alone.
func companionFields(err *TypedError) {
	switch err.Kind {
	case "ambiguous_scope":
		err.Candidates = []string{"prod-alpha", "prod-beta"}
	case "version_conflict":
		err.CurrentVersions = []ChangedRef{{EntityKind: "work_item", ID: "work-1", Version: "2"}}
	case "budget_refused":
		err.SupportedBudgetSeconds = 30
	}
}

// TestEveryCoupledKindValidatesWithItsCoupledAction proves the producer and the
// validator now agree: what publicRecovery emits for a coupled kind is what
// validateError accepts. Before the fix these two disagreed, and the
// disagreement surfaced only at MarshalJSON time as a transport fault.
func TestEveryCoupledKindValidatesWithItsCoupledAction(t *testing.T) {
	for kind, want := range enforcedRecoveryCouplings {
		err := TypedError{Kind: kind, RecoveryAction: RecoveryAction{Kind: publicRecovery(kind, "operator prose")}, EffectState: EffectNone}
		companionFields(&err)
		if err.RecoveryAction.Kind != want {
			t.Fatalf("publicRecovery(%q) = %q, want %q", kind, err.RecoveryAction.Kind, want)
		}
		if x := validateError(err); x != nil {
			t.Errorf("validateError(%q with its coupled action) = %v, want nil", kind, x)
		}
	}
}

// TestValidateErrorStillRefusesABrokenCoupling keeps the guard honest. The fix
// removes the disagreement; it must not remove the detection. A caller that
// hand-builds a mismatched pair is still refused.
func TestValidateErrorStillRefusesABrokenCoupling(t *testing.T) {
	for kind, want := range enforcedRecoveryCouplings {
		wrong := "reread_entities"
		if want == wrong {
			wrong = "contact_operator"
		}
		err := TypedError{Kind: kind, RecoveryAction: RecoveryAction{Kind: wrong}, EffectState: EffectNone}
		companionFields(&err)
		x := validateError(err)
		if x == nil {
			t.Errorf("validateError(%q paired with %q) = nil, want a coupling refusal", kind, wrong)
			continue
		}
		if !strings.Contains(x.Error(), "recovery coupling violated") {
			t.Errorf("validateError(%q paired with %q) = %v, want a coupling refusal", kind, wrong, x)
		}
	}
}

// TestPublicRecoveryHonorsEnforcedCouplings is the reproduction for issue #715.
//
// validateTypedError refuses an envelope whose error kind is coupled to a
// recovery action but does not carry it. publicRecovery chooses that action
// from the store's proposed value, and the store proposes free operator prose
// as newFailure's `recovery` argument. Prose never matches publicRecovery's
// allow-list, so control reaches its switch, which carries no case for the
// coupled kinds, and the default returns "contact_operator".
//
// The producer and the validator therefore disagree, and the disagreement is
// observable only at MarshalJSON time: the caller receives "operation recovery
// coupling violated" as a transport failure in place of the typed refusal the
// core decided, and with no operation_id to reconcile against.
func TestPublicRecoveryHonorsEnforcedCouplings(t *testing.T) {
	for kind, want := range enforcedRecoveryCouplings {
		// The store proposes operator prose, which is the normal case for
		// every newFailure call site.
		if got := publicRecovery(kind, "use the existing pinned workflow"); got != want {
			t.Errorf("publicRecovery(%q, prose) = %q, want %q", kind, got, want)
		}
		// A proposed value that is a valid action but not the coupled one must
		// not pass through. The coupling is the contract, not a fallback.
		if got := publicRecovery(kind, "reread_entities"); got != want {
			t.Errorf("publicRecovery(%q, %q) = %q, want %q", kind, "reread_entities", got, want)
		}
	}
}

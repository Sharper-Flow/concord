package store

import (
	"context"
	"testing"
)

// The fold admits bind_evidence past its declared binding step when a required
// kind is still unbound. The pin must say so. A caller that reaches a human
// checkpoint with an outstanding kind, and reads only declared step actions,
// sees confirm_premise refuse and no route that produces the missing evidence.
func TestWorkPinAdvertisesEvidenceBindingRecoveryWhenAKindIsOutstanding(t *testing.T) {
	ctx := context.Background()
	f := newAcceptanceRecoveryFixture(ctx, t, wf04OutstandingKind)

	pin, err := ReadWorkPin(ctx, f.store, f.workID)
	if err != nil {
		t.Fatalf("read work pin: %v", err)
	}
	if !workPinContainsAction(pin.NextValidIntents, "bind_evidence") {
		t.Fatalf("pin at step %q omits bind_evidence while %s is outstanding; intents = %v", pin.Step, wf04OutstandingKind, intentActionIDs(pin.NextValidIntents))
	}
	for _, intent := range pin.NextValidIntents {
		if intent.ActionID != "bind_evidence" {
			continue
		}
		if intent.ReasonCode != "evidence_binding_recovery" {
			t.Fatalf("bind_evidence reason_code = %q, want evidence_binding_recovery", intent.ReasonCode)
		}
		if intent.ExpectedVersion != pin.Version {
			t.Fatalf("bind_evidence expected_version = %d, want %d", intent.ExpectedVersion, pin.Version)
		}
	}
}

// The pin never advertises an action the fold would refuse. With every required
// kind already bound, the recovery route is closed and must not appear.
func TestWorkPinOmitsEvidenceBindingRecoveryWhenNothingIsOutstanding(t *testing.T) {
	ctx := context.Background()
	f := newAcceptanceRecoveryFixture(ctx, t)

	pin, err := ReadWorkPin(ctx, f.store, f.workID)
	if err != nil {
		t.Fatalf("read work pin: %v", err)
	}
	if workPinContainsAction(pin.NextValidIntents, "bind_evidence") {
		t.Fatalf("pin at step %q advertises bind_evidence with no outstanding requirement; intents = %v", pin.Step, intentActionIDs(pin.NextValidIntents))
	}
}

func intentActionIDs(intents []WorkPinIntent) []string {
	ids := make([]string, 0, len(intents))
	for _, intent := range intents {
		ids = append(ids, intent.ActionID)
	}
	return ids
}

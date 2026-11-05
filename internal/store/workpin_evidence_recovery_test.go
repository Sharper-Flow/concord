package store

import (
	"context"
	"testing"
)

func TestWorkPinAdvertisesOrdinaryEvidenceBinding(t *testing.T) {
	ctx := context.Background()
	f := newAcceptanceRecoveryFixture(ctx, t)

	pin, err := ReadWorkPin(ctx, f.store, f.workID)
	if err != nil {
		t.Fatalf("read work pin: %v", err)
	}
	if !workPinContainsAction(pin.NextValidIntents, "bind_evidence") {
		t.Fatalf("pin at step %q omits bind_evidence; intents = %v", pin.Step, intentActionIDs(pin.NextValidIntents))
	}
	for _, intent := range pin.NextValidIntents {
		if intent.ActionID != "bind_evidence" {
			continue
		}
		if intent.ReasonCode != "declared_step_action" {
			t.Fatalf("bind_evidence reason_code = %q, want declared_step_action", intent.ReasonCode)
		}
		if intent.ExpectedVersion != pin.Version {
			t.Fatalf("bind_evidence expected_version = %d, want %d", intent.ExpectedVersion, pin.Version)
		}
	}
}

func TestWorkPinAdvertisesEvidenceBindingWhenNothingIsOutstanding(t *testing.T) {
	ctx := context.Background()
	f := newAcceptanceRecoveryFixture(ctx, t)

	pin, err := ReadWorkPin(ctx, f.store, f.workID)
	if err != nil {
		t.Fatalf("read work pin: %v", err)
	}
	if !workPinContainsAction(pin.NextValidIntents, "bind_evidence") {
		t.Fatalf("pin at step %q omits bind_evidence with no outstanding requirement; intents = %v", pin.Step, intentActionIDs(pin.NextValidIntents))
	}
}

func intentActionIDs(intents []WorkPinIntent) []string {
	ids := make([]string, 0, len(intents))
	for _, intent := range intents {
		ids = append(ids, intent.ActionID)
	}
	return ids
}

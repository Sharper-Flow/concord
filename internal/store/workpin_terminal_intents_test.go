package store

import (
	"context"
	"testing"
)

// A work pin states what the caller may do next. workPinIntents already holds
// that rule for a held dispatch (CD-0133 D4): an action the fold refuses is not
// an intent. Terminality is the same rule from the other side.
//
// A terminal lifecycle closes the workflow instance in the same fold that ends
// the item, and the workflow action preflight then refuses every workflow action
// against that instance. The pin built its intents from the current step alone,
// so it kept advertising the step's actions after the item ended. A caller that
// trusted the pin was sent at a route the core had already closed.

// TestTerminalWorkOffersNoWorkflowIntent is the reproduction. It reads the pin
// before and after the item ends, and holds the pin to the same answer the
// fold gives.
func TestTerminalWorkOffersNoWorkflowIntent(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	_, version := continuityTestWorkflow(t, s, "workpin-terminal")

	live, err := ReadWorkPin(ctx, s, "workpin-terminal")
	if err != nil {
		t.Fatal(err)
	}
	if len(live.NextValidIntents) == 0 {
		t.Fatal("the live pin offers no intent, so this test cannot show that terminality removes them")
	}

	if err := applyWorkEvent(t, s, workTransitionEvent("end-it", "workpin-terminal", "needed", "completed", version, version+1), workVersion("workpin-terminal", version)); err != nil {
		t.Fatalf("end the work item: %v", err)
	}

	terminal, err := ReadWorkPin(ctx, s, "workpin-terminal")
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Lifecycle != "completed" {
		t.Fatalf("lifecycle=%q, want completed", terminal.Lifecycle)
	}

	// The fold is the authority the pin must agree with. Ask it directly
	// rather than restating its rule here.
	refusal := InspectWorkflowActionAdmission(ctx, s, WorkflowActionPreflightRequest{WorkID: "workpin-terminal", ActionID: live.NextValidIntents[0].ActionID})
	if refusal == nil {
		t.Fatalf("the fold still admits %q on terminal work, so the pin is not the defect", live.NextValidIntents[0].ActionID)
	}

	if len(terminal.NextValidIntents) != 0 {
		offered := make([]string, 0, len(terminal.NextValidIntents))
		for _, intent := range terminal.NextValidIntents {
			offered = append(offered, intent.ActionID)
		}
		t.Fatalf("terminal work offers %v, but the fold refuses each one: %v", offered, refusal)
	}
}

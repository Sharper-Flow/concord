package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestReadWorkPinReportsEveryCoordinatorSessionDrivingTheItem pins the gap a
// live incident exposed: two coordinator sessions drove one work item, each
// recorded workflow state, and neither pin ever named the other. Concord
// admits the second coordinator by design, because CD-0061 D4 keeps the
// orchestrator identity event evidence rather than authorization. Admission
// is not the defect. Silence is: a coordinator reads a pin on every response
// and cannot learn that another session is driving the same item.
func TestReadWorkPinReportsEveryCoordinatorSessionDrivingTheItem(t *testing.T) {
	s := openTemp(t)
	first, version := continuityTestWorkflow(t, s, "workpin-codriver")

	second := WorkflowActor{
		PrincipalRef: first.PrincipalRef,
		ClientRef:    first.ClientRef,
		AgentRef:     first.AgentRef,
		SessionRef:   "session:second-coordinator",
		ActorClass:   ActorAgent,
	}
	if _, err := continuityAction(t, s, "workpin-codriver", version, "record_proposal", "codriver-proposal-1", nil, second); err != nil {
		t.Fatalf("second coordinator was refused: %v, want admission per CD-0061 D4", err)
	}

	pin, err := ReadWorkPin(context.Background(), s, "workpin-codriver")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(pin)
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range []string{first.SessionRef, second.SessionRef} {
		if !strings.Contains(string(encoded), session) {
			t.Fatalf("pin does not name coordinator session %q; a coordinator cannot see it shares the item\npin=%s", session, encoded)
		}
	}
}

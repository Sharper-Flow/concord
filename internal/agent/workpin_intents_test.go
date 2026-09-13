package agent

import (
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestNextIntentsFromPinPreservesRecoveryReason(t *testing.T) {
	intents := nextIntentsFromPin(store.WorkPin{NextValidIntents: []store.WorkPinIntent{
		{Tool: "concord_work_transition", Operation: "workflow_action", ActionID: "bind_evidence", ReasonCode: "evidence_binding_recovery", ExpectedVersion: 7},
	}})

	if len(intents) != 1 || intents[0].ReasonCode != "evidence_binding_recovery" || intents[0].ExpectedVersion != 7 {
		t.Fatalf("mutation intents=%+v, want recovery reason and version", intents)
	}
}

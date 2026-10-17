package store

import (
	"encoding/json"
	"errors"
	"testing"
)

// CD-0013 criterion 25 covers every callable fenced start, not one action:
// on each live pin that keeps a callable start, another actor's start over a
// held attempt refuses approval_required before it opens an epoch, and the
// holder's own restart stays free.
func TestFencedStartTakeoverRequiresApprovalOnEveryCallableStart(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, ref, step, start string
		version                int64
	}{
		{name: "implementation-execution", ref: "workflow.implementation", version: 19, step: "execution", start: "start_execution"},
		{name: "implementation-refine", ref: "workflow.implementation", version: 19, step: "refine", start: "start_refine"},
		{name: "break-fix-repair", ref: "workflow.break_fix", version: 24, step: "repair", start: "start_repair"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			workID := "fenced-start-takeover-" + tc.name
			fixture := seedHistoricalWorkflowReturnRouteFixture(t, workID, tc.ref, tc.version, tc.step)
			s := fixture.store
			if err := runVerdictActionAs(t, s, workID, tc.start, json.RawMessage(`{}`), 0, fixture.owner); err != nil {
				t.Fatalf("holder %s: %v", tc.start, err)
			}
			held := reviewGateStartedEpoch(t, s, workID, tc.start)
			other := WorkflowActor{PrincipalRef: fixture.owner.PrincipalRef, ClientRef: fixture.owner.ClientRef, AgentRef: "agent/other", SessionRef: "session/" + workID + "-other", ActorClass: ActorAgent}
			err := runVerdictActionAs(t, s, workID, tc.start, json.RawMessage(`{}`), 0, other)
			var failure *Failure
			if !errors.As(err, &failure) || failure.Kind != KindApprovalRequired {
				t.Fatalf("another actor's unapproved %s over a held attempt = %v, want approval_required", tc.start, err)
			}
			if got := reviewGateStartedEpoch(t, s, workID, tc.start); got != held {
				t.Fatalf("refused takeover moved the attempt epoch %d -> %d", held, got)
			}
			if err := runVerdictActionAs(t, s, workID, tc.start, json.RawMessage(`{}`), 0, fixture.owner); err != nil {
				t.Fatalf("holder restart %s: %v", tc.start, err)
			}
			if got := reviewGateStartedEpoch(t, s, workID, tc.start); got != held+1 {
				t.Fatalf("holder restart epoch = %d, want %d", got, held+1)
			}
		})
	}
}

package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Every fenced start checks the boundary's held-attempt snapshot.
func TestFencedStartTakeoverRequiresSnapshotOnEveryCallableStart(t *testing.T) {
	t.Parallel()
	type startCase struct{ name, ref, step, start string }
	var cases []startCase
	for _, ref := range []string{"workflow.implementation", "workflow.break_fix", "workflow.research"} {
		for _, step := range mustBuiltinDefinition(t, ref).Definition.StepGraph.Steps {
			for _, action := range step.Actions {
				if strings.HasPrefix(action, "start_") {
					cases = append(cases, startCase{name: strings.TrimPrefix(ref, "workflow.") + "-" + step.ID, ref: ref, step: step.ID, start: action})
				}
			}
		}
	}
	if len(cases) == 0 {
		t.Fatal("no current definition declares a callable fenced start")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			workID := "fenced-start-takeover-" + tc.name
			fixture := seedWorkflowReturnRouteFixture(t, workID, tc.ref, tc.step)
			s := fixture.store
			if err := runVerdictActionAs(t, s, workID, tc.start, json.RawMessage(`{}`), 0, fixture.owner); err != nil {
				t.Fatalf("holder %s: %v", tc.start, err)
			}
			held := reviewGateStartedEpoch(t, s, workID, tc.start)
			other := WorkflowActor{PrincipalRef: fixture.owner.PrincipalRef, ClientRef: fixture.owner.ClientRef, AgentRef: "agent/other", SessionRef: "session/" + workID + "-other", ActorClass: ActorAgent}
			err := runVerdictActionAs(t, s, workID, tc.start, json.RawMessage(`{}`), 0, other)
			var failure *Failure
			if !errors.As(err, &failure) || failure.Kind != KindOperationConflict {
				t.Fatalf("another actor's %s over a held attempt = %v, want operation_conflict", tc.start, err)
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

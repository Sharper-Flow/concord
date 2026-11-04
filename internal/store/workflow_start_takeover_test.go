package store

import (
	"encoding/json"
	"errors"
	"testing"
)

// A fenced execution start checks the boundary's held-attempt snapshot.
func TestFencedStartTakeoverRequiresSnapshot(t *testing.T) {
	t.Parallel()
	type startCase struct {
		name        string
		planType    WorkflowStepPlanType
		step, start string
	}
	cases := []startCase{{"change", WorkflowStepPlanChange, "implement", "start_execution"}, {"fix", WorkflowStepPlanFix, "implement", "start_execution"}, {"removal", WorkflowStepPlanRemoval, "implement", "start_execution"}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			workID := "fenced-start-takeover-" + tc.name
			fixture := seedWorkflowReturnRouteFixture(t, workID, tc.planType, tc.step)
			s := fixture.store
			startedEpoch := func() int64 {
				var epoch int64
				if err := s.db.QueryRow(`SELECT attempt_epoch FROM workflow_step_epochs WHERE work_id=? AND step_id=?`, workID, tc.step).Scan(&epoch); err != nil {
					t.Fatal(err)
				}
				return epoch
			}
			if err := runVerdictActionAs(t, s, workID, tc.start, json.RawMessage(`{}`), 0, fixture.owner); err != nil {
				t.Fatalf("holder %s: %v", tc.start, err)
			}
			held := startedEpoch()
			other := WorkflowActor{PrincipalRef: fixture.owner.PrincipalRef, ClientRef: fixture.owner.ClientRef, AgentRef: "agent/other", SessionRef: "session/" + workID + "-other", ActorClass: ActorAgent}
			err := runVerdictActionAs(t, s, workID, tc.start, json.RawMessage(`{}`), 0, other)
			var failure *Failure
			if !errors.As(err, &failure) || failure.Kind != KindOperationConflict {
				t.Fatalf("another actor's %s over a held attempt = %v, want operation_conflict", tc.start, err)
			}
			if got := startedEpoch(); got != held {
				t.Fatalf("refused takeover moved the attempt epoch %d -> %d", held, got)
			}
			if err := runVerdictActionAs(t, s, workID, tc.start, json.RawMessage(`{}`), 0, fixture.owner); err != nil {
				t.Fatalf("holder restart %s: %v", tc.start, err)
			}
			if got := startedEpoch(); got != held+1 {
				t.Fatalf("holder restart epoch = %d, want %d", got, held+1)
			}
		})
	}
}

package store

import (
	"context"
	"testing"
	"time"
)

func TestWorkPinIntentsMatchPlanFrontiers(t *testing.T) {
	checked := 0
	for _, planType := range WorkflowStepPlanTypes {
		for _, step := range currentWorkflowPlanFixture(t, planType).Steps {
			t.Run(string(planType)+"/"+step.ID, func(t *testing.T) {
				s := openTemp(t)
				const workID = "pin-admission"
				seedWorkWithUrgency(t, s, workID, "standard", 10)
				actor := WorkflowActor{PrincipalRef: "principal/pin", ClientRef: "client/pin", AgentRef: "agent/pin", SessionRef: "session/pin", ActorClass: ActorAgent}
				if err := s.Transact(context.Background(), func(tx *Transaction) error {
					return InitializeWorkflowTx(context.Background(), tx, WorkflowInitializationRequest{WorkID: workID, Actor: actor, Now: time.Unix(1, 0).UTC()})
				}); err != nil {
					t.Fatal(err)
				}
				seedWorkflowPlanFrontier(t, s, workID, planType, step.ID)
				pin, err := ReadWorkPin(t.Context(), s, workID)
				if err != nil {
					t.Fatal(err)
				}
				plan, err := readWorkflowPlanTx(t.Context(), s.db, workID)
				if err != nil || plan == nil || pin.Step != step.ID || pin.WorkflowType != string(planType) {
					t.Fatalf("pin=%+v plan=%+v err=%v", pin, plan, err)
				}
				for _, intent := range pin.NextValidIntents {
					if intent.ActionID != "insert_step" && intent.ActionID != "pick_type" && !workflowPlanAllowsAction(plan, step.ID, intent.ActionID) {
						t.Errorf("frontier %s advertises undeclared action %s", step.ID, intent.ActionID)
					}
					if intent.Tool != "concord_work_transition" || intent.Operation != "workflow_action" || intent.ExpectedVersion != pin.Version {
						t.Errorf("intent does not name an action at its pin version: %+v", intent)
					}
					checked++
				}
			})
		}
	}
	if checked == 0 {
		t.Fatal("no emitted intent was checked")
	}
}
